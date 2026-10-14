package app

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateToolArgs(t *testing.T) {
	schema := map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"command"},
		"properties": map[string]interface{}{
			"command": map[string]interface{}{"type": "string"},
			"count":   map[string]interface{}{"type": "integer"},
			"mode":    map[string]interface{}{"type": "string", "enum": []interface{}{"a", "b"}},
			"opts": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"flag": map[string]interface{}{"type": "boolean"}},
			},
			"tags": map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"type": "string"},
			},
		},
	}

	ok := []string{
		`{"command": "ls"}`,
		`{"command": "ls", "count": 3, "mode": "a", "opts": {"flag": true}, "tags": ["x", "y"]}`,
	}
	for _, args := range ok {
		if err := validateToolArgs(schema, args); err != nil {
			t.Errorf("debería ser válido %s: %v", args, err)
		}
	}

	bad := []string{
		`{}`,                              // falta required
		`{"command": 5}`,                  // tipo incorrecto
		`{"command": "ls", "count": 1.5}`, // no es integer
		`{"command": "ls", "mode": "z"}`,  // enum
		`{"command": "ls", "opts": {"flag": "sí"}}`, // anidado
		`{"command": "ls", "tags": [1, 2]}`,         // items
		`no es json`,                                // JSON inválido
	}
	for _, args := range bad {
		if err := validateToolArgs(schema, args); err == nil {
			t.Errorf("debería ser inválido: %s", args)
		}
	}

	// Esquema ausente no bloquea.
	if err := validateToolArgs(nil, `{cualquier cosa}`); err != nil {
		t.Errorf("sin esquema no debería validar: %v", err)
	}
}

func TestToolSchemasAndValidateCalls(t *testing.T) {
	tools := []map[string]interface{}{
		{"type": "function", "function": map[string]interface{}{
			"name": "run_command",
			"parameters": map[string]interface{}{
				"type": "object", "required": []interface{}{"command"},
				"properties": map[string]interface{}{"command": map[string]interface{}{"type": "string"}},
			},
		}},
	}
	schemas := toolSchemas(tools)
	if schemas["run_command"] == nil {
		t.Fatal("no se indexó el esquema de run_command")
	}
	calls := []ToolCall{
		{Function: ToolCallFunction{Name: "run_command", Arguments: `{"command":"ls"}`}},
		{Function: ToolCallFunction{Name: "run_command", Arguments: `{}`}},
		{Function: ToolCallFunction{Name: "desconocida", Arguments: `x`}}, // sin esquema: se ignora
	}
	if errs := validateToolCalls(calls, schemas); len(errs) != 1 {
		t.Errorf("se esperaba 1 error de validación, se obtuvieron %d: %v", len(errs), errs)
	}
}

func TestToolChoiceUnsatisfied(t *testing.T) {
	call := []ToolCall{{Function: ToolCallFunction{Name: "run_command"}}}

	if toolChoiceUnsatisfied("auto", nil) {
		t.Error("auto siempre está satisfecho")
	}
	if !toolChoiceUnsatisfied("required", nil) {
		t.Error("required sin llamadas no está satisfecho")
	}
	if toolChoiceUnsatisfied("required", call) {
		t.Error("required con una llamada sí está satisfecho")
	}
	named := map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "run_command"}}
	if toolChoiceUnsatisfied(named, call) {
		t.Error("la función nombrada está entre las llamadas")
	}
	other := map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "read_file"}}
	if !toolChoiceUnsatisfied(other, call) {
		t.Error("la función nombrada no está entre las llamadas")
	}
}

func knownSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func TestResolveToolName(t *testing.T) {
	known := knownSet("bash", "read", "write", "edit", "glob", "grep", "webfetch")
	cases := []struct {
		in   string
		want string
	}{
		// declarado: se devuelve tal cual
		{"bash", "bash"},
		// sinónimo → nombre canónico declarado
		{"shell_command", "bash"},
		{"run_command", "bash"},
		{"sh", "bash"},
		{"cat", "read"},
		{"write_file", "write"},
		{"str_replace", "edit"},
		{"ls", "glob"},
		{"rg", "grep"},
		{"http", "webfetch"},
		// variación de mayúsculas/guiones → mismo nombre normalizado
		{"Read_File", "read"},
		// errata pequeña (distancia 1) → se corrige
		{"bashh", "bash"},
		{"writ", "write"},
		// nombre sin relación con nada declarado → no se toca
		{"inventada", "inventada"},
		{"zzzzzzzz", "zzzzzzzz"},
	}
	for _, c := range cases {
		if got := resolveToolName(c.in, known); got != c.want {
			t.Errorf("resolveToolName(%q) = %q, se esperaba %q", c.in, got, c.want)
		}
	}
	// Sin conjunto declarado no se corrige nada.
	if got := resolveToolName("shell", nil); got != "shell" {
		t.Errorf("sin known no debería corregir: %q", got)
	}
}

