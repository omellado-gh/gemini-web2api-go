package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const Version = "4.21.0"

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	body, _ := json.Marshal(data)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	w.Write(body)
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	var data []map[string]interface{}
	for name, c := range availableModels() {
		data = append(data, map[string]interface{}{
			"id":          name,
			"object":      "model",
			"created":     1700000000,
			"owned_by":    "google",
			"description": c.Desc,
		})
	}
	writeJSON(w, 200, map[string]interface{}{"object": "list", "data": data})
}

func handleRoot(w http.ResponseWriter, r *http.Request) {
	var modelNames []string
	for n := range availableModels() {
		modelNames = append(modelNames, n)
	}
	writeJSON(w, 200, map[string]interface{}{
		"status":  "ok",
		"version": Version,
		"models":  modelNames,
	})
}

func handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.WriteHeader(204)
}

// buildUsage 用 tiktoken 算 token 数，跟 requests 表里记的口径保持一致。
// responsesAPI=true 时用 /v1/responses 的字段名（input_tokens/output_tokens）。
func buildUsage(prompt, text string, responsesAPI bool) map[string]int {
	return buildUsageWithReasoning(prompt, text, "", responsesAPI)
}

// buildUsageWithReasoning 在 usage 里单列思考链的 token 数。
//
// 思考链**不计入** completion_tokens：它是模型自己的推理过程，客户端默认不展示，
// 算进去等于让用户为看不见的输出买单（下游 newapi 是按 completion_tokens 计费的）。
// 单列在 completion_tokens_details.reasoning_tokens 里，跟 OpenAI 的做法一致，
// 想计费的人自己加。
func buildUsageWithReasoning(prompt, text, reasoning string, responsesAPI bool) map[string]int {
	// 媒体产物是 base64 data URL，上百万字符。不计进 output token：那是二进制内容，
	// 按 token 计费等于让用户为看不见的字节买单，下游 newapi 按 output token 收钱。
	in, out := countTokens(prompt), countTokens(stripDataURLs(text))
	if responsesAPI {
		return map[string]int{
			"input_tokens":  in,
			"output_tokens": out,
			"total_tokens":  in + out,
		}
	}
	u := map[string]int{
		"prompt_tokens":     in,
		"completion_tokens": out,
		"total_tokens":      in + out,
	}
	if reasoning != "" {
		u["reasoning_tokens"] = countTokens(reasoning)
	}
	return u
}

// rejectUnsupported 检查客户端传了但我们兑现不了的字段。
//
// 只拦"静默忽略会让客户端拿到错误结果"的：n>1 少给候选、图片输入被丢掉会
// 让模型答非所问。采样类参数（temperature/top_p/max_tokens/...）上游根本
// 没有对应旋钮，收下忽略即可，报错反而会挡住正常客户端。
func rejectUnsupported(req map[string]interface{}, messages []map[string]interface{}) error {
	if n, ok := req["n"].(float64); ok && n > 1 {
		return fmt.Errorf("n=%d not supported: upstream returns a single candidate", int(n))
	}
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
			switch getStr(cm, "type") {
			case "image_url", "input_image":
				// 有 cookie 就能收：图会被上传成附件再引用。匿名不行 —— 传得上去，
				// 但一引用就被服务端拒，收下只会让客户端拿到一个看不懂的失败。
				if !hasCookie() {
					return fmt.Errorf("image input needs a Google account cookie: anonymous " +
						"uploads succeed but referencing them in a conversation is rejected " +
						"upstream. Add a cookie in the admin panel (Cookie pool)")
				}
			case "video_url", "input_video":
				// 视频跟图片同理：登录态才能引用，匿名一引用就 1100。
				if !hasCookie() {
					return fmt.Errorf("video input needs a Google account cookie: anonymous " +
						"uploads succeed but referencing them in a conversation is rejected " +
						"upstream. Add a cookie in the admin panel (Cookie pool)")
				}
			case "input_audio":
				return fmt.Errorf("audio input not supported")
			}
		}
	}
	return nil
}

