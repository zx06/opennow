// Package oauthtest provides an in-process fake OAuth2 / OIDC / GitHub
// identity provider for tests. It implements discovery, JWKS, the
// authorization endpoint (auto-approving), a PKCE-checking token endpoint,
// userinfo and the GitHub REST endpoints used by opennow.
package oauthtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

// IdP is a fake identity provider.
type IdP struct {
	Server       *httptest.Server
	ClientID     string
	ClientSecret string

	// Claims are returned in the ID token and from /userinfo (OIDC and
	// generic OAuth2). "sub" defaults to "user-1".
	Claims map[string]any
	// UserInfoOnly moves all profile claims out of the ID token (only
	// iss/sub/aud/exp/iat/nonce remain) to exercise the userinfo merge.
	UserInfoOnly bool

	// GitHub API data.
	GitHubUser   map[string]any
	GitHubEmails []map[string]any
	GitHubOrgs   map[string]string // org -> membership state

	// Tampering knobs for negative tests.
	BadSignature  bool
	WrongNonce    bool
	WrongAudience bool
	WrongIssuer   bool
	Expired       bool
	OmitIDToken   bool

	mu    sync.Mutex
	codes map[string]codeInfo
	key   *rsa.PrivateKey
	other *rsa.PrivateKey
	t     testing.TB
}

type codeInfo struct {
	clientID, redirect, challenge, method, nonce string
}

// New starts a fake IdP; it is closed with the test.
func New(t testing.TB) *IdP {
	t.Helper()
	k1, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &IdP{
		ClientID: "client-123", ClientSecret: "secret-456",
		Claims:       map[string]any{"sub": "user-1", "preferred_username": "alice", "email": "alice@example.com", "email_verified": true, "name": "Alice", "picture": "https://img.example.com/a.png"},
		GitHubUser:   map[string]any{"id": 42, "login": "zx06", "name": "ZX", "avatar_url": "https://avatars.githubusercontent.com/u/42"},
		GitHubEmails: []map[string]any{{"email": "zx@example.com", "primary": true, "verified": true}},
		GitHubOrgs:   map[string]string{},
		codes:        map[string]codeInfo{}, key: k1, other: k2, t: t,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("GET /jwks", f.jwks)
	mux.HandleFunc("GET /cdn-cgi/access/certs", f.jwks)
	mux.HandleFunc("GET /authorize", f.authorize)
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("GET /userinfo", f.userinfo)
	mux.HandleFunc("GET /login/oauth/authorize", f.authorize)
	mux.HandleFunc("POST /login/oauth/access_token", f.token)
	mux.HandleFunc("GET /api/user", f.ghUser)
	mux.HandleFunc("GET /api/user/emails", f.ghEmails)
	mux.HandleFunc("GET /api/user/memberships/orgs/{org}", f.ghOrg)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Server.Close)
	return f
}

// URL is the issuer / base URL.
func (f *IdP) URL() string { return f.Server.URL }

