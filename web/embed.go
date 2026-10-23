// Package web 内嵌前端静态资源，编译产物为单一二进制，无需外部文件。
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var staticFS embed.FS

// StaticFS 返回 static 目录子文件系统（供 http.FileServer 挂载）。
func StaticFS() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // embed 编译期保证存在，不可达
	}
	return sub
}

// IndexHTML 返回页面入口文件内容。
func IndexHTML() ([]byte, error) {
	return staticFS.ReadFile("static/index.html")
}

// AdminHTML 返回管理页面内容。
func AdminHTML() ([]byte, error) {
	return staticFS.ReadFile("static/admin.html")
}
