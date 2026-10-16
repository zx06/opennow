package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zx06/opennow/internal/auth"
	"github.com/zx06/opennow/internal/config"
)

const (
	// SessionCookie carries the signed admin session.
	SessionCookie = "opennow_session"
	// StateCookie carries the signed OAuth state / nonce / PKCE verifier
	// between /_auth/login and /_auth/callback.
	StateCookie = "opennow_oauth_state"
	// CSRFHeader must accompany state-changing requests authenticated by an
	// ambient credential (session cookie or Cloudflare Access).
	CSRFHeader = "X-OpenNow-CSRF"

	stateTTL        = 10 * time.Minute
	kindSession     = "admin-session"
	kindKeySession  = "admin-session-apikey"
	kindOAuthState  = "oauth-state"
	exchangeTimeout = 20 * time.Second
)

// ErrBadBearer is returned when an Authorization: Bearer header is present
// but does not carry the API key.
var ErrBadBearer = errors.New("invalid bearer token")

// Service wires providers, the allowlist, sessions and Cloudflare Access.
type Service struct {
	cfg       config.AuthConfig
	apiKey    string
	publicURL string
	admin     string
	adminHost string
	secure    bool
	signer    *auth.Signer
	allow     *Allowlist
	cf        *CFAccess
	providers map[string]Provider
	order     []string
	now       func() time.Time
}

// NewService validates the configuration and builds the login service. client
// is used for all outbound IdP requests (nil = http.DefaultClient with a
// timeout).
func NewService(cfg config.Config, signer *auth.Signer, client *http.Client) (*Service, error) {
	ac := cfg.Auth
	if ac.SessionTTL == 0 {
		ac.SessionTTL = config.DefaultSessionTTL
	}
	if err := ac.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: exchangeTimeout}
	}
	base := strings.TrimRight(cfg.PublicURL, "/")
	if ac.BaseURL != "" {
		base = strings.TrimRight(ac.BaseURL, "/")
	}
	s := &Service{
		cfg:       ac,
		apiKey:    cfg.APIKey,
		publicURL: base,
		admin:     cfg.AdminPrefix,
		adminHost: cfg.AdminHost,
		secure:    strings.HasPrefix(strings.ToLower(base), "https://"),
		signer:    signer,
		allow:     NewAllowlist(ac),
		cf:        NewCFAccess(ac.CFAccess, client),
		providers: map[string]Provider{},
		now:       time.Now,
	}
	if s.admin == "" {
		s.admin = "/_admin/"
	}
	if len(ac.Providers) > 0 {
		if s.publicURL == "" {
			return nil, errors.New("OPENNOW_PUBLIC_URL (or OPENNOW_OAUTH_BASE_URL) is required for OAuth login (callback URL is <OPENNOW_PUBLIC_URL>/_auth/callback/<provider>)")
		}
		if s.allow.Empty() && !ac.AllowAny {
			log.Printf("warning: OAuth providers configured but the allowlist is empty; every OAuth login will be denied (set OPENNOW_OAUTH_ALLOWED_* or OPENNOW_OAUTH_ALLOW_ANY=true)")
		}
	}
	for _, pc := range ac.Providers {
		var orgs []string
		if pc.Type == "github" {
			orgs = ac.AllowedGitHubOrgs
		}
		p, err := NewProvider(pc, s.CallbackURL(pc.Name), client, orgs)
		if err != nil {
			return nil, err
		}
		s.providers[pc.Name] = p
		s.order = append(s.order, pc.Name)
	}
	return s, nil
}

// CallbackURL is the redirect URI to register with the provider.
func (s *Service) CallbackURL(provider string) string {
	// Keep the GitHub OAuth App callback used by OPENNOW_GITHUB_* deployments.
	if provider == "github" {
		return s.publicURL + "/api/v1/auth/github/callback"
	}
	return s.publicURL + "/_auth/callback/" + url.PathEscape(provider)
}

// APIKeyLoginEnabled reports whether the web admin accepts API key login.
func (s *Service) APIKeyLoginEnabled() bool { return s.apiKey != "" && !s.cfg.DisableAPIKeyLogin }

// Mount registers the /_auth/* routes and the public provider listing.
func (s *Service) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /_auth/login/{provider}", s.handleLogin)
	mux.HandleFunc("GET /_auth/callback/{provider}", s.handleCallback)
	mux.HandleFunc("POST /_auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/providers", s.handleProviders)
	// Legacy aliases for the GitHub-only admin login (#4) so existing OAuth Apps
	// and bookmarks keep working without changing OPENNOW_GITHUB_* or the callback URL.
	if _, ok := s.providers["github"]; ok {
		mux.HandleFunc("GET /api/v1/auth/github/login", func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("provider", "github")
			s.handleLogin(w, r)
		})
		mux.HandleFunc("GET /api/v1/auth/github/callback", func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("provider", "github")
			s.handleCallback(w, r)
		})
		mux.HandleFunc("GET /api/v1/auth/options", s.handleLegacyOptions)
	}
}

