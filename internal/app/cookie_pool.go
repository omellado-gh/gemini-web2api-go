package app

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CookieAccount es una fila del pool de cookies: una cuenta de sesión de Google.
type CookieAccount struct {
	ID         int64  `json:"id"`
	Label      string `json:"label"`
	Cookie     string `json:"cookie"` // cadena completa; la capa API la enmascara antes de devolverla
	Status     string `json:"status"`
	Note       string `json:"note"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
	LastOkAt   int64  `json:"last_ok_at"`
	LastError  string `json:"last_error"`
	FailCount  int64  `json:"fail_count"`
	ProxyID    int64  `json:"proxy_id"` // salida vinculada; 0 = aún sin vincular
}

// splitCookiePairs divide "k=v; k=v" en pares clave-valor.
//
// Se corta por ";" y luego se hace TrimSpace a cada segmento, no se corta por "; ":
// las cadenas copiadas de DevTools no siempre llevan espacios, y al cortar por "; "
// "SID=a;SAPISID=b" quedaría como una sola clave — así extractSAPISID no obtiene
// valor, no se envía la cabecera Authorization y el upstream trata la petición como
// anónima, sin que el usuario lo sepa.
//
// El valor puede contener "=" (relleno base64), así que solo se corta en el primer igual.
func splitCookiePairs(cookie string) [][2]string {
	var out [][2]string
	for _, p := range strings.Split(cookie, ";") {
		p = strings.TrimSpace(p)
		if i := strings.Index(p, "="); i > 0 {
			out = append(out, [2]string{p[:i], p[i+1:]})
		}
	}
	return out
}

// cookieValue obtiene el valor del nombre indicado dentro de una cadena de cookie; si no lo encuentra, devuelve cadena vacía.
func cookieValue(cookie, name string) string {
	for _, kv := range splitCookiePairs(cookie) {
		if kv[0] == name {
			return kv[1]
		}
	}
	return ""
}

// extractSAPISID obtiene el valor de SAPISID de una cadena de cookie; si no lo encuentra, devuelve cadena vacía.
func extractSAPISID(cookie string) string {
	return cookieValue(cookie, "SAPISID")
}

// cookieSubset deja solo las entradas con los nombres indicados, conservando el orden
// del string original. Al refrescar 1PSIDTS no se puede arrastrar el string completo:
// en pruebas, arrastrar el DBSC de Chrome y cookies de otros hosts hace que
// RotateCookies devuelva 401.
func cookieSubset(cookie string, names []string) string {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var parts []string
	seen := map[string]bool{}
	for _, kv := range splitCookiePairs(cookie) {
		if !want[kv[0]] || kv[1] == "" || seen[kv[0]] {
			continue
		}
		seen[kv[0]] = true
		parts = append(parts, kv[0]+"="+kv[1])
	}
	return strings.Join(parts, "; ")
}

// cookieNames devuelve todos los nombres de cookie que aparecen en el string (conservando el orden), para mostrarlos en la UI.
func cookieNames(cookie string) []string {
	var names []string
	for _, kv := range splitCookiePairs(cookie) {
		names = append(names, kv[0])
	}
	return names
}

// accountAdd inserta una fila en el pool. La cookie debe permitir obtener un SAPISID no vacío.
// Este es el camino de la adición manual desde el panel/API: el usuario ve el error en el
// momento, así que rechazarla es lo correcto.
//
// El criterio es **si se puede obtener el valor**, no "si la cadena contiene SAPISID":
// esto último metería en el pool una plantilla por ítems sin rellenar (nombres de clave
// presentes, valores vacíos), guardada como un bloque JSON entero, y cada vez que le tocara
// el turno fallaría sin remedio.
func accountAdd(label, cookie, note string) (int64, error) {
	c, ok := normalizeCookie(cookie, "cookie añadida a mano")
	if !ok {
		return 0, fmt.Errorf("fallo al analizar la cookie: no es una cadena \"k=v; k=v\" ni un JSON reconocible")
	}
	if extractSAPISID(c) == "" {
		return 0, fmt.Errorf("la cookie no tiene SAPISID; seguramente no se copió completa (hace falta la cookie completa de gemini.google.com, con al menos SID / HSID / SSID / APISID / SAPISID / __Secure-1PSID)")
	}
	return accountInsert(label, c, note)
}

// accountAdopt es específica de la importación de la configuración existente al arrancar:
// **no comprueba el SAPISID**.
//
// El criterio es «una actualización no debe cambiar la disponibilidad de la configuración
// que el usuario ya tenía». Una cookie sin SAPISID en la versión antigua se enviaba tal cual
// como cabecera Cookie (solo que no se podía calcular la cabecera de autorización SAPISIDHASH),
// y si sirve o no lo decide el upstream: no deberíamos bloquearla al actualizar con una
// validación recién añadida y hacer que el usuario vuelva al anonimato sin enterarse. Solo se
// avisa; la salud se reflejará con veracidad en el panel.
func accountAdopt(label, cookie, note string) (int64, error) {
	cookie, ok := normalizeCookie(cookie, label)
	if !ok {
		return 0, fmt.Errorf("fallo al analizar la cookie")
	}
	if extractSAPISID(cookie) == "" {
		logf("[cookie] %s no tiene SAPISID; no se puede calcular la cabecera de autorización SAPISIDHASH, "+
			"así que puede tratarse como anónima; se importa tal cual, verifíquelo en el panel", label)
	}
	return accountInsert(label, cookie, note)
}

func accountInsert(label, cookie, note string) (int64, error) {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return 0, fmt.Errorf("la cookie no puede estar vacía")
	}
	return insertID(
		`INSERT INTO accounts(label, cookie, status, note, created_at) VALUES (?,?,?,?,?)`,
		strings.TrimSpace(label), cookie, "enabled", strings.TrimSpace(note), time.Now().Unix())
}

// accountList devuelve todas las cuentas del pool, en orden ascendente de id.
func accountList() []CookieAccount {
	rows, err := getDB().Query(
		`SELECT id, label, cookie, status, note, created_at, last_used_at, last_ok_at, last_error, fail_count, proxy_id
		 FROM accounts ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []CookieAccount
	for rows.Next() {
		var a CookieAccount
		if err := rows.Scan(&a.ID, &a.Label, &a.Cookie, &a.Status, &a.Note,
			&a.CreatedAt, &a.LastUsedAt, &a.LastOkAt, &a.LastError, &a.FailCount, &a.ProxyID); err != nil {
			continue
		}
		out = append(out, a)
	}
	return out
}

