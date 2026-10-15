package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/zx06/opennow/internal/auth"
	"github.com/zx06/opennow/internal/config"
	"github.com/zx06/opennow/internal/site"
	"github.com/zx06/opennow/internal/storage"
	"github.com/zx06/opennow/internal/storage/local"
)

// Handler serves the public admin JSON API.
type Handler struct {
	cfg      config.Config
	store    storage.Backend
	sessions *sessionStore
	signer   *auth.Signer
}

// PreviewTTL is how long a signed preview link stays valid.
const PreviewTTL = 24 * time.Hour

// SiteCookieTTL is how long a site unlock cookie stays valid.
const SiteCookieTTL = 7 * 24 * time.Hour

func New(cfg config.Config, store storage.Backend, signer *auth.Signer) *Handler {
	if signer == nil {
		signer = auth.NewSigner(cfg.Secret)
	}
	return &Handler{cfg: cfg, store: store, sessions: newSessionStore(), signer: signer}
}

// IsAdmin reports whether the request carries a valid admin session.
func (h *Handler) IsAdmin(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && h.sessions.valid(c.Value)
}

// MetaView is the public JSON shape of site metadata (never exposes the password hash).
type MetaView struct {
	Slug           string     `json:"slug"`
	CurrentVersion string     `json:"current_version"`
	HasPassword    bool       `json:"has_password"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func viewMeta(m *site.Meta) MetaView {
	return MetaView{
		Slug:           m.Slug,
		CurrentVersion: m.CurrentVersion,
		HasPassword:    m.PasswordHash != "",
		ExpiresAt:      m.ExpiresAt,
		CreatedAt:      m.CreatedAt,
		UpdatedAt:      m.UpdatedAt,
	}
}

// VersionView enriches a version with stats and preview link for the admin UI.
type VersionView struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	Current    bool      `json:"current"`
	FileCount  int       `json:"file_count"`
	TotalBytes int64     `json:"total_bytes"`
	PreviewURL string    `json:"preview_url"`
}

// PreviewPath is the server-relative preview URL of a version (admin session required).
func PreviewPath(slug, versionID string) string {
	return "/_preview/" + url.PathEscape(slug) + "/" + url.PathEscape(versionID) + "/"
}

func (h *Handler) signedPreviewURL(slug, versionID string) string {
	tok := h.signer.PreviewToken(slug, versionID, time.Now().Add(PreviewTTL))
	return h.cfg.PublicURL + PreviewPath(slug, versionID) + "?token=" + url.QueryEscape(tok)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.HandleFunc("GET /api/v1/auth/me", h.me)

	mux.HandleFunc("GET /api/v1/storage/overview", h.storageOverview)
	mux.HandleFunc("GET /api/v1/sites", h.listSites)
	mux.HandleFunc("GET /api/v1/sites/{slug}", h.getSite)
	mux.HandleFunc("DELETE /api/v1/sites/{slug}", h.deleteSite)
	mux.HandleFunc("PATCH /api/v1/sites/{slug}", h.patchSite)
	mux.HandleFunc("GET /api/v1/sites/{slug}/versions", h.listVersions)
	mux.HandleFunc("GET /api/v1/sites/{slug}/versions/{versionId}/files", h.listVersionFiles)
	mux.HandleFunc("POST /api/v1/sites/{slug}/rollback", h.rollback)
	mux.HandleFunc("POST /api/v1/sites/{slug}/unlock", h.unlockSite)

	mux.HandleFunc("POST /api/v1/publish", h.createPublish)
	mux.HandleFunc("PUT /api/v1/publish/{revisionId}/files/{filepath...}", h.uploadFile)
	mux.HandleFunc("POST /api/v1/publish/{revisionId}/finalize", h.finalizePublish)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if h.cfg.APIKey == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "未配置 OPENNOW_API_KEY"})
		return
	}
	var body struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.APIKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需要 api_key"})
		return
	}
	if !constantTimeEqual(body.APIKey, h.cfg.APIKey) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "密钥无效"})
		return
	}
	sid, err := h.sessions.create()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "会话创建失败"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		h.sessions.delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (h *Handler) storageOverview(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	ov, err := h.store.Overview(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func (h *Handler) listSites(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	sites, err := h.store.ListSites(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if q != "" {
		var filtered []site.Summary
		for _, s := range sites {
			if strings.Contains(s.Slug, q) {
				filtered = append(filtered, s)
			}
		}
		sites = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": sites})
}

func (h *Handler) getSite(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	slug := r.PathValue("slug")
	meta, err := h.store.GetMeta(r.Context(), slug)
	if err != nil {
		if errors.Is(err, local.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "站点不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	files, _ := h.store.ListCurrentFiles(r.Context(), slug)
	if files == nil {
		files = []site.FileInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": viewMeta(meta), "files": files, "urls": h.siteURLs(slug)})
}

func (h *Handler) deleteSite(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	slug := r.PathValue("slug")
	if err := h.store.DeleteSite(r.Context(), slug); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) patchSite(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	slug := r.PathValue("slug")
	meta, err := h.store.GetMeta(r.Context(), slug)
	if err != nil {
		if errors.Is(err, local.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "站点不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		Password      *string `json:"password"`
		ClearPassword bool    `json:"clear_password"`
		ExpiresAt     *string `json:"expires_at"`
		ClearExpiry   bool    `json:"clear_expiry"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效 JSON"})
		return
	}
	if body.ClearPassword {
		meta.PasswordHash = ""
	} else if body.Password != nil {
		if *body.Password == "" {
			meta.PasswordHash = ""
		} else {
			hash, err := bcrypt.GenerateFromPassword([]byte(*body.Password), bcrypt.DefaultCost)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "密码哈希失败"})
				return
			}
			meta.PasswordHash = string(hash)
		}
	}
	if body.ClearExpiry {
		meta.ExpiresAt = nil
	} else if body.ExpiresAt != nil {
		if *body.ExpiresAt == "" {
			meta.ExpiresAt = nil
		} else {
			t, err := time.Parse(time.RFC3339, *body.ExpiresAt)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expires_at 需为 RFC3339"})
				return
			}
			meta.ExpiresAt = &t
		}
	}
	if err := h.store.PutMeta(r.Context(), meta); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": viewMeta(meta)})
}

