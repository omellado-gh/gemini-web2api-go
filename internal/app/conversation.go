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

// convState mantiene el estado de una sesión nativa de Gemini. Toda la sesión está ligada a la misma cookie/egreso; no se cambia a mitad.
// Si cambia el entorno Gemini, se trata como otra sesión y el cid para reanudar deja de ser válido.
type convState struct {
	cid, rid, rcid, tok26 string
	turn                  int    // índice de turno dentro de la sesión; se coloca en inner[17], +1 por turno
	cookie                string // cookie ligada a la sesión: la de la cuenta con login, o la session obtenida anónimamente en /app
	sapisid               string // se usa para calcular SAPISIDHASH en estado con login; vacío en anónimo
	isLogin               bool
	accountID             int64
	proxyID               int64
	proxyURL              string
	updated               time.Time
}

const (
	// convTTL se alinea con la vida útil (~2h) de las cookies exportadas; sesiones caducadas no merecen reutilizarse.
	convTTL = 90 * time.Minute
	convMax = 4000
)

var (
	convMu    sync.Mutex
	convStore = map[string]*convState{}
)

// convDelete invalida una sesión (usado al fallar reanudación para evitar reintentos sobre misma sesión muerta).
func convDelete(key string) {
	if key == "" {
		return
	}
	convMu.Lock()
	delete(convStore, key)
	convMu.Unlock()
}

// convGet recupera una sesión y elimina las caducadas.
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

// convPut guarda una sesión; si supera el límite, elimina las más antiguas.
func convPut(key string, st *convState) {
	if key == "" {
		return
	}
	convMu.Lock()
	defer convMu.Unlock()
	st.updated = time.Now()
	convStore[key] = st
	if len(convStore) > convMax {
		// Política simple: primero caducadas; si aún supera, elimina el lote más antiguo.
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

// canonicalMessages 把消息列表压成 role+text 的稳定串，用来算会话指纹。
// 只取 role 和文本内容 —— 客户端每轮重发同样的历史，压出来的串一致才能识别续接。
func canonicalMessages(messages []map[string]interface{}) string {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(getStr(m, "role"))
		b.WriteByte(0x1f)
		b.WriteString(contentToString(m["content"]))
		b.WriteByte(0x1e)
	}
	return b.String()
}

func hashStr(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// convParentKey 是"这轮之前的历史"的指纹：除最后一条消息外的全部。
// 命中 store 说明这是某路已知会话的延续，只要发最后一条新消息即可。
func convParentKey(messages []map[string]interface{}) string {
	if len(messages) < 2 {
		return ""
	}
	return hashStr(canonicalMessages(messages[:len(messages)-1]))
}

// convChildKey 是"这轮之后的历史"的指纹：历史 + 本轮模型回复。
// 下一轮请求的 parentKey 会正好等于它（客户端把我们的回复原样带回来），从而续上。
func convChildKey(messages []map[string]interface{}, responseText string) string {
	full := append(append([]map[string]interface{}{}, messages...),
		map[string]interface{}{"role": "assistant", "content": responseText})
	return hashStr(canonicalMessages(full))
}

var rcidRe = regexp.MustCompile(`"(rc_[A-Za-z0-9_-]{6,})"`)

// parseConvIDs 从 StreamGenerate 响应里取续接要用的四样：cid=[1][0]、rid=[1][1]、
// tok26=帧[26]、rcid=正文里的 rc_xxx（登录态续接第 3 位要用它，匿名为空）。
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

// getAnonSession hace GET anónimo a /app para obtener cookie de sesión (NID/COMPASS etc.), usada como portador en multironda anónima.
// Cada reanudación debe llevarla; si no, el servidor trata como nueva sesión (sin ella 0/4 pasan; con ella 4/4).
// Debe usar el mismo egreso que la solicitud formal.
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
		return "", fmt.Errorf("匿名 /app 没返回 Set-Cookie")
	}
	return strings.Join(pairs, "; "), nil
}

// webUA 是匿名 GET /app 的 User-Agent（tls-client 路径要手动带）。
const webUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"

