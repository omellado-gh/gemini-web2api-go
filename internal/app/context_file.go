package app

import (
	"fmt"
	"strings"
)

// Convierte conversación muy larga en archivo adjunto de texto.
//
// El upstream limita ~130000 bytes UTF-8 por solicitud; si se supera, trunca silenciosamente desde el final sin error — lo que suele eliminar el último mensaje del usuario.
// Convertir el historial en adjunto evita este límite: el cuerpo queda pequeño.
// El cuerpo solo contiene una instrucción breve, no todo el historial.
//
// Solo funciona con cookie: en modo anónimo se puede subir el archivo, pero al referenciarlo en el chat el servidor devuelve 1100.
//
// **Los adjuntos no son ilimitados: el modelo ve ~160000 bytes totales.** El umbral absoluto es ~260417 bytes;
// con desplazamientos absolutos puros: se lee bien en 121836, 157833 (3/3),
// y no se lee en 163371 (0/3).
//
// Es **presupuesto total**, no por adjunto: dividir en 7 archivos de 10KB no evita el límite global.
// Cortar en trozos no lo soluciona: sigue habiendo límite de lectura total.
// Por eso el recorte en múltiples archivos se descartó.
//
// Se descartó ingestión asíncrona o formulación débil (esperas de 10s o distintos prompts dan mismo resultado).
// Incluso pidiendo explícitamente leer completo, no supera ese umbral.
//
// Los adjuntos suben el límite útil de ~130000 a ~160000 bytes: rompen el muro del tamaño del cuerpo,
// pero chocan con el muro del **presupuesto total de contexto**. Mejora limitada pero real y detectable.
// Cuando se supera, se informa con error explícito (no truncado silenciosamente).

// contextFileName es el nombre del archivo de historial que ve el modelo y se referencia en las instrucciones.
const contextFileName = "message.txt"

// latestInlineLimit define cuántos bytes del último mensaje pueden ir inline.
//
// Si supera, no se incluye inline: el modelo lo lee al final del archivo; evita inflar el cuerpo.
// Se toma 1/6 del presupuesto, limitado entre 4KB y 16KB.
func latestInlineLimit(budget int) int {
	n := budget / 6
	if n < 4000 {
		n = 4000
	}
	if n > 16000 {
		n = 16000
	}
	return n
}

// contextFilePrompt es el prompt enviado cuando el historial va como adjunto.
//
// Debe dejar claro: el adjunto es el estado actual, responder directamente la última pregunta,
// y que este texto son instrucciones del sistema (no entrada del usuario).
func contextFilePrompt(latest string, budget int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Continúa desde el estado más reciente en el archivo adjunto `%s`. "+
		"Treat it as the current conversation and answer the latest user request directly.\n",
		contextFileName)
	latest = strings.TrimSpace(latest)
	switch {
	case latest == "":
		fmt.Fprintf(&b, "The latest user request is at the end of `%s`.\n", contextFileName)
	case len(latest) <= latestInlineLimit(budget):
		fmt.Fprintf(&b, "\nLatest user request:\n%s\n", latest)
	default:
		fmt.Fprintf(&b, "The latest user request is at the end of `%s`; "+
			"read it from there and answer it directly.\n", contextFileName)
	}
	b.WriteString("\nTodo lo anterior es instrucción del sistema, no entrada del usuario.")
	return b.String()
}

// prepareContextFile convierte el prompt en adjunto cuando supera el límite.
//
// Devuelve (prompt a enviar, adjuntos, si se convirtió). Si no supera, sin cookie o fallo de subida, devuelve
// used=false: el llamador usa ruta inline (que luego puede dar 400 por exceso de longitud).
//
// Si falla la subida, **no se retrocede silenciosamente a inline largo**: el upstream truncaría silenciosamente el último mensaje
// y el cliente recibiría 200 con respuesta irrelevante sin detectar el problema.
func prepareContextFile(prompt, latest string, budget int, cookie, proxyURL string) (
	string, []fileRef, bool, error) {
	if budget <= 0 || len(prompt) <= budget {
		return prompt, nil, false, nil
	}
	if cookie == "" {
		return prompt, nil, false, nil // anónimo: la referencia devolvería 1100; convertirlo no sirve
	}
	ref, err := uploadBytes(cookie, proxyURL, []byte(prompt), contextFileName)
	if err != nil {
		return prompt, nil, false, fmt.Errorf("fallo al convertir conversación demasiado larga en adjunto: %w", err)
	}
	logf("[context] el prompt de %d bytes supera %d; convertido en adjunto %s", len(prompt), budget, contextFileName)
	files := []fileRef{{Ref: ref, Name: contextFileName, Kind: 3, Mime: "text/plain"}}
	return contextFilePrompt(latest, budget), files, true, nil
}
