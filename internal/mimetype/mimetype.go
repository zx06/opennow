// Package mimetype maps file names to Content-Type values.
package mimetype

import (
	"mime"
	"path/filepath"
	"strings"
)

func init() {
	_ = mime.AddExtensionType(".html", "text/html; charset=utf-8")
	_ = mime.AddExtensionType(".css", "text/css; charset=utf-8")
	_ = mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	_ = mime.AddExtensionType(".svg", "image/svg+xml")
	_ = mime.AddExtensionType(".wasm", "application/wasm")
}

// ForName returns the Content-Type for a file name, defaulting to
// application/octet-stream.
func ForName(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
