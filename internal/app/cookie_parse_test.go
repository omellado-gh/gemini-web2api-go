package app

import "testing"

// La plantilla rellenable del panel debe poder entrar en el pool igual que una cadena de
// cookie original. El criterio es "lo que se rellena es lo que se reconstruye": si la
// normalización falla, en el pool queda todo un bloque JSON y tanto la extracción de SAPISID
// como las peticiones posteriores salen mal.
func TestNormalizeCookieForms(t *testing.T) {
	want := "SID=a; HSID=b; SSID=c; APISID=d; SAPISID=e; __Secure-1PSID=f; __Secure-1PSIDTS=g"

	cases := []struct{ name, in, want string }{
		{"原始串原样通过", want, want},
		{
			"填好的 JSON 模板",
			`{"SID":"a","HSID":"b","SSID":"c","APISID":"d","SAPISID":"e",
			  "__Secure-1PSID":"f","__Secure-1PSIDTS":"g"}`,
			want,
		},
		{
			// El orden de las claves JSON no debe afectar al resultado; de lo contrario la misma
			// cookie se tomaría como dos cuentas distintas según el orden de pegado y la
			// deduplicación dejaría de funcionar.
			"模板字段顺序打乱",
			`{"SAPISID":"e","__Secure-1PSIDTS":"g","HSID":"b","SID":"a",
			  "__Secure-1PSID":"f","APISID":"d","SSID":"c"}`,
			want,
		},
		{
			"留空的项自动跳过",
			`{"SID":"a","HSID":"","SSID":"  ","SAPISID":"e"}`,
			"SID=a; SAPISID=e",
		},
		{
			// Los campos que el usuario copia de más desde DevTools no pueden perderse: se añaden
			// tras los campos de la plantilla y se ordenan por nombre.
			"模板外的字段保留",
			`{"SID":"a","SAPISID":"e","NID":"z","AEC":"y"}`,
			"SID=a; SAPISID=e; AEC=y; NID=z",
		},
		{"旧单 cookie 的 JSON 形态", `{"cookie":"SID=a; SAPISID=e","sapisid":"e"}`, "SID=a; SAPISID=e"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := normalizeCookie(c.in, "test")
			if !ok {
				t.Fatal("fallo al analizar")
			}
			if got != c.want {
				t.Errorf("se obtuvo %q\nse esperaba %q", got, c.want)
			}
		})
	}
}

// Una plantilla vacía debe quedar bloqueada. El nombre de campo "SAPISID" también existe en la
// plantilla vacía, así que el criterio solo puede ser "¿hay algún valor no vacío?", nunca
// "¿aparece SAPISID en la cadena?".
func TestAccountAddRejectsBlankTemplate(t *testing.T) {
	resetSeedState(t)
	blank := `{"SID":"","HSID":"","SSID":"","APISID":"","SAPISID":"","__Secure-1PSID":"","__Secure-1PSIDTS":""}`
	if _, err := accountAdd("空模板", blank, ""); err == nil {
		t.Fatal("la plantilla vacía entró en el pool")
	}
	if n := len(accountList()); n != 0 {
		t.Errorf("el pool tiene %d entradas de más", n)
	}
}

// Una plantilla rellenable debe poder entrar de verdad en el pool, y lo que se guarda debe ser
// la cadena desnuda reconstruida, no el JSON.
func TestAccountAddAcceptsTemplate(t *testing.T) {
	resetSeedState(t)
	filled := `{"SID":"a","HSID":"b","SSID":"c","APISID":"d","SAPISID":"e","__Secure-1PSID":"f","__Secure-1PSIDTS":""}`
	if _, err := accountAdd("模板", filled, ""); err != nil {
		t.Fatal(err)
	}
	list := accountList()
	if len(list) != 1 {
		t.Fatalf("debería haber 1 entrada, hay %d", len(list))
	}
	want := "SID=a; HSID=b; SSID=c; APISID=d; SAPISID=e; __Secure-1PSID=f"
	if list[0].Cookie != want {
		t.Errorf("se guardó %q\nse esperaba %q", list[0].Cookie, want)
	}
	if extractSAPISID(list[0].Cookie) != "e" {
		t.Error("no se puede extraer SAPISID tras entrar en el pool")
	}
}

// Las cadenas copiadas de DevTools no siempre traen espacios. Si se dividen por "; ", el bloque
// entero se toma como una sola clave, así que SAPISID no obtiene valor, no se envía la cabecera
// Authorization y el upstream trata la petición como anónima, mientras el usuario ve un
// "éxito": otra degradación silenciosa.
func TestCookiePairsWithoutSpaces(t *testing.T) {
	for _, in := range []string{
		"SID=a; SAPISID=b; HSID=c",
		"SID=a;SAPISID=b;HSID=c",
		"  SID=a ;  SAPISID=b ;HSID=c  ",
		"SID=a;\nSAPISID=b;\nHSID=c",
	} {
		if got := extractSAPISID(in); got != "b" {
			t.Errorf("extractSAPISID(%q) = %q, se esperaba \"b\"", in, got)
		}
		if n := len(cookieNames(in)); n != 3 {
			t.Errorf("cookieNames(%q) cuenta %d elementos, se esperaban 3", in, n)
		}
	}
	// Un '=' en el valor (relleno base64) solo puede cortarse en el primer signo igual
	if got := extractSAPISID("SAPISID=ab==; SID=x"); got != "ab==" {
		t.Errorf("el signo igual del valor se cortó mal: %q", got)
	}
}

// Una cadena sin espacios antes pasaba la comprobación de subcadena de accountAdd, pero lo
// guardado quedaba inservible.
func TestAccountAddAcceptsNoSpaceCookie(t *testing.T) {
	resetSeedState(t)
	if _, err := accountAdd("无空格", "SID=a;SAPISID=b;__Secure-1PSID=c", ""); err != nil {
		t.Fatalf("una cadena sin espacios debería poder entrar en el pool: %v", err)
	}
	if extractSAPISID(accountList()[0].Cookie) != "b" {
		t.Error("no se puede extraer SAPISID tras entrar en el pool")
	}
}
