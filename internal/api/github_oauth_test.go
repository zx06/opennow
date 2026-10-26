package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zx06/opennow/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testHandler(t *testing.T, cfg config.Config) (*Handler, *http.ServeMux) {
	t.Helper()
	if cfg.AdminPrefix == "" {
		cfg.AdminPrefix = "/_admin/"
	}
	h := New(cfg, nil, nil)
	mux := http.NewServeMux()
	h.Mount(mux)
	return h, mux
}

func TestLoginOptions_Unconfigured(t *testing.T) {
	_, mux := testHandler(t, config.Config{APIKey: "k"})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/auth/options", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["github"] != false {
		t.Fatalf("github=%v want false", body["github"])
	}
	if body["api_key"] != true {
		t.Fatalf("api_key=%v want true", body["api_key"])
	}
}

func TestGitHubLogin_NotConfigured(t *testing.T) {
	_, mux := testHandler(t, config.Config{PublicURL: "https://r.example"})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/login", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
}

func TestGitHubLogin_RedirectAndStateCookie(t *testing.T) {
	cfg := config.Config{
		PublicURL:          "https://r.xuzhuo.xyz",
		GitHubClientID:     "cid",
		GitHubClientSecret: "sec",
		GitHubAllowlist:    []string{"zx06"},
		AdminPrefix:        "/_admin/",
	}
	_, mux := testHandler(t, cfg)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/login", nil))
	if rr.Code != http.StatusFound {
		t.Fatalf("status %d", rr.Code)
	}
	loc := rr.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(loc, githubAuthorizeEndpoint) {
		t.Fatalf("location %s", loc)
	}
	q := u.Query()
	if q.Get("client_id") != "cid" {
		t.Fatalf("client_id %s", q.Get("client_id"))
	}
	wantCB := "https://r.xuzhuo.xyz/api/v1/auth/github/callback"
	if q.Get("redirect_uri") != wantCB {
		t.Fatalf("redirect_uri %s", q.Get("redirect_uri"))
	}
	if q.Get("allow_signup") != "false" {
		t.Fatalf("allow_signup %s", q.Get("allow_signup"))
	}
	state := q.Get("state")
	if state == "" {
		t.Fatal("missing state")
	}
	var stateCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == oauthStateCookie {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("missing state cookie")
	}
	if stateCookie.Value != state {
		t.Fatalf("cookie state mismatch")
	}
	if !stateCookie.HttpOnly {
		t.Fatal("state cookie must be HttpOnly")
	}
	if !stateCookie.Secure {
		t.Fatal("state cookie must be Secure when PublicURL is https")
	}
	if stateCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite=%v", stateCookie.SameSite)
	}
	if stateCookie.Path != "/api/v1/auth/github/" {
		t.Fatalf("path %s", stateCookie.Path)
	}
}

func TestGitHubCallback_InvalidState(t *testing.T) {
	cfg := config.Config{
		PublicURL:          "https://r.xuzhuo.xyz",
		GitHubClientID:     "cid",
		GitHubClientSecret: "sec",
		GitHubAllowlist:    []string{"zx06"},
	}
	_, mux := testHandler(t, cfg)

	// Missing cookie
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?code=x&state=abc", nil)
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("status %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Location"), "login_error=invalid_state") {
		t.Fatalf("loc %s", rr.Header().Get("Location"))
	}

	// Mismatched cookie
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?code=x&state=abc", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "other"})
	mux.ServeHTTP(rr, req)
	if !strings.Contains(rr.Header().Get("Location"), "login_error=invalid_state") {
		t.Fatalf("loc %s", rr.Header().Get("Location"))
	}
}

