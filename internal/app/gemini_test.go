package app

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Confirma que el hex id y el valor de header resueltos para cada nombre de modelo sean correctos.
func TestModelHeader(t *testing.T) {
	withPoolCookie(t) // hace seleccionable 3.1 Pro; ver TestProHiddenWithoutCookie

	cases := []struct{ name, wantHex string }{
		{"gemini-3.6-flash", hexFlash36},
		{"gemini-3.5-flash-lite", hexFlashLite},
		{"gemini-3.1-pro", hexPro31},
	}
	for _, c := range cases {
		name, mc, err := resolveModel(c.name)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if mc.HexID != c.wantHex {
			t.Errorf("%s: hex=%s want %s", c.name, mc.HexID, c.wantHex)
		}
		h := buildGeminiHeaders("", "", mc.HexID)
		got := h["x-goog-ext-525001261-jspb"]
		want := `[1,null,null,null,"` + c.wantHex + `"]`
		if got != want {
			t.Errorf("%s: header=%s want %s", c.name, got, want)
		}
		t.Logf("%-32s -> mode=%d header=%s", name, mc.Mode, got)
	}

	// @think=N es un parámetro ficticio heredado; se elimina y se ignora, sin error ni cambio de enrutado
	name, mc, err := resolveModel("gemini-3.6-flash@think=2")
	if err != nil || name != "gemini-3.6-flash" || mc.HexID != hexFlash36 {
		t.Errorf("el sufijo @think debería ignorarse, got name=%s hex=%s err=%v", name, mc.HexID, err)
	}
	// un modelo desconocido sigue dando error, sin recaída silenciosa
	if _, _, err := resolveModel("no-such-model"); err == nil {
		t.Error("unknown model should error")
	}
	// los alias antiguos eliminados deben dar error explícito, sin recaer silenciosamente en 3.6 Flash
	for _, gone := range []string{"gemini-3.5-flash", "gemini-3.5-flash-thinking",
		"gemini-3.5-flash-thinking-lite", "gemini-auto", "gemini-flash-lite"} {
		if _, _, err := resolveModel(gone); err == nil {
			t.Errorf("el alias eliminado %s debería dar error", gone)
		}
	}
	// Lo que se defiende es «que no aparezcan modelos falsos»: la versión thinking reutiliza el mismo
	// hex, así que se mira el **número de hex únicos**, no el de entradas. En el listado del servidor
	// (otAQ7b) 3.6/3.5-lite/3.1-pro + el 3.7 liberado en gris = 4.
	hexes := map[string]bool{}
	for _, m := range Models {
		hexes[m.HexID] = true
	}
	if len(hexes) != 4 {
		t.Errorf("deberían existir solo 4 hex de modelos reales, got %d", len(hexes))
	}
	// a la inversa: cada hex real debería tener una versión thinking
	for _, base := range []string{hexFlash36, hexFlashLite, hexPro31, hexFlash38} {
		found := false
		for _, m := range Models {
			if m.HexID == base && m.Thinking {
				found = true
			}
		}
		if !found {
			t.Errorf("al hex %s le falta la versión thinking", base)
		}
	}
}

// Respuesta real de "rechazado" en la captura: solo trama de fin, sin trama de contenido (216 bytes).
// Ojo: la trama de fin lleva BardErrorInfo[1096], pero la trama de fin de una respuesta normal
// también lleva ese código, así que el criterio solo puede ser "si hay o no trama de contenido".
const rejectedRaw = ")]}'\n\n122\n" +
	`[["wrb.fr",null,null,null,null,[13,null,[["type.googleapis.com/assistant.boq.bard.application.BardErrorInfo",[1096]]]]]]` +
	"\n56\n" + `[["di",192],["af.httprm",191,"8196459853603899163",2]]` +
	"\n25\n" + `[["e",4,null,null,216]]` + "\n"

// Respuesta normal: hay trama de contenido, [39] es el hex id del modelo y [42] el nombre mostrado; la trama de fin también lleva 1096.
const okRaw = ")]}'\n\n900\n" +
	`[["wrb.fr",null,"[null,[\"c_x\",\"r_y\"],null,null,[[\"rc_z\",[\"banana\"],null,null,null,null,null,null,[2],\"en\"]],null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,\"fbb127bbb056c959\",null,null,\"3.6 Flash\",true]"]]` +
	"\n122\n" +
	`[["wrb.fr",null,null,null,null,[13,null,[["type.googleapis.com/assistant.boq.bard.application.BardErrorInfo",[1096]]]]]]` + "\n"

