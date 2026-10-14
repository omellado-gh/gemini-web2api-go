package app

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// validateToolArgs valida los argumentos de una llamada contra el esquema de "parameters" de
// su herramienta. Es un subconjunto deliberadamente pequeño de JSON Schema (type, required,
// enum y anidamiento en object/array), suficiente para detectar los errores típicos de un
// modelo a nivel de prompt. Si el esquema no está disponible o está vacío, no se bloquea.
func validateToolArgs(schema map[string]interface{}, argsJSON string) error {
	if schema == nil {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(argsJSON))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("los argumentos no son JSON válido: %w", err)
	}
	return validateValue(schema, v, "$")
}

func validateValue(schema map[string]interface{}, v interface{}, path string) error {
	if enum, ok := schema["enum"].([]interface{}); ok && len(enum) > 0 {
		found := false
		for _, e := range enum {
			if jsonEqual(e, v) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s: el valor no está en enum", path)
		}
	}
	if t, ok := schema["type"].(string); ok && t != "" && !typeMatches(t, v) {
		return fmt.Errorf("%s: se esperaba tipo %s", path, t)
	}
	if obj, ok := v.(map[string]interface{}); ok {
		if req, ok := schema["required"].([]interface{}); ok {
			for _, r := range req {
				if rs, ok := r.(string); ok {
					if _, present := obj[rs]; !present {
						return fmt.Errorf("%s: falta el campo requerido %q", path, rs)
					}
				}
			}
		}
		if props, ok := schema["properties"].(map[string]interface{}); ok {
			for k, sub := range props {
				val, present := obj[k]
				if !present {
					continue
				}
				if subSchema, ok := sub.(map[string]interface{}); ok {
					if err := validateValue(subSchema, val, path+"."+k); err != nil {
						return err
					}
				}
			}
		}
	}
	if arr, ok := v.([]interface{}); ok {
		if items, ok := schema["items"].(map[string]interface{}); ok {
			for i, el := range arr {
				if err := validateValue(items, el, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func typeMatches(t string, v interface{}) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]interface{})
		return ok
	case "array":
		_, ok := v.([]interface{})
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := toFloat(v)
		return ok
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		_, err := n.Int64()
		return err == nil
	case "null":
		return v == nil
	}
	return true
}

func jsonEqual(a, b interface{}) bool {
	if an, aok := toFloat(a); aok {
		if bn, bok := toFloat(b); bok {
			return an == bn
		}
	}
	return reflect.DeepEqual(a, b)
}

func toFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// toolSchemas indexa los esquemas de parámetros por nombre de herramienta.
func toolSchemas(tools []map[string]interface{}) map[string]map[string]interface{} {
	m := make(map[string]map[string]interface{}, len(tools))
	for _, tool := range tools {
		fn := tool
		if t, ok := tool["type"].(string); ok && t == "function" {
			if f, ok := tool["function"].(map[string]interface{}); ok {
				fn = f
			}
		}
		name := getStr(fn, "name")
		if name == "" {
			continue
		}
		if ps, ok := fn["parameters"].(map[string]interface{}); ok {
			m[name] = ps
		}
	}
	return m
}

// validateToolCalls valida los argumentos de cada llamada contra su esquema. Devuelve la lista
// de errores (vacía si todo cuadra). No es destructivo: quien llama decide qué hacer.
func validateToolCalls(calls []ToolCall, schemas map[string]map[string]interface{}) []error {
	var errs []error
	for _, c := range calls {
		schema := schemas[c.Function.Name]
		if schema == nil {
			continue
		}
		if err := validateToolArgs(schema, c.Function.Arguments); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", c.Function.Name, err))
		}
	}
	return errs
}

// toolChoiceUnsatisfied indica si el tool_choice del cliente no se cumplió: "required" sin
// ninguna llamada, o una función concreta que no está entre las llamadas.
func toolChoiceUnsatisfied(toolChoice interface{}, calls []ToolCall) bool {
	mode, forced := parseToolChoice(toolChoice)
	if mode != "required" && forced == "" {
		return false
	}
	if forced != "" {
		for _, c := range calls {
			if c.Function.Name == forced {
				return false
			}
		}
		return true
	}
	return len(calls) == 0
}

// toolForceReminder es el texto reforzado que se añade al prompt en el reintento único cuando
// el tool_choice no se cumplió. A nivel de prompt no se puede garantizar, solo empujar.
func toolForceReminder(toolChoice interface{}) string {
	mode, forced := parseToolChoice(toolChoice)
	if forced != "" {
		return fmt.Sprintf("[System instruction — highest priority]: You MUST call the tool %q "+
			"now. Reply with exactly one ```tool_call block and nothing else, even if you think "+
			"you can answer the question directly.", forced)
	}
	if mode == "required" {
		return "[System instruction — highest priority]: You MUST call one of the available " +
			"tools now. Reply with exactly one ```tool_call block and nothing else, even if you " +
			"think you can answer the question directly."
	}
	return ""
}

