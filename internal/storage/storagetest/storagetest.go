// Package storagetest is a conformance suite run against every storage
// backend (local, S3 with an in-memory store, GitHub with a fake API, and the
// gated MinIO / GitHub end-to-end tests).
package storagetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/zx06/opennow/internal/site"
	"github.com/zx06/opennow/internal/storage/storeerr"
)

// Backend mirrors storage.Backend (redeclared to avoid an import cycle).
type Backend interface {
	Name() string
	CreateRevision(ctx context.Context, slug string) (*site.DraftRevision, error)
	Upload(ctx context.Context, revisionID, relPath string, r io.Reader, size int64) error
	Finalize(ctx context.Context, revisionID string) (*site.Version, string, error)
	GetMeta(ctx context.Context, slug string) (*site.Meta, error)
	PutMeta(ctx context.Context, meta *site.Meta) error
	ListSites(ctx context.Context) ([]site.Summary, error)
	DeleteSite(ctx context.Context, slug string) error
	ListVersions(ctx context.Context, slug string) ([]site.Version, error)
	Rollback(ctx context.Context, slug, versionID string) (*site.Version, error)
	OpenCurrentFile(ctx context.Context, slug, relPath string) (io.ReadCloser, *site.FileInfo, error)
	OpenVersionFile(ctx context.Context, slug, versionID, relPath string) (io.ReadCloser, *site.FileInfo, error)
	ListCurrentFiles(ctx context.Context, slug string) ([]site.FileInfo, error)
	ListVersionFiles(ctx context.Context, slug, versionID string) ([]site.FileInfo, error)
	Overview(ctx context.Context) (*site.StorageOverview, error)
}

// Options tweak the suite.
type Options struct {
	// Slug prefix, so runs against shared remote stores do not collide.
	SlugPrefix string
	// LargeFile is the size of a binary file that exercises the streaming
	// (uncached) read path. 0 skips it.
	LargeFile int
	// Reopen, when set, returns a fresh backend instance over the same
	// store to verify persistence (no shared in-memory state).
	Reopen func(t *testing.T) Backend
}

// Publish uploads files and finalises a version.
func Publish(t *testing.T, b Backend, slug string, files map[string][]byte) (string, string) {
	t.Helper()
	ctx := context.Background()
	d, err := b.CreateRevision(ctx, slug)
	if err != nil {
		t.Fatalf("create revision: %v", err)
	}
	for p, data := range files {
		if err := b.Upload(ctx, d.ID, p, bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatalf("upload %s: %v", p, err)
		}
	}
	v, gotSlug, err := b.Finalize(ctx, d.ID)
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if slug != "" && gotSlug != slug {
		t.Fatalf("finalize slug %q, want %q", gotSlug, slug)
	}
	return v.ID, gotSlug
}

