package app

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// sseWriter escribe chat.completion.chunk en orden según especificación OpenAI:
// primero delta{role} → varios delta{content} → delta{} + finish_reason → opcional usage → [DONE].
//
// Las cabeceras se envían con retraso: solo WriteHeader(200) cuando se escribe el primer chunk.
// Si falla antes del primer chunk, se puede devolver 502 JSON en lugar de dejar un stream a medias.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	id      string
	created int64
	model   string
	started bool
}

func newSSEWriter(w http.ResponseWriter, id string, created int64, model string) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{w: w, flusher: f, id: id, created: created, model: model}
}

func (s *sseWriter) Started() bool { return s.started }

// start envía la cabecera de respuesta SSE y el primer chunk delta{"role":"assistant"}. Idempotente.
func (s *sseWriter) start() {
	if s.started {
		return
	}
	s.started = true
	s.w.Header().Set("Content-Type", "text/event-stream")
	s.w.Header().Set("Cache-Control", "no-cache")
	s.w.Header().Set("Connection", "keep-alive")
	// Desactiva buffering global de proxies (nginx etc.) para SSE: con esta cabecera, conexiones directas son streaming;
	// si pasa por proxy puede agruparse y perder streaming (caso observado con Dify).
	s.w.Header().Set("X-Accel-Buffering", "no")
	s.w.Header().Set("Access-Control-Allow-Origin", "*")
	s.w.WriteHeader(200)
	s.chunk(map[string]interface{}{"role": "assistant"}, nil, nil)
}

func (s *sseWriter) chunk(delta map[string]interface{}, finish interface{}, usage map[string]int) {
	c := map[string]interface{}{
		"id":      s.id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []map[string]interface{}{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finish,
		}},
	}
	if usage != nil {
		c["usage"] = usage
	}
	b, _ := json.Marshal(c)
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// SendContent envía incremento de contenido; usado desde callback onDelta.
// SendReasoning envía incremento de cadena de pensamiento. El upstream envía primero pensamiento, luego contenido,
// por lo que el cliente ve "proceso de pensamiento" antes que la respuesta, igual que en web.
func (s *sseWriter) SendReasoning(delta string) {
	if delta == "" {
		return
	}
	s.start()
	s.chunk(map[string]interface{}{"reasoning_content": delta}, nil, nil)
}

func (s *sseWriter) SendContent(delta string) {
	if delta == "" {
		return
	}
	s.start()
	s.chunk(map[string]interface{}{"content": delta}, nil, nil)
}

func (s *sseWriter) SendToolCalls(tcs []ToolCall) {
	s.start()
	// En streaming delta, cada tool_call debe incluir index para que el cliente reconstruya fragmentos
	// (requerido por especificación OpenAI). ToolCall no tiene index; aquí se añade secuencialmente.
	out := make([]map[string]interface{}, len(tcs))
	for i, tc := range tcs {
		out[i] = map[string]interface{}{
			"index": i,
			"id":    tc.ID,
			"type":  tc.Type,
			"function": map[string]interface{}{
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			},
		}
	}
	s.chunk(map[string]interface{}{"tool_calls": out}, nil, nil)
}

// SendToolCallDelta emite UNA tool_call como delta. Se usa para emitir cada llamada en el
// momento en que se cierra su bloque en el stream, en vez de acumular todas hasta el final.
func (s *sseWriter) SendToolCallDelta(index int, tc ToolCall) {
	s.start()
	s.chunk(map[string]interface{}{"tool_calls": []map[string]interface{}{{
		"index": index,
		"id":    tc.ID,
		"type":  tc.Type,
		"function": map[string]interface{}{
			"name":      tc.Function.Name,
			"arguments": tc.Function.Arguments,
		},
	}}}, nil, nil)
}

// Finish cierra el stream: delta vacío + finish_reason, opcional chunk usage, luego [DONE].
func (s *sseWriter) Finish(reason string, usage map[string]int) {
	s.start()
	s.chunk(map[string]interface{}{}, reason, nil)
	if usage != nil {
		// Chunk usage de OpenAI: choices debe ser array vacío.
		c := map[string]interface{}{
			"id": s.id, "object": "chat.completion.chunk",
			"created": s.created, "model": s.model,
			"choices": []map[string]interface{}{},
			"usage":   usage,
		}
		b, _ := json.Marshal(c)
		fmt.Fprintf(s.w, "data: %s\n\n", b)
	}
	fmt.Fprintf(s.w, "data: [DONE]\n\n")
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// Fail se usa tras iniciar stream: envía chunk con error y luego cierra.
// En este punto el código HTTP ya es 200; no se puede cambiar.
func (s *sseWriter) Fail(err error) {
	s.start()
	c := map[string]interface{}{
		"id": s.id, "object": "chat.completion.chunk",
		"created": s.created, "model": s.model,
		"choices": []map[string]interface{}{},
		"error":   map[string]string{"message": err.Error(), "type": "upstream_error"},
	}
	b, _ := json.Marshal(c)
	fmt.Fprintf(s.w, "data: %s\n\n", b)
	fmt.Fprintf(s.w, "data: [DONE]\n\n")
	if s.flusher != nil {
		s.flusher.Flush()
	}
}
