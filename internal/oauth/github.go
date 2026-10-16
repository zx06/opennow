package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/oauth2"
)

// GitHub is not an OIDC provider for user login, so the preset uses the
// classic OAuth app flow and the REST API (/user, /user/emails and, when
// OPENNOW_OAUTH_ALLOWED_GITHUB_ORGS is set, /user/memberships/orgs/<org>).
type githubProvider struct {
	base
	api  string
	orgs []string
}

func newGitHub(b base, orgs []string) *githubProvider {
	if b.cfg.AuthURL == "" {
		b.cfg.AuthURL = "https://github.com/login/oauth/authorize"
	}
	if b.cfg.TokenURL == "" {
		b.cfg.TokenURL = "https://github.com/login/oauth/access_token"
	}
	api := strings.TrimRight(b.cfg.APIURL, "/")
	if api == "" {
		api = "https://api.github.com"
	}
	b.oc.Endpoint = oauth2.Endpoint{AuthURL: b.cfg.AuthURL, TokenURL: b.cfg.TokenURL, AuthStyle: oauth2.AuthStyleInParams}
	if len(b.oc.Scopes) == 0 {
		b.oc.Scopes = []string{"read:user", "user:email"}
		if len(orgs) > 0 {
			b.oc.Scopes = append(b.oc.Scopes, "read:org")
		}
	}
	return &githubProvider{base: b, api: api, orgs: orgs}
}

func (p *githubProvider) AuthCodeURL(_ context.Context, state, _ string, verifier string) (string, error) {
	return p.oc.AuthCodeURL(state, pkceOpts(verifier, oauth2.SetAuthURLParam("allow_signup", "false"))...), nil
}

func (p *githubProvider) Exchange(ctx context.Context, code, verifier, _ string) (*Identity, error) {
	tok, err := p.exchange(ctx, &p.oc, code, verifier)
	if err != nil {
		return nil, err
	}
	var u struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if _, err := p.getJSON(ctx, tok, p.api+"/user", &u); err != nil {
		return nil, fmt.Errorf("github user: %w", err)
	}
	if u.ID == 0 || u.Login == "" {
		return nil, errors.New("github user: missing id/login")
	}
	id := &Identity{
		Provider: p.cfg.Name,
		Subject:  strconv.FormatInt(u.ID, 10),
		Username: u.Login,
		Name:     u.Name,
		Avatar:   u.AvatarURL,
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if _, err := p.getJSON(ctx, tok, p.api+"/user/emails", &emails); err == nil {
		for _, e := range emails {
			if e.Primary && e.Verified {
				id.Email, id.EmailVerified = e.Email, true
			}
		}
	}
	for _, org := range p.orgs {
		var m struct {
			State string `json:"state"`
		}
		status, err := p.getJSON(ctx, tok, p.api+"/user/memberships/orgs/"+url.PathEscape(org), &m)
		if err == nil && m.State == "active" {
			id.Orgs = append(id.Orgs, strings.ToLower(org))
		} else if err != nil && status != http.StatusNotFound && status != http.StatusForbidden {
			return nil, fmt.Errorf("github org %s: %w", org, err)
		}
	}
	return id, nil
}
