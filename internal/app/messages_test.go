package app

import (
	"encoding/json"
	"strings"
	"testing"
)

// El bloque no interpretable NUNCA debe perderse: si se descarta en silencio, el usuario ve
// una respuesta vacía (el texto de fuera ya se emitió en streaming). Debe conservarse.
func TestParseToolCallsInvalidKeepsBlock(t *testing.T) {
	in := "antes\n```tool_call\n{name: esto no es JSON}\n```\ndespues"
	clean, calls := parseToolCalls(in)
	if len(calls) != 0 {
		t.Fatalf("un JSON inválido no debería producir llamadas: %+v", calls)
	}
	if !strings.Contains(clean, "tool_call") {
		t.Errorf("el bloque no interpretable se perdió; texto devuelto: %q", clean)
	}
	if !strings.Contains(clean, "antes") || !strings.Contains(clean, "despues") {
		t.Errorf("se perdió el texto que rodea al bloque: %q", clean)
	}
}

func TestParseToolCallsMissingNameKeepsBlock(t *testing.T) {
	in := "```tool_call\n{\"arguments\": {\"a\": 1}}\n```"
	clean, calls := parseToolCalls(in)
	if len(calls) != 0 {
		t.Fatalf("sin nombre no hay llamada: %+v", calls)
	}
	if !strings.Contains(clean, "tool_call") {
		t.Errorf("el bloque sin nombre debería conservarse como texto: %q", clean)
	}
}

func TestParseToolCallsValidObject(t *testing.T) {
	in := "hola\n```tool_call\n{\"name\": \"get_weather\", \"arguments\": {\"city\": \"London\"}}\n```\nfin"
	clean, calls := parseToolCalls(in)
	if len(calls) != 1 {
		t.Fatalf("se esperaba 1 llamada, se obtuvo %d: %+v", len(calls), calls)
	}
	if calls[0].Function.Name != "get_weather" {
		t.Errorf("nombre = %q, se esperaba get_weather", calls[0].Function.Name)
	}
	if !strings.Contains(calls[0].Function.Arguments, "London") {
		t.Errorf("argumentos = %q, se esperaba que llevaran London", calls[0].Function.Arguments)
	}
	if calls[0].ID == "" || calls[0].Type != "function" {
		t.Errorf("la llamada debería llevar id y type=function: %+v", calls[0])
	}
	if strings.Contains(clean, "tool_call") {
		t.Errorf("el bloque válido debería eliminarse del texto: %q", clean)
	}
}

func TestParseToolCallsArgumentsAsString(t *testing.T) {
	in := "```tool_call\n{\"name\": \"run\", \"arguments\": \"{\\\"cmd\\\": \\\"ls\\\"}\"}\n```"
	_, calls := parseToolCalls(in)
	if len(calls) != 1 {
		t.Fatalf("se esperaba 1 llamada: %+v", calls)
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &got); err != nil {
		t.Fatalf("los argumentos no son JSON válido: %q", calls[0].Function.Arguments)
	}
	if got["cmd"] != "ls" {
		t.Errorf("argumentos como cadena no se normalizaron: %q", calls[0].Function.Arguments)
	}
}

func TestParseToolCallsAliases(t *testing.T) {
	in := "```tool_call\n{\"tool\": \"run\", \"parameters\": {\"cmd\": \"ls\"}}\n```"
	_, calls := parseToolCalls(in)
	if len(calls) != 1 {
		t.Fatalf("se esperaba 1 llamada con aliases: %+v", calls)
	}
	if calls[0].Function.Name != "run" || !strings.Contains(calls[0].Function.Arguments, "ls") {
		t.Errorf("aliases no reconocidos: %+v", calls[0])
	}
}

// Prosa alrededor del JSON y comas finales sobrantes (JSON blando que sueltan los modelos
// débiles) deben tolerarse.
func TestParseToolCallsLooseJSON(t *testing.T) {
	in := "```tool_call\nAquí tienes: {\"name\": \"f\", \"arguments\": {\"a\": 1},\n}\n```"
	_, calls := parseToolCalls(in)
	if len(calls) != 1 {
		t.Fatalf("se esperaba 1 llamada tolerando prosa y coma final: %+v", calls)
	}
	if calls[0].Function.Name != "f" {
		t.Errorf("nombre = %q, se esperaba f", calls[0].Function.Name)
	}
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &got); err != nil {
		t.Errorf("los argumentos no son JSON válido: %q", calls[0].Function.Arguments)
	}
}

