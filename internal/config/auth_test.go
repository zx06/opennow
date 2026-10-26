package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestAuthFromEnv(t *testing.T) {
	ac, err := AuthFromEnv(envMap(map[string]string{
		"OPENNOW_OAUTH_PROVIDERS":             "github, Keycloak",
		"OPENNOW_OAUTH_GITHUB_CLIENT_ID":      "gh-id",
		"OPENNOW_OAUTH_GITHUB_CLIENT_SECRET":  "gh-secret",
		"OPENNOW_OAUTH_KEYCLOAK_TYPE":         "oidc",
		"OPENNOW_OAUTH_KEYCLOAK_CLIENT_ID":    "kc",
		"OPENNOW_OAUTH_KEYCLOAK_ISSUER":       "https://sso.example.com/realms/main",
		"OPENNOW_OAUTH_KEYCLOAK_SCOPES":       "openid profile email groups",
		"OPENNOW_OAUTH_KEYCLOAK_DISPLAY_NAME": "公司 SSO",
		"OPENNOW_OAUTH_ALLOWED_USERS":         "github:zx06,keycloak:alice",
		"OPENNOW_OAUTH_ALLOWED_DOMAINS":       "example.com",
		"OPENNOW_ADMIN_API_KEY_LOGIN":         "false",
		"OPENNOW_SESSION_TTL":                 "12h",
		"OPENNOW_CF_ACCESS_TEAM_DOMAIN":       "myteam",
		"OPENNOW_CF_ACCESS_AUD":               "aud",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(ac.Providers) != 2 || ac.Providers[0].Type != "github" || ac.Providers[1].Name != "keycloak" || ac.Providers[1].Type != "oidc" {
		t.Fatalf("providers: %+v", ac.Providers)
	}
	if len(ac.Providers[1].Scopes) != 4 || ac.Providers[1].DisplayName != "公司 SSO" {
		t.Fatalf("keycloak: %+v", ac.Providers[1])
	}
	if !ac.DisableAPIKeyLogin || ac.SessionTTL != 12*time.Hour || !ac.CFAccess.Enabled() || len(ac.AllowedUsers) != 2 {
		t.Fatalf("auth: %+v", ac)
	}
}

func TestAuthDefaultsAndErrors(t *testing.T) {
	ac, err := AuthFromEnv(envMap(nil))
	if err != nil || ac.DisableAPIKeyLogin || ac.SessionTTL != DefaultSessionTTL || len(ac.Providers) != 0 {
		t.Fatalf("defaults: %+v %v", ac, err)
	}
	bad := []map[string]string{
		{"OPENNOW_OAUTH_PROVIDERS": "x", "OPENNOW_OAUTH_X_CLIENT_ID": "id"},                                   // no type
		{"OPENNOW_OAUTH_PROVIDERS": "oidc", "OPENNOW_OAUTH_OIDC_CLIENT_ID": "id"},                             // no issuer
		{"OPENNOW_OAUTH_PROVIDERS": "x", "OPENNOW_OAUTH_X_TYPE": "oauth2", "OPENNOW_OAUTH_X_CLIENT_ID": "id"}, // no urls
		{"OPENNOW_OAUTH_PROVIDERS": "github"},                                                                 // no client id
		{"OPENNOW_OAUTH_PROVIDERS": "x", "OPENNOW_OAUTH_X_TYPE": "saml", "OPENNOW_OAUTH_X_CLIENT_ID": "id"},
		{"OPENNOW_OAUTH_ALLOWED_USERS": "zx06"},
		{"OPENNOW_SESSION_TTL": "soon"},
		{"OPENNOW_CF_ACCESS_TEAM_DOMAIN": "t"},
		{"OPENNOW_OAUTH_PROVIDERS": "Bad/Name", "OPENNOW_OAUTH_BAD/NAME_TYPE": "github"},
	}
	for i, m := range bad {
		if _, err := AuthFromEnv(envMap(m)); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestAuthConfigFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(p, []byte(`{
	  "providers": [{"name": "google", "type": "oidc", "client_id": "g", "client_secret": "s", "issuer": "https://accounts.google.com"}],
	  "allowed_emails": ["me@gmail.com"],
	  "session_ttl": "24h",
	  "api_key_login": false
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ac, err := AuthFromEnv(envMap(map[string]string{
		"OPENNOW_OAUTH_CONFIG":           p,
		"OPENNOW_OAUTH_PROVIDERS":        "github",
		"OPENNOW_OAUTH_GITHUB_CLIENT_ID": "x",
		"OPENNOW_OAUTH_ALLOWED_EMAILS":   "you@gmail.com",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(ac.Providers) != 2 || ac.Providers[0].Name != "google" || len(ac.AllowedEmails) != 2 || ac.SessionTTL != 24*time.Hour || !ac.DisableAPIKeyLogin {
		t.Fatalf("file: %+v", ac)
	}
	if _, err := AuthFromEnv(envMap(map[string]string{"OPENNOW_OAUTH_CONFIG": p + ".missing"})); err == nil || !strings.Contains(err.Error(), "OPENNOW_OAUTH_CONFIG") {
		t.Fatalf("missing file: %v", err)
	}
}
