package app

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed admin_ui/*
var adminUIFS embed.FS

func handleAdminUI(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(adminUIFS, "admin_ui")
	if err != nil {
		http.Error(w, "embed error", 500)
		return
	}
	// Las direcciones del panel son todas relativas, para que funcione también bajo
	// una subruta detrás de un reverse proxy (example.com/gemini/admin). Las direcciones
	// relativas se resuelven según el **directorio de la URL del documento**, así que /admin
	// y /admin/ producen resultados distintos —— el directorio del primero es el nivel superior.
	// Se redirige de forma uniforme a la forma con barra final.
	//
	// Location debe ser un valor relativo: si se escribe "/admin/", tras el reverse proxy el
	// navegador saltaría a /admin/ en la raíz del sitio y se perdería el prefijo /gemini. Aquí
	// no se usa http.Redirect —— este expande el destino relativo a una ruta absoluta según la
	// ruta de la petición, justo lo que arruinaría esto.
	path := r.URL.Path
	if path == "/admin" {
		w.Header().Set("Location", "admin/")
		w.WriteHeader(http.StatusMovedPermanently)
		return
	}
	if path == "/admin/" {
		path = "index.html"
	} else {
		path = path[len("/admin/"):]
	}
	f, err := sub.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	stat, _ := f.Stat()
	data := make([]byte, stat.Size())
	f.Read(data)
	switch {
	case len(path) > 5 && path[len(path)-5:] == ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case len(path) > 3 && path[len(path)-3:] == ".js":
		w.Header().Set("Content-Type", "application/javascript")
	case len(path) > 4 && path[len(path)-4:] == ".css":
		w.Header().Set("Content-Type", "text/css")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Write(data)
}