// Varios fences: uno válido y otro no. El válido se convierte en llamada; el otro se
// conserva como texto.
func TestParseToolCallsMixed(t *testing.T) {
	in := "```tool_call\n{\"name\": \"a\", \"arguments\": {}}\n```\n```tool_call\nroto\n```"
	clean, calls := parseToolCalls(in)
	if len(calls) != 1 || calls[0].Function.Name != "a" {
		t.Fatalf("se esperaba solo la llamada válida: %+v", calls)
	}
	if !strings.Contains(clean, "roto") {
		t.Errorf("el bloque roto debería conservarse: %q", clean)
	}
}

// Con un conjunto de nombres declarados, una llamada a una herramienta no declarada se EMITE
// igualmente (mejor que degradarla a texto en silencio), y queda registrada en el log.
func TestParseToolCallsUnknownNameStillEmitted(t *testing.T) {
	in := "```tool_call\n{\"name\": \"inventada\", \"arguments\": {}}\n```"
	clean, calls := parseToolCalls(in, map[string]bool{"run_command": true})
	if len(calls) != 1 || calls[0].Function.Name != "inventada" {
		t.Fatalf("una herramienta no declarada debe emitirse igualmente: %+v", calls)
	}
	if strings.Contains(clean, "tool_call") {
		t.Errorf("el bloque no debería conservarse como texto: %q", clean)
	}
}

func TestParseToolCallsKnownNameWithSet(t *testing.T) {
	in := "```tool_call\n{\"name\": \"run_command\", \"arguments\": {\"command\": \"ls\"}}\n```"
	_, calls := parseToolCalls(in, map[string]bool{"run_command": true})
	if len(calls) != 1 || calls[0].Function.Name != "run_command" {
		t.Fatalf("una herramienta declarada debería parsear: %+v", calls)
	}
}

func TestToolNameSet(t *testing.T) {
	tools := []map[string]interface{}{
		{"type": "function", "function": map[string]interface{}{"name": "run_command"}},
		{"name": "read_file"},
	}
	set := toolNameSet(tools)
	if !set["run_command"] || !set["read_file"] || set["otra"] {
		t.Errorf("toolNameSet incorrecto: %+v", set)
	}
}

// El resultado de una herramienta (rol "tool") debe etiquetarse con el nombre de la función,
// aunque el cliente no repita "name" y solo mande el tool_call_id.
func TestBuildPromptResolvesToolNameByCallID(t *testing.T) {
	messages := []map[string]interface{}{
		{"role": "user", "content": "lista los archivos"},
		{"role": "assistant", "content": "", "tool_calls": []interface{}{
			map[string]interface{}{
				"id": "call_1", "type": "function",
				"function": map[string]interface{}{"name": "run_command", "arguments": `{"command":"ls"}`},
			},
		}},
		{"role": "tool", "tool_call_id": "call_1", "content": "a.txt\nb.txt"},
	}
	prompt := buildPrompt(messages, nil, nil, false)
	if !strings.Contains(prompt, "run_command") {
		t.Errorf("el nombre de la herramienta no se resolvió por tool_call_id: %q", prompt)
	}
	if !strings.Contains(prompt, "a.txt") {
		t.Errorf("el resultado de la herramienta no aparece en el prompt: %q", prompt)
	}
}

