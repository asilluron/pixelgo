// Package web embeds the server-rendered templates and static assets
// shipped with pixelgo.
package web

import (
	"embed"
	"io/fs"
)

//go:embed templates static
var embedded embed.FS

// Templates returns the template filesystem rooted at web/templates.
func Templates() fs.FS {
	sub, err := fs.Sub(embedded, "templates")
	if err != nil {
		// Static layout — this cannot fail at runtime.
		panic(err)
	}
	return sub
}

// Static returns the static-asset filesystem rooted at web/static. It
// holds public assets like the OpenGraph image referenced from index.html.
func Static() fs.FS {
	sub, err := fs.Sub(embedded, "static")
	if err != nil {
		panic(err)
	}
	return sub
}
