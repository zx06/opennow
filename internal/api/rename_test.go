package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPatchSiteRename(t *testing.T) {
	_, mux, _ := newUploadTest(t)
	ck := loginCookie(t, mux)

	// publish via API
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/publish", strings.NewReader(`{"slug":"alpha"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("create %d %s", rr.Code, rr.Body)
	}
	var created struct {
		RevisionID string `json:"revision_id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	body := "<h1>a</h1>"
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/publish/"+created.RevisionID+"/files/index.html", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = int64(len(body))
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("upload %d %s", rr.Code, rr.Body)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/publish/"+created.RevisionID+"/finalize", nil)
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("finalize %d %s", rr.Code, rr.Body)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/sites/alpha", strings.NewReader(`{"slug":"beta"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("rename %d %s", rr.Code, rr.Body)
	}
	var out struct {
		Meta struct {
			Slug string `json:"slug"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out.Meta.Slug != "beta" {
		t.Fatalf("slug %q", out.Meta.Slug)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sites/alpha", nil)
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	if rr.Code != 404 {
		t.Fatalf("old slug still exists: %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/sites/beta", strings.NewReader(`{"slug":"../x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Fatalf("invalid slug want 400 got %d %s", rr.Code, rr.Body)
	}
}
