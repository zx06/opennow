package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zx06/opennow/internal/config"
)

func TestHealthz(t *testing.T) {
	s := New(config.Config{AdminPrefix: "/_admin/"}, nil, http.NotFoundHandler())
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
