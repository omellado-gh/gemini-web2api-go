package app

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

func logf(format string, args ...interface{}) {
	if rtCfg().LogRequests {
		fmt.Fprintf(os.Stderr, "[%s] %s\n",
			time.Now().Format("15:04:05"),
			fmt.Sprintf(format, args...))
	}
}

// maskKey shows "sk-gemini-XXXX...YYYY" for banner. Empty stays empty.
func maskKey(k string) string {
	if k == "" {
		return "(disabled)"
	}
	if len(k) <= 12 {
		return "****"
	}
	return k[:12] + "..." + k[len(k)-4:]
}

// Run es el punto de entrada del proceso; lo llama el main.go del directorio raíz.
func Run() {
	port := flag.Int("port", 0, "listening port (default: 8083 or config.json)")
	configPath := flag.String("config", "", "path to config.json")
	cookieFile := flag.String("cookie-file", "", "path to cookie file")
	proxy := flag.String("proxy", "", "HTTP proxy fallback when proxy pool is empty")
	impersonate := flag.String("impersonate", "", "TLS profile (chrome_146, chrome_133, firefox_147, ...)")
	dbPath := flag.String("db", "", "SQLite path (default: ./data/gemini.db)")
	adminToken := flag.String("admin-token", "", "admin token (empty = no auth, only safe on 127.0.0.1)")
	apiKey := flag.String("api-key", "", "API key for /v1/* (locked). Empty = use kv-table key (auto-gen on first boot, mutable from admin UI)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("gemini-web2api-go %s\n", Version)
		return
	}

	if err := loadConfig(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "config load error: %v\n", err)
		os.Exit(1)
	}
	if *port > 0 {
		cfg.Port = *port
	}
	if *cookieFile != "" {
		cfg.CookieFile = *cookieFile
	}
	if *proxy != "" {
		cfg.Proxy = *proxy
	}
	if *impersonate != "" {
		cfg.Impersonate = *impersonate
	}
	if *dbPath != "" {
		cfg.DBPath = *dbPath
	}
	if *adminToken != "" {
		cfg.AdminToken = *adminToken
	}
	if envTok := os.Getenv("ADMIN_TOKEN"); envTok != "" && cfg.AdminToken == "" {
		cfg.AdminToken = envTok
	}

	// boot DB and load proxy pool
	getDB()
	initRuntimeConfig() // la configuración en tiempo de ejecución modificada en el panel se superpone sobre la de arranque
	loadProxies()
	seedProxiesFromConfig() // --proxy / los proxies estáticos heredados se incorporan al pool de proxies
	seedCookiesFromConfig() // --cookie-file / la cookie única heredada se incorpora al pool de cookies
	resolvedAPIKey := initAPIKey(*apiKey)
	initTokenizer()
	startScheduler()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", requireAPIKey(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "OPTIONS":
			handleOptions(w, r)
		case "GET":
			handleModels(w, r)
		default:
			writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		}
	}))
	mux.HandleFunc("/v1/chat/completions", requireAPIKey(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "OPTIONS":
			handleOptions(w, r)
		case "POST":
			handleChatCompletions(w, r)
		default:
			writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		}
	}))
	mux.HandleFunc("/v1/responses", requireAPIKey(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "OPTIONS":
			handleOptions(w, r)
		case "POST":
			handleResponses(w, r)
		default:
			writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		}
	}))
	// /v1/videos —— generación asíncrona de vídeo con la forma de OpenAI(Sora). POST crea la tarea, GET hace polling, GET .../content descarga el MP4.
	mux.HandleFunc("/v1/videos", requireAPIKey(handleCreateVideo))
	mux.HandleFunc("/v1/videos/", requireAPIKey(handleVideoItem))
	// MCP over HTTP (Streamable HTTP): mismo proceso y mismo puerto que la interfaz OpenAI, expone web_search.
	// Se autentica con la misma API key; el cliente configura Authorization: Bearer <key> para conectarse a esta URL.
	mux.HandleFunc("/mcp", requireAPIKey(handleMCPHTTP))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			handleRoot(w, r)
		} else {
			writeJSON(w, 404, map[string]string{"error": "not found"})
		}
	})

	// ─── Admin UI + API ────────────────────────────────────────────────
	if cfg.AdminEnabled {
		mux.HandleFunc("/admin", handleAdminUI)
		mux.HandleFunc("/admin/", handleAdminUI)
		mux.HandleFunc("/admin/api/login", handleAdminLogin)
		mux.HandleFunc("/admin/api/logout", handleAdminLogout)
		mux.HandleFunc("/admin/api/stats", requireAuth(handleAdminStats))
		mux.HandleFunc("/admin/api/timeseries", requireAuth(handleAdminTimeseries))
		mux.HandleFunc("/admin/api/requests", requireAuth(handleAdminRequests))
		mux.HandleFunc("/admin/api/proxies", requireAuth(handleAdminProxies))
		mux.HandleFunc("/admin/api/proxies/", requireAuth(handleAdminProxyItem))
		mux.HandleFunc("/admin/api/apikey", requireAuth(handleAdminAPIKey))
		mux.HandleFunc("/admin/api/usage", requireAuth(handleAdminUsage))
		mux.HandleFunc("/admin/api/config", requireAuth(handleAdminConfig))
		mux.HandleFunc("/admin/api/cookies", requireAuth(handleAdminCookies))
		mux.HandleFunc("/admin/api/cookies/", requireAuth(handleAdminCookieItem))
		mux.HandleFunc("/admin/api/test", requireAuth(handleAdminTest))
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // streaming responses can be long
	}

	cookieStatus := "none (anonymous)"
	if total, enabled := accountCount(); enabled > 0 {
		cookieStatus = fmt.Sprintf("%d/%d in pool", enabled, total)
	}
	proxyStatus := "none (direct)"
	if n := len(listProxies()); n > 0 {
		proxyStatus = fmt.Sprintf("%d in pool", n)
	}
	var modelNames []string
	for n := range availableModels() {
		modelNames = append(modelNames, n)
	}
	sort.Strings(modelNames)

	fmt.Printf("gemini-web2api-go v%s\n", Version)
	fmt.Printf("  Listening:   http://%s\n", addr)
	fmt.Printf("  Base URL:    http://localhost:%d/v1\n", cfg.Port)
	fmt.Printf("  API key:     %s%s\n", maskKey(resolvedAPIKey),
		map[bool]string{true: "  (locked by flag/env)", false: "  (mutable in admin UI)"}[apiKeyLocked])
	if cfg.AdminEnabled {
		authMode := "no auth (open)"
		if cfg.AdminToken != "" {
			authMode = "token auth"
		}
		fmt.Printf("  Admin UI:    http://localhost:%d/admin  (%s)\n", cfg.Port, authMode)
	}
	fmt.Printf("  DB:          %s\n", cfg.DBPath)
	fmt.Printf("  Models:      %v\n", modelNames)
	fmt.Printf("  Cookie:      %s\n", cookieStatus)
	fmt.Printf("  Proxy:       %s\n", proxyStatus)
	fmt.Printf("  Impersonate: %s\n", rtCfg().Impersonate)
	tokInfo := "chars/4 (fallback)"
	if tokenizerOK {
		tokInfo = "tiktoken cl100k_base"
	}
	fmt.Printf("  Tokenizer:   %s\n", tokInfo)
	fmt.Printf("  Per-IP limitación de tasa: concurrencia=%d / RPM=%d / RPH=%d\n",
		rtCfg().PerIPConcurrent, rtCfg().PerIPRPM, rtCfg().PerIPRPH)
	fmt.Printf("  Retry:       %dx / %ds\n", rtCfg().RetryAttempts, rtCfg().RetryDelaySec)
	fmt.Println()
	warnEnvProxyIgnored()

	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

