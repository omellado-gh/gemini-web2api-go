package app

import (
	"os"
	"path/filepath"
	"testing"
)

// Este grupo prueba **qué pasa al actualizar una base de datos que ya está en marcha**. Todos
// los criterios giran en torno a una cosa: que los proxies / cookies que ya tenía el usuario no
// desaparezcan de la nada ni se dupliquen en silencio durante la actualización.

func resetSeedState(t *testing.T) {
	t.Helper()
	clean := func() {
		for _, a := range accountList() {
			_ = accountDelete(a.ID)
		}
		for _, p := range listProxies() {
			_ = proxyDelete(p.ID)
		}
		for _, k := range []string{
			"google_cookie", runtimeConfigKey,
			kvLegacyCookieDone, kvSeededCookieID, kvSeededCookieVal,
			kvLegacyProxyDone, kvSeededProxyID, kvSeededProxyURL,
		} {
			_ = kvSet(k, "")
		}
		cfg.CookieFile = ""
		cfg.Proxy = ""
	}
	clean()
	t.Cleanup(clean)
}

// ── Migración puntual de valores heredados ──────────────────────────────

func TestMigrateLegacyCookie(t *testing.T) {
	resetSeedState(t)
	const raw = "SAPISID=legacy; SID=y"
	mustKV(t, "google_cookie", raw)

	seedCookiesFromConfig()

	list := accountList()
	if len(list) != 1 || list[0].Cookie != raw {
		t.Fatalf("no se trasladó al pool: %+v", list)
	}
	// El valor original se conserva: al revertir a la versión antigua esa cookie única sigue
	// sirviendo y el comportamiento no cambia
	if kvGet("google_cookie") != raw {
		t.Errorf("no debería tocar el valor original en kv")
	}

	// Si el usuario la borra del pool, un nuevo arranque no debe resucitarla
	_ = accountDelete(list[0].ID)
	seedCookiesFromConfig()
	if n := len(accountList()); n != 0 {
		t.Errorf("tras borrarla volvió a meterse; el pool tiene %d entradas", n)
	}
}

// Una cookie sin SAPISID también debe aceptarse: la versión antigua la enviaba tal cual como
// cabecera Cookie y si servía o no lo decidía el upstream. La actualización no puede hacer que la
// cuenta del usuario vuelva a anónimo en silencio solo porque hayamos añadido una validación.
func TestMigrateLegacyCookieWithoutSAPISID(t *testing.T) {
	resetSeedState(t)
	const raw = "SID=only; HSID=x; __Secure-1PSID=z"
	mustKV(t, "google_cookie", raw)

	seedCookiesFromConfig()

	list := accountList()
	if len(list) != 1 || list[0].Cookie != raw {
		t.Fatalf("se bloqueó por no tener SAPISID; el usuario quedará anónimo tras actualizar: %+v", list)
	}
	if !hasCookie() {
		t.Error("hay cuentas en el pool pero hasCookie es false")
	}
	// Pero añadir a mano el mismo valor desde el panel sí debe bloquearse: ahí el usuario ve el
	// mensaje de error al momento
	if _, err := accountAdd("手工", raw, ""); err == nil {
		t.Error("el alta manual desde el panel no debería dejar pasar una cookie sin SAPISID")
	}
}

// Un fallo de migración **no** debe marcar la tarea como completada; si no, la cookie del
// usuario nunca podría entrar en el pool.
func TestMigrateLegacyCookieBadValueKeepsRetrying(t *testing.T) {
	resetSeedState(t)
	// Formato JSON pero sin poder extraer el campo cookie: normalizeCookie lo considera fallo
	mustKV(t, "google_cookie", `{"sapisid":"x"}`)

	seedCookiesFromConfig()

	if n := len(accountList()); n != 0 {
		t.Fatalf("no debería entrar en el pool; hay %d entradas", n)
	}
	if kvGet(kvLegacyCookieDone) == "1" {
		t.Error("falló el alta en el pool pero se marcó la migración como completada; la cookie del usuario ya no tendría oportunidad de entrar")
	}
	if kvGet("google_cookie") == "" {
		t.Error("falló el alta en el pool y además se borró el valor original: es perder datos directamente")
	}
}

// El scheme no distingue mayúsculas: url.Parse pone el scheme en minúsculas, así que HTTP://
// funcionaba en la ruta de proxy estático sin validación de la 4.0.0 y no puede bloquearse al
// actualizar.
func TestProxySchemeCaseInsensitive(t *testing.T) {
	resetSeedState(t)
	mustKV(t, runtimeConfigKey, `{"proxy":"HTTP://1.2.3.4:8080"}`)

	seedProxiesFromConfig()

	list := listProxies()
	if len(list) != 1 || list[0].URL != "HTTP://1.2.3.4:8080" {
		t.Fatalf("se bloqueó el scheme en mayúsculas; el proxy del usuario no funcionará tras actualizar: %+v", list)
	}
}

