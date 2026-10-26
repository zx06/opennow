package oauth

import (
	"strings"

	"github.com/zx06/opennow/internal/config"
)

// Allowlist decides which authenticated identities may use the admin.
// It denies everyone when no rule is configured, unless AllowAny is set.
type Allowlist struct {
	users   map[string]bool // "<provider>:<username|subject>" (lowercase)
	emails  map[string]bool
	domains map[string]bool
	orgs    map[string]bool
	any     bool
}

// NewAllowlist builds an allowlist from the auth configuration.
func NewAllowlist(ac config.AuthConfig) *Allowlist {
	a := &Allowlist{users: map[string]bool{}, emails: map[string]bool{}, domains: map[string]bool{}, orgs: map[string]bool{}, any: ac.AllowAny}
	for _, u := range ac.AllowedUsers {
		a.users[strings.ToLower(strings.TrimSpace(u))] = true
	}
	for _, e := range ac.AllowedEmails {
		a.emails[strings.ToLower(strings.TrimSpace(e))] = true
	}
	for _, d := range ac.AllowedDomains {
		a.domains[strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@"))] = true
	}
	for _, o := range ac.AllowedGitHubOrgs {
		a.orgs[strings.ToLower(strings.TrimSpace(o))] = true
	}
	return a
}

// Empty reports whether no rule is configured.
func (a *Allowlist) Empty() bool {
	return len(a.users) == 0 && len(a.emails) == 0 && len(a.domains) == 0 && len(a.orgs) == 0
}

// Allows reports whether id may log in. Email and domain rules only match a
// verified email address. Organisation rules only match identities whose
// provider reported the membership (GitHub).
func (a *Allowlist) Allows(id Identity) bool {
	if a.any {
		return true
	}
	p := strings.ToLower(id.Provider)
	if id.Username != "" && a.users[p+":"+strings.ToLower(id.Username)] {
		return true
	}
	if id.Subject != "" && a.users[p+":"+strings.ToLower(id.Subject)] {
		return true
	}
	if id.Email != "" && id.EmailVerified {
		email := strings.ToLower(id.Email)
		if a.emails[email] {
			return true
		}
		if at := strings.LastIndex(email, "@"); at > 0 && a.domains[email[at+1:]] {
			return true
		}
	}
	for _, o := range id.Orgs {
		if a.orgs[strings.ToLower(o)] {
			return true
		}
	}
	return false
}

// OrgsFor returns the subset of orgs configured in the allowlist (kept in the
// session so that the allowlist can be re-evaluated on every request).
func (a *Allowlist) OrgsFor(orgs []string) []string {
	var out []string
	for _, o := range orgs {
		if a.orgs[strings.ToLower(o)] {
			out = append(out, strings.ToLower(o))
		}
	}
	return out
}
