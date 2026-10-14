package app

import (
	"sort"
	"sync"
	"time"
)

// IPSlot identifica una IP independiente (id=0 = IP del host en conexión directa; id>0 = proxy del pool de proxies).
// Cada slot mantiene de forma independiente el número de concurrencia + contadores RPM/RPH de ventana deslizante.
//
// Los límites vienen de los campos cfg.PerIP* y por defecto se basan en la tolerancia medida de Google por IP única:
//   - concurrencia instantánea: 5 (50 concurrentes inmediatos pasan en pruebas, pero a medio y largo plazo dispara sorry)
//   - por minuto: 30 (se deja un margen de 2× para ráfagas)
//   - por hora: 80 (el extremo inferior del intervalo medido 80-180, ver abajo)
//
// No hay un único número para cuántas veces puede pegar una salida única; lo determinan sobre todo **la estrategia de conexión y la calidad de la salida**,
// y el criterio solo cuenta un 302 → /sorry/: con concurrencia 10 y reutilización del pool de conexiones 151/172/177, con conexión nueva cada vez 106/109
// (ambas variantes arrancan a la vez, mismo lote de salida, 80 segundos en total, con rango interno de solo 3 y 5); en IP estática 188.
//
// Y **el ritmo importa más que todo esto**: un ritmo suave de 10 veces/minuto pegó 800 veces seguidas a lo largo de 110 minutos sin ser bloqueado ni una vez.
// Tras ser bloqueado, el bloqueo es duro y se recupera automáticamente en 106-121 minutos.
//
// Por eso la ventana móvil de 1 hora hourWin es una suposición conservadora y no una conclusión medida —— la forma real del umbral se parece más
// a "una ráfaga que satura se bloquea, ir despacio no se bloquea" que a "N veces por hora". Por defecto se toma el extremo inferior del intervalo: mejor enviar de menos.
// Los despliegues que corren explícitamente a baja tasa pueden subir mucho el RPH.

type ipSlot struct {
	mu        sync.Mutex
	inflight  int
	minuteWin []int64 // marca de tiempo unix de cada petición en la ventana móvil (segundos)
	hourWin   []int64
}

var (
	slotsMu sync.RWMutex
	slots   = map[int64]*ipSlot{} // proxy_id -> slot; 0 = directo
)

func getSlot(proxyID int64) *ipSlot {
	slotsMu.RLock()
	s, ok := slots[proxyID]
	slotsMu.RUnlock()
	if ok {
		return s
	}
	slotsMu.Lock()
	defer slotsMu.Unlock()
	if s, ok := slots[proxyID]; ok {
		return s
	}
	s = &ipSlot{}
	slots[proxyID] = s
	return s
}

// trySlotAcquire intenta ocupar un slot; si se excede el límite devuelve false + código de motivo.
// Quien llama y recibe true debe invocar slotRelease() con deferred.
//
//	"concurrent" — se alcanzó el límite de concurrencia instantánea
//	"rpm"        — se superó el límite por minuto
//	"rph"        — se superó el límite por hora
func trySlotAcquire(proxyID int64) (bool, string) {
	s := getSlot(proxyID)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().Unix()
	// Limpiar ventanas caducadas
	cutMin := now - 60
	cutHour := now - 3600
	s.minuteWin = pruneTimestamps(s.minuteWin, cutMin)
	s.hourWin = pruneTimestamps(s.hourWin, cutHour)

	if rtCfg().PerIPConcurrent > 0 && s.inflight >= rtCfg().PerIPConcurrent {
		return false, "concurrent"
	}
	if rtCfg().PerIPRPM > 0 && len(s.minuteWin) >= rtCfg().PerIPRPM {
		return false, "rpm"
	}
	if rtCfg().PerIPRPH > 0 && len(s.hourWin) >= rtCfg().PerIPRPH {
		return false, "rph"
	}

	s.inflight++
	s.minuteWin = append(s.minuteWin, now)
	s.hourWin = append(s.hourWin, now)
	return true, ""
}

// slotRelease libera el contador de concurrencia (los contadores de ventana caducan solos y no se limpian aquí).
func slotRelease(proxyID int64) {
	s := getSlot(proxyID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight > 0 {
		s.inflight--
	}
}

// SlotUsage lo usa la UI de administración para ver el uso.
type SlotUsage struct {
	ProxyID   int64 `json:"proxy_id"`
	Inflight  int   `json:"inflight"`
	RPM       int   `json:"rpm"`
	RPH       int   `json:"rph"`
	LimitConc int   `json:"limit_concurrent"`
	LimitRPM  int   `json:"limit_rpm"`
	LimitRPH  int   `json:"limit_rph"`
}

func slotUsage(proxyID int64) SlotUsage {
	s := getSlot(proxyID)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	s.minuteWin = pruneTimestamps(s.minuteWin, now-60)
	s.hourWin = pruneTimestamps(s.hourWin, now-3600)
	return SlotUsage{
		ProxyID:   proxyID,
		Inflight:  s.inflight,
		RPM:       len(s.minuteWin),
		RPH:       len(s.hourWin),
		LimitConc: rtCfg().PerIPConcurrent,
		LimitRPM:  rtCfg().PerIPRPM,
		LimitRPH:  rtCfg().PerIPRPH,
	}
}

// allSlotUsage devuelve una instantánea del uso de todos los slot (conexión directa + todos los proxies).
// allSlotUsage enumera todos los slot de IP **que pueden llegar a ser programados**, incluidos los que nunca se han usado.
//
// No basta con devolver solo los que ya existen en el map slots: ese map se crea de forma perezosa la primera vez que se usa,
// y al reiniciar el servicio está vacío, así que en el panel «distancia a la línea roja de bloqueo» quedaría todo en blanco —— y esa es precisamente
// la pantalla que más hay que vigilar al desplegar. Aquí se enumera con las mismas reglas que acquireSlot: si hay proxies configurados, se listan solo proxies
// (cuando existen proxies no se vuelve a la conexión directa); de lo contrario, se lista la conexión directa.
func allSlotUsage() []SlotUsage {
	seen := map[int64]bool{}
	var ids []int64

	proxyMu.RLock()
	for _, p := range proxyCache {
		if p.Enabled {
			ids = append(ids, p.ID)
			seen[p.ID] = true
		}
	}
	hasProxies := len(ids) > 0
	proxyMu.RUnlock()

	if !hasProxies {
		ids = append(ids, 0)
		seen[0] = true
	}

	// Los slot que ya tienen contador pero fueron eliminados/deshabilitados del pool también se incluyen, si no su uso desaparecería de la nada
	slotsMu.RLock()
	for id := range slots {
		if !seen[id] && (hasProxies == (id != 0)) {
			ids = append(ids, id)
		}
	}
	slotsMu.RUnlock()

	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]SlotUsage, 0, len(ids))
	for _, id := range ids {
		out = append(out, slotUsage(id))
	}
	return out
}

func pruneTimestamps(ts []int64, cutoff int64) []int64 {
	// ts se añade de forma creciente en el tiempo; se busca la primera posición >= cutoff y se corta lo anterior
	i := 0
	for i < len(ts) && ts[i] < cutoff {
		i++
	}
	if i == 0 {
		return ts
	}
	return ts[i:]
}