// callGemini ejecuta un turno por la ruta de un solo turno y, si se buffereó (onDelta == nil) y
// hay tools, aplica un reintento correctivo cuando el tool_choice no se cumplió o los argumentos
// no validan. Adopta el reintento solo si resuelve el problema; si no, conserva la primera
// respuesta. En streaming real (onDelta != nil) no se reintenta: los bytes ya se enviaron.
func callGemini(prompt, latest string, mc ModelConfig, tools []map[string]interface{},
	toolChoice interface{}, images []pendingUpload, onDelta, onReasoning func(string)) (string, []ToolCall, *StreamResult, error) {
	text, toolCalls, res, err := callGeminiOnce(prompt, latest, mc, tools, images, onDelta, onReasoning)
	if err != nil || len(tools) == 0 || onDelta != nil {
		return text, toolCalls, res, err
	}
	schemas := toolSchemas(tools)
	if bad, argErrs := needsToolRetry(toolChoice, toolCalls, schemas); bad {
		reinforced := prompt + "\n\n" + toolRetryReminder(toolChoice, argErrs)
		t2, c2, r2, e2 := callGeminiOnce(reinforced, latest, mc, tools, images, nil, onReasoning)
		if e2 == nil {
			if stillBad, _ := needsToolRetry(toolChoice, c2, schemas); !stillBad {
				logf("[tools] reintento correctivo adoptado (tool_choice/argumentos)")
				return t2, c2, r2, nil
			}
			logf("[tools] el reintento no resolvió; se conserva la primera respuesta")
		} else {
			logf("[tools] reintento correctivo falló: %v", e2)
		}
	}
	return text, toolCalls, res, err
}

func callGeminiOnce(prompt, latest string, mc ModelConfig, tools []map[string]interface{},
	images []pendingUpload, onDelta, onReasoning func(string)) (string, []ToolCall, *StreamResult, error) {
	res, err := streamGenerateWithFiles(prompt, latest, mc, images, onDelta, onReasoning)
	if err != nil {
		// res no es nil: en caso de fallo solo trae la atribución (qué cuenta / qué salida), para recordRequest
		return "", nil, res, err
	}
	text := extractResponseText(res.Raw)
	// Canvas: el documento HTML va en línea en la respuesta (a diferencia de imagen/música, que hay que descargar); se extrae de la estructura immersive.
	// La extracción estándar de texto solo obtiene el preamble ("I will generate…"); el documento está en inner[4][0][30]…,
	// así que se toma aparte con extractCanvasDoc.
	if mc.Tool == toolCanvas {
		doc := extractCanvasDoc(res.Raw)
		if doc == "" {
			return "", nil, res, fmt.Errorf("canvas generation failed: no HTML document in response (raw %d bytes)", len(res.Raw))
		}
		if text != "" && !strings.Contains(doc, text) {
			return text + "\n\n" + doc, nil, res, nil // preamble + documento
		}
		return doc, nil, res, nil
	}
	// Modelos multimedia (generación de imagen/música): si la generación da 200 pero no se recuperan los bytes del producto, se da error directamente en vez de devolver
	// un producto a medias solo con texto y sin imagen —— lo que el cliente quiere es esa imagen / esa música.
	if mc.Tool == toolImage || mc.Tool == toolMusic || mc.Tool == toolVideo {
		if len(res.Artifacts) == 0 {
			msg := res.MediaErr
			if msg == "" {
				msg = "fallo al recuperar el producto multimedia"
			}
			return "", nil, res, fmt.Errorf("media generation succeeded but artifact retrieval failed: %s", msg)
		}
		// El producto se añade al cuerpo como data URL en base64 (puede no haber cuerpo y solo imagen/música/vídeo).
		text = appendArtifactMarkdown(text, res.Artifacts)
		return text, nil, res, nil
	}
	if text == "" {
		// Cuando el upstream rechaza solo devuelve un frame de fin, sin frame de contenido (medido: al ser rechazado, el raw es de solo 216
		// bytes). En este caso hay que dar error: antes se trataba como respuesta vacía y se devolvía 200 + content:null,
		// y el cliente no podía ver que la petición había fallado en realidad.
		// Ojo: no se puede usar BardErrorInfo para detectar el error —— el frame de fin de una respuesta normal también lleva ese código.
		return "", nil, res, fmt.Errorf("upstream returned no content frame (raw %d bytes)", len(res.Raw))
	}
	var toolCalls []ToolCall
	if len(tools) > 0 {
		text, toolCalls = parseToolCalls(text, toolNameSet(tools))
	}
	return text, toolCalls, res, nil
}

