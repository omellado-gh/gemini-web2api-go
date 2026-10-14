package app

import (
	"strings"
	"testing"
)

// feed alimenta todo el texto a la compuerta con el troceado dado y devuelve lo que realmente se envió al cliente.
// El troceado debe poder variar libremente: dónde caen los límites de trama del upstream no lo decidimos
// nosotros en absoluto, y el resultado de la compuerta no puede cambiar por ello.
func feed(t *testing.T, chunks []string) string {
	t.Helper()
	var got strings.Builder
	g := newToolFenceGate(func(s string) { got.WriteString(s) })
	for _, c := range chunks {
		g.Push(c)
	}
	if got.String() != g.Sent() {
		t.Fatalf("Sent() no coincide con lo realmente enviado: %q vs %q", g.Sent(), got.String())
	}
	return got.String()
}

// Alimentar byte a byte es el troceado más duro: cada carácter es una trama nueva, y una valla a medias aparecerá forzosamente en el límite de trama.
func chars(s string) []string {
	var out []string
	for _, r := range s {
		out = append(out, string(r))
	}
	return out
}

func TestToolFenceGate(t *testing.T) {
	cases := []struct {
		name string
		full string
		want string
	}{
		{"没有围栏就原样发", "你好，今天天气不错。", "你好，今天天气不错。"},
		{
			"围栏前后的正文都要发，围栏本身扣住",
			"让我查一下。\n```tool_call\n{\"name\": \"get_weather\", \"arguments\": {}}\n```\n查完了。",
			"让我查一下。\n\n查完了。",
		},
		{
			"整段都是围栏则一个字都不发",
			"```tool_call\n{\"name\": \"f\", \"arguments\": {}}\n```",
			"",
		},
		{
			"连着两个围栏",
			"a\n```tool_call\n{\"name\":\"f\"}\n```\nb\n```tool_call\n{\"name\":\"g\"}\n```\nc",
			"a\n\nb\n\nc",
		},
		{
			// un bloque de código normal no es tool_call y no debe retenerse
			"普通代码块照常发",
			"看这段：\n```python\nprint(1)\n```\n就这样。",
			"看这段：\n```python\nprint(1)\n```\n就这样。",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// los tres troceados deben dar el mismo resultado
			if got := feed(t, []string{c.full}); got != c.want {
				t.Errorf("alimentado completo: se obtuvo %q, se esperaba %q", got, c.want)
			}
			if got := feed(t, chars(c.full)); got != c.want {
				t.Errorf("alimentado carácter a carácter: se obtuvo %q, se esperaba %q", got, c.want)
			}
			mid := len(c.full) / 2
			for mid < len(c.full) && !isRuneStart(c.full[mid]) {
				mid++
			}
			if got := feed(t, []string{c.full[:mid], c.full[mid:]}); got != c.want {
				t.Errorf("alimentado por mitades: se obtuvo %q, se esperaba %q", got, c.want)
			}
		})
	}
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// Lo enviado por la compuerta + el texto final de parseToolCalls deben reconstruir el mismo cuerpo;
// este es el criterio de que el resultado en streaming y sin streaming coinciden.
func TestToolFenceGateMatchesParse(t *testing.T) {
	full := "让我查一下。\n```tool_call\n{\"name\": \"get_weather\", \"arguments\": {\"city\": \"London\"}}\n```\n查完了。"

	streamed := feed(t, chars(full))
	final, calls := parseToolCalls(full)

	if len(calls) != 1 || calls[0].Function.Name != "get_weather" {
		t.Fatalf("no se parseó la llamada a herramienta: %+v", calls)
	}
	// lo emitido en streaming puede tener espacios sobrantes al inicio/fin respecto al texto final (parseToolCalls hace TrimSpace al cerrar).
	if strings.TrimSpace(streamed) != final {
		t.Errorf("streaming emitió %q, texto final %q", strings.TrimSpace(streamed), final)
	}
}

// Valla sin cerrar: la compuerta la retiene sin emitir; al cerrar, remainingOf la repone, el cliente no pierde contenido.
func TestToolFenceGateUnclosed(t *testing.T) {
	full := "开始了。\n```tool_call\n{\"name\": \"f\""
	streamed := feed(t, chars(full))
	if streamed != "开始了。\n" {
		t.Fatalf("la parte retenida es incorrecta: %q", streamed)
	}
	// parseToolCalls no procesa la valla sin cerrar y la deja tal cual en el texto
	final, calls := parseToolCalls(full)
	if len(calls) != 0 {
		t.Fatalf("una valla sin cerrar no debería parsear llamadas a herramientas: %+v", calls)
	}
	if rest := remainingOf(final, streamed); rest == "" {
		t.Error("el reenvío al cerrar sale vacío; el contenido retenido se perdería")
	}
}

