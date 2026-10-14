package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/google/uuid"
)

// convState 是一路 Gemini 原生会话的状态。整条会话绑定同一个 cookie / 出口，中途不换
// —— 换了 Gemini 就当成另一个会话，续接的 cid 失效。
type convState struct {
	cid, rid, rcid, tok26 string
	turn                  int    // 会话内轮次索引，填 inner[17]，逐轮 +1
	cookie                string // 会话绑定 cookie：登录号的 cookie，或匿名 /app 拿的 session
	sapisid               string // 登录态算 SAPISIDHASH 用；匿名为空
	isLogin               bool
	accountID             int64
	proxyID               int64
	proxyURL              string
	updated               time.Time
}

const (
	// convTTL 跟导出 cookie ~2 小时寿命对齐，过期的会话状态没有复用价值。
	convTTL = 90 * time.Minute
	convMax = 4000
)

var (
	convMu    sync.Mutex
	convStore = map[string]*convState{}
)

// convDelete 作废一路会话（续接失败时用，避免客户端重试还撞同一路死会话）。
func convDelete(key string) {
	if key == "" {
		return
	}
	convMu.Lock()
	delete(convStore, key)
	convMu.Unlock()
}

// convGet 取一路会话，顺带清掉过期的。
func convGet(key string) *convState {
	if key == "" {
		return nil
	}
	convMu.Lock()
	defer convMu.Unlock()
	st, ok := convStore[key]
	if !ok {
		return nil
	}
	if time.Since(st.updated) > convTTL {
		delete(convStore, key)
		return nil
	}
	return st
}

// convPut 存一路会话，超量就按最旧的淘汰一批。
func convPut(key string, st *convState) {
	if key == "" {
		return
	}
	convMu.Lock()
	defer convMu.Unlock()
	st.updated = time.Now()
	convStore[key] = st
	if len(convStore) > convMax {
		// 简单淘汰：删掉过期的；还超就删最旧的一小批。
		var oldestK string
		var oldestT time.Time
		first := true
		for k, v := range convStore {
			if time.Since(v.updated) > convTTL {
				delete(convStore, k)
				continue
			}
			if first || v.updated.Before(oldestT) {
				oldestK, oldestT, first = k, v.updated, false
			}
		}
		if len(convStore) > convMax && oldestK != "" {
			delete(convStore, oldestK)
		}
	}
}

// canonicalMessages serializa mensajes para generar una huella estable de sesión. Incluye
// role, contenido textual, tool_call_id (en mensajes de rol "tool") y tool_calls (id, nombre y
// argumentos) del assistant.
//
// Incluir las tool_calls es imprescindible en un bucle agéntico: dos ramas que parten del mismo
// historial y solo se diferencian en qué herramienta llamaron (ambas con content "") daban la
// misma huella, así que la continuación cruzaba sesiones y el modelo veía un historial ajeno.
// Los reenvíos idénticos del cliente siguen dando la misma huella.
func canonicalMessages(messages []map[string]interface{}) string {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(getStr(m, "role"))
		b.WriteByte(0x1f)
		b.WriteString(contentToString(m["content"]))
		if id := getStr(m, "tool_call_id"); id != "" {
			b.WriteByte(0x1c)
			b.WriteString(id)
		}
		if tcs, ok := m["tool_calls"].([]interface{}); ok {
			for _, tc := range tcs {
				tcm, ok := tc.(map[string]interface{})
				if !ok {
					continue
				}
				fn, _ := tcm["function"].(map[string]interface{})
				b.WriteByte(0x1d)
				b.WriteString(getStr(tcm, "id"))
				b.WriteByte(0x1d)
				b.WriteString(getStr(fn, "name"))
				b.WriteByte(0x1d)
				b.WriteString(getStr(fn, "arguments"))
			}
		}
		b.WriteByte(0x1e)
	}
	return b.String()
}

