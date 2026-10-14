package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/google/uuid"
)

// IDs de modelo reconocidos por el servidor de Gemini, extraídos del listado oficial devuelto por batchexecute?rpcids=otAQ7b.
const (
	hexFlash36   = "fbb127bbb056c959" // 3.6 Flash
	hexFlashLite = "cf41b0e0dd7d53e5" // 3.5 Flash-Lite
	hexPro31     = "9d8ca3786ebdfbea" // 3.1 Pro
	// 3.7 Flash: lanzado por gradación por cuenta. El hex es el **hex principal** de la entrada 3.7 en otAQ7b (primer elemento),
	// aparece en los listados de dos cuentas con gradación independientes; en pruebas con usuarios con 3.7, enviarlo devuelve "3.7 Flash"
	// (issue #4 / PR #11). Atención: no usar el hex 797f3d0293f288ad de la lista de compatibilidad — ese es
	// un puntero genérico de "Flash actual": con lotes antiguos devuelve 3.6 y puede parecer 3.7. Con lotes antiguos, enviar este hex principal
	// produce una bajada limpia a 3.5 Flash-Lite (igual que 3.1 Pro), por lo que requiere cookie para habilitarlo.
	hexFlash38 = "56fdd199312815e2" // 3.8 Flash (originalmente 3.7; Google promovió este hex in situ a 3.8; en tramas HAR el nombre mostrado ya es "3.8 Flash")
)

// innerSlots es la longitud del array inner en el payload. El navegador envía 97-98 slots; originalmente abríamos 80,
// por lo que inner[80] (pensamiento extendido) no existía y no se podía rellenar.
//
// Aumentar este valor es seguro: se probó subirlo de 80 a 102 para ver si inner[79] reaparecía; conclusión: no reaparece
// — la selección de modelo depende exclusivamente de la cabecera x-goog-ext-525001261-jspb; la longitud no afecta.
const innerSlots = 97

// Valor para inner[80] / índice 15 de la cabecera de modelo: 1=normal, 2=pensamiento extendido.
//
// **Solo válido con sesión iniciada**: en solicitudes anónimas el servidor lo ignora silenciosamente y devuelve el modelo normal con cadena de pensamiento vacía
// (coincide con 3 grupos de capturas en estado conectado, 8 pruebas negativas). Por ello, los modelos con esto se comportan como 3.1 Pro:
// no se exponen sin cookie.
const (
	thinkingNormal   = 1
	thinkingExtended = 2
)

// ModelConfig holds the server-side model id plus the legacy MODE_CATEGORY value.
type ModelConfig struct {
	// HexID va en la cabecera x-goog-ext-525001261-jspb; es el único selector de modelo reconocido por el servidor.
	// Pruebas: si no se envía esta cabecera, cualquier valor de inner[79] entre 1..6 cae en 3.5 Flash-Lite;
	// si la cabecera indica 3.6 y inner[79] indica 6, se obtiene 3.6 — la cabecera tiene prioridad sobre inner[79].
	HexID string
	Mode  int
	Desc  string
	// Si Thinking es true, se establece inner[80]=2 (pensamiento extendido en UI web). Es ortogonal a HexID
	// — funciona con los tres modelos, no es exclusivo de uno.
	Thinking bool
	// Si Tool != 0, se establece inner[49] para que el servidor cambie al modelo backend que genera medios (14=imagen Nano Banana
	// / 21=música Lyria). El resultado no viene en la respuesta de StreamGenerate: hay que llamar a hNvQHb para obtener el enlace de descarga,
	// y con un subconjunto de cookies válidas para el host de descarga obtener los bytes originales (ver media.go). Requiere sesión iniciada: en solicitudes anónimas
	// se degrada silenciosamente a un texto "Are you signed in?".
	Tool int
}

// Solo se exponen modelos que realmente existen en el listado del servidor (batchexecute?rpcids=otAQ7b).
// Los alias antiguos gemini-3.5-flash / -thinking / -thinking-lite / gemini-auto /
// gemini-flash-lite se eliminaron: no tienen entrada correspondiente en el servidor; mantenerlos induciría a error.
// creyendo que hay cinco modelos distintos disponibles.
var Models = map[string]ModelConfig{
	"gemini-3.6-flash":      {HexID: hexFlash36, Mode: 1, Desc: "Latest all-around model"},
	"gemini-3.5-flash-lite": {HexID: hexFlashLite, Mode: 6, Desc: "Fastest, lightweight"},
	"gemini-3.1-pro":        {HexID: hexPro31, Mode: 3, Desc: "Most capable; needs a signed-in cookie (downgraded to Flash-Lite without one)"},
	// 3.8 Flash: Google actualizó in situ el hex de 3.7 (mismo hex; el nombre mostrado pasó de 3.7 a 3.8).
	// En pruebas es **exclusivo de cuentas de pago** (no por gradación): solo cuentas de pago Gemini lo tienen; cuentas gratuitas se degradan a 3.5 Flash-Lite.
	// gemini-3.7-flash se mantiene como alias (mismo hex).
	"gemini-3.8-flash": {HexID: hexFlash38, Mode: 1, Desc: "3.8 Flash; needs a signed-in PAID Google account (free accounts get downgraded to 3.5 Flash-Lite)"},
	"gemini-3.7-flash": {HexID: hexFlash38, Mode: 1, Desc: "alias of gemini-3.8-flash (same hex; Google renamed 3.7→3.8)"},

	// Versión con pensamiento extendido. inner[80]=2 es ortogonal al hex del modelo y se puede activar con cualquiera; pero solo es efectivo con sesión iniciada,
	// por lo que, igual que 3.1 Pro, no se expone sin cookie.
	"gemini-3.6-flash-thinking":      {HexID: hexFlash36, Mode: 1, Thinking: true, Desc: "3.6 Flash with extended thinking; needs a signed-in cookie"},
	"gemini-3.5-flash-lite-thinking": {HexID: hexFlashLite, Mode: 6, Thinking: true, Desc: "3.5 Flash-Lite with extended thinking; needs a signed-in cookie"},
	"gemini-3.1-pro-thinking":        {HexID: hexPro31, Mode: 3, Thinking: true, Desc: "3.1 Pro with extended thinking; needs a signed-in cookie"},
	"gemini-3.8-flash-thinking":      {HexID: hexFlash38, Mode: 1, Thinking: true, Desc: "3.8 Flash with extended thinking; needs a signed-in cookie"},
	"gemini-3.7-flash-thinking":      {HexID: hexFlash38, Mode: 1, Thinking: true, Desc: "alias of gemini-3.8-flash-thinking"},

	// Generación de medios. Al establecer inner[49], el servidor cambia al modelo backend para generar imagen/audio; los archivos se obtienen vía hNvQHb + host de descarga.
	// Se recuperan e insertan como data URL base64 en content. Requieren sesión iniciada; sin cookie no se exponen.
	"gemini-image": {HexID: hexFlash36, Mode: 1, Tool: toolImage, Desc: "Image generation (Nano Banana); returns a base64 data URL; needs a signed-in cookie"},
	"gemini-music": {HexID: hexFlash36, Mode: 1, Tool: toolMusic, Desc: "Music generation (Lyria, ~30s); returns a base64 data URL; needs a signed-in cookie"},
	"gemini-video": {HexID: hexFlash36, Mode: 1, Tool: toolVideo, Desc: "Video generation (Veo, async); returns a base64 data URL; needs a signed-in cookie (usually a paid account)"},
	// Canvas: genera un documento HTML interactivo inmersivo, devuelto inline (no binario, no requiere descarga). Requiere sesión iniciada.
	"gemini-canvas": {HexID: hexFlash36, Mode: 1, Tool: toolCanvas, Desc: "Canvas: generates an interactive HTML document (returned inline as a ```html block); needs a signed-in cookie"},
}