func (h *Handler) listVersions(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	slug := r.PathValue("slug")
	meta, err := h.store.GetMeta(r.Context(), slug)
	if err != nil {
		if errors.Is(err, local.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "站点不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	vers, err := h.store.ListVersions(r.Context(), slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]VersionView, 0, len(vers))
	for _, v := range vers {
		vv := VersionView{
			ID:         v.ID,
			CreatedAt:  v.CreatedAt,
			Current:    v.ID == meta.CurrentVersion,
			PreviewURL: h.signedPreviewURL(slug, v.ID),
		}
		if files, err := h.store.ListVersionFiles(r.Context(), slug, v.ID); err == nil {
			vv.FileCount = len(files)
			for _, f := range files {
				vv.TotalBytes += f.Size
			}
		}
		out = append(out, vv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"versions": out, "current_version": meta.CurrentVersion})
}

func (h *Handler) listVersionFiles(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	slug, ver := r.PathValue("slug"), r.PathValue("versionId")
	files, err := h.store.ListVersionFiles(r.Context(), slug, ver)
	if err != nil {
		if errors.Is(err, local.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "版本不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if files == nil {
		files = []site.FileInfo{}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	writeJSON(w, http.StatusOK, map[string]any{"files": files, "preview_url": h.signedPreviewURL(slug, ver)})
}

func (h *Handler) rollback(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	slug := r.PathValue("slug")
	var body struct {
		VersionID string `json:"version_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.VersionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "需要 version_id"})
		return
	}
	v, err := h.store.Rollback(r.Context(), slug, body.VersionID)
	if err != nil {
		if errors.Is(err, local.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "版本不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v})
}

func (h *Handler) unlockSite(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	meta, err := h.store.GetMeta(r.Context(), slug)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "站点不存在"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效 JSON"})
		return
	}
	if meta.PasswordHash == "" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(meta.PasswordHash), []byte(body.Password)) != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "密码错误"})
		return
	}
	h.SetSiteCookie(w, slug, meta.PasswordHash, h.SiteCookiePath(r, slug))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// SetSiteCookie issues an HMAC-signed unlock cookie for slug.
func (h *Handler) SetSiteCookie(w http.ResponseWriter, slug, passwordHash, cookiePath string) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SiteAuthCookieName(slug),
		Value:    h.signer.SiteCookieValue(slug, passwordHash, time.Now().Add(SiteCookieTTL)),
		Path:     cookiePath,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SiteCookieTTL / time.Second),
	})
}

// SiteAuthed verifies the signed unlock cookie for slug.
func (h *Handler) SiteAuthed(r *http.Request, slug, passwordHash string) bool {
	c, err := r.Cookie(auth.SiteAuthCookieName(slug))
	if err != nil {
		return false
	}
	return h.signer.VerifySiteCookie(c.Value, slug, passwordHash, time.Now())
}

// SiteCookiePath scopes the unlock cookie to the site: "/" when the request is
// on the site's own subdomain, otherwise "/<slug>/".
func (h *Handler) SiteCookiePath(r *http.Request, slug string) string {
	if h.cfg.SubdomainMode && h.cfg.BaseDomain != "" {
		host := r.Host
		if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host[i:], "]") {
			host = host[:i]
		}
		if strings.EqualFold(host, slug+"."+h.cfg.BaseDomain) {
			return "/"
		}
	}
	return "/" + slug + "/"
}

func (h *Handler) createPublish(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	var body struct {
		Slug string `json:"slug"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	draft, err := h.store.CreateRevision(r.Context(), body.Slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"revision_id": draft.ID,
		"slug":        draft.Slug,
	})
}

func (h *Handler) uploadFile(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	rev := r.PathValue("revisionId")
	fp := r.PathValue("filepath")
	fp = path.Clean(fp)
	if err := h.store.Upload(r.Context(), rev, fp, r.Body, r.ContentLength); err != nil {
		if errors.Is(err, local.ErrDraftNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "修订不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) finalizePublish(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	rev := r.PathValue("revisionId")
	v, slug, err := h.store.Finalize(r.Context(), rev)
	if err != nil {
		if errors.Is(err, local.ErrDraftNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "修订不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": v,
		"slug":    slug,
		"urls":    h.siteURLs(slug),
	})
}

func (h *Handler) siteURLs(slug string) map[string]string {
	urls := map[string]string{}
	if h.cfg.PublicURL != "" {
		urls["path"] = h.cfg.PublicURL + "/" + slug + "/"
	}
	if h.cfg.BaseDomain != "" {
		urls["subdomain"] = "https://" + slug + "." + h.cfg.BaseDomain + "/"
	}
	return urls
}

// AuthenticatedAPI performs login + request with session cookie (for CLI).
func (h *Handler) AuthenticateAPIKey(apiKey string) (http.Header, error) {
	if h.cfg.APIKey == "" {
		return nil, errors.New("server has no API key configured")
	}
	if !constantTimeEqual(apiKey, h.cfg.APIKey) {
		return nil, errors.New("invalid api key")
	}
	sid, err := h.sessions.create()
	if err != nil {
		return nil, err
	}
	hdr := http.Header{}
	hdr.Add("Cookie", sessionCookie+"="+sid)
	return hdr, nil
}

// ReadBody helper for tests.
func ReadBody(r io.Reader) []byte {
	b, _ := io.ReadAll(r)
	return b
}