func hashStr(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// convFind localiza la sesión de continuación correspondiente a este historial probando los
// prefijos que terminan justo después de un mensaje del assistant, del más reciente al más
// antiguo. Devuelve (estado, clave almacenada, índice del primer mensaje nuevo); (nil, "", -1)
// si no hay coincidencia (sesión nueva). La clave se devuelve para poder invalidar la sesión si
// falla la continuación.
func convFind(messages []map[string]interface{}) (*convState, string, int) {
	for i := len(messages) - 2; i >= 0; i-- {
		if getStr(messages[i], "role") != "assistant" {
			continue
		}
		key := hashStr(canonicalMessages(messages[:i+1]))
		if st := convGet(key); st != nil {
			return st, key, i + 1
		}
	}
	return nil, "", -1
}

// convChildKey es la huella del historial tras este turno: el historial de entrada más el
// mensaje del assistant que acabamos de producir (con sus tool_calls). El cliente reproducirá
// ese mismo mensaje en la siguiente petición, así que el prefijo hasta el assistant coincidirá.
func convChildKey(messages []map[string]interface{}, text string, calls []ToolCall) string {
	full := append(append([]map[string]interface{}{}, messages...), assistantMsgWithCalls(text, calls))
	return hashStr(canonicalMessages(full))
}

// assistantMsgWithCalls construye el mensaje del assistant en la forma que lo reenviará el
// cliente (OpenAI), para poder huellarlo idéntico.
func assistantMsgWithCalls(text string, calls []ToolCall) map[string]interface{} {
	m := map[string]interface{}{"role": "assistant", "content": text}
	if len(calls) > 0 {
		tcs := make([]interface{}, 0, len(calls))
		for _, c := range calls {
			tcs = append(tcs, map[string]interface{}{
				"id":   c.ID,
				"type": "function",
				"function": map[string]interface{}{
					"name":      c.Function.Name,
					"arguments": c.Function.Arguments,
				},
			})
		}
		m["tool_calls"] = tcs
	}
	return m
}

var rcidRe = regexp.MustCompile(`"(rc_[A-Za-z0-9_-]{6,})"`)

// parseConvIDs extrae cid=[1][0], rid=[1][1], tok26=trama[26], rcid=rc_xxx del cuerpo (necesarios para reanudar).
// rcid solo se usa en posición 3 al reanudar con login; vacío en anónimo.
func parseConvIDs(raw string) (cid, rid, rcid, tok26 string) {
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
			var f []interface{}
			if json.Unmarshal([]byte(payload), &f) != nil {
				continue
			}
			if len(f) > 1 {
				if meta, ok := f[1].([]interface{}); ok && len(meta) >= 2 {
					if s, ok := meta[0].(string); ok && s != "" {
						cid = s
					}
					if s, ok := meta[1].(string); ok && s != "" {
						rid = s
					}
				}
			}
			if len(f) > 26 {
				if s, ok := f[26].(string); ok && s != "" {
					tok26 = s
				}
			}
		}
	}
	if m := rcidRe.FindStringSubmatch(raw); m != nil {
		rcid = m[1]
	}
	return
}

// getAnonSession 匿名 GET /app 拿一份 session cookie（NID/COMPASS 等），给匿名多轮当会话
// 载体。续接请求必须每轮带上它，否则被服务端当新会话（实测不带 0/4 通、带 4/4 通）。
// 必须走跟正式请求同一出口。
func getAnonSession(proxyURL string) (string, error) {
	req := func() (*http.Response, io.ReadCloser, []string, error) {
		if proxyURL != "" {
			r, err := http.NewRequest("GET", "https://gemini.google.com/app", nil)
			if err != nil {
				return nil, nil, nil, err
			}
			applyChromeHeaders(r)
			resp, err := getStdlibClient(proxyURL).Do(r)
			if err != nil {
				return nil, nil, nil, err
			}
			return resp, resp.Body, resp.Header.Values("Set-Cookie"), nil
		}
		r, err := fhttp.NewRequest("GET", "https://gemini.google.com/app", nil)
		if err != nil {
			return nil, nil, nil, err
		}
		r.Header.Set("User-Agent", webUA)
		resp, err := getTLSClient().Do(r)
		if err != nil {
			return nil, nil, nil, err
		}
		return nil, resp.Body, resp.Header.Values("Set-Cookie"), nil
	}
	_, body, setCookies, err := req()
	if err != nil {
		return "", err
	}
	if body != nil {
		io.Copy(io.Discard, body)
		body.Close()
	}
	var pairs []string
	seen := map[string]bool{}
	for _, sc := range setCookies {
		nv := strings.SplitN(sc, ";", 2)[0]
		name := strings.TrimSpace(strings.SplitN(nv, "=", 2)[0])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		pairs = append(pairs, strings.TrimSpace(nv))
	}
	if len(pairs) == 0 {
		return "", fmt.Errorf("la /app anónima no devolvió Set-Cookie")
	}
	return strings.Join(pairs, "; "), nil
}

// webUA es el User-Agent para GET anónimo a /app (necesario con ruta tls-client).
const webUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"

