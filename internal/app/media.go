package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

// inner[49] interruptor de herramientas multimedia: si se rellena, el servidor cambia a
// un modelo de backend para generar el producto.
const (
	toolImage  = 14 // imagen → Nano Banana
	toolMusic  = 21 // música → Lyria (unos 30 segundos)
	toolCanvas = 2  // lienzo → documento HTML immersive (en línea en la respuesta, sin descarga aparte)
	toolVideo  = 11 // vídeo → Veo (asíncrono: tras enviar, MUAZcd sondea hasta terminar y luego hNvQHb obtiene el enlace de descarga)
)

// extractCanvasDoc extrae el documento HTML generado de la respuesta de canvas
// (inner[49]=2).
//
// A diferencia de imagen/música, el documento no se descarga aparte: viene en línea en
// los frames — la estructura immersive (la entrada inner[4][0][30]…) contiene una cadena
// con la forma "```html\n<!DOCTYPE html>…```". En streaming, el mismo documento se
// reenvía acumulado en varios frames; se toma la cadena **más larga** que contenga
// DOCTYPE entre todos los frames = la versión final completa.
func extractCanvasDoc(raw string) string {
	best := ""
	var walk func(interface{})
	walk = func(o interface{}) {
		switch v := o.(type) {
		case string:
			if len(v) > len(best) && strings.Contains(v, "DOCTYPE") {
				best = v
			}
		case []interface{}:
			for _, x := range v {
				walk(x)
			}
		case map[string]interface{}:
			for _, x := range v {
				walk(x)
			}
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[[") {
			continue
		}
		var arr []interface{}
		if json.Unmarshal([]byte(line), &arr) != nil {
			continue
		}
		for _, it := range arr {
			row, ok := it.([]interface{})
			if !ok || len(row) < 3 {
				continue
			}
			payload, ok := row[2].(string)
			if !ok {
				continue
			}
			var inner interface{}
			if json.Unmarshal([]byte(payload), &inner) == nil {
				walk(inner)
			}
		}
	}
	return best
}

// downloadOPI es el parámetro opi obligatorio de la URL de descarga de productos (la de
// contribution). Se toma de la página /app; en las pruebas dos cuentas independientes
// daban este valor, así que se usa como constante global. La ruta de imagen por el CDN
// lh3 no lo necesita.
const downloadOPI = "103135050"

// MediaArtifact son los bytes en bruto de un producto multimedia ya generado.
type MediaArtifact struct {
	Mime string
	Data []byte
}

// downloadCookieNames es la lista blanca de cookies que reconoce el host de descarga de
// productos — el navegador solo le envía estas 18, todas con ámbito de dominio
// .google.com.
//
// Clave: si se le pasan también cookies exclusivas del host gemini/accounts
// (__Host-1PLSID / __Host-GAPS / LSID / OSID / OTZ / COMPASS / _ga* etc.), la
// autenticación del host de descarga devuelve directamente 403 y una página de error
// vacía comprimida con gzip. En las pruebas, solo enviando estas 18 daba 200. Es una de
// las causas raíz del 403 en las descargas de media, ajena a la frescura del token / la
// huella TLS / el opi / los distintos headers.
//
// Otra causa raíz es la redirección entre dominios: la cadena de imagen
// (lh3.googleusercontent.com/gg-dl/…) hace 302 a
// work.fife.usercontent.google.com/rd-gg-dl/…, y el cliente http, por seguridad, no lleva
// la cabecera Cookie al nuevo dominio por defecto, así que el destino de la redirección
// se queda sin cookie y también da 403. Se resuelve reenviando la cookie en cada salto con
// mediaGetFollow.
var downloadCookieNames = map[string]bool{
	"HSID": true, "SSID": true, "APISID": true, "SAPISID": true,
	"__Secure-1PAPISID": true, "__Secure-3PAPISID": true,
	"SID": true, "__Secure-1PSID": true, "__Secure-3PSID": true,
	"GOOGLE_ABUSE_EXEMPTION": true, "NID": true,
	"__Secure-1PSIDTS": true, "__Secure-1PSIDRTS": true,
	"__Secure-3PSIDTS": true, "__Secure-3PSIDRTS": true,
	"SIDCC": true, "__Secure-1PSIDCC": true, "__Secure-3PSIDCC": true,
}

// filterDownloadCookies deja solo las 18 que reconoce el host de descarga y no envía
// ninguna otra.
func filterDownloadCookies(cookie string) string {
	var kept []string
	for _, p := range strings.Split(cookie, ";") {
		p = strings.TrimSpace(p)
		i := strings.IndexByte(p, '=')
		if i <= 0 {
			continue
		}
		if downloadCookieNames[p[:i]] {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "; ")
}

// fetchMediaArtifacts recupera los bytes del producto multimedia. cookie / sapisid /
// xsrf / proxyURL deben ser los mismos que en la generación — el producto está atado a
// esta sesión / esta salida, y no se puede recuperar cambiando de salida o de cuenta.
//
// La ruta de recuperación difiere entre imagen y música: la imagen va por el CDN lh3 (el
// enlace está en la respuesta de StreamGenerate y se sigue el 302), mientras que la
// música/vídeo va por contribution.usercontent.google.com/download (el enlace está en el
// historial de hNvQHb y responde 200 en una sola llamada). Se separa por tool.
func fetchMediaArtifacts(tool int, raw, cid, cookie, sapisid, xsrf, proxyURL, defaultMime string) ([]MediaArtifact, error) {
	if tool == toolImage {
		return fetchImageArtifacts(raw, cid, cookie, sapisid, xsrf, proxyURL, defaultMime)
	}
	// La música está lista casi al instante; el vídeo tarda de decenas de segundos a
	// varios minutos, así que al sondeo de vídeo se le concede presupuesto de sobra.
	maxPolls, interval := 6, 2*time.Second
	if tool == toolVideo {
		maxPolls, interval = 45, 8*time.Second // unos 6 minutos
	}
	arts, err := fetchDownloadArtifacts(cid, cookie, sapisid, xsrf, proxyURL, defaultMime, maxPolls, interval)
	if err != nil {
		return arts, err
	}
	// En una sola petición a hNvQHb, el vídeo puede traer varios enlaces de descarga
	// (¿candidatos múltiples o varios perfiles de codificación de Veo? el significado no
	// está demostrado con rigor), así que solo se conserva el mayor para que el cliente no
	// reciba dos vídeos de golpe. Para distinguir su significado haría falta verificar con
	// una salida limpia (ver CLAUDE.md).
	if tool == toolVideo && len(arts) > 1 {
		largest := arts[0]
		for _, a := range arts[1:] {
			if len(a.Data) > len(largest.Data) {
				largest = a
			}
		}
		arts = []MediaArtifact{largest}
	}
	return arts, nil
}

// fetchImageArtifacts recupera las imágenes generadas. El enlace ya viene en la respuesta
// de StreamGenerate; si no se consigue, se recurre a sondear hNvQHb. La descarga pasa por
// mediaGetFollow (sigue los 302 y reenvía la cookie en cada salto).
func fetchImageArtifacts(raw, cid, cookie, sapisid, xsrf, proxyURL, defaultMime string) ([]MediaArtifact, error) {
	urls := collectImageURLs(raw)
	if len(urls) == 0 {
		for i := 0; i < 6; i++ {
			if body, err := pollHistoryRaw(cid, cookie, sapisid, xsrf, proxyURL); err == nil {
				if u := collectImageURLs(body); len(u) > 0 {
					urls = u
					break
				}
			}
			time.Sleep(2 * time.Second)
		}
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("la respuesta no trae enlaces de CDN de imagen")
	}
	var arts []MediaArtifact
	for _, u := range urls {
		mime, data, err := downloadBytes(imageFullResURL(u), cookie, proxyURL, defaultMime)
		if err != nil {
			return arts, err
		}
		// La misma imagen puede aparecer con tokens distintos (distintas resoluciones /
		// miniaturas); se deduplica por bytes para que el cliente no reciba varias copias
		// idénticas.
		if !artifactSeen(arts, data) {
			arts = append(arts, MediaArtifact{Mime: mime, Data: data})
		}
	}
	return arts, nil
}

// imageFullResURL añade el parámetro de tamaño a la cadena del CDN de imagen para obtener
// el original. Un gg-dl sin más devuelve por defecto una miniatura de ~500px (issue #14:
// en la consola era 1365×768; cambiar el final =s1024-rj por =s2048-rj da más). La
// convención de googleusercontent es añadir =sN al final para fijar el lado mayor y =s0
// para el tamaño original. Aquí se usa =s0 para obtener la resolución completa, y sin
// cambiar el formato (-rj fuerza jpeg, y queremos conservar PNG).
//
// Las opciones van en el último segmento de ruta, separadas por =; el token de gg-dl es
// base64url y no contiene =, así que se corta por el = del último segmento de ruta, se
// eliminan las opciones existentes y se añade =s0.
func imageFullResURL(u string) string {
	i := strings.LastIndexByte(u, '/')
	if i < 0 {
		return u + "=s0"
	}
	seg := u[i+1:]
	if j := strings.IndexByte(seg, '='); j >= 0 {
		u = u[:i+1] + seg[:j]
	}
	return u + "=s0"
}

// fetchDownloadArtifacts recupera música/vídeo: sondea hNvQHb hasta obtener el enlace de
// descarga response_data y luego descarga. Los enlaces gg-dl (lh3) y temp_data son para
// vista previa; solo el de response_data permite descargar los bytes reales.
func fetchDownloadArtifacts(cid, cookie, sapisid, xsrf, proxyURL, defaultMime string,
	maxPolls int, interval time.Duration) ([]MediaArtifact, error) {
	if cid == "" {
		return nil, fmt.Errorf("no se obtuvo el id de sesión; no se puede localizar el producto")
	}
	var dlURLs []string
	for i := 0; i < maxPolls; i++ {
		if body, err := pollHistoryRaw(cid, cookie, sapisid, xsrf, proxyURL); err == nil {
			if picked := pickResponseDataURLs(collectDownloadURLs(body)); len(picked) > 0 {
				dlURLs = picked
				break
			}
			// Cuando la política de contenido rechaza el vídeo, en hNvQHb aparece «I can't
			// generate that video»; no hay que esperar en vano hasta el timeout.
			if strings.Contains(body, "can't generate that video") {
				return nil, fmt.Errorf("el vídeo fue rechazado por la política de contenido (prueba con otro prompt)")
			}
		}
		time.Sleep(interval)
	}
	if len(dlURLs) == 0 {
		return nil, fmt.Errorf("hNvQHb no devolvió ningún enlace de producto descargable (response_data)")
	}
	var arts []MediaArtifact
	for _, u := range dlURLs {
		// Al enlace de contribution hay que añadirle filename / opi.
		if !strings.Contains(u, "opi=") {
			sep := "?"
			if strings.Contains(u, "?") {
				sep = "&"
			}
			u += sep + "filename=artifact&opi=" + downloadOPI
		}
		mime, data, err := downloadBytes(u, cookie, proxyURL, defaultMime)
		if err != nil {
			return arts, err
		}
		if !artifactSeen(arts, data) {
			arts = append(arts, MediaArtifact{Mime: mime, Data: data})
		}
	}
	return arts, nil
}

// pollHistoryRaw llama una vez a hNvQHb para obtener el historial de la conversación y
// devuelve el cuerpo de respuesta en bruto.
func pollHistoryRaw(cid, cookie, sapisid, xsrf, proxyURL string) (string, error) {
	inner, _ := json.Marshal([]interface{}{cid, 10, nil, 1, []interface{}{0}, []interface{}{4}, nil, 1})
	freq, _ := json.Marshal([]interface{}{[]interface{}{[]interface{}{"hNvQHb", string(inner), nil, "generic"}}})
	form := url.Values{}
	form.Set("f.req", string(freq))
	if xsrf != "" {
		form.Set("at", xsrf)
	}
	reqid := time.Now().UnixNano() % 1000000
	endpoint := fmt.Sprintf(
		"https://gemini.google.com/_/BardChatUi/data/batchexecute?rpcids=hNvQHb&bl=%s&hl=en&_reqid=%d&rt=c",
		currentBL(proxyURL), reqid)

	// batchexecute no lleva la cabecera de modelo; el resto (cookie / SAPISIDHASH /
	// x-same-domain) es igual que en la petición principal.
	headers := buildGeminiHeaders(cookie, sapisid, "")
	delete(headers, "x-goog-ext-525001261-jspb")

	status, _, body, err := uploadPost(endpoint, headers, []byte(form.Encode()), proxyURL)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("hNvQHb HTTP %d", status)
	}
	return string(body), nil
}

// deleteConversation elimina una conversación que queda en gemini.google.com (#19).
//
// El protocolo se toma literalmente de una captura: rpc GzXR5e, parámetro ["<cid>"],
// mode "generic", con at=XSRF.
//
//	f.req=[[["GzXR5e","[\"c_xxx\"]",null,"generic"]]]&at=<xsrf>
//
// Solo funciona con sesión iniciada (el anónimo no tiene XSRF y la conversación tampoco
// queda en la cuenta). Best-effort: si falla el borrado solo se registra en el log y no
// afecta a la respuesta ya devuelta al cliente.
func deleteConversation(cid, cookie, sapisid, xsrf, proxyURL string) {
	inner, _ := json.Marshal([]interface{}{cid})
	freq, _ := json.Marshal([]interface{}{[]interface{}{[]interface{}{"GzXR5e", string(inner), nil, "generic"}}})
	form := url.Values{}
	form.Set("f.req", string(freq))
	form.Set("at", xsrf)
	reqid := time.Now().UnixNano() % 1000000
	endpoint := fmt.Sprintf(
		"https://gemini.google.com/_/BardChatUi/data/batchexecute?rpcids=GzXR5e&bl=%s&hl=en&_reqid=%d&rt=c",
		currentBL(proxyURL), reqid)
	headers := buildGeminiHeaders(cookie, sapisid, "")
	delete(headers, "x-goog-ext-525001261-jspb")
	status, _, body, err := uploadPost(endpoint, headers, []byte(form.Encode()), proxyURL)
	if err != nil {
		logf("[autodel] fallo al borrar la conversación %s: %v", cid, err)
		return
	}
	if status != 200 {
		logf("[autodel] al borrar la conversación %s se recibió HTTP %d: %s", cid, status, truncate(string(body), 120))
		return
	}
	logf("[autodel] conversación %s borrada", cid)
}

// walkFramesForURLs recorre recursivamente todas las cadenas del sobre de batchexecute y
// devuelve las que cumplan want (sin duplicados y conservando el orden).
//
// Estructura de la respuesta: cada línea es [["wrb.fr","<rpc>","<cadena json>",…], y los
// datos reales están enterrados en esa cadena json interna, a profundidad variable. Hay
// que pasar por un parseo JSON, no aplicar regex directamente sobre raw — raw es JSON con
// **doble escape**, y las barras/los límites de las URL no son iguales en raw que una vez
// desescapados: la regex capturaría unos caracteres de más o de menos y la URL resultante
// quedaría inservible (en las pruebas, lo extraído con regex salía 2 caracteres más largo
// que la URL real y la descarga daba 400 directamente).
func walkFramesForURLs(raw string, want func(string) bool) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(interface{})
	walk = func(o interface{}) {
		switch v := o.(type) {
		case string:
			if want(v) && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		case []interface{}:
			for _, x := range v {
				walk(x)
			}
		case map[string]interface{}:
			for _, x := range v {
				walk(x)
			}
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[[") {
			continue
		}
		var arr []interface{}
		if json.Unmarshal([]byte(line), &arr) != nil {
			continue
		}
		for _, it := range arr {
			row, ok := it.([]interface{})
			if !ok || len(row) < 3 {
				continue
			}
			payload, ok := row[2].(string)
			if !ok {
				continue
			}
			var inner interface{}
			if json.Unmarshal([]byte(payload), &inner) == nil {
				walk(inner)
			}
		}
	}
	return out
}

// collectDownloadURLs extrae de la respuesta todos los enlaces de descarga contribution
// (para música/vídeo).
func collectDownloadURLs(raw string) []string {
	return walkFramesForURLs(raw, func(s string) bool {
		return strings.Contains(s, "contribution.usercontent.google.com/download")
	})
}

// collectImageURLs extrae de la respuesta todos los enlaces de CDN de las imágenes
// generadas (gg-dl viene del primer frame, gg de hNvQHb).
// Este enlace sin más devuelve la imagen real con un GET directo — no hay que añadirle el
// prefijo rd- (el rd- de la captura es **otro juego de tokens**; montar rd- con el token
// de este enlace da 400).
func collectImageURLs(raw string) []string {
	return walkFramesForURLs(raw, func(s string) bool {
		return strings.Contains(s, "lh3.googleusercontent.com/gg-dl/") ||
			strings.Contains(s, "lh3.googleusercontent.com/gg/")
	})
}

// pickResponseDataURLs elige de entre varios enlaces de descarga el que realmente permite
// descargar (al decodificar el parámetro c contiene "response_data").
// Los otros dos (temp_data de vista previa y gg-dl) dan 403 al descargar y hay que
// excluirlos.
func pickResponseDataURLs(urls []string) []string {
	var out []string
	for _, u := range urls {
		if downloadIsResponseData(u) {
			out = append(out, u)
		}
	}
	return out
}

// downloadIsResponseData decodifica el base64 del parámetro c para ver si el protobuf
// contiene la marca "response_data".
// No se hace coincidencia de cadena directamente sobre el fragmento base64: eso depende
// del alineamiento y con otro prefijo se escapa.
func downloadIsResponseData(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	c := u.Query().Get("c")
	if c == "" {
		return false
	}
	dec, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(c, "="))
	if err != nil {
		dec, err = base64.StdEncoding.DecodeString(c)
		if err != nil {
			return false
		}
	}
	return strings.Contains(string(dec), "response_data")
}

// downloadBytes hace un GET de un enlace de producto, sigue las redirecciones y reenvía el
// subconjunto de cookies en cada salto, y devuelve el content-type y los bytes en bruto.
// Tanto la cadena de imagen (que da 302) como la de contribution (200 en una sola llamada)
// pasan por aquí.
func downloadBytes(rawURL, cookie, proxyURL, defaultMime string) (string, []byte, error) {
	headers := map[string]string{
		"Cookie":  filterDownloadCookies(cookie),
		"Origin":  "https://gemini.google.com",
		"Referer": "https://gemini.google.com/",
		"Accept":  "image/avif,image/webp,image/apng,image/*,*/*;q=0.8",
	}
	status, respHead, body, err := mediaGetFollow(rawURL, headers, proxyURL)
	if err != nil {
		return "", nil, err
	}
	if status != 200 {
		return "", nil, fmt.Errorf("descarga del producto HTTP %d (%d bytes)", status, len(body))
	}
	mime := respHead["content-type"]
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	if mime == "" || strings.HasPrefix(mime, "text/html") {
		mime = defaultMime
	}
	return mime, body, nil
}

// mediaGetFollow sigue las redirecciones manualmente (hasta 6 saltos) y reenvía el mismo
// juego de cabeceras (con Cookie) en cada salto.
//
// Por qué no usar el seguimiento automático del cliente: la cadena de imagen hace un 302
// entre dominios (lh3 → work.fife.usercontent.google.com), y el cliente http, por
// seguridad, no lleva la cabecera Cookie al nuevo dominio por defecto, así que el destino
// de la redirección se queda sin cookie y da 403. Solo se pasa siguiendo manualmente y
// reenviando la cookie en cada salto. Además, ambos client están configurados para «no
// seguir automáticamente».
func mediaGetFollow(rawURL string, headers map[string]string, proxyURL string) (int, map[string]string, []byte, error) {
	cur := rawURL
	for hop := 0; hop < 6; hop++ {
		status, respHead, body, err := mediaGetOnce(cur, headers, proxyURL)
		if err != nil {
			return status, respHead, body, err
		}
		if status >= 300 && status < 400 {
			loc := respHead["location"]
			if loc == "" {
				return status, respHead, body, nil
			}
			next, err := resolveRef(cur, loc)
			if err != nil {
				return status, respHead, body, err
			}
			cur = next
			continue
		}
		return status, respHead, body, nil
	}
	return 0, nil, nil, fmt.Errorf("demasiadas redirecciones de descarga")
}

// resolveRef resuelve el Location respecto a la URL actual (normalmente es una dirección
// absoluta, pero también admite relativa).
func resolveRef(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

// mediaGetOnce lanza un GET sin seguir redirecciones y devuelve también las cabeceras de
// respuesta (hay que leer content-type / location).
// Con proxy usa stdlib; sin proxy usa tls-client, con la misma norma que doGeminiRequest /
// uploadPost.
func mediaGetOnce(rawURL string, headers map[string]string, proxyURL string) (int, map[string]string, []byte, error) {
	if proxyURL != "" {
		req, err := http.NewRequest("GET", rawURL, nil)
		if err != nil {
			return 0, nil, nil, err
		}
		applyChromeHeaders(req)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := getStdlibClient(proxyURL).Do(req)
		if err != nil {
			return 0, nil, nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return resp.StatusCode, flattenHeaders(resp.Header), b, err
	}
	req, err := fhttp.NewRequest("GET", rawURL, nil)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := getTLSClient().Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, flattenFHeaders(resp.Header), b, err
}

func flattenHeaders(h http.Header) map[string]string {
	m := map[string]string{}
	for k := range h {
		m[strings.ToLower(k)] = h.Get(k)
	}
	return m
}

func flattenFHeaders(h fhttp.Header) map[string]string {
	m := map[string]string{}
	for k := range h {
		m[strings.ToLower(k)] = h.Get(k)
	}
	return m
}

// extractConversationID obtiene el id de sesión de la respuesta de StreamGenerate (el
// [1][0] del frame).
// Se necesita para recuperar los productos de música/vídeo consultando en hNvQHb el
// historial de esta generación.
func extractConversationID(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[[") {
			continue
		}
		var arr []interface{}
		if json.Unmarshal([]byte(line), &arr) != nil {
			continue
		}
		for _, it := range arr {
			row, ok := it.([]interface{})
			if !ok || len(row) < 3 || row[0] != "wrb.fr" {
				continue
			}
			payload, ok := row[2].(string)
			if !ok {
				continue
			}
			var inner []interface{}
			if json.Unmarshal([]byte(payload), &inner) != nil || len(inner) < 2 {
				continue
			}
			meta, ok := inner[1].([]interface{})
			if ok && len(meta) > 0 {
				if cid, ok := meta[0].(string); ok && cid != "" {
					return cid
				}
			}
		}
	}
	return ""
}

// artifactSeen indica si estos bytes ya se habían recibido (comparación por contenido,
// descarta duplicados de la misma imagen con distinto token).
func artifactSeen(arts []MediaArtifact, data []byte) bool {
	for _, a := range arts {
		if bytes.Equal(a.Data, data) {
			return true
		}
	}
	return false
}

// appendArtifactMarkdown convierte los bytes del producto en una data URL base64 y la
// añade al final del cuerpo.
// Las imágenes usan la sintaxis Markdown de imagen (la mayoría de UI de chat las renderiza
// directamente); el resto (audio) usa la sintaxis de enlace.
func appendArtifactMarkdown(text string, arts []MediaArtifact) string {
	var b strings.Builder
	b.WriteString(text)
	for _, a := range arts {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		dataURL := "data:" + a.Mime + ";base64," + base64.StdEncoding.EncodeToString(a.Data)
		if strings.HasPrefix(a.Mime, "image/") {
			b.WriteString("![image](" + dataURL + ")")
		} else {
			b.WriteString("[audio](" + dataURL + ")")
		}
	}
	return b.String()
}

// dataURLRe reconoce una data URL base64 completa.
var dataURLRe = regexp.MustCompile(`data:([-\w.+/]+);base64,[A-Za-z0-9+/=]+`)

// stripDataURLs elimina las data URL base64 del texto y deja solo un marcador corto.
// Se usa al contar tokens / medir longitud — una imagen en base64 ocupa millones de
// caracteres y cobrarla equivaldría a que el usuario pagara por binario que no ve; el
// newapi de aguas abajo cobra por output token. El producto en sí se devuelve como siempre
// dentro de content.
func stripDataURLs(text string) string {
	return dataURLRe.ReplaceAllString(text, "data:$1;base64,<omitted>")
}