func TestEmptyFrameDetection(t *testing.T) {
	if got := extractResponseText(rejectedRaw); got != "" {
		t.Errorf("una respuesta rechazada debería parsear a texto vacío, got %q", got)
	}
	if got := extractResponseText(okRaw); got != "banana" {
		t.Errorf("una respuesta normal debería parsear a banana, got %q", got)
	}
}

// usage debe seguir el mismo criterio que la tabla requests (ambos pasan por tiktoken), no puede volver a chars/4.
func TestUsageUsesTokenizer(t *testing.T) {
	initTokenizer()
	// Toma una muestra donde tiktoken y chars/4 difieran claramente; si no, la aserción no distingue la implementación nueva de la vieja.
	prompt := "请详细解释注意力机制的计算过程，并说明多头注意力为什么有效。"
	text := "自注意力通过查询、键、值三组投影计算token之间的相关性权重。"

	if tokenizerOK && countTokens(prompt) == len(prompt)/4 {
		t.Skipf("la muestra no distingue (tiktoken y chars/4 dan ambos %d); cambia de muestra y repite", countTokens(prompt))
	}

	u := buildUsage(prompt, text, false)
	if u["prompt_tokens"] != countTokens(prompt) || u["completion_tokens"] != countTokens(text) {
		t.Errorf("chat usage no pasó por countTokens: %v", u)
	}
	if u["total_tokens"] != u["prompt_tokens"]+u["completion_tokens"] {
		t.Errorf("total no es igual a la suma de ambos: %v", u)
	}
	if tokenizerOK && u["prompt_tokens"] == len(prompt)/4 {
		t.Errorf("prompt_tokens cae en chars/4, sigue siendo la implementación vieja: %v", u)
	}

	r := buildUsage(prompt, text, true)
	if r["input_tokens"] != countTokens(prompt) || r["output_tokens"] != countTokens(text) {
		t.Errorf("responses usage no pasó por countTokens: %v", r)
	}
	if _, ok := r["prompt_tokens"]; ok {
		t.Error("la API responses no debería incluir el campo prompt_tokens")
	}
}

func TestToolChoice(t *testing.T) {
	for _, c := range []struct {
		in         interface{}
		mode, name string
	}{
		{nil, "auto", ""},
		{"auto", "auto", ""},
		{"none", "none", ""},
		{"required", "required", ""},
		{map[string]interface{}{"type": "function",
			"function": map[string]interface{}{"name": "get_weather"}}, "required", "get_weather"},
		{"garbage", "auto", ""},
	} {
		m, n := parseToolChoice(c.in)
		if m != c.mode || n != c.name {
			t.Errorf("%v -> (%s,%s) want (%s,%s)", c.in, m, n, c.mode, c.name)
		}
	}

	tools := []map[string]interface{}{
		{"type": "function", "function": map[string]interface{}{"name": "get_weather"}},
		{"type": "function", "function": map[string]interface{}{"name": "send_email"}},
	}
	msgs := []map[string]interface{}{{"role": "user", "content": "hi"}}

	// none: las definiciones de herramientas no entran en absoluto en el prompt
	if p := mustPrompt(t, msgs, tools, "none"); strings.Contains(p, "get_weather") {
		t.Errorf("tool_choice=none no debería inyectar definiciones de herramientas: %s", p)
	}
	// required: debe aparecer la redacción forzosa
	p := mustPrompt(t, msgs, tools, "required")
	if !strings.Contains(p, "MUST call one of the tools") {
		t.Errorf("tool_choice=required carece de la instrucción forzosa: %s", p)
	}
	// función específica: se deja solo esa función y se la nombra
	p = mustPrompt(t, msgs, tools, map[string]interface{}{"type": "function",
		"function": map[string]interface{}{"name": "get_weather"}})
	if !strings.Contains(p, `MUST call the tool "get_weather"`) {
		t.Errorf("falta la instrucción forzosa para la función específica: %s", p)
	}
	if strings.Contains(p, "send_email") {
		t.Errorf("con una función específica no deberían incluirse otras herramientas: %s", p)
	}
	// auto: se conserva la redacción permisiva original
	if p := mustPrompt(t, msgs, tools, "auto"); !strings.Contains(p, "when needed") {
		t.Errorf("auto debería conservar la redacción permisiva: %s", p)
	}
}

