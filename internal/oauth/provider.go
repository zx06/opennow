// Package oauth implements admin login with OAuth2 / OpenID Connect providers
// (authorization-code flow with PKCE, state and nonce), an identity allowlist,
// signed session cookies and optional Cloudflare Access identity verification.
package oauth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/zx06/opennow/internal/config"
)

// Identity is the normalised user returned by a provider.
type Identity struct {
	Provider      string   `json:"provider"`
	Subject       string   `json:"subject"`
	Username      string   `json:"username"`
	Email         string   `json:"email,omitempty"`
	EmailVerified bool     `json:"email_verified,omitempty"`
	Name          string   `json:"name,omitempty"`
	Avatar        string   `json:"avatar,omitempty"`
	Orgs          []string `json:"orgs,omitempty"`
}

// Provider is one configured login provider.
type Provider interface {
	Name() string
	DisplayName() string
	Type() string
	// AuthCodeURL returns the authorization endpoint URL for a new login.
	AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error)
	// Exchange redeems code (with the PKCE verifier) and returns the
	// authenticated identity. For OIDC the ID token is fully verified,
	// including the nonce.
	Exchange(ctx context.Context, code, verifier, nonce string) (*Identity, error)
}

// NewProvider builds a provider from its configuration. redirectURL is the
// absolute callback URL. orgs lists GitHub organisations whose membership
// should be checked (GitHub providers only).
func NewProvider(pc config.ProviderConfig, redirectURL string, client *http.Client, orgs []string) (Provider, error) {
	if client == nil {
		client = http.DefaultClient
	}
	b := base{cfg: pc, client: client, oc: oauth2.Config{
		ClientID:     pc.ClientID,
		ClientSecret: pc.ClientSecret,
		RedirectURL:  redirectURL,
		Scopes:       pc.Scopes,
	}}
	switch pc.Type {
	case "github":
		return newGitHub(b, orgs), nil
	case "oidc":
		if len(b.oc.Scopes) == 0 {
			b.oc.Scopes = []string{oidc.ScopeOpenID, "profile", "email"}
		} else if !contains(b.oc.Scopes, oidc.ScopeOpenID) {
			b.oc.Scopes = append([]string{oidc.ScopeOpenID}, b.oc.Scopes...)
		}
		return &oidcProvider{base: b}, nil
	case "oauth2":
		b.oc.Endpoint = oauth2.Endpoint{AuthURL: pc.AuthURL, TokenURL: pc.TokenURL}
		return &genericProvider{base: b}, nil
	}
	return nil, fmt.Errorf("unknown provider type %q", pc.Type)
}

type base struct {
	cfg    config.ProviderConfig
	client *http.Client
	oc     oauth2.Config
}

func (b *base) Name() string { return b.cfg.Name }
func (b *base) Type() string { return b.cfg.Type }
func (b *base) DisplayName() string {
	if b.cfg.DisplayName != "" {
		return b.cfg.DisplayName
	}
	switch b.cfg.Type {
	case "github":
		return "GitHub"
	}
	return b.cfg.Name
}

func (b *base) ctx(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, b.client)
}

func (b *base) exchange(ctx context.Context, oc *oauth2.Config, code, verifier string) (*oauth2.Token, error) {
	tok, err := oc.Exchange(b.ctx(ctx), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("token exchange: empty access token")
	}
	return tok, nil
}

// getJSON performs an authenticated GET and decodes a JSON response.
func (b *base) getJSON(ctx context.Context, tok *oauth2.Token, url string, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	tok.SetAuthHeader(req)
	resp, err := b.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			return resp.StatusCode, fmt.Errorf("GET %s: %w", url, err)
		}
	}
	return resp.StatusCode, nil
}

func pkceOpts(verifier string, extra ...oauth2.AuthCodeOption) []oauth2.AuthCodeOption {
	return append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}, extra...)
}

// ---------- OIDC ----------

type oidcProvider struct {
	base
	mu       sync.Mutex
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oc2      oauth2.Config
}

// discover lazily performs OIDC discovery so that an unreachable issuer does
// not prevent the server from starting; failures are retried on next use.
func (p *oidcProvider) discover(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != nil {
		return nil
	}
	pr, err := oidc.NewProvider(oidc.ClientContext(ctx, p.client), p.cfg.Issuer)
	if err != nil {
		return fmt.Errorf("oidc discovery %s: %w", p.cfg.Issuer, err)
	}
	p.provider = pr
	// The key set fetches keys with the client stored in this context; it
	// must outlive the request.
	p.verifier = pr.VerifierContext(oidc.ClientContext(context.Background(), p.client), &oidc.Config{ClientID: p.cfg.ClientID})
	p.oc2 = p.oc
	p.oc2.Endpoint = pr.Endpoint()
	if p.cfg.AuthURL != "" {
		p.oc2.Endpoint.AuthURL = p.cfg.AuthURL
	}
	if p.cfg.TokenURL != "" {
		p.oc2.Endpoint.TokenURL = p.cfg.TokenURL
	}
	return nil
}

