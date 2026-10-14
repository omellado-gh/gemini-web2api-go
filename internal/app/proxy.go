package app

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Proxy struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Enabled   bool   `json:"enabled"`
	Weight    int    `json:"weight"`
	FailCount int    `json:"fail_count"`
	LastUsed  int64  `json:"last_used"`
	LastError string `json:"last_error"`
	CreatedAt int64  `json:"created_at"`
}

var (
	proxyMu     sync.RWMutex
	proxyCache  []Proxy
	proxyCursor uint64
)

// loadProxies refresca en memoria la lista de proxies desde la DB.
//
// Si la lectura falla a mitad, **conserva el pool anterior**; nunca lo sobrescribe con
// un resultado parcial. La versión antigua tenía dos puntos de fallo silenciosos: si
// Scan daba error hacía continue (se saltaba un proxy sin avisar) y rows.Err() no se
// consultaba en absoluto (una iteración interrumpida se tomaba como lectura completa).
// Ambos acortaban o vaciaban proxyCache, y acquireSlot usa len(proxyCache)==0 para
// decidir "no hay pool de proxies", así que **un pool vacío volvía a conexión directa**
// — la IP real del operador quedaba expuesta al upstream y en los logs solo se veían
// peticiones directas esporádicas.
//
// Esta ruta se recorre en cada petición (se llama al terminar recordProxyResult), y en
// modo WAL, durante un UPDATE concurrente, rows.Next() puede perfectamente devolver
// SQLITE_BUSY: de ahí lo "esporádico".
func loadProxies() {
	rows, err := getDB().Query(`SELECT id, name, url, enabled, weight, fail_count,
        COALESCE(last_used,0), COALESCE(last_error,''), created_at FROM proxies ORDER BY id`)
	if err != nil {
		logf("[proxy] fallo al leer; se conserva el pool de proxies anterior: %v", err)
		return
	}
	defer rows.Close()
	var list []Proxy
	for rows.Next() {
		var p Proxy
		var enabled int
		if err := rows.Scan(&p.ID, &p.Name, &p.URL, &enabled, &p.Weight, &p.FailCount,
			&p.LastUsed, &p.LastError, &p.CreatedAt); err != nil {
			logf("[proxy] no se pudo leer una fila; se conserva el pool de proxies anterior: %v", err)
			return
		}
		p.Enabled = enabled == 1
		list = append(list, p)
	}
	if err := rows.Err(); err != nil {
		logf("[proxy] iteración interrumpida; se conserva el pool de proxies anterior: %v", err)
		return
	}
	proxyMu.Lock()
	proxyCache = list
	proxyMu.Unlock()
}

// Al llegar a este número de fallos consecutivos se cortocircuita el proxy; vuelve al
// pool cuando pasa el enfriamiento.
const proxyFailThreshold = 5

// proxyUsable indica si este proxy se puede usar ahora.
//
// El cortacircuitos no es una expulsión permanente: las salidas bloqueadas por Google
// se recuperan solas en 106-121 minutos según las pruebas, pero la versión antigua solo
// aceptaba fail_count<5, y una vez superado no se volvía a elegir, así que nunca llegaba
// una petición exitosa que pusiera fail_count a cero — solo quedaba reiniciar a mano
// desde el panel. El enfriamiento se cuenta desde last_used (el último intento), y al
// devolverlo al pool, un nuevo fallo reinicia el cómputo.
//
// cooldownMin<=0 desactiva el enfriamiento y recupera el comportamiento antiguo de
// "cortocircuito = expulsión permanente".
func proxyUsable(p Proxy, now int64, cooldownMin int) bool {
	if !p.Enabled {
		return false
	}
	if p.FailCount < proxyFailThreshold {
		return true
	}
	if cooldownMin <= 0 {
		return false
	}
	return now-p.LastUsed >= int64(cooldownMin)*60
}

// Claves en kv que registran el estado de migración/siembra.
const (
	kvLegacyProxyDone = "legacy_static_proxy_migrated"
	kvSeededProxyID   = "seeded_proxy_id"
	kvSeededProxyURL  = "seeded_proxy_url"
)

