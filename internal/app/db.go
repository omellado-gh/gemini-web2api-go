package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var (
	db     *sql.DB
	dbw    *dbx
	dbOnce sync.Once
)

const schema = `
CREATE TABLE IF NOT EXISTS proxies (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL,
    url          TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    weight       INTEGER NOT NULL DEFAULT 1,
    fail_count   INTEGER NOT NULL DEFAULT 0,
    last_used    INTEGER,
    last_error   TEXT,
    created_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS requests (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    ts              INTEGER NOT NULL,
    model           TEXT NOT NULL,
    upstream_model  TEXT,
    proxy_id        INTEGER,
    proxy_name      TEXT,
    account_id      INTEGER,
    account_label   TEXT,
    status          INTEGER NOT NULL,
    error           TEXT,
    ttfb_ms         INTEGER,
    total_ms        INTEGER NOT NULL,
    prompt_chars    INTEGER NOT NULL,
    response_chars  INTEGER NOT NULL,
    prompt_tokens   INTEGER NOT NULL,
    output_tokens   INTEGER NOT NULL,
    endpoint        TEXT,
    stream          INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_requests_ts ON requests(ts);
CREATE INDEX IF NOT EXISTS idx_requests_proxy ON requests(proxy_id);
CREATE INDEX IF NOT EXISTS idx_requests_model ON requests(model);

-- Hourly aggregate (se conserva para siempre; el detalle solo 30 días)
CREATE TABLE IF NOT EXISTS stats_hourly (
    bucket          INTEGER NOT NULL,    -- unix ts of hour start
    model           TEXT NOT NULL,
    proxy_id        INTEGER NOT NULL,    -- 0 = no proxy
    requests        INTEGER NOT NULL DEFAULT 0,
    successes       INTEGER NOT NULL DEFAULT 0,
    failures        INTEGER NOT NULL DEFAULT 0,
    total_ms        INTEGER NOT NULL DEFAULT 0,
    p50_ms          INTEGER NOT NULL DEFAULT 0,
    p95_ms          INTEGER NOT NULL DEFAULT 0,
    prompt_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, model, proxy_id)
);
CREATE INDEX IF NOT EXISTS idx_hourly_bucket ON stats_hourly(bucket);

-- Daily aggregate (se conserva para siempre)
CREATE TABLE IF NOT EXISTS stats_daily (
    bucket          INTEGER NOT NULL,
    model           TEXT NOT NULL,
    proxy_id        INTEGER NOT NULL,
    requests        INTEGER NOT NULL DEFAULT 0,
    successes       INTEGER NOT NULL DEFAULT 0,
    failures        INTEGER NOT NULL DEFAULT 0,
    total_ms        INTEGER NOT NULL DEFAULT 0,
    p50_ms          INTEGER NOT NULL DEFAULT 0,
    p95_ms          INTEGER NOT NULL DEFAULT 0,
    prompt_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket, model, proxy_id)
);
CREATE INDEX IF NOT EXISTS idx_daily_bucket ON stats_daily(bucket);

CREATE TABLE IF NOT EXISTS sessions (
    token        TEXT PRIMARY KEY,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS kv (
    k TEXT PRIMARY KEY,
    v TEXT
);

-- Pool de cookies: una fila por cuenta con sesión de Google iniciada (una cadena completa de cookie de gemini.google.com).
-- Al hacer una petición se elige una enabled dando prioridad a la de last_used_at más antiguo: rotación natural + reparto del límite de una sola IP.
CREATE TABLE IF NOT EXISTS accounts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    label         TEXT NOT NULL DEFAULT '',      -- el usuario puede nombrarlo (normalmente el correo)
    cookie        TEXT NOT NULL,                 -- cadena de cookie completa "k=v; k=v"
    status        TEXT NOT NULL DEFAULT 'enabled', -- enabled | disabled
    note          TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER NOT NULL DEFAULT 0,     -- momento en que se eligió por última vez para enviar una petición
    last_ok_at    INTEGER NOT NULL DEFAULT 0,     -- momento del último éxito
    last_error    TEXT NOT NULL DEFAULT '',
    fail_count    INTEGER NOT NULL DEFAULT 0,     -- número de fallos consecutivos (vuelve a cero con un éxito)
    proxy_id      INTEGER NOT NULL DEFAULT 0      -- salida vinculada; 0 = aún sin vincular
);
CREATE INDEX IF NOT EXISTS idx_accounts_pick ON accounts(status, last_used_at);
`

