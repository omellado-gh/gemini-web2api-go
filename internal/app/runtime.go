package app

import (
	"encoding/json"
	"fmt"
	"sync"
)

// RuntimeConfig es la parte de la configuración que se puede cambiar desde el panel en cualquier momento y que surte efecto al instante.
//
// La configuración de despliegue (dirección de escucha, ruta de DB, admin token, API key, archivo de cookies) **no está aquí**:
// cambiarla obliga a reiniciar el proceso de todos modos, así que es mejor ponerla en environment / command de docker-compose.yml,
// y además evita meter credenciales en un formulario web. Aquí solo van parámetros de ajuste: reiniciar el servicio por cambiar un timeout es absurdo.
//
// Prioridad de valores: lo modificado en el panel (guardado en la tabla kv) > config.json / CLI flag > valores por defecto integrados.
//
// Obligatorio leer al añadir campos: el guardado del panel hace una deserialización completa (Decode(&RuntimeConfig) en admin.go), y el frontend
// saveRtCfg solo arma el body del PUT según RT_GROUPS de admin_ui. Los campos nuevos deben sincronizarse en RT_GROUPS,
// o no estarán en el body y cada clic en «Guardar y aplicar» los resolverá a valor cero y los pisará (así se restableció silenciosamente
// multi_turn, dejando inútil incluso lo configurado en config.json; ver #27).
type RuntimeConfig struct {
	RetryAttempts   int    `json:"retry_attempts"`
	RetryDelaySec   int    `json:"retry_delay_sec"`
	RequestTimeout  int    `json:"request_timeout_sec"`
	DefaultModel    string `json:"default_model"`
	PerIPConcurrent int    `json:"per_ip_concurrent"`
	PerIPRPM        int    `json:"per_ip_rpm"`
	PerIPRPH        int    `json:"per_ip_rph"`
	RetentionDays   int    `json:"retention_days"`
	LogRequests     bool   `json:"log_requests"`
	Impersonate     string `json:"impersonate"`
	GeminiBL        string `json:"gemini_bl"`
	// ProxyCooldownMin es cuánto tiempo, en minutos, se espera antes de devolver al pool un proxy tras cortarlo por fallos consecutivos.
	// 0 = no se recupera (el corte lo elimina permanentemente y hay que reiniciarlo a mano). Por defecto se toma 120 según la duración de recuperación de bloqueo medida.
	ProxyCooldownMin int `json:"proxy_cooldown_min"`
	// FallbackDirect decide, cuando no se puede usar ninguna salida del pool de proxies, si se retrocede a Directo o se devuelve 429 directamente.
	// Por defecto false: configurar un pool de proxies implica que no se quiere exponer la IP local; una conexión Directa silenciosa anularía esa premisa.
	FallbackDirect bool `json:"fallback_direct"`
	// FallbackAnon decide, cuando caduca la cookie, si se degrada a anónimo y se sigue o se devuelve error directamente.
	// Por defecto false: el modo anónimo no obtiene 3.1 Pro / pensamiento extendido / generación de imágenes, y el cliente no notaría la degradación.
	FallbackAnon bool `json:"fallback_anon"`
	// GeminiBLAuto decide si se obtiene periódicamente de la página /app el último número de versión bl para sobrescribir el valor fijado arriba.
	GeminiBLAuto bool `json:"gemini_bl_auto"`
	// MaxPromptBytes es el límite superior en bytes UTF-8 del prompt de una sola petición; si se supera, devuelve error directamente.
	// La unidad son bytes, no tokens: se comprobó que el muro del upstream va por bytes, con independencia del idioma; ver messages.go.
	// 0 = desactiva la comprobación (se envía tal cual y el upstream lo trunca silenciosamente por el final).
	MaxPromptBytes int `json:"max_prompt_bytes"`
	// Con MultiTurn activado se usa la conversación nativa de servidor de Gemini vía conversation_id: se detecta la continuación por el prefijo del historial,
	// y si coincide solo se envía la última frase, dejando el historial en el servidor, evitando el muro de bytes por petición. Vale para anónimo y con sesión iniciada; ver conversation.go.
	// Por defecto false (mantiene el comportamiento actual de "recomponer el prompt completo en cada turno"). Se comprobó que el multirround no amplía la ventana de contexto, solo hace que las conversaciones
	// largas no choquen con el muro por petición: sirve para conversaciones largas como las de Codex, no para "alimentar un documento largo".
	MultiTurn bool `json:"multi_turn"`
	// Al terminar el resultado, borra automáticamente esta conversación en gemini.google.com (#19). Solo surte efecto con sesión iniciada. Por defecto false.
	AutoDeleteConversation bool `json:"auto_delete_conversation"`
	// Anónimo primero (#20): las peticiones que no necesitan capacidades con sesión iniciada (texto plano, sin pensamiento, sin herramientas, sin imagen) van por anónimo,
	// sin ocupar una cuenta con cookie, ahorrando cuota de cuentas; solo se elige cuenta cuando hace falta iniciar sesión. Por defecto false; ver modelNeedsLogin.
	AnonFirst bool `json:"anon_first"`
	// Arranque forzado del bucle de herramientas: en el primer turno de una sesión con tools y con
	// tool_choice auto/ausente, se eleva a "required" para que el modelo empiece a actuar en vez de
	// responder en prosa. Opt-in; por defecto false.
	ForceToolFirstTurn bool `json:"force_tool_first_turn"`
}

