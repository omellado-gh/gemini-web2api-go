package app

import (
	"os"
	"strings"
	"testing"
)

// Sonda de conectividad contra el upstream real; solo se ejecuta si GW2A_UPLOAD_PROBE=<URL de proxy> está definido.
// Un go test normal la salta: la subida necesita un token de página y una salida real, y no entra en el entorno cerrado de las pruebas unitarias.
func TestUploadProbe(t *testing.T) {
	proxy := os.Getenv("GW2A_UPLOAD_PROBE")
	if proxy == "" {
		t.Skip("no se definió GW2A_UPLOAD_PROBE")
	}
	text := "PANTE-UPLOAD-PROBE\n" + strings.Repeat("这是一段用于验证上传链路的文本。\n", 50)
	ref, err := uploadBytes("", proxy, []byte(text), "context.txt")
	if err != nil {
		t.Fatalf("fallo en la subida: %v", err)
	}
	t.Logf("subida correcta, ruta de referencia = %s", ref)
	if !strings.HasPrefix(ref, "/") {
		t.Errorf("la forma de la ruta de referencia es incorrecta: %q", ref)
	}
}
