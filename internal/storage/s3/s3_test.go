package s3

import (
	"context"
	"strings"
	"testing"

	"github.com/zx06/opennow/internal/storage/storagetest"
)

func TestConformanceMemStore(t *testing.T) {
	store := NewMemStore()
	cfg := Config{Name: "s3", Prefix: "pfx/", Cache: CacheConfig{MetaTTL: 0, FileBytes: 1 << 20, FileMaxObject: 64 << 10}}
	b := NewWithStore(cfg, store)
	storagetest.Run(t, b, storagetest.Options{LargeFile: 200 << 10, Reopen: func(t *testing.T) storagetest.Backend {
		return NewWithStore(cfg, store)
	}})
	for _, k := range store.Keys() {
		if !strings.HasPrefix(k, "pfx/") {
			t.Fatalf("key outside prefix: %s", k)
		}
		if strings.HasPrefix(k, "pfx/sites/") {
			t.Fatalf("leftover after delete: %s", k)
		}
		if strings.HasPrefix(k, "pfx/drafts/") {
			t.Fatalf("leftover draft: %s", k)
		}
	}
}

// A draft created by one instance can be finalised by another (LIST fallback).
func TestFinalizeFromOtherInstance(t *testing.T) {
	store := NewMemStore()
	a := NewWithStore(Config{}, store)
	b := NewWithStore(Config{}, store)
	ctx := context.Background()
	d, err := a.CreateRevision(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Upload(ctx, d.ID, "index.html", strings.NewReader("hello"), 5); err != nil {
		t.Fatal(err)
	}
	if err := b.Upload(ctx, d.ID, "app.js", strings.NewReader("js"), 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Finalize(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	files, err := b.ListCurrentFiles(ctx, "x")
	if err != nil || len(files) != 2 {
		t.Fatalf("files %v %v", files, err)
	}
	if files[0].ContentType == "" {
		t.Fatal("content type missing")
	}
}

func TestParseEndpoint(t *testing.T) {
	cases := []struct {
		in     string
		host   string
		secure bool
	}{
		{"", "s3.amazonaws.com", true},
		{"http://127.0.0.1:9000", "127.0.0.1:9000", false},
		{"https://acc.r2.cloudflarestorage.com/", "acc.r2.cloudflarestorage.com", true},
		{"minio.local:9000", "minio.local:9000", true},
	}
	for _, c := range cases {
		h, s, err := parseEndpoint(c.in, false)
		if err != nil || h != c.host || s != c.secure {
			t.Errorf("%q → %q %v %v", c.in, h, s, err)
		}
	}
	if _, _, err := parseEndpoint("https://x/y", false); err == nil {
		t.Error("path in endpoint should fail")
	}
}
