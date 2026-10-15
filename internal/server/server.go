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
	"github.com/zx06/opennow/internal/storage/local"
)

// Server is the HTTP front door for sites and admin.
type Server struct {
	cfg     config.Config
	store   storage.Backend
	api     *api.Handler
	adminFS http.Handler
}

func New(cfg config.Config, store storage.Backend, adminFS http.Handler) *Server {
	return &Server{
		cfg:     cfg,
		store:   store,
		api:     api.New(cfg, store),
		adminFS: adminFS,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.api.Mount(mux)

	prefix := strings.TrimRight(s.cfg.AdminPrefix, "/")
	mux.Handle(prefix+"/", http.StripPrefix(prefix, s.adminFS))
	mux.Handle(prefix, http.RedirectHandler(prefix+"/", http.StatusPermanentRedirect))

	mux.HandleFunc("/", s.serveRoot)
	return mux
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
		if errors.Is(err, local.ErrNotFound) {
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
	if meta.PasswordHash != "" && !s.siteAuthed(r, slug, mode) {
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
	rc, info, err := s.store.OpenCurrentFile(r.Context(), slug, rel)
	if err != nil {
		if errors.Is(err, local.ErrNotFound) && !strings.HasSuffix(rel, "index.html") {
			rel = path.Join(strings.TrimSuffix(rel, "/"), "index.html")
			rc, info, err = s.store.OpenCurrentFile(r.Context(), slug, rel)
		}
	}
	if err != nil {
		if errors.Is(err, local.ErrNotFound) {
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

func (s *Server) siteAuthed(r *http.Request, slug string, mode routeMode) bool {
	c, err := r.Cookie(auth.SiteAuthCookieName(slug))
	if err != nil || c.Value != "1" {
		return false
	}
	return true
}

func (s *Server) cookiePath(slug string, mode routeMode) string {
	if mode == modeSubdomain {
		return "/"
	}
	return "/" + slug + "/"
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
	if meta.PasswordHash != "" && !s.siteAuthed(r, slug, mode) {
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
<html lang="zh-CN"><head><meta charset="utf-8"><title>需要密码</title>
<style>body{font-family:system-ui,sans-serif;max-width:24rem;margin:4rem auto;padding:0 1rem}input,button{width:100%;padding:.5rem;margin:.25rem 0}</style>
</head><body>
<h1>站点已加密</h1>{{if .Error}}<p style="color:#c00">密码错误</p>{{end}}
<form method="POST"><input type="password" name="password" placeholder="访问密码" required autofocus>
<button type="submit">进入</button></form></body></html>`))

func (s *Server) servePasswordPage(w http.ResponseWriter, r *http.Request, bad bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = passwordPage.Execute(w, map[string]any{"Error": bad})
}

func (s *Server) handlePasswordForm(w http.ResponseWriter, r *http.Request, slug, passwordHash string, mode routeMode) {
	_ = r.ParseForm()
	pw := r.FormValue("password")
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(pw)) != nil {
		s.servePasswordPage(w, r, true)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SiteAuthCookieName(slug),
		Value:    "1",
		Path:     s.cookiePath(slug, mode),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 3600,
	})
	base := siteBasePath(slug, mode)
	if base == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	http.Redirect(w, r, base+"/", http.StatusFound)
}