// Invariante central del streaming: todos los incrementos concatenados deben igualar exactamente el texto final, sin pérdidas ni duplicados.
func TestDeltaTrackerAccumulates(t *testing.T) {
	// cada trama del upstream es el texto completo acumulado y crece trama a trama
	frames := []string{
		"Transformer",
		"Transformer 的核心",
		"Transformer 的核心是自注意力",
		"Transformer 的核心是自注意力机制。",
	}
	d := &deltaTracker{}
	var got string
	for _, f := range frames {
		got += d.Push(f)
	}
	want := cleanGeminiText(frames[len(frames)-1])
	if got != want {
		t.Errorf("concatenación de incrementos=%q want %q", got, want)
	}
	if d.emitted != want {
		t.Errorf("emitted=%q want %q", d.emitted, want)
	}

	// una trama repetida no debe emitirse de nuevo
	if extra := d.Push(frames[len(frames)-1]); extra != "" {
		t.Errorf("una trama repetida no debería producir incremento, got %q", extra)
	}
	// una trama más corta (que llega fuera de orden) tampoco se emite
	if extra := d.Push("Transformer"); extra != "" {
		t.Errorf("una trama más corta no debería producir incremento, got %q", extra)
	}
	// si el prefijo no coincide se omite y emitted permanece igual
	before := d.emitted
	if extra := d.Push("完全不同的一段文本，比原来的还要长很多很多很多很多"); extra != "" {
		t.Errorf("una trama que no es prefijo no debería producir incremento, got %q", extra)
	}
	if d.emitted != before {
		t.Errorf("una trama que no es prefijo no debería modificar emitted")
	}

	// remainingText se encarga de completar la cola que el tracker omitió
	full := want + "补充的尾巴"
	if r := remainingText(full, &StreamResult{Emitted: d.emitted}); r != "补充的尾巴" {
		t.Errorf("remainingText=%q want %q", r, "补充的尾巴")
	}
	// cuando no es streaming real (Emitted vacío) debe devolver el texto completo
	if r := remainingText(full, &StreamResult{}); r != full {
		t.Errorf("con Emitted vacío debería devolver el texto completo")
	}
	// si el prefijo no coincide no se reenvía, para evitar duplicados
	if r := remainingText("另一段内容", &StreamResult{Emitted: d.emitted}); r != "" {
		t.Errorf("si el prefijo no coincide no debería reenviarse, got %q", r)
	}
}

// El servidor degrada en silencio (una petición anónima a 3.1 Pro en realidad recibe 3.5 Flash-Lite);
// mirando solo el nombre de la petición no se detecta, así que hay que extraer de la trama de respuesta
// el nombre de modelo que él mismo declara y registrarlo aparte.
func TestExtractUpstreamModel(t *testing.T) {
	if got := extractUpstreamModel(okRaw); got != "3.6 Flash" {
		t.Errorf("una respuesta normal debería extraer '3.6 Flash', got %q", got)
	}
	// un nombre de modelo con el sufijo "扩展" también debe extraerse completo, sin cortarlo en el espacio
	downgraded := `[["wrb.fr",null,"[null,[\"c_x\",\"r_y\"],null,null,[[\"rc_z\",[\"hi\"]]],null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,null,\"fbb127bbb056c959\",null,null,\"3.6 Flash 扩展\",true]"]]`
	if got := extractUpstreamModel(downgraded); got != "3.6 Flash 扩展" {
		t.Errorf("debería extraer '3.6 Flash 扩展', got %q", got)
	}
	// una respuesta rechazada no tiene nombre de modelo; no hay que adivinar
	if got := extractUpstreamModel(rejectedRaw); got != "" {
		t.Errorf("una respuesta rechazada debería devolver vacío, got %q", got)
	}
}

