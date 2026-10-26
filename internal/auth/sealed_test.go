package auth

import (
	"strings"
	"testing"
	"time"
)

func TestSealOpenRoundTrip(t *testing.T) {
	s := NewSigner("secret-secret-secret-secret-0000")
	now := time.Now()
	v, err := s.SealJSON("session", Session{Method: "oauth", Username: "zx06"}, now.Add(time.Hour), "bind")
	if err != nil {
		t.Fatal(err)
	}
	var out Session
	if err := s.OpenJSON("session", v, now, &out, "bind"); err != nil || out.Username != "zx06" {
		t.Fatalf("open: %v %+v", err, out)
	}
	// Wrong kind, wrong binding, other key, expired, tampered payload.
	if s.OpenJSON("state", v, now, &out, "bind") == nil {
		t.Fatal("kind not bound")
	}
	if s.OpenJSON("session", v, now, &out, "other") == nil {
		t.Fatal("extra not bound")
	}
	if NewSigner("another-secret-another-secret-00").OpenJSON("session", v, now, &out, "bind") == nil {
		t.Fatal("key not checked")
	}
	if s.OpenJSON("session", v, now.Add(2*time.Hour), &out, "bind") == nil {
		t.Fatal("expiry not checked")
	}
	parts := strings.Split(v, ".")
	forged, _ := s.SealJSON("x", Session{Method: "oauth", Username: "attacker"}, now.Add(time.Hour))
	tampered := strings.Split(forged, ".")[0] + "." + parts[1] + "." + parts[2]
	if s.OpenJSON("session", tampered, now, &out, "bind") == nil {
		t.Fatal("payload not bound")
	}
	for _, bad := range []string{"", "a.b", "a.b.c.d", "..", v + "x"} {
		if s.OpenJSON("session", bad, now, &out, "bind") == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