// streamGenerateConv envía un turno de sesión: turn==0 crea sesión (envía todo), >0 reanuda (solo nuevo mensaje).
// Sesión ligada a conv.cookie/conv.proxyID (no cambia cuenta/egreso). Tras éxito se actualiza cid/rid/rcid/tok26 y turn++.
// para siguiente reanudación.
func streamGenerateConv(prompt string, mc ModelConfig, conv *convState,
	onDelta, onReasoning func(string)) (*StreamResult, error) {
	// Guarda de presupuesto: la vía nativa no convierte el prompt en adjunto, así que un prompt
	// demasiado largo se truncaría en silencio en el upstream (perdería el final). Se rechaza
	// explícitamente igual que en la ruta de un solo turno.
	if b := rtCfg().MaxPromptBytes; b > 0 && len(prompt) > b {
		return &StreamResult{}, &PromptTooLongError{Bytes: len(prompt), Budget: b, HasCookie: conv.cookie != "" || conv.isLogin}
	}
	p, slotOK, slotErr := acquireSlot(conv.proxyID)
	if !slotOK {
		return &StreamResult{}, slotErr
	}
	defer releaseSlot(p.ID)
	proxyURL := p.URL
	pickedOK := p.ID > 0

	// 首轮定出口；续接沿用会话原出口（acquireSlot 已优先，但拿不到原出口时只能换，
	// 换了续接大概率失败，靠调用方回退到全量重发兜底）。
	if conv.turn == 0 {
		conv.proxyID = p.ID
		conv.proxyURL = proxyURL
		// 匿名首轮：没有登录 cookie，就地拿一份 session cookie 当会话载体。
		if conv.cookie == "" && !conv.isLogin {
			c, err := getAnonSession(proxyURL)
			if err != nil {
				return &StreamResult{ProxyID: p.ID, ProxyName: p.Name}, fmt.Errorf("建匿名会话失败: %w", err)
			}
			conv.cookie = c
		}
	}

	// 登录态每轮要带 at（XSRF）；匿名不要。
	xsrf := ""
	if conv.isLogin && conv.cookie != "" {
		if tok, err := getXSRF(conv.cookie, proxyURL); err == nil {
			xsrf = tok
		} else {
			return &StreamResult{ProxyID: p.ID, ProxyName: p.Name, AccountID: conv.accountID},
				fmt.Errorf("取 XSRF 失败: %w", err)
		}
	}

	inner := make([]interface{}, innerSlots)
	inner[0] = []interface{}{prompt, 0, nil, nil, nil, nil, 0}
	inner[1] = []interface{}{"en"}
	if conv.turn == 0 {
		inner[2] = []interface{}{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	} else {
		// 续接：[cid, rid, rcid(登录)/""(匿名), null×6, tok26]
		inner[2] = []interface{}{conv.cid, conv.rid, conv.rcid,
			nil, nil, nil, nil, nil, nil, conv.tok26}
	}
	inner[6] = []interface{}{0}
	inner[7] = 1
	inner[10] = 1
	inner[11] = 0
	inner[17] = []interface{}{[]interface{}{conv.turn}}
	inner[18] = 0
	inner[27] = 1
	inner[30] = []interface{}{4}
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

	innerJSON, _ := json.Marshal(inner)
	outerJSON, _ := json.Marshal([]interface{}{nil, string(innerJSON)})

	buildBody := func(at string) string {
		form := url.Values{}
		form.Set("f.req", string(outerJSON))
		if at != "" {
			form.Set("at", at)
		}
		return form.Encode()
	}
	body := buildBody(xsrf)

	thinkVal := thinkingNormal
	if mc.Thinking {
		thinkVal = thinkingExtended
	}
	geminiHeaders := buildGeminiHeaders(conv.cookie, conv.sapisid, mc.HexID)
	geminiHeaders["x-goog-ext-525001261-jspb"] = buildModelHeader(mc.HexID, mc.Mode, thinkVal, uuid.NewString())
	geminiHeaders["x-goog-ext-525005358-jspb"] = fmt.Sprintf(`["%s",1]`, reqUUID)

	reqid := time.Now().UnixNano() % 1000000
	endpoint := fmt.Sprintf(
		"https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?bl=%s&hl=en&_reqid=%d&rt=c",
		currentBL(proxyURL), reqid)

	tracker := &deltaTracker{}
	rtracker := &deltaTracker{}
	var lineCB func(string)
	if onDelta != nil || onReasoning != nil {
		lineCB = func(line string) {
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

	t0 := time.Now()
	var lastErr error
	for attempt := 0; attempt < rtCfg().RetryAttempts; attempt++ {
		statusCode, raw, ttfb, setCookie, err := doGeminiRequest(endpoint, body, geminiHeaders, proxyURL, lineCB)
		if len(setCookie) > 0 && conv.cookie != "" {
			if merged := mergeSetCookie(conv.cookie, setCookie); merged != conv.cookie {
				conv.cookie = merged
			}
		}
		if err != nil || statusCode != 200 || !hasContentFrame(string(raw)) {
			if err == nil && statusCode == 200 {
				lastErr = fmt.Errorf("续接无内容帧（会话可能已失效，raw %d 字节）", len(raw))
			} else if err != nil {
				lastErr = err
			} else {
				lastErr = fmt.Errorf("upstream HTTP %d: %s", statusCode, truncate(string(raw), 160))
			}
			if pickedOK {
				recordProxyResult(p.ID, false, lastErr.Error())
			}
			if tracker.emitted != "" || rtracker.emitted != "" {
				break
			}
			if attempt < rtCfg().RetryAttempts-1 {
				time.Sleep(time.Duration(rtCfg().RetryDelaySec) * time.Second)
			}
			continue
		}
		// 成功：回填续接状态。
		cid, rid, rcid, tok26 := parseConvIDs(string(raw))
		if cid != "" {
			conv.cid = cid
		}
		if rid != "" {
			conv.rid = rid
		}
		if rcid != "" {
			conv.rcid = rcid
		}
		conv.tok26 = tok26 // 每轮更新（可能为空）
		conv.turn++
		if pickedOK {
			recordProxyResult(p.ID, true, "")
		}
		return &StreamResult{
			Emitted:          tracker.emitted,
			EmittedReasoning: rtracker.emitted,
			Raw:              string(raw),
			Reasoning:        extractReasoning(string(raw)),
			UpstreamModel:    extractUpstreamModel(string(raw)),
			ProxyID:          p.ID,
			ProxyName:        p.Name,
			AccountID:        conv.accountID,
			TTFBMs:           ttfb,
			TotalMs:          time.Since(t0).Milliseconds(),
		}, nil
	}
	return &StreamResult{ProxyID: p.ID, ProxyName: p.Name, AccountID: conv.accountID}, lastErr
}

// callGeminiConv es el punto de entrada cuando se usa la conversación nativa de servidor
// (multironda): identifica la continuación por el historial.
//   - Coincide con una sesión conocida → solo envía los mensajes nuevos (el último turno de
//     usuario, o los resultados de herramienta); el historial queda en el servidor (evita el
//     muro de bytes y evita reenviar en cada vuelta del bucle agéntico).
//   - No coincide → crea una sesión nueva y en el primer turno envía el historial completo
//     concatenado (con las instrucciones de tools).
//
// Soporta tools (en la continuación también se parsean los fences ```tool_call```); las imágenes
// van por la ruta original de un solo turno (callGemini).
func callGeminiConv(messages []map[string]interface{}, mc ModelConfig,
	tools []map[string]interface{}, toolChoice interface{}, parallel bool,
	onDelta, onReasoning func(string)) (string, []ToolCall, *StreamResult, error) {
	conv, convKey, newFrom := convFind(messages)
	fresh := conv == nil

	var prompt string
	if fresh {
		conv = &convState{}
		// Si hay pool de cookies se usa una cuenta con login (capacidades más completas); si no, anónimo (en el primer turno se obtiene una session cookie in situ).
		// #20 anónimo primero: no se ocupa cuenta cuando el modelo no necesita login. Hay que
		// cortocircuitar antes de la llamada: pickCookieAccount actualiza last_used_at, y
		// llamarlo sin usarlo equivale a ocupar un turno en vano.
		if !anonFirstEligible(mc, false) {
			if a, ok := pickCookieAccount(); ok {
				conv.cookie = a.Cookie
				conv.sapisid = extractSAPISID(a.Cookie)
				conv.isLogin = true
				conv.accountID = a.ID
				conv.proxyID = a.ProxyID
			}
		}
		// En el primer turno se incluyen las instrucciones de tools: el modelo las necesita para saber cómo emitir los fences tool_call; en los turnos de continuación no se reenvían
		// (las instrucciones ya están en el historial del primer turno en el servidor).
		var latest string
		prompt, latest = messagesToPrompt(messages, tools, toolChoice, parallel)
		// Un prompt inicial que supera el presupuesto se maneja mejor por la ruta de un solo
		// turno, que puede convertirlo en adjunto de texto con cookie. Enviarlo por la sesión
		// nativa lo truncaría en silencio en el upstream (se perdería justo el final). La
		// continuación nunca llega aquí porque solo envía los mensajes nuevos.
		if b := rtCfg().MaxPromptBytes; b > 0 && len(prompt) > b {
			logf("[conv] prompt inicial %d bytes > presupuesto %d; se degrada a la ruta de un solo turno", len(prompt), b)
			return callGemini(prompt, latest, mc, tools, toolChoice, nil, onDelta, onReasoning)
		}
	} else {
		// Continuación: se envían solo los mensajes nuevos (puede haber varios: un assistturn de
		// tool_calls seguido de N resultados de herramienta, o simplemente un mensaje de usuario).
		// Los resultados de herramienta se formatean con la misma señal limpia que el primer turno
		// (formatToolResult) y resolviendo el nombre por tool_call_id cuando el cliente no lo repite.
		prompt = formatNewMessages(messages[newFrom:], toolCallNames(messages))
		if len(tools) > 0 {
			// Re-anclar el formato de herramientas en cada continuación: el modelo se desvía
			// si no lo vuelve a ver, aunque las definiciones sigan en el historial del servidor.
			prompt += "\n\n" + toolTurnReminder
		}
	}
	if fresh {
		logf("[conv] sesión nueva, primer turno envía %d bytes (tools=%d)", len(prompt), len(tools))
	} else {
		logf("[conv] continuación acertada turn=%d, %d mensajes nuevos, %d bytes (historial queda en el servidor)",
			conv.turn, len(messages)-newFrom, len(prompt))
	}

	res, err := streamGenerateConv(prompt, mc, conv, onDelta, onReasoning)
	if err != nil {
		if !fresh {
			// Fallo de continuación: esta sesión queda invalidada; cuando el cliente reintente se tratará como sesión nueva y se reenviará todo.
			convDelete(convKey)
		}
		return "", nil, res, err
	}
	text := extractResponseText(res.Raw)
	var toolCalls []ToolCall
	if len(tools) > 0 {
		text, toolCalls = parseToolCalls(text, toolNameSet(tools))
	}
	if text == "" && len(toolCalls) == 0 {
		if !fresh {
			convDelete(convKey)
		}
		return "", nil, res, fmt.Errorf("upstream returned no content frame (raw %d bytes)", len(res.Raw))
	}
	// Guardar el estado de continuación: cuando el cliente devuelva este turno tal cual en la
	// siguiente petición, el prefijo hasta este assistant coincidirá.
	convPut(convChildKey(messages, text, toolCalls), conv)
	return text, toolCalls, res, nil
}

// shouldUseConv decide si una petición va por la conversación nativa de servidor (multironda).
// No aplica a modelos multimedia ni a peticiones con imágenes (esos necesitan adjuntos).
//   - Con MultiTurn activado, siempre que no haya imágenes.
//   - Activación consciente: aunque MultiTurn esté apagado, en una sesión con herramientas y con
//     cuenta con cookie se usa la continuación server-side, para no reenviar todo el historial en
//     cada vuelta del bucle agéntico (menos presión sobre el muro de bytes, menos repetición de
//     trabajo). Si el cliente no reproduce el historial de forma idéntica, la huella no coincide
//     y cae a sesión nueva (comportamiento de un solo turno), así que no hay regresión.
func shouldUseConv(mc ModelConfig, hasImages, hasTools bool) bool {
	if mc.Tool != 0 || hasImages {
		return false
	}
	return rtCfg().MultiTurn || (hasTools && hasCookie())
}

// formatNewMessages formatea los mensajes nuevos de una continuación, alineado con cómo
// messagesToPrompt escribe cada rol, para que el modelo del historial del servidor reconozca si
// es un resultado de herramienta (con la señal limpia de éxito/fallo) o un mensaje nuevo del usuario.
// Acepta varios mensajes porque un turno de herramienta en paralelo llega como un assistant de
// tool_calls seguido de N resultados.
func formatNewMessages(msgs []map[string]interface{}, callNames map[string]string) string {
	var parts []string
	for _, m := range msgs {
		content := contentToString(m["content"])
		switch getStr(m, "role") {
		case "tool":
			name := getStr(m, "name")
			if name == "" {
				name = callNames[getStr(m, "tool_call_id")]
			}
			parts = append(parts, formatToolResult(name, content))
		case "assistant":
			parts = append(parts, "[Assistant]: "+content)
		default:
			if content != "" {
				parts = append(parts, content)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}
