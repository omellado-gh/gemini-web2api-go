package app

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
)

// Mantenimiento de sesión. El mismo POST /RotateCookies tiene dos payloads, y no
// refrescan la misma familia de cookies:
//
//  1. El centinela `[000,"-0000000000000000000"]`: reemite sin condición
//     `__Secure-1PSIDTS` / `__Secure-3PSIDTS`. Es el verdadero billete efímero de la
//     sesión iniciada (unos 30 minutos); si no se refresca, se pasa por anónimo. El
//     payload debe ser esta cadena JSPB (los ceros a la izquierda son válidos, el JSON
//     estricto no), json.Marshal lo convertiría en `[0,"…"]` y el servidor no lo acepta.
//
//  2. La del iframe del navegador: primero GET RotateCookiesPage para obtener el id de
//     sesión, luego POST `[658,"<id>"]`. Solo refresca SIDCC / `__Secure-1PSIDCC` /
//     `__Secure-3PSIDCC`; el intervalo lo da el último parámetro del init(...) de la
//     página (600 segundos medidos).
//
// Antes solo se hacía la segunda, así que la cuenta moría a la media hora (issue #6). Las
// versiones nuevas de Chrome tienen además vinculación de dispositivo DBSC (GET
// /RotateBoundCookies + JWT firmado), que no podemos replicar; el centinela sí vale para
// exportaciones de Firefox y sesiones sin dispositivo vinculado. Si una cuenta exportada
// de Chrome devuelve 401 repetidamente, vuelva a iniciar sesión en Firefox y exporte otra vez.

const (
	rotatePageURL = "https://accounts.google.com/RotateCookiesPage" +
		"?og_pid=658&rot=3&origin=https%3A%2F%2Fgemini.google.com&exp_id=0"
	rotatePostURL = "https://accounts.google.com/RotateCookies"
	// og_pid es el identificador de producto, fijo en 658 para Gemini; sirve como query de la
	// página de arriba y también se devuelve en la propia página.
	rotateProductID = 658
	// Valor de reserva para cuando el servidor no da intervalo.
	defaultRotateInterval = 10 * time.Minute
	// Refrescar una vez lo antes posible tras arrancar: al importar, la cookie puede estar a
	// punto de caducar y esperar 10 minutos la haría caducar directamente.
	firstRotateDelay = 15 * time.Second
	// Payload centinela. Los ceros a la izquierda son intencionados, ver la cabecera del archivo.
	rotate1PSIDTSBody = `[000,"-0000000000000000000"]`
	// Si se llama demasiado a menudo, da 429. Tanto Gemini-API como notebooklm-py usan un
	// mínimo de 60 segundos.
	min1PSIDTSInterval = 60 * time.Second
)

// El refresco de 1PSIDTS lleva solo este par. En pruebas, llevar más da 401.
var rotate1PSIDTSCookies = []string{"__Secure-1PSID", "__Secure-1PSIDTS"}

var (
	psidtsMu     sync.Mutex
	psidtsLastAt = map[int64]time.Time{}
)

// En la página tiene la forma: init('4162200486104360679', 658.0, 0.0, 0.0, 600.0)
// El primer parámetro es el identificador de esta sesión y el último, los segundos hasta la
// próxima rotación.
var rotateInitRe = regexp.MustCompile(`init\('([^']{4,64})'\s*,\s*([0-9.]+)\s*,[^)]*?([0-9.]+)\s*\)`)

