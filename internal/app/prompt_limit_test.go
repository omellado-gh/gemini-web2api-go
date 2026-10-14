package app

import (
	"strings"
	"testing"
)

// 判据：prompt 超过单请求上限时**明确报错**，绝不悄悄改动内容。
//
// 上游超限时从**尾部**静默截断且不报错，而最新消息拼在末尾，所以被吃掉的正好是
// 用户刚问的那句 —— 模型只看到前面的系统前言，回一句通用开场白，既不答题也不
// 调工具。实测两个不同客户端的用户都栽在这上面，都被误认成"模型变笨"。
//
// 我们自己丢历史同样不行：那还是静默丢数据，只是换了个地方丢。客户端以为整段都
// 发出去了，模型却忘了东西。报 context_length_exceeded 是 OpenAI 兼容客户端认得
// 的信号，agentic 客户端收到会自己压缩上下文再试。

func withBudget(t *testing.T, n int) { // n = 字节预算
	t.Helper()
	old := rtCfg()
	next := old
	next.MaxPromptBytes = n
	rtMu.Lock()
	rtVal = next
	rtMu.Unlock()
	t.Cleanup(func() {
		rtMu.Lock()
		rtVal = old
		rtMu.Unlock()
	})
}

func longConversation(turns int) []map[string]interface{} {
	msgs := []map[string]interface{}{
		{"role": "system", "content": "你是一个 helpful assistant。"},
	}
	for i := 0; i < turns; i++ {
		msgs = append(msgs,
			map[string]interface{}{"role": "user", "content": strings.Repeat("历史填充内容。", 20)},
			map[string]interface{}{"role": "assistant", "content": "Noted."})
	}
	return append(msgs, map[string]interface{}{"role": "user", "content": "读取一下 task.md"})
}

func TestPromptOverLimitErrors(t *testing.T) {
	withBudget(t, 20000) // bytes

	prompt, _ := messagesToPrompt(longConversation(200), nil, nil, false)
	if len(prompt) <= 20000 {
		t.Fatalf("el propio caso de prueba no superó el presupuesto (%d bytes)", len(prompt))
	}
	// el mensaje de error debe explicar por qué se rechazó; si no, el usuario pensará que estamos rotos
	noCookie := error(&PromptTooLongError{Bytes: len(prompt), Budget: 20000})
	for _, want := range []string{"truncates", "latest message", "not tokens"} {
		if !strings.Contains(noCookie.Error(), want) {
			t.Errorf("al mensaje de error le falta %q: %v", want, noCookie)
		}
	}
	// sin cookie esto no es un callejón sin salida: hay que decirle al usuario que importando una podrá usar adjuntos
	for _, want := range []string{"Cookie pool", "attachment"} {
		if !strings.Contains(noCookie.Error(), want) {
			t.Errorf("sin cookie debería sugerir importar una cookie, falta %q: %v", want, noCookie)
		}
	}
	// tener cookie y aun así pasarse = la vía de adjuntos tampoco lo salvó; ya no hay que mandar a importar una cookie
	withCookie := error(&PromptTooLongError{Bytes: len(prompt), Budget: 20000, HasCookie: true})
	if strings.Contains(withCookie.Error(), "Cookie pool") {
		t.Error("ya hay cookie y aun así se sugiere importar una cookie")
	}
	if !strings.Contains(withCookie.Error(), "Shorten") {
		t.Error("con cookie debería sugerir comprimir el contenido")
	}
}

// Sin pasarse del límite no se puede tocar ni un carácter, incluida cualquier inyección del tipo "se omitió parte del historial".
func TestPromptUnderLimitUntouched(t *testing.T) {
	withBudget(t, 10000000)
	msgs := longConversation(3)

	got, _ := messagesToPrompt(msgs, nil, nil, false)
	if want := buildPrompt(msgs, nil, nil, false); got != want {
		t.Error("no se superó el límite y aun así se modificó el prompt")
	}
	if !strings.Contains(got, "读取一下 task.md") {
		t.Error("la pregunta más reciente no está en el prompt")
	}
}

// 0 = desactiva la comprobación y vuelve al comportamiento antiguo (se envía tal cual; el upstream trunca por el final).
func TestPromptLimitDisabled(t *testing.T) {
	withBudget(t, 0)
	msgs := longConversation(200)

	got, _ := messagesToPrompt(msgs, nil, nil, false)
	if len(got) < 20000 {
		t.Error("con la comprobación desactivada no debería haber ningún recorte")
	}
}

// El límite se aplica al **prompt completo**, y las definiciones de herramientas cuentan: el esquema
// de herramientas de un cliente agentic suele ser mayor que la propia conversación; no contarlo es
// como no poner defensa.
func TestPromptLimitCountsToolDefs(t *testing.T) {
	withBudget(t, 5000)
	tools := []map[string]interface{}{{"type": "function", "function": map[string]interface{}{
		"name": "read_file", "description": strings.Repeat("一个很长的工具描述。", 300),
		"parameters": map[string]interface{}{"type": "object"}}}}
	msgs := []map[string]interface{}{{"role": "user", "content": "你好"}}

	if p, _ := messagesToPrompt(msgs, tools, nil, false); len(p) <= 5000 {
		t.Errorf("las definiciones de herramientas deberían llevar el prompt por encima de 5000 bytes; en realidad %d", len(p))
	}
}