func TestValueToString(t *testing.T) {
	if got := valueToString("hola"); got != "hola" {
		t.Errorf("cadena: %q", got)
	}
	if got := valueToString(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
	if got := valueToString(map[string]interface{}{"exit": 0}); !strings.Contains(got, "exit") {
		t.Errorf("mapa debería serializarse a JSON: %q", got)
	}
}

// Sin parallel_tool_calls se pide una sola llamada; con el flag se permiten varias.
func TestBuildPromptParallelToolCalls(t *testing.T) {
	tools := []map[string]interface{}{{
		"type":     "function",
		"function": map[string]interface{}{"name": "run_command", "parameters": map[string]interface{}{"type": "object"}},
	}}
	msgs := []map[string]interface{}{{"role": "user", "content": "haz dos cosas"}}
	one := buildPrompt(msgs, tools, "auto", false)
	many := buildPrompt(msgs, tools, "auto", true)
	if !strings.Contains(one, "ONE ```tool_call```") {
		t.Errorf("sin parallel debería pedir una sola llamada")
	}
	if !strings.Contains(many, "SEVERAL") {
		t.Errorf("con parallel debería permitir varias llamadas")
	}
}

// Los modelos a nivel de prompt meten saltos de línea CRUDOS dentro del content del write;
// eso no es JSON válido, pero debe recuperarse y reemitirse como JSON válido.
func TestParseToolCallsRawNewlinesInContent(t *testing.T) {
	in := "```tool_call\n{\"name\":\"write\",\"arguments\":{\"filePath\":\"/tmp/a.txt\",\"content\":\"line1\nline2\nline3\"}}\n```"
	_, calls := parseToolCalls(in)
	if len(calls) != 1 || calls[0].Function.Name != "write" {
		t.Fatalf("no se parseó el write con newlines crudos: %+v", calls)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments no es JSON válido: %q (%v)", calls[0].Function.Arguments, err)
	}
	if args["content"] != "line1\nline2\nline3" {
		t.Errorf("content mal recuperado: %q", args["content"])
	}
}

// Comillas internas sin escapar en el valor (habitual en código): deben recuperarse.
func TestParseToolCallsUnescapedQuotes(t *testing.T) {
	in := "```tool_call\n{\"name\":\"edit\",\"arguments\":{\"filePath\":\"a.go\",\"oldString\":\"x\",\"newString\":\"say \"hi\" now\"}}\n```"
	_, calls := parseToolCalls(in)
	if len(calls) != 1 {
		t.Fatalf("no se parseó el edit con comillas sin escapar: %+v", calls)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments no es JSON válido: %q (%v)", calls[0].Function.Arguments, err)
	}
	if args["newString"] != `say "hi" now` {
		t.Errorf("newString mal recuperado: %q", args["newString"])
	}
}

// El caso combinado y peor: newlines crudos Y comillas internas en el mismo valor.
func TestParseToolCallsNewlinesAndQuotes(t *testing.T) {
	in := "```tool_call\n{\"name\":\"write\",\"arguments\":{\"filePath\":\"a.go\",\"content\":\"if x {\n  print(\"a\")\n}\n\"}}\n```"
	_, calls := parseToolCalls(in)
	if len(calls) != 1 {
		t.Fatalf("no se parseó el write mixto: %+v", calls)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments no es JSON válido: %q (%v)", calls[0].Function.Arguments, err)
	}
	if args["content"] != "if x {\n  print(\"a\")\n}\n" {
		t.Errorf("content mal recuperado: %q", args["content"])
	}
}

// Fence truncado (salida cortada a mitad): no se puede parsear y se conserva como texto.
func TestParseToolCallsTruncatedFence(t *testing.T) {
	in := "```tool_call\n{\"name\":\"write\",\"arguments\":{\"filePath\":\"a\",\"content\":\"part"
	clean, calls := parseToolCalls(in)
	if len(calls) != 0 {
		t.Fatalf("un fence truncado no debería producir llamadas: %+v", calls)
	}
	if !strings.Contains(clean, "tool_call") {
		t.Errorf("el fence truncado debería conservarse como texto: %q", clean)
	}
}

// La guía de archivos debe aparecer cuando hay tools.
func TestBuildPromptFileGuidance(t *testing.T) {
	tools := []map[string]interface{}{{
		"type":     "function",
		"function": map[string]interface{}{"name": "write", "parameters": map[string]interface{}{"type": "object"}},
	}}
	msgs := []map[string]interface{}{{"role": "user", "content": "crea un archivo"}}
	p := buildPrompt(msgs, tools, "auto", false)
	if !strings.Contains(p, "read it first") {
		t.Errorf("falta la guía de read-before-write")
	}
	if !strings.Contains(p, `escape line breaks as \n`) {
		t.Errorf("falta la guía de escapado de saltos de línea")
	}
}

// marshalToolDefs compacta por encima del presupuesto y no muta los parámetros originales.
func TestMarshalToolDefsCompaction(t *testing.T) {
	params := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"content": map[string]interface{}{"type": "string", "description": strings.Repeat("y", 500)},
		},
	}
	defs := []map[string]interface{}{{
		"name":        "write",
		"description": strings.Repeat("x", 500),
		"parameters":  params,
	}}
	b := marshalToolDefs(defs, 100)
	if len(b) >= 1000 {
		t.Errorf("no se compactó lo suficiente: %d bytes", len(b))
	}
	// El mapa original no debe haberse mutado (se copia antes de tocar).
	props := params["properties"].(map[string]interface{})
	content := props["content"].(map[string]interface{})
	if _, ok := content["description"]; !ok {
		t.Errorf("los parámetros originales se mutaron")
	}
}