// rotateAccount hace un mantenimiento a una cuenta: primero refresca 1PSIDTS y luego SIDCC.
// Devuelve el próximo intervalo sugerido por el servidor y los nombres de cookie refrescados
// realmente en esta ronda.
func rotateAccount(a CookieAccount) (time.Duration, []string, error) {
	proxyURL := ""
	if a.ProxyID > 0 {
		proxyURL = proxyURLByID(a.ProxyID)
	}

	cookie := a.Cookie
	var names []string
	var psidtsErr, sidccErr error
	interval := defaultRotateInterval

	c2, n, err := tryRotate1PSIDTS(a.ID, cookie, proxyURL)
	if err != nil {
		psidtsErr = err
		logf("[rotate] fallo al refrescar 1PSIDTS de la cuenta #%d: %v", a.ID, err)
	} else {
		cookie = c2
		names = append(names, n...)
	}

	c3, iv, n2, err := rotateSIDCC(cookie, proxyURL)
	if err != nil {
		sidccErr = err
		logf("[rotate] fallo el mantenimiento SIDCC de la cuenta #%d: %v", a.ID, err)
	} else {
		cookie = c3
		names = append(names, n2...)
		if iv > 0 {
			interval = iv
		}
	}

	names = uniqueKeepOrder(names)
	if cookie != a.Cookie {
		old := a.Cookie
		updateAccountCookie(a.ID, cookie)
		invalidateXSRF(old)
	}
	if len(names) > 0 {
		logf("[rotate] la cuenta #%d ha refrescado %s", a.ID, strings.Join(names, ", "))
	}
	// Solo cuenta como fallo si fallan las dos rutas. Si se refrescó 1PSIDTS pero la página
	// del iframe no tenía init, sigue siendo un mantenimiento con éxito.
	if cookie == a.Cookie && len(names) == 0 {
		if psidtsErr != nil {
			return 0, nil, psidtsErr
		}
		if sidccErr != nil {
			return 0, nil, sidccErr
		}
	}
	return interval, names, nil
}

// tryRotate1PSIDTS reemite 1PSIDTS con el payload centinela. Si no hay __Secure-1PSID o
// han pasado menos de 60 segundos desde la última vez, se omite (no cuenta como fallo).
func tryRotate1PSIDTS(id int64, cookie, proxyURL string) (string, []string, error) {
	if cookieValue(cookie, "__Secure-1PSID") == "" {
		return cookie, nil, nil
	}
	if ok, _ := allow1PSIDTSRotate(id); !ok {
		logf("[rotate] se omite el refresco de 1PSIDTS de la cuenta #%d (menos de %s desde la última vez, para evitar el 429)", id, min1PSIDTSInterval)
		return cookie, nil, nil
	}
	c2, names, err := rotate1PSIDTS(cookie, proxyURL)
	note1PSIDTSAttempt(id, err)
	if err != nil {
		return cookie, nil, err
	}
	return c2, names, nil
}

func allow1PSIDTSRotate(id int64) (bool, time.Duration) {
	psidtsMu.Lock()
	defer psidtsMu.Unlock()
	last := psidtsLastAt[id]
	if last.IsZero() {
		return true, 0
	}
	elapsed := time.Since(last)
	if elapsed >= min1PSIDTSInterval {
		return true, 0
	}
	return false, min1PSIDTSInterval - elapsed
}

func note1PSIDTSAttempt(id int64, err error) {
	// Tanto el éxito como el 401/403 y el 429 registran el tiempo, para no volver a llamar
	// justo después. Los errores de red no se registran, lo que permite reintentar de inmediato.
	if err != nil {
		msg := err.Error()
		if !strings.Contains(msg, "HTTP 401") &&
			!strings.Contains(msg, "HTTP 403") &&
			!strings.Contains(msg, "HTTP 429") {
			return
		}
	}
	psidtsMu.Lock()
	psidtsLastAt[id] = time.Now()
	psidtsMu.Unlock()
}

