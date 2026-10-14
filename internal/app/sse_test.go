package app

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Verifica la emisión SSE del camino bufferizado (turnos con tools): primero el chunk de rol,
// luego el cuerpo, después las tool_calls, el finish_reason y [DONE], en el orden de la spec.
func TestSSEWriterBufferedToolCalls(t *testing.T) {
	rec := httptest.NewRecorder()
	s := newSSEWriter(rec, "chatcmpl-test", 0, "gemini-3.1-pro")
	s.SendContent("hola ")
	s.SendToolCalls([]ToolCall{{
		ID: "call_1", Type: "function",
		Function: ToolCallFunction{Name: "bash", Arguments: `{"command":"ls"}`},
	}})
	s.Finish("tool_calls", nil)
	body := rec.Body.String()

	for _, want := range []string{
		`"role":"assistant"`,
		`"content":"hola "`,
		`"tool_calls"`,
		`"name":"bash"`,
		`"finish_reason":"tool_calls"`,
		`data: [DONE]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("falta %q en el SSE:\n%s", want, body)
		}
	}
	if strings.Contains(body, "tool_call`") || strings.Contains(body, "```tool_call") {
		t.Errorf("el fence crudo no debe llegar al cliente:\n%s", body)
	}
}