// accountByID toma una fila por id; si no la encuentra, devuelve nil.
//
// La rotación escribe la cookie nueva en la base de datos, y después hay que seguir
// haciendo peticiones con **la de la base de datos**: el CookieAccount que tenemos en la
// mano es una instantánea anterior a la rotación, y seguir usándola equivale a tirar el
// valor recién refrescado.
func accountByID(id int64) *CookieAccount {
	var a CookieAccount
	err := getDB().QueryRow(
		`SELECT id, label, cookie, status, note, created_at, last_used_at, last_ok_at, last_error, fail_count, proxy_id
		 FROM accounts WHERE id=?`, id).
		Scan(&a.ID, &a.Label, &a.Cookie, &a.Status, &a.Note,
			&a.CreatedAt, &a.LastUsedAt, &a.LastOkAt, &a.LastError, &a.FailCount, &a.ProxyID)
	if err != nil {
		return nil
	}
	return &a
}

// accountDelete elimina una fila.
func accountDelete(id int64) error {
	_, err := getDB().Exec(`DELETE FROM accounts WHERE id=?`, id)
	return err
}

// accountSetStatus cambia el estado (enabled / disabled).
func accountSetStatus(id int64, status string) error {
	if status != "enabled" && status != "disabled" {
		return fmt.Errorf("estado no válido %q", status)
	}
	_, err := getDB().Exec(`UPDATE accounts SET status=? WHERE id=?`, status, id)
	return err
}

