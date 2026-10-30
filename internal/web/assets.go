// Package web embeds the locally built Svelte application. Run make build (or
// npm --prefix web run build) before compiling the Go service.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var files embed.FS

func Assets() (fs.FS, error) { return fs.Sub(files, "dist") }