func TestGitHubCallback_Allowlist(t *testing.T) {
	cfg := config.Config{
		PublicURL:          "https://r.xuzhuo.xyz",
		GitHubClientID:     "cid",
		GitHubClientSecret: "sec",
		GitHubAllowlist:    []string{"zx06"},
		AdminPrefix:        "/_admin/",
	}

	origHTTP := githubHTTP
	origTok := githubTokenEndpoint
	origUser := githubUserEndpoint
	t.Cleanup(func() {
		githubHTTP = origHTTP
		githubTokenEndpoint = origTok
		githubUserEndpoint = origUser
	})

	muxTok := http.NewServeMux()
	muxTok.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("redirect_uri") != "https://r.xuzhuo.xyz/api/v1/auth/github/callback" {
			http.Error(w, "bad redirect_uri", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"tok","token_type":"bearer"}`)
	})
	muxTok.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		login := r.Header.Get("X-Test-Login")
		if login == "" {
			login = "zx06"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"login": login, "id": 1})
	})
	srv := httptest.NewServer(muxTok)
	defer srv.Close()
	githubTokenEndpoint = srv.URL + "/login/oauth/access_token"
	githubUserEndpoint = srv.URL + "/user"
	githubHTTP = srv.Client()

	h, mux := testHandler(t, cfg)

	// Allowed user
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?code=ok&state=st", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "st"})
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Location") != "/_admin/" {
		t.Fatalf("loc %s", rr.Header().Get("Location"))
	}
	var sessCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			sessCookie = c
		}
	}
	if sessCookie == nil {
		t.Fatal("missing session cookie")
	}
	if !sessCookie.HttpOnly || !sessCookie.Secure || sessCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie flags HttpOnly=%v Secure=%v SameSite=%v", sessCookie.HttpOnly, sessCookie.Secure, sessCookie.SameSite)
	}
	if !h.sessions.valid(sessCookie.Value) {
		t.Fatal("session not valid")
	}
	sess, ok := h.sessions.get(sessCookie.Value)
	if !ok || sess.method != "github" || !strings.EqualFold(sess.login, "zx06") {
		t.Fatalf("session=%+v", sess)
	}

	// Denied user (case-insensitive allowlist still rejects others)
	githubHTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/access_token") {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"tok"}`)),
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"login":"someone-else","id":2}`)),
			Request:    r,
		}, nil
	})}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?code=ok&state=st2", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "st2"})
	mux.ServeHTTP(rr, req)
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "login_error=not_allowed") {
		t.Fatalf("loc %s", loc)
	}
	if !strings.Contains(loc, "user=someone-else") {
		t.Fatalf("expected user in loc %s", loc)
	}
}

func TestGitHubCallback_CaseInsensitiveAllowlist(t *testing.T) {
	cfg := config.Config{
		PublicURL:          "https://r.xuzhuo.xyz",
		GitHubClientID:     "cid",
		GitHubClientSecret: "sec",
		GitHubAllowlist:    []string{"ZX06"},
		AdminPrefix:        "/_admin/",
	}
	origHTTP := githubHTTP
	origTok := githubTokenEndpoint
	origUser := githubUserEndpoint
	t.Cleanup(func() {
		githubHTTP = origHTTP
		githubTokenEndpoint = origTok
		githubUserEndpoint = origUser
	})
	githubHTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "access_token") {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"tok"}`)),
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"login":"zx06","id":1}`)),
			Request:    r,
		}, nil
	})}
	githubTokenEndpoint = "http://example.invalid/login/oauth/access_token"
	githubUserEndpoint = "http://example.invalid/user"

	_, mux := testHandler(t, cfg)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?code=ok&state=st", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "st"})
	mux.ServeHTTP(rr, req)
	if rr.Header().Get("Location") != "/_admin/" {
		t.Fatalf("loc %s", rr.Header().Get("Location"))
	}
}

func TestAPIKeyLogin_StillWorks(t *testing.T) {
	h, mux := testHandler(t, config.Config{
		APIKey:    "secret",
		PublicURL: "https://r.xuzhuo.xyz",
	})
	rr := httptest.NewRecorder()
	body := strings.NewReader(`{"api_key":"secret"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d %s", rr.Code, rr.Body.String())
	}
	var sess *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			sess = c
		}
	}
	if sess == nil || !h.sessions.valid(sess.Value) {
		t.Fatal("session missing")
	}
	if !sess.Secure {
		t.Fatal("want Secure session cookie on https PublicURL")
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(sess)
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("me %d", rr.Code)
	}
	var me map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &me)
	if me["method"] != "apikey" {
		t.Fatalf("me=%v", me)
	}
}

func TestLoginOptions_DisableAPIKeyUI(t *testing.T) {
	_, mux := testHandler(t, config.Config{APIKey: "k", DisableAdminAPIKeyLogin: true})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/auth/options", nil))
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body["api_key"] != false {
		t.Fatalf("api_key=%v want false", body["api_key"])
	}
}
