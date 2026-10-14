package app

import (
	"strings"
	"testing"
)

func cloneMsgs(ms []map[string]interface{}) []map[string]interface{} {
	out := make([]map[string]interface{}, len(ms))
	copy(out, ms)
	return out
}

func clearConvStore() {
	convMu.Lock()
	convStore = map[string]*convState{}
	convMu.Unlock()
}

func mkToolCallMsg(id, name, args string) map[string]interface{} {
	return map[string]interface{}{
		"role": "assistant", "content": "",
		"tool_calls": []interface{}{
			map[string]interface{}{
				"id": id, "type": "function",
				"function": map[string]interface{}{"name": name, "arguments": args},
			},
		},
	}
}

// Dos ramas que solo se diferencian en la herramienta llamada (ambas con content "") deben dar
// huellas distintas; si no, la continuación cruzaba sesiones.
func TestCanonicalMessagesDistinguishesToolCalls(t *testing.T) {
	history := []map[string]interface{}{{"role": "user", "content": "lee el archivo"}}
	a := append(cloneMsgs(history), mkToolCallMsg("call_1", "read", `{"filePath":"a.go"}`))
	b := append(cloneMsgs(history), mkToolCallMsg("call_2", "read", `{"filePath":"b.go"}`))
	if canonicalMessages(a) == canonicalMessages(b) {
		t.Fatal("tool_calls distintas dieron la misma huella de sesión")
	}
}

// La continuación se reconoce en el borde del assistant aunque después vengan varios resultados
// de herramienta (turno paralelo) y un mensaje de usuario nuevo.
func TestConvFindBoundaryWithToolResults(t *testing.T) {
	clearConvStore()
	history := []map[string]interface{}{
		{"role": "system", "content": "eres un agente"},
		{"role": "user", "content": "lee a y b"},
	}
	calls := []ToolCall{
		{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "read", Arguments: `{"filePath":"a"}`}},
		{ID: "call_2", Type: "function", Function: ToolCallFunction{Name: "read", Arguments: `{"filePath":"b"}`}},
	}
	// Simula el guardado tras el turno del assistant.
	stored := &convState{turn: 1, cid: "cid-1"}
	childKey := convChildKey(history, "", calls)
	convPut(childKey, stored)

	// Petición siguiente del cliente: historial + assistant (con las dos tool_calls) + 2 resultados.
	next := append(cloneMsgs(history), assistantMsgWithCalls("", calls))
	next = append(next,
		map[string]interface{}{"role": "tool", "tool_call_id": "call_1", "content": "a"},
		map[string]interface{}{"role": "tool", "tool_call_id": "call_2", "content": "b"},
	)
	st, key, from := convFind(next)
	if st == nil || key != childKey {
		t.Fatalf("no se encontró la sesión por el borde del assistant: st=%v key=%q", st, key)
	}
	if from != len(history)+1 {
		t.Fatalf("from = %d, se esperaba %d (justo tras el assistant)", from, len(history)+1)
	}
	newMsgs := next[from:]
	if len(newMsgs) != 2 || getStr(newMsgs[0], "role") != "tool" || getStr(newMsgs[1], "role") != "tool" {
		t.Fatalf("los mensajes nuevos deberían ser los 2 resultados de herramienta: %+v", newMsgs)
	}
}

// El turno de continuación solo debe enviar los mensajes nuevos, con la señal limpia de éxito y
// el nombre resuelto por tool_call_id, sin perder ninguno de los resultados en paralelo.
func TestFormatNewMessages(t *testing.T) {
	next := []map[string]interface{}{
		{"role": "tool", "tool_call_id": "call_1", "content": "Process exited with code 0\nOutput:\narchivo a"},
		{"role": "tool", "tool_call_id": "call_2", "content": "salida b"},
		{"role": "user", "content": "ahora resume"},
	}
	names := map[string]string{"call_1": "read", "call_2": "grep"}
	got := formatNewMessages(next, names)
	for _, want := range []string{"read", "grep", "archivo a", "salida b", "ahora resume", "exit 0"} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en el turno de continuación:\n%s", want, got)
		}
	}
	if hasCJK(got) {
		t.Errorf("el turno de continuación contiene CJK:\n%s", got)
	}
}

// La continuación solo se usa cuando corresponde: nunca con modelo multimedia ni con imágenes.
func TestShouldUseConvGuards(t *testing.T) {
	if shouldUseConv(ModelConfig{Tool: toolImage}, false, true) {
		t.Error("un modelo multimedia no debe ir por la vía de conversación")
	}
	if shouldUseConv(ModelConfig{}, true, true) {
		t.Error("una petición con imágenes no debe ir por la vía de conversación")
	}
}