// ProviderInfo is the public description of a provider for the login page.
type ProviderInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	LoginURL    string `json:"login_url"`
}

func (s *Service) handleProviders(w http.ResponseWriter, r *http.Request) {
	list := make([]ProviderInfo, 0, len(s.order))
	for _, n := range s.order {
		p := s.providers[n]
		list = append(list, ProviderInfo{Name: n, DisplayName: p.DisplayName(), Type: p.Type(), LoginURL: "/_auth/login/" + url.PathEscape(n)})
	}
	w.Header().Set("Cache-Control", "no-store")
	out := map[string]any{
		"providers":     list,
		"api_key_login": s.APIKeyLoginEnabled(),
		"cf_access":     s.cf != nil,
	}
	if s.cfg.DenyMessage != "" {
		out["deny_message"] = s.cfg.DenyMessage
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLegacyOptions mirrors GET /api/v1/auth/options from the GitHub-only login.
func (s *Service) handleLegacyOptions(w http.ResponseWriter, r *http.Request) {
	_, hasGithub := s.providers["github"]
	out := map[string]any{
		"github":       hasGithub,
		"api_key":      s.APIKeyLoginEnabled(),
		"github_login": "/api/v1/auth/github/login",
		"allowlist":    legacyGithubAllowlist(s.cfg),
	}
	if s.cfg.DenyMessage != "" {
		out["deny_message"] = s.cfg.DenyMessage
	}
	writeJSON(w, http.StatusOK, out)
}

func legacyGithubAllowlist(ac config.AuthConfig) []string {
	var out []string
	for _, u := range ac.AllowedUsers {
		if p, v, ok := strings.Cut(u, ":"); ok && p == "github" {
			out = append(out, v)
		}
	}
	return out
}

// oauthState is kept in a signed cookie between login and callback. The PKCE
// code_verifier is not stored: it is derived from the server key and the
// state, so it never leaves the server.
type oauthState struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Redirect string `json:"r"`
}

func (s *Service) verifier(st oauthState) string {
	return s.signer.Derive("pkce-verifier", st.Provider, st.State, st.Nonce)
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	p, ok := s.providers[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	st := oauthState{
		Provider: name,
		State:    randomToken(),
		Nonce:    randomToken(),
		Redirect: SafeRedirect(r.URL.Query().Get("redirect"), s.admin),
	}
	target, err := p.AuthCodeURL(r.Context(), st.State, st.Nonce, s.verifier(st))
	if err != nil {
		log.Printf("oauth %s: %v", name, err)
		s.failLogin(w, r, "provider_unavailable")
		return
	}
	val, err := s.signer.SealJSON(kindOAuthState, st, s.now().Add(stateTTL))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: StateCookie, Value: val, Path: "/", MaxAge: int(stateTTL / time.Second),
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("provider")
	p, ok := s.providers[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	// The state cookie is single use.
	http.SetCookie(w, &http.Cookie{Name: StateCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode})
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		log.Printf("oauth %s: provider returned error %q", name, e)
		s.failLogin(w, r, "denied_by_provider")
		return
	}
	var st oauthState
	c, err := r.Cookie(StateCookie)
	if err != nil || s.signer.OpenJSON(kindOAuthState, c.Value, s.now(), &st) != nil ||
		st.Provider != name || st.State == "" ||
		subtle.ConstantTimeCompare([]byte(st.State), []byte(q.Get("state"))) != 1 {
		s.failLogin(w, r, "invalid_state")
		return
	}
	code := q.Get("code")
	if code == "" {
		s.failLogin(w, r, "invalid_state")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), exchangeTimeout)
	defer cancel()
	id, err := p.Exchange(ctx, code, s.verifier(st), st.Nonce)
	if err != nil {
		log.Printf("oauth %s: %v", name, err)
		s.failLogin(w, r, "exchange_failed")
		return
	}
	if !s.allow.Allows(*id) {
		log.Printf("oauth %s: login denied for %s:%s (email=%q verified=%v)", name, id.Provider, id.Username, id.Email, id.EmailVerified)
		s.failLogin(w, r, "not_allowed")
		return
	}
	sess := auth.Session{
		Method: "oauth", Provider: id.Provider, Subject: id.Subject, Username: id.Username,
		Name: id.Name, Email: id.Email, Verified: id.EmailVerified, Avatar: safeAvatar(id.Avatar),
		Orgs: s.allow.OrgsFor(id.Orgs),
	}
	if err := s.IssueSession(w, sess); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("oauth %s: login %s:%s", name, id.Provider, id.Username)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, SafeRedirect(st.Redirect, s.admin), http.StatusFound)
}

