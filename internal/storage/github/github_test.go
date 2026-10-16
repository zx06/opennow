package github

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zx06/opennow/internal/storage/storagetest"
	"github.com/zx06/opennow/internal/storage/storeerr"
)

func newTestBackend(t *testing.T, repo *fakeRepo) *Backend {
	t.Helper()
	b, err := newWithAPI(Config{Owner: "o", Repo: "r", Branch: "main", Prefix: "data", DraftDir: t.TempDir(), HeadTTL: time.Minute}, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.currentHead(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConformanceFake(t *testing.T) {
	repo := newFakeRepo()
	b := newTestBackend(t, repo)
	storagetest.Run(t, b, storagetest.Options{LargeFile: 5 << 20, Reopen: func(t *testing.T) storagetest.Backend {
		return newTestBackend(t, repo)
	}})
}

func TestPublishIsOneCommitAndDedupsBlobs(t *testing.T) {
	repo := newFakeRepo()
	b := newTestBackend(t, repo)
	bin := []byte{0, 1, 2, 3, 0xff}
	commitsBefore := repo.calls["UpdateRef"]
	storagetest.Publish(t, b, "s", map[string][]byte{"index.html": []byte("a"), "app.css": []byte("b"), "logo.png": bin})
	if n := repo.calls["UpdateRef"] - commitsBefore; n != 1 {
		t.Fatalf("publish made %d commits, want 1", n)
	}
	// Text files are inlined in the tree; only the binary needs a blob request.
	if n := repo.calls["CreateBlob"]; n != 1 {
		t.Fatalf("CreateBlob calls = %d, want 1", n)
	}
	storagetest.Publish(t, b, "s", map[string][]byte{"index.html": []byte("a2"), "logo.png": bin})
	if n := repo.calls["CreateBlob"]; n != 1 {
		t.Fatalf("unchanged binary was re-uploaded (CreateBlob=%d)", n)
	}
}

func TestCommitRetriesOnConflict(t *testing.T) {
	repo := newFakeRepo()
	b := newTestBackend(t, repo)
	other := newTestBackend(t, repo)
	repo.beforeUpdate = func() {
		// Another writer publishes between our tree build and ref update.
		storagetest.Publish(t, other, "other", map[string][]byte{"index.html": []byte("o")})
	}
	storagetest.Publish(t, b, "mine", map[string][]byte{"index.html": []byte("m")})
	ctx := context.Background()
	for _, s := range []string{"mine", "other"} {
		if _, err := b.GetMeta(ctx, s); err != nil {
			t.Fatalf("%s lost after conflict retry: %v", s, err)
		}
	}
}

func TestHotReadsUseNoAPICalls(t *testing.T) {
	repo := newFakeRepo()
	b := newTestBackend(t, repo)
	storagetest.Publish(t, b, "hot", map[string][]byte{"index.html": []byte("<h1>hot</h1>")})
	ctx := context.Background()
	rc, _, err := b.OpenCurrentFile(ctx, "hot", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	total := func() int {
		n := 0
		for _, v := range repo.calls {
			n += v
		}
		return n
	}
	before := total()
	for i := 0; i < 100; i++ {
		rc, _, err := b.OpenCurrentFile(ctx, "hot", "index.html")
		if err != nil {
			t.Fatal(err)
		}
		rc.Close()
		_, _ = b.GetMeta(ctx, "favicon.ico")
	}
	if n := total() - before; n != 0 {
		t.Fatalf("hot reads made %d API calls: %v", n, repo.calls)
	}
}

func TestUploadSizeLimit(t *testing.T) {
	repo := newFakeRepo()
	b := newTestBackend(t, repo)
	b.cfg.MaxFileSize = 10
	ctx := context.Background()
	d, _ := b.CreateRevision(ctx, "big")
	err := b.Upload(ctx, d.ID, "a.bin", bytes.NewReader(make([]byte, 11)), -1)
	if !errors.Is(err, storeerr.ErrTooLarge) {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestEmptyRepoIsInitialised(t *testing.T) {
	repo := newFakeRepo()
	b := newTestBackend(t, repo)
	if repo.ref == "" {
		t.Fatal("branch not initialised")
	}
	if sites, err := b.ListSites(context.Background()); err != nil || len(sites) != 0 {
		t.Fatalf("%v %v", sites, err)
	}
}

func TestRESTClientRateLimitAndETag(t *testing.T) {
	var hits atomic.Int64
	limited := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing auth header")
		}
		w.Header().Set("X-RateLimit-Limit", "5000")
		if limited.Load() {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "9999999999")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"message":"API rate limit exceeded"}`))
			return
		}
		w.Header().Set("X-RateLimit-Remaining", "4000")
		w.Header().Set("X-RateLimit-Reset", "9999999999")
		if strings.HasSuffix(r.URL.Path, "/git/ref/heads/main") {
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			w.Write([]byte(`{"object":{"sha":"abc"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := newRESTClient(srv.URL, "o", "r", "main", "tok")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		sha, err := c.GetRef(ctx)
		if err != nil || sha != "abc" {
			t.Fatalf("GetRef: %q %v", sha, err)
		}
	}
	if ri := c.RateInfo(); ri.NotModified != 2 || ri.Remaining != 4000 || ri.Limit != 5000 {
		t.Fatalf("rate info %+v", ri)
	}
	if _, err := c.GetTree(ctx, "nope"); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("404 mapping: %v", err)
	}
	limited.Store(true)
	if _, err := c.GetRef(ctx); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
	n := hits.Load()
	if _, err := c.GetTree(ctx, "x"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want fast-fail ErrRateLimited, got %v", err)
	}
	if hits.Load() != n {
		t.Fatal("client hit GitHub while rate limited")
	}
}

func TestGitBlobSHA(t *testing.T) {
	// `printf 'hello\r\nworld\n' | git hash-object --stdin`
	if got := GitBlobSHA([]byte("hello\r\nworld\n")); got != "c78c5056793fb2d4e9a1917b0272c4a91e3135a7" {
		t.Fatal(got)
	}
}