// rotate1PSIDTS hace POST del payload centinela y fusiona el Set-Cookie de la respuesta en la
// cadena de cookie completa.
func rotate1PSIDTS(cookie, proxyURL string) (string, []string, error) {
	subset := cookieSubset(cookie, rotate1PSIDTSCookies)
	headers := map[string]string{
		"Accept":         "*/*",
		"Content-Type":   "application/json",
		"Origin":         "https://accounts.google.com",
		"Cookie":         subset,
		"Cache-Control":  "no-cache",
		"Pragma":         "no-cache",
		"Sec-Fetch-Dest": "empty",
		"Sec-Fetch-Mode": "cors",
		"Sec-Fetch-Site": "same-origin",
	}
	status, setCookie, respBody, err := rotatePost(rotatePostURL, headers, []byte(rotate1PSIDTSBody), proxyURL)
	if err != nil {
		return cookie, nil, err
	}
	if status == 401 || status == 403 {
		return cookie, nil, fmt.Errorf("RotateCookies 1PSIDTS devolvió HTTP %d (la cookie exportada de Chrome puede estar limitada por la vinculación de dispositivo; se recomienda volver a exportarla con Firefox): %s",
			status, truncate(string(respBody), 120))
	}
	if status != 200 {
		return cookie, nil, fmt.Errorf("RotateCookies 1PSIDTS devolvió HTTP %d: %s", status, truncate(string(respBody), 120))
	}
	merged := mergeSetCookie(cookie, setCookie)
	return merged, setCookieNames(setCookie), nil
}

// rotateSIDCC usa la ruta del iframe del navegador: GET de la página de rotación para obtener
// el id de sesión y luego POST [658, id].
func rotateSIDCC(cookie, proxyURL string) (string, time.Duration, []string, error) {
	id, interval, pageSet, err := fetchRotateParams(cookie, proxyURL)
	if err != nil {
		return cookie, 0, nil, err
	}
	cookie = mergeSetCookie(cookie, pageSet)

	body := fmt.Sprintf(`[%d,"%s"]`, rotateProductID, id)
	headers := rotatePostHeaders()
	headers["Cookie"] = cookie
	status, setCookie, respBody, err := rotatePost(rotatePostURL, headers, []byte(body), proxyURL)
	if err != nil {
		return cookie, 0, nil, err
	}
	if status != 200 {
		return cookie, 0, nil, fmt.Errorf("RotateCookies devolvió HTTP %d: %s", status, truncate(string(respBody), 120))
	}
	cookie = mergeSetCookie(cookie, setCookie)
	names := setCookieNames(append(append([]string{}, pageSet...), setCookie...))
	return cookie, interval, names, nil
}

