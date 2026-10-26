package embed

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
)

// AdminHandler serves the embedded admin UI with short HTML cache headers,
// content-hash version query on CSS/JS, and data-cfasync="false" on scripts
// so Cloudflare Rocket Loader does not rewrite them.
func AdminHandler() http.Handler {
	sub, err := fs.Sub(Admin, "admin")
	if err != nil {
		panic("embed admin: " + err.Error())
	}
	return &adminStatic{fsys: sub, files: http.FileServer(http.FS(sub))}
}

type adminStatic struct {
	fsys  fs.FS
	files http.Handler
	once  sync.Once
	ver   string
	index []byte
}

func (a *adminStatic) ensure() {
	a.once.Do(func() {
		h := sha256.New()
		for _, name := range []string{"admin.css", "app.js", "alpine.min.js"} {
			b, err := fs.ReadFile(a.fsys, name)
			if err != nil {
				continue
			}
			h.Write(b)
		}
		a.ver = hex.EncodeToString(h.Sum(nil))[:12]
		raw, err := fs.ReadFile(a.fsys, "index.html")
		if err != nil {
			a.index = []byte("admin index missing")
			return
		}
		html := string(raw)
		html = strings.ReplaceAll(html, `href="admin.css"`, `href="admin.css?v=`+a.ver+`"`)
		html = strings.ReplaceAll(html, `src="app.js"`, `src="app.js?v=`+a.ver+`"`)
		html = strings.ReplaceAll(html, `src="alpine.min.js"`, `src="alpine.min.js?v=`+a.ver+`"`)
		// Ensure Rocket Loader skip even if the HTML source omitted the attribute.
		if !strings.Contains(html, `data-cfasync="false"`) {
			html = strings.ReplaceAll(html, `<script defer src="app.js?v=`+a.ver+`"`,
				`<script defer src="app.js?v=`+a.ver+`" data-cfasync="false"`)
			html = strings.ReplaceAll(html, `<script defer src="alpine.min.js?v=`+a.ver+`"`,
				`<script defer src="alpine.min.js?v=`+a.ver+`" data-cfasync="false"`)
		}
		a.index = []byte(html)
	})
}

func (a *adminStatic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.ensure()
	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if p == "." || p == "" || p == "index.html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(a.index)
		return
	}
	switch {
	case strings.HasSuffix(p, ".js"):
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		if r.URL.Query().Get("v") == a.ver {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=60")
		}
	case strings.HasSuffix(p, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		if r.URL.Query().Get("v") == a.ver {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=60")
		}
	default:
		w.Header().Set("Cache-Control", "public, max-age=60")
	}
	// Serve the file ourselves so our Cache-Control is not overwritten.
	f, err := a.fsys.Open(p)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "unsupported", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), rs)
}
