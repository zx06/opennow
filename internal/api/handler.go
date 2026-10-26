package api

import (
	"os"
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
	"github.com/zx06/opennow/internal/oauth"
	"github.com/zx06/opennow/internal/site"
	"github.com/zx06/opennow/internal/storage"
	"github.com/zx06/opennow/internal/storage/common"
	"github.com/zx06/opennow/internal/storage/storeerr"
)

// Handler serves the public admin JSON API.
type Handler struct {
	cfg    config.Config
	store  storage.Backend
	authn  *oauth.Service
	signer *auth.Signer
}

// PreviewTTL is how long a signed preview link stays valid.
const PreviewTTL = 24 * time.Hour

// SiteCookieTTL is how long a site unlock cookie stays valid.
const SiteCookieTTL = 7 * 24 * time.Hour

// New builds the API handler. authn handles sessions, OAuth login and
// Cloudflare Access; when nil it is built from cfg.
func New(cfg config.Config, store storage.Backend, signer *auth.Signer, authn *oauth.Service) (*Handler, error) {
	if signer == nil {
		signer = auth.NewSigner(cfg.Secret)
	}
	if authn == nil {
		var err error
		if authn, err = oauth.NewService(cfg, signer, nil); err != nil {
			return nil, err
		}
	}
	return &Handler{cfg: cfg, store: store, authn: authn, signer: signer}, nil
}

// IsAdmin reports whether the request carries valid admin credentials
// (session cookie, Cloudflare Access identity or bearer API key).
func (h *Handler) IsAdmin(r *http.Request) bool {
	sess, _, _ := h.authn.Authenticate(r)
	return sess != nil
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
	h.authn.Mount(mux)
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

// login exchanges the API key for a signed session cookie (web admin).
// Disabled by OPENNOW_ADMIN_API_KEY_LOGIN=false; the CLI uses a bearer token.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if h.cfg.APIKey == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "未配置 OPENNOW_API_KEY"})
		return
	}
	if !h.authn.APIKeyLoginEnabled() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "已禁用 API 密钥登录，请使用第三方账号登录"})
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
	if err := h.authn.IssueSession(w, auth.Session{Method: "apikey", Name: "API 密钥"}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "会话创建失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	h.authn.ClearSession(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	sess, ok := h.principal(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"method":        sess.Method,
		"provider":      sess.Provider,
		"username":      sess.Username,
		"name":          sess.Name,
		"email":         sess.Email,
		"avatar":        sess.Avatar,
		"csrf_header":   oauth.CSRFHeader,
	})
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
		if errors.Is(err, storeerr.ErrNotFound) {
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
		if errors.Is(err, storeerr.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "站点不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		Slug          *string `json:"slug"`
		Password      *string `json:"password"`
		ClearPassword bool    `json:"clear_password"`
		ExpiresAt     *string `json:"expires_at"`
		ClearExpiry   bool    `json:"clear_expiry"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效 JSON"})
		return
	}
	if body.Slug != nil && *body.Slug != "" && *body.Slug != slug {
		if !common.ValidID(*body.Slug) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效 slug"})
			return
		}
		meta, err = h.store.RenameSite(r.Context(), slug, *body.Slug)
		if err != nil {
			if errors.Is(err, storeerr.ErrConflict) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "slug 已被占用"})
				return
			}
			if errors.Is(err, storeerr.ErrNotFound) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "站点不存在"})
				return
			}
			if errors.Is(err, storeerr.ErrInvalidPath) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "无效 slug"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		slug = meta.Slug
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
	touched := body.ClearPassword || body.Password != nil || body.ClearExpiry || body.ExpiresAt != nil
	if touched {
		if err := h.store.PutMeta(r.Context(), meta); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
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
		if errors.Is(err, storeerr.ErrNotFound) {
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
		if errors.Is(err, storeerr.ErrNotFound) {
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
		if errors.Is(err, storeerr.ErrNotFound) {
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
		"chunk_size":  DefaultUploadChunkBytes,
	})
}

func (h *Handler) uploadFile(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	rev := r.PathValue("revisionId")
	fp := path.Clean(r.PathValue("filepath"))

	offset, total, chunked, err := parseChunkMeta(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if !chunked {
		if err := h.store.Upload(r.Context(), rev, fp, r.Body, r.ContentLength); err != nil {
			h.writeUploadErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	complete, partFile, written, err := h.writeChunk(rev, fp, offset, total, r.Body, r.ContentLength)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !complete {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "partial",
			"offset":  offset,
			"written": written,
			"next":    offset + written,
			"total":   total,
		})
		return
	}

	f, err := os.Open(partFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法读取拼装文件"})
		return
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	upErr := h.store.Upload(r.Context(), rev, fp, f, st.Size())
	f.Close()
	_ = os.Remove(partFile)
	if upErr != nil {
		h.writeUploadErr(w, upErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "size": st.Size()})
}

func (h *Handler) writeUploadErr(w http.ResponseWriter, err error) {
	if errors.Is(err, storeerr.ErrDraftNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "修订不存在"})
		return
	}
	if errors.Is(err, storeerr.ErrTooLarge) {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": err.Error()})
		return
	}
	if errors.Is(err, storeerr.ErrInvalidPath) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "非法路径"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func (h *Handler) finalizePublish(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	rev := r.PathValue("revisionId")
	v, slug, err := h.store.Finalize(r.Context(), rev)
	if err != nil {
		if errors.Is(err, storeerr.ErrDraftNotFound) {
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

// ReadBody helper for tests.
func ReadBody(r io.Reader) []byte {
	b, _ := io.ReadAll(r)
	return b
}
