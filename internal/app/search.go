package app

import (
	"encoding/json"
	"strings"
)

// Source es una fuente citada por una búsqueda web.
type Source struct {
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// extractGrounding saca las fuentes de la búsqueda web de la respuesta cruda de StreamGenerate.
//
// Ubicación: el inner[4][0][2][1] de la trama es un grupo de grounding chunks; el [2] de cada chunk es la lista de fuentes,
// y cada fuente tiene la forma [url, title, favicon, snippet]. Tomado literalmente de la captura, sigue la misma
// ruta de análisis wrb.fr → inner que textsInLine. Cuando no hay búsqueda web esa estructura no existe y devuelve nil.
//
// La URL suele llevar el ancla de fragmento de texto `#:~:text=` (el "desplázate al texto indicado" de la Búsqueda de Google); al mostrarla queda más limpio recortarla.
func extractGrounding(raw string) []Source {
	var out []Source
	seen := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		if !strings.Contains(line, `"wrb.fr"`) || len(line) < 200 {
			continue
		}
		var arr []interface{}
		if json.Unmarshal([]byte(line), &arr) != nil || len(arr) == 0 {
			continue
		}
		first, ok := arr[0].([]interface{})
		if !ok || len(first) < 3 {
			continue
		}
		innerStr, ok := first[2].(string)
		if !ok {
			continue
		}
		var inner []interface{}
		if json.Unmarshal([]byte(innerStr), &inner) != nil || len(inner) <= 4 {
			continue
		}
		chunks := groundingChunks(inner)
		for _, c := range chunks {
			cl, ok := c.([]interface{})
			if !ok || len(cl) <= 2 {
				continue
			}
			srcs, ok := cl[2].([]interface{})
			if !ok {
				continue
			}
			for _, s := range srcs {
				sl, ok := s.([]interface{})
				if !ok || len(sl) < 2 {
					continue
				}
				url, _ := sl[0].(string)
				url = stripTextFragment(url)
				if url == "" || seen[url] {
					continue
				}
				title, _ := sl[1].(string)
				snippet := ""
				if len(sl) > 3 {
					snippet, _ = sl[3].(string)
				}
				seen[url] = true
				out = append(out, Source{URL: url, Title: title, Snippet: snippet})
			}
		}
	}
	return out
}

// groundingChunks toma inner[4][0][2][1]; si falta cualquier nivel devuelve nil.
func groundingChunks(inner []interface{}) []interface{} {
	f4, ok := inner[4].([]interface{})
	if !ok || len(f4) == 0 {
		return nil
	}
	node, ok := f4[0].([]interface{})
	if !ok || len(node) <= 2 {
		return nil
	}
	g, ok := node[2].([]interface{})
	if !ok || len(g) <= 1 {
		return nil
	}
	chunks, ok := g[1].([]interface{})
	if !ok {
		return nil
	}
	return chunks
}

// stripTextFragment elimina el ancla de fragmento `#:~:text=...` del final de la URL.
func stripTextFragment(u string) string {
	if i := strings.Index(u, "#:~:text="); i >= 0 {
		return u[:i]
	}
	return u
}
