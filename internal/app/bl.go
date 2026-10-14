package app

import (
	"regexp"
	"sync"
	"time"
)

// bl es el número de versión de compilación del frontend upstream, con la forma boq_gemini-web-uiserver_20261007.01_p0,
// que cada petición StreamGenerate debe llevar. Está incrustado en el campo "cfb2h" del HTML de la página /app.
// (en 2026-10 el upstream cambió el nombre del servicio de assistant-bard-web-server a gemini-web-uiserver,
// los valores viejo y nuevo siguen sirviendo según pruebas; la regex de validación se relajó para no fijar el nombre del servicio, ver blValueRe.)
//
// Fijar un valor funciona —— un valor viejo con varios meses de retraso sigue siendo válido. Pero "sigue siendo válido" no equivale a "válido para siempre",
// y si el upstream deja de usar este valor, el síntoma es que todas las peticiones fallan en masa, y al depurar es difícil pensar de entrada
// que se trata de un número de versión caducado. Como xsrf.go ya descarga la página /app, obtenerlo de paso cuesta casi nada.
var (
	blMu       sync.RWMutex
	blFetched  string    // último valor obtenido; vacío significa que aún no se ha obtenido
	blFetchAt  time.Time // hora del último intento de obtención (se registra éxito y fallo, para evitar reintentos desmedidos tras un fallo)
	blFetching bool      // hay una obtención en curso
)

// la granularidad de cambio de bl es de días; mirarlo cada pocas horas es suficiente.
const blTTL = 6 * time.Hour

var cfb2hRe = regexp.MustCompile(`"cfb2h":"([^"]{10,120})"`)

// blValueRe fija la forma: boq_<nombre de servicio>_<fecha de 8 dígitos>.<2 dígitos>_p<número>.
// El cfb2h de la página en teoría tiene este formato, pero es una entrada externa; usarlo
// directamente para componer la URL equivale a dejar que la página del upstream decida qué petición enviamos.
// Si la forma no coincide, se trata como no obtenido y se sigue usando el valor fijado en la configuración ——
// mejor quedarse atrás que enviar cualquier cosa.
//
// El nombre de servicio no se fija (antes se fijaba assistant-bard-web-server): en 2026-10 el upstream
// renombró el servicio a gemini-web-uiserver; fijar el nombre haría que el seguimiento automático tratara el valor nuevo como "forma incorrecta"
// y lo rechazara, volviendo siempre al valor viejo de la configuración. Solo se valida la forma general prefijo boq_ + sufijo fecha.versión_p;
// el nombre de servicio puede cambiar —— basta para bloquear basura mal capturada y resiste un cambio de nombre.
var blValueRe = regexp.MustCompile(`^boq_[a-z0-9-]+_\d{8}\.\d{2}_p\d+$`)

// currentBL devuelve el bl que debe usar esta petición.
//
// No bloquea: al caducar solo lanza una obtención en segundo plano y esta vez sigue devolviendo el valor actual. bl es un número de versión que cambia lentamente,
// usar el valor viejo implica como mucho quedarse unas versiones atrás (con más de dos meses de retraso sigue sirviendo en pruebas), y no vale la pena añadirle a cada petición
// la latencia de obtener la página por él. Con auto desactivado se usa siempre el valor de la configuración.
func currentBL(proxyURL string) string {
	pinned := rtCfg().GeminiBL
	if !rtCfg().GeminiBLAuto {
		return pinned
	}

	blMu.RLock()
	val, fetchedAt, inflight := blFetched, blFetchAt, blFetching
	blMu.RUnlock()

	if !inflight && time.Since(fetchedAt) > blTTL {
		blMu.Lock()
		// Doble comprobación: bajo peticiones concurrentes solo se deja que un goroutine obtenga.
		if !blFetching && time.Since(blFetchAt) > blTTL {
			blFetching = true
			go refreshBL(proxyURL)
		}
		blMu.Unlock()
	}

	if val != "" {
		return val
	}
	return pinned
}

// refreshBL obtiene una vez /app para extraer cfb2h. Basta con una obtención anónima; este valor no depende del estado de sesión.
func refreshBL(proxyURL string) {
	defer func() {
		blMu.Lock()
		blFetching = false
		blFetchAt = time.Now() // se registra éxito y fallo; tras un fallo también hay que esperar el TTL completo antes de reintentar
		blMu.Unlock()
	}()

	body, err := fetchAppPage("", proxyURL)
	if err != nil {
		logf("[bl] fallo al obtener /app, se sigue usando el valor actual: %v", err)
		return
	}
	m := cfb2hRe.FindSubmatch(body)
	if m == nil {
		logf("[bl] no hay cfb2h en la página, se sigue usando el valor actual")
		return
	}
	got := string(m[1])
	if !blValueRe.MatchString(got) {
		logf("[bl] la forma del valor obtenido es incorrecta, se ignora: %q", truncate(got, 60))
		return
	}

	blMu.Lock()
	changed := blFetched != got
	blFetched = got
	blMu.Unlock()
	if changed {
		logf("[bl] actualizado a %s (en la configuración está fijado %s)", got, rtCfg().GeminiBL)
	}
}
