package server

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed all:ui_dist
var uiDist embed.FS

func uiHTTPFS() http.FileSystem {
	sub, err := fs.Sub(uiDist, "ui_dist")
	if err != nil {
		panic(err)
	}
	return http.FS(sub)
}
