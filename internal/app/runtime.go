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
	// ProxyCooldownMin 是代理连续失败熔断后隔多久放回池子，单位分钟。
	// 0 = 不恢复（熔断即永久除名，要手动重置）。默认按实测的封禁恢复时长取 120。
	ProxyCooldownMin int `json:"proxy_cooldown_min"`
	// FallbackDirect 决定代理池一个出口都用不上时是退回直连还是直接 429。
	// 默认 false：配了代理池就意味着不想暴露本机 IP，悄悄直连会把这个前提废掉。
	FallbackDirect bool `json:"fallback_direct"`
	// FallbackAnon 决定 cookie 失效时是降级成匿名继续跑还是直接报错。
	// 默认 false：匿名档拿不到 3.1 Pro / 扩展思考 / 生图，降级了客户端也看不出来。
	FallbackAnon bool `json:"fallback_anon"`
	// GeminiBLAuto 决定是否定期从 /app 页面抓最新的 bl 版本号覆盖上面钉死的值。
	GeminiBLAuto bool `json:"gemini_bl_auto"`
	// MaxPromptBytes 是单次请求 prompt 的 UTF-8 字节上限，超了直接报错。
	// 单位是字节不是 token：实测上游的墙按字节走，跟语言无关，见 messages.go。
	// 0 = 关掉检查（原样发出，由上游从尾部静默截断）。
	MaxPromptBytes int `json:"max_prompt_bytes"`
	// MultiTurn 开启后走 Gemini 原生 conversation_id 服务端多轮：按历史前缀识别续接，
	// 命中就只发最新一句、历史留服务端，绕开单请求字节墙。匿名/登录都行，见 conversation.go。
	// 默认 false（保持现有"每轮拼全量 prompt"行为）。实测多轮不放大上下文窗口，只让长
	// 对话不撞单请求墙——对 Codex 这类长会话有用，对"喂长文档"没用。
	MultiTurn bool `json:"multi_turn"`
	// 出完结果自动删掉 gemini.google.com 上的这条会话（#19）。只登录态生效。默认 false。
	AutoDeleteConversation bool `json:"auto_delete_conversation"`
	// 匿名优先（#20）：不需要登录态能力的请求（纯文本、非思考、无工具、无图）走匿名、
	// 不占 cookie 账号，省账号额度；需要登录才挑号。默认 false，见 modelNeedsLogin。
	AnonFirst bool `json:"anon_first"`
	// 临时对话（#34）：塞 inner[45]=1，走网页端原生「临时对话」——不进会话列表、不入训练，
	// 取代 auto_delete_conversation（省掉删除 RPC）。只登录态有意义，与 multi_turn 冲突
	// （multi_turn 开着时不发）。默认 false，见 temporaryChatEligible。
	TemporaryChat bool `json:"temporary_chat"`
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
		TemporaryChat:          cfg.TemporaryChat,
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
