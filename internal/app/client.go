package app

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

var (
	clientCache   = map[string]tls_client.HttpClient{}
	clientCacheMu sync.RWMutex
)

// resolveProfile mapea cadena de configuración a perfil tls-client.
// Por defecto Chrome_146 (coincide con curl_cffi chrome146 de Python v2).
func resolveProfile(name string) profiles.ClientProfile {
	switch strings.ToLower(name) {
	case "chrome_120":
		return profiles.Chrome_120
	case "chrome_124":
		return profiles.Chrome_124
	case "chrome_131":
		return profiles.Chrome_131
	case "chrome_133":
		return profiles.Chrome_133
	case "chrome_144":
		return profiles.Chrome_144
	case "chrome_146", "chrome146":
		return profiles.Chrome_146
	case "firefox_120":
		return profiles.Firefox_120
	case "firefox_123":
		return profiles.Firefox_123
	case "firefox_147", "firefox147":
		return profiles.Firefox_147
	case "safari_16_0":
		return profiles.Safari_16_0
	case "safari_ios_17_0":
		return profiles.Safari_IOS_17_0
	default:
		return profiles.Chrome_146
	}
}

// getTLSClient devuelve cliente tls-client con huella Chrome 146, solo para conexiones directas.
// Para conexiones por proxy se usa stdlib (ver getStdlibClient): implementación SOCKS5 de tls-client es frágil.
// comparada con net/http.
func getTLSClient() tls_client.HttpClient {
	clientCacheMu.RLock()
	if c, ok := clientCache[rtCfg().Impersonate]; ok {
		clientCacheMu.RUnlock()
		return c
	}
	clientCacheMu.RUnlock()

	clientCacheMu.Lock()
	defer clientCacheMu.Unlock()
	if c, ok := clientCache[rtCfg().Impersonate]; ok {
		return c
	}
	opts := []tls_client.HttpClientOption{
		tls_client.WithTimeoutSeconds(rtCfg().RequestTimeout),
		tls_client.WithClientProfile(resolveProfile(rtCfg().Impersonate)),
		tls_client.WithNotFollowRedirects(),
	}
	client, err := tls_client.NewHttpClient(tls_client.NewNoopLogger(), opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[client] tls-client init failed: %v\n", err)
		os.Exit(1)
	}
	clientCache[rtCfg().Impersonate] = client
	return client
}

// ─── Cliente HTTP stdlib para solicitudes por proxy ─────────────────────────────────
//
// Se usa stdlib para conexiones proxy (no tls-client) por:
// 1. http.ProxyURL de stdlib soporta nativo socks5://, socks5h://, http://, https://
//    y funciona con proxies probados.
// 2. Implementación SOCKS de tls-client devuelve EOF con algunos proxies (comprobado).
//
// Compromiso: con proxy, Google ve **nuestra propia huella TLS** (stdlib Go),
// no Chrome 146. HTTP CONNECT solo túnel; TLS es extremo-a-extremo con destino.
// JA3 medido: mismo JA3 con stdlib directo o vía proxy; con mismo proxy pero tls-client cambia JA3.
// Lo que indica que el proxy no interviene en TLS.
//
// No justifica volver a tls-client: en pruebas paralelas, tls-client Chrome_146 recibió más bloqueos que stdlib;
// diferencia pequeña; variación entre egresos llega a 36% — la huella tiene impacto limitado.
// La diferencia de huella existe, pero no se observó impacto notable en umbral de bloqueo.
//
// Sin proxy se sigue usando getTLSClient para conservar huella real utls/chrome146.

var (
	stdlibClientCache sync.Map // proxyURL -> *http.Client
)

// getStdlibClient devuelve http.Client con ruta por proxyURL.
// proxyURL no debe estar vacío (verificado por llamador).
func getStdlibClient(proxyURL string) *http.Client {
	if cached, ok := stdlibClientCache.Load(proxyURL); ok {
		return cached.(*http.Client)
	}
	t := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  false,
		// Con proxy no se fuerza HTTP/2 (algunos proxies no lo soportan).
		ForceAttemptHTTP2: false,
	}
	if u, err := url.Parse(proxyURL); err == nil {
		t.Proxy = http.ProxyURL(u)
	}
	c := &http.Client{
		Timeout:   time.Duration(rtCfg().RequestTimeout) * time.Second,
		Transport: t,
		// Igual que tls-client: no seguir redirecciones automáticamente (302 es señal diagnóstica).
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	stdlibClientCache.Store(proxyURL, c)
	return c
}

// loadCookie lee archivo de cookie (formato Netscape una línea o JSON).
// loadCookie devuelve (cadena cookie, SAPISID). Solo usado en sondas laterales;
// en solicitudes reales streamGenerate selecciona cuenta (necesita rotar y vincular egreso).
//
// Solo hay una fuente de cookies: pool de cookies (cuenta habilitada con menor last_used_at; rotación automática).
// Para repartir carga por IP. Pool vacío → modo anónimo. La ruta antigua "usar cookie única si pool vacío" se eliminó.
// El valor se importa via seedCookiesFromConfig al pool al arrancar.
//
// Tercer retorno (no usado aquí) era ID de registro para actualizar salud con markCookieByStatus.
func loadCookie() (cookie, sapisid string) {
	if a, ok := pickCookieAccount(); ok {
		return a.Cookie, extractSAPISID(a.Cookie)
	}
	return "", ""
}

func makeSAPISIDHash(sapisid string) string {
	ts := time.Now().Unix()
	h := sha1.Sum([]byte(fmt.Sprintf("%d %s https://gemini.google.com", ts, sapisid)))
	return fmt.Sprintf("SAPISIDHASH %d_%s", ts, hex.EncodeToString(h[:]))
}

// ChromeUA: plantilla UA real Chrome 146 para stdlib con proxy.
const ChromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"

// applyChromeHeaders añade cabeceras Chrome 146 para solicitudes stdlib.
// No se puede emular huella TLS/HTTP2 utls con proxy, pero las cabeceras deben ser coherentes.
func applyChromeHeaders(req *http.Request) {
	req.Header.Set("User-Agent", ChromeUA)
	req.Header.Set("Sec-CH-UA", `"Chromium";v="146", "Google Chrome";v="146", "Not?A_Brand";v="24"`)
	req.Header.Set("Sec-CH-UA-Mobile", "?0")
	req.Header.Set("Sec-CH-UA-Platform", `"Windows"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
}
