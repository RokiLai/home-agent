package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFS embed.FS

// Handler returns an http.Handler that serves the embedded web dashboard assets.
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}

// GetIndexHTML returns the raw index.html content.
func GetIndexHTML() ([]byte, error) {
	return staticFS.ReadFile("static/index.html")
}
