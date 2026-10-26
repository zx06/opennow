package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zx06/opennow/internal/config"
)

const (
	githubAuthorizeURL = "https://github.com/login/oauth/authorize"
	githubTokenURL     = "https://github.com/login/oauth/access_token"
	githubUserURL      = "https://api.github.com/user"
	githubHTTPTimeout  = 45 * time.Second
)

// githubHTTP is the client used for token exchange and /user.
// Uses the default transport so HTTP_PROXY / HTTPS_PROXY / NO_PROXY apply.
// Tests may replace it.
// Timeout only: a nil Transport uses http.DefaultTransport, which already
// honours HTTP_PROXY / HTTPS_PROXY / NO_PROXY. Do not add a custom proxy library.
var githubHTTP = &http.Client{Timeout: githubHTTPTimeout}

// Overrideable endpoints for tests.
var (
	githubAuthorizeEndpoint = githubAuthorizeURL
	githubTokenEndpoint     = githubTokenURL
	githubUserEndpoint      = githubUserURL
)

func (h *Handler) githubCallbackURL() string {
	return strings.TrimRight(h.cfg.PublicURL, "/") + "/api/v1/auth/github/callback"
}

func (h *Handler) adminRedirect(pathQuery string) string {
	base := h.cfg.AdminPrefix
	if base == "" {
		base = "/_admin/"
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	if pathQuery == "" {
		return base
	}
	if strings.HasPrefix(pathQuery, "?") {
		return base + pathQuery
	}
	return base + "?" + pathQuery
}

func (h *Handler) loginOptions(w http.ResponseWriter, r *http.Request) {
	apiKeyUI := h.cfg.APIKey != "" && !h.cfg.DisableAdminAPIKeyLogin
	writeJSON(w, http.StatusOK, map[string]any{
		"github":       h.cfg.GitHubOAuthConfigured(),
		"api_key":      apiKeyUI,
		"github_login": "/api/v1/auth/github/login",
		"deny_message": h.cfg.GitHubDenyMessage,
		"allowlist":    h.cfg.GitHubAllowlist,
	})
}

func (h *Handler) githubLogin(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.GitHubOAuthConfigured() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "未配置 GitHub OAuth（需 OPENNOW_GITHUB_CLIENT_ID / OPENNOW_GITHUB_CLIENT_SECRET）",
		})
		return
	}
	if h.cfg.PublicURL == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "未配置 OPENNOW_PUBLIC_URL，无法拼装 GitHub 回调地址",
		})
		return
	}

	state, err := randomHex(32)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "无法生成 state"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    state,
		Path:     "/api/v1/auth/github/",
		HttpOnly: true,
		Secure:   h.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(oauthStateTTL / time.Second),
	})

	q := url.Values{}
	q.Set("client_id", h.cfg.GitHubClientID)
	q.Set("redirect_uri", h.githubCallbackURL())
	q.Set("scope", "read:user")
	q.Set("state", state)
	q.Set("allow_signup", "false")
	http.Redirect(w, r, githubAuthorizeEndpoint+"?"+q.Encode(), http.StatusFound)
}

func (h *Handler) githubCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(code string, extra url.Values) {
		h.clearOAuthStateCookie(w)
		q := url.Values{}
		q.Set("login_error", code)
		for k, vs := range extra {
			for _, v := range vs {
				q.Set(k, v)
			}
		}
		http.Redirect(w, r, h.adminRedirect(q.Encode()), http.StatusFound)
	}

	if !h.cfg.GitHubOAuthConfigured() {
		fail("not_configured", nil)
		return
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		log.Printf("github oauth: provider error=%q desc=%q", errParam, r.URL.Query().Get("error_description"))
		if errParam == "access_denied" {
			fail("denied", nil)
			return
		}
		fail("provider_error", nil)
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		fail("invalid_state", nil)
		return
	}
	c, err := r.Cookie(oauthStateCookie)
	if err != nil || c.Value == "" || !constantTimeEqual(c.Value, state) {
		fail("invalid_state", nil)
		return
	}
	h.clearOAuthStateCookie(w)

	tok, err := exchangeGitHubCode(h.cfg, code, h.githubCallbackURL())
	if err != nil {
		log.Printf("github oauth: token exchange failed: %v", err)
		fail("exchange_failed", nil)
		return
	}
	user, err := fetchGitHubUser(tok)
	if err != nil {
		log.Printf("github oauth: fetch user failed: %v", err)
		fail("exchange_failed", nil)
		return
	}
	if user.Login == "" {
		log.Printf("github oauth: fetch user returned empty login")
		fail("exchange_failed", nil)
		return
	}
	if !config.AllowlistContains(h.cfg.GitHubAllowlist, user.Login) {
		log.Printf("github oauth: user %q not in allowlist %v", user.Login, h.cfg.GitHubAllowlist)
		fail("not_allowed", url.Values{"user": {user.Login}})
		return
	}

	sid, err := h.sessions.create("github", user.Login)
	if err != nil {
		log.Printf("github oauth: create session failed: %v", err)
		fail("session_failed", nil)
		return
	}
	h.setSessionCookie(w, sid)
	http.Redirect(w, r, h.adminRedirect(""), http.StatusFound)
}

func (h *Handler) clearOAuthStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    "",
		Path:     "/api/v1/auth/github/",
		HttpOnly: true,
		Secure:   h.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type githubTokenResp struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

func exchangeGitHubCode(cfg config.Config, code, redirectURI string) (string, error) {
	form := url.Values{}
	form.Set("client_id", cfg.GitHubClientID)
	form.Set("client_secret", cfg.GitHubClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequest(http.MethodPost, githubTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "opennow")

	resp, err := githubHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token endpoint status %d body=%s", resp.StatusCode, truncate(string(body), 200))
	}
	var tr githubTokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("token json: %w body=%s", err, truncate(string(body), 200))
	}
	if tr.Error != "" || tr.AccessToken == "" {
		if tr.ErrorDesc != "" {
			return "", fmt.Errorf("github token: %s (%s)", tr.Error, tr.ErrorDesc)
		}
		if tr.Error != "" {
			return "", fmt.Errorf("github token: %s", tr.Error)
		}
		return "", fmt.Errorf("github token: empty access_token body=%s", truncate(string(body), 200))
	}
	return tr.AccessToken, nil
}

type githubUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Name  string `json:"name"`
}

func fetchGitHubUser(token string) (githubUser, error) {
	var u githubUser
	req, err := http.NewRequest(http.MethodGet, githubUserEndpoint, nil)
	if err != nil {
		return u, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "opennow")

	resp, err := githubHTTP.Do(req)
	if err != nil {
		return u, fmt.Errorf("user request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return u, fmt.Errorf("user endpoint status %d body=%s", resp.StatusCode, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return u, fmt.Errorf("user json: %w body=%s", err, truncate(string(body), 200))
	}
	return u, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
