package app

import (
	"encoding/json"
	"fmt"
	"sync"
)

// RuntimeConfig 是面板里能随时改、改完立刻生效的那部分配置。
//
// 部署期配置（监听地址、DB 路径、admin token、API key、cookie 文件）**不在这里**：
// 那些改了本来就要重启进程，放 docker-compose.yml 的 environment / command 更合适，
// 也避免把凭证放进一个网页表单。这里只放调优参数——为了改个超时重启一次服务太蠢。
//
// 取值优先级：面板改过的（存 kv 表） > config.json / CLI flag > 内置默认。
//
// 加字段必读：面板保存走整体反序列化（admin.go 的 Decode(&RuntimeConfig)），而前端
// saveRtCfg 只按 admin_ui 的 RT_GROUPS 拼 PUT body。新加的字段必须同步进 RT_GROUPS，
// 否则它不在 body 里、每次点「保存并生效」都被解成零值冲掉（multi_turn 就这么被静默
// 重置过，连 config.json 里设的都白设，见 #27）。
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

// initRuntimeConfig 用启动配置做基线，再把面板改过的值盖上去。
// 必须在 DB 打开之后调用。
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
				logf("[config] 忽略 kv 里不合法的运行时配置: %v", err)
			}
		}
	}
	rtMu.Lock()
	rtVal = base
	rtMu.Unlock()
}

// rtCfg 返回当前运行时配置的快照。
func rtCfg() RuntimeConfig {
	rtMu.RLock()
	defer rtMu.RUnlock()
	return rtVal
}

// validateRuntimeConfig 校验面板传来的值。
//
// 这些数字直接决定重试次数、超时和限流额度，是外部输入进敏感落点：
// 0 或负数会让限流器永远拒绝或永远放行，超大值能把单个请求挂死几小时。
// 上界给得比任何合理用法都宽，只挡明显离谱的输入。
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
			return fmt.Errorf("%s=%d 超出允许范围 [%d, %d]", r.name, r.v, r.min, r.max)
		}
	}
	if _, _, err := resolveModel(c.DefaultModel); err != nil {
		return fmt.Errorf("default_model 不可用: %v", err)
	}
	if c.Impersonate == "" {
		return fmt.Errorf("impersonate 不能为空")
	}
	if c.GeminiBL == "" {
		return fmt.Errorf("gemini_bl 不能为空")
	}
	return nil
}

// saveRuntimeConfig 校验并持久化，成功后立刻生效。
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
	logf("[config] 运行时配置已更新")
	return nil
}