// toolExample debe preferir una herramienta de shell real y, si no, la primera declarada.
func TestToolExample(t *testing.T) {
	n, a := toolExample([]map[string]interface{}{{"name": "read"}, {"name": "bash"}, {"name": "edit"}})
	if n != "bash" || !strings.Contains(a, "ls") {
		t.Errorf("se esperaba bash con ls: %q %q", n, a)
	}
	if n2, _ := toolExample([]map[string]interface{}{{"name": "read"}, {"name": "edit"}}); n2 != "read" {
		t.Errorf("se esperaba la primera herramienta (read): %q", n2)
	}
}

// hasCJK indica si s contiene algún carácter del bloque CJK unificado (chino, japonés,
// coreano). El protocolo de herramientas se escribe siempre en inglés para no mezclar idiomas
// dentro del prompt: el modelo (y el cliente) razonan en el idioma de la conversación, y las
// etiquetas del proxy no deben introducir otro.
func hasCJK(s string) bool {
	for _, r := range s {
		if (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified Ideographs
			(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
			(r >= 0x3040 && r <= 0x30FF) || // Hiragana + Katakana
			(r >= 0xAC00 && r <= 0xD7A3) { // Hangul syllables
			return true
		}
	}
	return false
}

// La señal de éxito/fallo del resultado de herramienta debe ir en inglés: estaba en chino y
// desorientaba al modelo en conversaciones en inglés/español (bucle y reintentos).
func TestFormatToolResultEnglish(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"vacío", "   "},
		{"exit 0 sin salida", "Chunk ID: x\nWall time: 0.1s\nProcess exited with code 0\nOutput:\n"},
		{"exit 0 con salida", "Process exited with code 0\nOutput:\nhola"},
		{"exit no cero", "Process exited with code 1\nOutput:\nerror"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := formatToolResult("run_command", c.raw)
			if hasCJK(got) {
				t.Errorf("el resultado de herramienta contiene CJK: %q", got)
			}
		})
	}
}

// El prompt ensamblado no debe contener CJK en ninguna de sus partes inyectadas (encabezado de
// herramientas, historia, resultado de herramienta ni pie conductual).
func TestBuildPromptHasNoCJK(t *testing.T) {
	tools := []map[string]interface{}{{
		"type":     "function",
		"function": map[string]interface{}{"name": "run_command", "parameters": map[string]interface{}{"type": "object"}},
	}}
	messages := []map[string]interface{}{
		{"role": "system", "content": "eres un agente"},
		{"role": "user", "content": "lista los archivos"},
		{"role": "assistant", "content": "", "tool_calls": []interface{}{
			map[string]interface{}{
				"id": "call_1", "type": "function",
				"function": map[string]interface{}{"name": "run_command", "arguments": `{"command":"ls"}`},
			},
		}},
		{"role": "tool", "tool_call_id": "call_1", "content": "Process exited with code 0\nOutput:\n"},
	}
	p := buildPrompt(messages, tools, "auto", false)
	if hasCJK(p) {
		t.Errorf("el prompt contiene CJK (mezcla de idiomas):\n%s", p)
	}
}

// effectiveToolChoice eleva a "required" el primer turno cuando el flag está activo.
func TestEffectiveToolChoiceFirstTurn(t *testing.T) {
	old := rtCfg()
	rtMu.Lock()
	rtVal.ForceToolFirstTurn = true
	rtMu.Unlock()
	defer func() {
		rtMu.Lock()
		rtVal = old
		rtMu.Unlock()
	}()

	tools := []map[string]interface{}{{"type": "function", "function": map[string]interface{}{"name": "read"}}}
	first := []map[string]interface{}{{"role": "user", "content": "haz algo"}}
	if got := effectiveToolChoice(map[string]interface{}{}, tools, first); got != "required" {
		t.Errorf("primer turno debería elevarse a required, se obtuvo %v", got)
	}
	continued := []map[string]interface{}{{"role": "user", "content": "hola"}, {"role": "tool", "content": "x"}}
	if got := effectiveToolChoice(map[string]interface{}{}, tools, continued); got == "required" {
		t.Error("una continuación no debería forzarse")
	}
	if got := effectiveToolChoice(map[string]interface{}{"tool_choice": "none"}, tools, first); got != "none" {
		t.Errorf("none debe respetarse, se obtuvo %v", got)
	}
	if got := effectiveToolChoice(map[string]interface{}{}, nil, first); got == "required" {
		t.Error("sin tools no debería aplicarse")
	}
}
