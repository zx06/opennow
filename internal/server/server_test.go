package server

import (
	"net/http/httptest"
	"testing"

	"github.com/zx06/opennow/internal/config"
)

func TestResolveSubdomain(t *testing.T) {
	cfg := config.Config{SubdomainMode: true, BaseDomain: "xuzhuo.xyz", PathMode: true}
	s := &Server{cfg: cfg}
	r := httptest.NewRequest("GET", "http://hello.xuzhuo.xyz/page.html", nil)
	slug, path, mode, ok := s.resolveSite(r)
	if !ok || slug != "hello" || path != "/page.html" || mode != modeSubdomain {
		t.Fatalf("got %q %q %v %v", slug, path, mode, ok)
	}
}

func TestResolvePath(t *testing.T) {
	cfg := config.Config{PathMode: true}
	s := &Server{cfg: cfg}
	r := httptest.NewRequest("GET", "http://localhost/demo/a.html", nil)
	slug, path, mode, ok := s.resolveSite(r)
	if !ok || slug != "demo" || path != "/a.html" || mode != modePath {
		t.Fatalf("got %q %q %v %v", slug, path, mode, ok)
	}
}