// hasCookie indica si hay cuentas disponibles en el pool de cookies. Determina si 3.1 Pro aparece en la lista de modelos.
func hasCookie() bool {
	_, enabled := accountCount()
	return enabled > 0
}

// modelNeedsLogin determina si un modelo requiere sesión iniciada para funcionar realmente. En solicitudes anónimas, estos modelos
// se degradan silenciosamente: 3.1 Pro / 3.8 Flash → 3.5 Flash-Lite; desaparece la cadena de pensamiento; herramientas multimedia devuelven
// el texto "Are you signed in?". Este es el criterio único para decidir qué excluir sin cookie y para el caso "prioridad anónima" (#20)
// sobre si debe ocupar una cuenta del pool.
//
// Se evalúa por HexID + Thinking + Tool; equivale al criterio anterior basado en nombres (3.1-pro/3.8-flash/3.7-flash)
// (los hex son hexPro31/hexFlash38); el resto de modelos que requieren login quedan cubiertos por Thinking o Tool.

func modelNeedsLogin(mc ModelConfig) bool {
	return mc.HexID == hexPro31 || mc.HexID == hexFlash38 || mc.Thinking || mc.Tool > 0
}

// anonFirstEligible determina si una solicitud puede usar ruta anónima (sin ocupar cuenta) cuando está activada la opción "prioridad anónima":
// activado + sin adjuntos + modelo no requiere login. Los adjuntos (imagen/vídeo) requieren sesión iniciada en el contexto;
// solicitudes anónimas con adjuntos devuelven 1100 desde upstream, por lo que con adjuntos siempre se elige una cuenta. Si está desactivado, devuelve false (comportamiento anterior:
// si hay cuentas, se usa una).
func anonFirstEligible(mc ModelConfig, hasAttachment bool) bool {
	return rtCfg().AnonFirst && !hasAttachment && !modelNeedsLogin(mc)
}

// availableModels devuelve los modelos a exponer según la configuración actual.
//
// Sin cookies, se excluye 3.1 Pro: en pruebas, solicitudes anónimas lo degradan silenciosamente a 3.5 Flash-Lite;
// el cliente cree erróneamente que usa Pro. Mejor no exponerlo y que falle en la selección.
//
// Con cookies válidas sí funciona: tras 6 llamadas consecutivas el servidor devuelve "3.1 Pro"
// y cada respuesta incluye cadena de pensamiento (118-152 caracteres; 3.6 Flash normal devuelve 0).
// Las observaciones anteriores sobre cuentas gratuitas quedando en 3.6 Flash con thinking se dieron sin token XSRF; en ese caso las solicitudes con cookie ni siquiera se enviaban (ver xsrf.go).

func availableModels() map[string]ModelConfig {
	if hasCookie() {
		return Models
	}
	out := make(map[string]ModelConfig, len(Models))
	for k, v := range Models {
		if modelNeedsLogin(v) {
			continue
		}
		out[k] = v
	}
	return out
}

// resolveModel mapea un nombre de modelo a su configuración.
//
// El sufijo "name@think=N" se elimina e ignora. Versiones antiguas lo escribían en inner[17] como profundidad de pensamiento,
// pero las capturas muestran que inner[17] es el índice de turno dentro de la sesión (primer turno [[0]], segundo con ID de sesión [[1]], incrementando)
// — no guarda relación con la profundidad de pensamiento. Como siempre abrimos nueva sesión, este valor es 0.
// Se ignora sin error para mantener compatibilidad con clientes que lo usen.
func resolveModel(modelName string) (string, ModelConfig, error) {
	if idx := strings.Index(modelName, "@think="); idx >= 0 {
		modelName = modelName[:idx]
	}
	mc, ok := availableModels()[modelName]
	if !ok {
		if full, exists := Models[modelName]; exists && !hasCookie() {
			downgrade := "se degradan silenciosamente a 3.5 Flash-Lite"
			if full.Tool > 0 {
				downgrade = "se degradan silenciosamente a una respuesta de texto sin más «Are you signed in?»"
			}
			return "", ModelConfig{}, fmt.Errorf(
				"%s no está disponible sin cookie de cuenta Google: las solicitudes anónimas "+
					"%s. Añade una cookie desde el panel de administración (pool de cookies) o mediante --cookie-file para habilitarlo",
				modelName, downgrade)
		}
		return "", ModelConfig{}, fmt.Errorf("modelo desconocido: %s", modelName)
	}
	return modelName, mc, nil
}

// StreamResult holds raw body + per-request proxy + timing info.
type StreamResult struct {
	// Emitted: texto ya enviado por onDelta en modo streaming; vacío en modo no-streaming.
	Emitted string
	Raw     string
	// Reasoning: cadena de pensamiento del modelo (solo 3.1 Pro la produce).
	// El upstream la envía siempre; anteriormente solo se tomaba el texto final y se descartaba esta cadena.
	Reasoning string
	// EmittedReasoning: cadena de pensamiento ya enviada por onReasoning en streaming; vacío en no-streaming.
	EmittedReasoning string
	// UpstreamModel: nombre de modelo reportado por el servidor en la trama de respuesta [42].
	// Puede diferir del modelo solicitado: gemini-3.1-pro en modo anónimo se degrada silenciosamente a 3.5 Flash-Lite;
	// por eso es importante conservar este campo.
	UpstreamModel string
	ProxyID       int64
	ProxyName     string
	// AccountID: cuenta usada del pool de cookies (0 = anónimo). También se registra en solicitudes fallidas;
	// al diagnosticar fallos generalizados con cookies, es fundamental saber qué cuenta se usó.
	AccountID    int64
	AccountLabel string
	TTFBMs       int64
	TotalMs      int64
	// Artifacts: bytes originales de los medios generados (imagen/música); vacío si no es modelo multimedia.
	Artifacts []MediaArtifact
	// MediaErr: registra fallo al recuperar medios; la generación devolvió 200 pero falló hNvQHb/descarga.
	// El llamador debe reportar error, en lugar de devolver un "éxito parcial" con solo texto.
	MediaErr string
}

// RateLimitError indica que todas las ranuras de IP alcanzaron el límite de tasa.
// El handler HTTP devuelve 429 al cliente cuando ve este error.
type RateLimitError struct {
	Reason  string // "concurrent" / "rpm" / "rph"
	ProxyID int64  // 0 = ranura directa llena
}

func (e *RateLimitError) Error() string {
	if e.ProxyID == 0 {
		return "ranuras IP directas llenas (slot full): " + e.Reason + " (limit reached; configura proxies para escalar)"
	}
	return "todas las ranuras de proxy llenas (slot full): " + e.Reason + " (limit reached)"
}

