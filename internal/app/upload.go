package app

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
)

// Dos pasos resumable, alineado con la captura del navegador. También existe la vía de un
// único multipart contra content-push.googleapis.com (que también sube bien); se eligen dos
// pasos solo por ser idéntico al navegador.
const uploadHost = "https://push.clients6.google.com/upload/"

// uploadBytes sube bytes y devuelve la ruta de referencia (con forma /contrib_service/ttl_1d/…). La subida no lleva mime.
//
// En anónimo también sube bien, pero el archivo subido devolverá 1100 al referenciarlo en una conversación,
// por lo que quien llama debe asegurarse de tener cookie.
// proxyURL debe usar la misma salida que la petición formal; de lo contrario, para Google son dos sesiones que comparten archivo.
func uploadBytes(cookie, proxyURL string, data []byte, filename string) (string, error) {
	pushID, pctx, err := getUploadTokens(cookie, proxyURL)
	if err != nil {
		return "", fmt.Errorf("no se pudieron obtener los parámetros de la página de subida: %w", err)
	}
	if pushID == "" {
		return "", fmt.Errorf("la página no tiene push_id; no se puede subir")
	}

	base := map[string]string{
		"Origin":         "https://gemini.google.com",
		"Referer":        "https://gemini.google.com/",
		"X-Tenant-Id":    "bard-storage",
		"Push-ID":        pushID,
		"Accept":         "*/*",
		"Sec-Fetch-Site": "same-site",
		"Sec-Fetch-Mode": "cors",
		"Sec-Fetch-Dest": "empty",
	}
	if pctx != "" {
		base["X-Client-Pctx"] = pctx
	}
	if cookie != "" {
		base["Cookie"] = cookie
	}
	with := func(extra map[string]string) map[string]string {
		h := make(map[string]string, len(base)+len(extra))
		for k, v := range base {
			h[k] = v
		}
		for k, v := range extra {
			h[k] = v
		}
		return h
	}

	// start obtiene una URL de subida de un solo uso. El body es texto plano "File name: xxx"; aunque el
	// Content-Type diga urlencoded, no hay que adivinar la forma del body por él.
	startHeaders := with(map[string]string{
		"Content-Type":                        "application/x-www-form-urlencoded;charset=UTF-8",
		"X-Goog-Upload-Command":               "start",
		"X-Goog-Upload-Protocol":              "resumable",
		"X-Goog-Upload-Header-Content-Length": strconv.Itoa(len(data)),
	})
	status, respHead, body, err := uploadPost(uploadHost, startHeaders,
		[]byte("File name: "+sanitizeUploadName(filename)), proxyURL)
	if err != nil {
		return "", fmt.Errorf("fallo en el start de la subida: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("el start de la subida devolvió HTTP %d: %s", status, truncate(string(body), 160))
	}
	putURL := respHead["x-goog-upload-url"]
	if putURL == "" {
		return "", fmt.Errorf("el start de la subida no devolvió x-goog-upload-url")
	}

	// Envía los bytes y finaliza
	upHeaders := with(map[string]string{
		"Content-Type":          "application/x-www-form-urlencoded;charset=utf-8",
		"X-Goog-Upload-Command": "upload, finalize",
		"X-Goog-Upload-Offset":  "0",
	})
	status, _, body, err = uploadPost(putURL, upHeaders, data, proxyURL)
	if err != nil {
		return "", fmt.Errorf("fallo en el finalize de la subida: %w", err)
	}
	if status != 200 {
		return "", fmt.Errorf("el finalize de la subida devolvió HTTP %d: %s", status, truncate(string(body), 160))
	}
	// El cuerpo de la respuesta es la ruta de referencia. Si no empieza por "/" es una página de error;
	// metida en el payload el servidor puede no dar error, pero el adjunto que ve el modelo está vacío.
	ref := strings.TrimSpace(string(body))
	if !strings.HasPrefix(ref, "/") {
		return "", fmt.Errorf("la subida no devolvió una ruta de referencia: %s", truncate(ref, 160))
	}
	return ref, nil
}

// sanitizeUploadName elimina los caracteres que romperían el cuerpo de la petición; el nombre de archivo entra tal cual en el body del start.
func sanitizeUploadName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "upload.bin"
	}
	return strings.NewReplacer("\r", "", "\n", "").Replace(name)
}

// uploadPost envía el POST por la misma salida que la petición principal y devuelve también las cabeceras de respuesta (el start necesita x-goog-upload-url).
func uploadPost(url string, headers map[string]string, body []byte, proxyURL string) (
	int, map[string]string, []byte, error) {
	if proxyURL != "" {
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
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
		h := map[string]string{}
		for k := range resp.Header {
			h[strings.ToLower(k)] = resp.Header.Get(k)
		}
		return resp.StatusCode, h, b, err
	}
	req, err := fhttp.NewRequest("POST", url, bytes.NewReader(body))
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
	h := map[string]string{}
	for k := range resp.Header {
		h[strings.ToLower(k)] = resp.Header.Get(k)
	}
	return resp.StatusCode, h, b, err
}
