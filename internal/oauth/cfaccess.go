package oauth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/zx06/opennow/internal/config"
)

// CFAccess verifies the Cf-Access-Jwt-Assertion header that Cloudflare Access
// adds to proxied requests (RS256 JWT signed by the team's keys at
// https://<team>.cloudflareaccess.com/cdn-cgi/access/certs, aud = the
// application AUD tag).
type CFAccess struct {
	issuer   string
	verifier *oidc.IDTokenVerifier
	allowAny bool
}

// CFAccessIssuer normalises a team domain ("myteam",
// "myteam.cloudflareaccess.com" or a full https URL) into the issuer URL.
func CFAccessIssuer(team string) string {
	team = strings.TrimRight(strings.TrimSpace(team), "/")
	if strings.Contains(team, "://") {
		return team
	}
	if !strings.Contains(team, ".") {
		team += ".cloudflareaccess.com"
	}
	return "https://" + team
}

// NewCFAccess returns nil when Cloudflare Access is not configured.
func NewCFAccess(c config.CFAccessConfig, client *http.Client) *CFAccess {
	if !c.Enabled() {
		return nil
	}
	if client == nil {
		client = http.DefaultClient
	}
	iss := CFAccessIssuer(c.TeamDomain)
	ks := oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), iss+"/cdn-cgi/access/certs")
	return &CFAccess{
		issuer:   iss,
		verifier: oidc.NewVerifier(iss, ks, &oidc.Config{ClientID: c.AUD, SupportedSigningAlgs: []string{oidc.RS256}}),
		allowAny: c.AllowAny,
	}
}

// Identify verifies the request's Access JWT. It returns (nil, nil) when the
// request carries no Access assertion.
func (c *CFAccess) Identify(r *http.Request) (*Identity, error) {
	raw := r.Header.Get("Cf-Access-Jwt-Assertion")
	if raw == "" {
		return nil, nil
	}
	idt, err := c.verifier.Verify(r.Context(), raw)
	if err != nil {
		return nil, err
	}
	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := idt.Claims(&claims); err != nil {
		return nil, err
	}
	if claims.Email == "" {
		// Service tokens carry no email; they are not admin users.
		return nil, errors.New("cf access: token has no email")
	}
	if h := r.Header.Get("Cf-Access-Authenticated-User-Email"); h != "" && !strings.EqualFold(h, claims.Email) {
		return nil, errors.New("cf access: email header does not match token")
	}
	return &Identity{
		Provider:      "cloudflare",
		Subject:       idt.Subject,
		Username:      claims.Email,
		Email:         claims.Email,
		EmailVerified: true,
		Name:          claims.Name,
	}, nil
}
