package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zx06/opennow/internal/config"
	"github.com/zx06/opennow/internal/storage/local"
)

func publishFiles(t *testing.T, b *local.Backend, slug string, files map[string]string) string {
	t.Helper()
	ctx := context.Background()
	d, err := b.CreateRevision(ctx, slug)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := b.Upload(ctx, d.ID, name, strings.NewReader(body), int64(len(body))); err != nil {
			t.Fatal(err)
		}
	}
	v, _, err := b.Finalize(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}

func mdServer(t *testing.T) (*Server, *local.Backend) {
	t.Helper()
	b, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		APIKey:      "k",
		PathMode:    true,
		AdminPrefix: "/_admin/",
		Secret:      "test-secret-0123456789abcdef0123456789",
	}
	return New(cfg, b, http.NotFoundHandler()), b
}

func TestServeMarkdownFile(t *testing.T) {
	srv, b := mdServer(t)
	publishFiles(t, b, "docs", map[string]string{
		"guide.md": "# Guide\n\nHello **world**.\n\n<script>alert(1)</script>\n",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://127.0.0.1/docs/guide.md", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<strong>world</strong>") {
		t.Fatalf("missing rendered markdown: %s", body)
	}
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Fatalf("script not sanitized: %s", body)
	}
	if !strings.Contains(body, "<title>Guide</title>") {
		t.Fatalf("missing title: %s", body)
	}
}

func TestServeMarkdownIndexFallback(t *testing.T) {
	srv, b := mdServer(t)
	publishFiles(t, b, "mdsite", map[string]string{
		"README.md": "# Readme Home\n\nwelcome\n",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://127.0.0.1/mdsite/", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("ct %q", rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "Readme Home") {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestIndexHTMLPreferredOverMarkdown(t *testing.T) {
	srv, b := mdServer(t)
	publishFiles(t, b, "both", map[string]string{
		"index.html": "<h1>HTML wins</h1>",
		"index.md":   "# MD should not win\n",
		"README.md":  "# README no\n",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://127.0.0.1/both/", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "HTML wins") {
		t.Fatalf("expected html index: %s", body)
	}
	if strings.Contains(body, "MD should not win") {
		t.Fatalf("markdown took precedence: %s", body)
	}
}

func TestIndexMdBeforeReadme(t *testing.T) {
	srv, b := mdServer(t)
	publishFiles(t, b, "order", map[string]string{
		"index.md":  "# Index MD\n",
		"README.md": "# README MD\n",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://127.0.0.1/order/", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Index MD") {
		t.Fatalf("expected index.md: %s", body)
	}
	if strings.Contains(body, "README MD") {
		t.Fatalf("README took precedence: %s", body)
	}
}

func TestNestedMarkdownIndex(t *testing.T) {
	srv, b := mdServer(t)
	publishFiles(t, b, "nest", map[string]string{
		"docs/index.md": "# Nested Docs\n",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://127.0.0.1/nest/docs/", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Nested Docs") {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestHTMLUnchanged(t *testing.T) {
	srv, b := mdServer(t)
	publishFiles(t, b, "plain", map[string]string{
		"index.html": "<h1>still html</h1>",
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://127.0.0.1/plain/", nil)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if string(body) != "<h1>still html</h1>" {
		t.Fatalf("html mutated: %q", body)
	}
}
