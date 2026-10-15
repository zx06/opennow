package config

import (
	"os"
	"strings"
)

// Config holds runtime settings from flags and environment.
type Config struct {
	Addr         string
	DataDir      string
	Backend      string
	APIKey       string
	BaseDomain   string
	AdminPrefix  string
	AdminHost    string
	PublicURL    string
	PathMode     bool
	SubdomainMode bool
}

func FromEnv() Config {
	c := Config{
		Addr:          env("OPENNOW_ADDR", ":8080"),
		DataDir:       env("OPENNOW_DATA_DIR", "./data"),
		Backend:       env("OPENNOW_STORAGE", "local"),
		APIKey:        os.Getenv("OPENNOW_API_KEY"),
		BaseDomain:    os.Getenv("OPENNOW_BASE_DOMAIN"),
		AdminPrefix:   env("OPENNOW_ADMIN_PREFIX", "/_admin/"),
		AdminHost:     os.Getenv("OPENNOW_ADMIN_HOST"),
		PublicURL:     strings.TrimRight(os.Getenv("OPENNOW_PUBLIC_URL"), "/"),
		PathMode:      envBool("OPENNOW_PATH_MODE", true),
		SubdomainMode: envBool("OPENNOW_SUBDOMAIN_MODE", true),
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
