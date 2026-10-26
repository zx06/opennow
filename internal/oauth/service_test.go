package oauth_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zx06/opennow/internal/auth"
	"github.com/zx06/opennow/internal/config"
	"github.com/zx06/opennow/internal/oauth"
	"github.com/zx06/opennow/internal/oauth/oauthtest"
)

const testSecret = "test-secret-0123456789abcdef0123456789"

type env struct {
	svc *oauth.Service
	h   http.Handler
	idp *oauthtest.IdP
}

func providerFor(idp *oauthtest.IdP, name, typ string) config.ProviderConfig {
	pc := config.ProviderConfig{Name: name, Type: typ, ClientID: idp.ClientID, ClientSecret: idp.ClientSecret}
	switch typ {
	case "oidc":
		pc.Issuer = idp.URL()
	case "oauth2":
		pc.AuthURL, pc.TokenURL, pc.UserInfoURL = idp.URL()+"/authorize", idp.URL()+"/token", idp.URL()+"/userinfo"
	case "github":
		pc.AuthURL, pc.TokenURL, pc.APIURL = idp.URL()+"/login/oauth/authorize", idp.URL()+"/login/oauth/access_token", idp.URL()+"/api"
	}
	return pc
}

func newEnv(t *testing.T, ac config.AuthConfig, pcs ...func(*oauthtest.IdP) config.ProviderConfig) *env {
	t.Helper()
	idp := oauthtest.New(t)
	for _, f := range pcs {
		ac.Providers = append(ac.Providers, f(idp))
	}
	cfg := config.Config{APIKey: "k", PublicURL: "http://opennow.test", AdminPrefix: "/_admin/", Auth: ac}
	svc, err := oauth.NewService(cfg, auth.NewSigner(testSecret), nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svc.Mount(mux)
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		s, _, _ := svc.Authenticate(r)
		if s == nil {
			http.Error(w, "anon", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(s.Method + "|" + s.Provider + "|" + s.Username + "|" + s.Email))
	})
	return &env{svc: svc, h: mux, idp: idp}
}

func p(name, typ string) func(*oauthtest.IdP) config.ProviderConfig {
	return func(idp *oauthtest.IdP) config.ProviderConfig { return providerFor(idp, name, typ) }
}

func sessionFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauth.SessionCookie && c.Value != "" && c.MaxAge > 0 {
			return c
		}
	}
	return nil
}

func (e *env) whoami(c *http.Cookie) (int, string) {
	req := httptest.NewRequest("GET", "http://opennow.test/whoami", nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func expectDenied(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "login_error="+code) {
		t.Fatalf("want redirect with login_error=%s, got %d %q", code, rec.Code, rec.Header().Get("Location"))
	}
	if sessionFrom(t, rec) != nil {
		t.Fatal("session issued on failed login")
	}
}

func TestOIDCLoginFlow(t *testing.T) {
	e := newEnv(t, config.AuthConfig{AllowedUsers: []string{"corp:alice"}}, p("corp", "oidc"))

	// The authorization request carries state, nonce and an S256 PKCE challenge.
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "http://opennow.test/_auth/login/corp", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	q := loc.Query()
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("auth url missing state/nonce/pkce: %s", loc)
	}
	if q.Get("redirect_uri") != "http://opennow.test/_auth/callback/corp" || !strings.Contains(q.Get("scope"), "openid") {
		t.Fatalf("auth url: %s", loc)
	}
	if strings.Contains(loc.String(), "code_verifier") {
		t.Fatal("verifier leaked into auth URL")
	}
	var st *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauth.StateCookie {
			st = c
		}
	}
	if st == nil || !st.HttpOnly || st.SameSite != http.SameSiteLaxMode || st.Path != "/" {
		t.Fatalf("state cookie: %+v", st)
	}

	rec = e.idp.Login(t, e.h, "corp", "/_admin/?x=1", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/_admin/?x=1" {
		t.Fatalf("callback: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	c := sessionFrom(t, rec)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure {
		t.Fatalf("session cookie: %+v", c)
	}
	if code, body := e.whoami(c); code != 200 || body != "oauth|corp|alice|alice@example.com" {
		t.Fatalf("whoami: %d %s", code, body)
	}
}

