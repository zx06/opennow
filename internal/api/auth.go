package api

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/zx06/opennow/internal/auth"
	"github.com/zx06/opennow/internal/oauth"
)

// principal resolves the caller and enforces CSRF protection for
// state-changing requests that use ambient (cookie / Cloudflare Access)
// credentials. It writes the error response itself.
func (h *Handler) principal(w http.ResponseWriter, r *http.Request) (*auth.Session, bool) {
	sess, ambient, err := h.authn.Authenticate(r)
	if errors.Is(err, oauth.ErrBadBearer) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="opennow"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "API 密钥无效"})
		return nil, false
	}
	if sess == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录"})
		return nil, false
	}
	if ambient && !h.authn.CheckCSRF(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "CSRF 校验失败：缺少 " + oauth.CSRFHeader + " 请求头或来源不一致"})
		return nil, false
	}
	return sess, true
}

func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	_, ok := h.principal(w, r)
	return ok
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
