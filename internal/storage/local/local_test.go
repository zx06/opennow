package local

import (
	"context"
	"strings"
	"testing"
)

func TestPublishFlow(t *testing.T) {
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	draft, err := b.CreateRevision(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Upload(ctx, draft.ID, "index.html", strings.NewReader("<h1>hi</h1>"), 13); err != nil {
		t.Fatal(err)
	}
	ver, slug, err := b.Finalize(ctx, draft.ID)
	if err != nil || slug != "demo" {
		t.Fatalf("finalize: %v %q", err, slug)
	}
	rc, info, err := b.OpenCurrentFile(ctx, "demo", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if info.Path != "index.html" {
		t.Fatalf("path %q", info.Path)
	}
	vers, err := b.ListVersions(ctx, "demo")
	if err != nil || len(vers) != 1 {
		t.Fatalf("versions: %v", vers)
	}
	draft2, _ := b.CreateRevision(ctx, "demo")
	_ = b.Upload(ctx, draft2.ID, "index.html", strings.NewReader("<h1>v2</h1>"), 14)
	v2, _, _ := b.Finalize(ctx, draft2.ID)
	if _, err := b.Rollback(ctx, "demo", ver.ID); err != nil {
		t.Fatal(err)
	}
	meta, _ := b.GetMeta(ctx, "demo")
	if meta.CurrentVersion != ver.ID {
		t.Fatalf("rollback wanted %s got %s", ver.ID, meta.CurrentVersion)
	}
	_ = v2
}