// seedProxiesFromConfig incorpora al pool de proxies los parámetros de arranque y el
// «proxy estático» heredado.
//
// Antes había dos entradas para proxies: el pool de proxies y el campo de texto del
// proxy estático de la página «Ajustes» (que solo se usaba si el pool estaba vacío).
// Este último no era una "versión simple" sino una **versión coja**: pasaba por la ruta
// picked.ID=0, así que compartía el mismo slot de limitación de tasa que la conexión
// directa, recordProxyResult no se llamaba nunca y por tanto no había contador de fallos,
// ni cortacircuitos, ni enfriamiento, ni estado visible en el panel. Meter uno en el pool
// es más estricto en todos los aspectos.
//
// Ahora la ruta de peticiones solo reconoce el pool; esta función se encarga de trasladar
// el valor de la entrada antigua.
func seedProxiesFromConfig() {
	migrateLegacyStaticProxy()
	syncSeededProxy()
}

// migrateLegacyStaticProxy traslada una sola vez al pool el proxy estático heredado
// de kv.
//
// Dos reglas de seguridad, ambas pensando en "la base de datos del usuario ya está en
// marcha":
//
//  1. **Solo se marca como hecho si entra al pool con éxito**; si falla se deja tal cual
//     para reintentarlo la próxima vez. En la versión antigua validateRuntimeConfig no
//     validaba este campo en absoluto, y el usuario podía perfectamente haber guardado
//     un valor sin scheme como `1.2.3.4:8080`, que proxyCreate rechaza: si se borrara el
//     valor antes de meterlo al pool, el proxy desaparecería al actualizar, todo el
//     tráfico pasaría a conexión directa y el upstream lo bloquearía.
//  2. Usar una **marca de migración independiente** sin reescribir el JSON de
//     runtime_config. Tocar una sola vez menos datos ya existentes reduce el riesgo de
//     dañar otros campos; además, al revertir a la versión antigua el proxy estático
//     sigue ahí y el comportamiento no cambia.
func migrateLegacyStaticProxy() {
	if kvGet(kvLegacyProxyDone) == "1" {
		return
	}
	v := strings.TrimSpace(legacyStaticProxy())
	if v == "" {
		_ = kvSet(kvLegacyProxyDone, "1") // No había ninguno; se marca para no parsear en cada arranque
		return
	}
	if !poolHasProxyURL(v) {
		if _, err := proxyCreate("Proxy estático anterior", v, 1); err != nil {
			logf("[proxy] fallo al migrar el proxy estático de la página «Ajustes» al pool; se conserva el valor original y se reintentará en el próximo arranque: %v", err)
			return
		}
		logf("[proxy] el proxy estático de la página «Ajustes» se ha migrado al pool de proxies")
	}
	_ = kvSet(kvLegacyProxyDone, "1")
}

// legacyStaticProxy lee en modo solo lectura el runtime_config.proxy heredado de kv.
// RuntimeConfig ya no tiene este campo, así que solo queda mirar el JSON en crudo.
func legacyStaticProxy() string {
	raw := kvGet(runtimeConfigKey)
	if raw == "" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	v, _ := m["proxy"].(string)
	return v
}

// syncSeededProxy mantiene en el pool un registro ligado a --proxy / config.json.
//
// Los parámetros de arranque son **declarativos**: si cambia el valor se actualiza el
// mismo registro, no se añade otro. Deduplicar por URL no lo evita: si el usuario cambia
// el proxy en su compose, el registro viejo se queda en el pool, sigue enabled y sigue
// recibiendo tráfico, convertido en una salida zombi que nadie sabe que sigue en uso.
//
// Si el valor no cambia **no se toca el pool en absoluto**: los altas, bajas, ediciones y
// desactivaciones que el usuario haga en el panel sobre ese registro mandan.
func syncSeededProxy() {
	url := strings.TrimSpace(cfg.Proxy)
	prev := kvGet(kvSeededProxyURL)
	if url == prev {
		return
	}
	dropSeededProxy(prev)
	if url == "" {
		_ = kvSet(kvSeededProxyURL, "")
		_ = kvSet(kvSeededProxyID, "")
		logf("[proxy] --proxy se ha eliminado de los parámetros de arranque; también se retira su registro del pool")
		return
	}
	if poolHasProxyURL(url) {
		// El usuario ya añadió la misma salida desde el panel; no se duplica, solo se anota
		_ = kvSet(kvSeededProxyURL, url)
		_ = kvSet(kvSeededProxyID, "")
		return
	}
	id, err := proxyCreate("Parámetros de arranque", url, 1)
	if err != nil {
		logf("[proxy] fallo al añadir --proxy al pool: %v", err)
		return // No se anota la URL; se reintentará en el próximo arranque
	}
	_ = kvSet(kvSeededProxyURL, url)
	_ = kvSet(kvSeededProxyID, strconv.FormatInt(id, 10))
	logf("[proxy] el proxy de --proxy / config.json se ha añadido al pool de proxies")
}