// toolRetryReminder compone el recordatorio reforzado del reintento correctivo: puede incluir la
// exigencia de tool_choice y/o el aviso de argumentos inválidos. Devuelve "" si no hay nada que
// reforzar.
func toolRetryReminder(toolChoice interface{}, argErrs []error) string {
	var parts []string
	if r := toolForceReminder(toolChoice); r != "" {
		parts = append(parts, r)
	}
	if len(argErrs) > 0 {
		texts := make([]string, 0, len(argErrs))
		for _, e := range argErrs {
			texts = append(texts, e.Error())
		}
		parts = append(parts, fmt.Sprintf(
			"[System instruction]: Your previous tool call had invalid arguments (%s). "+
				"Call the tool again with arguments that are valid JSON and match the schema exactly.",
			truncate(strings.Join(texts, "; "), 200)))
	}
	return strings.Join(parts, "\n")
}

// needsToolRetry indica si el turno merece un reintento correctivo: el tool_choice no se cumplió
// o los argumentos no validan contra el esquema declarado. Devuelve también los errores de args.
func needsToolRetry(toolChoice interface{}, calls []ToolCall, schemas map[string]map[string]interface{}) (bool, []error) {
	argErrs := validateToolCalls(calls, schemas)
	return toolChoiceUnsatisfied(toolChoice, calls) || len(argErrs) > 0, argErrs
}

// normalizeToolName reduce un nombre a su forma comparable: minúsculas y solo [a-z0-9]. Así
// "read_file", "readFile" y "read-file" colapsan al mismo valor.
func normalizeToolName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// toolAliasTargets canoniza sinónimos que los modelos usan con frecuencia y que no siempre
// coinciden con el nombre declarado por el cliente. Los destinos se listan en orden de
// preferencia y solo se aplican si están entre las herramientas declaradas. Es deliberadamente
// corto: se prefiere no corregir a corregir mal.
var toolAliasTargets = map[string][]string{
	"shell": {"bash"}, "sh": {"bash"}, "shellcommand": {"bash"},
	"runcommand": {"bash"}, "execute": {"bash"}, "cmd": {"bash"},
	"command": {"bash"}, "terminal": {"bash"}, "run": {"bash"},
	"readfile": {"read"}, "cat": {"read"}, "view": {"read"},
	"writefile": {"write"}, "createfile": {"write"}, "save": {"write"},
	"editfile": {"edit"}, "strreplace": {"edit"}, "applypatch": {"edit"}, "patch": {"edit"},
	"ls": {"glob", "list"}, "find": {"glob"}, "listfiles": {"glob", "list"},
	"search": {"grep"}, "rg": {"grep"},
	"fetch": {"webfetch"}, "http": {"webfetch"},
}

// resolveToolName intenta mapear un nombre no declarado al nombre declarado más probable:
// primero por sinónimo conocido, luego por coincidencia normalizada y por último por distancia
// de edición (única y dentro de un umbral conservador). Si no hay certeza devuelve el nombre
// original, y el bloque se emite tal cual (el cliente mostrará el error de tool desconocida,
// que es más diagnosticable que degradarlo en silencio).
func resolveToolName(name string, known map[string]bool) string {
	if known == nil || len(known) == 0 || known[name] {
		return name
	}
	nn := normalizeToolName(name)
	if targets, ok := toolAliasTargets[nn]; ok {
		for _, t := range targets {
			if known[t] {
				return t
			}
		}
	}
	for k := range known {
		if normalizeToolName(k) == nn {
			return k
		}
	}
	best, bestD, tie := "", 1<<30, false
	for k := range known {
		d := editDistance(nn, normalizeToolName(k))
		switch {
		case d < bestD:
			best, bestD, tie = k, d, false
		case d == bestD:
			tie = true
		}
	}
	limit := len(nn) / 3
	if limit < 1 {
		limit = 1
	}
	if limit > 3 {
		limit = 3
	}
	if best != "" && !tie && bestD <= limit {
		return best
	}
	return name
}

// editDistance es la distancia de Levenshtein clásica (inserción/borrado/sustitución = 1),
// con memoria O(min(a,b)) y operando sobre bytes (los nombres de herramienta son ASCII).
func editDistance(a, b string) int {
	if len(a) > len(b) {
		a, b = b, a
	}
	prev := make([]int, len(a)+1)
	cur := make([]int, len(a)+1)
	for i := 0; i <= len(a); i++ {
		prev[i] = i
	}
	for j := 1; j <= len(b); j++ {
		cur[0] = j
		for i := 1; i <= len(a); i++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[i] = minInt(prev[i]+1, cur[i-1]+1, prev[i-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(a)]
}

func minInt(vs ...int) int {
	m := vs[0]
	for _, v := range vs[1:] {
		if v < m {
			m = v
		}
	}
	return m
}
