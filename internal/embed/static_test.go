package embed

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminHandler_IndexVersionAndCFAsync(t *testing.T) {
	h := AdminHandler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Fatalf("Cache-Control %q", cc)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-cfasync="false"`) {
		t.Fatal("missing data-cfasync")
	}
	if !strings.Contains(body, "app.js?v=") || !strings.Contains(body, "admin.css?v=") {
		t.Fatalf("missing version query in %s", body[:200])
	}
}

func TestAdminHandler_AssetCache(t *testing.T) {
	h := AdminHandler()
	// Get version from index
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rr.Body.String()
	i := strings.Index(body, "app.js?v=")
	if i < 0 {
		t.Fatal("no ver")
	}
	ver := body[i+len("app.js?v="):]
	ver = strings.Split(ver, `"`)[0]

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/app.js?v="+ver, nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	if !strings.Contains(rr.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("Cache-Control %q", rr.Header().Get("Cache-Control"))
	}
}
