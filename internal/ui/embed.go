// Package ui 提供嵌入式 Web 控制面板（Dashboard）的静态资源与 HTML 模板。
package ui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFS embed.FS

// Handler 返回用于提供 Web 控制台静态资源（HTML、CSS、JS、图标）的 HTTP 处理器。
func Handler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}

// GetIndexHTML 读取并返回已嵌入的 index.html 页面内容。
func GetIndexHTML() ([]byte, error) {
	return staticFS.ReadFile("static/index.html")
}