// acquireSlot selecciona una ranura con capacidad para la solicitud.
// Prioridad: proxy con capacidad en el pool → conexión directa.
// Si todas llenas, devuelve *RateLimitError.
//
// Quien llame debe liberar con releaseSlot() diferido si ok=true.
func acquireSlot(preferProxyID int64) (Proxy, bool, error) {
	// 1. Intentar pool de proxies (si configurado)
	proxyMu.RLock()
	hasProxies := len(proxyCache) > 0
	proxyMu.RUnlock()

	if hasProxies {
		if p, ok := pickProxyPreferring(preferProxyID); ok {
			return p, true, nil
		}
		// Hay proxies en el pool pero ninguno disponible (límite alcanzado / todos deshabilitados / todos en cortocircuito sin enfriamiento).
		// Por defecto **no se retrocede a conexión directa**: usar pool implica no exponer IP directa al upstream;
		// caer silenciosamente a directa rompería este principio y sería difícil de detectar en logs.
		// Para dar prioridad a disponibilidad sobre ocultación de IP, habilita fallback_direct.
		if !rtCfg().FallbackDirect {
			return Proxy{}, false, &RateLimitError{Reason: "rph", ProxyID: -1}
		}
		if ok, reason := trySlotAcquire(0); ok {
			logf("[proxy] el pool de proxies no tiene salida disponible; esta vez se retrocede a conexión directa (fallback_direct activado)")
			return Proxy{}, true, nil
		} else {
			return Proxy{}, false, &RateLimitError{Reason: reason, ProxyID: 0}
		}
	}

	// 2. Sin pool → usar ranura directa (id=0)
	if ok, reason := trySlotAcquire(0); ok {
		return Proxy{}, true, nil // ProxyID=0 = conexión directa
	} else {
		return Proxy{}, false, &RateLimitError{Reason: reason, ProxyID: 0}
	}
}

// Un request prueba como máximo unos pocos cookies. Con un pool grande, probarlos
// todos uno por uno alarga mucho las solicitudes fallidas, y fallar 3 seguidos ya
// indica que el problema es del pool entero y no de una cuenta mala puntual.
const maxCookieTries = 3

// releaseSlot libera la ocupación. proxyID=0 = conexión directa.
func releaseSlot(proxyID int64) {
	slotRelease(proxyID)
}

// deltaTracker convierte los frames acumulativos del upstream en incrementos.
//
// Cada frame del upstream trae el **texto completo hasta ese momento**, no la parte
// nueva, así que hay que compararlo como prefijo contra lo ya emitido. Entre frames
// a veces no se cumple la relación de prefijo (el modelo reescribe, o un artifact
// limpiado cae justo en el borde); en ese caso es mejor saltar que emitir, porque
// emitir equivaldría a empujar contenido duplicado al cliente, y lo ya emitido no
// se puede recuperar. La parte que falte la completa el llamador al final con remainingText.
type deltaTracker struct{ emitted string }

// Push ingiere el texto acumulado de un frame y devuelve el incremento respecto a
// la vez anterior; devuelve "" si no hay nada nuevo o no se puede diffear de forma segura.
func (d *deltaTracker) Push(fullText string) string {
	cleaned := cleanGeminiText(fullText)
	if len(cleaned) <= len(d.emitted) || !strings.HasPrefix(cleaned, d.emitted) {
		return ""
	}
	delta := cleaned[len(d.emitted):]
	d.emitted = cleaned
	return delta
}

// streamGenerate POSTs to Gemini's StreamGenerate endpoint and returns raw body
// plus proxy/timing telemetry for the metrics layer.
// The 80-slot inner array is verbatim from the Python reference.
// onDelta distinto de nil activa el streaming real: cada vez que el upstream
// escribe un frame se parsea, se hace diff de prefijo contra lo ya emitido y se
// devuelve de inmediato la parte nueva por callback. Cada frame del upstream trae
// texto acumulado, no incrementos, así que el diff hay que hacerlo uno mismo. Una
// vez que ya se emitió contenido no se reintenta: el reintento haría que el cliente
// reciba texto duplicado.
func streamGenerate(prompt, latest string, mc ModelConfig,
	onDelta, onReasoning func(string)) (*StreamResult, error) {
	return streamGenerateWithFiles(prompt, latest, mc, nil, onDelta, onReasoning)
}

// fileRef es la referencia a un adjunto ya subido.
type fileRef struct {
	Ref  string // ruta devuelta por la subida, con la forma /contrib_service/ttl_1d/…
	Name string // nombre de archivo que se muestra al modelo
	Kind int    // tipo de adjunto: 1=imagen, 3=texto/archivo normal
	Mime string // tipo de contenido; el servidor decide con él cómo parsear el adjunto
}

