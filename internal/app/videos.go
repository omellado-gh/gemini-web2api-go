package app

// /v1/videos —— endpoint asíncrono de generación de vídeo con la forma de OpenAI (Sora).
//
// El vídeo de gemini tarda de decenas de segundos a minutos; bloquear una petición HTTP de forma síncrona no es realista, así que se sigue el esquema asíncrono de OpenAI Sora:
//   POST /v1/videos               crea la tarea y devuelve al instante {id, status:"queued"}
//   GET  /v1/videos/{id}          consulta el estado queued|in_progress|completed|failed
//   GET  /v1/videos/{id}/content  descarga el MP4 al terminar
// Por debajo reutiliza callGemini (la misma cadena que model=gemini-video en /v1/chat/completions),
// solo que traslada «bloquear esperando el resultado» a una goroutine de fondo y el frente devuelve el id al instante.

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

type videoJob struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Model     string `json:"model"`
	Status    string `json:"status"` // queued | in_progress | completed | failed
	CreatedAt int64  `json:"created_at"`
	Prompt    string `json:"prompt,omitempty"`
	Error     string `json:"error,omitempty"`

	mp4  []byte // bytes del vídeo al terminar; no entran en el JSON
	mime string
}

var (
	videoJobs   = map[string]*videoJob{}
	videoJobsMu sync.Mutex
)

func putVideoJob(j *videoJob) {
	videoJobsMu.Lock()
	// De paso se limpian las tareas de más de 2 horas para que no se acumulen en memoria.
	cutoff := time.Now().Unix() - 2*3600
	for id, old := range videoJobs {
		if old.CreatedAt < cutoff {
			delete(videoJobs, id)
		}
	}
	videoJobs[j.ID] = j
	videoJobsMu.Unlock()
}

func getVideoJob(id string) *videoJob {
	videoJobsMu.Lock()
	defer videoJobsMu.Unlock()
	return videoJobs[id]
}

// handleCreateVideo gestiona POST /v1/videos.
func handleCreateVideo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": map[string]string{"message": "method not allowed"}})
		return
	}
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]string{"message": "bad json body"}})
		return
	}
	prompt, _ := req["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		writeJSON(w, 400, map[string]any{"error": map[string]string{"message": "missing prompt", "type": "invalid_request_error"}})
		return
	}
	model, _ := req["model"].(string)
	if model == "" {
		model = "gemini-video"
	}
	// Se valida que el modelo sea realmente un modelo de vídeo (se mira la configuración base, sin usar resolveModel —— sin cookie, este
	// devuelve directamente el error «se necesita cookie» para los modelos con sesión, y ese error debe dejarse al job; no hay que confundirlo aquí con «no es un modelo de vídeo»).
	if base, ok := Models[model]; !ok || base.Tool != toolVideo {
		writeJSON(w, 400, map[string]any{"error": map[string]string{
			"message": "model must be a video model (gemini-video)", "type": "invalid_request_error"}})
		return
	}
	j := &videoJob{
		ID:        "video_" + randHex(16),
		Object:    "video",
		Model:     model,
		Status:    "queued",
		CreatedAt: time.Now().Unix(),
		Prompt:    prompt,
	}
	putVideoJob(j)
	go runVideoJob(j)
	writeJSON(w, 200, j)
}

// runVideoJob ejecuta la generación de vídeo en segundo plano y escribe el resultado de vuelta al job.
func runVideoJob(j *videoJob) {
	videoJobsMu.Lock()
	j.Status = "in_progress"
	videoJobsMu.Unlock()

	_, mc, err := resolveModel(j.Model)
	if err != nil {
		finishVideoJob(j, nil, "", err.Error())
		return
	}
	_, _, res, err := callGemini(j.Prompt, j.Prompt, mc, nil, nil, nil, nil, nil)
	if err != nil {
		recordRequest("videos", j.Model, j.Prompt, "", res, 502, err.Error(), false)
		finishVideoJob(j, nil, "", err.Error())
		return
	}
	if res == nil || len(res.Artifacts) == 0 {
		recordRequest("videos", j.Model, j.Prompt, "", res, 502, "no video produced", false)
		finishVideoJob(j, nil, "", "no video produced")
		return
	}
	a := res.Artifacts[0]
	recordRequest("videos", j.Model, j.Prompt, "", res, 200, "", false)
	finishVideoJob(j, a.Data, a.Mime, "")
}

func finishVideoJob(j *videoJob, mp4 []byte, mime, errStr string) {
	videoJobsMu.Lock()
	defer videoJobsMu.Unlock()
	if errStr != "" {
		j.Status = "failed"
		j.Error = errStr
		return
	}
	j.mp4 = mp4
	j.mime = mime
	if j.mime == "" {
		j.mime = "video/mp4"
	}
	j.Status = "completed"
}

// handleVideoItem gestiona GET /v1/videos/{id} y /v1/videos/{id}/content.
func handleVideoItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]any{"error": map[string]string{"message": "method not allowed"}})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/v1/videos/")
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	j := getVideoJob(id)
	if j == nil {
		writeJSON(w, 404, map[string]any{"error": map[string]string{"message": "video job not found", "type": "not_found"}})
		return
	}
	if len(parts) == 2 && parts[1] == "content" {
		videoJobsMu.Lock()
		status, mp4, mime := j.Status, j.mp4, j.mime
		videoJobsMu.Unlock()
		if status != "completed" || len(mp4) == 0 {
			writeJSON(w, 404, map[string]any{"error": map[string]string{
				"message": "video not ready (status=" + status + ")", "type": "not_found"}})
			return
		}
		w.Header().Set("Content-Type", mime)
		w.WriteHeader(200)
		_, _ = w.Write(mp4)
		return
	}
	writeJSON(w, 200, j)
}
