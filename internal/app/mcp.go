package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Servidor MCP (Model Context Protocol): expone la búsqueda web del Gemini web como una
// herramienta web_search, para que la usen clientes MCP como Claude Desktop / Claude Code / Cursor.
//
// El transporte usa HTTP (Streamable HTTP), montado en /mcp sobre el --port existente del backend,
// en el mismo proceso y el mismo puerto que la interfaz OpenAI —— una vez desplegado como servidor,
// un cliente remoto se conecta a la URL y listo, reutilizando el pool de cuentas / pool de proxies / limitación de tasa existentes. (No se implementó el transporte stdio, en el que el cliente lanza un subproceso local.)
//
// Se escribe JSON-RPC 2.0 a mano en lugar de importar un SDK de terceros: solo hay una herramienta
// y la superficie del protocolo es mínima (initialize / tools/list / tools/call), así que hacerlo a mano no añade dependencias y encaja con el estilo de binario único.
//
// La búsqueda usa streamGenerate y aprovecha el pool de proxies / limitación de tasa / reintento / anti-bloqueo existentes.
// Se puede buscar de forma anónima, así que no depende del pool de cookies.

const mcpProtocolVersion = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// handleMCPHTTP es el transporte HTTP de MCP (Streamable HTTP), montado en `/mcp` del backend,
// en el mismo proceso y el mismo puerto que la interfaz OpenAI, y reutiliza el pool de cuentas / pool de proxies / limitación de tasa existentes.
//
// El cliente hace POST de un mensaje JSON-RPC y devolvemos una respuesta application/json. El caso
// de herramientas es petición-respuesta pura y no necesita envío proactivo del servidor, así que no se abre flujo SSE: GET responde directamente 405.
// Las notificaciones (sin id) responden 202 con cuerpo vacío según la especificación.
func handleMCPHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, MCP-Protocol-Version")
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodGet:
		// No se ofrece flujo SSE con envío proactivo del servidor; la especificación permite responder así.
		w.Header().Set("Allow", "POST")
		http.Error(w, "this MCP endpoint is POST-only (no server-initiated stream)", http.StatusMethodNotAllowed)
		return
	case http.MethodPost:
		// Se maneja más abajo
	default:
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeMCPHTTPError(w, nil, -32700, "read error")
		return
	}
	var req rpcRequest
	if json.Unmarshal(body, &req) != nil {
		writeMCPHTTPError(w, nil, -32700, "parse error")
		return
	}
	resp := dispatchMCP(&req)
	if resp == nil {
		w.WriteHeader(http.StatusAccepted) // notificación: sin cuerpo de respuesta
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(resp)
	w.Write(b)
}

func writeMCPHTTPError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
	w.Write(b)
}

func dispatchMCP(req *rpcRequest) *rpcResponse {
	// Las que no tienen id son notificaciones (notifications/initialized, etc.) y no devuelven respuesta.
	if len(req.ID) == 0 {
		return nil
	}
	ok := func(result interface{}) *rpcResponse {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
	}
	fail := func(code int, msg string) *rpcResponse {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: code, Message: msg}}
	}

	switch req.Method {
	case "initialize":
		// Hace eco de la versión de protocolo solicitada por el cliente (si no se obtiene, se usa la que soportamos).
		ver := mcpProtocolVersion
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if json.Unmarshal(req.Params, &p) == nil && p.ProtocolVersion != "" {
			ver = p.ProtocolVersion
		}
		return ok(map[string]interface{}{
			"protocolVersion": ver,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": "gemini-search-mcp", "version": Version},
		})

	case "tools/list":
		return ok(map[string]interface{}{"tools": []interface{}{webSearchToolDef()}})

	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments struct {
				Query string `json:"query"`
			} `json:"arguments"`
		}
		if json.Unmarshal(req.Params, &p) != nil {
			return fail(-32602, "invalid params")
		}
		if p.Name != "web_search" {
			return fail(-32602, "unknown tool: "+p.Name)
		}
		query := strings.TrimSpace(p.Arguments.Query)
		if query == "" {
			return ok(toolTextError("query no puede estar vacío"))
		}
		text, err := mcpWebSearch(query)
		if err != nil {
			return ok(toolTextError("búsqueda fallida: " + err.Error()))
		}
		return ok(map[string]interface{}{
			"content": []interface{}{map[string]interface{}{"type": "text", "text": text}},
		})

	case "ping":
		return ok(map[string]interface{}{})

	default:
		return fail(-32601, "method not found: "+req.Method)
	}
}

// webSearchToolDef es la definición de la herramienta web_search (con JSON Schema).
func webSearchToolDef() map[string]interface{} {
	return map[string]interface{}{
		"name": "web_search",
		"description": "Search the web via Google Gemini and return a synthesized answer " +
			"with source links. Use for current events, facts, or anything needing " +
			"up-to-date information from the internet.",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "The search query or question to research.",
				},
			},
			"required": []interface{}{"query"},
		},
	}
}

// mcpWebSearch ejecuta una búsqueda web y devuelve un texto de "respuesta + lista de fuentes".
func mcpWebSearch(query string) (string, error) {
	mc, ok := Models[rtCfg().DefaultModel]
	if !ok {
		mc = Models["gemini-3.6-flash"]
	}
	res, err := streamGenerate(query, query, mc, nil, nil)
	if err != nil {
		return "", err
	}
	text := extractResponseText(res.Raw)
	if text == "" {
		return "", fmt.Errorf("el upstream no devolvió contenido")
	}
	sources := extractGrounding(res.Raw)
	var b strings.Builder
	b.WriteString(text)
	if len(sources) > 0 {
		b.WriteString("\n\n---\nSources:\n")
		for i, s := range sources {
			title := s.Title
			if title == "" {
				title = s.URL
			}
			fmt.Fprintf(&b, "%d. %s — %s\n", i+1, title, s.URL)
		}
	}
	return b.String(), nil
}

func toolTextError(msg string) map[string]interface{} {
	return map[string]interface{}{
		"content": []interface{}{map[string]interface{}{"type": "text", "text": msg}},
		"isError": true,
	}
}
