package local

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/zx06/opennow/internal/storage/storeerr"
)

func TestFinalizeCopiesSoleHTMLToIndex(t *testing.T) {
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d, err := b.CreateRevision(ctx, "solo")
	if err != nil {
		t.Fatal(err)
	}
	body := "<h1>hello sole</h1>"
	if err := b.Upload(ctx, d.ID, "page.html", strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Finalize(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	rc, _, err := b.OpenCurrentFile(ctx, "solo", "index.html")
	if err != nil {
		t.Fatalf("index.html missing after finalize: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != body {
		t.Fatalf("index content %q", got)
	}
	rc, _, err = b.OpenCurrentFile(ctx, "solo", "page.html")
	if err != nil {
		t.Fatalf("original missing: %v", err)
	}
	rc.Close()
}

func TestRenameSite(t *testing.T) {
	b, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	d, _ := b.CreateRevision(ctx, "old-slug")
	_ = b.Upload(ctx, d.ID, "index.html", strings.NewReader("<h1>x</h1>"), 10)
	if _, _, err := b.Finalize(ctx, d.ID); err != nil {
		t.Fatal(err)
	}

	meta, err := b.RenameSite(ctx, "old-slug", "new-slug")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Slug != "new-slug" {
		t.Fatalf("meta slug %q", meta.Slug)
	}
	if _, err := b.GetMeta(ctx, "old-slug"); !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("old slug still live: %v", err)
	}
	rc, _, err := b.OpenCurrentFile(ctx, "new-slug", "index.html")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()

	if _, err := b.RenameSite(ctx, "new-slug", "../escape"); !errors.Is(err, storeerr.ErrInvalidPath) {
		t.Fatalf("want ErrInvalidPath, got %v", err)
	}
	d2, _ := b.CreateRevision(ctx, "taken")
	_ = b.Upload(ctx, d2.ID, "index.html", strings.NewReader("y"), 1)
	_, _, _ = b.Finalize(ctx, d2.ID)
	if _, err := b.RenameSite(ctx, "new-slug", "taken"); !errors.Is(err, storeerr.ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}
