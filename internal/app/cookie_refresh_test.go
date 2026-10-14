package app

import (
	"strings"
	"testing"
)

// El servidor refresca SIDCC / __Secure-1PSIDCC / __Secure-3PSIDCC en casi todas las
// respuestas, y el cliente debe aceptarlos y volver a enviarlos. Enviar siempre el valor viejo
// provoca que se tome la sesión por caducada: medido, la cuenta aguanta una o dos horas y muere.
func TestMergeSetCookie(t *testing.T) {
	base := "SID=a; SAPISID=b; SIDCC=old1; __Secure-1PSIDCC=old2; __Secure-1PSID=c"

	got := mergeSetCookie(base, []string{
		"SIDCC=new1; expires=Sat, 07 Aug 2027 00:00:00 GMT; path=/; secure",
		"__Secure-1PSIDCC=new2; path=/; secure; httponly",
	})
	for _, want := range []string{"SIDCC=new1", "__Secure-1PSIDCC=new2"} {
		if !strings.Contains(got, want) {
			t.Errorf("no se refrescó %q: %s", want, got)
		}
	}
	// Los elementos que no se enviaron deben conservarse tal cual: perder uno equivale a
	// recortar un trozo de la sesión iniciada
	for _, want := range []string{"SID=a", "SAPISID=b", "__Secure-1PSID=c"} {
		if !strings.Contains(got, want) {
			t.Errorf("se perdió %q: %s", want, got)
		}
	}
	if strings.Contains(got, "old1") || strings.Contains(got, "old2") {
		t.Errorf("el valor viejo no se reemplazó: %s", got)
	}
}

// Las claves nuevas de la respuesta deben añadirse, no perderse.
func TestMergeSetCookieAddsNew(t *testing.T) {
	got := mergeSetCookie("SID=a", []string{"__Secure-3PSIDCC=fresh; path=/"})
	if !strings.Contains(got, "SID=a") || !strings.Contains(got, "__Secure-3PSIDCC=fresh") {
		t.Errorf("no se añadió la clave nueva: %s", got)
	}
}

// Una instrucción de borrado (valor vacío + caducidad en 1970) no debe escribirse como valor
// nuevo; si no, vaciaría una cookie perfectamente válida.
func TestMergeSetCookieIgnoresDeletion(t *testing.T) {
	got := mergeSetCookie("SID=a; SIDCC=keepme",
		[]string{"SIDCC=; expires=Thu, 01 Jan 1970 00:00:00 GMT; path=/"})
	if !strings.Contains(got, "SIDCC=keepme") {
		t.Errorf("la instrucción de borrado vació el valor: %s", got)
	}
}

// Sin Set-Cookie, devolver tal cual: si no, cada petición reordenaría la cadena de cookie y los
// puntos que comparan por contenido (poolHasCookie / deduplicación de --cookie-file) la tomarían
// por cambiada.
func TestMergeSetCookieNoop(t *testing.T) {
	base := "SID=a; SAPISID=b"
	if got := mergeSetCookie(base, nil); got != base {
		t.Errorf("con entrada vacía cambió la cadena: %q", got)
	}
	if got := mergeSetCookie(base, []string{"garbage-without-equals"}); got != base {
		t.Errorf("con un Set-Cookie imposible de analizar cambió la cadena: %q", got)
	}
}

// Un '=' en el valor (relleno base64) no puede cortarse mal.
func TestMergeSetCookieKeepsEquals(t *testing.T) {
	got := mergeSetCookie("SID=a", []string{"SIDCC=AB==; path=/"})
	if !strings.Contains(got, "SIDCC=AB==") {
		t.Errorf("el signo igual del valor se cortó mal: %s", got)
	}
}
