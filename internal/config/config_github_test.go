package config

import (
	"os"
	"reflect"
	"testing"
)

func TestParseAllowlist_DefaultZX06(t *testing.T) {
	if got := parseAllowlist(""); !reflect.DeepEqual(got, []string{"zx06"}) {
		t.Fatalf("got %v", got)
	}
	if got := parseAllowlist("  ,  "); !reflect.DeepEqual(got, []string{"zx06"}) {
		t.Fatalf("got %v", got)
	}
}

func TestParseAllowlist_Custom(t *testing.T) {
	got := parseAllowlist("Alice, bob ,ZX06")
	want := []string{"Alice", "bob", "ZX06"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestAllowlistContains_CaseInsensitive(t *testing.T) {
	list := []string{"zx06", "Other"}
	if !AllowlistContains(list, "ZX06") {
		t.Fatal("expected match")
	}
	if AllowlistContains(list, "nope") {
		t.Fatal("unexpected match")
	}
}

func TestFromEnv_GitHubOAuth(t *testing.T) {
	t.Setenv("OPENNOW_GITHUB_CLIENT_ID", " id ")
	t.Setenv("OPENNOW_GITHUB_CLIENT_SECRET", " sec ")
	t.Setenv("OPENNOW_GITHUB_ALLOWLIST", "zx06,friend")
	t.Setenv("OPENNOW_PUBLIC_URL", "https://r.xuzhuo.xyz/")
	// Clear unrelated noise
	for _, k := range []string{"OPENNOW_API_KEY", "OPENNOW_SECRET"} {
		_ = os.Unsetenv(k)
	}
	c := FromEnv()
	if c.GitHubClientID != "id" || c.GitHubClientSecret != "sec" {
		t.Fatalf("creds %+v", c)
	}
	if !reflect.DeepEqual(c.GitHubAllowlist, []string{"zx06", "friend"}) {
		t.Fatalf("allowlist %v", c.GitHubAllowlist)
	}
	if c.PublicURL != "https://r.xuzhuo.xyz" {
		t.Fatalf("PublicURL %q", c.PublicURL)
	}
	if !c.GitHubOAuthConfigured() {
		t.Fatal("expected configured")
	}
}

func TestGitHubOAuthConfigured(t *testing.T) {
	if (Config{GitHubClientID: "a"}).GitHubOAuthConfigured() {
		t.Fatal("secret missing")
	}
	if !(Config{GitHubClientID: "a", GitHubClientSecret: "b"}).GitHubOAuthConfigured() {
		t.Fatal("want true")
	}
}

func TestFromEnv_AdminAPIKeyLoginAndDenyMessage(t *testing.T) {
	t.Setenv("OPENNOW_ADMIN_API_KEY_LOGIN", "false")
	t.Setenv("OPENNOW_GITHUB_DENY_MESSAGE", "仅内部账号")
	c := FromEnv()
	if !c.DisableAdminAPIKeyLogin {
		t.Fatal("expected DisableAdminAPIKeyLogin")
	}
	if c.GitHubDenyMessage != "仅内部账号" {
		t.Fatalf("deny %q", c.GitHubDenyMessage)
	}
}
