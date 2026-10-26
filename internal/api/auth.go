package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie    = "opennow_session"
	oauthStateCookie = "opennow_github_oauth_state"
	sessionTTL       = 7 * 24 * time.Hour
	oauthStateTTL    = 10 * time.Minute
)

type session struct {
	exp    time.Time
	login  string // GitHub login when method=github; empty for API key
	method string // "apikey" | "github"
}

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]session
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]session)}
}

func (s *sessionStore) create(method, login string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[id] = session{
		exp:    time.Now().Add(sessionTTL),
		login:  login,
		method: method,
	}
	s.mu.Unlock()
	return id, nil
}

func (s *sessionStore) get(id string) (session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return session{}, false
	}
	if time.Now().After(sess.exp) {
		delete(s.sessions, id)
		return session{}, false
	}
	return sess, true
}

func (s *sessionStore) valid(id string) bool {
	_, ok := s.get(id)
	return ok
}

func (s *sessionStore) delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || !h.sessions.valid(c.Value) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
		return false
	}
	return true
}

func (h *Handler) secureCookies() bool {
	return strings.HasPrefix(strings.ToLower(h.cfg.PublicURL), "https://")
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, sid string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL / time.Second),
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.secureCookies(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