// accountUpdateMeta cambia label / note (sin tocar la cookie en sí).
func accountUpdateMeta(id int64, label, note string) error {
	_, err := getDB().Exec(`UPDATE accounts SET label=?, note=? WHERE id=?`,
		strings.TrimSpace(label), strings.TrimSpace(note), id)
	return err
}

// accountCount devuelve (total, número de enabled).
func accountCount() (int, int) {
	var total, enabled int
	_ = getDB().QueryRow(`SELECT COUNT(*), SUM(CASE WHEN status='enabled' THEN 1 ELSE 0 END) FROM accounts`).
		Scan(&total, &enabled)
	return total, enabled
}

// Claves de kv donde se registra el estado de la migración/siembra.
const (
	kvLegacyCookieDone = "legacy_single_cookie_migrated"
	kvSeededCookieID   = "seeded_cookie_id"
	kvSeededCookieVal  = "seeded_cookie_value"
)

// seedCookiesFromConfig incorpora al pool de cookies los parámetros de arranque y la
// cookie única heredada.
//
// La cookie también tenía antes dos entradas: el pool de cookies y la entrada de cookie
// única de la página «Ajustes», que solo se usaba cuando el pool estaba vacío (el valor se
// guarda en el kv google_cookie, o viene de --cookie-file). El mismo defecto que con el proxy
// estático: la ruta de cookie única devuelve un ID de cuenta 0, y markAccountResult empieza
// con id<=0 y retorna directamente, de modo que **no escribe ni rastro de salud**: fail_count
// siempre 0, last_ok_at siempre vacío, y sin rotación.
//
//   - google_cookie en kv: migración única, con una marca aparte que registra "ya migrado",
//     sin tocar el valor original
//   - cfg.CookieFile (--cookie-file): seguimiento declarativo; si el contenido cambia, se
//     reemplaza la misma fila
func seedCookiesFromConfig() {
	migrateLegacyCookie()
	syncSeededCookieFile()
}

// migrateLegacyCookie traslada una sola vez al pool la cookie única heredada del kv.
// Tres reglas vitales: pasar por accountAdopt sin validar el SAPISID (la ruta antigua no
// validaba, y una actualización no debe cambiar la disponibilidad), marcar como completado
// solo si la entrada al pool tuvo éxito, y no tocar el valor original del kv (para poder
// volver a la versión antigua y seguir usándolo).
func migrateLegacyCookie() {
	if kvGet(kvLegacyCookieDone) == "1" {
		return
	}
	raw := strings.TrimSpace(kvGet("google_cookie"))
	if raw == "" {
		_ = kvSet(kvLegacyCookieDone, "1")
		return
	}
	cookie, ok := normalizeCookie(raw, "cookie única de la página Ajustes")
	if !ok {
		return
	}
	if !poolHasCookie(cookie) {
		if _, err := accountAdopt("cookie original", cookie, "migrado desde la página Ajustes"); err != nil {
			logf("[cookie] fallo al migrar al pool la cookie única de la página Ajustes; se conserva el valor original y se reintentará en el próximo arranque: %v", err)
			return
		}
		logf("[cookie] la cookie única de la página Ajustes ya se migró al pool de cookies")
	}
	_ = kvSet(kvLegacyCookieDone, "1")
}

