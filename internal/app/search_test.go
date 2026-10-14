package app

import (
	"os"
	"testing"
)

// Con una respuesta real de búsqueda en línea capturada, se verifica que las fuentes de grounding se puedan analizar.
func TestExtractGrounding(t *testing.T) {
	raw, err := os.ReadFile("testdata/search_raw.txt")
	if err != nil {
		t.Skipf("no hay testdata/search_raw.txt, se omite: %v", err)
	}
	sources := extractGrounding(string(raw))
	if len(sources) == 0 {
		t.Fatal("esta respuesta debería tener fuentes de búsqueda en línea, pero no se analizó ninguna")
	}
	for i, s := range sources {
		if s.URL == "" {
			t.Errorf("la URL de la fuente %d está vacía", i)
		}
		if idx := len(s.URL); idx > 0 && contains(s.URL, "#:~:text=") {
			t.Errorf("la URL de la fuente %d todavía lleva el fragmento #:~:text=: %s", i, s.URL)
		}
		t.Logf("fuente %d: %s | %s", i, s.Title, s.URL)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
