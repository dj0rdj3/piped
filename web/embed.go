// Package web embeds the built Vue frontend in the standalone server.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var assets embed.FS

func Assets() (fs.FS, error) { return fs.Sub(assets, "dist") }
