// Package web embeds the built single-page app (web/dist, produced by `make web`) into the
// Dupearr binary. In a Go-only build dist contains just .gitkeep and the API serves no UI.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the SPA files as a sub-FS rooted at dist (index.html at its root once built).
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Unreachable: "dist" is a valid path and is always embedded (see //go:embed above).
		panic(err)
	}
	return sub
}
