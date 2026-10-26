package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zx06/opennow/internal/config"
	"github.com/zx06/opennow/internal/storage/local"
)

func newUploadTest(t *testing.T) (*Handler, *http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := local.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		APIKey:      "k",
		DataDir:     dir,
		PublicURL:   "http://127.0.0.1",
		AdminPrefix: "/_admin/",
	}
	h := New(cfg, store, nil)
	mux := http.NewServeMux()
	h.Mount(mux)
	return h, mux, dir
}

func loginCookie(t *testing.T, mux *http.ServeMux) *http.Cookie {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"api_key":"k"}`))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("login %d %s", rr.Code, rr.Body.String())
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("no session")
	return nil
}

func TestChunkedUpload_AssemblesAndPublishes(t *testing.T) {
	h, mux, _ := newUploadTest(t)
	ck := loginCookie(t, mux)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/publish", strings.NewReader(`{"slug":"big"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("create %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		RevisionID string `json:"revision_id"`
		ChunkSize  int    `json:"chunk_size"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created.ChunkSize != DefaultUploadChunkBytes {
		t.Fatalf("chunk_size %d", created.ChunkSize)
	}

	// 700 bytes in 256-byte chunks
	payload := bytes.Repeat([]byte("abcdefghij"), 70) // 700
	chunk := 256
	var offset int64
	for offset < int64(len(payload)) {
		end := offset + int64(chunk)
		if end > int64(len(payload)) {
			end = int64(len(payload))
		}
		part := payload[offset:end]
		rr = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPut, "/api/v1/publish/"+created.RevisionID+"/files/sre.html", bytes.NewReader(part))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set(hdrUploadOffset, strconv.FormatInt(offset, 10))
		req.Header.Set(hdrUploadTotal, strconv.Itoa(len(payload)))
		req.ContentLength = int64(len(part))
		req.AddCookie(ck)
		mux.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Fatalf("chunk @%d: %d %s", offset, rr.Code, rr.Body.String())
		}
		var resp map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &resp)
		offset = end
		if offset < int64(len(payload)) {
			if resp["status"] != "partial" {
				t.Fatalf("want partial got %v", resp)
			}
		} else if resp["status"] != "ok" {
			t.Fatalf("want ok got %v", resp)
		}
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/publish/"+created.RevisionID+"/finalize", nil)
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("finalize %d %s", rr.Code, rr.Body.String())
	}

	rc, _, err := h.store.OpenCurrentFile(t.Context(), "big", "sre.html")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, payload) {
		t.Fatalf("content mismatch len=%d want=%d", len(got), len(payload))
	}
}

func TestWholeFileUpload_StillWorks(t *testing.T) {
	h, mux, _ := newUploadTest(t)
	ck := loginCookie(t, mux)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/publish", strings.NewReader(`{"slug":"small"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	var created struct {
		RevisionID string `json:"revision_id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	body := []byte("<h1>hi</h1>")
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/publish/"+created.RevisionID+"/files/index.html", bytes.NewReader(body))
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("upload %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/publish/"+created.RevisionID+"/finalize", nil)
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	rc, _, err := h.store.OpenCurrentFile(t.Context(), "small", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, body) {
		t.Fatalf("%q", got)
	}
}

func TestChunkOutOfOrderRejected(t *testing.T) {
	_, mux, dir := newUploadTest(t)
	ck := loginCookie(t, mux)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/publish", strings.NewReader(`{"slug":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	var created struct {
		RevisionID string `json:"revision_id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// skip first chunk — offset 100 with empty file should fail
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/publish/"+created.RevisionID+"/files/a.bin", bytes.NewReader([]byte("abcd")))
	req.Header.Set(hdrUploadOffset, "100")
	req.Header.Set(hdrUploadTotal, "200")
	req.AddCookie(ck)
	mux.ServeHTTP(rr, req)
	if rr.Code == 200 {
		t.Fatalf("expected error, got %s", rr.Body.String())
	}
	// no orphan huge dirs
	_ = os.RemoveAll(filepath.Join(dir, "upload-parts"))
}

func TestParseContentRange(t *testing.T) {
	off, tot, ok, err := parseContentRange("bytes 0-255/1000")
	if err != nil || !ok || off != 0 || tot != 1000 {
		t.Fatalf("%v %v %v %v", off, tot, ok, err)
	}
}