func TestOIDCUserInfoMerge(t *testing.T) {
	e := newEnv(t, config.AuthConfig{AllowedEmails: []string{"alice@example.com"}}, p("corp", "oidc"))
	e.idp.UserInfoOnly = true
	c := sessionFrom(t, e.idp.Login(t, e.h, "corp", "", nil))
	if code, body := e.whoami(c); code != 200 || body != "oauth|corp|alice|alice@example.com" {
		t.Fatalf("whoami: %d %s", code, body)
	}
}

func TestOIDCTamperedTokensRejected(t *testing.T) {
	cases := map[string]func(*oauthtest.IdP){
		"bad signature":  func(i *oauthtest.IdP) { i.BadSignature = true },
		"wrong nonce":    func(i *oauthtest.IdP) { i.WrongNonce = true },
		"wrong audience": func(i *oauthtest.IdP) { i.WrongAudience = true },
		"wrong issuer":   func(i *oauthtest.IdP) { i.WrongIssuer = true },
		"expired":        func(i *oauthtest.IdP) { i.Expired = true },
		"no id_token":    func(i *oauthtest.IdP) { i.OmitIDToken = true },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, config.AuthConfig{AllowAny: true}, p("corp", "oidc"))
			mut(e.idp)
			expectDenied(t, e.idp.Login(t, e.h, "corp", "", nil), "exchange_failed")
		})
	}
}

func TestStateValidation(t *testing.T) {
	e := newEnv(t, config.AuthConfig{AllowAny: true}, p("corp", "oidc"), p("other", "oidc"))
	// Tampered state parameter.
	expectDenied(t, e.idp.Login(t, e.h, "corp", "", func(cb *url.URL, c []*http.Cookie) []*http.Cookie {
		q := cb.Query()
		q.Set("state", q.Get("state")+"x")
		cb.RawQuery = q.Encode()
		return c
	}), "invalid_state")
	// Missing state cookie (e.g. login CSRF: attacker's code in victim browser).
	expectDenied(t, e.idp.Login(t, e.h, "corp", "", func(*url.URL, []*http.Cookie) []*http.Cookie { return nil }), "invalid_state")
	// Forged state cookie.
	expectDenied(t, e.idp.Login(t, e.h, "corp", "", func(_ *url.URL, cs []*http.Cookie) []*http.Cookie {
		for _, c := range cs {
			if c.Name == oauth.StateCookie {
				c.Value = strings.Replace(c.Value, ".", "x.", 1)
			}
		}
		return cs
	}), "invalid_state")
	// State minted for another provider.
	expectDenied(t, e.idp.Login(t, e.h, "corp", "", func(cb *url.URL, c []*http.Cookie) []*http.Cookie {
		cb.Path = "/_auth/callback/other"
		return c
	}), "invalid_state")
	// Provider-side error.
	expectDenied(t, e.idp.Login(t, e.h, "corp", "", func(cb *url.URL, c []*http.Cookie) []*http.Cookie {
		cb.RawQuery = "error=access_denied&state=" + cb.Query().Get("state")
		return c
	}), "denied_by_provider")
	// Wrong PKCE verifier: replay the code with a state cookie from another login.
	var otherCookies []*http.Cookie
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "http://opennow.test/_auth/login/corp", nil))
	otherCookies = rec.Result().Cookies()
	expectDenied(t, e.idp.Login(t, e.h, "corp", "", func(cb *url.URL, _ []*http.Cookie) []*http.Cookie {
		// Use the other login's (valid, signed) state cookie and its state, so
		// only the PKCE verifier mismatches at the token endpoint.
		loc, _ := url.Parse(rec.Header().Get("Location"))
		q := cb.Query()
		q.Set("state", loc.Query().Get("state"))
		cb.RawQuery = q.Encode()
		return otherCookies
	}), "exchange_failed")
	// Unknown provider.
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "http://opennow.test/_auth/login/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider: %d", rec.Code)
	}
}

