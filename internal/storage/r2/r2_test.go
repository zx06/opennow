package r2

import (
	"context"
	"strings"
	"testing"

	"github.com/zx06/opennow/internal/storage/s3"
)

func TestS3ConfigDefaults(t *testing.T) {
	sc, err := Config{AccountID: "abc123", Bucket: "sites", AccessKey: "k", SecretKey: "s"}.S3Config()
	if err != nil {
		t.Fatal(err)
	}
	if sc.Name != "r2" || sc.Client.Endpoint != "https://abc123.r2.cloudflarestorage.com" ||
		sc.Client.Region != "auto" || sc.Client.PathStyle != "true" {
		t.Fatalf("unexpected %+v", sc)
	}
	if sc.Cache.MetaTTL < DefaultCache().MetaTTL || sc.Cache.FileBytes == 0 {
		t.Fatalf("cache defaults not applied: %+v", sc.Cache)
	}
	if _, err := (Config{Bucket: "x"}).S3Config(); err == nil {
		t.Fatal("missing account id should fail")
	}
	if _, err := (Config{AccountID: "x"}).S3Config(); err == nil {
		t.Fatal("missing bucket should fail")
	}
}

// TestHotPathRequestBudget checks that repeated reads of a published R2 site
// are served from memory (no GET after warm-up, never a LIST).
func TestHotPathRequestBudget(t *testing.T) {
	sc, _ := Config{AccountID: "a", Bucket: "b"}.S3Config()
	b := s3.NewWithStore(sc, s3.NewMemStore())
	ctx := context.Background()
	d, _ := b.CreateRevision(ctx, "hot")
	_ = b.Upload(ctx, d.ID, "index.html", strings.NewReader("<h1>hot</h1>"), 12)
	if _, _, err := b.Finalize(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	c := b.Counters()
	gets, lists := c.Get.Load(), c.List.Load()
	for i := 0; i < 50; i++ {
		if _, err := b.GetMeta(ctx, "hot"); err != nil {
			t.Fatal(err)
		}
		rc, _, err := b.OpenCurrentFile(ctx, "hot", "index.html")
		if err != nil {
			t.Fatal(err)
		}
		rc.Close()
		if _, _, err := b.OpenCurrentFile(ctx, "hot", "missing.js"); err == nil {
			t.Fatal("missing file should 404")
		}
		_, _ = b.GetMeta(ctx, "favicon.ico") // negative-cached
	}
	// Allow the first GET of the file body and the first negative meta lookup.
	if g := c.Get.Load() - gets; g > 2 {
		t.Fatalf("hot path issued %d GETs, want ≤2", g)
	}
	if c.List.Load() != lists {
		t.Fatal("hot path must never LIST")
	}
}