// recordRequest writes one row of metadata to the requests table.
// Privacy: the prompt/response strings themselves are never persisted —
// only their length, model name, latency, status, and proxy info.
func recordRequest(endpoint, model, prompt, response string, res *StreamResult, status int, errStr string, stream bool) {
	// 媒体产物是超长 base64，剥掉再算 token/长度：既不让它污染统计，也免得在请求线程里
	// 对上百万字符跑 tiktoken 白白拖慢。
	response = stripDataURLs(response)
	r := &RequestRow{
		TS:            time.Now().Unix(),
		Model:         model,
		Status:        status,
		Error:         errStr,
		PromptChars:   len(prompt),
		ResponseChars: len(response),
		PromptTokens:  countTokens(prompt),
		OutputTokens:  countTokens(response),
		Endpoint:      endpoint,
	}
	if stream {
		r.Stream = 1
	}
	if res != nil {
		r.UpstreamModel = res.UpstreamModel
		r.TotalMs = res.TotalMs
		r.TTFBMs = &res.TTFBMs
		if res.ProxyID > 0 {
			r.ProxyID = &res.ProxyID
		}
		r.ProxyName = res.ProxyName
		if res.AccountID > 0 {
			r.AccountID = &res.AccountID
		}
		r.AccountLabel = res.AccountLabel
	}
	go insertRequest(r)
}

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{"message": "invalid JSON"}})
		return
	}

	modelInput := getStr(req, "model")
	if modelInput == "" {
		modelInput = rtCfg().DefaultModel
	}
	modelName, modelCfg, err := resolveModel(modelInput)
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}

	messagesRaw, _ := req["messages"].([]interface{})
	var messages []map[string]interface{}
	for _, m := range messagesRaw {
		if mm, ok := m.(map[string]interface{}); ok {
			messages = append(messages, mm)
		}
	}

	toolsRaw, _ := req["tools"].([]interface{})
	var tools []map[string]interface{}
	for _, t := range toolsRaw {
		if tm, ok := t.(map[string]interface{}); ok {
			tools = append(tools, tm)
		}
	}

	if err := rejectUnsupported(req, messages); err != nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{
			"message": err.Error(), "type": "invalid_request_error"}})
		return
	}

	// 图片先解出来带着，真正上传要等挑完账号和出口（见 streamGenerate）。
	images, err := collectImages(messages, "")
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{
			"message": err.Error(), "type": "invalid_request_error"}})
		return
	}

	toolChoice := effectiveToolChoice(req, tools, messages)
	prompt, latest := messagesToPrompt(messages, tools, toolChoice, parallelToolCalls(req))
	if strings.TrimSpace(prompt) == "" && len(images) == 0 {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{"message": "empty prompt"}})
		return
	}

	stream, _ := req["stream"].(bool)
	includeUsage := false
	if so, ok := req["stream_options"].(map[string]interface{}); ok {
		includeUsage, _ = so["include_usage"].(bool)
	}

	cid := "chatcmpl-" + randHex(12)
	created := time.Now().Unix()

	// Con tools se bufferea la respuesta completa en vez de emitir en streaming: el bloque
	// ```tool_call``` necesita el texto entero para parsearse, y sobre todo hace falta poder
	// validar/corregir/reintentar (tool_choice y argumentos) antes de enviar nada — una vez
	// emitidos los bytes ya no se pueden retirar. El cuerpo se emite al final en el mismo formato
	// SSE. Sin tools sí hay streaming real (cada frame del upstream se reenvía como delta).
	var sse *sseWriter
	var onDelta, onReasoning func(string)
	if stream {
		sse = newSSEWriter(w, cid, created, modelName)
		onReasoning = sse.SendReasoning
		if len(tools) == 0 {
			onDelta = sse.SendContent
		}
	}

	var text string
	var toolCalls []ToolCall
	var res *StreamResult
	if shouldUseConv(modelCfg, len(images) > 0, len(tools) > 0) {
		// Multironda: se reconoce la continuación por el prefijo del historial; si coincide, solo se envían los mensajes nuevos y el historial se queda en el servidor. Con tools también va por aquí.
		text, toolCalls, res, err = callGeminiConv(messages, modelCfg, tools, toolChoice, parallelToolCalls(req), onDelta, onReasoning)
	} else {
		text, toolCalls, res, err = callGemini(prompt, latest, modelCfg, tools, toolChoice, images, onDelta, onReasoning)
	}
	if err != nil {
		recordRequest("chat.completions", modelName, prompt, "", res, 502, err.Error(), stream)
		if sse != nil && sse.Started() {
			sse.Fail(err) // el stream ya está abierto; el código de estado HTTP ya no se puede cambiar
			return
		}
		if ptl, ok := err.(*PromptTooLongError); ok {
			// Se da 400 explícitamente en vez de dejar que el upstream recorte la pregunta del usuario —— así el cliente recibiría
			// un 200 que responde a otra cosa y no podría ver que la petición no se envió.
			writeJSON(w, 400, map[string]interface{}{"error": map[string]string{
				"message": ptl.Error(), "type": "invalid_request_error",
				"code": "context_length_exceeded"}})
			return
		}
		if rle, ok := err.(*RateLimitError); ok {
			writeJSON(w, 429, map[string]interface{}{"error": map[string]string{
				"message": rle.Error(),
				"type":    "rate_limit_exceeded",
				"code":    "ip_slot_full",
			}})
			return
		}
		writeJSON(w, 502, map[string]interface{}{"error": map[string]string{"message": "upstream error: " + err.Error()}})
		return
	}

	// Validación final: el reintento correctivo (tool_choice y argumentos) ya se intentó dentro
	// de callGemini/callGeminiConv cuando se buffereó. Aquí solo queda registrar lo irresoluble.
	if len(tools) > 0 {
		for _, verr := range validateToolCalls(toolCalls, toolSchemas(tools)) {
			logf("[tools] argumentos aún no válidos tras el reintento: %v", verr)
		}
		if toolChoiceUnsatisfied(toolChoice, toolCalls) {
			logf("[tools] tool_choice sigue sin cumplirse (stream=%v)", stream)
		}
	}

	msg := map[string]interface{}{"role": "assistant"}
	if text != "" {
		msg["content"] = text
	} else {
		msg["content"] = nil
	}
	// La cadena de pensamiento va por reasoning_content —— el estándar de facto que popularizó DeepSeek-R1; tanto newapi como
	// los clientes principales lo reconocen y lo renderizan como «proceso de pensamiento» plegable. Solo lo tiene 3.1 Pro; el resto va vacío.
	if res != nil && res.Reasoning != "" {
		msg["reasoning_content"] = res.Reasoning
	}
	finish := "stop"
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
		finish = "tool_calls"
	} else if len(tools) > 0 && hasUnclosedToolFence(text) {
		// El modelo intentó llamar a una herramienta pero la salida se cortó a mitad del bloque
		// (no hay tool_call parseable). Se señaliza "length" para que el cliente sepa que puede
		// continuar, en vez de "stop", que le haría creer que la respuesta está completa.
		finish = "length"
	}

	recordRequest("chat.completions", modelName, prompt, text, res, 200, "", stream)
	if stream {
		var usage map[string]int
		if includeUsage {
			usage = usageOf(prompt, text, res)
		}
		// La cadena de pensamiento debe completarse antes del cuerpo, para mantener el orden
		// «primero pensar, luego responder».
		if res != nil && res.Reasoning != "" {
			if rest := remainingOf(res.Reasoning, res.EmittedReasoning); rest != "" {
				sse.SendReasoning(rest)
			}
		}
		// Sin herramientas hubo streaming real: aquí solo queda la cola. Con herramientas se
		// buffereó (Emitted vacío), así que se envía el cuerpo completo.
		if rest := remainingOf(text, res.Emitted); rest != "" {
			sse.SendContent(rest)
		}
		if len(toolCalls) > 0 {
			sse.SendToolCalls(toolCalls)
		}
		sse.Finish(finish, usage)
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"id":      cid,
		"object":  "chat.completion",
		"created": created,
		"model":   modelName,
		"choices": []map[string]interface{}{{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
		"usage": usageOf(prompt, text, res),
	})
}