func TestOpenRedirectProtection(t *testing.T) {
	e := newEnv(t, config.AuthConfig{AllowAny: true}, p("corp", "oidc"))
	for _, bad := range []string{"https://evil.com/", "//evil.com", "/\\evil.com", "javascript:alert(1)", "evil.com", "/ok\r\nSet-Cookie: x=1", "http:/evil.com"} {
		rec := e.idp.Login(t, e.h, "corp", bad, nil)
		if loc := rec.Header().Get("Location"); loc != "/_admin/" {
			t.Fatalf("redirect %q -> %q", bad, loc)
		}
	}
	if got := oauth.SafeRedirect("/_admin/?tab=x#y", "/d"); got != "/_admin/?tab=x#y" {
		t.Fatalf("safe path rejected: %q", got)
	}
}

func TestGenericOAuth2Login(t *testing.T) {
	e := newEnv(t, config.AuthConfig{AllowedDomains: []string{"example.com"}}, p("gitea", "oauth2"))
	e.idp.Claims = map[string]any{"id": 7, "login": "bob", "email": "bob@example.com", "email_verified": true, "avatar_url": "https://img/b.png"}
	c := sessionFrom(t, e.idp.Login(t, e.h, "gitea", "", nil))
	if code, body := e.whoami(c); code != 200 || body != "oauth|gitea|bob|bob@example.com" {
		t.Fatalf("whoami: %d %s", code, body)
	}
}

func TestGenericOAuth2FieldMappingAndUnverifiedEmail(t *testing.T) {
	ac := config.AuthConfig{AllowedEmails: []string{"carol@example.com"}}
	e := newEnv(t, ac, func(i *oauthtest.IdP) config.ProviderConfig {
		pc := providerFor(i, "custom", "oauth2")
		pc.UserField, pc.EmailField = "data.handle", "data.mail"
		return pc
	})
	// No email_verified claim and TrustEmail=false: the email must not match.
	e.idp.Claims = map[string]any{"sub": "u9", "data": map[string]any{"handle": "carol", "mail": "carol@example.com"}}
	expectDenied(t, e.idp.Login(t, e.h, "custom", "", nil), "not_allowed")

	e2 := newEnv(t, ac, func(i *oauthtest.IdP) config.ProviderConfig {
		pc := providerFor(i, "custom", "oauth2")
		pc.UserField, pc.EmailField, pc.TrustEmail = "data.handle", "data.mail", true
		return pc
	})
	e2.idp.Claims = e.idp.Claims
	c := sessionFrom(t, e2.idp.Login(t, e2.h, "custom", "", nil))
	if code, body := e2.whoami(c); code != 200 || body != "oauth|custom|carol|carol@example.com" {
		t.Fatalf("whoami: %d %s", code, body)
	}
}

func TestGitHubLogin(t *testing.T) {
	e := newEnv(t, config.AuthConfig{AllowedUsers: []string{"github:ZX06"}}, p("github", "github"))
	c := sessionFrom(t, e.idp.Login(t, e.h, "github", "", nil))
	if code, body := e.whoami(c); code != 200 || body != "oauth|github|zx06|zx@example.com" {
		t.Fatalf("whoami: %d %s", code, body)
	}
	// Another GitHub user is denied.
	e.idp.GitHubUser = map[string]any{"id": 43, "login": "mallory"}
	expectDenied(t, e.idp.Login(t, e.h, "github", "", nil), "not_allowed")
}