// Un valor sin scheme sí lo rechaza proxyCreate; aquí se garantiza que al rechazarlo no se
// pierde el valor original ni se marca como completado.
//
// El tratamiento difiere del de la cookie (allí se relajó la validación): `1.2.3.4:8080` en la
// 4.0.0 **nunca llegó a funcionar** (url.Parse da error directamente: first path segment in URL
// cannot contain colon), t.Proxy se queda en nil y las peticiones van por conexión directa. Así
// que rechazarlo no supone una regresión funcional y no hace falta relajarlo.
func TestMigrateLegacyProxyRejectedKeepsRetrying(t *testing.T) {
	resetSeedState(t)
	mustKV(t, runtimeConfigKey, `{"proxy":"1.2.3.4:8080","per_ip_rph":80}`)

	seedProxiesFromConfig()

	if n := len(listProxies()); n != 0 {
		t.Fatalf("un valor sin scheme no debería entrar en el pool; hay %d entradas", n)
	}
	if kvGet(kvLegacyProxyDone) == "1" {
		t.Error("falló el alta en el pool pero se marcó la migración como completada")
	}
	// El runtime_config original no puede tocarse ni una letra: contiene otros campos
	if got := kvGet(runtimeConfigKey); got != `{"proxy":"1.2.3.4:8080","per_ip_rph":80}` {
		t.Errorf("se ha reescrito runtime_config: %s", got)
	}
}

func TestMigrateLegacyProxyOK(t *testing.T) {
	resetSeedState(t)
	mustKV(t, runtimeConfigKey, `{"proxy":"http://u:p@1.2.3.4:8080","per_ip_rph":80}`)

	seedProxiesFromConfig()

	list := listProxies()
	if len(list) != 1 || list[0].URL != "http://u:p@1.2.3.4:8080" {
		t.Fatalf("no se trasladó al pool: %+v", list)
	}
	// La migración no reescribe runtime_config: cuanto menos se toquen los datos existentes,
	// menos riesgo de estropear otros campos
	if got := kvGet(runtimeConfigKey); got != `{"proxy":"http://u:p@1.2.3.4:8080","per_ip_rph":80}` {
		t.Errorf("se ha reescrito runtime_config: %s", got)
	}
	// Idempotencia: otro arranque no debe añadir una entrada de más
	seedProxiesFromConfig()
	if n := len(listProxies()); n != 1 {
		t.Errorf("el segundo arranque añadió otra; el pool tiene %d entradas", n)
	}
}

// ── Seguimiento declarativo de los parámetros de arranque ───────────────

// Cambiar el --proxy del compose debe **reemplazar** esa entrada, no añadir otra.
// Si se acumulan, la salida vieja se queda en el pool todavía enabled, sigue recibiendo tráfico
// y se convierte en una salida zombi.
func TestSeededProxyReplacedOnChange(t *testing.T) {
	resetSeedState(t)
	cfg.Proxy = "http://old:1080"
	seedProxiesFromConfig()
	if list := listProxies(); len(list) != 1 || list[0].URL != "http://old:1080" {
		t.Fatalf("la primera siembra está mal: %+v", list)
	}

	cfg.Proxy = "http://new:1080" // el usuario cambió el compose y reinició
	seedProxiesFromConfig()

	list := listProxies()
	if len(list) != 1 {
		t.Fatalf("debería reemplazarse por 1 entrada, hay %d: %+v", len(list), list)
	}
	if list[0].URL != "http://new:1080" {
		t.Errorf("se cambió a %s, se esperaba http://new:1080", list[0].URL)
	}

	// El parámetro se elimina del todo → también se retira la entrada que creamos
	cfg.Proxy = ""
	seedProxiesFromConfig()
	if n := len(listProxies()); n != 0 {
		t.Errorf("tras quitar --proxy debería retirarse; el pool tiene %d entradas", n)
	}
}

// Si el parámetro no cambia, no se toca el pool: si el usuario desactiva/borra este registro en
// el panel, es su decisión.
func TestSeededProxyRespectsPanelEdits(t *testing.T) {
	resetSeedState(t)
	cfg.Proxy = "http://seed:1080"
	seedProxiesFromConfig()
	id := listProxies()[0].ID

	no := false
	if err := proxyUpdate(id, "", "", &no, nil); err != nil { // el usuario lo desactiva en el panel
		t.Fatal(err)
	}
	seedProxiesFromConfig() // reinicio

	list := listProxies()
	if len(list) != 1 {
		t.Fatalf("no debería tocar el pool; hay %d entradas", len(list))
	}
	if list[0].Enabled {
		t.Error("se reactivó un registro que el usuario había desactivado")
	}
}

