// Package web embeds the Vite-built dashboard SPA at compile time so the Go
// binary is a single-file deploy. The Vue/AntDv source lives in ../../web/ and
// builds into ./dist via `pnpm --dir go/web build`.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded dist/ directory rooted so paths are relative to
// the SPA output (e.g. "index.html", "assets/index-xxxx.js").
func DistFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Unreachable: `//go:embed all:dist` guarantees the subtree exists.
		panic(err)
	}
	return sub
}

// IndexHTML returns the SPA shell; used for SPA-style fallbacks so client
// routes like /dashboard/accounts still hit Vue Router on first load.
func IndexHTML() []byte {
	b, err := fs.ReadFile(distFS, "dist/index.html")
	if err != nil {
		return []byte("<!doctype html><title>WindsurfAPI</title><h1>dashboard build missing</h1><p>run: <code>pnpm --dir go/web install &amp;&amp; pnpm --dir go/web build</code></p>")
	}
	return b
}