// dropSeededProxy retira el registro creado la última vez por los parámetros de
// arranque. Solo se borra si ese registro **sigue teniendo la URL con la que lo creamos**
// — si el usuario lo cambió en el panel por otra salida, es que ha tomado el control de
// ese registro y no debe caer por un cambio en los parámetros de arranque.
func dropSeededProxy(prevURL string) {
	idStr := kvGet(kvSeededProxyID)
	if idStr == "" || prevURL == "" {
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		return
	}
	proxyMu.RLock()
	var found *Proxy
	for i := range proxyCache {
		if proxyCache[i].ID == id {
			p := proxyCache[i]
			found = &p
			break
		}
	}
	proxyMu.RUnlock()
	if found == nil || found.URL != prevURL {
		return
	}
	if err := proxyDelete(id); err != nil {
		logf("[proxy] fallo al retirar el proxy antiguo de los parámetros de arranque: %v", err)
	}
}

// poolHasProxyURL indica si el pool contiene esta URL.
func poolHasProxyURL(url string) bool {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	for _, p := range proxyCache {
		if p.URL == url {
			return true
		}
	}
	return false
}

// pickProxyWithCapacity busca un proxy disponible (enabled + sin cortocircuito o con el
// enfriamiento ya pasado) y cuya limitación de tasa no esté llena.
// Devuelve (proxy, ok). Si ningún proxy sirve o todos están llenos, ok=false.
//
// Diferencia con el antiguo pickProxy: consulta trySlotAcquire para ver si el slot tiene
// capacidad; el slot que recibe quien llama debe emparejarse con slotRelease(proxy.ID).
func pickProxyWithCapacity() (Proxy, bool) { return pickProxyPreferring(0) }

// pickProxyPreferring elige preferentemente la salida preferID y solo rota a otras si
// aquella no está disponible o no tiene capacidad.
//
// Por qué conviene fijarla: si el pool de cookies y el de proxies rotan por separado, la
// misma cuenta de Google salta entre decenas de IP de salida — justo lo que Google
// identifica como uso compartido de cuenta. Cuando no se puede mantener fija, se prefiere
// cambiar de salida antes que esperar en cola: la disponibilidad primero.
func pickProxyPreferring(preferID int64) (Proxy, bool) {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	if len(proxyCache) == 0 {
		return Proxy{}, false
	}
	now := time.Now().Unix()
	cooldown := rtCfg().ProxyCooldownMin
	var pool []Proxy
	for _, p := range proxyCache {
		if proxyUsable(p, now, cooldown) {
			pool = append(pool, p)
		}
	}
	if len(pool) == 0 {
		return Proxy{}, false
	}
	if preferID > 0 {
		for _, p := range pool {
			if p.ID == preferID {
				if ok, _ := trySlotAcquire(p.ID); ok {
					return p, true
				}
				break // La vinculada está llena; se pasa a la rotación normal
			}
		}
	}
	// Desde el punto de inicio de la rotación, busca el primer slot que no esté lleno
	start := atomic.AddUint64(&proxyCursor, 1) - 1
	for i := 0; i < len(pool); i++ {
		p := pool[(int(start)+i)%len(pool)]
		if ok, _ := trySlotAcquire(p.ID); ok {
			return p, true
		}
	}
	return Proxy{}, false
}

// recordProxyResult escribe el resultado de una petición y actualiza en consecuencia esa
// misma entrada en memoria.
//
// Solo modifica la entrada en memoria, sin releer toda la tabla: esta función se llama en
// cada petición, y releer supondría un SELECT de toda la tabla justo después de que ella
// misma lanzara un UPDATE — con alta concurrencia, ese par de lectura/escritura es el que
// choca en WAL y provoca SQLITE_BUSY, es decir, la condición que vacía el pool de proxies
// y devuelve las peticiones a conexión directa.
//
// El precio es que el FailCount++ en memoria y el fail_count+1 de la DB se llevan por
// separado: si otro proceso modifica la base directamente, ambos valores divergen. Como
// un único proceso posee esta base y al reiniciar se recarga desde la DB, es aceptable.
func recordProxyResult(id int64, success bool, errStr string) {
	if id == 0 {
		return
	}
	now := time.Now().Unix()
	if success {
		_, _ = getDB().Exec(`UPDATE proxies SET fail_count=0, last_used=?, last_error='' WHERE id=?`, now, id)
	} else {
		_, _ = getDB().Exec(`UPDATE proxies SET fail_count=fail_count+1, last_used=?, last_error=? WHERE id=?`,
			now, errStr, id)
	}
	proxyMu.Lock()
	for i := range proxyCache {
		if proxyCache[i].ID != id {
			continue
		}
		proxyCache[i].LastUsed = now
		if success {
			proxyCache[i].FailCount = 0
			proxyCache[i].LastError = ""
		} else {
			proxyCache[i].FailCount++
			proxyCache[i].LastError = errStr
		}
		break
	}
	proxyMu.Unlock()
}