func getDB() *dbx {
	dbOnce.Do(func() {
		var driver, connStr, sqliteDir string
		if dsnEnv := strings.TrimSpace(os.Getenv("SQL_DSN")); dsnEnv != "" {
			curDialect, driver, connStr = detectDSN(dsnEnv)
		} else {
			curDialect = dialectSQLite
			driver = "sqlite"
			path := cfg.DBPath
			if path == "" {
				path = "./data/gemini.db"
			}
			sqliteDir = filepath.Dir(path)
			if err := os.MkdirAll(sqliteDir, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "[db] fallo al crear el directorio %s: %v\n%s", sqliteDir, err, dbPermHint(sqliteDir))
				os.Exit(1)
			}
			connStr = fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
		}
		conn, err := sql.Open(driver, connStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[db] open failed: %v\n", err)
			os.Exit(1)
		}
		conn.SetMaxOpenConns(8)
		conn.SetMaxIdleConns(4)
		conn.SetConnMaxLifetime(0)
		// los drivers de mysql/pg no aceptan Exec multi-sentencia por defecto, así que se crean tabla por tabla según el dialecto.
		for _, stmt := range schemaStatements(curDialect) {
			if _, err := conn.Exec(stmt); err != nil {
				// el CREATE INDEX de mysql no tiene IF NOT EXISTS, y al reiniciar vuelve a dar error; se tolera.
				if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt)), "CREATE INDEX") {
					continue
				}
				// el caso más común en sqlite es SQLITE_CANTOPEN(14): se puede entrar al directorio pero no escribir; se imprimen juntos el diagnóstico y la solución.
				hint := ""
				if curDialect == dialectSQLite {
					hint = dbPermHint(sqliteDir)
				}
				fmt.Fprintf(os.Stderr, "[db] fallo de inicialización: %v\n  sentencia: %s\n%s", err, stmt, hint)
				os.Exit(1)
			}
		}
		// migración de columnas de bases sqlite antiguas. Las nuevas/mysql/pg no lo necesitan (ejecutarlo solo daría errores ignorados); se hace solo en sqlite.
		if curDialect == dialectSQLite {
			_, _ = conn.Exec(`ALTER TABLE requests ADD COLUMN upstream_model TEXT`)
			_, _ = conn.Exec(`ALTER TABLE requests ADD COLUMN account_id INTEGER`)
			_, _ = conn.Exec(`ALTER TABLE requests ADD COLUMN account_label TEXT`)
			_, _ = conn.Exec(`ALTER TABLE accounts ADD COLUMN proxy_id INTEGER NOT NULL DEFAULT 0`)
			_, _ = conn.Exec(`ALTER TABLE requests DROP COLUMN prompt_preview`)
			_, _ = conn.Exec(`ALTER TABLE requests DROP COLUMN response_preview`)
		}
		db = conn
		dbw = &dbx{conn}
		logf("[db] opened dialect=%d driver=%s", curDialect, driver)
	})
	return dbw
}

// Request rows ───────────────────────────────────────────────────────────────

type RequestRow struct {
	ID            int64  `json:"id"`
	TS            int64  `json:"ts"`
	Model         string `json:"model"`
	UpstreamModel string `json:"upstream_model"`
	ProxyID       *int64 `json:"proxy_id"`
	ProxyName     string `json:"proxy_name"`
	AccountID     *int64 `json:"account_id"`
	AccountLabel  string `json:"account_label"`
	Status        int    `json:"status"`
	Error         string `json:"error"`
	TTFBMs        *int64 `json:"ttfb_ms"`
	TotalMs       int64  `json:"total_ms"`
	PromptChars   int    `json:"prompt_chars"`
	ResponseChars int    `json:"response_chars"`
	PromptTokens  int    `json:"prompt_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	Endpoint      string `json:"endpoint"`
	Stream        int    `json:"stream"`
}

func insertRequest(r *RequestRow) {
	_, err := getDB().Exec(`INSERT INTO requests
        (ts, model, upstream_model, proxy_id, proxy_name, account_id, account_label,
         status, error, ttfb_ms, total_ms,
         prompt_chars, response_chars, prompt_tokens, output_tokens,
         endpoint, stream)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.TS, r.Model, r.UpstreamModel, r.ProxyID, r.ProxyName, r.AccountID, r.AccountLabel,
		r.Status, r.Error, r.TTFBMs, r.TotalMs,
		r.PromptChars, r.ResponseChars, r.PromptTokens, r.OutputTokens,
		r.Endpoint, r.Stream)
	if err != nil {
		logf("[db] insert request failed: %v", err)
	}
}

// Sessions ───────────────────────────────────────────────────────────────────

func createSession(token string, ttl time.Duration) {
	now := time.Now().Unix()
	_, err := getDB().Exec(upsert("sessions", []string{"token", "created_at", "expires_at"}, []string{"token"}),
		token, now, now+int64(ttl.Seconds()))
	if err != nil {
		logf("[db] session insert failed: %v", err)
	}
}

func validSession(token string) bool {
	if token == "" {
		return false
	}
	var exp int64
	err := getDB().QueryRow(`SELECT expires_at FROM sessions WHERE token=?`, token).Scan(&exp)
	if err != nil {
		return false
	}
	return time.Now().Unix() < exp
}

// KV helpers ─────────────────────────────────────────────────────────────────

func kvGet(k string) string {
	var v string
	if err := getDB().QueryRow(`SELECT v FROM kv WHERE k=?`, k).Scan(&v); err != nil {
		return ""
	}
	return v
}

func kvSet(k, v string) error {
	_, err := getDB().Exec(upsert("kv", []string{"k", "v"}, []string{"k"}), k, v)
	return err
}

// dbPermHint da una pista accionable cuando no se puede abrir la base de datos.
//
// Diagnóstico: la imagen del contenedor es distroless + USER nonroot (uid 65532), y el bind mount sobrescribe por completo el propietario de /data de la imagen
// con el propietario del directorio del host. Cuando el directorio no existe en el host, Docker lo crea automáticamente con propietario
// root, así que el 65532 del contenedor no puede escribir; esta es la causa comprobada del `unable to open database file (14)`
// (el mismo comando arranca con solo cambiar el propietario a 65532 con chown).
func dbPermHint(dir string) string {
	uid := os.Getuid() // en Windows devuelve -1; allí este problema no aplica
	return fmt.Sprintf(""+
		"      el proceso actual uid=%d no puede escribir en el directorio %s.\n"+
		"      En Docker el motivo más común es que el directorio anfitrión del bind mount es propiedad de root,\n"+
		"      y la imagen se ejecuta como nonroot(65532). Dos soluciones:\n"+
		"        1) Usar un volumen con nombre (recomendado): escribir gw2a-data:/data en volumes\n"+
		"        2) Si se conserva el bind mount, cambiar el propietario: sudo chown -R 65532:65532 ./data\n",
		uid, dir)
}