func read(t *testing.T, b Backend, slug, vid, p string) ([]byte, *site.FileInfo) {
	t.Helper()
	ctx := context.Background()
	var rc io.ReadCloser
	var info *site.FileInfo
	var err error
	if vid == "" {
		rc, info, err = b.OpenCurrentFile(ctx, slug, p)
	} else {
		rc, info, err = b.OpenVersionFile(ctx, slug, vid, p)
	}
	if err != nil {
		t.Fatalf("open %s@%s %s: %v", slug, vid, p, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return data, info
}

// Run executes the conformance suite.
func Run(t *testing.T, b Backend, opt Options) {
	ctx := context.Background()
	slug := opt.SlugPrefix + "demo"

	if _, err := b.GetMeta(ctx, opt.SlugPrefix+"nope"); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("missing site: want ErrNotFound, got %v", err)
	}
	if _, _, err := b.OpenCurrentFile(ctx, opt.SlugPrefix+"nope", "index.html"); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("missing site file: want ErrNotFound, got %v", err)
	}

	bin := []byte{0x89, 'P', 'N', 'G', 0, 1, 2, 3, 0xff, 0xfe}
	v1Files := map[string][]byte{
		"index.html":            []byte("<h1>v1</h1>\r\n"),
		"css/app.css":           []byte("body{color:red}"),
		"img/logo.png":          bin,
		"docs/guide/index.html": []byte("<p>guide</p>"),
	}
	var large []byte
	if opt.LargeFile > 0 {
		large = make([]byte, opt.LargeFile)
		for i := range large {
			large[i] = byte(i*7 + i/251)
		}
		v1Files["media/big.bin"] = large
	}
	v1, _ := Publish(t, b, slug, v1Files)

	m, err := b.GetMeta(ctx, slug)
	if err != nil || m.CurrentVersion != v1 {
		t.Fatalf("meta after publish: %+v %v", m, err)
	}
	for p, want := range v1Files {
		got, info := read(t, b, slug, "", p)
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: content mismatch (%d vs %d bytes)", p, len(got), len(want))
		}
		if info.Size != int64(len(want)) && info.Size != 0 {
			t.Fatalf("%s: size %d want %d", p, info.Size, len(want))
		}
		if info.Path != p {
			t.Fatalf("%s: info path %q", p, info.Path)
		}
	}
	// Second read must also succeed (cached path).
	if got, _ := read(t, b, slug, "", "index.html"); string(got) != "<h1>v1</h1>\r\n" {
		t.Fatalf("cached read %q", got)
	}
	for _, bad := range []string{"missing.js", "css", "../" + slug + "/meta.json", "../../meta.json"} {
		if rc, _, err := b.OpenCurrentFile(ctx, slug, bad); err == nil {
			rc.Close()
			t.Fatalf("%q: expected an error", bad)
		}
	}
	files, err := b.ListCurrentFiles(ctx, slug)
	if err != nil || len(files) != len(v1Files) {
		t.Fatalf("list current files: %d %v", len(files), err)
	}

	time.Sleep(10 * time.Millisecond)
	v2, _ := Publish(t, b, slug, map[string][]byte{
		"index.html":   []byte("<h1>v2</h1>"),
		"css/app.css":  []byte("body{color:red}"), // unchanged (dedup for git)
		"img/logo.png": bin,
	})
	if got, _ := read(t, b, slug, "", "index.html"); string(got) != "<h1>v2</h1>" {
		t.Fatalf("v2 live content %q", got)
	}
	if got, _ := read(t, b, slug, v1, "index.html"); string(got) != "<h1>v1</h1>\r\n" {
		t.Fatalf("v1 preview content %q", got)
	}
	if rc, _, err := b.OpenCurrentFile(ctx, slug, "docs/guide/index.html"); err == nil {
		rc.Close()
		t.Fatal("v2 must not contain files from v1")
	}
	vers, err := b.ListVersions(ctx, slug)
	if err != nil || len(vers) != 2 {
		t.Fatalf("versions: %v %v", vers, err)
	}
	vf, err := b.ListVersionFiles(ctx, slug, v1)
	if err != nil || len(vf) != len(v1Files) {
		t.Fatalf("version files: %d %v", len(vf), err)
	}
	if _, err := b.ListVersionFiles(ctx, slug, "no-such-version"); err == nil {
		t.Fatal("unknown version files should fail")
	}

	if _, err := b.Rollback(ctx, slug, v1); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if m, _ := b.GetMeta(ctx, slug); m.CurrentVersion != v1 {
		t.Fatalf("rollback: current %s want %s", m.CurrentVersion, v1)
	}
	if got, _ := read(t, b, slug, "", "index.html"); string(got) != "<h1>v1</h1>\r\n" {
		t.Fatalf("after rollback %q", got)
	}
	if _, err := b.Rollback(ctx, slug, "no-such-version"); err == nil {
		t.Fatal("rollback to unknown version should fail")
	}
	if _, err := b.Rollback(ctx, slug, "../x"); err == nil {
		t.Fatal("rollback traversal should fail")
	}

	m, _ = b.GetMeta(ctx, slug)
	exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	m.PasswordHash = "hash"
	m.ExpiresAt = &exp
	if err := b.PutMeta(ctx, m); err != nil {
		t.Fatalf("put meta: %v", err)
	}
	m2, _ := b.GetMeta(ctx, slug)
	if m2.PasswordHash != "hash" || m2.ExpiresAt == nil || !m2.ExpiresAt.Equal(exp) || m2.CurrentVersion != v1 {
		t.Fatalf("meta not persisted: %+v", m2)
	}
	_ = v2

	_, autoSlug := Publish(t, b, "", map[string][]byte{"index.html": []byte("auto")})
	if autoSlug == "" {
		t.Fatal("auto slug empty")
	}

	if err := b.Upload(ctx, "00000000-bogus", "x.html", strings.NewReader("x"), 1); !errors.Is(err, storeerr.ErrDraftNotFound) {
		t.Fatalf("upload to bogus draft: %v", err)
	}
	if _, _, err := b.Finalize(ctx, "00000000-bogus"); !errors.Is(err, storeerr.ErrDraftNotFound) {
		t.Fatalf("finalize bogus draft: %v", err)
	}

	if opt.Reopen != nil {
		b2 := opt.Reopen(t)
		m3, err := b2.GetMeta(ctx, slug)
		if err != nil || m3.CurrentVersion != v1 || m3.PasswordHash != "hash" {
			t.Fatalf("reopened meta: %+v %v", m3, err)
		}
		if got, _ := read(t, b2, slug, "", "img/logo.png"); !bytes.Equal(got, bin) {
			t.Fatal("reopened binary mismatch")
		}
		if large != nil {
			if got, _ := read(t, b2, slug, "", "media/big.bin"); !bytes.Equal(got, large) {
				t.Fatal("reopened large file mismatch")
			}
		}
		if vers, _ := b2.ListVersions(ctx, slug); len(vers) != 2 {
			t.Fatalf("reopened versions %d", len(vers))
		}
	}

	sites, err := b.ListSites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range sites {
		if s.Slug == slug {
			found = true
			if s.FileCount != len(v1Files) || !s.HasPassword || s.CurrentVersion != v1 {
				t.Fatalf("summary %+v", s)
			}
		}
	}
	if !found {
		t.Fatalf("site %s not listed in %+v", slug, sites)
	}
	ov, err := b.Overview(ctx)
	if err != nil || ov.Backend != b.Name() || ov.SiteCount < 2 || ov.TotalBytes <= 0 {
		t.Fatalf("overview %+v %v", ov, err)
	}

	for _, s := range []string{slug, autoSlug} {
		if err := b.DeleteSite(ctx, s); err != nil {
			t.Fatalf("delete %s: %v", s, err)
		}
		if _, err := b.GetMeta(ctx, s); !errors.Is(err, storeerr.ErrNotFound) {
			t.Fatalf("after delete %s: %v", s, err)
		}
	}
	sites, _ = b.ListSites(ctx)
	for _, s := range sites {
		if s.Slug == slug || s.Slug == autoSlug {
			t.Fatalf("deleted site still listed: %s", s.Slug)
		}
	}
}
