package app

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// keyCookieWants son las claves que se miran para saber si una cookie se ha "copiado entera".
//
// En capturas reales, el navegador envía 30 cookies a gemini.google.com en cada petición, de
// las cuales 16 son elementos de sesión que van siempre. Aquí solo se eligen las que mejor
// reflejan si "se copió todo" para mostrarlas, no porque las demás no importen: reenviamos la
// cadena entera tal cual, y una de menos es una que no se envía.
var keyCookieWants = []string{"SID", "HSID", "SSID", "APISID", "SAPISID",
	"__Secure-1PSID", "__Secure-1PSIDTS", "__Secure-1PAPISID", "__Secure-1PSIDCC"}

// cookieAcctView es la vista desensibilizada de una cuenta: **no incluye el valor completo de
// la cookie**, solo un resumen de estado. No hace falta enviar las credenciales del servidor
// al navegador otra vez (mismo principio que el antiguo handleAdminCookie).
func cookieAcctView(a CookieAccount) map[string]interface{} {
	names := cookieNames(a.Cookie)
	nameSet := map[string]bool{}
	for _, n := range names {
		nameSet[n] = true
	}
	var key []string
	for _, w := range keyCookieWants {
		if nameSet[w] {
			key = append(key, w)
		}
	}
	tail := ""
	if s := extractSAPISID(a.Cookie); len(s) >= 4 {
		tail = s[len(s)-4:]
	}
	// health es la salud derivada para marcar en rojo en el panel (#18). El criterio usa
	// fail_count: markAccountResult lo pone a cero al tener éxito y solo lo incrementa al
	// fallar, y solo el 401/403 cuenta como fallo (ver markCookieByStatus), así que
	// fail_count>0 significa "la autenticación actual está fallando" = lo más probable es que
	// la cookie haya caducado; se marca en rojo y se pide reimportar.
	//   dead     autenticación fallando (rojo): tanto si está habilitada como si se desactivó
	//            automáticamente, basta con que fail_count>0
	//   disabled desactivada a mano por el usuario y sin registros de fallo (gris)
	//   unknown  aún no se ha usado ni comprobado con éxito (pendiente de comprobar)
	//   ok       en uso y sin fallos (verde)
	health := "ok"
	switch {
	case a.FailCount > 0:
		health = "dead"
	case a.Status != "enabled":
		health = "disabled"
	case a.LastOkAt == 0:
		health = "unknown"
	}
	return map[string]interface{}{
		"id":           a.ID,
		"label":        a.Label,
		"status":       a.Status,
		"health":       health,
		"note":         a.Note,
		"created_at":   a.CreatedAt,
		"last_used_at": a.LastUsedAt,
		"last_ok_at":   a.LastOkAt,
		"last_error":   a.LastError,
		"fail_count":   a.FailCount,
		"cookie_count": len(names),
		"key_cookies":  key,
		"sapisid_tail": tail,
		"has_1psidts":  nameSet["__Secure-1PSIDTS"],
		"proxy_id":     a.ProxyID,
		"proxy_name":   proxyNameByID(a.ProxyID),
	}
}

// handleAdminCookies: GET lista el pool / POST añade una nueva.
func handleAdminCookies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		accts := accountList()
		items := make([]map[string]interface{}, 0, len(accts))
		for _, a := range accts {
			items = append(items, cookieAcctView(a))
		}
		total, enabled := accountCount()
		writeJSON(w, 200, map[string]interface{}{
			"items": items, "total": total, "enabled": enabled,
		})
	case http.MethodPost:
		var p struct {
			Label  string `json:"label"`
			Cookie string `json:"cookie"`
			Note   string `json:"note"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &p); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad json"})
			return
		}
		id, err := accountAdd(p.Label, p.Cookie, p.Note)
		if err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		logf("[cookies] nueva cuenta #%d label=%q", id, strings.TrimSpace(p.Label))
		writeJSON(w, 200, map[string]interface{}{"id": id})
	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}

// handleAdminCookieItem: /admin/api/cookies/{id}[/toggle]
//
//	DELETE          borrar
//	POST .../toggle alternar enabled/disabled
//	PATCH           cambiar label / note / status
func handleAdminCookieItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/admin/api/cookies/")
	parts := strings.Split(rest, "/")
	if len(parts) < 1 || parts[0] == "" {
		writeJSON(w, 404, map[string]string{"error": "missing id"})
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad id"})
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch r.Method {
	case http.MethodDelete:
		if err := accountDelete(id); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		logf("[cookies] borrada la cuenta #%d", id)
		writeJSON(w, 200, map[string]bool{"ok": true})
	case http.MethodPost:
		if action == "rotate" {
			for _, a := range accountList() {
				if a.ID == id {
					iv, names, err := rotateAccount(a)
					if err != nil {
						writeJSON(w, 200, map[string]interface{}{"ok": false, "detail": err.Error()})
						return
					}
					detail := "Mantenimiento con éxito"
					if len(names) > 0 {
						detail = "Mantenimiento con éxito, refrescadas " + strings.Join(names, ", ")
					}
					writeJSON(w, 200, map[string]interface{}{
						"ok": true, "detail": detail, "refreshed": names, "next_sec": int(iv.Seconds())})
					return
				}
			}
			writeJSON(w, 404, map[string]string{"error": "account not found"})
			return
		}
		if action == "check" {
			for _, a := range accountList() {
				if a.ID == id {
					writeJSON(w, 200, checkAccountCookie(a))
					return
				}
			}
			writeJSON(w, 404, map[string]string{"error": "account not found"})
			return
		}
		if action != "toggle" {
			writeJSON(w, 400, map[string]string{"error": "unknown action"})
			return
		}
		cur := ""
		for _, a := range accountList() {
			if a.ID == id {
				cur = a.Status
				break
			}
		}
		next := "enabled"
		if cur == "enabled" {
			next = "disabled"
		}
		if err := accountSetStatus(id, next); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]interface{}{"status": next})
	case http.MethodPatch:
		var p struct {
			Label  *string `json:"label"`
			Note   *string `json:"note"`
			Status *string `json:"status"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &p); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad json"})
			return
		}
		if p.Status != nil {
			if err := accountSetStatus(id, *p.Status); err != nil {
				writeJSON(w, 400, map[string]string{"error": err.Error()})
				return
			}
		}
		if p.Label != nil || p.Note != nil {
			// Solo se cambian los campos enviados: los no enviados conservan el valor actual.
			var curLabel, curNote string
			for _, a := range accountList() {
				if a.ID == id {
					curLabel, curNote = a.Label, a.Note
					break
				}
			}
			if p.Label != nil {
				curLabel = *p.Label
			}
			if p.Note != nil {
				curNote = *p.Note
			}
			if err := accountUpdateMeta(id, curLabel, curNote); err != nil {
				writeJSON(w, 500, map[string]string{"error": err.Error()})
				return
			}
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}