// warnEnvProxyIgnored avisa cuando hay variables de entorno de proxy definidas pero el pool de proxies está vacío.
//
// En Linux la costumbre es que el cliente HTTP lea automáticamente HTTP_PROXY / ALL_PROXY (la biblioteca estándar de Go ya trae
// http.ProxyFromEnvironment), pero nosotros usamos http.ProxyURL explícito y no las leemos ninguna. Es
// intencional: un export cualquiera en el host cambiaría silenciosamente la IP de salida mientras el panel sigue mostrando Directo, y al depurar
// llevaría a engaño. Pero "no surte efecto y no da ninguna señal" también es difícil de diagnosticar: hay usuarios que inyectaron estas variables siguiendo la práctica habitual de un systemd drop-in
// y tardaron mucho en descubrir que había que configurarlo en el panel. Así que, aunque no las leamos, hay que avisar.
func warnEnvProxyIgnored() {
	// Se comprueban ambas grafías, mayúsculas y minúsculas, pero solo se reporta un nombre: en Windows las variables de entorno no distinguen mayúsculas de minúsculas,
	// y listarlas todas generaría ruido como "HTTPS_PROXY / https_proxy", que parece que son dos variables.
	var set []string
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		if os.Getenv(k) != "" || os.Getenv(strings.ToLower(k)) != "" {
			set = append(set, k)
		}
	}
	if len(set) == 0 || len(listProxies()) > 0 {
		return
	}
	fmt.Printf("  ⚠ Se detectaron variables de entorno de proxy %s, pero este programa **no las lee**; actualmente va por Directo.\n"+
		"    Añádelo en «pool de proxies» del panel, o usa el parámetro de arranque --proxy (que lo importa al pool de proxies al iniciar).\n\n",
		strings.Join(set, " / "))
}