// streamGenerateConv 发一路会话的一轮。turn==0 是首轮（建会话），>0 是续接（只发新消息）。
// 整条会话绑定 conv.cookie / conv.proxyID，中途不换号不换出口。成功后回填 conv 的
// cid/rid/rcid/tok26 和 turn+1，供下一轮续接。
func streamGenerateConv(prompt string, mc ModelConfig, conv *convState,
	onDelta, onReasoning func(string)) (*StreamResult, error) {
	p, slotOK, slotErr := acquireSlot(conv.proxyID)
	if !slotOK {
		return &StreamResult{}, slotErr
	}
	defer releaseSlot(p.ID)
	proxyURL := p.URL
	pickedOK := p.ID > 0

	// El primer turno fija el egreso; las continuaciones reutilizan el egreso original de la sesión (acquireSlot ya lo prioriza,
	// pero si no se obtiene el original solo queda cambiarlo; cambiar el egreso hará fallar la continuación con alta probabilidad,
	// y el llamador recurre al reenvío completo como respaldo).
	if conv.turn == 0 {
		conv.proxyID = p.ID
		conv.proxyURL = proxyURL
		// Primer turno anónimo: no hay cookie de login; se obtiene una session cookie in situ como portador de la sesión.
		if conv.cookie == "" && !conv.isLogin {
			c, err := getAnonSession(proxyURL)
			if err != nil {
				return &StreamResult{ProxyID: p.ID, ProxyName: p.Name}, fmt.Errorf("fallo al crear sesión anónima: %w", err)
			}
			conv.cookie = c
		}
	}

	// En estado con login hay que enviar at (XSRF) en cada turno; en anónimo no.
	xsrf := ""
	if conv.isLogin && conv.cookie != "" {
		if tok, err := getXSRF(conv.cookie, proxyURL); err == nil {
			xsrf = tok
		} else {
			return &StreamResult{ProxyID: p.ID, ProxyName: p.Name, AccountID: conv.accountID},
				fmt.Errorf("fallo al obtener XSRF: %w", err)
		}
	}

	inner := make([]interface{}, innerSlots)
	inner[0] = []interface{}{prompt, 0, nil, nil, nil, nil, 0}
	inner[1] = []interface{}{"en"}
	if conv.turn == 0 {
		inner[2] = []interface{}{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	} else {
		// Continuación: [cid, rid, rcid(login)/""(anónimo), null×6, tok26]
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
				lastErr = fmt.Errorf("continuación sin trama de contenido (la sesión puede haber caducado, raw %d bytes)", len(raw))
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
		// Éxito: rellenar el estado de continuación.
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
		conv.tok26 = tok26 // se actualiza cada turno (puede estar vacío)
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

// callGeminiConv 是多轮开启时的入口：按历史前缀识别续接。
//   - 命中已知会话 → 只发最后一条新消息（user 或 tool 结果），历史留服务端（绕开字节墙）。
//   - 没命中 → 新建会话，首轮发全量拼接的历史（含 tools 指令，把当前上下文交给服务端）。
//
// 支持 tools（续接里也解析 ```tool_call``` 围栏）；图片走原单轮路径（callGemini）。
func callGeminiConv(messages []map[string]interface{}, mc ModelConfig,
	tools []map[string]interface{}, toolChoice interface{},
	onDelta, onReasoning func(string)) (string, []ToolCall, *StreamResult, error) {
	parentKey := convParentKey(messages)
	conv := convGet(parentKey)
	fresh := conv == nil

	var prompt string
	if fresh {
		conv = &convState{}
		// 有 cookie 池就用登录号（能力更全），否则匿名（首轮就地拿 session cookie）。
		// #20 匿名优先：模型不需要登录态时不占号（多轮已被 gate 限成无图无工具，附件
		// 恒无，所以第二个参数传 false）。必须在调用前短路：pickCookieAccount 会更新
		// last_used_at，调了不用等于白占一轮。
		if !anonFirstEligible(mc, false) {
			if a, ok := pickCookieAccount(); ok {
				conv.cookie = a.Cookie
				conv.sapisid = extractSAPISID(a.Cookie)
				conv.isLogin = true
				conv.accountID = a.ID
				conv.proxyID = a.ProxyID
			}
		}
		// 首轮带上 tools 指令：模型要靠它知道怎么吐 tool_call 围栏，续接轮不再重发
		//（指令已在服务端首轮历史里）。
		prompt, _ = messagesToPrompt(messages, tools, toolChoice)
	} else {
		prompt = formatNewTurn(messages[len(messages)-1])
	}
	if fresh {
		logf("[conv] 新会话，首轮发 %d 字节（tools=%d）", len(prompt), len(tools))
	} else {
		logf("[conv] 续接命中 turn=%d，只发 %d 字节（历史留服务端）", conv.turn, len(prompt))
	}

	res, err := streamGenerateConv(prompt, mc, conv, onDelta, onReasoning)
	if err != nil {
		if !fresh {
			// 续接失败：这路会话作废，客户端重试时会当新会话全量重发。
			convDelete(parentKey)
		}
		return "", nil, res, err
	}
	text := extractResponseText(res.Raw)
	var toolCalls []ToolCall
	if len(tools) > 0 {
		text, toolCalls = parseToolCalls(text)
	}
	if text == "" && len(toolCalls) == 0 {
		if !fresh {
			convDelete(parentKey)
		}
		return "", nil, res, fmt.Errorf("upstream returned no content frame (raw %d bytes)", len(res.Raw))
	}
	// 存续接状态：客户端下一轮把这段回复原样带回来时，parentKey 会命中这里。
	// 存的是解析掉围栏后的正文 —— 客户端把 assistant 消息带回来时 content 也是这段。
	convPut(convChildKey(messages, text), conv)
	return text, toolCalls, res, nil
}

// formatNewTurn 把续接要发的那条新消息格式化，跟 messagesToPrompt 里各角色的写法对齐，
// 好让服务端历史里的模型认得出这是工具结果还是用户新话。
func formatNewTurn(msg map[string]interface{}) string {
	content := contentToString(msg["content"])
	if getStr(msg, "role") == "tool" {
		return fmt.Sprintf("[Tool result for %s]: %s", getStr(msg, "name"), content)
	}
	return content
}
