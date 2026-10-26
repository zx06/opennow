package config

import (
	"testing"
)

func TestGitHubLegacyEnvShim(t *testing.T) {
	env := map[string]string{
		"OPENNOW_GITHUB_CLIENT_ID":     " id ",
		"OPENNOW_GITHUB_CLIENT_SECRET": " sec ",
		"OPENNOW_GITHUB_ALLOWLIST":     "zx06,friend",
		"OPENNOW_GITHUB_DENY_MESSAGE":  "仅内部账号",
	}
	getenv := func(k string) string { return env[k] }
	ac, err := AuthFromEnv(getenv)
	if err != nil {
		t.Fatal(err)
	}
	if len(ac.Providers) != 1 || ac.Providers[0].Name != "github" || ac.Providers[0].ClientID != "id" || ac.Providers[0].ClientSecret != "sec" {
		t.Fatalf("provider: %+v", ac.Providers)
	}
	wantUsers := map[string]bool{"github:zx06": true, "github:friend": true}
	if len(ac.AllowedUsers) != 2 {
		t.Fatalf("users: %+v", ac.AllowedUsers)
	}
	for _, u := range ac.AllowedUsers {
		if !wantUsers[u] {
			t.Fatalf("unexpected user %q in %+v", u, ac.AllowedUsers)
		}
	}
	if ac.DenyMessage != "仅内部账号" {
		t.Fatalf("deny: %q", ac.DenyMessage)
	}
}

func TestGitHubLegacyDefaultAllowlist(t *testing.T) {
	env := map[string]string{
		"OPENNOW_GITHUB_CLIENT_ID":     "id",
		"OPENNOW_GITHUB_CLIENT_SECRET": "sec",
	}
	ac, err := AuthFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if len(ac.AllowedUsers) != 1 || ac.AllowedUsers[0] != "github:zx06" {
		t.Fatalf("default allowlist: %+v", ac.AllowedUsers)
	}
}