func uniqueKeepOrder(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// setCookieNames extrae y deduplica los nombres de las cabeceras Set-Cookie; solo se usa para el log.
func setCookieNames(headers []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range headers {
		name := h
		if i := strings.Index(name, "="); i > 0 {
			name = name[:i]
		}
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// fetchRotateParams descarga RotateCookiesPage y obtiene el identificador de sesión, el
// intervalo indicado por el servidor y el Set-Cookie que trae esta misma petición.
func fetchRotateParams(cookie, proxyURL string) (string, time.Duration, []string, error) {
	status, setCookie, body, err := rotateGet(rotatePageURL, cookie, proxyURL)
	if err != nil {
		return "", 0, nil, err
	}
	if status != 200 {
		return "", 0, nil, fmt.Errorf("la descarga de la página de rotación devolvió HTTP %d", status)
	}
	m := rotateInitRe.FindSubmatch(body)
	if m == nil {
		// La página se obtuvo pero no tiene init(...): lo más probable es que la cookie ya no
		// valga y haya saltado a la página de inicio de sesión.
		return "", 0, nil, fmt.Errorf("la página de rotación no tiene init(...) (puede que la cookie ya no valga)")
	}
	id := string(m[1])
	interval := defaultRotateInterval
	if sec, e := strconv.ParseFloat(string(m[3]), 64); e == nil && sec >= 60 && sec <= 3600 {
		interval = time.Duration(sec) * time.Second
	}
	return id, interval, setCookie, nil
}

// Los dos grupos de cabeceras siguientes están copiados punto por punto de una captura
// (wireHeaders, no headers: esta última no incluye las cookies). En la captura también hay
// sec-ch-ua-arch / -bitness / -form-factors / -full-version-list / -model /
// -platform-version / -wow64 y x-browser-* / x-client-data /
// x-chrome-id-consistency-request, pero esas son la identidad de navegador que pega el propio
// Chrome; si las pegáramos nosotros, la huella TLS no cuadraría, así que no se ponen.

// rotateGet descarga la página de rotación. En el navegador es una navegación de iframe, así
// que el grupo sec-fetch es totalmente distinto del de un XHR normal (dest=iframe /
// mode=navigate / site=same-site); no le aplique los valores por defecto.
func rotateGet(url, cookie, proxyURL string) (int, []string, []byte, error) {
	return rotateDo("GET", url, map[string]string{
		"Cookie":                    cookie,
		"Accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		"Referer":                   "https://gemini.google.com/",
		"Sec-Fetch-Dest":            "iframe",
		"Sec-Fetch-Mode":            "navigate",
		"Sec-Fetch-Site":            "same-site",
		"Sec-Fetch-User":            "?1",
		"Upgrade-Insecure-Requests": "1",
		"Priority":                  "u=0, i",
	}, nil, proxyURL)
}

// rotatePostHeaders son las cabeceras completas del POST /RotateCookies; quien llama solo añade la Cookie.
func rotatePostHeaders() map[string]string {
	return map[string]string{
		"Accept":         "*/*",
		"Content-Type":   "application/json",
		"Origin":         "https://accounts.google.com",
		"Referer":        rotatePageURL,
		"Cache-Control":  "no-cache",
		"Pragma":         "no-cache",
		"Priority":       "u=1, i",
		"Sec-Fetch-Dest": "empty",
		"Sec-Fetch-Mode": "same-origin",
		"Sec-Fetch-Site": "same-origin",
	}
}

func rotatePost(url string, headers map[string]string, body []byte, proxyURL string) (
	int, []string, []byte, error) {
	return rotateDo("POST", url, headers, body, proxyURL)
}

// rotateDo sale por la misma salida que las peticiones normales: hacer el mantenimiento desde
// otra IP equivale a decirle al upstream que esta sesión está activa en dos sitios distintos.
//
// Las dos rutas de transporte comparten las mismas cabeceras. Antes solo la que pasaba por el
// proxy llamaba a applyChromeHeaders, y la de conexión directa ni siquiera enviaba User-Agent:
// la misma cuenta se veía en el upstream como dos clientes según usara proxy o no.
func rotateDo(method, url string, headers map[string]string, body []byte, proxyURL string) (
	int, []string, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = strings.NewReader(string(body))
	}
	merged := map[string]string{
		"User-Agent":         ChromeUA,
		"Accept-Language":    "en-US,en;q=0.9",
		"Sec-CH-UA":          `"Chromium";v="146", "Google Chrome";v="146", "Not?A_Brand";v="24"`,
		"Sec-CH-UA-Mobile":   "?0",
		"Sec-CH-UA-Platform": `"Windows"`,
	}
	for k, v := range headers {
		merged[k] = v
	}
	if proxyURL != "" {
		req, err := http.NewRequest(method, url, rdr)
		if err != nil {
			return 0, nil, nil, err
		}
		for k, v := range merged {
			req.Header.Set(k, v)
		}
		resp, err := getStdlibClient(proxyURL).Do(req)
		if err != nil {
			return 0, nil, nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Values("Set-Cookie"), b, err
	}
	req, err := fhttp.NewRequest(method, url, rdr)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range merged {
		req.Header.Set(k, v)
	}
	resp, err := getTLSClient().Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Values("Set-Cookie"), b, err
}

// rotateAllAccounts hace un mantenimiento a cada cuenta habilitada del pool y devuelve cuánto
// hay que esperar la próxima vez.
//
// Los fallos **no cuentan para la salud**: el mantenimiento llama a accounts.google.com y que
// la conversación funcione o no es otra cosa; marcar la cuenta como mala por un temblor de red
// la hundiría al seleccionar cuentas y dañaría la disponibilidad.
func rotateAllAccounts() time.Duration {
	next := defaultRotateInterval
	for _, a := range accountList() {
		if a.Status != "enabled" {
			continue
		}
		iv, _, err := rotateAccount(a)
		if err != nil {
			logf("[rotate] fallo el mantenimiento de la cuenta #%d: %v", a.ID, err)
			continue
		}
		if iv > 0 {
			next = iv
		}
	}
	return next
}
