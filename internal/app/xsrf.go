package app

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

// Gemini, cuando lleva cookie de inicio de sesión, exige que las peticiones batchexecute lleven un campo de formulario adicional at (token XSRF);
// sin él devuelve directamente 400, con un cuerpo de respuesta de la forma [["er",...,400,...,[{"48448350":["xsrf", ...]}]]].
// Las peticiones anónimas no lo necesitan, por eso este problema nunca se había expuesto —— en cuanto se pone una cookie válida, todas las peticiones fallan de inmediato.
//
// El token viene del HTML de la página /app, en "SNlM0e":"<token>:<marca de tiempo en ms>", y está vinculado a la sesión
// de la cookie, así que se cachea por cookie. En pruebas el mismo token se puede reutilizar; tras caducar el servidor sigue devolviendo
// error xsrf, y quien llama, al recibir este error, debe invalidar y obtenerlo de nuevo.
var (
	xsrfMu    sync.Mutex
	xsrfCache = map[string]xsrfEntry{}
)

type xsrfEntry struct {
	token   string
	pushID  string // cabecera Push-ID usada para subir archivos
	pctx    string // cabecera X-Client-Pctx usada para subir archivos
	fetched time.Time
}

// El token de la página no indica su vigencia; se toma un valor conservador y se vuelve a obtener periódicamente.
const xsrfTTL = 20 * time.Minute

var snlm0eRe = regexp.MustCompile(`"SNlM0e":"([^"]{10,200})"`)

// Los dos parámetros de página que necesita la subida se toman de la misma página que el token XSRF, ahorrando una petición de página.
var pushIDRe = regexp.MustCompile(`"qKIAYe":"([^"]{4,400})"`)

var pctxRe = regexp.MustCompile(`"Ylro7b":"([^"]{4,400})"`)

// cookieKey usa un resumen corto de la cookie como clave de caché, para evitar meter la cadena entera de credenciales como clave del map.
func cookieKey(cookie string) string {
	sum := sha1.Sum([]byte(cookie))
	return hex.EncodeToString(sum[:8])
}

// invalidateXSRF descarta el token en caché de una cookie; la próxima vez que se obtenga se volverá a descargar la página.
func invalidateXSRF(cookie string) {
	if cookie == "" {
		return
	}
	xsrfMu.Lock()
	delete(xsrfCache, cookieKey(cookie))
	xsrfMu.Unlock()
}

// getXSRF obtiene el token XSRF correspondiente a esa cookie; si acierta en caché y no ha caducado, lo devuelve directamente.
// Con cookie vacía (anónimo) devuelve cadena vacía —— las peticiones anónimas no necesitan este campo.
func getXSRF(cookie, proxyURL string) (string, error) {
	if cookie == "" {
		return "", nil
	}
	key := cookieKey(cookie)

	xsrfMu.Lock()
	if e, ok := xsrfCache[key]; ok && time.Since(e.fetched) < xsrfTTL {
		xsrfMu.Unlock()
		return e.token, nil
	}
	xsrfMu.Unlock()

	e, err := fetchAppTokens(cookie, proxyURL)
	if err != nil {
		return "", err
	}
	xsrfMu.Lock()
	xsrfCache[key] = e
	xsrfMu.Unlock()
	return e.token, nil
}

// getUploadTokens obtiene el Push-ID / X-Client-Pctx que necesita la subida, de la misma caché que el token XSRF.
func getUploadTokens(cookie, proxyURL string) (pushID, pctx string, err error) {
	key := cookieKey(cookie)

	xsrfMu.Lock()
	if e, ok := xsrfCache[key]; ok && time.Since(e.fetched) < xsrfTTL {
		xsrfMu.Unlock()
		return e.pushID, e.pctx, nil
	}
	xsrfMu.Unlock()

	e, err := fetchAppTokens(cookie, proxyURL)
	if err != nil {
		return "", "", err
	}
	xsrfMu.Lock()
	xsrfCache[key] = e
	xsrfMu.Unlock()
	return e.pushID, e.pctx, nil
}

// fetchAppPage descarga el HTML de gemini.google.com/app.
// Usa la misma salida que la petición principal: con proxy usa stdlib, sin proxy usa tls-client,
// para evitar que el token obtenido de la página y las peticiones posteriores vengan de dos IP distintas.
// Con cookie como cadena vacía se obtiene de forma anónima (la página responde igual, solo que sin campos de estado de sesión).
func fetchAppPage(cookie, proxyURL string) ([]byte, error) {
	const pageURL = "https://gemini.google.com/app"
	headers := map[string]string{
		"Accept":          "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language": "en-US,en;q=0.9",
	}
	if cookie != "" {
		headers["Cookie"] = cookie
	}

	var body []byte
	if proxyURL != "" {
		req, err := http.NewRequest("GET", pageURL, nil)
		if err != nil {
			return nil, err
		}
		applyChromeHeaders(req)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := getStdlibClient(proxyURL).Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("fetch /app: HTTP %d", resp.StatusCode)
		}
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
	} else {
		req, err := fhttp.NewRequest("GET", pageURL, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := getTLSClient().Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("fetch /app: HTTP %d", resp.StatusCode)
		}
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
	}
	return body, nil
}

// fetchAppTokens descarga una vez la página /app y extrae los tres token juntos.
func fetchAppTokens(cookie, proxyURL string) (xsrfEntry, error) {
	body, err := fetchAppPage(cookie, proxyURL)
	if err != nil {
		return xsrfEntry{}, err
	}
	e := xsrfEntry{fetched: time.Now()}
	if m := snlm0eRe.FindSubmatch(body); m != nil {
		e.token = string(m[1])
	} else if cookie != "" {
		// Con cookie pero sin token = la cookie ya caducó y se trata como anónimo; el anónimo no tiene este campo, así que no cuenta como error.
		return xsrfEntry{}, fmt.Errorf("no SNlM0e in page (cookie expired or not signed in)")
	}
	if p := pushIDRe.FindSubmatch(body); p != nil {
		e.pushID = string(p[1])
	}
	if p := pctxRe.FindSubmatch(body); p != nil {
		e.pctx = string(p[1])
	}
	return e, nil
}

// isXSRFError determina si el 400 del upstream es un problema de token XSRF.
// El cuerpo de respuesta tiene la forma: [["er",null,...,400,...,[{"48448350":["xsrf","<token nuevo>",...]}]]]
func isXSRFError(raw string) bool {
	return strings.Contains(raw, `"xsrf"`)
}
