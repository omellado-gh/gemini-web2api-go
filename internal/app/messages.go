package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// randHex returns n random hex chars. Equivalent to Python's
// `uuid.uuid4().hex[:n]` — produces a pure hex string of length n.
//
// Note: Go's `uuid.NewString()` returns the dashed 8-4-4-4-12 format,
// so naive slicing like `uuid.NewString()[:12]` would yield 11 hex + 1 dash.
// We sidestep that by drawing fresh random bytes.
func randHex(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// messagesToPrompt converts OpenAI messages[] (+ optional tools[]) to a single
// prompt string for Gemini. Tool schemas are embedded as a system instruction
// telling the model to emit ```tool_call``` blocks.
//
// toolChoice es el campo tool_choice de OpenAI ("none"/"auto"/"required" o
// {"type":"function","function":{"name":...}}). El upstream no tiene llamadas a
// herramientas a nivel de protocolo, así que la restricción solo puede escribirse en las
// instrucciones. "required" es especialmente necesario: en pruebas, Gemini responde
// directamente a preguntas que puede contestar por sí mismo (como consultar el clima) sin
// llamar a ninguna herramienta; si no se fuerza, no se obtiene el tool_call.
// Devuelve (prompt ensamblado, último mensaje del usuario).
//
// La comprobación de longitud excesiva no se hace aquí — con cookie configurada, un prompt
// demasiado largo se convierte en adjunto de texto, y eso solo se sabe tras elegir la cuenta
// y la salida, así que la decisión va en streamGenerate. El último mensaje se devuelve
// aparte para poder incrustarlo al convertirlo en adjunto, de modo que el modelo no tenga
// que ir a buscar la pregunta dentro del archivo.
func messagesToPrompt(messages []map[string]interface{}, tools []map[string]interface{},
	toolChoice interface{}, parallel bool) (string, string) {
	return buildPrompt(messages, tools, toolChoice, parallel), latestUserMessage(messages)
}

// latestUserMessage devuelve el cuerpo del último mensaje user; si no hay, cadena vacía.
func latestUserMessage(messages []map[string]interface{}) string {
	for i := len(messages) - 1; i >= 0; i-- {
		role := getStr(messages[i], "role")
		if role == "user" || role == "" {
			if c := strings.TrimSpace(contentToString(messages[i]["content"])); c != "" {
				return c
			}
		}
	}
	return ""
}

func buildPrompt(messages []map[string]interface{}, tools []map[string]interface{},
	toolChoice interface{}, parallel bool) string {
	var parts []string

	toolsInjected := false
	exampleName, exampleArgs := "run_command", "{\"command\": \"ls -la\"}"
	mode, forced := parseToolChoice(toolChoice)
	if len(tools) > 0 && mode != "none" {
		var defs []map[string]interface{}
		for _, tool := range tools {
			fn := tool
			if t, ok := tool["type"].(string); ok && t == "function" {
				if f, ok := tool["function"].(map[string]interface{}); ok {
					fn = f
				}
			}
			name := getStr(fn, "name")
			if forced != "" && name != forced {
				continue // 指定了函数名，其余不进 prompt
			}
			defs = append(defs, map[string]interface{}{
				"name":        name,
				"description": getStr(fn, "description"),
				"parameters":  fn["parameters"],
			})
		}
		if len(defs) > 0 {
			exampleName, exampleArgs = toolExample(defs)
			defsJSON := marshalToolDefs(defs, toolDefsBudget)
			rule := "Only use tool_call blocks when needed."
			switch {
			case forced != "":
				rule = fmt.Sprintf(
					"You MUST call the tool %q. Reply with the tool_call block and nothing "+
						"else — do not answer the question yourself, even if you know the answer.",
					forced)
			case mode == "required":
				rule = "You MUST call one of the tools above. Reply with the tool_call block " +
					"and nothing else — do not answer the question yourself, even if you " +
					"know the answer."
			}
			parts = append(parts, fmt.Sprintf(
				"[System instruction]: You have access to tools. "+
					"To call a tool, respond with:\n"+
					toolCallFormat+"\n"+
					"%s\n\n"+
					"Available tools:\n%s", rule, string(defsJSON),
			))
			toolsInjected = true
		}
	}

	// callNames mapea id de tool_call → nombre de la función, para poder etiquetar el
	// resultado de una herramienta (rol "tool") aunque el cliente no repita el nombre.
	callNames := toolCallNames(messages)
	for _, msg := range messages {
		role := getStr(msg, "role")
		content := contentToString(msg["content"])

		switch role {
		case "system":
			parts = append(parts, "[System instruction]: "+content)
		case "assistant":
			if tcs, ok := msg["tool_calls"].([]interface{}); ok && len(tcs) > 0 {
				var tcStrs []string
				for _, tc := range tcs {
					tcMap, ok := tc.(map[string]interface{})
					if !ok {
						continue
					}
					fn, _ := tcMap["function"].(map[string]interface{})
					name := getStr(fn, "name")
					args := getStr(fn, "arguments")
					if args == "" {
						args = "{}"
					}
					tcStrs = append(tcStrs, fmt.Sprintf(
						"```tool_call\n{\"name\": \"%s\", \"arguments\": %s}\n```",
						name, args,
					))
				}
				parts = append(parts, "[Assistant]: "+content+"\n"+strings.Join(tcStrs, "\n"))
			} else {
				parts = append(parts, "[Assistant]: "+content)
			}
		case "tool":
			name := getStr(msg, "name")
			if name == "" {
				name = callNames[getStr(msg, "tool_call_id")]
			}
			parts = append(parts, formatToolResult(name, content))
		default:
			if content != "" {
				parts = append(parts, content)
			}
		}
	}

	// La instrucción de formato de herramientas va al principio, pero los prompts de
	// desarrollador de clientes agénticos (Codex/rikkahub, etc.) rondan los 40KB y traen su
	// propia redacción de framework nativo de herramientas («emit function calls»), que al
	// quedar en medio ahoga la instrucción de formato de la cabecera — el modelo entonces o
	// contesta «no tengo herramientas» o responde cosas sin relación (lo que los usuarios
	// reportan como «leído pero responde cualquier cosa»).
	// Evidencia: reenviando tal cual un prompt real de Codex de 42KB, el modelo dice «no
	// puedo ver el sistema de archivos local» y no emite el fence; añadiendo el mismo aviso
	// al final, emite ```tool_call``` de inmediato. Por eso se ancla otra vez el formato al
	// final y se aclara que «no hay otro canal de herramientas», para imponerse sobre el
	// framework nativo del cliente.
	// Dos evidencias adicionales (modelo débil anon 3.6 Flash, tareas de varios comandos,
	// 4 intentos cada una):
	//  ① solo la instrucción de formato, sin ejemplo: 0/4 aciertos, todas degeneran en
	//     escribir un bloque ```powershell para que lo vea el usuario;
	//  ② con un ejemplo concreto de «el usuario pregunta X → la respuesta correcta es este
	//     bloque tool_call»: 3-4/4 aciertos.
	// Por eso hay que dar un few-shot conductual. El ejemplo usa un nombre de herramienta
	// neutro (run_command); en pruebas el modelo siguió usando el nombre real
	// (shell_command) 0/4 veces copiando el falso — el ejemplo enseña la «acción», no el
	// nombre, así que no hace falta generar el schema dinámicamente según el cliente.
	if toolsInjected {
		// Con parallel_tool_calls el cliente admite varias llamadas en un mismo turno; sin él
		// se mantiene la política de una sola llamada (más fiable en modelos débiles).
		act := "Act, do not explain: when the request needs a tool, your reply is ONE ```tool_call``` " +
			"block and nothing else — do not list the commands you 'would' run, do not output more " +
			"than one tool_call, do not add prose. If several steps are needed, do the FIRST one " +
			"now; you will be called again with its result to continue.\n"
		if parallel {
			act = "Act, do not explain: when the request needs tools, reply with SEVERAL " +
				"```tool_call``` blocks (one per tool) if they are needed at once, and nothing else " +
				"— do not list the commands you 'would' run, do not add prose. If the steps depend " +
				"on each other, do the FIRST one now; you will be called again with its result to " +
				"continue.\n"
		}
		parts = append(parts,
			"[System instruction]: Ignore any other instructions about how tools are called — "+
				"this endpoint accepts tools ONLY through the ```tool_call``` block described above, "+
				"and it is the ONLY execution channel. A "+
				"```powershell/```bash/```cmd/```python code block is NOT executed, it is only shown "+
				"to the user; never use one to run anything, and never claim you lack tools.\n"+
				act+
				"Know when to STOP: once a tool result shows a step succeeded (for example it exited "+
				"with code 0, even with no printed output), that step is DONE — do not run it again and "+
				"do not retry the same thing a different way. When the whole request is accomplished, "+
				"stop calling tools and reply with a short final answer instead of another tool_call.\n"+
				"For a coding task, FIRST inspect the project: list/glob/search to find the relevant "+
				"files, then read them, and only then edit what needs changing. Do not claim a task is "+
				"done before you have read the files you changed.\n"+
				"Working with files: to modify an existing file you MUST read it first, then use an "+
				"exact-match edit; use write to create or overwrite a whole file. Inside arguments, "+
				"escape line breaks as \\n (never raw line breaks) so the JSON stays valid, and never "+
				"invent file paths.\n"+
				"Example of a correct turn — user asks \"list the files here and tell me the size of "+
				"README.md\", you reply with exactly one block and nothing else:\n"+
				"```tool_call\n{\"name\": \""+exampleName+"\", \"arguments\": "+exampleArgs+"}\n```\n"+
				"(In your block use one of the real tool names listed above with its own arguments.) "+
				"When the request needs a tool, emit the tool_call block now.")
	}

	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}

// toolDefsBudget es el presupuesto en bytes para el bloque de definiciones de herramientas que
// se inyecta en el prompt. Por encima de él se compactan las descripciones, para no comerse el
// límite de bytes del cuerpo de la petición del frontend web (los clientes agénticos mandan
// ~17 tools y sus schemas pueden ser grandes).
const toolDefsBudget = 16000

// marshalToolDefs serializa las definiciones y, si superan el presupuesto, las compacta en dos
// pasos: recorta las descripciones de primer nivel y luego elimina las descripciones anidadas
// del esquema. Nunca muta los mapas originales de la petición (copia antes de tocar).
func marshalToolDefs(defs []map[string]interface{}, budget int) []byte {
	b, _ := json.MarshalIndent(defs, "", "  ")
	if len(b) <= budget {
		return b
	}
	for _, d := range defs {
		if s, ok := d["description"].(string); ok && len(s) > 200 {
			d["description"] = s[:200] + "…"
		}
	}
	b, _ = json.MarshalIndent(defs, "", "  ")
	if len(b) <= budget {
		logf("[tools] definiciones compactadas a %d bytes (descripciones recortadas)", len(b))
		return b
	}
	for _, d := range defs {
		if p, ok := d["parameters"]; ok {
			cp := deepCopyJSON(p)
			stripDescriptions(cp)
			d["parameters"] = cp
		}
	}
	b, _ = json.MarshalIndent(defs, "", "  ")
	logf("[tools] definiciones compactadas a %d bytes (sin descripciones de esquema)", len(b))
	return b
}

func deepCopyJSON(v interface{}) interface{} {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out interface{}
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}

func stripDescriptions(v interface{}) {
	switch t := v.(type) {
	case map[string]interface{}:
		delete(t, "description")
		for _, vv := range t {
			stripDescriptions(vv)
		}
	case []interface{}:
		for _, vv := range t {
			stripDescriptions(vv)
		}
	}
}

// toolExample elige un nombre de herramienta REAL para el ejemplo few-shot, para que un modelo
// débil no copie un nombre inexistente (antes se usaba "run_command" fijo, que no existe en la
// mayoría de clientes, y el modelo lo copiaba tal cual). Prefiere una herramienta de shell; si
// no la hay, usa la primera declarada con argumentos vacíos.
func toolExample(defs []map[string]interface{}) (string, string) {
	shellNames := map[string]bool{
		"bash": true, "sh": true, "shell": true, "shell_command": true,
		"run_command": true, "execute": true, "cmd": true, "powershell": true,
	}
	fallback := ""
	for _, d := range defs {
		name := getStr(d, "name")
		if name == "" {
			continue
		}
		if fallback == "" {
			fallback = name
		}
		if shellNames[strings.ToLower(name)] {
			return name, "{\"command\": \"ls -la\"}"
		}
	}
	if fallback != "" {
		return fallback, "{}"
	}
	return "run_command", "{\"command\": \"ls -la\"}"
}

func contentToString(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v
	case []interface{}:
		var bits []string
		for _, c := range v {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			t := getStr(cm, "type")
			if t == "text" || t == "input_text" {
				bits = append(bits, getStr(cm, "text"))
			}
		}
		return strings.Join(bits, " ")
	default:
		return ""
	}
}

