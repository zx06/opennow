package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Config holds runtime settings from flags and environment.
type Config struct {
	Addr          string
	DataDir       string
	Backend       string
	APIKey        string
	BaseDomain    string
	AdminPrefix   string
	AdminHost     string
	PublicURL     string
	PathMode      bool
	SubdomainMode bool
	// Secret signs unlock cookies and preview tokens (OPENNOW_SECRET).
	Secret string

	// GitHub OAuth for admin UI login (OPENNOW_GITHUB_CLIENT_*).
	GitHubClientID     string
	GitHubClientSecret string
	// GitHubAllowlist is the comma-separated list of GitHub logins allowed
	// into the admin UI. Comparison is case-insensitive. Default: zx06.
	GitHubAllowlist []string
}

func FromEnv() Config {
	c := Config{
		Addr:               env("OPENNOW_ADDR", ":8080"),
		DataDir:            env("OPENNOW_DATA_DIR", "./data"),
		Backend:            env("OPENNOW_STORAGE", "local"),
		APIKey:             os.Getenv("OPENNOW_API_KEY"),
		BaseDomain:         os.Getenv("OPENNOW_BASE_DOMAIN"),
		AdminPrefix:        env("OPENNOW_ADMIN_PREFIX", "/_admin/"),
		AdminHost:          os.Getenv("OPENNOW_ADMIN_HOST"),
		PublicURL:          strings.TrimRight(os.Getenv("OPENNOW_PUBLIC_URL"), "/"),
		PathMode:           envBool("OPENNOW_PATH_MODE", true),
		SubdomainMode:      envBool("OPENNOW_SUBDOMAIN_MODE", true),
		Secret:             os.Getenv("OPENNOW_SECRET"),
		GitHubClientID:     strings.TrimSpace(os.Getenv("OPENNOW_GITHUB_CLIENT_ID")),
		GitHubClientSecret: strings.TrimSpace(os.Getenv("OPENNOW_GITHUB_CLIENT_SECRET")),
		GitHubAllowlist:    parseAllowlist(os.Getenv("OPENNOW_GITHUB_ALLOWLIST")),
	}
	if !strings.HasSuffix(c.AdminPrefix, "/") {
		c.AdminPrefix += "/"
	}
	if !strings.HasPrefix(c.AdminPrefix, "/") {
		c.AdminPrefix = "/" + c.AdminPrefix
	}
	return c
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// EnsureSecret fills c.Secret from OPENNOW_SECRET or a persisted random key at
// <DataDir>/secret.key (created with 0600 permissions on first start).
func (c *Config) EnsureSecret() error {
	if c.Secret != "" {
		return nil
	}
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(c.DataDir, "secret.key")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		c.Secret = strings.TrimSpace(string(b))
		return nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	c.Secret = hex.EncodeToString(buf)
	return os.WriteFile(p, []byte(c.Secret+"\n"), 0o600)
}

// GitHubOAuthConfigured reports whether GitHub admin login is fully wired.
func (c Config) GitHubOAuthConfigured() bool {
	return c.GitHubClientID != "" && c.GitHubClientSecret != ""
}

// parseAllowlist splits a comma-separated GitHub login allowlist.
// Empty / whitespace-only input defaults to []string{"zx06"}.
func parseAllowlist(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"zx06"}
	}
	return out
}

// AllowlistContains reports whether login is in the allowlist (case-insensitive).
func AllowlistContains(list []string, login string) bool {
	for _, a := range list {
		if strings.EqualFold(a, login) {
			return true
		}
	}
	return false
}