// syncSeededCookieFile hace que en el pool siga una fila a --cookie-file.
//
// El mismo esquema que con --proxy: si el contenido del archivo cambia, se retira la fila
// antigua y se crea una nueva, en lugar de añadir otra. La deduplicación por contenido no
// evita esto: un despliegue que rotacione cookie.txt periódicamente iría apilando cookies
// muertas en el pool, que seguirían enabled y participando en la rotación, así que cada N
// peticiones habría una condenada al fracaso. Si el contenido no cambia, no se toca el pool
// en absoluto; las altas, bajas, cambios y desactivaciones del panel mandan en el panel.
func syncSeededCookieFile() {
	var cur string
	if cfg.CookieFile != "" {
		data, err := os.ReadFile(cfg.CookieFile)
		if err != nil {
			// Si no se puede leer, se trata como no configurado: mejor mantener el estado
			// actual que retirar la cookie que el usuario está usando porque al contenedor le
			// falte un volumen.
			logf("[cookie] no se puede leer --cookie-file %s; el pool no cambia: %v", cfg.CookieFile, err)
			return
		}
		cur = strings.TrimSpace(string(data))
	}
	if cur != "" {
		if c, ok := normalizeCookie(cur, "--cookie-file"); ok {
			cur = c
		} else {
			return
		}
	}

	prev := kvGet(kvSeededCookieVal)
	if cur == prev {
		return
	}
	dropSeededCookie(prev)
	if cur == "" {
		_ = kvSet(kvSeededCookieVal, "")
		_ = kvSet(kvSeededCookieID, "")
		logf("[cookie] --cookie-file se ha eliminado; se retira también su fila del pool")
		return
	}
	if poolHasCookie(cur) {
		_ = kvSet(kvSeededCookieVal, cur)
		_ = kvSet(kvSeededCookieID, "")
		return
	}
	id, err := accountAdopt("cookie-file", cur, "desde --cookie-file")
	if err != nil {
		logf("[cookie] fallo al incorporar --cookie-file al pool: %v", err)
		return
	}
	_ = kvSet(kvSeededCookieVal, cur)
	_ = kvSet(kvSeededCookieID, strconv.FormatInt(id, 10))
	logf("[cookie] la cookie de --cookie-file ya se ha añadido al pool de cookies")
}

// dropSeededCookie retira la fila creada la última vez por --cookie-file.
// Solo se borra si el contenido sigue siendo el que escribimos: si el usuario lo ha cambiado
// en el panel, es que ha tomado el control.
func dropSeededCookie(prevCookie string) {
	idStr := kvGet(kvSeededCookieID)
	if idStr == "" || prevCookie == "" {
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		return
	}
	for _, a := range accountList() {
		if a.ID == id {
			if a.Cookie == prevCookie {
				if err := accountDelete(id); err != nil {
					logf("[cookie] fallo al retirar el registro antiguo de --cookie-file: %v", err)
				}
			}
			return
		}
	}
}

// Las cookies que enumera el modo de relleno por ítems del panel, y a la vez el orden fijo
// al componer la cadena. El orden debe ser determinista: si una misma cookie compone dos
// cadenas por distinto orden de claves, la deduplicación por contenido deja de funcionar.
var cookieTemplateOrder = []string{
	"SID", "HSID", "SSID", "APISID", "SAPISID", "__Secure-1PSID", "__Secure-1PSIDTS",
}