// Si el usuario ya añadió la misma salida en el panel, la siembra no debe crear otra duplicada.
func TestSeededProxySkipsExistingURL(t *testing.T) {
	resetSeedState(t)
	if _, err := proxyCreate("手工加的", "http://same:1080", 1); err != nil {
		t.Fatal(err)
	}
	cfg.Proxy = "http://same:1080"
	seedProxiesFromConfig()
	if n := len(listProxies()); n != 1 {
		t.Errorf("se crearon %d entradas para la misma URL", n)
	}
}

// Rotación de --cookie-file: se reemplaza la misma entrada, no puede acumularse más y más.
// Las cookies muertas acumuladas siguen enabled y siguen participando en la rotación, así que
// cada N peticiones una está condenada a fallar.
func TestSeededCookieFileRotates(t *testing.T) {
	resetSeedState(t)
	path := filepath.Join(t.TempDir(), "cookie.txt")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.CookieFile = path

	write("  SAPISID=first; SID=z\n")
	seedCookiesFromConfig()
	seedCookiesFromConfig() // segundo arranque sin cambios de contenido
	list := accountList()
	if len(list) != 1 {
		t.Fatalf("sin cambios de contenido no debería insertarse de nuevo; hay %d entradas", len(list))
	}
	if list[0].Cookie != "SAPISID=first; SID=z" {
		t.Errorf("no se quitaron los espacios de los extremos: %q", list[0].Cookie)
	}

	write("SAPISID=rotated; SID=z")
	seedCookiesFromConfig()
	list = accountList()
	if len(list) != 1 {
		t.Fatalf("tras la rotación debería seguir habiendo 1 entrada, hay %d: %+v", len(list), list)
	}
	if list[0].Cookie != "SAPISID=rotated; SID=z" {
		t.Errorf("no se cambió por la nueva: %q", list[0].Cookie)
	}
}

// Si el archivo no se puede leer (por ejemplo, un volumen sin montar en el contenedor) se deja
// todo como está; no se puede retirar la cookie que está en uso.
func TestSeededCookieFileMissingKeepsPool(t *testing.T) {
	resetSeedState(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "cookie.txt")
	if err := os.WriteFile(path, []byte("SAPISID=live; SID=z"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.CookieFile = path
	seedCookiesFromConfig()
	if len(accountList()) != 1 {
		t.Fatal("no se cumple la precondición")
	}

	_ = os.Remove(path) // el volumen no está montado
	seedCookiesFromConfig()

	if n := len(accountList()); n != 1 {
		t.Errorf("el archivo no se pudo leer y se retiró la cookie; quedan %d entradas en el pool", n)
	}
}

// La ruta antigua de cookie única acepta formato JSON, y antes de entrar en el pool debe
// normalizarse a una cadena de cookie desnuda; si no, en el pool queda todo un bloque JSON y
// tanto la extracción de SAPISID como las peticiones posteriores salen mal.
func TestMigrateLegacyCookieJSONForm(t *testing.T) {
	resetSeedState(t)
	mustKV(t, "google_cookie", `{"cookie":"SAPISID=fromjson; SID=w","sapisid":"fromjson"}`)

	seedCookiesFromConfig()

	list := accountList()
	if len(list) != 1 {
		t.Fatalf("debería haber 1 entrada, hay %d", len(list))
	}
	if list[0].Cookie != "SAPISID=fromjson; SID=w" {
		t.Errorf("el JSON no se normalizó: %q", list[0].Cookie)
	}
	if got := extractSAPISID(list[0].Cookie); got != "fromjson" {
		t.Errorf("no se puede extraer SAPISID tras entrar en el pool: %q", got)
	}
}

// Despliegue nuevo: sin ninguna configuración heredada, no debe crearse cuentas ni proxies de la
// nada.
func TestSeedNoop(t *testing.T) {
	resetSeedState(t)
	seedCookiesFromConfig()
	seedProxiesFromConfig()
	if n := len(accountList()); n != 0 {
		t.Errorf("no debería crear cuentas; hay %d entradas", n)
	}
	if n := len(listProxies()); n != 0 {
		t.Errorf("no debería crear proxies; hay %d entradas", n)
	}
	if hasCookie() {
		t.Error("con el pool vacío, hasCookie debería ser false")
	}
}

func mustKV(t *testing.T, k, v string) {
	t.Helper()
	if err := kvSet(k, v); err != nil {
		t.Fatal(err)
	}
}
