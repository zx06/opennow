package github

// End-to-end test against a real (throwaway, private) GitHub repository.
// Skipped unless OPENNOW_TEST_GITHUB_REPO=owner/repo and
// OPENNOW_TEST_GITHUB_TOKEN are set. Each run uses its own path prefix and
// deletes its sites at the end (git history keeps the commits).

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zx06/opennow/internal/storage/storagetest"
)

func TestE2EGitHub(t *testing.T) {
	repo, token := os.Getenv("OPENNOW_TEST_GITHUB_REPO"), os.Getenv("OPENNOW_TEST_GITHUB_TOKEN")
	if repo == "" || token == "" {
		t.Skip("OPENNOW_TEST_GITHUB_REPO / OPENNOW_TEST_GITHUB_TOKEN not set")
	}
	owner, name, _ := strings.Cut(repo, "/")
	branch := os.Getenv("OPENNOW_TEST_GITHUB_BRANCH")
	if branch == "" {
		branch = "main"
	}
	cfg := Config{
		Token: token, Owner: owner, Repo: name, Branch: branch,
		Prefix:   fmt.Sprintf("e2e/%d", time.Now().Unix()),
		DraftDir: t.TempDir(), HeadTTL: 2 * time.Second, MaxFileSize: maxGitHubFile,
		WriteInterval: 200 * time.Millisecond,
	}
	ctx := context.Background()
	b, err := New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	storagetest.Run(t, b, storagetest.Options{LargeFile: 5 << 20, Reopen: func(t *testing.T) storagetest.Backend {
		c := cfg
		c.DraftDir = t.TempDir()
		b2, err := New(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		return b2
	}})
	ri := b.api.RateInfo()
	t.Logf("prefix=%s requests=%d (304: %d) rate remaining=%d/%d", cfg.Prefix, ri.Requests, ri.NotModified, ri.Remaining, ri.Limit)
}
