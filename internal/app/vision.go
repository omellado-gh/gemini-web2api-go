package app

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
)

// Lectura de imágenes / vídeos: se sube la imagen o el vídeo que envía el cliente como adjunto y luego se referencia en la conversación.
//
// Solo funciona con sesión iniciada —— el modo anónimo puede subir el archivo, pero en cuanto se referencia el servidor responde 1100.
// Bits de tipo de adjunto: 1=imagen, 2=vídeo, 3=archivo de texto, compartidos por la subida y la referencia (véase la tupla de archivo en gemini.go).
// Vídeo comprobado (captura de tráfico): se sube con el mismo resumable, con el 2.º campo de la tupla de archivo del payload a 2 y mime video/mp4,
// y el modelo puede leer el contenido del vídeo (responde «es un corto con una animación de iconos»).

// Límite de tamaño de una imagen individual. El upstream no da una cifra concreta, así que se toma un valor que cubra una captura de pantalla normal
// sin que una petición se alargue demasiado. Si se supera, se da error directamente en vez de forzar la subida —— si la subida es rechazada, el mensaje de error es más difícil de entender.
const maxImageBytes = 12 * 1024 * 1024

// Límite de tamaño de vídeo. El vídeo va en base64 dentro del cuerpo JSON de la petición; si es muy grande, alarga mucho la petición,
// así que se toma un valor que cubra los cortos habituales sin dejar la petición colgada. Si se supera, se da error directamente.
const maxVideoBytes = 50 * 1024 * 1024

// pendingUpload es un adjunto aún no subido. La subida real espera a elegir cuenta y salida,
// así que se transporta así desde el handler hasta streamGenerate.
type pendingUpload struct {
	Data []byte
	Name string
	Mime string
	Kind int // 1=imagen, 2=vídeo, 3=texto/archivo normal
}

