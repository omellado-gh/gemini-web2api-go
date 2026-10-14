package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain apunta la DB a un directorio temporal antes de correr los tests.
//
// hasCookie() ahora solo mira el pool de cookies, es decir, consulta la DB; si no se hace cargo de
// cfg.DBPath, getDB() abriría el ./data/gemini.db real y los tests leerían y escribirían datos de producción.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gw2a-test-")
	if err != nil {
		panic(err)
	}
	cfg.DBPath = filepath.Join(dir, "test.db")
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// withPoolCookie mete una cuenta de prueba en el pool de cookies y la borra sola al terminar.
// Sustituye al antiguo cookieRuntime.Store: la vía de cookie única ya se eliminó.
func withPoolCookie(t *testing.T) {
	t.Helper()
	id, err := accountAdd("test", "SAPISID=dummy; SID=x", "")
	if err != nil {
		t.Fatalf("fallo al insertar la cookie de prueba: %v", err)
	}
	t.Cleanup(func() { _ = accountDelete(id) })
}
