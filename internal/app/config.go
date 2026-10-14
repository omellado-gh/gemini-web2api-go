package app

import (
	"encoding/json"
	"os"
	"sync"
)

type Config struct {
	Port           int    `json:"port"`
	Host           string `json:"host"`
	RetryAttempts  int    `json:"retry_attempts"`
	RetryDelaySec  int    `json:"retry_delay_sec"`
	RequestTimeout int    `json:"request_timeout_sec"`
	GeminiBL       string `json:"gemini_bl"`
	DefaultModel   string `json:"default_model"`
	LogRequests    bool   `json:"log_requests"`
	CookieFile     string `json:"cookie_file"`
	Proxy          string `json:"proxy"` // solo se usa para sembrar el pool de proxies al arranque; ver seedProxiesFromConfig
	Impersonate    string `json:"impersonate"`
	DBPath         string `json:"db_path"`
	AdminToken     string `json:"admin_token"`
	AdminEnabled   bool   `json:"admin_enabled"`
	RetentionDays  int    `json:"retention_days"`

	// Per-IP rate limit (un slot = Directo/o un proxy), 0 significa sin límite.
	// El valor por defecto toma el extremo inferior del rango medido 80-180; ver la explicación en ratelimit.go.
	PerIPConcurrent int `json:"per_ip_concurrent"` // límite de concurrencia instantánea
	PerIPRPM        int `json:"per_ip_rpm"`        // límite de peticiones por minuto
	PerIPRPH        int `json:"per_ip_rph"`        // límite de peticiones por hora

	// Cuánto tiempo, en minutos, se espera antes de devolver al pool un proxy tras cortarlo por fallos consecutivos; 0 = no se recupera.
	ProxyCooldownMin int `json:"proxy_cooldown_min"`
	// Si se retrocede a Directo cuando el pool de proxies no tiene salida disponible. Por defecto false.
	FallbackDirect bool `json:"fallback_direct"`
	// Si se degrada a anónimo y se sigue cuando caduca la cookie. Por defecto false.
	FallbackAnon bool `json:"fallback_anon"`
	// Si se obtiene automáticamente de la página /app el último número de versión bl. Por defecto true.
	GeminiBLAuto bool `json:"gemini_bl_auto"`
	// Límite superior en bytes UTF-8 del prompt de una sola petición; 0 = sin límite.
	MaxPromptBytes int `json:"max_prompt_bytes"`
	// Si se usa la conversación nativa de servidor de Gemini vía conversation_id. Por defecto false; ver conversation.go.
	MultiTurn bool `json:"multi_turn"`
	// Si al terminar el resultado se borra automáticamente esta conversación dejada en gemini.google.com (#19, rpc GzXR5e).
	// Solo surte efecto con sesión iniciada (borrar requiere XSRF); asíncrono best-effort: si falla el borrado solo se registra en el log sin afectar a la respuesta. Por defecto false.
	AutoDeleteConversation bool `json:"auto_delete_conversation"`
	// Anónimo primero (#20): cuando esta petición no necesita capacidades con sesión iniciada (texto plano, sin pensamiento, sin herramientas, sin imagen)
	// no ocupa una cuenta con cookie y va por anónimo para ahorrar cuota; solo se elige cuenta cuando hace falta iniciar sesión. Por defecto false. Ver modelNeedsLogin.
	AnonFirst bool `json:"anon_first"`
	// Arranque forzado del bucle de herramientas: en el primer turno de una sesión con tools y
	// con tool_choice auto/ausente, se eleva a "required" para que el modelo empiece a actuar
	// (listar/leer) en vez de responder en prosa. Opt-in; por defecto false.
	//
	// Cambia la semántica de la petición del cliente: un "auto" pasa a "required", así que puede
	// forzar una llamada incluso cuando el mensaje no necesita ninguna (p. ej. una pregunta de
	// chat dentro de una sesión con tools), y puede romper a clientes que dependen de "auto". Es
	// una muleta para modelos débiles; con 3.1 Pro conviene dejarlo desactivado.
	ForceToolFirstTurn bool `json:"force_tool_first_turn"`
}

var (
	cfg     Config
	cfgOnce sync.Once
)

func defaultConfig() Config {
	return Config{
		Port:           8083,
		Host:           "0.0.0.0",
		RetryAttempts:  3,
		RetryDelaySec:  2,
		RequestTimeout: 180,
		GeminiBL:       "boq_gemini-web-uiserver_20261007.01_p0",
		DefaultModel:   "gemini-3.6-flash",
		LogRequests:    true,
		CookieFile:     "",
		Proxy:          "",
		Impersonate:    "chrome_146",
		DBPath:         "./data/gemini.db",
		AdminToken:     "",
		AdminEnabled:   true,
		RetentionDays:  30,
		// Por salida medida varía entre 80-180 (lo determinan la estrategia de conexión y la calidad de la salida); en IP estática 188.
		// El RPH toma el extremo inferior 80 de forma conservadora; un despliegue a baja tasa puede subirlo mucho:
		// 10 peticiones/minuto seguidas 800 veces, a lo largo de 110 minutos, ninguna fue bloqueada.
		PerIPConcurrent: 5,
		PerIPRPM:        30,
		PerIPRPH:        80,
		// Las salidas bloqueadas se recuperan solas en 106-121 minutos, medido; el cooldown se toma en 120 minutos.
		ProxyCooldownMin: 120,
		FallbackDirect:   false,
		FallbackAnon:     false,
		GeminiBLAuto:     true,
		// El muro del upstream medido está en unos 130 mil bytes UTF-8: 129.950 bytes pasan 3/3 tanto en chino como en inglés,
		// 135.990 bytes pasan 1/3 en cada uno, 141.920 bytes pasan 1/3 en cada uno. Se toma 128000 para dejar un margen.
		MaxPromptBytes:         128000,
		MultiTurn:              false,
		AutoDeleteConversation: false,
		AnonFirst:              false,
		ForceToolFirstTurn:     false,
	}
}

func loadConfig(path string) error {
	cfg = defaultConfig()
	if path == "" {
		for _, p := range []string{"./config.json", os.ExpandEnv("$HOME/.config/gemini-web2api/config.json")} {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &cfg)
}
