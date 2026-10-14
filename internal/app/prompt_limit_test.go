package app

import (
	"strings"
	"testing"
)

// Criterio: cuando el prompt supera el límite de una petición se **informa de un error claro**;
// nunca se altera el contenido en silencio.
//
// Cuando el upstream se pasa del límite, trunca en silencio por el **final** sin dar error, y como
// el mensaje más reciente va al final, lo que se come es justo la frase que acaba de preguntar el
// usuario: el modelo solo ve el preámbulo de sistema y responde con una introducción genérica, sin
// contestar ni llamar a herramientas. En pruebas reales, usuarios de dos clientes distintos cayeron
// en esto y ambos lo interpretaron como "el modelo se volvió tonto".
//
// Descartar historial nosotros tampoco vale: sigue siendo pérdida silenciosa de datos, solo que en
// otro sitio. El cliente cree que se envió todo, pero el modelo olvidó cosas. Devolver
// context_length_exceeded es la señal que reconocen los clientes compatibles con OpenAI; un cliente
// agentic que la recibe comprime el contexto por su cuenta y reintenta.

func withBudget(t *testing.T, n int) { // n = presupuesto en bytes
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
