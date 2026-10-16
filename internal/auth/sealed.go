package auth

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidSealed is returned when a sealed value is malformed, expired, or
// carries a bad signature.
var ErrInvalidSealed = errors.New("invalid or expired signed value")

// SealJSON serialises v as JSON and returns "<payload>.<exp>.<mac>", where the
// MAC binds kind (domain separation), the payload, the expiry and any extra
// binding values. The payload is only signed, not encrypted: never put secrets
// in it.
func (s *Signer) SealJSON(kind string, v any, exp time.Time, extra ...string) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	p := base64.RawURLEncoding.EncodeToString(b)
	e := strconv.FormatInt(exp.Unix(), 10)
	return p + "." + e + "." + s.mac(append([]string{"sealed", kind, p, e}, extra...)...), nil
}

// OpenJSON verifies a value produced by SealJSON (same kind and extra values)
// and decodes its payload into v.
func (s *Signer) OpenJSON(kind, value string, now time.Time, v any, extra ...string) error {
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return ErrInvalidSealed
	}
	want := s.mac(append([]string{"sealed", kind, parts[0], parts[1]}, extra...)...)
	if !hmac.Equal([]byte(parts[2]), []byte(want)) {
		return ErrInvalidSealed
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || now.Unix() > exp {
		return ErrInvalidSealed
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ErrInvalidSealed
	}
	if err := json.Unmarshal(b, v); err != nil {
		return ErrInvalidSealed
	}
	return nil
}

// Derive returns a deterministic secret value (64 hex chars) bound to the
// server key and parts. Used for values that must not be stored client-side,
// such as the PKCE code_verifier, which is derived from the OAuth state.
func (s *Signer) Derive(parts ...string) string {
	return s.mac(append([]string{"derive"}, parts...)...)
}

// Session is the identity stored in the signed admin session cookie.
type Session struct {
	// Method is how the user authenticated: "apikey", "oauth", "cf-access"
	// or "bearer" (the last two are never stored in a cookie).
	Method   string   `json:"m"`
	Provider string   `json:"p,omitempty"`
	Subject  string   `json:"s,omitempty"`
	Username string   `json:"u,omitempty"`
	Name     string   `json:"n,omitempty"`
	Email    string   `json:"e,omitempty"`
	Verified bool     `json:"ev,omitempty"`
	Avatar   string   `json:"a,omitempty"`
	Orgs     []string `json:"o,omitempty"`
	IssuedAt int64    `json:"iat"`
}