// normalizeCookie normaliza las distintas formas de entrada a la cadena desnuda
// "k=v; k=v" que quiere el pool.
//
// Acepta tres:
//   - cadena desnuda, se devuelve tal cual
//   - {"cookie":"k=v; k=v","sapisid":"..."} de la ruta antigua de cookie única
//   - {"SID":"a","SAPISID":"b",...} del modo por ítems del panel
//
// Sin normalizar, en el pool se guardaría un bloque JSON entero, y la extracción de SAPISID
// y las peticiones posteriores fallarían todos.
func normalizeCookie(raw, who string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		return raw, true
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		logf("[cookie] %s es JSON pero falla al analizarse; se omite: %v", who, err)
		return "", false
	}
	if c, ok := m["cookie"].(string); ok && strings.TrimSpace(c) != "" {
		return strings.TrimSpace(c), true
	}

	str := func(k string) string {
		s, _ := m[k].(string)
		return strings.TrimSpace(s)
	}
	var parts []string
	inTemplate := map[string]bool{}
	for _, k := range cookieTemplateOrder {
		inTemplate[k] = true
		if v := str(k); v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	// Si el usuario copia de más algunos ítems desde DevTools no se deben perder: se añaden
	// tras los campos de la plantilla, ordenados por nombre
	var extra []string
	for k := range m {
		if !inTemplate[k] && k != "sapisid" && str(k) != "" {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		parts = append(parts, k+"="+str(k))
	}
	if len(parts) == 0 {
		logf("[cookie] %s es JSON pero no tiene ni un valor no vacío; se omite", who)
		return "", false
	}
	return strings.Join(parts, "; "), true
}

func poolHasCookie(cookie string) bool {
	for _, a := range accountList() {
		if a.Cookie == cookie {
			return true
		}
	}
	return false
}

// pickMu convierte «SELECT de la cuenta usada hace más tiempo + UPDATE que la marca como
// recién usada» en una operación atómica. Sin el bloqueo, dos peticiones concurrentes harían
// SELECT de la misma cuenta "usada hace más tiempo" y cada una su UPDATE, de modo que la
// usarían a la vez en el mismo instante y la rotación sería papel mojado (defecto conocido
// anotado en CLAUDE.md). Con un solo mutex dentro del proceso basta: el limitador de tasa, el
// planificador de rotación y el pool de proxies son estado del proceso, y este proxy inverso
// es de un solo proceso por naturaleza, así que no hay selección de cuentas entre procesos;
// tampoco hace falta recurrir a transacciones/bloqueos de fila entre dialectos (sqlite no tiene
// FOR UPDATE, mysql no tiene RETURNING, y ese camino es todo ramas por dialecto). Seleccionar
// una cuenta no es más que un SELECT+UPDATE muy rápido, y la contención de la serialización es
// despreciable.
var pickMu sync.Mutex

// pickCookieAccount elige una cuenta enabled del pool, dando prioridad a la de last_used_at
// más antiguo, y en cuanto la elige marca last_used_at como ahora (la próxima vez le tocará a
// otra). Si el pool está vacío, devuelve (nil,false).
func pickCookieAccount() (*CookieAccount, bool) {
	return pickCookieAccountExcept(nil)
}

// pickCookieAccountExcept es igual, pero omite las cuentas ya probadas en esta operación.
//
// Motivo de su existencia: que una cookie falle no debería hacer fallar toda la petición. Si
// en el pool hay 2 cuentas y 1 está mal, la rotación hará que aproximadamente la mitad de las
// peticiones choquen con la mala: se manifiesta como "una tasa de éxito inexplicablemente
// baja", y cada fallo parece un problema del upstream.
func pickCookieAccountExcept(skip map[int64]bool) (*CookieAccount, bool) {
	pickMu.Lock()
	defer pickMu.Unlock()
	// Las sanas van delante; entre las igual de sanas, rotación por la usada hace más tiempo.
	//
	// Sin este orden, las malas entrarían en el turno de igual a igual con las buenas, y al
	// elegir una mala, como no tiene salida vinculada, la salida se escogería "sin preferencia";
	// cuando luego toca una buena, la salida ya está fijada, así que la afinidad de salida de la
	// buena queda desviada por la mala. En pruebas, con 1 buena y 2 malas la salida se repartía
	// a partes iguales entre dos proxies.
	rows, err := getDB().Query(
		`SELECT id, label, cookie, status, note, created_at, last_used_at, last_ok_at, last_error, fail_count, proxy_id
		 FROM accounts WHERE status='enabled' ORDER BY fail_count ASC, last_used_at ASC, id ASC`)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	for rows.Next() {
		var a CookieAccount
		if err := rows.Scan(&a.ID, &a.Label, &a.Cookie, &a.Status, &a.Note,
			&a.CreatedAt, &a.LastUsedAt, &a.LastOkAt, &a.LastError, &a.FailCount, &a.ProxyID); err != nil {
			continue
		}
		if skip[a.ID] {
			continue
		}
		_, _ = getDB().Exec(`UPDATE accounts SET last_used_at=? WHERE id=?`, time.Now().Unix(), a.ID)
		return &a, true
	}
	return nil, false
}

// markCookieByStatus escribe de vuelta la salud de la cookie según lo que devuelve el upstream.
//
// Solo cuenta como culpa de la cookie el fallo de autenticación explícito (401/403). Los
// errores de red, los fallos del proxy y el 302 → sorry (IP bloqueada por Google) no cuentan
// nunca: en pruebas, la tasa de degradación de las salidas de proxies residenciales llega al
// 75%, y meterlos en fail_count lo convertiría en ruido del proxy, y cookies buenas quedarían
// dañadas como "las que más fallan".
//
// statusCode igual a 0 significa que no se obtuvo respuesta alguna (fallo en la capa de red).
func markCookieByStatus(id int64, statusCode int, errStr string) {
	switch {
	case statusCode == 200:
		markAccountResult(id, true, "")
	case statusCode == 401 || statusCode == 403:
		markAccountResult(id, false, errStr)
	default:
		// En los demás casos la culpa no es de la cookie; no se toca su salud
	}
}

// markAccountResult escribe el resultado al terminar la petición: en caso de éxito pone
// fail_count a cero y registra last_ok_at; en caso de fallo incrementa fail_count y registra
// last_error.
//
// Ojo: la semántica de last_ok_at es "una petición en la que participó esta cookie tuvo
// éxito", lo que **no equivale a "la cookie sigue siendo válida"**: cuando la cookie caduca,
// Gemini no da error, solo te trata como usuario anónimo, y una petición de solo texto sigue
// devolviendo 200. Para verificar de verdad la validez, el criterio más sencillo es pedir
// gemini-3.1-pro: con la cookie válida el servidor responde "3.1 Pro" y con cadena de
// pensamiento; si no es válida, degrada en silencio a 3.5 Flash-Lite. (En xsrf.go, al obtener
// el token, si la página no tiene SNlM0e también se determina ahí mismo que la cookie no vale.)
func markAccountResult(id int64, ok bool, errStr string) {
	if id <= 0 {
		return
	}
	if ok {
		_, _ = getDB().Exec(
			`UPDATE accounts SET last_ok_at=?, fail_count=0, last_error='' WHERE id=?`,
			time.Now().Unix(), id)
		return
	}
	_, _ = getDB().Exec(
		`UPDATE accounts SET fail_count=fail_count+1, last_error=? WHERE id=?`,
		truncate(errStr, 200), id)
	autoDisableIfDead(id)
}

// maxCookieAuthFailures es cuántos fallos de autenticación consecutivos desactivan la cuenta
// automáticamente.
//
// Solo 401/403 incrementan fail_count (ver markCookieByStatus); los errores de red y los
// bloqueos de Google no cuentan, así que 3 seguidos equivalen básicamente a que la cookie ya
// no está. Se escoge 3 y no 1: un parpadeo ocasional de la página XSRF también llega por este
// camino, y desactivar a la primera haría daño.
const maxCookieAuthFailures = 3

// autoDisableIfDead desactiva la cuenta que acumula fallos consecutivos hasta el límite.
//
// Por qué se desactiva automáticamente: la selección es `ORDER BY fail_count ASC`, así que
// las malas van al final, pero **si en el pool solo quedan malas, se elegirán igualmente**, y
// entonces cada petición tendrá que probarlas antes de llegar al error. Y como fail_count solo
// se pone a cero con un éxito, una cuenta muerta nunca se recupera sola, lo que equivale a que
// cada petición pague un ida y vuelta XSRF por ella.
//
// Se desactiva en vez de eliminar: la cookie son datos que ha importado el usuario y la
// valoración puede equivocarse (por ejemplo, que la salida sea bloqueada de forma continuada
// también puede manifestarse como fallo de autenticación); se deja para que el usuario la vea
// en el panel y decida por sí mismo.
func autoDisableIfDead(id int64) {
	var fails int64
	var status string
	err := getDB().QueryRow(`SELECT fail_count, status FROM accounts WHERE id=?`, id).
		Scan(&fails, &status)
	if err != nil || status != "enabled" || fails < maxCookieAuthFailures {
		return
	}
	if _, err := getDB().Exec(
		`UPDATE accounts SET status='disabled' WHERE id=? AND status='enabled'`, id); err == nil {
		logf("[cookie] la cuenta #%d acumula %d fallos de autenticación consecutivos; se ha desactivado automáticamente", id, fails)
	}
}

// CookieCheck es el resultado de una comprobación de validez de cookie.
type CookieCheck struct {
	OK        bool   `json:"ok"`
	Detail    string `json:"detail"`
	ProxyName string `json:"proxy_name"`
	TookMs    int64  `json:"took_ms"`
}

// checkAccountCookie determina si una cookie aún tiene sesión iniciada.
//
// El criterio es si la página /app tiene SNlM0e: cuando la cookie no es válida, Gemini no da
// error, solo te trata como usuario anónimo, y una petición de solo texto sigue devolviendo
// 200, así que no se puede tomar "la petición tuvo éxito" como criterio de validez. Que esta
// página no tenga SNlM0e indica que el servidor no reconoce esta sesión iniciada.
//
// Solo se descarga la página: no se envía conversación ni se consume cuota de generación.
func checkAccountCookie(a CookieAccount) CookieCheck {
	t0 := time.Now()
	picked, ok, err := acquireSlot(a.ProxyID)
	if !ok {
		return CookieCheck{Detail: "no se puede obtener salida: " + err.Error()}
	}
	defer releaseSlot(picked.ID)
	proxyURL := picked.URL
	name := picked.Name
	if name == "" {
		name = "Directo"
	}

	// Primero se invalida la caché; de lo contrario se podría obtener una conclusión antigua
	// de hace unos minutos y la comprobación no tendría sentido
	invalidateXSRF(a.Cookie)
	_, err = getXSRF(a.Cookie, proxyURL)
	took := time.Since(t0).Milliseconds()
	if err != nil {
		markAccountResult(a.ID, false, err.Error())
		return CookieCheck{Detail: explainCookieFailure(err), ProxyName: name, TookMs: took}
	}
	markAccountResult(a.ID, true, "")
	detail := "sesión iniciada válida"
	if extractSAPISID(a.Cookie) == "" {
		detail += ", pero falta SAPISID (no se puede calcular la cabecera de autorización; en algunas interfaces se tratará como anónimo)"
	}
	return CookieCheck{OK: true, Detail: detail, ProxyName: name, TookMs: took}
}

// explainCookieFailure traduce el error de bajo nivel a una conclusión que el operador entienda.
func explainCookieFailure(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no SNlM0e"):
		return "la cookie ya no es válida: la página abre pero no hay sesión iniciada; las peticiones se tratarán como anónimas"
	case strings.Contains(msg, "HTTP 302"):
		return "cookie no válida: redirigida a la página de inicio de sesión"
	case strings.Contains(msg, "HTTP 429"), strings.Contains(msg, "sorry"):
		return "el upstream limita la tasa de la salida; pruebe con otro proxy (no es problema de la cookie)"
	default:
		return "fallo la comprobación: " + msg
	}
}

