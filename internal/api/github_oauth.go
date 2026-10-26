package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
)

// githubHTTP is the client used for token exchange and /user.
// Tests may replace it.
var githubHTTP = &http.Client{Timeout: 15 * time.Second}

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
	writeJSON(w, http.StatusOK, map[string]any{
		"github":       h.cfg.GitHubOAuthConfigured(),
		"api_key":      h.cfg.APIKey != "",
		"github_login": "/api/v1/auth/github/login",
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
	fail := func(code string) {
		h.clearOAuthStateCookie(w)
		http.Redirect(w, r, h.adminRedirect("login_error="+url.QueryEscape(code)), http.StatusFound)
	}

	if !h.cfg.GitHubOAuthConfigured() {
		fail("not_configured")
		return
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		if errParam == "access_denied" {
			fail("denied")
			return
		}
		fail("provider_error")
		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		fail("invalid_state")
		return
	}
	c, err := r.Cookie(oauthStateCookie)
	if err != nil || c.Value == "" || !constantTimeEqual(c.Value, state) {
		fail("invalid_state")
		return
	}
	h.clearOAuthStateCookie(w)

	tok, err := exchangeGitHubCode(h.cfg, code, h.githubCallbackURL())
	if err != nil {
		fail("exchange_failed")
		return
	}
	user, err := fetchGitHubUser(tok)
	if err != nil || user.Login == "" {
		fail("exchange_failed")
		return
	}
	if !config.AllowlistContains(h.cfg.GitHubAllowlist, user.Login) {
		fail("not_allowed")
		return
	}

	sid, err := h.sessions.create("github", user.Login)
	if err != nil {
		fail("session_failed")
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

	resp, err := githubHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var tr githubTokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", err
	}
	if tr.Error != "" || tr.AccessToken == "" {
		if tr.ErrorDesc != "" {
			return "", fmt.Errorf("github token: %s", tr.ErrorDesc)
		}
		return "", fmt.Errorf("github token: %s", tr.Error)
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

	resp, err := githubHTTP.Do(req)
	if err != nil {
		return u, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return u, fmt.Errorf("user endpoint status %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return u, err
	}
	return u, nil
}
