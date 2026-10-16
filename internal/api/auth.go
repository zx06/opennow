package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const sessionCookie = "opennow_session"

type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]time.Time)}
}

func (s *sessionStore) create() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[id] = time.Now().Add(7 * 24 * time.Hour)
	s.mu.Unlock()
	return id, nil
}

func (s *sessionStore) valid(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[id]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, id)
		return false
	}
	return true
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

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