const runtimeConfigKey = "runtime_config"

var (
	rtMu  sync.RWMutex
	rtVal RuntimeConfig
)

// initRuntimeConfig usa la configuración de arranque como base y luego superpone los valores modificados en el panel.
// Debe llamarse después de abrir la DB.
func initRuntimeConfig() {
	base := RuntimeConfig{
		RetryAttempts:   cfg.RetryAttempts,
		RetryDelaySec:   cfg.RetryDelaySec,
		RequestTimeout:  cfg.RequestTimeout,
		DefaultModel:    cfg.DefaultModel,
		PerIPConcurrent: cfg.PerIPConcurrent,
		PerIPRPM:        cfg.PerIPRPM,
		PerIPRPH:        cfg.PerIPRPH,
		RetentionDays:   cfg.RetentionDays,
		LogRequests:     cfg.LogRequests,
		Impersonate:     cfg.Impersonate,
		GeminiBL:        cfg.GeminiBL,

		ProxyCooldownMin: cfg.ProxyCooldownMin,
		FallbackDirect:   cfg.FallbackDirect,
		FallbackAnon:     cfg.FallbackAnon,
		GeminiBLAuto:     cfg.GeminiBLAuto,
		MaxPromptBytes:   cfg.MaxPromptBytes,
		MultiTurn:        cfg.MultiTurn,

		AutoDeleteConversation: cfg.AutoDeleteConversation,
		AnonFirst:              cfg.AnonFirst,
		ForceToolFirstTurn:     cfg.ForceToolFirstTurn,
	}
	if raw := kvGet(runtimeConfigKey); raw != "" {
		saved := base
		if err := json.Unmarshal([]byte(raw), &saved); err == nil {
			if err := validateRuntimeConfig(saved); err == nil {
				base = saved
			} else {
				logf("[config] se ignora la configuración en tiempo de ejecución no válida de kv: %v", err)
			}
		}
	}
	rtMu.Lock()
	rtVal = base
	rtMu.Unlock()
}

// rtCfg devuelve una instantánea de la configuración en tiempo de ejecución actual.
func rtCfg() RuntimeConfig {
	rtMu.RLock()
	defer rtMu.RUnlock()
	return rtVal
}

// validateRuntimeConfig valida los valores que llegan del panel.
//
// Estos números determinan directamente reintentos, timeouts y cuota de limitación de tasa, y son entrada externa en puntos sensibles:
// un 0 o un negativo haría que el limitador rechace o permita siempre, y un valor enorme podría colgar una petición durante horas.
// El límite superior es más amplio que cualquier uso razonable; solo bloquea entradas claramente absurdas.
func validateRuntimeConfig(c RuntimeConfig) error {
	type rangeCheck struct {
		name     string
		v        int
		min, max int
	}
	for _, r := range []rangeCheck{
		{"retry_attempts", c.RetryAttempts, 1, 10},
		{"retry_delay_sec", c.RetryDelaySec, 0, 60},
		{"request_timeout_sec", c.RequestTimeout, 5, 600},
		{"per_ip_concurrent", c.PerIPConcurrent, 0, 1000},
		{"per_ip_rpm", c.PerIPRPM, 0, 10000},
		{"per_ip_rph", c.PerIPRPH, 0, 100000},
		{"retention_days", c.RetentionDays, 1, 3650},
		{"proxy_cooldown_min", c.ProxyCooldownMin, 0, 10080},
		{"max_prompt_bytes", c.MaxPromptBytes, 0, 10000000},
	} {
		if r.v < r.min || r.v > r.max {
			return fmt.Errorf("%s=%d fuera del rango permitido [%d, %d]", r.name, r.v, r.min, r.max)
		}
	}
	if _, _, err := resolveModel(c.DefaultModel); err != nil {
		return fmt.Errorf("default_model no disponible: %v", err)
	}
	if c.Impersonate == "" {
		return fmt.Errorf("impersonate no puede estar vacío")
	}
	if c.GeminiBL == "" {
		return fmt.Errorf("gemini_bl no puede estar vacío")
	}
	return nil
}

// saveRuntimeConfig valida y persiste, y si tiene éxito surte efecto de inmediato.
func saveRuntimeConfig(next RuntimeConfig) error {
	if err := validateRuntimeConfig(next); err != nil {
		return err
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := kvSet(runtimeConfigKey, string(b)); err != nil {
		return err
	}
	rtMu.Lock()
	rtVal = next
	rtMu.Unlock()
	logf("[config] configuración en tiempo de ejecución actualizada")
	return nil
}
