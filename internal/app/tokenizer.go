package app

import (
	"fmt"
	"os"
	"sync"

	"github.com/pkoukk/tiktoken-go"
)

// tiktoken cl100k_base (GPT-4 BPE). El tokenizer real de Gemini no está publicado;
// las reglas BPE son similares y para texto mixto chino/inglés es mucho más preciso que chars/4 (desviación ~±20% en pruebas).
//
// Se carga una vez en memoria (~5MB); Encode toma ~microsegundos.
var (
	tokenizer     *tiktoken.Tiktoken
	tokenizerOnce sync.Once
	tokenizerOK   bool
)

func initTokenizer() {
	tokenizerOnce.Do(func() {
		// La primera carga de tiktoken-go descarga cl100k_base.tiktoken (~3MB) desde remoto;
		// se guarda en caché en ${TIKTOKEN_CACHE_DIR} o ${HOME}/.cache/tiktoken.
		// En imagen Docker se recomienda precachearlo durante docker build.
		t, err := tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			fmt.Fprintf(os.Stderr, "[tokenizer] init failed (will fall back to chars/4): %v\n", err)
			return
		}
		tokenizer = t
		tokenizerOK = true
	})
}

// countTokens devuelve una estimación aproximada de tokens.
// Si falla la carga del tokenizer, se usa estimación chars/4.
func countTokens(s string) int {
	if !tokenizerOK || tokenizer == nil {
		return len(s) / 4
	}
	return len(tokenizer.Encode(s, nil, nil))
}
