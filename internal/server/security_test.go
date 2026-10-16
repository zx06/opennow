package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zx06/opennow/internal/auth"
	"github.com/zx06/opennow/internal/config"
	"github.com/zx06/opennow/internal/storage/local"
)

type fixture struct {
	t      *testing.T
	h      http.Handler
	store  *local.Backend
	admin  *http.Cookie
	v1, v2 string
}

func publish(t *testing.T, b *local.Backend, slug, body string) string {
	t.Helper()
	ctx := context.Background()
	d, err := b.CreateRevision(ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Upload(ctx, d.ID, "index.html", strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	v, _, err := b.Finalize(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	b, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{APIKey: "k", PathMode: true, AdminPrefix: "/_admin/", Secret: "test-secret-0123456789abcdef0123456789"}
	srv, err := New(cfg, b, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, h: srv.Handler(), store: b}
	// v2 is published first, then v1 so that v1 is current (mirrors the box demo).
	f.v2 = publish(t, b, "hello", "<h1>v2</h1>")
	f.v1 = publish(t, b, "hello", "<h1>v1</h1>")
	rec := f.do("POST", "/api/v1/auth/login", `{"api_key":"k"}`)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "opennow_session" {
			f.admin = c
		}
	}
	if f.admin == nil {
		t.Fatal("no admin session")
	}
	return f
}

func (f *fixture) do(method, target, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1"+target, r)
	req.Header.Set("X-OpenNow-CSRF", "1")
	if strings.HasPrefix(body, "{") {
		req.Header.Set("Content-Type", "application/json")
	} else if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) setPassword(pw string) {
	rec := f.do("PATCH", "/api/v1/sites/hello", `{"password":"`+pw+`"}`, f.admin)
	if rec.Code != 200 {
		f.t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "password_hash") || strings.Contains(rec.Body.String(), "$2a$") {
		f.t.Fatalf("PATCH leaked password hash: %s", rec.Body)
	}
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestForgedUnlockCookieRejected(t *testing.T) {
	f := newFixture(t)
	f.setPassword("s3cret")
	for _, v := range []string{"1", "9999999999.deadbeef", ""} {
		rec := f.do("GET", "/hello/", "", &http.Cookie{Name: auth.SiteAuthCookieName("hello"), Value: v})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("forged cookie %q: got %d", v, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "v1") {
			t.Fatalf("forged cookie %q leaked content", v)
		}
	}
}

func TestPasswordFormFlow(t *testing.T) {
	f := newFixture(t)
	f.setPassword("s3cret")
	if rec := f.do("POST", "/hello/", "password=wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	rec := f.do("POST", "/hello/", "password=s3cret")
	c := cookieNamed(rec, auth.SiteAuthCookieName("hello"))
	if c == nil || c.Value == "1" || c.Path != "/hello/" || !c.HttpOnly {
		t.Fatalf("bad unlock cookie: %+v", c)
	}
	rec = f.do("GET", "/hello/", "", c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "v1") {
		t.Fatalf("signed cookie: %d %s", rec.Code, rec.Body)
	}
	// Changing the password invalidates existing cookies.
	f.setPassword("other")
	if rec := f.do("GET", "/hello/", "", c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale cookie accepted: %d", rec.Code)
	}
}

func TestUnlockAPICookiePath(t *testing.T) {
	f := newFixture(t)
	f.setPassword("s3cret")
	rec := f.do("POST", "/api/v1/sites/hello/unlock", `{"password":"s3cret"}`)
	c := cookieNamed(rec, auth.SiteAuthCookieName("hello"))
	if rec.Code != 200 || c == nil || c.Path != "/hello/" || c.Value == "1" {
		t.Fatalf("unlock: %d %+v", rec.Code, c)
	}
	if rec := f.do("GET", "/hello/", "", c); rec.Code != 200 {
		t.Fatalf("unlock cookie rejected: %d", rec.Code)
	}
}

func TestGetSiteHidesPasswordHash(t *testing.T) {
	f := newFixture(t)
	f.setPassword("s3cret")
	rec := f.do("GET", "/api/v1/sites/hello", "", f.admin)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	b := rec.Body.String()
	if strings.Contains(b, "password_hash") || strings.Contains(b, "$2a$") || !strings.Contains(b, `"has_password":true`) {
		t.Fatalf("GET leaked or missing: %s", b)
	}
}

func TestPreviewHistoricalVersion(t *testing.T) {
	f := newFixture(t)
	// Unauthenticated preview is denied.
	if rec := f.do("GET", "/_preview/hello/"+f.v2+"/", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon preview: %d", rec.Code)
	}
	// Admin session can preview.
	rec := f.do("GET", "/_preview/hello/"+f.v2+"/", "", f.admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "v2") {
		t.Fatalf("admin preview: %d %s", rec.Code, rec.Body)
	}
	// Current version unchanged.
	if rec := f.do("GET", "/hello/", ""); !strings.Contains(rec.Body.String(), "v1") {
		t.Fatalf("current changed: %s", rec.Body)
	}
	// Signed URL from versions API works without admin session.
	rec = f.do("GET", "/api/v1/sites/hello/versions", "", f.admin)
	var out struct {
		Versions []struct {
			ID         string `json:"id"`
			Current    bool   `json:"current"`
			FileCount  int    `json:"file_count"`
			PreviewURL string `json:"preview_url"`
		} `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Versions) != 2 {
		t.Fatalf("versions: %v %s", err, rec.Body)
	}
	var signed string
	for _, v := range out.Versions {
		if v.ID == f.v2 {
			if v.Current || v.FileCount != 1 {
				t.Fatalf("v2 view: %+v", v)
			}
			signed = v.PreviewURL
		} else if !v.Current {
			t.Fatalf("v1 should be current: %+v", v)
		}
	}
	u, _ := url.Parse(signed)
	rec = f.do("GET", u.RequestURI(), "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "v2") {
		t.Fatalf("signed preview: %d %s", rec.Code, rec.Body)
	}
	pc := cookieNamed(rec, auth.PreviewCookieName)
	if pc == nil || pc.Path != "/_preview/hello/"+f.v2+"/" {
		t.Fatalf("preview cookie: %+v", pc)
	}
	// Token is bound to its version.
	if rec := f.do("GET", "/_preview/hello/"+f.v1+"/?"+u.RawQuery, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token reused for other version: %d", rec.Code)
	}
	// Tampered token rejected.
	if rec := f.do("GET", "/_preview/hello/"+f.v2+"/?token=9999999999.00", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered token: %d", rec.Code)
	}
	// Version files endpoint.
	rec = f.do("GET", "/api/v1/sites/hello/versions/"+f.v2+"/files", "", f.admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "index.html") {
		t.Fatalf("version files: %d %s", rec.Code, rec.Body)
	}
	// Path traversal in version id.
	if rec := f.do("GET", "/_preview/hello/..%2F..%2F/index.html", "", f.admin); rec.Code == 200 {
		t.Fatalf("traversal served: %d", rec.Code)
	}
}
