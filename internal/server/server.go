package server

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/zx06/opennow/internal/api"
	"github.com/zx06/opennow/internal/auth"
	"github.com/zx06/opennow/internal/config"
	"github.com/zx06/opennow/internal/storage"
	"github.com/zx06/opennow/internal/storage/storeerr"
)

// Server is the HTTP front door for sites and admin.
type Server struct {
	cfg     config.Config
	store   storage.Backend
	api     *api.Handler
	signer  *auth.Signer
	adminFS http.Handler
}

func New(cfg config.Config, store storage.Backend, adminFS http.Handler) *Server {
	signer := auth.NewSigner(cfg.Secret)
	return &Server{
		cfg:     cfg,
		store:   store,
		api:     api.New(cfg, store, signer),
		signer:  signer,
		adminFS: adminFS,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.api.Mount(mux)

	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /healthz/", s.healthz)

	prefix := strings.TrimRight(s.cfg.AdminPrefix, "/")
	mux.Handle(prefix+"/", http.StripPrefix(prefix, s.adminFS))
	mux.Handle(prefix, http.RedirectHandler(prefix+"/", http.StatusPermanentRedirect))

	mux.HandleFunc("/_preview/", s.servePreview)
	mux.HandleFunc("/", s.serveRoot)
	return mux
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (s *Server) serveRoot(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AdminHost != "" && strings.EqualFold(r.Host, s.cfg.AdminHost) {
		http.Redirect(w, r, s.cfg.AdminPrefix, http.StatusFound)
		return
	}

	slug, sitePath, mode, ok := s.resolveSite(r)
	if !ok {
		if r.URL.Path == "/" || r.URL.Path == "" {
			s.serveLanding(w, r)
			return
		}
		if s.tryRefererAsset(w, r) {
			return
		}
		http.NotFound(w, r)
		return
	}

	meta, err := s.store.GetMeta(r.Context(), slug)
	if err != nil {
		if errors.Is(err, storeerr.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if meta.ExpiresAt != nil && time.Now().After(*meta.ExpiresAt) {
		http.Error(w, "站点已过期", http.StatusGone)
		return
	}
	if meta.PasswordHash != "" && !s.siteAuthed(r, slug, meta.PasswordHash) {
		if r.Method == http.MethodPost {
			s.handlePasswordForm(w, r, slug, meta.PasswordHash, mode)
			return
		}
		s.servePasswordPage(w, r, false)
		return
	}

	if sitePath == "" {
		http.Redirect(w, r, siteBasePath(slug, mode)+"/", http.StatusFound)
		return
	}
	rel := strings.Trim(sitePath, "/")
	if rel == "" {
		rel = "index.html"
	} else if strings.HasSuffix(sitePath, "/") {
		rel = path.Join(rel, "index.html")
	}
	s.serveVersionFile(w, r, slug, meta.CurrentVersion, rel)
}

// serveVersionFile streams rel from a version, falling back to rel/index.html.
func (s *Server) serveVersionFile(w http.ResponseWriter, r *http.Request, slug, versionID, rel string) {
	rc, info, err := s.store.OpenVersionFile(r.Context(), slug, versionID, rel)
	if err != nil {
		if errors.Is(err, storeerr.ErrNotFound) && !strings.HasSuffix(rel, "index.html") {
			rel = path.Join(strings.TrimSuffix(rel, "/"), "index.html")
			rc, info, err = s.store.OpenVersionFile(r.Context(), slug, versionID, rel)
		}
	}
	if err != nil {
		if errors.Is(err, storeerr.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", ContentType(info.Path))
	io.Copy(w, rc)
}

type routeMode int

const (
	modePath routeMode = iota
	modeSubdomain
)

func (s *Server) resolveSite(r *http.Request) (slug, sitePath string, mode routeMode, ok bool) {
	host := r.Host
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	if s.cfg.SubdomainMode && s.cfg.BaseDomain != "" {
		suffix := "." + s.cfg.BaseDomain
		if strings.HasSuffix(strings.ToLower(host), strings.ToLower(suffix)) {
			sub := strings.TrimSuffix(host, suffix)
			if sub != "" && sub != "www" && !strings.Contains(sub, ".") {
				return sub, r.URL.Path, modeSubdomain, true
			}
		}
	}
	if s.cfg.PathMode {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			return "", "", modePath, false
		}
		if !strings.Contains(p, "/") {
			slug = p
			if slug == "" || strings.HasPrefix(slug, "_") || slug == "api" || strings.Contains(slug, ".") {
				return "", "", modePath, false
			}
			return slug, "", modePath, true
		}
		parts := strings.SplitN(p, "/", 2)
		slug = parts[0]
		if slug == "" || strings.HasPrefix(slug, "_") || slug == "api" {
			return "", "", modePath, false
		}
		if len(parts) == 2 {
			sitePath = "/" + parts[1]
		}
		return slug, sitePath, modePath, true
	}
	return "", "", modePath, false
}

func siteBasePath(slug string, mode routeMode) string {
	if mode == modeSubdomain {
		return ""
	}
	return "/" + slug
}

func (s *Server) siteAuthed(r *http.Request, slug, passwordHash string) bool {
	return s.api.SiteAuthed(r, slug, passwordHash)
}

func (s *Server) cookiePath(slug string, mode routeMode) string {
	if mode == modeSubdomain {
		return "/"
	}
	return "/" + slug + "/"
}

// servePreview serves /_preview/<slug>/<versionId>/<path> from a specific
// version without touching the current pointer. Access requires an admin
// session or a signed preview token (?token=, then remembered in a cookie
// scoped to that version's preview path).
func (s *Server) servePreview(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/_preview/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	slug, ver := parts[0], parts[1]
	base := "/_preview/" + slug + "/" + ver + "/"
	if len(parts) == 2 {
		target := base
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	authed := s.api.IsAdmin(r)
	if tok := r.URL.Query().Get("token"); tok != "" {
		if !s.signer.VerifyPreviewToken(tok, slug, ver, time.Now()) {
			s.servePreviewDenied(w)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     auth.PreviewCookieName,
			Value:    tok,
			Path:     base,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(api.PreviewTTL / time.Second),
		})
		authed = true
	} else if c, err := r.Cookie(auth.PreviewCookieName); err == nil && s.signer.VerifyPreviewToken(c.Value, slug, ver, time.Now()) {
		authed = true
	}
	if !authed {
		s.servePreviewDenied(w)
		return
	}
	if _, err := s.store.GetMeta(r.Context(), slug); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	rel := parts[2]
	if rel == "" {
		rel = "index.html"
	} else if strings.HasSuffix(rel, "/") {
		rel = path.Join(rel, "index.html")
	}
	s.serveVersionFile(w, r, slug, ver, rel)
}

func (s *Server) servePreviewDenied(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprintf(w, `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>无权预览</title>
<style>body{font-family:system-ui,sans-serif;max-width:28rem;margin:5rem auto;padding:0 1rem;color:#334155}a{color:#4f46e5}</style></head>
<body><h1>无权预览</h1><p>预览链接无效或已过期，请登录<a href="%s">管理后台</a>后重新打开。</p></body></html>`, s.cfg.AdminPrefix)
}

func (s *Server) tryRefererAsset(w http.ResponseWriter, r *http.Request) bool {
	ref := r.Referer()
	if ref == "" || !strings.HasPrefix(r.URL.Path, "/") {
		return false
	}
	u, err := url.Parse(ref)
	if err != nil {
		return false
	}
	slug, _, mode, ok := s.resolveSite(&http.Request{URL: u, Host: r.Host})
	if !ok || mode != modePath {
		return false
	}
	meta, err := s.store.GetMeta(r.Context(), slug)
	if err != nil {
		return false
	}
	if meta.PasswordHash != "" && !s.siteAuthed(r, slug, meta.PasswordHash) {
		http.Error(w, "需要密码", http.StatusUnauthorized)
		return true
	}
	assetPath := strings.TrimPrefix(r.URL.Path, "/")
	rc, info, err := s.store.OpenCurrentFile(r.Context(), slug, assetPath)
	if err != nil {
		return false
	}
	defer rc.Close()
	w.Header().Set("Content-Type", ContentType(info.Path))
	io.Copy(w, rc)
	return true
}

func (s *Server) serveLanding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>opennow</title></head>
<body><h1>opennow</h1><p>自托管静态站点发布服务。</p><p><a href="%s">管理后台</a></p></body></html>`, s.cfg.AdminPrefix)
}

var passwordPage = template.Must(template.New("pw").Parse(`<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>需要密码</title>
<style>
:root{color-scheme:light dark;--bg:#f6f7fb;--card:#fff;--fg:#0f172a;--muted:#64748b;--line:#e2e8f0;--acc:#4f46e5}
@media (prefers-color-scheme:dark){:root{--bg:#0b1020;--card:#121a2e;--fg:#e2e8f0;--muted:#94a3b8;--line:#243049;--acc:#818cf8}}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--fg);font-family:system-ui,-apple-system,"PingFang SC","Microsoft YaHei",sans-serif}
.card{width:min(22rem,92vw);background:var(--card);border:1px solid var(--line);border-radius:16px;padding:2rem;box-shadow:0 10px 30px rgba(0,0,0,.06)}
h1{font-size:1.25rem;margin:0 0 .25rem}p{color:var(--muted);margin:0 0 1.25rem;font-size:.9rem}
input,button{width:100%;padding:.7rem .85rem;border-radius:10px;font:inherit}
input{border:1px solid var(--line);background:transparent;color:inherit;margin-bottom:.75rem}
button{border:0;background:var(--acc);color:#fff;font-weight:600;cursor:pointer}.err{color:#dc2626}
</style></head><body><main class="card">
<h1>🔒 站点已加密</h1><p>请输入访问密码以继续。</p>{{if .Error}}<p class="err">密码错误，请重试</p>{{end}}
<form method="POST"><input type="password" name="password" placeholder="访问密码" required autofocus>
<button type="submit">进入</button></form></main></body></html>`))

func (s *Server) servePasswordPage(w http.ResponseWriter, r *http.Request, bad bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_ = passwordPage.Execute(w, map[string]any{"Error": bad})
}

func (s *Server) handlePasswordForm(w http.ResponseWriter, r *http.Request, slug, passwordHash string, mode routeMode) {
	_ = r.ParseForm()
	pw := r.FormValue("password")
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(pw)) != nil {
		s.servePasswordPage(w, r, true)
		return
	}
	s.api.SetSiteCookie(w, slug, passwordHash, s.cookiePath(slug, mode))
	base := siteBasePath(slug, mode)
	if base == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	http.Redirect(w, r, base+"/", http.StatusFound)
}
