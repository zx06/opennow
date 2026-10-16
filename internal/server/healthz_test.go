package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zx06/opennow/internal/config"
)

func TestHealthz(t *testing.T) {
	s, err := New(config.Config{AdminPrefix: "/_admin/", Secret: "test-secret-0123456789abcdef0123456789"}, nil, http.NotFoundHandler())
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if body := rr.Body.String(); body != "ok\n" {
		t.Fatalf("body %q", body)
	}
}
