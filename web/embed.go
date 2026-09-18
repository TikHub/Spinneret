// Package web embeds the compiled Spinneret console (the Vite build output in
// web/dist) into the Go binary. Build the console with `pnpm build` in web/
// before `go build`; without a build only dist/.keep is embedded and the
// server serves no UI files.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Assets returns the console files rooted at dist/ (index.html at the top
// level). It never fails: if the sub tree cannot be created an empty file
// system is returned.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return emptyFS{}
	}
	return sub
}

// emptyFS is a file system without files.
type emptyFS struct{}

// Open always reports that the file does not exist.
func (emptyFS) Open(name string) (fs.File, error) {
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}