// usageOf 是 buildUsageWithReasoning 的便捷包装，res 可能为 nil。
func usageOf(prompt, text string, res *StreamResult) map[string]int {
	r := ""
	if res != nil {
		r = res.Reasoning
	}
	return buildUsageWithReasoning(prompt, text, r, false)
}

// remainingText 返回最终文本里还没通过 onDelta 发出去的部分。
// 真流式下通常只剩末尾一点或为空；没走真流式时 Emitted 为空，返回全文。
func remainingText(text string, res *StreamResult) string {
	if res == nil {
		return text
	}
	return remainingOf(text, res.Emitted)
}

// remainingOf devuelve la cola de full que aún no se ha enviado. Si emitted está vacío, devuelve el texto completo.
func remainingOf(full, emitted string) string {
	if emitted == "" {
		return full
	}
	if strings.HasPrefix(full, emitted) {
		return full[len(emitted):]
	}
	// 前缀对不上（上游中途改写过），已发的收不回，不再补发以免重复。
	return ""
}

// handleResponses implements OpenAI's /v1/responses (Codex CLI format).
func handleResponses(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{"message": "invalid JSON"}})
		return
	}

	modelInput := getStr(req, "model")
	if modelInput == "" {
		modelInput = rtCfg().DefaultModel
	}
	modelName, modelCfg, err := resolveModel(modelInput)
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}

	var messages []map[string]interface{}
	if instr := getStr(req, "instructions"); instr != "" {
		messages = append(messages, map[string]interface{}{"role": "system", "content": instr})
	}

	switch input := req["input"].(type) {
	case string:
		messages = append(messages, map[string]interface{}{"role": "user", "content": input})
	case []interface{}:
		for _, raw := range input {
			switch item := raw.(type) {
			case string:
				messages = append(messages, map[string]interface{}{"role": "user", "content": item})
			case map[string]interface{}:
				if t := getStr(item, "type"); t == "function_call_output" {
					messages = append(messages, map[string]interface{}{
						"role":         "tool",
						"tool_call_id": getStr(item, "call_id"),
						"name":         getStr(item, "name"),
						"content":      valueToString(item["output"]),
					})
					continue
				}
				role := getStr(item, "role")
				if role == "assistant" || (getStr(item, "type") == "message" && role == "assistant") {
					var textAcc strings.Builder
					var tcList []map[string]interface{}
					if cp, ok := item["content"].([]interface{}); ok {
						for _, c := range cp {
							cm, ok := c.(map[string]interface{})
							if !ok {
								continue
							}
							switch getStr(cm, "type") {
							case "output_text":
								textAcc.WriteString(getStr(cm, "text"))
							case "function_call":
								tcList = append(tcList, cm)
							}
						}
					} else if s, ok := item["content"].(string); ok {
						textAcc.WriteString(s)
					}
					m := map[string]interface{}{"role": "assistant", "content": textAcc.String()}
					if len(tcList) > 0 {
						var tcs []map[string]interface{}
						for i, tc := range tcList {
							id := getStr(tc, "call_id")
							if id == "" {
								id = fmt.Sprintf("call_%d", i)
							}
							tcs = append(tcs, map[string]interface{}{
								"id":   id,
								"type": "function",
								"function": map[string]interface{}{
									"name":      getStr(tc, "name"),
									"arguments": getStr(tc, "arguments"),
								},
							})
						}
						m["tool_calls"] = tcs
					}
					messages = append(messages, m)
				} else {
					if role == "" {
						role = "user"
					}
					content := contentToString(item["content"])
					messages = append(messages, map[string]interface{}{"role": role, "content": content})
				}
			}
		}
	}

	toolsRaw, _ := req["tools"].([]interface{})
	var tools []map[string]interface{}
	for _, t := range toolsRaw {
		tm, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		// Normalize Responses API tool shape to Chat Completions shape.
		if getStr(tm, "type") == "function" && tm["function"] == nil {
			tools = append(tools, map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        getStr(tm, "name"),
					"description": getStr(tm, "description"),
					"parameters":  tm["parameters"],
				},
			})
		} else {
			tools = append(tools, tm)
		}
	}

	if err := rejectUnsupported(req, messages); err != nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{
			"message": err.Error(), "type": "invalid_request_error"}})
		return
	}

	images, err := collectImages(messages, "")
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{
			"message": err.Error(), "type": "invalid_request_error"}})
		return
	}

	toolChoice := effectiveToolChoice(req, tools, messages)
	prompt, latest := messagesToPrompt(messages, tools, toolChoice, parallelToolCalls(req))
	if strings.TrimSpace(prompt) == "" && len(images) == 0 {
		writeJSON(w, 400, map[string]interface{}{"error": map[string]string{"message": "empty input"}})
		return
	}

	stream, _ := req["stream"].(bool)
	rid := "resp_" + randHex(16)
	mid := "msg_" + randHex(12)

	// El protocolo de streaming de Responses exige declarar primero el item con response.output_item.added para poder enviarle
	// output_text.delta; si falta, clientes estrictos como Codex reportan "OutputTextDelta without active
	// item". msgIndex/nextIdx asignan un número a cada output item; ensureMsg declara el item message de forma perezosa
	// (solo se envía en el primer delta; en una ronda de solo llamadas a herramienta no se envía un message vacío).
	msgIndex := -1
	nextIdx := 0
	var ensureMsg func()

	// En streaming hay que enviar primero la cabecera y response.created para poder ir empujando delta mientras se recibe.
	// El coste es que, una vez abierto el stream, el código de estado HTTP ya no se puede cambiar; un fallo del upstream solo se puede comunicar
	// con el evento response.failed —— el mismo compromiso que en la ruta /v1/chat/completions.
	var writeEvent func(string, interface{})
	var emitDelta func(string)
	var emitFunctionCall func(map[string]interface{})
	var onDelta func(string)
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no") // desactiva el búfer del reverse proxy para SSE; véase sse.go
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		writeEvent = func(eventType string, payload interface{}) {
			pj, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, pj)
			if flusher != nil {
				flusher.Flush()
			}
		}
		writeEvent("response.created", map[string]interface{}{
			"type": "response.created",
			"response": map[string]interface{}{
				"id":     rid,
				"object": "response",
				"status": "in_progress",
				"model":  modelName,
				"output": []interface{}{},
			},
		})
		ensureMsg = func() {
			if msgIndex >= 0 {
				return
			}
			msgIndex = nextIdx
			nextIdx++
			writeEvent("response.output_item.added", map[string]interface{}{
				"type":         "response.output_item.added",
				"output_index": msgIndex,
				"item": map[string]interface{}{
					"id": mid, "type": "message", "role": "assistant",
					"status": "in_progress", "content": []interface{}{},
				},
			})
			writeEvent("response.content_part.added", map[string]interface{}{
				"type": "response.content_part.added", "item_id": mid,
				"output_index": msgIndex, "content_index": 0,
				"part": map[string]interface{}{"type": "output_text", "text": "", "annotations": []interface{}{}},
			})
		}
		emitDelta = func(d string) {
			ensureMsg()
			writeEvent("response.output_text.delta", map[string]interface{}{
				"type":          "response.output_text.delta",
				"item_id":       mid,
				"output_index":  msgIndex,
				"content_index": 0,
				"delta":         d,
			})
		}
		// emitFunctionCall emite el ciclo added → arguments.done → item.done de una llamada.
		// Se invoca en cuanto la compuerta cierra el bloque (streaming incremental) y también
		// como red de seguridad al final para lo que ella no hubiera cubierto.
		emitFunctionCall = func(item map[string]interface{}) {
			idx := nextIdx
			nextIdx++
			writeEvent("response.output_item.added", map[string]interface{}{
				"type": "response.output_item.added", "output_index": idx, "item": item,
			})
			writeEvent("response.function_call_arguments.done", map[string]interface{}{
				"type":      "response.function_call_arguments.done",
				"item_id":   item["id"],
				"call_id":   item["call_id"],
				"name":      item["name"],
				"arguments": item["arguments"],
			})
			writeEvent("response.output_item.done", map[string]interface{}{
				"type": "response.output_item.done", "output_index": idx, "item": item,
			})
		}
		// Con tools se bufferea la respuesta (igual que en la ruta chat) para poder validar,
		// corregir y reintentar antes de emitir; los eventos de la función se emiten al final.
		if len(tools) == 0 {
			onDelta = emitDelta
		}
	}

	// onReasoning se pasa nil: la API Responses tiene su propia forma de eventos reasoning, que no es compatible con el
	// reasoning_content de chat; por ahora esta ruta no expone la cadena de pensamiento.
	var text string
	var toolCalls []ToolCall
	var res *StreamResult
	if shouldUseConv(modelCfg, len(images) > 0, len(tools) > 0) {
		text, toolCalls, res, err = callGeminiConv(messages, modelCfg, tools, toolChoice, parallelToolCalls(req), onDelta, nil)
	} else {
		text, toolCalls, res, err = callGemini(prompt, latest, modelCfg, tools, toolChoice, images, onDelta, nil)
	}
	if err != nil {
		recordRequest("responses", modelName, prompt, "", res, 502, err.Error(), stream)
		if stream {
			writeEvent("response.failed", map[string]interface{}{
				"type": "response.failed",
				"response": map[string]interface{}{
					"id":     rid,
					"object": "response",
					"status": "failed",
					"model":  modelName,
					"error":  map[string]string{"message": err.Error()},
				},
			})
			return
		}
		if ptl, ok := err.(*PromptTooLongError); ok {
			// 明确报 400 而不是发出去让上游把用户的问题截掉 —— 那样客户端拿到的是
			// 一个答非所问的 200，根本看不出请求其实没送到。
			writeJSON(w, 400, map[string]interface{}{"error": map[string]string{
				"message": ptl.Error(), "type": "invalid_request_error",
				"code": "context_length_exceeded"}})
			return
		}
		if rle, ok := err.(*RateLimitError); ok {
			writeJSON(w, 429, map[string]interface{}{"error": map[string]string{
				"message": rle.Error(),
				"type":    "rate_limit_exceeded",
				"code":    "ip_slot_full",
			}})
			return
		}
		writeJSON(w, 502, map[string]interface{}{"error": map[string]string{"message": "upstream error: " + err.Error()}})
		return
	}

	// Validación final (el reintento correctivo ya se intentó dentro de callGemini/callGeminiConv).
	if len(tools) > 0 {
		for _, verr := range validateToolCalls(toolCalls, toolSchemas(tools)) {
			logf("[tools] argumentos aún no válidos tras el reintento: %v", verr)
		}
		if toolChoiceUnsatisfied(toolChoice, toolCalls) {
			logf("[tools] tool_choice sigue sin cumplirse (responses, stream=%v)", stream)
		}
	}

	var output []map[string]interface{}
	for _, tc := range toolCalls {
		output = append(output, map[string]interface{}{
			"type":      "function_call",
			"id":        tc.ID,
			"call_id":   tc.ID,
			"name":      tc.Function.Name,
			"arguments": tc.Function.Arguments,
			"status":    "completed",
		})
	}
	if text != "" || len(toolCalls) == 0 {
		output = append(output, map[string]interface{}{
			"type":   "message",
			"id":     mid,
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]interface{}{{
				"type":        "output_text",
				"text":        text,
				"annotations": []interface{}{},
			}},
		})
	}

	// Salida cortada a mitad de un bloque tool_call sin llamada parseable: se marca incompleta
	// para que Codex sepa que puede continuar, en vez de "completed" sin tool_calls.
	incomplete := len(toolCalls) == 0 && len(tools) > 0 && hasUnclosedToolFence(text)
	respStatus := "completed"
	if incomplete {
		respStatus = "incomplete"
	}

	recordRequest("responses", modelName, prompt, text, res, 200, "", stream)
	if stream {
		// Se emiten los items en el mismo orden que el array `output` para que los índices cuadren.
		// Sin herramientas hubo streaming real, así que del mensaje solo queda la cola (Emitted);
		// con herramientas se buffereó y se envía el cuerpo completo.
		for _, item := range output {
			switch item["type"] {
			case "function_call":
				// El item de llamada a herramienta también debe envolverse con added → done para que Codex lo reconozca.
				emitFunctionCall(item)
			case "message":
				ensureMsg() // si no hubo delta pero sí cuerpo, aquí se completa la declaración del message item
				if rest := remainingOf(text, res.Emitted); rest != "" {
					emitDelta(rest)
				}
				if cps, ok := item["content"].([]map[string]interface{}); ok {
					for ci, cp := range cps {
						writeEvent("response.output_text.done", map[string]interface{}{
							"type":          "response.output_text.done",
							"item_id":       item["id"],
							"output_index":  msgIndex,
							"content_index": ci,
							"text":          cp["text"],
						})
						writeEvent("response.content_part.done", map[string]interface{}{
							"type": "response.content_part.done", "item_id": item["id"],
							"output_index": msgIndex, "content_index": ci,
							"part": map[string]interface{}{"type": "output_text", "text": cp["text"], "annotations": []interface{}{}},
						})
					}
				}
				writeEvent("response.output_item.done", map[string]interface{}{
					"type": "response.output_item.done", "output_index": msgIndex, "item": item,
				})
			}
		}
		respObj := map[string]interface{}{
			"id":     rid,
			"object": "response",
			"status": respStatus,
			"model":  modelName,
			"output": output,
			"usage":  buildUsage(prompt, text, true),
		}
		if incomplete {
			respObj["incomplete_details"] = map[string]interface{}{"reason": "max_output_tokens"}
		}
		writeEvent("response.completed", map[string]interface{}{
			"type":     "response.completed",
			"response": respObj,
		})
		return
	}

	respOut := map[string]interface{}{
		"id":         rid,
		"object":     "response",
		"created_at": time.Now().Unix(),
		"status":     respStatus,
		"model":      modelName,
		"output":     output,
		"usage":      buildUsage(prompt, text, true),
	}
	if incomplete {
		respOut["incomplete_details"] = map[string]interface{}{"reason": "max_output_tokens"}
	}
	writeJSON(w, 200, respOut)
}