func TestPartialPrefixLen(t *testing.T) {
	m := toolFenceOpen // "```tool_call"
	for _, c := range []struct {
		s    string
		want int
	}{
		{"abc", 0},
		{"abc`", 1},
		{"abc``", 2},
		{"abc```", 3},
		{"abc```tool_c", 9},
		{"", 0},
		{"中文`", 1}, // el punto de corte debe caer en ASCII; no debe partir un carácter multibyte
		{"中文", 0},
	} {
		if got := partialPrefixLen(c.s, m); got != c.want {
			t.Errorf("partialPrefixLen(%q) = %d, se esperaba %d", c.s, got, c.want)
		}
	}
}

// La compuerta debe emitir la tool_call en cuanto se cierra el bloque, con índice
// incremental y sin enviar el fence como texto.
func TestToolFenceGateEmitsToolCalls(t *testing.T) {
	type emitted struct {
		idx  int
		name string
		args string
	}
	var got []emitted
	full := "vamos\n```tool_call\n{\"name\": \"run_command\", \"arguments\": {\"command\": \"ls\"}}\n```\nlisto"
	g := newToolFenceGate(func(string) {})
	g.known = map[string]bool{"run_command": true}
	g.onTool = func(i int, tc ToolCall) {
		got = append(got, emitted{i, tc.Function.Name, tc.Function.Arguments})
	}
	for _, c := range chars(full) {
		g.Push(c)
	}
	if len(got) != 1 || got[0].idx != 0 || got[0].name != "run_command" || !strings.Contains(got[0].args, "ls") {
		t.Fatalf("emisión incorrecta: %+v", got)
	}
	if g.ToolCallCount() != 1 {
		t.Fatalf("ToolCallCount = %d, se esperaba 1", g.ToolCallCount())
	}
	if strings.Contains(g.Sent(), "tool_call") {
		t.Errorf("el fence no debería enviarse como texto: %q", g.Sent())
	}
}

func TestToolFenceGateEmitsMultipleIndexed(t *testing.T) {
	var idxs []int
	full := "```tool_call\n{\"name\":\"a\",\"arguments\":{}}\n```\n```tool_call\n{\"name\":\"b\",\"arguments\":{}}\n```"
	g := newToolFenceGate(func(string) {})
	g.onTool = func(i int, tc ToolCall) { idxs = append(idxs, i) }
	for _, c := range chars(full) {
		g.Push(c)
	}
	if len(idxs) != 2 || idxs[0] != 0 || idxs[1] != 1 {
		t.Fatalf("índices incorrectos: %+v", idxs)
	}
}

// Un bloque no interpretable no produce llamada y se conserva como texto (no se pierde).
func TestToolFenceGateInvalidKeepsText(t *testing.T) {
	g := newToolFenceGate(func(string) {})
	g.onTool = func(int, ToolCall) { t.Error("no debería emitir una llamada inválida") }
	for _, c := range chars("```tool_call\n{roto}\n```") {
		g.Push(c)
	}
	if g.ToolCallCount() != 0 {
		t.Errorf("ToolCallCount = %d, se esperaba 0", g.ToolCallCount())
	}
	if !strings.Contains(g.Sent(), "tool_call") {
		t.Errorf("un bloque inválido debe conservarse como texto: %q", g.Sent())
	}
}

// Con nombres declarados, una herramienta no declarada se EMITE igualmente (no se degrada a texto).
func TestToolFenceGateUnknownNameStillEmitted(t *testing.T) {
	emitted := false
	g := newToolFenceGate(func(string) {})
	g.known = map[string]bool{"run_command": true}
	g.onTool = func(_ int, tc ToolCall) { emitted = tc.Function.Name == "otra" }
	for _, c := range chars("```tool_call\n{\"name\":\"otra\",\"arguments\":{}}\n```") {
		g.Push(c)
	}
	if !emitted {
		t.Error("una herramienta no declarada debe emitirse igualmente")
	}
	if strings.Contains(g.Sent(), "tool_call") {
		t.Errorf("el bloque no debería enviarse como texto: %q", g.Sent())
	}
}
