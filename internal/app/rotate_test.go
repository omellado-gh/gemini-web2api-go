package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// El centinela debe ser esta cadena JSPB, no puede pasar por json.Marshal: este último
// reduciría 000 a 0 y el servidor no lo aceptaría.
func TestRotate1PSIDTSBodyIsJSPBSentinel(t *testing.T) {
	const want = `[000,"-0000000000000000000"]`
	if rotate1PSIDTSBody != want {
		t.Fatalf("payload = %q, se esperaba %q", rotate1PSIDTSBody, want)
	}
	b, err := json.Marshal([]interface{}{0, "-0000000000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == rotate1PSIDTSBody {
		t.Fatal("el resultado de json.Marshal ha coincidido con el centinela por casualidad; este test debe cambiarse")
	}
}

func TestCookieSubsetKeepsOnlyNamed(t *testing.T) {
	in := "SID=a; __Secure-1PSID=psid; SAPISID=sap; __Secure-1PSIDTS=ts; NID=n"
	got := cookieSubset(in, []string{"__Secure-1PSID", "__Secure-1PSIDTS"})
	if got != "__Secure-1PSID=psid; __Secure-1PSIDTS=ts" {
		t.Fatalf("got %q", got)
	}
}

func TestCookieSubsetSkipsEmptyAndDup(t *testing.T) {
	in := "__Secure-1PSID=psid; __Secure-1PSIDTS=; __Secure-1PSID=other"
	got := cookieSubset(in, rotate1PSIDTSCookies)
	if got != "__Secure-1PSID=psid" {
		t.Fatalf("got %q", got)
	}
}

func TestCookieValue(t *testing.T) {
	in := "SID=a; __Secure-1PSID=psid==; SAPISID=sap"
	if got := cookieValue(in, "__Secure-1PSID"); got != "psid==" {
		t.Fatalf("got %q", got)
	}
	if got := cookieValue(in, "nope"); got != "" {
		t.Fatalf("missing should be empty, got %q", got)
	}
}

func TestMergeSetCookieRefreshes1PSIDTS(t *testing.T) {
	base := "SID=a; __Secure-1PSID=psid; __Secure-1PSIDTS=oldts; SAPISID=sap"
	got := mergeSetCookie(base, []string{
		"__Secure-1PSIDTS=newts; Domain=.google.com; Secure; HttpOnly",
		"__Secure-3PSIDTS=new3; Domain=.google.com; Secure; HttpOnly",
	})
	if !strings.Contains(got, "__Secure-1PSIDTS=newts") {
		t.Errorf("1PSIDTS no se refrescó: %s", got)
	}
	if strings.Contains(got, "oldts") {
		t.Errorf("el 1PSIDTS viejo sigue ahí: %s", got)
	}
	if !strings.Contains(got, "__Secure-3PSIDTS=new3") {
		t.Errorf("no se añadió el 3PSIDTS: %s", got)
	}
	for _, want := range []string{"SID=a", "__Secure-1PSID=psid", "SAPISID=sap"} {
		if !strings.Contains(got, want) {
			t.Errorf("se perdió %q: %s", want, got)
		}
	}
}

func TestAllow1PSIDTSRotateThrottle(t *testing.T) {
	const id int64 = -42
	defer func() {
		psidtsMu.Lock()
		delete(psidtsLastAt, id)
		psidtsMu.Unlock()
	}()
	if ok, _ := allow1PSIDTSRotate(id); !ok {
		t.Fatal("la primera vez debería permitirse")
	}
	note1PSIDTSAttempt(id, nil)
	ok, wait := allow1PSIDTSRotate(id)
	if ok {
		t.Fatal("dentro de 60 s debería bloquearse")
	}
	if wait <= 0 || wait > min1PSIDTSInterval {
		t.Fatalf("wait=%s", wait)
	}
}

func TestNote1PSIDTSAttemptSkipsNetworkError(t *testing.T) {
	const id int64 = -43
	defer func() {
		psidtsMu.Lock()
		delete(psidtsLastAt, id)
		psidtsMu.Unlock()
	}()
	note1PSIDTSAttempt(id, fmt.Errorf("dial tcp timeout"))
	if ok, _ := allow1PSIDTSRotate(id); !ok {
		t.Fatal("un error de red no debería registrar el límite")
	}
	note1PSIDTSAttempt(id, fmt.Errorf("RotateCookies 1PSIDTS 返回 HTTP 401（x）"))
	if ok, _ := allow1PSIDTSRotate(id); ok {
		t.Fatal("el 401 debería registrar el límite")
	}
}

func TestUniqueKeepOrder(t *testing.T) {
	got := uniqueKeepOrder([]string{"SIDCC", "__Secure-1PSIDTS", "SIDCC", "", "__Secure-1PSIDTS"})
	if strings.Join(got, ",") != "SIDCC,__Secure-1PSIDTS" {
		t.Fatalf("got %v", got)
	}
}

func TestMin1PSIDTSInterval(t *testing.T) {
	if min1PSIDTSInterval < 60*time.Second {
		t.Fatalf("un mínimo demasiado corto da 429: %s", min1PSIDTSInterval)
	}
}
