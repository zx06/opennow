package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// ProviderConfig describes one OAuth2 / OIDC login provider.
type ProviderConfig struct {
	// Name is the URL-safe identifier used in /_auth/login/<name> and
	// /_auth/callback/<name>, and as the prefix of OPENNOW_OAUTH_ALLOWED_USERS
	// entries ("github:zx06").
	Name string `json:"name"`
	// Type is "github", "oidc" or "oauth2".
	Type         string   `json:"type"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Issuer       string   `json:"issuer,omitempty"`
	AuthURL      string   `json:"auth_url,omitempty"`
	TokenURL     string   `json:"token_url,omitempty"`
	UserInfoURL  string   `json:"userinfo_url,omitempty"`
	APIURL       string   `json:"api_url,omitempty"` // GitHub (Enterprise) REST API base
	Scopes       []string `json:"scopes,omitempty"`
	DisplayName  string   `json:"display_name,omitempty"`
	UserField    string   `json:"user_field,omitempty"`
	EmailField   string   `json:"email_field,omitempty"`
	NameField    string   `json:"name_field,omitempty"`
	AvatarField  string   `json:"avatar_field,omitempty"`
	// TrustEmail treats the email claim as verified when the provider does
	// not send email_verified (e.g. Microsoft Entra ID, many OAuth2 APIs).
	TrustEmail bool `json:"trust_email,omitempty"`
}

// CFAccessConfig enables trusting Cloudflare Access identities.
type CFAccessConfig struct {
	TeamDomain string `json:"team_domain,omitempty"`
	AUD        string `json:"aud,omitempty"`
	// AllowAny trusts every identity Cloudflare Access lets through (the
	// Access policy is the allowlist). Otherwise the opennow allowlist applies.
	AllowAny bool `json:"allow_any,omitempty"`
}

// Enabled reports whether Cloudflare Access verification is configured.
func (c CFAccessConfig) Enabled() bool { return c.TeamDomain != "" && c.AUD != "" }

// AuthConfig holds admin login settings.
type AuthConfig struct {
	Providers         []ProviderConfig `json:"providers,omitempty"`
	AllowedUsers      []string         `json:"allowed_users,omitempty"`
	AllowedEmails     []string         `json:"allowed_emails,omitempty"`
	AllowedDomains    []string         `json:"allowed_domains,omitempty"`
	AllowedGitHubOrgs []string         `json:"allowed_github_orgs,omitempty"`
	AllowAny          bool             `json:"allow_any,omitempty"`
	// SessionTTL is the lifetime of the signed admin session cookie.
	SessionTTL time.Duration `json:"-"`
	// DisableAPIKeyLogin turns off logging in to the web admin with
	// OPENNOW_API_KEY (OPENNOW_ADMIN_API_KEY_LOGIN=false). Bearer API key
	// access for the CLI / API is unaffected.
	DisableAPIKeyLogin bool           `json:"-"`
	CFAccess           CFAccessConfig `json:"cf_access,omitempty"`
	// BaseURL overrides OPENNOW_PUBLIC_URL as the origin of the OAuth
	// callback (OPENNOW_OAUTH_BASE_URL), e.g. when the admin lives on
	// OPENNOW_ADMIN_HOST.
	BaseURL string `json:"base_url,omitempty"`
	// DenyMessage is shown in the admin UI when login is rejected by the allowlist
	// (OPENNOW_OAUTH_DENY_MESSAGE).
	DenyMessage string `json:"deny_message,omitempty"`
}

// DefaultSessionTTL is used when OPENNOW_SESSION_TTL is not set.
const DefaultSessionTTL = 7 * 24 * time.Hour

var providerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// authFile is the JSON shape of OPENNOW_OAUTH_CONFIG.
type authFile struct {
	AuthConfig
	SessionTTL  string `json:"session_ttl,omitempty"`
	APIKeyLogin *bool  `json:"api_key_login,omitempty"`
}

// AuthFromEnv reads login settings from OPENNOW_OAUTH_CONFIG (optional JSON
// file) and OPENNOW_OAUTH_* / OPENNOW_CF_ACCESS_* environment variables.
// Environment values extend (lists, providers) or override (scalars) the file.
func AuthFromEnv(getenv func(string) string) (AuthConfig, error) {
	ac := AuthConfig{SessionTTL: DefaultSessionTTL}
	if p := getenv("OPENNOW_OAUTH_CONFIG"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return ac, fmt.Errorf("OPENNOW_OAUTH_CONFIG: %w", err)
		}
		var f authFile
		if err := json.Unmarshal(b, &f); err != nil {
			return ac, fmt.Errorf("OPENNOW_OAUTH_CONFIG: %w", err)
		}
		ttl := ac.SessionTTL
		ac = f.AuthConfig
		ac.SessionTTL = ttl
		if f.SessionTTL != "" {
			d, err := time.ParseDuration(f.SessionTTL)
			if err != nil {
				return ac, fmt.Errorf("session_ttl: %w", err)
			}
			ac.SessionTTL = d
		}
		if f.APIKeyLogin != nil {
			ac.DisableAPIKeyLogin = !*f.APIKeyLogin
		}
	}

	for _, name := range splitList(getenv("OPENNOW_OAUTH_PROVIDERS")) {
		name = strings.ToLower(name)
		up := "OPENNOW_OAUTH_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_"
		get := func(k string) string { return strings.TrimSpace(getenv(up + k)) }
		pc := ProviderConfig{
			Name:         name,
			Type:         strings.ToLower(get("TYPE")),
			ClientID:     get("CLIENT_ID"),
			ClientSecret: get("CLIENT_SECRET"),
			Issuer:       get("ISSUER"),
			AuthURL:      get("AUTH_URL"),
			TokenURL:     get("TOKEN_URL"),
			UserInfoURL:  get("USERINFO_URL"),
			APIURL:       get("API_URL"),
			Scopes:       splitScopes(get("SCOPES")),
			DisplayName:  get("DISPLAY_NAME"),
			UserField:    get("USER_FIELD"),
			EmailField:   get("EMAIL_FIELD"),
			NameField:    get("NAME_FIELD"),
			AvatarField:  get("AVATAR_FIELD"),
			TrustEmail:   parseBool(get("TRUST_EMAIL"), false),
		}
		if pc.Type == "" {
			// Convenience: a provider literally named "github" or "oidc".
			switch name {
			case "github", "oidc":
				pc.Type = name
			}
		}
		ac.Providers = append(ac.Providers, pc)
	}

	ac.AllowedUsers = append(ac.AllowedUsers, splitList(getenv("OPENNOW_OAUTH_ALLOWED_USERS"))...)
	ac.AllowedEmails = append(ac.AllowedEmails, splitList(getenv("OPENNOW_OAUTH_ALLOWED_EMAILS"))...)
	ac.AllowedDomains = append(ac.AllowedDomains, splitList(getenv("OPENNOW_OAUTH_ALLOWED_DOMAINS"))...)
	ac.AllowedGitHubOrgs = append(ac.AllowedGitHubOrgs, splitList(getenv("OPENNOW_OAUTH_ALLOWED_GITHUB_ORGS"))...)
	ac.AllowAny = parseBool(getenv("OPENNOW_OAUTH_ALLOW_ANY"), ac.AllowAny)
	ac.DisableAPIKeyLogin = !parseBool(getenv("OPENNOW_ADMIN_API_KEY_LOGIN"), !ac.DisableAPIKeyLogin)
	if v := getenv("OPENNOW_SESSION_TTL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return ac, fmt.Errorf("OPENNOW_SESSION_TTL: %w", err)
		}
		ac.SessionTTL = d
	}
	if v := getenv("OPENNOW_OAUTH_BASE_URL"); v != "" {
		ac.BaseURL = strings.TrimRight(strings.TrimSpace(v), "/")
	}
	if v := getenv("OPENNOW_CF_ACCESS_TEAM_DOMAIN"); v != "" {
		ac.CFAccess.TeamDomain = strings.TrimSpace(v)
	}
	if v := getenv("OPENNOW_CF_ACCESS_AUD"); v != "" {
		ac.CFAccess.AUD = strings.TrimSpace(v)
	}
	ac.CFAccess.AllowAny = parseBool(getenv("OPENNOW_CF_ACCESS_ALLOW_ANY"), ac.CFAccess.AllowAny)

	if v := strings.TrimSpace(getenv("OPENNOW_OAUTH_DENY_MESSAGE")); v != "" {
		ac.DenyMessage = v
	}
	return ac, ac.Validate()
}

// Validate checks provider definitions for obvious mistakes.
func (ac AuthConfig) Validate() error {
	if ac.SessionTTL < 0 {
		return fmt.Errorf("session TTL must be positive")
	}
	seen := map[string]bool{}
	for _, p := range ac.Providers {
		if !providerNameRe.MatchString(p.Name) {
			return fmt.Errorf("oauth provider %q: name must match %s", p.Name, providerNameRe)
		}
		if seen[p.Name] {
			return fmt.Errorf("oauth provider %q defined twice", p.Name)
		}
		seen[p.Name] = true
		if p.ClientID == "" {
			return fmt.Errorf("oauth provider %q: CLIENT_ID is required", p.Name)
		}
		switch p.Type {
		case "github":
		case "oidc":
			if p.Issuer == "" {
				return fmt.Errorf("oauth provider %q: ISSUER is required for type oidc", p.Name)
			}
		case "oauth2":
			if p.AuthURL == "" || p.TokenURL == "" || p.UserInfoURL == "" {
				return fmt.Errorf("oauth provider %q: AUTH_URL, TOKEN_URL and USERINFO_URL are required for type oauth2", p.Name)
			}
		case "":
			return fmt.Errorf("oauth provider %q: TYPE is required (github|oidc|oauth2)", p.Name)
		default:
			return fmt.Errorf("oauth provider %q: unknown TYPE %q", p.Name, p.Type)
		}
	}
	for _, u := range ac.AllowedUsers {
		if p, v, ok := strings.Cut(u, ":"); !ok || p == "" || v == "" {
			return fmt.Errorf("OPENNOW_OAUTH_ALLOWED_USERS entry %q must look like <provider>:<username>", u)
		}
	}
	if (ac.CFAccess.TeamDomain == "") != (ac.CFAccess.AUD == "") {
		return fmt.Errorf("OPENNOW_CF_ACCESS_TEAM_DOMAIN and OPENNOW_CF_ACCESS_AUD must be set together")
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitScopes(s string) []string {
	return splitList(s)
}

func parseBool(v string, def bool) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}
