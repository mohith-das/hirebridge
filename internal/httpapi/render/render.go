package render

import (
	"embed"
	"html/template"
	"io/fs"
	"net/http"
)

//go:embed templates/*.html static/*
var assets embed.FS

var tpl *template.Template

func init() {
	tpl = template.Must(template.ParseFS(assets, "templates/*.html"))
}

func HTML(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func Static() http.Handler {
	sub, _ := fs.Sub(assets, "static")
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}