// bindAccountProxy recuerda la salida que ha usado esta cuenta esta vez, para reutilizarla con
// preferencia la próxima.
func bindAccountProxy(accountID, proxyID int64) {
	if accountID <= 0 {
		return
	}
	_, _ = getDB().Exec(`UPDATE accounts SET proxy_id=? WHERE id=?`, proxyID, accountID)
}

// accountDisplayName es el nombre de cuenta que se muestra en el panel/registro; si no hay
// etiqueta, usa #id.
func accountDisplayName(a *CookieAccount) string {
	if a.Label != "" {
		return a.Label
	}
	return fmt.Sprintf("#%d", a.ID)
}

// mergeSetCookie fusiona el Set-Cookie que envía la respuesta en la cadena de cookie actual.
//
// El servidor refresca SIDCC / __Secure-1PSIDCC / __Secure-3PSIDCC en casi todas las
// respuestas (en una captura de 2 horas, batchexecute lo hizo 468 veces); el navegador las
// acepta y las devuelve. Un cliente que envía siempre el valor antiguo se considera sesión
// caducada: en pruebas, una cuenta sin fusionar solo aguanta una o dos horas antes de dejar de
// ser válida.
//
// El orden se mantiene y las claves nuevas se añaden al final: el orden de las cookies en sí
// no afecta a la semántica, pero mantenerlo estable evita que los sitios que comparan por
// contenido, como poolHasCookie, las tomen por un valor nuevo cada vez.
func mergeSetCookie(cookie string, setCookie []string) string {
	if len(setCookie) == 0 {
		return cookie
	}
	updates := map[string]string{}
	for _, sc := range setCookie {
		first := strings.TrimSpace(strings.SplitN(sc, ";", 2)[0])
		i := strings.Index(first, "=")
		if i <= 0 {
			continue
		}
		name, val := first[:i], first[i+1:]
		// Las instrucciones de borrado (fecha de expiración en el pasado + valor vacío) no se
		// pueden escribir como valor nuevo
		if val == "" && strings.Contains(strings.ToLower(sc), "expires=thu, 01 jan 1970") {
			continue
		}
		updates[name] = val
	}
	if len(updates) == 0 {
		return cookie
	}
	var parts []string
	seen := map[string]bool{}
	for _, kv := range splitCookiePairs(cookie) {
		seen[kv[0]] = true
		if v, ok := updates[kv[0]]; ok {
			parts = append(parts, kv[0]+"="+v)
			continue
		}
		parts = append(parts, kv[0]+"="+kv[1])
	}
	for name, val := range updates {
		if !seen[name] {
			parts = append(parts, name+"="+val)
		}
	}
	return strings.Join(parts, "; ")
}

