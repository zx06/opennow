package server

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/zx06/opennow/internal/config"
	"github.com/zx06/opennow/internal/storage/local"
)

func TestSoleRootHTMLFallback(t *testing.T) {
	b, err := local.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d, err := b.CreateRevision(ctx, "solo")
	if err != nil {
		t.Fatal(err)
	}
	body := "<h1>only</h1>"
	if err := b.Upload(ctx, d.ID, "only.html", strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	// Bypass finalize normalize: write version without index by using finalize
	// then deleting index.html from the version dir to simulate a legacy site.
	v, _, err := b.Finalize(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Remove the copied index.html so serve-time fallback is exercised.
	verFiles, err := b.ListVersionFiles(ctx, "solo", v.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasIndex := false
	for _, f := range verFiles {
		if f.Path == "index.html" {
			hasIndex = true
		}
	}
	if !hasIndex {
		t.Fatal("finalize should have copied index.html")
	}

	cfg := config.Config{APIKey: "k", PathMode: true, AdminPrefix: "/_admin/", Secret: "test-secret-0123456789abcdef0123456789"}
	srv, err := New(cfg, b, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, h: srv.Handler(), store: b}

	rec := f.do("GET", "/solo/", "")
	if rec.Code != 200 {
		t.Fatalf("with index: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "only") {
		t.Fatalf("body %q", rec.Body)
	}

	// Simulate legacy publish: remove index.html from disk under current version.
	filesRoot := b.Root + "/sites/solo/versions/" + v.ID
	if err := os.Remove(filesRoot + "/index.html"); err != nil {
		t.Fatal(err)
	}

	rec = f.do("GET", "/solo/", "")
	if rec.Code != 200 {
		t.Fatalf("fallback: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "only") {
		t.Fatalf("fallback body %q", rec.Body)
	}
}