// streamGenerateWithFiles es igual al anterior, pero admite adjuntos.
//
// Los adjuntos se colocan en inner[0][3], con la forma [[[ref, 1], "nombre de archivo"], …].
// Los adjuntos solo funcionan con sesión iniciada: de forma anónima se puede subir
// el archivo, pero en cuanto se referencia en la conversación el servidor responde 1100.
func streamGenerateWithFiles(prompt, latest string, mc ModelConfig, pending []pendingUpload,
	onDelta, onReasoning func(string)) (*StreamResult, error) {
	var files []fileRef

	// Primero se elige la cuenta y luego, según el proxy que esa cuenta tenía atado
	// la última vez, se elige la salida —— una misma cuenta debe salir en lo posible
	// siempre desde la misma IP; de otro modo, saltar entre decenas de salidas es
	// para Google un rasgo de cuenta compartida.
	//
	// Elegir la cuenta antes de acquireSlot no viola «obtener el XSRF debe pasar por
	// la salida oficial»: elegir la cuenta solo lee la base y no envía peticiones;
	// quien realmente envía es el getXSRF de abajo, después de obtener la ranura.
	// #20 Anónimo primero: cuando no se necesita capacidad con sesión, no se ocupa una
	// cuenta de cookie y se va anónimo para ahorrar cuota (ver anonFirstEligible).
	// Con adjuntos (imagen/vídeo) no se puede ir anónimo —— el bloque uploadBytes de
	// abajo fallará directamente porque cookieStr==""
	var acct *CookieAccount
	if !anonFirstEligible(mc, len(pending) > 0) {
		if a, ok := pickCookieAccount(); ok {
			acct = a
		}
	}
	preferProxy := int64(0)
	if acct != nil {
		preferProxy = acct.ProxyID
	}

	// En caso de error también hay que devolver «qué cuenta / qué salida se usó»;
	// si no, los registros de fallo quedan en blanco. picked se rellena tras obtener
	// la ranura; la clausura lo captura y cada attrib incluye la salida vigente en ese momento.
	var picked Proxy
	var cookieID int64
	var cookieLabel string
	attrib := func(err error) (*StreamResult, error) {
		return &StreamResult{
			AccountID: cookieID, AccountLabel: cookieLabel,
			ProxyID: picked.ID, ProxyName: picked.Name,
		}, err
	}

	// Se pide una ranura al limitador (proxy o conexión directa). Todas llenas → 429 directo.
	p, slotOK, slotErr := acquireSlot(preferProxy)
	if !slotOK {
		return attrib(slotErr)
	}
	picked = p
	defer releaseSlot(picked.ID) // picked.ID=0 = ranura directa

	// picked.URL vacío = ranura directa. El proxy solo tiene el pool como entrada, ya no
	// hay otra salida de respaldo (el antiguo campo «proxy estático» se integró en el pool, ver seedProxiesFromConfig).
	proxyURL := picked.URL
	pickedOK := picked.ID > 0 // si se usó realmente un proxy del pool

	// endpoint no se puede componer hasta fijar la salida: currentBL puede lanzar de paso
	// una captura en segundo plano, y esa captura debe usar la misma salida que la petición
	// oficial; si no, aun con pool de proxies se filtra una vez desde la IP local.
	reqid := time.Now().Unix() % 1000000
	endpoint := fmt.Sprintf(
		"https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?bl=%s&hl=en&_reqid=%d&rt=c",
		currentBL(proxyURL), reqid,
	)

	// Obtener el token XSRF. Que una cookie caduque no debe hacer fallar toda la
	// petición: si la cuenta actual no lo consigue, se pasa a la siguiente, hasta
	// maxCookieTries intentos. Si no, con 2 cuentas en el pool y 1 mala se caerían
	// cerca de la mitad de las peticiones, y cada fallo parecería un problema del
	// upstream; el usuario solo vería "una tasa de éxito raramente baja".
	//
	// Cambiar de cuenta **no cambia la salida**: la salida ya se eligió según el
	// vínculo de la primera cuenta, y no tiene sentido cambiarla dentro de la misma petición.
	cookieStr, sapisid, xsrfToken := "", "", ""
	var lastCookieErr error
	tried := map[int64]bool{}
	// A cada cuenta se le da una sola oportunidad de «reintentar tras rotación», para
	// no golpear accounts.google.com repetidamente dentro de una misma petición.
	rotatedOnce := map[int64]bool{}
	for acct != nil && len(tried) < maxCookieTries {
		tried[acct.ID] = true
		// Registrar primero la atribución: aunque este intento falle queda constancia,
		// y en el panel se ve qué cuenta está fallando.
		cookieID, cookieLabel = acct.ID, accountDisplayName(acct)
		tok, err := getXSRF(acct.Cookie, proxyURL)
		if err == nil {
			cookieStr, sapisid, xsrfToken = acct.Cookie, extractSAPISID(acct.Cookie), tok
			// Escribir el vínculo solo cuando «nunca se vinculó» o «la salida vinculada ya no existe».
			//
			// Nunca sobrescribir solo porque "esta vez se salió por otra salida": la salida se eligió
			// según el vínculo de **la primera cuenta elegida en esta petición**, y la elección de
			// cuenta puede cambiar (las cuentas nuevas con last_used_at=0 van primero, y al toparse
			// con una mala se cambia). Sobrescribir el vínculo de la cuenta actual con la salida de
			// otra equivale a deshacer la afinidad de una cuenta buena cada vez que se topa con una
			// mala —— en pruebas, así se deshacía.
			if acct.ProxyID == 0 || !proxyUsableByID(acct.ProxyID) {
				bindAccountProxy(acct.ID, picked.ID)
			}
			break
		}
		// No obtener SNlM0e equivale básicamente a que esta cookie caducó (la página nos trata como anónimos).
		// Antes de cambiar de cuenta se le da una oportunidad: forzar una rotación y reintentar.
		//
		// La rotación reemite __Secure-1PSIDTS (el ticket que caduca en unos 30 minutos) y fusiona *SIDCC.
		// Las cuentas cuyo ticket caducó pero cuya identidad persistente sigue viva se recuperan así;
		// si no, se cambia de cuenta. Solo se intenta una vez, y solo en esta ronda.
		if !rotatedOnce[acct.ID] {
			rotatedOnce[acct.ID] = true
			if _, _, rerr := rotateAccount(*acct); rerr == nil {
				if fresh := accountByID(acct.ID); fresh != nil {
					if tok2, err2 := getXSRF(fresh.Cookie, proxyURL); err2 == nil {
						logf("[cookie] la cuenta #%d se recuperó tras la rotación", acct.ID)
						acct = fresh
						cookieStr, sapisid, xsrfToken = fresh.Cookie, extractSAPISID(fresh.Cookie), tok2
						if fresh.ProxyID == 0 || !proxyUsableByID(fresh.ProxyID) {
							bindAccountProxy(fresh.ID, picked.ID)
						}
						break
					}
				}
			}
		}
		// No se pudo recuperar: registrar un fallo para que en el panel se vea qué cuenta
		// hay que reemplazar, y pasar a la siguiente.
		markCookieByStatus(acct.ID, 401, err.Error())
		lastCookieErr = err
		logf("[cookie] la cuenta #%d no sirve, se pasa a la siguiente: %v", acct.ID, err)
		acct, _ = pickCookieAccountExcept(tried) // devuelve nil si no encuentra; el bucle termina solo
	}
	if lastCookieErr != nil && cookieStr == "" {
		if !rtCfg().FallbackAnon {
			// Por defecto se error en vez de degradar: cuando una cookie caduca, el upstream
			// no rechaza, solo te trata como anónimo, y una petición de solo texto responde 200
			// —— así 3.1 Pro se degrada silenciosamente a 3.5 Flash-Lite, desaparece la cadena
			// de pensamiento, y el cliente no lo ve. Mejor fallar claramente que dar un falso éxito.
			return attrib(fmt.Errorf("las %d cuentas del pool de cookies no sirven (la última: %w);"+
				"revisa una por una con el botón «Detectar» del panel «Pool de cookies», o activa fallback_anon para degradar a anónimo",
				len(tried), lastCookieErr))
		}
		logf("[cookie] las %d cuentas probadas no sirven; esta vez se degrada a anónimo (la capacidad baja al nivel anónimo)", len(tried))
		cookieID, cookieLabel = 0, ""
	}
	// Adjuntos de imagen: la subida requiere cookie y debe salir por la misma salida que
	// la petición oficial, por eso va aquí.
	if len(pending) > 0 {
		if cookieStr == "" {
			return attrib(fmt.Errorf("image input needs a Google account cookie: " +
				"anonymous uploads succeed but referencing them in a conversation is " +
				"rejected upstream. Add a cookie in the admin panel (Cookie pool)"))
		}
		nImg, nVid := 0, 0
		for _, u := range pending {
			ref, uerr := uploadBytes(cookieStr, proxyURL, u.Data, u.Name)
			if uerr != nil {
				return attrib(fmt.Errorf("falló la subida del adjunto %s: %w", u.Name, uerr))
			}
			files = append(files, fileRef{Ref: ref, Name: u.Name, Kind: u.Kind, Mime: u.Mime})
			if u.Kind == 2 {
				nVid++
			} else {
				nImg++
			}
		}
		logf("[vision] se subieron %d imágenes / %d vídeos", nImg, nVid)
	}

	// Si el prompt es demasiado largo, se convierte en adjunto de texto. Hay que esperar
	// a elegir cuenta y salida: la subida requiere cookie y debe salir por la misma salida
	// que la petición oficial.
	budget := rtCfg().MaxPromptBytes
	if p, f, used, ferr := prepareContextFile(prompt, latest, budget, cookieStr, proxyURL); ferr != nil {
		return attrib(ferr)
	} else if used {
		prompt = p
		files = append(files, f...)
	}
	if budget > 0 && len(prompt) > budget {
		return attrib(&PromptTooLongError{
			Bytes: len(prompt), Budget: budget, HasCookie: cookieStr != "",
		})
	}

	inner := make([]interface{}, innerSlots)
	if len(files) > 0 {
		// La forma está tomada literalmente de una captura del navegador:
		//   [[[ruta, tipo, null, mime], "nombre de archivo", null×6, [0]], …]
		// El bit de tipo es 1=imagen / 3=archivo de texto —— pasar un archivo de texto
		// con tipo 1 equivale a decirle al servidor "esto es una imagen".
		refs := make([]interface{}, 0, len(files))
		for _, f := range files {
			kind := f.Kind
			if kind == 0 {
				kind = 3
			}
			mime := f.Mime
			if mime == "" {
				mime = "text/plain"
			}
			refs = append(refs, []interface{}{
				[]interface{}{f.Ref, kind, nil, mime}, f.Name,
				nil, nil, nil, nil, nil, nil,
				[]interface{}{0},
			})
		}
		inner[0] = []interface{}{prompt, 0, nil, refs, nil, nil, 0}
	} else {
		inner[0] = []interface{}{prompt, 0, nil, nil, nil, nil, 0}
	}
	inner[1] = []interface{}{"en"}
	inner[2] = []interface{}{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	inner[6] = []interface{}{0}
	inner[7] = 1
	inner[10] = 1
	inner[11] = 0
	// Índice de turno dentro de la sesión; como siempre abrimos una sesión nueva, es 0.
	inner[17] = []interface{}{[]interface{}{0}}
	inner[18] = 0
	inner[27] = 1
	inner[30] = []interface{}{4}
	// En las capturas, los tres escenarios del navegador (con cookie / sin cookie / pensamiento extendido) son siempre [1].
	// Nosotros escribíamos [2], un valor copiado en una etapa temprana y desmentido a nivel de protocolo. Su significado sigue sin conocerse;
	// con anónimo pasan ambos valores, pero no hay motivo para seguir desviándose del navegador.
	inner[41] = []interface{}{1}
	inner[53] = 0
	reqUUID := uuid.NewString()
	inner[59] = reqUUID
	inner[61] = []interface{}{}
	inner[68] = 1
	inner[79] = mc.Mode
	inner[80] = thinkingNormal
	inner[91] = 0
	inner[96] = 0
	if mc.Thinking {
		inner[80] = thinkingExtended
		inner[96] = 1
	}
	// Interruptor de herramienta de medios. Si se rellena, el servidor cambia al modelo
	// backend que genera imagen/audio (la respuesta trae la referencia al archivo; los bytes hay que pedirlos aparte).
	if mc.Tool > 0 {
		inner[49] = mc.Tool
	}
	// Para vídeo hay que indicar además la relación de aspecto en inner[55]: [[16]]=16:9, [[17]]=9:16 (según captura).
	if mc.Tool == toolVideo {
		inner[55] = []interface{}{[]interface{}{16}}
	}

	innerJSON, err := json.Marshal(inner)
	if err != nil {
		return nil, err
	}
	outer := []interface{}{nil, string(innerJSON)}
	outerJSON, err := json.Marshal(outer)
	if err != nil {
		return nil, err
	}

	// Con cookie hay que enviar un campo de formulario adicional, at (token XSRF); si no, el upstream responde 400 directo.
	// Las peticiones anónimas no lo necesitan; getXSRF devuelve cadena vacía para cookie vacía. Ver xsrf.go.
	buildBody := func(at string) string {
		form := url.Values{}
		form.Set("f.req", string(outerJSON))
		if at != "" {
			form.Set("at", at)
		}
		return form.Encode()
	}

	body := buildBody(xsrfToken)

	thinkVal := thinkingNormal
	if mc.Thinking {
		thinkVal = thinkingExtended
	}
	// En las capturas, el índice 16 de la cabecera y inner[59] son dos uuid distintos, cada uno generado por separado.
	modelHeader := buildModelHeader(mc.HexID, mc.Mode, thinkVal, uuid.NewString())
	sessionHeader := fmt.Sprintf(`["%s",1]`, reqUUID)

	geminiHeaders := buildGeminiHeaders(cookieStr, sapisid, mc.HexID)
	geminiHeaders["x-goog-ext-525001261-jspb"] = modelHeader
	geminiHeaders["x-goog-ext-525005358-jspb"] = sessionHeader
	var lastErr error
	// Último código de estado HTTP obtenido; 0 indica que falló la capa de red y no hubo respuesta.
	// La salud de la cookie solo considera 401/403; otros estados no son culpa de la cookie, ver markCookieByStatus.
	lastStatus := 0
	xsrfRetried := false // la autocuración de XSRF se hace una sola vez, para evitar bucles infinitos
	t0 := time.Now()

	tracker := &deltaTracker{}
	rtracker := &deltaTracker{}
	var lineCB func(string)
	if onDelta != nil || onReasoning != nil {
		lineCB = func(line string) {
			// La cadena de pensamiento se emite por completo antes de que llegue el cuerpo,
			// así que se procesa primero para que el cliente reciba el orden correcto.
			if onReasoning != nil {
				if r := reasoningInLine(line); r != "" {
					if d := rtracker.Push(r); d != "" {
						onReasoning(d)
					}
				}
			}
			if onDelta == nil {
				return
			}
			for _, t := range textsInLine(line) {
				if d := tracker.Push(t); d != "" {
					onDelta(d)
				}
			}
		}
	}

	for attempt := 0; attempt < rtCfg().RetryAttempts; attempt++ {
		statusCode, raw, ttfb, setCookie, err := doGeminiRequest(endpoint, body, geminiHeaders, proxyURL, lineCB)
		if len(setCookie) > 0 && cookieID > 0 {
			if merged := mergeSetCookie(cookieStr, setCookie); merged != cookieStr {
				cookieStr = merged
				updateAccountCookie(cookieID, merged)
			}
		}
		if err != nil {
			lastErr = err
			if pickedOK {
				recordProxyResult(picked.ID, false, err.Error())
			}
			// Una vez emitido contenido al cliente no se puede reintentar, o se duplicaría
			// (la cadena de pensamiento también cuenta como emitida).
			if tracker.emitted != "" || rtracker.emitted != "" {
				break
			}
			if attempt < rtCfg().RetryAttempts-1 {
				logf("retry %d/%d: %v", attempt+1, rtCfg().RetryAttempts, err)
				time.Sleep(time.Duration(rtCfg().RetryDelaySec) * time.Second)
			}
			continue
		}
		if statusCode != 200 {
			// Cuando el token caduca, el upstream responde 400 + xsrf. Se invalida la caché,
			// se reobtiene una vez y se vuelve a probar; esta autocuración no cuenta en el
			// presupuesto de reintentos, o una sola caducidad se comería todos los reintentos.
			if statusCode == 400 && isXSRFError(string(raw)) && cookieStr != "" && !xsrfRetried {
				xsrfRetried = true
				invalidateXSRF(cookieStr)
				if tok, e := getXSRF(cookieStr, proxyURL); e == nil {
					body = buildBody(tok)
					geminiHeaders = buildGeminiHeaders(cookieStr, sapisid, mc.HexID)
					geminiHeaders["x-goog-ext-525001261-jspb"] = modelHeader
					geminiHeaders["x-goog-ext-525005358-jspb"] = sessionHeader
					attempt--
					continue
				}
			}
			lastErr = fmt.Errorf("upstream HTTP %d: %s", statusCode, truncate(string(raw), 200))
			lastStatus = statusCode
			if pickedOK {
				recordProxyResult(picked.ID, false, lastErr.Error())
			}
			if tracker.emitted != "" || rtracker.emitted != "" {
				break
			}
			if attempt < rtCfg().RetryAttempts-1 {
				time.Sleep(time.Duration(rtCfg().RetryDelaySec) * time.Second)
			}
			continue
		}
		// HTTP 200 pero sin ninguna trama de contenido —— rechazo transitorio del upstream (ese 1155 de la respuesta).
		// No es limitación de tasa: desde una IP limpia, 15 peticiones seguidas con 1s de intervalo pasan todas; 10 en
		// paralelo desde la misma IP, 18 en total, pasan todas; una IP que ya hizo 60+ peticiones igual tiene éxito;
		// no hay umbral predecible y la misma petición unas veces va y otras no.
		// Reenviar una vez suele bastar, así que hay que incluirlo en los reintentos —— si no, un solo parpadeo se
		// convierte en un 502 visible para el cliente.
		//
		// El criterio es **si hay trama de contenido**, no BardErrorInfo: la trama final de una respuesta normal
		// también trae un código de error (1096 = sesión no persistida); juzgar por él marcaría como fallo toda respuesta normal.
		if !hasContentFrame(string(raw)) {
			lastErr = fmt.Errorf("el upstream no devolvió trama de contenido (no content frame, raw %d bytes)", len(raw))
			if pickedOK {
				// Registrar en la salud del proxy: el 1155 está muy correlacionado con la calidad de la salida
				// (salida limpia: 0 casos en 60+; salida sucia: ~9 al día); encadenar varios indica que esa salida debe descansar.
				recordProxyResult(picked.ID, false, lastErr.Error())
			}
			if tracker.emitted != "" || rtracker.emitted != "" {
				break
			}
			if attempt < rtCfg().RetryAttempts-1 {
				logf("retry %d/%d: respuesta vacía (sin trama de contenido, %d bytes)", attempt+1, rtCfg().RetryAttempts, len(raw))
				time.Sleep(time.Duration(rtCfg().RetryDelaySec) * time.Second)
			}
			continue
		}
		if pickedOK {
			recordProxyResult(picked.ID, true, "")
		}
		markCookieByStatus(cookieID, 200, "")
		result := &StreamResult{
			Emitted:          tracker.emitted,
			EmittedReasoning: rtracker.emitted,
			Raw:              string(raw),
			Reasoning:        extractReasoning(string(raw)),
			UpstreamModel:    extractUpstreamModel(string(raw)),
			ProxyID:          picked.ID,
			ProxyName:        picked.Name,
			AccountID:        cookieID,
			AccountLabel:     cookieLabel,
			TTFBMs:           ttfb,
			TotalMs:          time.Since(t0).Milliseconds(),
		}
		// Modelos de medios: los bytes del archivo generado no vienen en esta respuesta; hay que
		// repetir el recorrido hNvQHb + host de descarga con el mismo par cookie / salida.
		// Si no se obtienen, se registra MediaErr para que la capa superior error en vez de devolver un producto a medias.
		if mc.Tool == toolImage || mc.Tool == toolMusic || mc.Tool == toolVideo {
			mime := "image/png"
			switch mc.Tool {
			case toolMusic:
				mime = "audio/mpeg"
			case toolVideo:
				mime = "video/mp4"
			}
			arts, aerr := fetchMediaArtifacts(
				mc.Tool, string(raw), extractConversationID(string(raw)),
				cookieStr, sapisid, xsrfToken, proxyURL, mime)
			if aerr != nil {
				logf("[media] falló la recuperación del archivo: %v", aerr)
				result.MediaErr = aerr.Error()
			} else {
				result.Artifacts = arts
				logf("[media] se recuperaron %d archivos", len(arts))
			}
		}
		// #19 Borrado automático de conversación: tras producir el resultado, se borra la conversación
		// que quedó en gemini.google.com, para no dejar un montón acumulado en la cuenta del usuario.
		// Solo se puede borrar con sesión iniciada (requiere XSRF); asíncrono best-effort, no afecta la respuesta.
		if rtCfg().AutoDeleteConversation && cookieStr != "" && xsrfToken != "" {
			if cid := extractConversationID(string(raw)); cid != "" {
				go deleteConversation(cid, cookieStr, sapisid, xsrfToken, proxyURL)
			}
		}
		return result, nil
	}
	if lastErr != nil {
		markCookieByStatus(cookieID, lastStatus, lastErr.Error())
	}
	return attrib(lastErr)
}

// upstreamModelRe coincide con el nombre de modelo que el servidor se atribuye en la trama de respuesta (posición [42] de la trama).
// Con la forma ...,"fbb127bbb056c959",null,null,"3.6 Flash",true,...
var upstreamModelRe = regexp.MustCompile(`\\"[0-9a-f]{16}\\",null,null,\\"([^"\\]{1,40})\\"`)

// extractUpstreamModel toma el nombre del modelo que el servidor usó realmente; devuelve cadena vacía si no lo encuentra.
func extractUpstreamModel(raw string) string {
	m := upstreamModelRe.FindAllStringSubmatch(raw, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// buildModelHeader compone x-goog-ext-525001261-jspb; la forma se toma ranura por ranura de una captura:
//
//	[1,null,null,null,"<hex>",null,null,0,[4,5,6,8],null,null,1,null,null,<mode>,<think>,"<uuid>"]
//	índice              4                8                          14      15       16
//
// El índice 14 tiene el mismo valor que inner[79] y el índice 15 el mismo que inner[80] —— el modelo y
// el modo de pensamiento se guardan por separado tanto en la cabecera como en el payload. **El servidor
// hace caso a la cabecera**: rellenando solo inner[80]=2 y dejando la cabecera en su forma mínima, los
// tres modelos dan cadenas de pensamiento de 0 caracteres en pruebas; es la misma regla que en la
// selección de modelo, "la cabecera pesa más que inner[79]".
//
// El índice 16 es **otro** uuid, distinto del de inner[59] —— el que coincide con inner[59] es
// x-goog-ext-525005358-jspb. Ambas capturas siguen esta regla; no reutilices el mismo por comodidad.
//
// Si uuid va vacío se vuelve a la forma mínima (la ruta anónima no necesita estas ranuras y enviar menos es más simple).
func buildModelHeader(hexID string, mode, think int, uuid string) string {
	if uuid == "" {
		return fmt.Sprintf(`[1,null,null,null,"%s"]`, hexID)
	}
	return fmt.Sprintf(
		`[1,null,null,null,"%s",null,null,0,[4,5,6,8],null,null,1,null,null,%d,%d,"%s"]`,
		hexID, mode, think, uuid)
}

// buildGeminiHeaders prepara las cabeceras de aplicación necesarias para StreamGenerate.
// hexID determina qué modelo usa el servidor; si va vacío, el servidor recurre siempre a 3.5 Flash-Lite.
func buildGeminiHeaders(cookieStr, sapisid, hexID string) map[string]string {
	h := map[string]string{
		"Accept":          "*/*",
		"Accept-Language": "en-US,en;q=0.9",
		"Content-Type":    "application/x-www-form-urlencoded;charset=UTF-8",
		"Origin":          "https://gemini.google.com",
		"Referer":         "https://gemini.google.com/app",
		"X-Same-Domain":   "1",
		"X-Goog-AuthUser": "0",
		// Estas dos las envía el navegador siempre; sus valores son fijos.
		"x-goog-ext-73010989-jspb": "[0]",
		"x-goog-ext-73010990-jspb": "[0,0,0]",
	}
	if hexID != "" {
		h["x-goog-ext-525001261-jspb"] = buildModelHeader(hexID, 0, 0, "")
	}
	if cookieStr != "" {
		h["Cookie"] = cookieStr
	}
	if sapisid != "" {
		h["Authorization"] = makeSAPISIDHash(sapisid)
	}
	return h
}

// doGeminiRequest envía una petición al endpoint. Si proxyURL no está vacío usa stdlib (soporta socks5/http);
// si está vacío usa tls-client (huella real de chrome146). Devuelve (estado HTTP, bytes del cuerpo, err).
// El valor de retorno añade setCookie: el servidor renueva SIDCC / __Secure-1PSIDCC /
// __Secure-3PSIDCC en casi cada respuesta; el navegador las recibe y las reenvía. Un cliente que
// siempre envía valores viejos es tratado como sesión caducada y, en pruebas, la cuenta deja de
// servir en una o dos horas —— por eso hay que recogerlas y reescribirlas en la cuenta.
func doGeminiRequest(endpoint, body string, headers map[string]string, proxyURL string,
	onLine func(string)) (int, []byte, int64, []string, error) {
	sendAt := time.Now()
	if proxyURL != "" {
		// Se usa http.ProxyURL de stdlib; se sabe que pasa socks5/socks5h.
		req, err := http.NewRequest("POST", endpoint, strings.NewReader(body))
		if err != nil {
			return 0, nil, 0, nil, err
		}
		applyChromeHeaders(req)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		client := getStdlibClient(proxyURL)
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, 0, nil, err
		}
		defer resp.Body.Close()
		raw, ttfb, err := readBody(resp.Body, onLine, sendAt)
		if err != nil {
			return resp.StatusCode, nil, ttfb, resp.Header.Values("Set-Cookie"), err
		}
		return resp.StatusCode, raw, ttfb, resp.Header.Values("Set-Cookie"), nil
	}

	// Conexión directa → tls-client, conservando la huella real de chrome146 TLS/HTTP2
	req, err := fhttp.NewRequest("POST", endpoint, strings.NewReader(body))
	if err != nil {
		return 0, nil, 0, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := getTLSClient()
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, 0, nil, err
	}
	defer resp.Body.Close()
	raw, ttfb, err := readBody(resp.Body, onLine, sendAt)
	if err != nil {
		return resp.StatusCode, nil, ttfb, resp.Header.Values("Set-Cookie"), err
	}
	return resp.StatusCode, raw, ttfb, resp.Header.Values("Set-Cookie"), nil
}

// readBody lee todo el cuerpo de la respuesta y lo devuelve tal cual; si onLine no es nil, llama una vez por cada línea leída,
// para que la capa superior pueda reenviar al cliente mientras el upstream aún no ha terminado de escribir.
//
// Siempre se recorre línea por línea (en lugar de usar io.ReadAll cuando onLine==nil, por comodidad),
// porque hay que obtener **el instante en que llega la primera línea** como TTFB. Con ReadAll se
// devuelve solo tras leer todo, y el "tiempo hasta el primer byte" medido sería en realidad el tiempo
// total, siempre igual al tiempo total.
//
// start debe ser **el instante anterior a enviar la petición**, pasado por quien llama. Poner aquí un
// time.Now() es incorrecto: en ese momento client.Do ya ha vuelto, e incluso la cabecera de respuesta y
// parte del cuerpo ya han llegado, así que la medición daría siempre 0.
func readBody(r io.Reader, onLine func(string), start time.Time) ([]byte, int64, error) {
	var buf bytes.Buffer
	sc := bufio.NewScanner(io.TeeReader(r, &buf))
	// Una sola trama puede ser muy grande (en pruebas se han visto respuestas de 400 mil bytes); el límite por defecto de 64KB no basta.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var ttfb int64 = -1
	for sc.Scan() {
		if ttfb < 0 {
			ttfb = time.Since(start).Milliseconds()
		}
		if onLine != nil {
			onLine(sc.Text())
		}
	}
	if ttfb < 0 {
		ttfb = time.Since(start).Milliseconds()
	}
	if err := sc.Err(); err != nil {
		return buf.Bytes(), ttfb, err
	}
	return buf.Bytes(), ttfb, nil
}

// reasoningInLine extrae la cadena de pensamiento de una sola línea wrb.fr.
//
// La posición es inner[4][0][37][0][0], dos niveles más profunda que el cuerpo (inner[4][0][1][0]).
// Solo la produce 3.1 Pro; 3.6 Flash / Flash-Lite siempre la devuelven vacía.
//
// Cronología (medida en un puzle del río): la cadena de pensamiento se acumula hasta 660 caracteres en
// las 5 primeras tramas, cuando el cuerpo aún está vacío; a partir de la trama 6 el cuerpo empieza a
// crecer y la cadena de pensamiento se congela y ya no cambia. Ambas son **el texto completo acumulado**,
// no incrementos, así que el lado de streaming puede reutilizar el diff de prefijo de deltaTracker.
func reasoningInLine(line string) string {
	if !strings.Contains(line, `"wrb.fr"`) || len(line) < 200 {
		return ""
	}
	var arr []interface{}
	if err := json.Unmarshal([]byte(line), &arr); err != nil || len(arr) == 0 {
		return ""
	}
	first, ok := arr[0].([]interface{})
	if !ok || len(first) < 3 {
		return ""
	}
	innerStr, ok := first[2].(string)
	if !ok || len(innerStr) < 50 {
		return ""
	}
	var inner []interface{}
	if err := json.Unmarshal([]byte(innerStr), &inner); err != nil || len(inner) <= 4 {
		return ""
	}
	parts, ok := inner[4].([]interface{})
	if !ok || len(parts) == 0 {
		return ""
	}
	p0, ok := parts[0].([]interface{})
	if !ok || len(p0) <= 37 {
		return ""
	}
	lvl1, ok := p0[37].([]interface{})
	if !ok || len(lvl1) == 0 {
		return ""
	}
	lvl2, ok := lvl1[0].([]interface{})
	if !ok || len(lvl2) == 0 {
		return ""
	}
	s, _ := lvl2[0].(string)
	return s
}

// extractReasoning toma la cadena de pensamiento más larga de toda la respuesta.
// Se toma la más larga y no la última: una vez congelada la cadena de pensamiento, en las
// tramas siguientes esa posición puede faltar o quedar recortada.
//
// Debe pasar por cleanGeminiText igual que extractResponseText: el lado de streaming, deltaTracker,
// emite texto ya limpiado; si aquí no se limpiara, los prefijos de ambos no coincidirían y al
// reenviar el cierre se reenviaría entera la cadena de pensamiento
// (en pruebas el orden de los bloques pasaba a ser R→C→R).
func extractReasoning(raw string) string {
	best := ""
	for _, line := range strings.Split(raw, "\n") {
		if r := reasoningInLine(line); len(r) > len(best) {
			best = r
		}
	}
	return cleanGeminiText(best)
}

// textsInLine extrae los textos de respuesta candidatos de una sola línea wrb.fr.
// El upstream envía **el texto completo acumulado** en cada trama, no incrementos, así que al
// reenviar en streaming hay que hacer uno mismo el diff de prefijos.
func textsInLine(line string) []string {
	if !strings.Contains(line, `"wrb.fr"`) || len(line) < 200 {
		return nil
	}
	var arr []interface{}
	if err := json.Unmarshal([]byte(line), &arr); err != nil || len(arr) == 0 {
		return nil
	}
	first, ok := arr[0].([]interface{})
	if !ok || len(first) < 3 {
		return nil
	}
	innerStr, ok := first[2].(string)
	if !ok || len(innerStr) < 50 {
		return nil
	}
	var inner []interface{}
	if err := json.Unmarshal([]byte(innerStr), &inner); err != nil || len(inner) <= 4 {
		return nil
	}
	parts, ok := inner[4].([]interface{})
	if !ok {
		return nil
	}
	var texts []string
	for _, p := range parts {
		pl, ok := p.([]interface{})
		if !ok || len(pl) < 2 {
			continue
		}
		tl, ok := pl[1].([]interface{})
		if !ok {
			continue
		}
		for _, t := range tl {
			if s, ok := t.(string); ok && s != "" {
				texts = append(texts, s)
			}
		}
	}
	return texts
}

// hasContentFrame determina si la respuesta contiene o no alguna trama de contenido.
//
// A propósito no reutiliza extractResponseText: ese limpia primero los productos de código con
// cleanGeminiText, así que una respuesta que sea puro producto de código le parece vacía, aunque
// el upstream haya entregado contenido perfectamente. Aquí solo se pregunta "¿hay trama o no?",
// se juzga si el enlace tuvo éxito, no si el contenido es aprovechable.
func hasContentFrame(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		if len(textsInLine(line)) > 0 || reasoningInLine(line) != "" {
			return true
		}
	}
	return false
}

// extractResponseText parses StreamGenerate's wrb.fr stream and returns the
// last non-empty text chunk (matches Python extract_response_text behavior).
func extractResponseText(raw string) string {
	var texts []string
	for _, line := range strings.Split(raw, "\n") {
		texts = append(texts, textsInLine(line)...)
	}
	for i := len(texts) - 1; i >= 0; i-- {
		if strings.TrimSpace(texts[i]) != "" {
			cleaned := cleanGeminiText(texts[i])
			if cleaned == "" {
				// Toda la respuesta es producto de ejecución de código (al preguntar por matemáticas/cálculo el modelo
				// ejecuta código directamente), y tras limpiarla queda vacía.
				// No devolver vacío —— si no, callGemini lo tomaría como "sin trama de contenido" y respondería 502,
				// y el usuario vería un fallo de invocación.
				// Alternativa: quitar solo las marcas ?code_reference/stdout, conservar el código y el resultado, y entregarlo como bloque de código normal.
				cleaned = strings.TrimSpace(codeMarkerRe.ReplaceAllString(texts[i], "```$1"))
			}
			return cleaned
		}
	}
	return ""
}

var codeArtifactRe = regexp.MustCompile("(?s)```(?:python|javascript|text)\\?code_(?:reference|stdout)&code_event_index=\\d+\\n.*?```\\n?")

// codeMarkerRe solo coincide con la **marca de apertura del cercado** de un producto de código (```python?code_reference&code_event_index=N),
// para el respaldo cuando tras limpiar queda vacío: degradar la marca a un ```python normal, conservando el código/resultado sin vaciar todo.
var codeMarkerRe = regexp.MustCompile("```(python|javascript|text)\\?code_(?:reference|stdout)&code_event_index=\\d+")

func cleanGeminiText(text string) string {
	return strings.TrimSpace(codeArtifactRe.ReplaceAllString(text, ""))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ProbeResult es el resultado del diagnóstico de conectividad devuelto por la interfaz de prueba de admin.
type ProbeResult struct {
	OK           bool   `json:"ok"`
	Status       string `json:"status"` // "success" / "blocked_sorry" / "rate_limited" / "upstream_error" / "network_error"
	HTTPCode     int    `json:"http_code"`
	TotalMs      int64  `json:"total_ms"`
	ProxyID      int64  `json:"proxy_id"`
	ProxyName    string `json:"proxy_name"`
	UseDirect    bool   `json:"use_direct"`
	ResponseText string `json:"response_text"` // recortado a 200 caracteres
	UpstreamSnip string `json:"upstream_snip"` // primeros 300 caracteres de la respuesta cruda del upstream
	Diagnostic   string `json:"diagnostic"`    // descripción del diagnóstico
	Impersonate  string `json:"impersonate"`
}

// probeGemini llama directamente a Gemini StreamGenerate (saltándose el limitador de tasa) y devuelve un diagnóstico detallado.
// No escribe en la db ni consume una ranura del limitador.
func probeGemini(prompt, proxyURL string) ProbeResult {
	res := ProbeResult{Impersonate: rtCfg().Impersonate}

	inner := make([]interface{}, innerSlots)
	inner[0] = []interface{}{prompt, 0, nil, nil, nil, nil, 0}
	inner[1] = []interface{}{"en"}
	inner[2] = []interface{}{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	inner[6] = []interface{}{0}
	inner[7] = 1
	inner[10] = 1
	inner[11] = 0
	inner[17] = []interface{}{[]interface{}{0}}
	inner[18] = 0
	inner[27] = 1
	inner[30] = []interface{}{4}
	// En las capturas, los tres escenarios del navegador (con cookie / sin cookie / pensamiento extendido) son siempre [1].
	// Nosotros escribíamos [2], un valor copiado en una etapa temprana y desmentido a nivel de protocolo. Su significado sigue sin conocerse;
	// con anónimo pasan ambos valores, pero no hay motivo para seguir desviándose del navegador.
	inner[41] = []interface{}{1}
	inner[53] = 0
	inner[59] = uuid.NewString()
	inner[61] = []interface{}{}
	inner[68] = 1
	probeModel := Models["gemini-3.6-flash"]
	inner[79] = probeModel.Mode

	innerJSON, _ := json.Marshal(inner)
	outer := []interface{}{nil, string(innerJSON)}
	outerJSON, _ := json.Marshal(outer)
	form := url.Values{}
	form.Set("f.req", string(outerJSON))
	body := form.Encode()

	reqid := time.Now().Unix() % 1000000
	endpoint := fmt.Sprintf(
		"https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?bl=%s&hl=en&_reqid=%d&rt=c",
		currentBL(proxyURL), reqid,
	)

	// probe es un sondeo lateral y no reescribe la salud de la cookie: su motivo de fallo no tiene que ver con la cookie.
	// Pero at sí debe incluirse —— de lo contrario, tras poner una cookie, el sondeo de conectividad respondería siempre 400, reportando un fallo falso.
	cookieStr, sapisid := loadCookie()
	if tok, e := getXSRF(cookieStr, proxyURL); e == nil && tok != "" {
		form.Set("at", tok)
		body = form.Encode()
	}
	headers := buildGeminiHeaders(cookieStr, sapisid, probeModel.HexID)

	// Se reutiliza la misma regla de selección de cliente que el flujo principal: con proxy usa stdlib, sin proxy usa tls-client.
	// Pero probe necesita ver la cabecera Location del 302, así que aquí se envía directamente sin usar doGeminiRequest.
	var statusCode int
	var raw []byte
	var locHeader string
	var err error

	if proxyURL != "" {
		req, e := http.NewRequest("POST", endpoint, strings.NewReader(body))
		if e != nil {
			res.Status = "network_error"
			res.Diagnostic = "Error al construir la solicitud: " + e.Error()
			return res
		}
		applyChromeHeaders(req)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		client := getStdlibClient(proxyURL)
		resp, e := client.Do(req)
		if e != nil {
			res.Status = "network_error"
			res.Diagnostic = "Error de capa de red (fallo en DNS/TCP/TLS): " + e.Error()
			return res
		}
		defer resp.Body.Close()
		statusCode = resp.StatusCode
		locHeader = resp.Header.Get("Location")
		raw, err = io.ReadAll(resp.Body)
	} else {
		req, e := fhttp.NewRequest("POST", endpoint, strings.NewReader(body))
		if e != nil {
			res.Status = "network_error"
			res.Diagnostic = "Error al construir la solicitud: " + e.Error()
			return res
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		client := getTLSClient()
		resp, e := client.Do(req)
		if e != nil {
			res.Status = "network_error"
			res.Diagnostic = "Error de capa de red (fallo en DNS/TCP/TLS): " + e.Error()
			return res
		}
		defer resp.Body.Close()
		statusCode = resp.StatusCode
		locHeader = resp.Header.Get("Location")
		raw, err = io.ReadAll(resp.Body)
	}
	_ = err
	res.HTTPCode = statusCode
	res.UpstreamSnip = truncate(string(raw), 300)

	switch {
	case statusCode == 302:
		res.Status = "blocked_sorry"
		res.Diagnostic = "IP bloqueada por el control de riesgos de Google (redirección a sorry/index)." +
			"Normalmente se levanta en 6-24 horas, o cambia de VPN/IP proxy para recuperarte de inmediato. Location: " + truncate(locHeader, 200)
		return res
	case statusCode == 429:
		res.Status = "rate_limited"
		res.Diagnostic = "Google devolvió 429 (límite de tasa). También apunta a la IP, pero es una rama de control de riesgos distinta (ruta básica del SDK)."
		return res
	case statusCode != 200:
		res.Status = "upstream_error"
		res.Diagnostic = fmt.Sprintf("El upstream devolvió un código distinto de 200 (HTTP %d), posible cambio de protocolo o fallo temporal.", statusCode)
		return res
	}

	text := extractResponseText(string(raw))
	if text == "" {
		res.Status = "upstream_error"
		res.Diagnostic = "El upstream devuelve 200 pero solo trama de fin, sin trama de contenido; normalmente el servidor rechazó la solicitud" +
			"(p.ej. ID de sesión no aceptado o conmutador de herramientas); no es un cambio de formato de tramas."
		return res
	}

	res.OK = true
	res.Status = "success"
	res.ResponseText = truncate(text, 200)
	res.Diagnostic = "Llamada correcta. La latencia/contenido está en los campos anteriores."
	return res
}