// cookieIdentity toma las entradas que representan "de qué cuenta se trata".
//
// SAPISID sirve para calcular la cabecera de autorización y __Secure-1PSID es la clave
// primaria de la sesión; ninguna de las dos cambia con el refresco (0 cambios en una captura
// de 2 horas: solo cambiaban las dos familias *SIDCC y *SIDTS). Por eso, si cambian, es señal
// de que esta cookie ya no es la de la cuenta original.
func cookieIdentity(cookie string) string {
	var sapisid, psid string
	for _, kv := range splitCookiePairs(cookie) {
		switch kv[0] {
		case "SAPISID":
			sapisid = kv[1]
		case "__Secure-1PSID":
			psid = kv[1]
		}
	}
	return sapisid + "|" + psid
}

// updateAccountCookie escribe en la cuenta la cookie ya refrescada.
//
// Antes de escribir, compara la identidad: al fusionar Set-Cookie, en teoría el upstream
// podría cambiar toda la sesión (por ejemplo, si la respuesta trae el SID de otra cuenta), y
// aceptarlo tal cual equivaldría a escribir las credenciales de la cuenta A en la fila de la
// cuenta B. Después, esta cuenta seguiría mostrando en el panel su etiqueta original, pero lo
// que realmente se envía es la sesión de otro, y **del todo en silencio**. Si la identidad no
// coincide, no se escribe: mejor dejar el refresco en vano.
func updateAccountCookie(id int64, cookie string) {
	if id <= 0 || cookie == "" {
		return
	}
	var cur string
	if err := getDB().QueryRow(`SELECT cookie FROM accounts WHERE id=?`, id).Scan(&cur); err == nil {
		if got, want := cookieIdentity(cookie), cookieIdentity(cur); got != want {
			logf("[cookie] la identidad del resultado del refresco de la cuenta #%d no coincide; se descarta y no se escribe de vuelta", id)
			return
		}
	}
	_, _ = getDB().Exec(`UPDATE accounts SET cookie=? WHERE id=?`, cookie, id)
}
