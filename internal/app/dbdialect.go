package app

// Soporte de múltiples dialectos de base de datos: sqlite por defecto; si se define la variable de entorno SQL_DSN, va por mysql / postgres.
//
// Por qué no basta con «añadir un env»:
//   - Marcadores de posición: sqlite/mysql usan ?, postgres usa $1/$2 —— todas las consultas se ven afectadas; se reescriben centralmente con rebind.
//   - DDL de creación de tablas: AUTOINCREMENT / tipos / si TEXT puede ser clave primaria, cada motor es distinto —— se resuelve con conversión de schema.
//   - upsert: INSERT OR REPLACE(sqlite) / REPLACE INTO(mysql) / ON CONFLICT(pg) —— se genera con upsert().
// Ninguna de las ~70 consultas con ? del código de negocio hay que tocarla; todas las diferencias de dialecto se concentran en este archivo.

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

type dbDialect int

const (
	dialectSQLite dbDialect = iota
	dialectMySQL
	dialectPostgres
)

// curDialect se fija una sola vez en getDB() según SQL_DSN y después es de solo lectura global.
var curDialect = dialectSQLite

// detectDSN reconoce el dialecto a partir de SQL_DSN y devuelve el nombre del driver de database/sql y la cadena de conexión.
// Si SQL_DSN está definido, debe ser mysql/pg (si está vacío no se llega aquí y sigue siendo sqlite). Si no se reconoce, sale directamente.
func detectDSN(dsn string) (dbDialect, string, string) {
	low := strings.ToLower(strings.TrimSpace(dsn))
	switch {
	case strings.HasPrefix(low, "postgres://"), strings.HasPrefix(low, "postgresql://"):
		return dialectPostgres, "postgres", dsn // lib/pq acepta la URL directamente
	case strings.HasPrefix(low, "mysql://"):
		return dialectMySQL, "mysql", mysqlURLToDSN(dsn)
	case strings.Contains(dsn, "@tcp("):
		return dialectMySQL, "mysql", ensureMySQLParams(dsn) // DSN nativo de go-sql-driver
	default:
		fmt.Fprintf(os.Stderr, "[db] SQL_DSN no reconoce el dialecto (debe empezar por postgres:// o postgresql:// o mysql://, "+
			"o tener la forma user:pass@tcp(host:port)/db de go-sql-driver): %s\n", dsn)
		os.Exit(1)
		return dialectSQLite, "", ""
	}
}

// mysqlURLToDSN convierte mysql://user:pass@host:port/db?x=y a la forma user:pass@tcp(host:port)/db?x=y de go-sql-driver.
func mysqlURLToDSN(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[db] fallo al analizar el DSN de mysql: %v\n", err)
		os.Exit(1)
	}
	cred := ""
	if u.User != nil {
		cred = u.User.Username()
		if p, ok := u.User.Password(); ok {
			cred += ":" + p
		}
	}
	out := fmt.Sprintf("%s@tcp(%s)/%s", cred, u.Host, strings.TrimPrefix(u.Path, "/"))
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return ensureMySQLParams(out)
}

func ensureMySQLParams(dsn string) string {
	if !strings.Contains(dsn, "charset=") {
		if strings.Contains(dsn, "?") {
			dsn += "&charset=utf8mb4"
		} else {
			dsn += "?charset=utf8mb4"
		}
	}
	return dsn
}

// ── shim de marcadores de posición: getDB() devuelve *dbx y el código de negocio sigue usando .Exec/.Query/.QueryRow/.Begin ──────

type dbx struct{ raw *sql.DB }

func (d *dbx) Exec(q string, a ...any) (sql.Result, error) { return d.raw.Exec(rebind(q), a...) }
func (d *dbx) Query(q string, a ...any) (*sql.Rows, error) { return d.raw.Query(rebind(q), a...) }
func (d *dbx) QueryRow(q string, a ...any) *sql.Row        { return d.raw.QueryRow(rebind(q), a...) }
func (d *dbx) Begin() (*txx, error) {
	t, err := d.raw.Begin()
	if err != nil {
		return nil, err
	}
	return &txx{t}, nil
}

type txx struct{ raw *sql.Tx }

func (t *txx) Exec(q string, a ...any) (sql.Result, error) { return t.raw.Exec(rebind(q), a...) }
func (t *txx) Query(q string, a ...any) (*sql.Rows, error) { return t.raw.Query(rebind(q), a...) }
func (t *txx) QueryRow(q string, a ...any) *sql.Row        { return t.raw.QueryRow(rebind(q), a...) }
func (t *txx) Commit() error                               { return t.raw.Commit() }
func (t *txx) Rollback() error                             { return t.raw.Rollback() }