func TestEditDistance(t *testing.T) {
	if d := editDistance("bash", "bash"); d != 0 {
		t.Errorf("idénticos: %d", d)
	}
	if d := editDistance("bashh", "bash"); d != 1 {
		t.Errorf("una inserción: %d", d)
	}
	if d := editDistance("read", "reed"); d != 1 {
		t.Errorf("una sustitución: %d", d)
	}
	if d := editDistance("", "abc"); d != 3 {
		t.Errorf("vacío vs abc: %d", d)
	}
}

// El nombre inventado por el modelo se corrige al declarado más probable cuando es un sinónimo
// conocido o una errata cercana, y solo se deja tal cual si no hay certeza.
func TestParseToolCallsCorrectsUnknownName(t *testing.T) {
	known := knownSet("bash", "read")
	in := "```tool_call\n{\"name\": \"shell_command\", \"arguments\": {\"command\": \"ls\"}}\n```"
	_, calls := parseToolCalls(in, known)
	if len(calls) != 1 || calls[0].Function.Name != "bash" {
		t.Fatalf("no se corrigió el sinónimo a bash: %+v", calls)
	}
	in2 := "```tool_call\n{\"name\": \"inventada\", \"arguments\": {}}\n```"
	_, calls2 := parseToolCalls(in2, known)
	if len(calls2) != 1 || calls2[0].Function.Name != "inventada" {
		t.Fatalf("un nombre sin relación no debería corregirse: %+v", calls2)
	}
}

// La compuerta de streaming aplica la misma corrección que parseToolCalls.
func TestToolFenceGateCorrectsUnknownName(t *testing.T) {
	g := newToolFenceGate(func(string) {})
	g.known = knownSet("bash", "read")
	var got string
	g.onTool = func(_ int, tc ToolCall) { got = tc.Function.Name }
	for _, c := range chars("```tool_call\n{\"name\":\"Bash\",\"arguments\":{}}\n```") {
		g.Push(c)
	}
	if got != "bash" {
		t.Fatalf("la compuerta no corrigió el nombre: %q", got)
	}
}

func TestHasUnclosedToolFence(t *testing.T) {
	if hasUnclosedToolFence("texto normal sin bloque") {
		t.Error("texto sin apertura no debería marcar truncado")
	}
	if !hasUnclosedToolFence("escribiendo…\n```tool_call\n{\"name\":\"write\"") {
		t.Error("una apertura sin cierre debería marcar truncado")
	}
	// Un fence válido lo elimina parseToolCalls, así que el texto limpio no lo marca.
	clean, _ := parseToolCalls("```tool_call\n{\"name\":\"write\",\"arguments\":{}}\n```")
	if hasUnclosedToolFence(clean) {
		t.Error("un fence válido cerrado no debería marcar truncado")
	}
}

// El reintento correctivo se dispara por tool_choice incumplido o por argumentos que no validan.
func TestNeedsToolRetry(t *testing.T) {
	schemas := toolSchemas([]map[string]interface{}{{
		"type": "function", "function": map[string]interface{}{
			"name": "run_command",
			"parameters": map[string]interface{}{
				"type": "object", "required": []interface{}{"command"},
				"properties": map[string]interface{}{"command": map[string]interface{}{"type": "string"}},
			},
		},
	}})

	if bad, _ := needsToolRetry("required", nil, schemas); !bad {
		t.Error("required sin llamadas debería pedir reintento")
	}
	okCall := []ToolCall{{Function: ToolCallFunction{Name: "run_command", Arguments: `{"command":"ls"}`}}}
	if bad, _ := needsToolRetry("auto", okCall, schemas); bad {
		t.Error("una llamada válida con auto no debería pedir reintento")
	}
	badCall := []ToolCall{{Function: ToolCallFunction{Name: "run_command", Arguments: `{}`}}}
	if bad, errs := needsToolRetry("auto", badCall, schemas); !bad || len(errs) == 0 {
		t.Error("argumentos inválidos deberían pedir reintento con errores")
	}
}

func TestToolRetryReminder(t *testing.T) {
	r := toolRetryReminder("required", nil)
	if !strings.Contains(r, "MUST call one of") {
		t.Errorf("falta el recordatorio de tool_choice: %q", r)
	}
	r2 := toolRetryReminder("auto", []error{fmt.Errorf("run_command: falta el campo requerido \"command\"")})
	if !strings.Contains(r2, "invalid arguments") || !strings.Contains(r2, "run_command") {
		t.Errorf("falta el recordatorio de argumentos: %q", r2)
	}
	if got := toolRetryReminder("auto", nil); got != "" {
		t.Errorf("auto sin errores no debería generar recordatorio: %q", got)
	}
}