// collectImages extrae las imágenes de los messages en formato OpenAI.
//
// Reconoce dos formas: image_url (OpenAI Chat) e input_image (Responses) dentro del array content.
// El origen de la imagen admite data URL y enlaces http(s); este último se descarga y luego se sube —— dar el enlace
// directamente al upstream no vale, solo reconoce adjuntos de su propio almacenamiento.
func collectImages(messages []map[string]interface{}, proxyURL string) ([]pendingUpload, error) {
	var out []pendingUpload
	for _, m := range messages {
		parts, ok := m["content"].([]interface{})
		if !ok {
			continue
		}
		for _, c := range parts {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			var src string
			switch getStr(cm, "type") {
			case "image_url":
				if iu, ok := cm["image_url"].(map[string]interface{}); ok {
					src = getStr(iu, "url")
				} else {
					src = getStr(cm, "image_url")
				}
			case "input_image":
				src = firstNonEmpty(getStr(cm, "image_url"), getStr(cm, "url"), getStr(cm, "data"))
			case "video_url":
				if vu, ok := cm["video_url"].(map[string]interface{}); ok {
					src = getStr(vu, "url")
				} else {
					src = getStr(cm, "video_url")
				}
			case "input_video":
				src = firstNonEmpty(getStr(cm, "video_url"), getStr(cm, "url"), getStr(cm, "data"))
			default:
				continue
			}
			if strings.TrimSpace(src) == "" {
				continue
			}
			img, err := materializeImage(src, proxyURL, len(out)+1)
			if err != nil {
				return nil, err
			}
			out = append(out, img)
		}
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// materializeImage convierte un origen de imagen en los bytes pendientes de subir.
func materializeImage(src, proxyURL string, idx int) (pendingUpload, error) {
	if strings.HasPrefix(src, "data:") {
		return decodeDataURL(src, idx)
	}
	if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
		return fetchImage(src, proxyURL, idx)
	}
	return pendingUpload{}, fmt.Errorf("image %d: unsupported source (want a data: URL or http(s) link)", idx)
}

// decodeDataURL analiza data:<mime>;base64,<datos>.
func decodeDataURL(src string, idx int) (pendingUpload, error) {
	comma := strings.Index(src, ",")
	if comma < 0 {
		return pendingUpload{}, fmt.Errorf("image %d: malformed data URL", idx)
	}
	meta, payload := src[5:comma], src[comma+1:]
	mime := "image/png"
	if i := strings.Index(meta, ";"); i > 0 {
		mime = meta[:i]
	} else if meta != "" {
		mime = meta
	}
	var data []byte
	var err error
	if strings.Contains(meta, "base64") {
		data, err = base64.StdEncoding.DecodeString(payload)
	} else {
		data = []byte(payload)
	}
	if err != nil {
		return pendingUpload{}, fmt.Errorf("image %d: bad base64: %w", idx, err)
	}
	return newMediaUpload(data, mime, idx)
}

// fetchImage descarga una imagen remota. Usa la misma salida que las peticiones normales: traer la imagen desde otra IP y enviar la conversación desde esta
// no aporta nada aparte de lentitud, y además expone una salida más.
func fetchImage(src, proxyURL string, idx int) (pendingUpload, error) {
	var body []byte
	var ctype string
	if proxyURL != "" {
		req, err := http.NewRequest("GET", src, nil)
		if err != nil {
			return pendingUpload{}, err
		}
		applyChromeHeaders(req)
		resp, err := getStdlibClient(proxyURL).Do(req)
		if err != nil {
			return pendingUpload{}, fmt.Errorf("image %d: fetch failed: %w", idx, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return pendingUpload{}, fmt.Errorf("image %d: fetch returned HTTP %d", idx, resp.StatusCode)
		}
		ctype = resp.Header.Get("Content-Type")
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
		if err != nil {
			return pendingUpload{}, err
		}
	} else {
		req, err := fhttp.NewRequest("GET", src, nil)
		if err != nil {
			return pendingUpload{}, err
		}
		resp, err := getTLSClient().Do(req)
		if err != nil {
			return pendingUpload{}, fmt.Errorf("image %d: fetch failed: %w", idx, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return pendingUpload{}, fmt.Errorf("image %d: fetch returned HTTP %d", idx, resp.StatusCode)
		}
		ctype = resp.Header.Get("Content-Type")
		body, err = io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
		if err != nil {
			return pendingUpload{}, err
		}
	}
	if i := strings.Index(ctype, ";"); i > 0 {
		ctype = ctype[:i]
	}
	if !strings.HasPrefix(ctype, "image/") && !strings.HasPrefix(ctype, "video/") {
		ctype = "image/png"
	}
	return newMediaUpload(body, ctype, idx)
}

// newMediaUpload decide según el mime si es imagen o vídeo:
//   - video/* → bit de tipo de adjunto 2 (igual que en la captura: tupla de archivo [ruta,2,null,"video/mp4"]), límite maxVideoBytes;
//   - el resto como imagen → tipo 1, límite maxImageBytes.
//
// Los bits de tipo 1/2 son el interruptor con que el servidor reconoce la clase de medio; si se rellenan mal, el modelo interpreta el adjunto como el tipo equivocado.
func newMediaUpload(data []byte, mime string, idx int) (pendingUpload, error) {
	if len(data) == 0 {
		return pendingUpload{}, fmt.Errorf("media %d: empty", idx)
	}
	if strings.HasPrefix(mime, "video/") {
		if len(data) > maxVideoBytes {
			return pendingUpload{}, fmt.Errorf("video %d: %d bytes exceeds the %d-byte limit",
				idx, len(data), maxVideoBytes)
		}
		return pendingUpload{
			Data: data, Mime: mime, Kind: 2,
			Name: fmt.Sprintf("video%d%s", idx, mediaExt(mime)),
		}, nil
	}
	if len(data) > maxImageBytes {
		return pendingUpload{}, fmt.Errorf("image %d: %d bytes exceeds the %d-byte limit",
			idx, len(data), maxImageBytes)
	}
	return pendingUpload{
		Data: data, Mime: mime, Kind: 1,
		Name: fmt.Sprintf("image%d%s", idx, mediaExt(mime)),
	}, nil
}

// mediaExt da una extensión según el mime. El nombre de archivo se le muestra al modelo; si la extensión no coincide, es fácil que se confunda.
func mediaExt(mime string) string {
	switch mime {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/heic":
		return ".heic"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "video/quicktime":
		return ".mov"
	default:
		if strings.HasPrefix(mime, "video/") {
			return ".mp4"
		}
		return ".png"
	}
}