// rebind sustituye los ? en orden por $1,$2… (solo postgres). Ninguna de nuestras consultas tiene literales de cadena con ?, así que la sustitución secuencial es segura.
func rebind(q string) string {
	if curDialect != dialectPostgres {
		return q
	}
	var b strings.Builder
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		} else {
			b.WriteByte(q[i])
		}
	}
	return b.String()
}

// insertID ejecuta un INSERT y devuelve la clave primaria autoincremental. pg (lib/pq) no soporta LastInsertId, así que usa RETURNING id.
// La query no debe llevar punto y coma final; el nombre de la columna de clave primaria es siempre id.
func insertID(query string, args ...any) (int64, error) {
	if curDialect == dialectPostgres {
		var id int64
		err := getDB().QueryRow(query+" RETURNING id", args...).Scan(&id)
		return id, err
	}
	res, err := getDB().Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// upsert genera una sentencia «insertar o sobrescribir por clave primaria» compatible con el dialecto. cols son todas las columnas (incluida la clave primaria) y pk son las columnas de clave primaria.
func upsert(table string, cols, pk []string) string {
	ph := "?" + strings.Repeat(",?", len(cols)-1)
	collist := strings.Join(cols, ", ")
	switch curDialect {
	case dialectMySQL:
		return fmt.Sprintf("REPLACE INTO %s(%s) VALUES(%s)", table, collist, ph)
	case dialectPostgres:
		pkset := map[string]bool{}
		for _, c := range pk {
			pkset[c] = true
		}
		var sets []string
		for _, c := range cols {
			if !pkset[c] {
				sets = append(sets, c+"=excluded."+c)
			}
		}
		return fmt.Sprintf("INSERT INTO %s(%s) VALUES(%s) ON CONFLICT(%s) DO UPDATE SET %s",
			table, collist, ph, strings.Join(pk, ","), strings.Join(sets, ", "))
	default: // sqlite
		return fmt.Sprintf("INSERT OR REPLACE INTO %s(%s) VALUES(%s)", table, collist, ph)
	}
}

// ── conversión de schema según el dialecto ────────────────────────────────────────────────────────

var (
	reAutoPK      = regexp.MustCompile(`INTEGER PRIMARY KEY AUTOINCREMENT`)
	reInteger     = regexp.MustCompile(`\bINTEGER\b`)
	reTextDefault = regexp.MustCompile(`TEXT NOT NULL DEFAULT ('[^']*')`)
	reModelText   = regexp.MustCompile(`model\s+TEXT NOT NULL`) // model entra en una clave primaria compuesta/índice; en mysql TEXT no puede ser clave
	reIdxIfExists = regexp.MustCompile(`(?i)CREATE INDEX IF NOT EXISTS`)
)

// schemaStatements convierte el schema de sqlite al dialecto objetivo y lo corta en sentencias sueltas (los drivers de mysql/pg no soportan Exec multi-sentencia por defecto).
func schemaStatements(d dbDialect) []string {
	s := schema
	switch d {
	case dialectMySQL:
		s = reAutoPK.ReplaceAllString(s, "BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY")
		// en mysql TEXT no puede ser directamente clave primaria / índice / llevar DEFAULT; las columnas de texto corto se cambian a VARCHAR.
		s = strings.ReplaceAll(s, "TEXT PRIMARY KEY", "VARCHAR(255) PRIMARY KEY")
		s = reModelText.ReplaceAllString(s, "model VARCHAR(255) NOT NULL")
		s = reTextDefault.ReplaceAllString(s, "VARCHAR(512) NOT NULL DEFAULT $1")
		s = reInteger.ReplaceAllString(s, "BIGINT")
		s = reIdxIfExists.ReplaceAllString(s, "CREATE INDEX") // mysql no soporta CREATE INDEX IF NOT EXISTS
	case dialectPostgres:
		s = reAutoPK.ReplaceAllString(s, "BIGSERIAL PRIMARY KEY")
		s = reInteger.ReplaceAllString(s, "BIGINT")
	}
	// Primero se eliminan línea a línea los comentarios -- y luego se corta por ;: los comentarios pueden llevar punto y coma (p. ej. la cadena de cookie "k=v; k=v"),
	// y un split directo cortaría la sentencia por medio del comentario. En los literales de cadena de este schema no hay --, así que el recorte directo es seguro.
	var clean strings.Builder
	for _, ln := range strings.Split(s, "\n") {
		if i := strings.Index(ln, "--"); i >= 0 {
			ln = ln[:i]
		}
		clean.WriteString(ln)
		clean.WriteByte('\n')
	}
	var out []string
	for _, part := range strings.Split(clean.String(), ";") {
		if stmt := strings.TrimSpace(part); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}