func TestGitHubOrgAndEmails(t *testing.T) {
	e := newEnv(t, config.AuthConfig{AllowedGitHubOrgs: []string{"acme"}}, p("github", "github"))
	// Not a member yet / pending invitation.
	e.idp.GitHubOrgs["acme"] = "pending"
	expectDenied(t, e.idp.Login(t, e.h, "github", "", nil), "not_allowed")
	e.idp.GitHubOrgs["acme"] = "active"
	c := sessionFrom(t, e.idp.Login(t, e.h, "github", "", nil))
	if code, _ := e.whoami(c); code != 200 {
		t.Fatalf("org member denied: %d", code)
	}

	// Unverified primary email does not satisfy an email rule.
	e2 := newEnv(t, config.AuthConfig{AllowedEmails: []string{"zx@example.com"}}, p("github", "github"))
	e2.idp.GitHubEmails = []map[string]any{{"email": "zx@example.com", "primary": true, "verified": false}}
	expectDenied(t, e2.idp.Login(t, e2.h, "github", "", nil), "not_allowed")
}

func TestDenyByDefault(t *testing.T) {
	e := newEnv(t, config.AuthConfig{}, p("corp", "oidc"))
	expectDenied(t, e.idp.Login(t, e.h, "corp", "", nil), "not_allowed")
}