// Sin cookie configurada, 3.1 Pro se degrada en silencio a Flash-Lite; mejor no exponerlo:
// que el cliente reciba un error claro al elegir el modelo, en lugar de una respuesta
// "exitosa pero que en realidad no es Pro". (Con una cookie válida sí funciona; ver el comentario de availableModels.)
func TestProHiddenWithoutCookie(t *testing.T) {
	// el pool de cookies está vacío en este momento (TestMain usa una base temporal nueva)
	if _, ok := availableModels()["gemini-3.1-pro"]; ok {
		t.Error("sin cookie no debería exponerse 3.1 Pro")
	}
	// inner[80] (pensamiento extendido) solo surte efecto con sesión iniciada; en anónimo el servidor
	// lo ignora en silencio, así que la versión thinking, igual que 3.1 Pro, no puede exponerse
	for name, m := range availableModels() {
		if m.Thinking {
			t.Errorf("sin cookie no debería exponerse la versión thinking: %s", name)
		}
	}
	if len(availableModels()) != 2 {
		t.Errorf("sin cookie deberían quedar solo 2 modelos, got %d", len(availableModels()))
	}
	_, _, err := resolveModel("gemini-3.1-pro")
	if err == nil {
		t.Fatal("sin cookie, elegir 3.1 Pro debería dar error")
	}
	// el mensaje de error debe explicar el porqué y el cómo, no solo "unknown model"
	if !strings.Contains(err.Error(), "cookie") ||
		!strings.Contains(err.Error(), "degradan") ||
		!strings.Contains(err.Error(), "panel de administración") {
		t.Errorf("el mensaje de error no explica la causa ni la solución: %v", err)
	}

	withPoolCookie(t)
	if _, ok := availableModels()["gemini-3.1-pro"]; !ok {
		t.Error("con cookie configurada debería exponerse 3.1 Pro")
	}
	if _, _, err := resolveModel("gemini-3.1-pro"); err != nil {
		t.Errorf("con cookie configurada no debería dar error: %v", err)
	}
	for _, n := range []string{"gemini-3.6-flash-thinking", "gemini-3.5-flash-lite-thinking",
		"gemini-3.1-pro-thinking"} {
		if _, ok := availableModels()[n]; !ok {
			t.Errorf("con cookie configurada debería exponerse %s", n)
		}
	}
	// un modelo que de verdad no existe sigue siendo "unknown model" y no debe quedar tapado por el aviso de cookie
	if _, _, err := resolveModel("no-such-model"); err == nil ||
		!strings.Contains(err.Error(), "modelo desconocido") {
		t.Errorf("un modelo desconocido debería dar «modelo desconocido», got %v", err)
	}
}

// modelNeedsLogin es el único criterio compartido por «qué modelos excluir sin cookie» y
// «si el anónimo primero #20 debe o no tomar número»; se fija modelo a modelo: 3.6/3.5-lite
// de solo texto y sin pensamiento sirven en anónimo; el resto requiere inicio de sesión.
func TestModelNeedsLogin(t *testing.T) {
	anon := map[string]bool{"gemini-3.6-flash": true, "gemini-3.5-flash-lite": true}
	for name, mc := range Models {
		want := !anon[name]
		if got := modelNeedsLogin(mc); got != want {
			t.Errorf("%s: modelNeedsLogin=%v want %v", name, got, want)
		}
	}
	// debe coincidir con el criterio de exclusión de availableModels: lo expuesto sin cookie es justo lo disponible en anónimo
	for name := range availableModels() {
		if modelNeedsLogin(Models[name]) {
			t.Errorf("%s lo expone availableModels pero se considera que necesita inicio de sesión; los dos criterios no coinciden", name)
		}
	}
}

// Derivación de health en cookieAcctView (panel en rojo #18): fail_count>0 siempre rojo (incluidas
// las cuentas muertas deshabilitadas automáticamente); deshabilitada a mano y sin fallos = gris;
// nunca ha tenido éxito = pendiente de comprobación; el resto = normal.
func TestCookieHealthView(t *testing.T) {
	cases := []struct {
		a    CookieAccount
		want string
	}{
		{CookieAccount{Status: "enabled", FailCount: 0, LastOkAt: 100}, "ok"},
		{CookieAccount{Status: "enabled", FailCount: 3, LastOkAt: 100}, "dead"},
		{CookieAccount{Status: "disabled", FailCount: 5, LastOkAt: 100}, "dead"},
		{CookieAccount{Status: "disabled", FailCount: 0}, "disabled"},
		{CookieAccount{Status: "enabled", FailCount: 0, LastOkAt: 0}, "unknown"},
	}
	for _, c := range cases {
		if got := cookieAcctView(c.a)["health"]; got != c.want {
			t.Errorf("status=%s fail=%d ok=%d: health=%v want %s",
				c.a.Status, c.a.FailCount, c.a.LastOkAt, got, c.want)
		}
	}
}