func (s *Service) failLogin(w http.ResponseWriter, r *http.Request, code string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, s.admin+"?login_error="+url.QueryEscape(code), http.StatusFound)
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.ClearSession(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// IssueSession sets the signed session cookie.
func (s *Service) IssueSession(w http.ResponseWriter, sess auth.Session) error {
	now := s.now()
	sess.IssuedAt = now.Unix()
	kind, extra := kindSession, []string(nil)
	if sess.Method == "apikey" {
		// Bound to the current API key: rotating the key ends these sessions.
		kind, extra = kindKeySession, []string{s.keyBinding()}
	}
	val, err := s.signer.SealJSON(kind, sess, now.Add(s.cfg.SessionTTL), extra...)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: val, Path: "/", MaxAge: int(s.cfg.SessionTTL / time.Second),
		HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// ClearSession expires the session cookie.
func (s *Service) ClearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode})
}

func (s *Service) keyBinding() string {
	h := sha256.Sum256([]byte("opennow-api-key|" + s.apiKey))
	return hex.EncodeToString(h[:])
}

// Authenticate resolves the caller. ambient is true when the credential is
// sent automatically by the browser (session cookie, Cloudflare Access) and
// state-changing requests therefore need CSRF protection. It returns
// ErrBadBearer for a wrong bearer token and (nil, false, nil) for anonymous
// requests.
func (s *Service) Authenticate(r *http.Request) (sess *auth.Session, ambient bool, err error) {
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, tok, _ := strings.Cut(h, " ")
		if strings.EqualFold(scheme, "Bearer") {
			tok = strings.TrimSpace(tok)
			if s.apiKey != "" && tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.apiKey)) == 1 {
				return &auth.Session{Method: "bearer"}, false, nil
			}
			return nil, false, ErrBadBearer
		}
	}
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		var ss auth.Session
		if s.signer.OpenJSON(kindSession, c.Value, s.now(), &ss) == nil && ss.Method == "oauth" {
			// Re-check the allowlist so removing a user takes effect at once.
			if s.allow.Allows(Identity{Provider: ss.Provider, Subject: ss.Subject, Username: ss.Username, Email: ss.Email, EmailVerified: ss.Verified, Orgs: ss.Orgs}) {
				if _, ok := s.providers[ss.Provider]; ok {
					return &ss, true, nil
				}
			}
		} else if s.APIKeyLoginEnabled() && s.signer.OpenJSON(kindKeySession, c.Value, s.now(), &ss, s.keyBinding()) == nil && ss.Method == "apikey" {
			return &ss, true, nil
		}
	}
	if s.cf != nil && r.Header.Get("Cf-Access-Jwt-Assertion") != "" {
		id, err := s.cf.Identify(r)
		if err != nil {
			log.Printf("cf access: %v", err)
			return nil, false, nil
		}
		if s.cf.allowAny || s.allow.Allows(*id) {
			return &auth.Session{Method: "cf-access", Provider: id.Provider, Subject: id.Subject, Username: id.Username, Email: id.Email, Verified: true, Name: id.Name}, true, nil
		}
		log.Printf("cf access: %s not in allowlist", id.Email)
	}
	return nil, false, nil
}

// CheckCSRF enforces the custom-header defence for state-changing requests
// that rely on ambient credentials: the request must carry X-OpenNow-CSRF
// (which a cross-site page cannot add without a CORS preflight, and opennow
// never answers preflights) and, when the browser sends them, Origin and
// Sec-Fetch-Site must indicate a same-origin request.
func (s *Service) CheckCSRF(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if r.Header.Get(CSRFHeader) == "" {
		return false
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" {
			return false
		}
		if !strings.EqualFold(u.Host, r.Host) && !strings.EqualFold(u.Host, s.adminHost) && !strings.EqualFold(o, originOf(s.publicURL)) {
			return false
		}
	}
	return true
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// SafeRedirect returns target if it is a same-site relative path, otherwise
// def. It rejects absolute and scheme-relative URLs ("//evil", "/\evil") and
// control characters, preventing open redirects after login.
func SafeRedirect(target, def string) string {
	if target == "" || len(target) > 2048 {
		return def
	}
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.HasPrefix(target, "/\\") {
		return def
	}
	for _, r := range target {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return def
		}
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return def
	}
	return target
}

// safeAvatar keeps only http(s) avatar URLs (the UI renders them in <img>).
func safeAvatar(u string) string {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
		return ""
	}
	return u
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
