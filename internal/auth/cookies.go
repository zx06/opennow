// Package auth contains helpers for site unlock cookies and signed preview
// tokens. All values are HMAC-SHA256 signed with a server-side secret so that
// clients cannot forge them.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// SiteAuthCookieName is the cookie that marks a password-protected site as unlocked.
func SiteAuthCookieName(slug string) string {
	return "opennow_site_" + slug
}

// PreviewCookieName is the cookie that carries a preview token, scoped to the
// preview path of a single version.
const PreviewCookieName = "opennow_preview"

// Signer creates and verifies HMAC-signed values.
type Signer struct {
	key []byte
}

// NewSigner returns a signer for the given secret. An empty secret yields a
// random, process-local key (values will not survive restarts).
func NewSigner(secret string) *Signer {
	if secret == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		return &Signer{key: b}
	}
	return &Signer{key: []byte(secret)}
}

func (s *Signer) mac(parts ...string) string {
	m := hmac.New(sha256.New, s.key)
	for _, p := range parts {
		m.Write([]byte(strconv.Itoa(len(p))))
		m.Write([]byte{':'})
		m.Write([]byte(p))
		m.Write([]byte{'|'})
	}
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Signer) sign(exp time.Time, parts ...string) string {
	e := strconv.FormatInt(exp.Unix(), 10)
	return e + "." + s.mac(append(parts, e)...)
}

func (s *Signer) verify(value string, now time.Time, parts ...string) bool {
	e, sig, ok := strings.Cut(value, ".")
	if !ok || e == "" || sig == "" {
		return false
	}
	exp, err := strconv.ParseInt(e, 10, 64)
	if err != nil || now.Unix() > exp {
		return false
	}
	want := s.mac(append(parts, e)...)
	return hmac.Equal([]byte(sig), []byte(want))
}

// SiteCookieValue signs an unlock cookie for slug. The password hash is part of
// the MAC so changing (or clearing) the password invalidates old cookies.
func (s *Signer) SiteCookieValue(slug, passwordHash string, exp time.Time) string {
	return s.sign(exp, "site", slug, passwordHash)
}

// VerifySiteCookie checks a value produced by SiteCookieValue.
func (s *Signer) VerifySiteCookie(value, slug, passwordHash string, now time.Time) bool {
	return s.verify(value, now, "site", slug, passwordHash)
}

// PreviewToken signs access to one version of one site.
func (s *Signer) PreviewToken(slug, versionID string, exp time.Time) string {
	return s.sign(exp, "preview", slug, versionID)
}

// VerifyPreviewToken checks a value produced by PreviewToken.
func (s *Signer) VerifyPreviewToken(token, slug, versionID string, now time.Time) bool {
	return s.verify(token, now, "preview", slug, versionID)
}

// ErrNoSecret is returned when no secret could be loaded or created.
var ErrNoSecret = errors.New("no server secret")