// El SELECT+UPDATE de pickCookieAccount debe ser atómico: N números elegidos por N peticiones
// concurrentes, cada una debe obtener un número distinto. Sin bloqueo, dos peticiones hacen SELECT
// del mismo número "menos usado" y ambas lo utilizan (con -race se expone más fácil).
func TestPickCookieConcurrentDistinct(t *testing.T) {
	const n = 8
	for i := 0; i < n; i++ {
		id, err := accountAdd("conc", fmt.Sprintf("SAPISID=d%d; SID=x", i), "")
		if err != nil {
			t.Fatalf("fallo al insertar número: %v", err)
		}
		t.Cleanup(func() { _ = accountDelete(id) })
	}

	got := make([]int64, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			if a, ok := pickCookieAccount(); ok {
				got[idx] = a.ID
			}
		}(i)
	}
	close(start) // hace que todas las goroutine luchen por elegir número casi a la vez
	wg.Wait()

	seen := map[int64]int{}
	for _, id := range got {
		seen[id]++
	}
	for id, c := range seen {
		if c > 1 {
			t.Errorf("la cuenta #%d fue elegida concurrentemente %d veces; la elección no está serializada", id, c)
		}
	}
}

// Criterio de elección de número del anónimo primero (#20): con el interruptor apagado siempre false
// (se conserva el comportamiento antiguo); encendido, solo pasan por anónimo las peticiones de solo
// texto, sin pensamiento, sin herramientas y sin adjuntos; el resto eligen número.
func TestAnonFirstEligible(t *testing.T) {
	set := func(on bool) { rtMu.Lock(); rtVal.AnonFirst = on; rtMu.Unlock() }
	defer set(false)

	plain := Models["gemini-3.6-flash"]
	pro := Models["gemini-3.1-pro"]
	img := Models["gemini-image"]
	think := Models["gemini-3.6-flash-thinking"]

	set(false)
	if anonFirstEligible(plain, false) {
		t.Error("con el interruptor apagado siempre debería ser false (si hay número en el pool, se usa)")
	}

	set(true)
	if !anonFirstEligible(plain, false) {
		t.Error("encendido + solo texto sin adjuntos debería ir por anónimo")
	}
	if anonFirstEligible(plain, true) {
		t.Error("con adjuntos hay que elegir número (la referencia anónima es rechazada por el upstream con 1100)")
	}
	if anonFirstEligible(pro, false) {
		t.Error("3.1 Pro requiere inicio de sesión, no puede ir por anónimo")
	}
	if anonFirstEligible(img, false) {
		t.Error("la generación de imágenes requiere inicio de sesión, no puede ir por anónimo")
	}
	if anonFirstEligible(think, false) {
		t.Error("el pensamiento extendido requiere inicio de sesión, no puede ir por anónimo")
	}
}

// La clasificación de errores debe hacerse según "qué hacer tras verlo"; sobre todo hay que separar
// "rechazo transitorio del upstream" de "salida bloqueada": el primero basta con reintentar, el segundo
// obliga a cambiar de IP; mezclados no se puede decidir al depurar.
func TestClassifyError(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"upstream returned no content frame (raw 381 bytes)", "upstream_rejected"},
		{"direct IP slot full: rpm limit reached (configure proxies to scale)", "rate_limited_local"},
		{"all proxy slots full: rph limit reached", "rate_limited_local"},
		{"upstream HTTP 302: <HTML><HEAD><meta ...", "blocked_sorry"},
		{"upstream HTTP 429: too many requests", "upstream_429"},
		{"upstream HTTP 500: internal", "upstream_http_error"},
		{"unknown model: gemini-9", "bad_request"},
		{"image input not supported: ...", "bad_request"},
		{"dial tcp 1.2.3.4:10000: connectex: connection refused", "network"},
		{"remote error: tls: handshake failure", "network"},
	} {
		if got := classifyError(c.in); got != c.want {
			t.Errorf("classifyError(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// mustPrompt 是测试里的便捷包装：messagesToPrompt 现在会在 prompt 超长时返回错误。
func mustPrompt(t *testing.T, msgs []map[string]interface{}, tools []map[string]interface{},
	toolChoice interface{}) string {
	t.Helper()
	p, _ := messagesToPrompt(msgs, tools, toolChoice)
	return p
}
