package auth

import (
	"testing"
	"time"
)

func TestSiteCookie(t *testing.T) {
	s := NewSigner("secret")
	now := time.Now()
	v := s.SiteCookieValue("a", "h", now.Add(time.Hour))
	if !s.VerifySiteCookie(v, "a", "h", now) {
		t.Fatal("valid cookie rejected")
	}
	cases := map[string]bool{
		"wrong slug":   s.VerifySiteCookie(v, "b", "h", now),
		"wrong hash":   s.VerifySiteCookie(v, "a", "x", now),
		"expired":      s.VerifySiteCookie(v, "a", "h", now.Add(2*time.Hour)),
		"other secret": NewSigner("other").VerifySiteCookie(v, "a", "h", now),
		"literal 1":    s.VerifySiteCookie("1", "a", "h", now),
	}
	for name, ok := range cases {
		if ok {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestPreviewToken(t *testing.T) {
	s := NewSigner("secret")
	now := time.Now()
	tok := s.PreviewToken("a", "v1", now.Add(time.Hour))
	if !s.VerifyPreviewToken(tok, "a", "v1", now) {
		t.Fatal("valid token rejected")
	}
	if s.VerifyPreviewToken(tok, "a", "v2", now) || s.VerifySiteCookie(tok, "a", "v1", now) {
		t.Fatal("token accepted out of context")
	}
}
