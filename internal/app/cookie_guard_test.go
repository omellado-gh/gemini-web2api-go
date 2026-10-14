package app

import "testing"

// Los fallos de autenticación consecutivos deben desactivar la cuenta automáticamente; si no,
// una cuenta muerta se queda siempre en la cola de selección y cada petición tiene que pagar
// antes un viaje XSRF por ella hasta llegar al error.
func TestAutoDisableAfterAuthFailures(t *testing.T) {
	id, err := accountAdd("dead", "SAPISID=aaa; __Secure-1PSID=bbb", "")
	if err != nil {
		t.Fatalf("fallo al insertar la cuenta: %v", err)
	}
	t.Cleanup(func() { _ = accountDelete(id) })

	for i := 1; i < maxCookieAuthFailures; i++ {
		markCookieByStatus(id, 401, "no SNlM0e")
		if a := accountByID(id); a == nil || a.Status != "enabled" {
			t.Fatalf("se desactivó en el fallo número %d, demasiado pronto", i)
		}
	}
	markCookieByStatus(id, 401, "no SNlM0e")
	if a := accountByID(id); a == nil || a.Status != "disabled" {
		t.Errorf("tras %d fallos de autenticación consecutivos debería desactivarse, got %+v", maxCookieAuthFailures, a)
	}
}

// Los fallos que no son de autenticación no deben incrementar fail_count ni, mucho menos,
// provocar la desactivación: las salidas residenciales se degradan mucho, y contar el 302 o los
// errores de red haría que una cookie buena quedara marcada como "la que más falla".
func TestNonAuthFailureDoesNotDisable(t *testing.T) {
	id, err := accountAdd("noisy", "SAPISID=ccc; __Secure-1PSID=ddd", "")
	if err != nil {
		t.Fatalf("fallo al insertar la cuenta: %v", err)
	}
	t.Cleanup(func() { _ = accountDelete(id) })

	for i := 0; i < maxCookieAuthFailures+2; i++ {
		markCookieByStatus(id, 302, "sorry page")
		markCookieByStatus(id, 0, "network error")
	}
	a := accountByID(id)
	if a == nil || a.Status != "enabled" || a.FailCount != 0 {
		t.Errorf("un fallo que no es de autenticación no debería tocar la salud, got %+v", a)
	}
}

// El éxito debe poner el contador a cero; si no, los fallos esporádicos se acumularían hasta el
// umbral de desactivación.
func TestSuccessResetsFailCount(t *testing.T) {
	id, err := accountAdd("flappy", "SAPISID=eee; __Secure-1PSID=fff", "")
	if err != nil {
		t.Fatalf("fallo al insertar la cuenta: %v", err)
	}
	t.Cleanup(func() { _ = accountDelete(id) })

	markCookieByStatus(id, 401, "x")
	markCookieByStatus(id, 200, "")
	markCookieByStatus(id, 401, "x")
	markCookieByStatus(id, 401, "x")
	if a := accountByID(id); a == nil || a.Status != "enabled" {
		t.Errorf("si hubo un éxito por medio debería ponerse a cero y no desactivarse, got %+v", a)
	}
}

// La identidad del resultado del refresco debe coincidir con la de la base de datos para
// escribirse. Sin esa comprobación, si el upstream cambiara toda la sesión en la respuesta, las
// credenciales de la cuenta A se escribirían en silencio en la fila de la cuenta B: el panel
// seguiría mostrando la etiqueta original, pero lo que se envía de verdad es la sesión de otra.
func TestUpdateAccountCookieIdentityGuard(t *testing.T) {
	orig := "SAPISID=keep; __Secure-1PSID=same; SIDCC=old"
	id, err := accountAdd("guarded", orig, "")
	if err != nil {
		t.Fatalf("fallo al insertar la cuenta: %v", err)
	}
	t.Cleanup(func() { _ = accountDelete(id) })

	// Solo se refresca SIDCC: la identidad no cambia, debería escribirse
	refreshed := "SAPISID=keep; __Secure-1PSID=same; SIDCC=new"
	updateAccountCookie(id, refreshed)
	if a := accountByID(id); a == nil || a.Cookie != refreshed {
		t.Errorf("un refresco con la misma identidad debería escribirse, got %+v", a)
	}

	// Se cambió SAPISID: ya no es la misma cuenta, debe descartarse
	updateAccountCookie(id, "SAPISID=other; __Secure-1PSID=same; SIDCC=x")
	if a := accountByID(id); a == nil || a.Cookie != refreshed {
		t.Errorf("con SAPISID cambiado debería descartarse sin escribir, got cookie=%q", a.Cookie)
	}

	// Se cambió __Secure-1PSID: igual
	updateAccountCookie(id, "SAPISID=keep; __Secure-1PSID=other; SIDCC=x")
	if a := accountByID(id); a == nil || a.Cookie != refreshed {
		t.Errorf("con __Secure-1PSID cambiado debería descartarse sin escribir, got cookie=%q", a.Cookie)
	}
}
