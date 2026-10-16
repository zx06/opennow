package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zx06/opennow/internal/config"
	"github.com/zx06/opennow/internal/oauth/oauthtest"
	"github.com/zx06/opennow/internal/storage/local"
)

func newOAuthServer(t *testing.T, mut func(*config.Config, *oauthtest.IdP)) (http.Handler, *oauthtest.IdP) {
	t.Helper()
	idp := oauthtest.New(t)
	b, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	publish(t, b, "hello", "<h1>v1</h1>")
	cfg := config.Config{
		APIKey: "k", PathMode: true, AdminPrefix: "/_admin/", PublicURL: "http://opennow.test",
		Secret: "test-secret-0123456789abcdef0123456789",
		Auth: config.AuthConfig{
			AllowedUsers: []string{"github:zx06"},
			Providers: []config.ProviderConfig{{
				Name: "github", Type: "github", ClientID: idp.ClientID, ClientSecret: idp.ClientSecret,
				AuthURL: idp.URL() + "/login/oauth/authorize", TokenURL: idp.URL() + "/login/oauth/access_token", APIURL: idp.URL() + "/api",
			}},
		},
	}
	if mut != nil {
		mut(&cfg, idp)
	}
	srv, err := New(cfg, b, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	return srv.Handler(), idp
}

func serve(h http.Handler, method, target, body string, hdr map[string]string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, "http://opennow.test"+target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, "http://opennow.test"+target, nil)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOAuthAdminEndToEnd(t *testing.T) {
	h, idp := newOAuthServer(t, nil)
	rec := idp.Login(t, h, "github", "/_admin/", nil)
	sess := cookieNamed(rec, "opennow_session")
	if rec.Code != http.StatusFound || sess == nil {
		t.Fatalf("login: %d %v", rec.Code, rec.Header())
	}
	me := serve(h, "GET", "/api/v1/auth/me", "", nil, sess)
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"username":"zx06"`) || !strings.Contains(me.Body.String(), `"method":"oauth"`) || !strings.Contains(me.Body.String(), "avatars.githubusercontent.com") {
		t.Fatalf("me: %d %s", me.Code, me.Body)
	}
	if rec := serve(h, "GET", "/api/v1/sites", "", nil, sess); rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("sites: %d %s", rec.Code, rec.Body)
	}
	// State-changing request with the cookie but without the CSRF header.
	if rec := serve(h, "PATCH", "/api/v1/sites/hello", `{"clear_expiry":true}`, nil, sess); rec.Code != http.StatusForbidden {
		t.Fatalf("csrf missing header: %d", rec.Code)
	}
	if rec := serve(h, "DELETE", "/api/v1/sites/hello", "", map[string]string{"X-OpenNow-CSRF": "1", "Origin": "https://evil.example"}, sess); rec.Code != http.StatusForbidden {
		t.Fatalf("csrf cross origin: %d", rec.Code)
	}
	if rec := serve(h, "PATCH", "/api/v1/sites/hello", `{"clear_expiry":true}`, map[string]string{"X-OpenNow-CSRF": "1", "Origin": "http://opennow.test"}, sess); rec.Code != 200 {
		t.Fatalf("csrf ok: %d %s", rec.Code, rec.Body)
	}
	// Admin session also unlocks previews.
	if rec := serve(h, "GET", "/hello/", "", nil); rec.Code != 200 {
		t.Fatalf("site: %d", rec.Code)
	}
	// Logout clears the cookie.
	rec = serve(h, "POST", "/_auth/logout", "", nil, sess)
	if c := cookieNamed(rec, "opennow_session"); c == nil || c.MaxAge >= 0 {
		t.Fatalf("logout cookie: %+v", c)
	}
	// Anonymous / tampered sessions are rejected.
	bad := *sess
	bad.Value = strings.Replace(sess.Value, ".", ".9", 1)
	if rec := serve(h, "GET", "/api/v1/sites", "", nil, &bad); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered session: %d", rec.Code)
	}
}

func TestBearerAPIKeyAndDisabledKeyLogin(t *testing.T) {
	h, _ := newOAuthServer(t, func(c *config.Config, _ *oauthtest.IdP) { c.Auth.DisableAPIKeyLogin = true })
	if rec := serve(h, "POST", "/api/v1/auth/login", `{"api_key":"k"}`, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("api key login should be disabled: %d", rec.Code)
	}
	// CLI / API bearer still works, without CSRF header.
	if rec := serve(h, "POST", "/api/v1/publish", `{"slug":"cli"}`, map[string]string{"Authorization": "Bearer k"}); rec.Code != 200 {
		t.Fatalf("bearer publish: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(h, "GET", "/api/v1/sites", "", map[string]string{"Authorization": "Bearer nope"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer: %d", rec.Code)
	}
	rec := serve(h, "GET", "/api/v1/auth/providers", "", nil)
	if !strings.Contains(rec.Body.String(), `"api_key_login":false`) || !strings.Contains(rec.Body.String(), `"login_url":"/_auth/login/github"`) {
		t.Fatalf("providers: %s", rec.Body)
	}
}

func TestAPIKeyLoginSessionCookie(t *testing.T) {
	h, _ := newOAuthServer(t, func(c *config.Config, _ *oauthtest.IdP) { c.PublicURL = "https://opennow.test" })
	rec := serve(h, "POST", "/api/v1/auth/login", `{"api_key":"k"}`, nil)
	c := cookieNamed(rec, "opennow_session")
	if rec.Code != 200 || c == nil || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("login: %d %+v", rec.Code, c)
	}
	if rec := serve(h, "GET", "/api/v1/auth/me", "", nil, c); !strings.Contains(rec.Body.String(), `"method":"apikey"`) {
		t.Fatalf("me: %s", rec.Body)
	}
	if rec := serve(h, "POST", "/api/v1/sites/hello/rollback", `{"version_id":"x"}`, nil, c); rec.Code != http.StatusForbidden {
		t.Fatalf("csrf: %d", rec.Code)
	}
}