func (p *oidcProvider) AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	if err := p.discover(ctx); err != nil {
		return "", err
	}
	return p.oc2.AuthCodeURL(state, pkceOpts(verifier, oidc.Nonce(nonce))...), nil
}

func (p *oidcProvider) Exchange(ctx context.Context, code, verifier, nonce string) (*Identity, error) {
	if err := p.discover(ctx); err != nil {
		return nil, err
	}
	tok, err := p.exchange(ctx, &p.oc2, code, verifier)
	if err != nil {
		return nil, err
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, errors.New("oidc: token response has no id_token")
	}
	// Verifies signature (JWKS), iss, aud and exp.
	idt, err := p.verifier.Verify(p.ctx(ctx), raw)
	if err != nil {
		return nil, fmt.Errorf("oidc: %w", err)
	}
	if nonce == "" || subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		return nil, errors.New("oidc: nonce mismatch")
	}
	claims := map[string]any{}
	if err := idt.Claims(&claims); err != nil {
		return nil, fmt.Errorf("oidc: claims: %w", err)
	}
	// Fill missing profile claims from the userinfo endpoint (e.g. when the
	// IdP keeps the ID token minimal). The sub must match the ID token.
	if p.provider.UserInfoEndpoint() != "" && (claimString(claims, p.userField()) == "" || claimString(claims, p.emailField()) == "") {
		if ui, err := p.provider.UserInfo(p.ctx(ctx), oauth2.StaticTokenSource(tok)); err == nil && ui.Subject == idt.Subject {
			extra := map[string]any{}
			if ui.Claims(&extra) == nil {
				for k, v := range extra {
					if _, ok := claims[k]; !ok {
						claims[k] = v
					}
				}
			}
		}
	}
	id := mapClaims(p.cfg, claims, idt.Subject)
	if id.Subject == "" {
		return nil, errors.New("oidc: id token has no sub")
	}
	return id, nil
}

func (p *oidcProvider) userField() string {
	return firstNonEmpty(p.cfg.UserField, "preferred_username")
}
func (p *oidcProvider) emailField() string { return firstNonEmpty(p.cfg.EmailField, "email") }

// ---------- generic OAuth2 ----------

type genericProvider struct{ base }

func (p *genericProvider) AuthCodeURL(_ context.Context, state, _ string, verifier string) (string, error) {
	return p.oc.AuthCodeURL(state, pkceOpts(verifier)...), nil
}

func (p *genericProvider) Exchange(ctx context.Context, code, verifier, _ string) (*Identity, error) {
	tok, err := p.exchange(ctx, &p.oc, code, verifier)
	if err != nil {
		return nil, err
	}
	claims := map[string]any{}
	if _, err := p.getJSON(ctx, tok, p.cfg.UserInfoURL, &claims); err != nil {
		return nil, fmt.Errorf("userinfo: %w", err)
	}
	sub := claimString(claims, "sub")
	if sub == "" {
		sub = claimString(claims, "id")
	}
	id := mapClaims(p.cfg, claims, sub)
	if id.Subject == "" && id.Username == "" {
		return nil, errors.New("userinfo: no user identifier (sub/id or USER_FIELD)")
	}
	if id.Subject == "" {
		id.Subject = id.Username
	}
	return id, nil
}

// mapClaims converts a claim set into an Identity using the provider's field
// mapping. The username falls back through common claim names to sub.
func mapClaims(pc config.ProviderConfig, c map[string]any, sub string) *Identity {
	id := &Identity{Provider: pc.Name, Subject: sub}
	userFields := []string{"preferred_username", "login", "username", "nickname"}
	if pc.UserField != "" {
		userFields = []string{pc.UserField}
	}
	for _, f := range userFields {
		if id.Username = claimString(c, f); id.Username != "" {
			break
		}
	}
	if id.Username == "" {
		id.Username = sub
	}
	id.Email = strings.TrimSpace(claimString(c, firstNonEmpty(pc.EmailField, "email")))
	if id.Email != "" {
		if v, ok := c["email_verified"]; ok {
			id.EmailVerified = truthy(v)
		} else {
			id.EmailVerified = pc.TrustEmail
		}
	}
	id.Name = claimString(c, firstNonEmpty(pc.NameField, "name"))
	if pc.AvatarField != "" {
		id.Avatar = claimString(c, pc.AvatarField)
	} else {
		id.Avatar = firstNonEmpty(claimString(c, "picture"), claimString(c, "avatar_url"))
	}
	return id
}

// claimString reads a (dot-separated, possibly nested) claim as a string.
func claimString(c map[string]any, key string) string {
	if key == "" {
		return ""
	}
	var cur any = c
	for _, part := range strings.Split(key, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		if cur, ok = m[part]; !ok {
			return ""
		}
	}
	switch v := cur.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true")
	}
	return false
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
