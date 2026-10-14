package app

import "testing"

// cfb2h se extrae del HTML de la página del upstream; es entrada externa que entra directa en la URL.
// La validación de forma bloquea el caso «la estructura de la página cambió y la expresión regular
// capturó otra cosa»: si se captura mal, hay que seguir usando el valor fijado en la configuración,
// en lugar de componer la petición con una cadena sin sentido.
func TestBLValueShape(t *testing.T) {
	good := []string{
		"boq_gemini-web-uiserver_20261007.01_p0",       // valor actual tras el cambio de nombre del upstream en 2026-10
		"boq_assistant-bard-web-server_20260805.16_p0", // valor antiguo de antes del cambio de nombre; sigue aceptándose
		"boq_assistant-bard-web-server_20260525.09_p0",
		"boq_assistant-bard-web-server_20260802.16_p12",
		"boq_some-future-service_20270101.01_p0", // aunque el nombre del servicio vuelva a cambiar se acepta; solo se valida la forma general
	}
	for _, s := range good {
		if !blValueRe.MatchString(s) {
			t.Errorf("debería pasar pero fue rechazado: %q", s)
		}
	}

	bad := []string{
		"",
		"boq_assistant-bard-web-server", // sin segmento de versión
		"boq_assistant-bard-web-server_2026080.16_p0",    // fecha de 7 dígitos
		"boq_assistant-bard-web-server_20260805.163_p0",  // versión menor de 3 dígitos
		"boq_assistant-bard-web-server_20260805.16_p",    // sin dígito después de la p
		"boq_assistant-bard-web-server_20260805.16_p0&x", // cola con parámetros de URL
		"gemini-web-uiserver_20261007.01_p0",             // sin el prefijo boq_
		"../../etc/passwd",
	}
	for _, s := range bad {
		if blValueRe.MatchString(s) {
			t.Errorf("debería ser rechazado pero pasó: %q", s)
		}
	}
}

// cfb2hRe debe extraer el valor con precisión del bloque enorme de JSON en línea de una página real.
func TestCfb2hExtract(t *testing.T) {
	page := `...,"cfb2h":"boq_assistant-bard-web-server_20260805.16_p0","fJfDgd":"x",...`
	m := cfb2hRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no se encontró coincidencia para cfb2h")
	}
	if want := "boq_assistant-bard-web-server_20260805.16_p0"; m[1] != want {
		t.Errorf("se obtuvo %q, se esperaba %q", m[1], want)
	}
}