// CRUD ───────────────────────────────────────────────────────────────────────

func proxyCreate(name, url string, weight int) (int64, error) {
	if name == "" || url == "" {
		return 0, errors.New("name and url required")
	}
	if err := validateProxyURL(url); err != nil {
		return 0, err
	}
	if weight <= 0 {
		weight = 1
	}
	id, err := insertID(`INSERT INTO proxies(name, url, enabled, weight, created_at)
        VALUES (?,?,?,?,?)`, name, url, 1, weight, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	loadProxies()
	return id, nil
}

// validateProxyURL valida el esquema de la URL del proxy.
// Admite http / https / socks5 / socks5h.
//
// El scheme se compara sin distinguir mayúsculas: el scheme de una URL no distingue
// mayúsculas y url.Parse lo pasa todo a minúsculas, así que HTTP:// funcionaba sin
// problema en la ruta de proxy estático de 4.0.0, que no validaba nada. Si la validación
// distinguiera mayúsculas, al actualizar se rechazaría ese tipo de valor: el usuario no
// habría cambiado nada y el proxy dejaría de funcionar.
func validateProxyURL(s string) error {
	low := strings.ToLower(s)
	for _, p := range []string{"http://", "https://", "socks5://", "socks5h://"} {
		if strings.HasPrefix(low, p) {
			return nil
		}
	}
	return errors.New("la URL del proxy debe empezar por http:// / https:// / socks5:// / socks5h://")
}

func proxyUpdate(id int64, name, url string, enabled *bool, weight *int) error {
	q := `UPDATE proxies SET `
	args := []interface{}{}
	parts := []string{}
	if name != "" {
		parts = append(parts, "name=?")
		args = append(args, name)
	}
	if url != "" {
		parts = append(parts, "url=?")
		args = append(args, url)
	}
	if enabled != nil {
		v := 0
		if *enabled {
			v = 1
		}
		parts = append(parts, "enabled=?")
		args = append(args, v)
	}
	if weight != nil {
		parts = append(parts, "weight=?")
		args = append(args, *weight)
	}
	if len(parts) == 0 {
		return nil
	}
	q += joinComma(parts) + " WHERE id=?"
	args = append(args, id)
	_, err := getDB().Exec(q, args...)
	if err == nil {
		loadProxies()
	}
	return err
}

func proxyDelete(id int64) error {
	_, err := getDB().Exec(`DELETE FROM proxies WHERE id=?`, id)
	if err == nil {
		loadProxies()
	}
	return err
}

func proxyResetFailures(id int64) error {
	_, err := getDB().Exec(`UPDATE proxies SET fail_count=0, last_error='' WHERE id=?`, id)
	if err == nil {
		loadProxies()
	}
	return err
}

func listProxies() []Proxy {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	out := make([]Proxy, len(proxyCache))
	copy(out, proxyCache)
	return out
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}

// proxyNameByID busca el nombre del proxy por id; si no lo encuentra devuelve cadena
// vacía (0 = sin vincular / directo).
func proxyNameByID(id int64) string {
	if id <= 0 {
		return ""
	}
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	for _, p := range proxyCache {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}

// proxyUsableByID indica si esta salida se puede usar ahora (existe + enabled + sin
// cortocircuito o con el enfriamiento ya pasado).
// Sirve para decidir si la salida vinculada a la cuenta sigue siendo válida; solo si no
// lo es procede volver a vincular.
func proxyUsableByID(id int64) bool {
	if id <= 0 {
		return false
	}
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	now := time.Now().Unix()
	cooldown := rtCfg().ProxyCooldownMin
	for _, p := range proxyCache {
		if p.ID == id {
			return proxyUsable(p, now, cooldown)
		}
	}
	return false
}

// proxyURLByID obtiene la URL del proxy por id; si no la encuentra devuelve cadena vacía
// (directo).
func proxyURLByID(id int64) string {
	if id <= 0 {
		return ""
	}
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	for _, p := range proxyCache {
		if p.ID == id {
			return p.URL
		}
	}
	return ""
}