func getStr(m map[string]interface{}, k string) string {
	if m == nil {
		return ""
	}
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

// valueToString convierte un valor de resultado de herramienta a texto sin perder datos:
// cadena tal cual, array de partes de texto unido, y cualquier otro tipo serializado a JSON.
func valueToString(v interface{}) string {
	if s := contentToString(v); s != "" {
		return s
	}
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}

// exitCodeRe extrae el código de salida de un resultado de herramienta. El resultado de
// shell de Codex es un texto envoltorio con "Process exited with code N".
var exitCodeRe = regexp.MustCompile(`(?i)exit(?:ed with|\s*code)?\s*(?:code\s*)?(\d+)`)

// formatToolResult renderiza el resultado de una herramienta dentro del prompt; la clave
// es que un modelo débil reconozca el «éxito».
//
// Por qué reescribirlo: el resultado exitoso de un cliente agéntico (Codex) se ve así——
//
//	Chunk ID: 0cdbf0\nWall time: 0.07s\nProcess exited with code 0\nOriginal token count: 0\nOutput:\n
//
// Los comandos de escribir archivo o asignar valor no tienen stdout; después de "Output:"
// va vacío. El modelo débil (anon 3.6 Flash) toma «sin salida» como «no se hizo», y entonces
// reintenta una y otra vez con otra variante——en pruebas, «escribir hello en a.txt» intentó
// 26 comandos distintos en una tarea y no convergió en 90s, cuando el archivo en realidad se
// escribió bien a la primera. El exit 0 está a la vista en el resultado, pero queda enterrado
// por ruido como Chunk ID / Wall time / token count, y no se le dice que «sin salida es
// normal». Aquí se comprime a una línea limpia de señal de éxito/fallo, y se explica
// explícitamente que sin salida es normal y que no hay que volver a ejecutar.
//
// Añadir solo la instrucción de parada (esa frase "Know when to STOP" del aviso) no sirvió
// en pruebas (26→27 comandos); solo junto con esta señal limpia se contiene el bucle.
func formatToolResult(name, raw string) string {
	label := "Tool result"
	if name != "" {
		label = "Tool result for " + name
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Sprintf("[%s]: ✅ Done, no output (this is normal; the action took effect — do NOT run it again).", label)
	}
	// Envoltorio estilo Codex: tras el "Output:" final va el stdout real.
	out := trimmed
	if i := strings.LastIndex(trimmed, "Output:"); i >= 0 {
		out = strings.TrimSpace(trimmed[i+len("Output:"):])
	}
	if m := exitCodeRe.FindStringSubmatch(trimmed); m != nil {
		if m[1] == "0" {
			if out == "" {
				return fmt.Sprintf("[%s]: ✅ Command succeeded (exit 0) with no text output — this is normal for file-write/set-value commands; the action is complete, do NOT run the same command again.", label)
			}
			return fmt.Sprintf("[%s]: ✅ Command succeeded (exit 0). Output:\n%s", label, out)
		}
		return fmt.Sprintf("[%s]: ❌ Command failed (exit %s). Output:\n%s", label, m[1], out)
	}
	return fmt.Sprintf("[%s]: %s", label, raw)
}

const (
	toolFenceOpen  = "```tool_call"
	toolFenceClose = "```"
)

// toolCallFormat es la forma canónica del bloque que el modelo debe emitir. Fuente única para
// el encabezado del prompt y el ejemplo few-shot, de modo que el formato del canal no pueda
// divergir entre ambos. El ejemplo few-shot del pie sustituye «func_name» por una herramienta
// real declarada, así que no reusa esta constante.
const toolCallFormat = "```tool_call\n{\"name\": \"func_name\", \"arguments\": {...}}\n```"

// toolTurnReminder es el recordatorio compacto del canal de herramientas que se re-ancla en
// cada turno de continuación. Las definiciones completas siguen en el historial del servidor,
// pero el modelo tiende a olvidar el formato si no lo vuelve a ver.
const toolTurnReminder = "[System instruction]: To run a command or read a file, reply with " +
	"ONE ```tool_call block and nothing else — it is the ONLY execution channel. A " +
	"```bash/```sh/```powershell code block is NOT executed, it is only shown. To modify an " +
	"existing file, read it first, then use an exact-match edit; use write to create/overwrite. " +
	"Inside arguments escape line breaks as \\n (never raw line breaks). After a tool " +
	"result: if the task is done, answer with a short final message instead of another tool_call."

// toolFenceGate retiene el bloque ```tool_call``` en un flujo incremental, emitiéndolo como
// tool_call en cuanto se cierra y dejando pasar solo el texto que con certeza no pertenece al
// fence.
//
// NOTA: desde el bufferizado de los turnos con herramientas (ver server.go), esta compuerta ya
// no está en el camino de producción — las peticiones con tools se reciben completas, se
// validan/corrigen y se emiten al final. Se conserva (y se prueba) como implementación de
// referencia del parseo incremental, por si en el futuro se retoma el streaming real con tools.
//
// Problema que resolvía: el upstream no tiene tool calling a nivel de protocolo; el modelo emite
// el fence ```tool_call```, y el fence necesita el texto completo para parsearse. Reenviarlo a
// medida que sale empujaría el texto crudo al cliente (vería un bloque markdown, no tool_calls).
// La solución era enviar solo la parte que **con certeza** no pertenece al fence, reteniendo
// también un posible prefijo de apertura a medias (p. ej. dos backticks) hasta la trama
// siguiente. Sent() es el texto realmente enviado al cliente, distinto del emitted del deltaTracker
// (que incluye el fence crudo).
type toolFenceGate struct {
	emit      func(string)
	buf       string // cola aún sin decidir
	sent      strings.Builder
	known     map[string]bool              // nombres declarados; nil = no validar
	onTool    func(index int, tc ToolCall) // se llama al cerrarse un fence válido
	toolCount int
}

func newToolFenceGate(emit func(string)) *toolFenceGate {
	return &toolFenceGate{emit: emit}
}

// Sent devuelve todo el texto realmente enviado al cliente hasta ahora.
func (g *toolFenceGate) Sent() string {
	if g == nil {
		return ""
	}
	return g.sent.String()
}

// ToolCallCount devuelve cuántas tool_calls se han emitido ya por la compuerta.
func (g *toolFenceGate) ToolCallCount() int {
	if g == nil {
		return 0
	}
	return g.toolCount
}

func (g *toolFenceGate) send(s string) {
	if s == "" {
		return
	}
	g.sent.WriteString(s)
	g.emit(s)
}

// Push consume un fragmento incremental y envía de inmediato la parte que seguro no está
// en el fence; al cerrarse un fence, lo parsea y emite la tool_call (o el texto crudo si no
// se puede interpretar, para no perder contenido).
func (g *toolFenceGate) Push(delta string) {
	g.buf += delta
	for {
		i := strings.Index(g.buf, toolFenceOpen)
		if i < 0 {
			// No hay fence completo; retener un posible prefijo de apertura en la cola.
			keep := partialPrefixLen(g.buf, toolFenceOpen)
			g.send(g.buf[:len(g.buf)-keep])
			g.buf = g.buf[len(g.buf)-keep:]
			return
		}
		rest := g.buf[i:]
		body := rest[len(toolFenceOpen):]
		j := strings.Index(body, toolFenceClose)
		if j < 0 {
			// El fence aún no se cierra; enviar lo de antes y retener el fence entero.
			g.send(g.buf[:i])
			g.buf = rest
			return
		}
		raw := rest[:len(toolFenceOpen)+j+len(toolFenceClose)]
		content := body[:j]
		g.send(g.buf[:i])

		tc, ok := decodeToolCall(content)
		if ok && g.known != nil && !g.known[tc.Function.Name] {
			if corrected := resolveToolName(tc.Function.Name, g.known); corrected != tc.Function.Name {
				logf("[tools] nombre no declarado %q; se corrige a %q", tc.Function.Name, corrected)
				tc.Function.Name = corrected
			} else {
				// Se emite igualmente (el cliente mostrará el error de tool desconocido).
				logf("[tools] el modelo llamó a una herramienta no declarada %q; se emite igualmente", tc.Function.Name)
			}
		}
		if ok {
			if g.onTool != nil {
				g.onTool(g.toolCount, tc)
			}
			g.toolCount++
		} else {
			logf("[tools] bloque tool_call no interpretable; se envía como texto: %q", truncate(strings.TrimSpace(content), 120))
			g.send(raw)
		}
		g.buf = rest[len(raw):]
	}
}

// partialPrefixLen devuelve cuántos bytes del final de s son prefijo de marker (sin
// contar una coincidencia completa).
//
// marker es todo ASCII, así que el sufijo que coincide también es todo ASCII y el punto de
// corte no cae en medio de un carácter multibyte —— los bytes de continuación de UTF-8 son
// >=0x80 y nunca igualan ningún byte de marker.
func partialPrefixLen(s, marker string) int {
	max := len(marker) - 1
	if len(s) < max {
		max = len(s)
	}
	for k := max; k > 0; k-- {
		if strings.HasPrefix(marker, s[len(s)-k:]) {
			return k
		}
	}
	return 0
}

// Más permisivo: dentro del fence no se exige salto de línea antes/después——el modelo a
// veces emite ```tool_call\n{...}\n```, a veces ```tool_call {...}``` o incluso en una sola
// línea. La regex antigua insistía en \n…\n y al cambiar el formato dejaba de parsear; al
// fallar, enviaba el bloque ```tool_call``` entero como texto al cliente = el «leído pero
// responde cualquier cosa» que ve el usuario. Aquí solo se reconoce el fence y el contenido
// se delega al parseo JSON como respaldo (el contenido fuera del fence no lleva ```, y la
// no-greedy se detiene en el primer fence de cierre).
var toolCallRe = regexp.MustCompile("(?s)```tool_call(.*?)```")

// Claves aceptadas dentro del bloque JSON: los modelos más débiles no siempre respetan el
// formato {"name","arguments"} — a veces emiten el nombre bajo otra clave, o los argumentos
// como cadena en lugar de objeto.
var (
	toolNameKeys = []string{"name", "tool", "tool_name", "toolName", "recipient", "function"}
	toolArgsKeys = []string{"arguments", "parameters", "args", "params", "input"}
)

// parseToolCalls extracts ```tool_call``` blocks. Returns clean text + tool_calls.
//
// Robustez: un bloque que no se puede interpretar (JSON inválido, sin nombre, o nombre no
// declarado cuando se pasa `known`) NO se descarta en silencio. El texto que rodea al fence
// ya se emitió en streaming, así que borrar el bloque dejaría al usuario con una respuesta
// vacía — justo el «leo la respuesta pero no hace nada» que se reportaba. En su lugar se
// conserva el bloque tal cual en el texto devuelto y se registra en el log.
//
// `known` es opcional: si se pasa, una llamada cuyo nombre no figure en el conjunto se trata
// como bloque no interpretable (se conserva como texto). Sin argumentos no se valida.
func parseToolCalls(text string, known ...map[string]bool) (string, []ToolCall) {
	var knownSet map[string]bool
	if len(known) > 0 {
		knownSet = known[0]
	}

	var toolCalls []ToolCall
	var clean strings.Builder
	last := 0
	keptInvalid := false
	for _, loc := range toolCallRe.FindAllStringSubmatchIndex(text, -1) {
		if len(loc) < 4 {
			continue
		}
		start, end := loc[0], loc[1]
		inner := text[loc[2]:loc[3]]
		clean.WriteString(text[last:start])
		last = end

		tc, ok := decodeToolCall(inner)
		if ok && knownSet != nil && !knownSet[tc.Function.Name] {
			if corrected := resolveToolName(tc.Function.Name, knownSet); corrected != tc.Function.Name {
				logf("[tools] nombre no declarado %q; se corrige a %q", tc.Function.Name, corrected)
				tc.Function.Name = corrected
			} else {
				// Se emite igualmente (el cliente mostrará el error de tool desconocido), que es más
				// diagnosticable que degradarlo a texto en silencio.
				logf("[tools] el modelo llamó a una herramienta no declarada %q; se emite igualmente", tc.Function.Name)
			}
		}
		if ok {
			toolCalls = append(toolCalls, tc)
			continue
		}
		// No interpretable: se conserva el bloque original para no perder contenido.
		clean.WriteString(text[start:end])
		keptInvalid = true
		logf("[tools] bloque tool_call no interpretable, se conserva como texto: %q", truncate(strings.TrimSpace(inner), 120))
	}
	clean.WriteString(text[last:])
	result := strings.TrimSpace(clean.String())
	// Una apertura ```tool_call sin cierre no la captura la regex: suele indicar respuesta
	// truncada (el modelo se cortó a mitad del bloque, típico al escribir un archivo grande).
	if !keptInvalid && strings.Contains(result, toolFenceOpen) {
		logf("[tools] bloque tool_call sin cerrar en la respuesta (probable truncado); no se emitió como tool_call")
	}
	return result, toolCalls
}

// hasUnclosedToolFence indica que el texto ya limpio todavía contiene una apertura ```tool_call:
// parseToolCalls no la elimina porque le falta el cierre, señal de que la salida se cortó a
// mitad del bloque (típico al escribir un archivo grande). En ese caso el cliente debe recibir
// finish_reason "length", no "stop", para saber que la respuesta quedó truncada y puede
// continuar, en vez de creer que no había ninguna llamada que hacer.
func hasUnclosedToolFence(cleaned string) bool {
	return strings.Contains(cleaned, toolFenceOpen)
}

// toolCallNames indexa id de tool_call → nombre de la función a partir de los mensajes del
// assistant, para poder etiquetar un resultado de herramienta (rol "tool") aunque el cliente no
// repita "name" y solo mande el tool_call_id.
func toolCallNames(messages []map[string]interface{}) map[string]string {
	names := map[string]string{}
	for _, msg := range messages {
		if getStr(msg, "role") != "assistant" {
			continue
		}
		tcs, ok := msg["tool_calls"].([]interface{})
		if !ok {
			continue
		}
		for _, tc := range tcs {
			tcm, ok := tc.(map[string]interface{})
			if !ok {
				continue
			}
			fn, _ := tcm["function"].(map[string]interface{})
			id, name := getStr(tcm, "id"), getStr(fn, "name")
			if id != "" && name != "" {
				names[id] = name
			}
		}
	}
	return names
}

// toolNameSet construye el conjunto de nombres declarados en tools[] para validar las
// llamadas que devuelve el modelo.
func toolNameSet(tools []map[string]interface{}) map[string]bool {
	set := make(map[string]bool, len(tools))
	for _, tool := range tools {
		fn := tool
		if t, ok := tool["type"].(string); ok && t == "function" {
			if f, ok := tool["function"].(map[string]interface{}); ok {
				fn = f
			}
		}
		if n := getStr(fn, "name"); n != "" {
			set[n] = true
		}
	}
	return set
}

// decodeToolCall interpreta el contenido de un fence como llamada a herramienta. Devuelve
// ok=false si no hay un objeto JSON con un nombre utilizable.
func decodeToolCall(raw string) (ToolCall, bool) {
	obj := looseJSONObject(raw)
	if obj == nil {
		return ToolCall{}, false
	}
	name := firstString(obj, toolNameKeys)
	if name == "" {
		return ToolCall{}, false
	}
	return ToolCall{
		ID:   "call_" + randHex(8),
		Type: "function",
		Function: ToolCallFunction{
			Name:      name,
			Arguments: encodeArguments(firstValue(obj, toolArgsKeys)),
		},
	}, true
}

func firstString(obj map[string]interface{}, keys []string) string {
	for _, k := range keys {
		if s, ok := obj[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstValue(obj map[string]interface{}, keys []string) interface{} {
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			return v
		}
	}
	return nil
}

// encodeArguments normaliza el argumento de la llamada a una cadena JSON. Los modelos a
// veces lo emiten como objeto, a veces como una cadena que ya es JSON, y a veces como texto
// suelto; se cubren los tres casos.
func encodeArguments(v interface{}) string {
	switch a := v.(type) {
	case nil:
		return "{}"
	case string:
		t := strings.TrimSpace(a)
		if t == "" {
			return "{}"
		}
		if json.Valid([]byte(t)) {
			return t
		}
		b, _ := json.Marshal(map[string]interface{}{"input": a})
		return string(b)
	default:
		b, err := json.Marshal(a)
		if err != nil {
			return "{}"
		}
		return string(b)
	}
}

var trailingCommaRe = regexp.MustCompile(`,(\s*[}\]])`)

// looseJSONObject extrae el objeto JSON que haya dentro del fence, tolerando las muchas
// formas en que un modelo a nivel de prompt lo emite mal: prosa alrededor, una cadena JSON
// que envuelve al objeto, comas finales sobrantes, **saltos de línea o tabuladores crudos
// dentro de los strings** (lo habitual al escribir un archivo), y comillas internas sin
// escapar. Cada capa se intenta en orden; la última es una extracción campo a campo.
func looseJSONObject(raw string) map[string]interface{} {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	// A veces el bloque entero llega como cadena JSON: "{\"name\": ...}".
	if strings.HasPrefix(s, "\"") {
		var unq string
		if json.Unmarshal([]byte(s), &unq) == nil {
			s = strings.TrimSpace(unq)
		}
	}
	candidates := []string{s}
	if obj := outermostObject(s); obj != "" {
		candidates = append(candidates, obj)
	}
	for _, c := range candidates {
		if m := unmarshalObject(c); m != nil {
			return m
		}
		fixed := trailingCommaRe.ReplaceAllString(c, "$1")
		if m := unmarshalObject(fixed); m != nil {
			return m
		}
		// Escapar caracteres de control crudos dentro de los strings (newlines \n crudos...).
		if m := unmarshalObject(escapeRawControls(fixed)); m != nil {
			return m
		}
	}
	// Último recurso: extracción campo a campo (tolera comillas internas sin escapar).
	if m := extractFields(s); m != nil {
		return m
	}
	return nil
}

func unmarshalObject(s string) map[string]interface{} {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '{' {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	return m
}

// outermostObject devuelve la subcadena desde el primer '{' hasta el último '}'.
func outermostObject(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j <= i {
		return ""
	}
	return s[i : j+1]
}

// escapeRawControls convierte newline/tab/otros caracteres de control **crudos** que estén
// dentro de un string JSON a su forma escapada válida. Fuera de los strings no toca nada;
// los escapes ya presentes (\n, \", \\) se respetan.
func escapeRawControls(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !inStr {
			b.WriteByte(c)
			if c == '"' {
				inStr = true
			}
			continue
		}
		switch c {
		case '\\':
			b.WriteByte(c)
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		case '"':
			b.WriteByte(c)
			inStr = false
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

// extractFields es el último recurso: parsea el objeto campo a campo, leyendo cada valor de
// texto de forma tolerante (una comilla interna sin escapar cuenta como contenido si no va
// seguida de ',' o '}'). Está pensado para los argumentos de escritura de archivos, donde el
// contenido casi siempre trae comillas y saltos de línea sin escapar.
func extractFields(s string) map[string]interface{} {
	if i := strings.IndexByte(s, '{'); i >= 0 {
		s = s[i+1:]
	} else {
		return nil
	}
	out := map[string]interface{}{}
	j := 0
	for {
		j = skipJSONSpace(s, j)
		if j >= len(s) || s[j] == '}' {
			break
		}
		if s[j] == ',' {
			j++
			continue
		}
		if s[j] != '"' {
			return nil
		}
		key, nj, ok := readStringToken(s, j, true)
		if !ok {
			return nil
		}
		j = skipJSONSpace(s, nj)
		if j >= len(s) || s[j] != ':' {
			return nil
		}
		j = skipJSONSpace(s, j+1)
		if j >= len(s) {
			return nil
		}
		switch s[j] {
		case '"':
			val, nj, ok := readStringToken(s, j, false)
			if !ok {
				return nil
			}
			out[key] = val
			j = nj
		case '{', '[':
			sub, nj := readBalanced(s, j)
			var v interface{}
			if json.Unmarshal([]byte(sub), &v) == nil {
				out[key] = v
			} else if s[j] == '{' {
				// Objeto anidado (p. ej. "arguments") con el mismo formato suelto: recursar.
				if m := looseJSONObject(sub); m != nil {
					out[key] = m
				} else {
					out[key] = sub
				}
			} else {
				out[key] = sub
			}
			j = nj
		default:
			k := j
			for k < len(s) && s[k] != ',' && s[k] != '}' {
				k++
			}
			tok := strings.TrimSpace(s[j:k])
			var v interface{}
			if json.Unmarshal([]byte(tok), &v) == nil {
				out[key] = v
			} else if tok != "" {
				out[key] = tok
			}
			j = k
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func skipJSONSpace(s string, i int) int {
	for i < len(s) {
		switch s[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// readStringToken lee un string JSON a partir de s[i]=='"'. Con isKey=true termina en la
// primera comilla sin escapar (las claves no contienen comillas); con isKey=false acepta
// comillas internas sin escapar cuando no van seguidas de ',' o '}' (o ']'), y acepta un
// literal sin cerrar (respuesta truncada). Devuelve (valor, índice siguiente, ok).
func readStringToken(s string, i int, isKey bool) (string, int, bool) {
	if i >= len(s) || s[i] != '"' {
		return "", i, false
	}
	var b strings.Builder
	j := i + 1
	for j < len(s) {
		c := s[j]
		if c == '\\' && j+1 < len(s) {
			switch s[j+1] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '/':
				b.WriteByte('/')
			case 'u':
				if j+5 < len(s) {
					if v, err := strconv.ParseUint(s[j+2:j+6], 16, 32); err == nil {
						b.WriteRune(rune(v))
						j += 6
						continue
					}
				}
				b.WriteByte('u')
			default:
				b.WriteByte(s[j+1])
			}
			j += 2
			continue
		}
		if c == '"' {
			if isKey {
				return b.String(), j + 1, true
			}
			k := skipJSONSpace(s, j+1)
			if k >= len(s) || s[k] == ',' || s[k] == '}' || s[k] == ']' {
				return b.String(), j + 1, true
			}
			// comilla interna sin escapar: forma parte del contenido
			b.WriteByte('"')
			j++
			continue
		}
		b.WriteByte(c)
		j++
	}
	// sin cierre (respuesta cortada): se acepta lo leído
	return b.String(), len(s), true
}

// readBalanced devuelve la subcadena balanceada desde s[i] (que debe ser '{' o '[') y el
// índice siguiente, respetando strings y escapes.
func readBalanced(s string, i int) (string, int) {
	open := s[i]
	closeCh := byte('}')
	if open == '[' {
		closeCh = ']'
	}
	depth := 0
	inStr := false
	for j := i; j < len(s); j++ {
		c := s[j]
		if inStr {
			if c == '\\' {
				j++
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case closeCh:
			depth--
			if depth == 0 {
				return s[i : j+1], j + 1
			}
		}
	}
	return s[i:], len(s)
}

// parseToolChoice analiza el tool_choice de OpenAI.
// Devuelve (mode, forcedName): mode ∈ {"auto","none","required"};
// forcedName no vacío indica que el cliente nombró una función concreta.
func parseToolChoice(tc interface{}) (string, string) {
	switch v := tc.(type) {
	case string:
		switch v {
		case "none", "required", "auto":
			return v, ""
		}
	case map[string]interface{}:
		// {"type":"function","function":{"name":"..."}}
		if f, ok := v["function"].(map[string]interface{}); ok {
			if n := getStr(f, "name"); n != "" {
				return "required", n
			}
		}
	}
	return "auto", ""
}

// parallelToolCalls indica si el cliente admite varias llamadas a herramientas en un mismo turno.
func parallelToolCalls(req map[string]interface{}) bool {
	b, _ := req["parallel_tool_calls"].(bool)
	return b
}

// firstToolTurn indica que la conversación aún no ha iniciado el bucle de herramientas: no hay
// ninguna llamada previa del assistant ni resultados de tool.
func firstToolTurn(messages []map[string]interface{}) bool {
	for _, m := range messages {
		switch getStr(m, "role") {
		case "tool":
			return false
		case "assistant":
			if tcs, ok := m["tool_calls"].([]interface{}); ok && len(tcs) > 0 {
				return false
			}
		}
	}
	return true
}

// effectiveToolChoice aplica el arranque forzado del bucle de herramientas (opt-in,
// force_tool_first_turn): en el primer turno de una sesión con tools y con tool_choice
// automático o ausente, lo eleva a "required" para que el modelo empiece a actuar en vez de
// responder en prosa con un modelo débil.
func effectiveToolChoice(req map[string]interface{}, tools []map[string]interface{}, messages []map[string]interface{}) interface{} {
	tc := req["tool_choice"]
	if !rtCfg().ForceToolFirstTurn || len(tools) == 0 {
		return tc
	}
	mode, forced := parseToolChoice(tc)
	if mode == "none" || mode == "required" || forced != "" {
		return tc
	}
	if firstToolTurn(messages) {
		return "required"
	}
	return tc
}

// PromptTooLongError indica que el prompt supera el límite que cabe en una sola petición.
//
// Por qué es un error y no lo truncamos nosotros: al superar el límite, el upstream
// **trunca en silencio desde el final** y no da error; como el último mensaje va al final,
// lo que se come es justo lo que el usuario acaba de preguntar —— el modelo solo ve el
// preámbulo del sistema y da una introducción genérica, sin responder ni llamar
// herramientas.
//
// Tampoco descartamos el historial por nuestra cuenta: eso sigue siendo pérdida silenciosa
// de datos, solo que en otro sitio. El cliente cree que envió todo, pero el modelo olvidó
// cosas y la respuesta falla de forma sutil sin que nadie lo sepa. Devolver
// context_length_exceeded es una señal que los clientes compatibles con OpenAI reconocen;
// un cliente agéntico que la reciba comprimirá su propio contexto y reintentará —— es mucho
// más listo que descartar a ciegas nuestros segmentos más antiguos.
//
// **Por qué se mide en bytes y no en tokens**: alineando chino e inglés al mismo número de
// bytes en una IP estática, en pruebas ambos muros quedaron exactamente en el mismo punto
// —— unos 129.950 bytes con 3/3 aprobados cada uno, 135.990 bytes con 1/3, 141.920 bytes con
// 1/3; mientras que el conteo tiktoken de la misma tanda difería 1,9 veces (24.273 en inglés
// frente a 46.591 en chino). Si el umbral se fijara en tokens, el mismo número sería
// demasiado holgado para inglés y dejaría al chino atascado en torno a un tercio de la
// capacidad real.
//
// De paso se descartó que «el muro esté en la capa de transporte»: el prompt entra en f.req
// tras pasar por JSON y urlencode; cada byte chino se vuelve %XX (expansión x3) y el inglés
// queda casi igual; ambos difieren casi 3 veces en volumen en línea y aun así chocan con el
// mismo muro, así que lo que se cuenta son los bytes del contenido del prompt, no el tamaño
// del cuerpo de la petición.
//
// Para sostener de verdad contextos largos hay que ir por otra vía: convertir el contenido
// en adjunto de archivo. Pero eso requiere sesión iniciada (el anónimo puede subir, pero al
// referenciarlo en la conversación el servidor responde 1100 y lo rechaza), así que por
// ahora solo se puede devolver error.
type PromptTooLongError struct {
	Bytes, Budget int
	HasCookie     bool // si hay cookie y aun así se supera, es que la vía del adjunto tampoco lo salvó
}

func (e *PromptTooLongError) Error() string {
	base := fmt.Sprintf(
		"prompt is %d bytes, over the %d-byte per-request limit of the Gemini web "+
			"protocol (the limit is on UTF-8 bytes, not tokens). The upstream silently "+
			"truncates from the end, which would drop your latest message and produce an "+
			"unrelated answer, so this request is rejected instead.",
		e.Bytes, e.Budget)
	if !e.HasCookie {
		// 没 cookie 时这不是死路：导一个进来就能走附件，长度限制基本就没了。
		// 不说这句的话用户只会以为"这项目撑不住长上下文"。
		return base + " Add a Google account cookie in the admin panel (Cookie pool) — " +
			"with one configured, oversized conversations are uploaded as a text " +
			"attachment instead and this limit largely goes away."
	}
	return base + " Shorten the conversation, the system prompt, or the tool definitions."
}