func (f *IdP) discovery(w http.ResponseWriter, r *http.Request) {
	u := f.URL()
	writeJSON(w, map[string]any{
		"issuer":                                u,
		"authorization_endpoint":                u + "/authorize",
		"token_endpoint":                        u + "/token",
		"userinfo_endpoint":                     u + "/userinfo",
		"jwks_uri":                              u + "/jwks",
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (f *IdP) jwks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
}

func (f *IdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != f.ClientID || q.Get("response_type") != "code" || q.Get("redirect_uri") == "" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	code := randString()
	f.mu.Lock()
	f.codes[code] = codeInfo{clientID: q.Get("client_id"), redirect: q.Get("redirect_uri"), challenge: q.Get("code_challenge"), method: q.Get("code_challenge_method"), nonce: q.Get("nonce")}
	f.mu.Unlock()
	cb, _ := url.Parse(q.Get("redirect_uri"))
	cq := cb.Query()
	cq.Set("code", code)
	cq.Set("state", q.Get("state"))
	cb.RawQuery = cq.Encode()
	http.Redirect(w, r, cb.String(), http.StatusFound)
}

func (f *IdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	id, sec, ok := r.BasicAuth()
	if !ok {
		id, sec = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != f.ClientID || sec != f.ClientSecret {
		tokenError(w, "invalid_client")
		return
	}
	code := r.PostForm.Get("code")
	f.mu.Lock()
	ci, ok := f.codes[code]
	delete(f.codes, code) // single use
	f.mu.Unlock()
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != ci.redirect {
		tokenError(w, "invalid_grant")
		return
	}
	// PKCE (RFC 7636) S256 is mandatory in this fake.
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if ci.method != "S256" || ci.challenge == "" || base64.RawURLEncoding.EncodeToString(sum[:]) != ci.challenge {
		tokenError(w, "invalid_grant")
		return
	}
	resp := map[string]any{"access_token": "at-" + randString(), "token_type": "Bearer", "expires_in": 3600}
	if !f.OmitIDToken {
		claims := map[string]any{}
		if !f.UserInfoOnly {
			for k, v := range f.Claims {
				claims[k] = v
			}
		}
		claims["sub"] = f.sub()
		claims["iss"] = f.URL()
		claims["aud"] = f.ClientID
		claims["iat"] = time.Now().Unix()
		claims["exp"] = time.Now().Add(5 * time.Minute).Unix()
		claims["nonce"] = ci.nonce
		if f.WrongNonce {
			claims["nonce"] = "not-the-nonce"
		}
		if f.WrongAudience {
			claims["aud"] = "someone-else"
		}
		if f.WrongIssuer {
			claims["iss"] = "https://evil.example.com"
		}
		if f.Expired {
			claims["exp"] = time.Now().Add(-time.Hour).Unix()
			claims["iat"] = time.Now().Add(-2 * time.Hour).Unix()
		}
		resp["id_token"] = f.sign(claims, f.BadSignature)
	}
	writeJSON(w, resp)
}

func (f *IdP) sub() string {
	if s, ok := f.Claims["sub"].(string); ok && s != "" {
		return s
	}
	return "user-1"
}

func (f *IdP) bearerOK(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	return strings.HasPrefix(h, "Bearer at-") || strings.HasPrefix(h, "token at-")
}

func (f *IdP) userinfo(w http.ResponseWriter, r *http.Request) {
	if !f.bearerOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	c := map[string]any{}
	for k, v := range f.Claims {
		c[k] = v
	}
	c["sub"] = f.sub()
	writeJSON(w, c)
}

func (f *IdP) ghUser(w http.ResponseWriter, r *http.Request) {
	if !f.bearerOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, f.GitHubUser)
}

func (f *IdP) ghEmails(w http.ResponseWriter, r *http.Request) {
	if !f.bearerOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, f.GitHubEmails)
}

func (f *IdP) ghOrg(w http.ResponseWriter, r *http.Request) {
	if !f.bearerOK(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	st, ok := f.GitHubOrgs[r.PathValue("org")]
	if !ok {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"state": st, "role": "member"})
}

// SignJWT signs claims with the IdP key (published in /jwks and
// /cdn-cgi/access/certs). With otherKey it uses a key that is not published.
func (f *IdP) SignJWT(claims map[string]any, otherKey bool) string { return f.sign(claims, otherKey) }

func (f *IdP) sign(claims map[string]any, otherKey bool) string {
	key := f.key
	if otherKey {
		key = f.other
	}
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1", Algorithm: "RS256"}}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		f.t.Fatal(err)
	}
	b, _ := json.Marshal(claims)
	obj, err := s.Sign(b)
	if err != nil {
		f.t.Fatal(err)
	}
	out, err := obj.CompactSerialize()
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func tokenError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func randString() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Login drives a full browser login against handler h: GET
// /_auth/login/<provider>, follow the redirect to the fake IdP's authorize
// endpoint, then deliver the callback (with the state cookie) to h. mutate,
// when non-nil, may alter the callback URL and cookies before delivery.
func (f *IdP) Login(t testing.TB, h http.Handler, provider, redirect string, mutate func(cb *url.URL, cookies []*http.Cookie) []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	target := "/_auth/login/" + provider
	if redirect != "" {
		target += "?redirect=" + url.QueryEscape(redirect)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://opennow.test"+target, nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login start: %d %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	authURL := rec.Header().Get("Location")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %d", resp.StatusCode)
	}
	cb, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		cookies = mutate(cb, cookies)
	}
	req := httptest.NewRequest(http.MethodGet, "http://opennow.test"+cb.RequestURI(), nil)
	for _, c := range cookies {
		if c.MaxAge >= 0 && c.Value != "" {
			req.AddCookie(c)
		}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