func TestAllowlistRecheckedPerRequest(t *testing.T) {
	idp := oauthtest.New(t)
	signer := auth.NewSigner(testSecret)
	mk := func(users ...string) (*oauth.Service, http.Handler) {
		cfg := config.Config{PublicURL: "https://opennow.test", AdminPrefix: "/_admin/", Auth: config.AuthConfig{AllowedUsers: users, Providers: []config.ProviderConfig{providerFor(idp, "corp", "oidc")}}}
		s, err := oauth.NewService(cfg, signer, nil)
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		s.Mount(mux)
		return s, mux
	}
	s1, h1 := mk("corp:alice")
	rec := idp.Login(t, h1, "corp", "", nil)
	c := sessionFrom(t, rec)
	if c == nil || !c.Secure {
		t.Fatalf("https public URL must set Secure: %+v", c)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	if s, _, _ := s1.Authenticate(req); s == nil {
		t.Fatal("session rejected")
	}
	s2, _ := mk("corp:bob")
	if s, _, _ := s2.Authenticate(req); s != nil {
		t.Fatal("removed user still authenticated")
	}
}

func TestAllowlist(t *testing.T) {
	a := oauth.NewAllowlist(config.AuthConfig{
		AllowedUsers:      []string{"github:zx06", "corp:00u123"},
		AllowedEmails:     []string{"Boss@Example.com"},
		AllowedDomains:    []string{"@team.io"},
		AllowedGitHubOrgs: []string{"Acme"},
	})
	yes := []oauth.Identity{
		{Provider: "github", Username: "ZX06"},
		{Provider: "corp", Subject: "00u123", Username: "someone"},
		{Provider: "x", Email: "boss@example.com", EmailVerified: true},
		{Provider: "x", Email: "dev@team.io", EmailVerified: true},
		{Provider: "github", Username: "m", Orgs: []string{"acme"}},
	}
	no := []oauth.Identity{
		{Provider: "gitlab", Username: "zx06"},     // provider-scoped
		{Provider: "x", Email: "boss@example.com"}, // unverified
		{Provider: "x", Email: "dev@team.io.evil.com", EmailVerified: true},
		{Provider: "x", Email: "dev@evilteam.io", EmailVerified: true},
		{Provider: "github", Username: "m", Orgs: []string{"other"}},
		{},
	}
	for _, id := range yes {
		if !a.Allows(id) {
			t.Errorf("should allow %+v", id)
		}
	}
	for _, id := range no {
		if a.Allows(id) {
			t.Errorf("should deny %+v", id)
		}
	}
	if oauth.NewAllowlist(config.AuthConfig{}).Allows(oauth.Identity{Provider: "github", Username: "a"}) {
		t.Error("empty allowlist must deny")
	}
	if !oauth.NewAllowlist(config.AuthConfig{AllowAny: true}).Allows(oauth.Identity{Provider: "github", Username: "a"}) {
		t.Error("allow any")
	}
}

func TestBearerAndAPIKeySessions(t *testing.T) {
	signer := auth.NewSigner(testSecret)
	mk := func(key string, disable bool) *oauth.Service {
		s, err := oauth.NewService(config.Config{APIKey: key, Auth: config.AuthConfig{DisableAPIKeyLogin: disable}}, signer, nil)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := mk("k", false)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer k")
	if sess, ambient, err := s.Authenticate(r); err != nil || sess == nil || sess.Method != "bearer" || ambient {
		t.Fatalf("bearer: %+v %v %v", sess, ambient, err)
	}
	r.Header.Set("Authorization", "Bearer wrong")
	if _, _, err := s.Authenticate(r); err != oauth.ErrBadBearer {
		t.Fatalf("wrong bearer: %v", err)
	}

	rec := httptest.NewRecorder()
	if err := s.IssueSession(rec, auth.Session{Method: "apikey"}); err != nil {
		t.Fatal(err)
	}
	c := rec.Result().Cookies()[0]
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	if sess, ambient, _ := s.Authenticate(r); sess == nil || !ambient {
		t.Fatal("api key session rejected")
	}
	if sess, _, _ := mk("rotated", false).Authenticate(r); sess != nil {
		t.Fatal("session survived API key rotation")
	}
	if sess, _, _ := mk("k", true).Authenticate(r); sess != nil {
		t.Fatal("api key session accepted with API key login disabled")
	}
	// A forged "oauth" session (signed with another secret) is rejected.
	other, _ := oauth.NewService(config.Config{APIKey: "k"}, auth.NewSigner("another-secret-another-secret-00"), nil)
	rec = httptest.NewRecorder()
	_ = other.IssueSession(rec, auth.Session{Method: "oauth", Provider: "github", Username: "zx06"})
	r = httptest.NewRequest("GET", "/", nil)
	r.AddCookie(rec.Result().Cookies()[0])
	if sess, _, _ := s.Authenticate(r); sess != nil {
		t.Fatal("forged session accepted")
	}
}

func TestCSRF(t *testing.T) {
	s, _ := oauth.NewService(config.Config{PublicURL: "https://pub.example.com"}, auth.NewSigner(testSecret), nil)
	mk := func(method string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest(method, "http://admin.example.com/api/v1/sites/x", nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		method string
		hdr    map[string]string
		ok     bool
	}{
		{"GET", nil, true},
		{"POST", nil, false},
		{"DELETE", map[string]string{"X-OpenNow-CSRF": "1"}, true},
		{"POST", map[string]string{"X-OpenNow-CSRF": "1", "Origin": "http://admin.example.com", "Sec-Fetch-Site": "same-origin"}, true},
		{"POST", map[string]string{"X-OpenNow-CSRF": "1", "Origin": "https://pub.example.com"}, true},
		{"POST", map[string]string{"X-OpenNow-CSRF": "1", "Origin": "https://evil.com"}, false},
		{"POST", map[string]string{"X-OpenNow-CSRF": "1", "Origin": "null"}, false},
		{"PATCH", map[string]string{"X-OpenNow-CSRF": "1", "Sec-Fetch-Site": "cross-site"}, false},
		{"PUT", map[string]string{"X-OpenNow-CSRF": "1", "Sec-Fetch-Site": "same-site"}, false},
	}
	for i, c := range cases {
		if got := s.CheckCSRF(mk(c.method, c.hdr)); got != c.ok {
			t.Errorf("case %d (%s %v): got %v", i, c.method, c.hdr, got)
		}
	}
}

func TestCloudflareAccess(t *testing.T) {
	idp := oauthtest.New(t)
	mk := func(cf config.CFAccessConfig, ac config.AuthConfig) *oauth.Service {
		ac.CFAccess = cf
		s, err := oauth.NewService(config.Config{Auth: ac}, auth.NewSigner(testSecret), nil)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	cf := config.CFAccessConfig{TeamDomain: idp.URL(), AUD: "aud-tag"}
	claims := func(mut func(map[string]any)) map[string]any {
		c := map[string]any{"iss": idp.URL(), "aud": []string{"aud-tag"}, "sub": "cf-1", "email": "ops@corp.com", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
		if mut != nil {
			mut(c)
		}
		return c
	}
	req := func(jwt, email string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Cf-Access-Jwt-Assertion", jwt)
		if email != "" {
			r.Header.Set("Cf-Access-Authenticated-User-Email", email)
		}
		return r
	}
	s := mk(cf, config.AuthConfig{AllowedDomains: []string{"corp.com"}})
	sess, ambient, _ := s.Authenticate(req(idp.SignJWT(claims(nil), false), "ops@corp.com"))
	if sess == nil || sess.Method != "cf-access" || sess.Email != "ops@corp.com" || !ambient {
		t.Fatalf("cf access: %+v", sess)
	}
	bad := map[string]*http.Request{
		"other key":       req(idp.SignJWT(claims(nil), true), ""),
		"wrong aud":       req(idp.SignJWT(claims(func(c map[string]any) { c["aud"] = "x" }), false), ""),
		"wrong iss":       req(idp.SignJWT(claims(func(c map[string]any) { c["iss"] = "https://evil.cloudflareaccess.com" }), false), ""),
		"expired":         req(idp.SignJWT(claims(func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() }), false), ""),
		"header spoof":    req(idp.SignJWT(claims(nil), false), "boss@corp.com"),
		"service token":   req(idp.SignJWT(claims(func(c map[string]any) { delete(c, "email") }), false), ""),
		"not allowlisted": req(idp.SignJWT(claims(func(c map[string]any) { c["email"] = "x@gmail.com" }), false), ""),
		"garbage":         req("a.b.c", ""),
	}
	for name, r := range bad {
		if sess, _, _ := s.Authenticate(r); sess != nil {
			t.Errorf("%s accepted", name)
		}
	}
	// CF policy as the allowlist.
	cf.AllowAny = true
	s = mk(cf, config.AuthConfig{})
	if sess, _, _ := s.Authenticate(req(idp.SignJWT(claims(func(c map[string]any) { c["email"] = "x@gmail.com" }), false), "")); sess == nil {
		t.Error("cf allow any")
	}
	if got := oauth.CFAccessIssuer("myteam"); got != "https://myteam.cloudflareaccess.com" {
		t.Error(got)
	}
	if got := oauth.CFAccessIssuer("myteam.cloudflareaccess.com/"); got != "https://myteam.cloudflareaccess.com" {
		t.Error(got)
	}
}

func TestProvidersEndpointAndConfigErrors(t *testing.T) {
	e := newEnv(t, config.AuthConfig{AllowAny: true, DisableAPIKeyLogin: true}, p("github", "github"), p("corp", "oidc"))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/auth/providers", nil))
	b := rec.Body.String()
	if !strings.Contains(b, `"name":"github"`) || !strings.Contains(b, `"display_name":"GitHub"`) || !strings.Contains(b, `"api_key_login":false`) || strings.Contains(b, "secret") {
		t.Fatalf("providers: %s", b)
	}
	// Providers without a public URL are a startup error.
	_, err := oauth.NewService(config.Config{Auth: config.AuthConfig{Providers: []config.ProviderConfig{{Name: "g", Type: "github", ClientID: "x"}}}}, auth.NewSigner(testSecret), nil)
	if err == nil {
		t.Fatal("expected error without public URL")
	}
}
