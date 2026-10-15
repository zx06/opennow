package s3_test

// Integration tests against a real S3-compatible server (MinIO in CI).
// Skipped unless OPENNOW_TEST_S3_ENDPOINT is set, e.g.
//
//	OPENNOW_TEST_S3_ENDPOINT=http://127.0.0.1:9000 \
//	OPENNOW_TEST_S3_ACCESS_KEY=minioadmin OPENNOW_TEST_S3_SECRET_KEY=minioadmin \
//	go test ./internal/storage/s3/ -run Integration -v

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zx06/opennow/internal/storage/r2"
	"github.com/zx06/opennow/internal/storage/s3"
	"github.com/zx06/opennow/internal/storage/storagetest"
)

func integrationClient(t *testing.T) s3.ClientConfig {
	ep := os.Getenv("OPENNOW_TEST_S3_ENDPOINT")
	if ep == "" {
		t.Skip("OPENNOW_TEST_S3_ENDPOINT not set; skipping S3 integration test")
	}
	bucket := os.Getenv("OPENNOW_TEST_S3_BUCKET")
	if bucket == "" {
		bucket = "opennow-test"
	}
	return s3.ClientConfig{
		Endpoint:  ep,
		Region:    "us-east-1",
		Bucket:    bucket,
		AccessKey: os.Getenv("OPENNOW_TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("OPENNOW_TEST_S3_SECRET_KEY"),
		PathStyle: "true",
	}
}

func runPrefix(name string) string {
	return fmt.Sprintf("it-%s-%d/", name, time.Now().UnixNano())
}

func TestIntegrationS3(t *testing.T) {
	cc := integrationClient(t)
	ctx := context.Background()
	cfg := s3.Config{Name: "s3", Client: cc, Prefix: runPrefix("s3"), CreateBucket: true,
		Cache: s3.CacheConfig{MetaTTL: time.Second, FileBytes: 8 << 20, FileMaxObject: 256 << 10, FileTTL: time.Minute}}
	b, err := s3.New(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	storagetest.Run(t, b, storagetest.Options{LargeFile: 6 << 20, Reopen: func(t *testing.T) storagetest.Backend {
		b2, err := s3.New(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		return b2
	}})
	t.Logf("requests: put=%d get=%d list=%d delete=%d", b.Counters().Put.Load(), b.Counters().Get.Load(), b.Counters().List.Load(), b.Counters().Delete.Load())
	assertPrefixEmpty(t, cc, cfg.Prefix+"sites/")
}

// TestIntegrationR2CodePath runs the R2 wrapper against the same server via
// OPENNOW_R2_ENDPOINT-style override (region auto, path style).
func TestIntegrationR2CodePath(t *testing.T) {
	cc := integrationClient(t)
	ctx := context.Background()
	rc := r2.Config{Endpoint: cc.Endpoint, Bucket: cc.Bucket, AccessKey: cc.AccessKey, SecretKey: cc.SecretKey,
		Prefix: runPrefix("r2"), CreateBucket: true}
	b, err := r2.New(ctx, rc)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name() != "r2" {
		t.Fatalf("name %q", b.Name())
	}
	storagetest.Run(t, b, storagetest.Options{SlugPrefix: "r2-", LargeFile: 3 << 20})
	ov, err := b.Overview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var reqs string
	for _, d := range ov.Details {
		if strings.HasPrefix(d.Label, "请求计数") {
			reqs = d.Value
		}
	}
	t.Logf("r2 overview requests: %s", reqs)
}

func assertPrefixEmpty(t *testing.T, cc s3.ClientConfig, prefix string) {
	t.Helper()
	store, _, err := s3.NewMinioStore(cc)
	if err != nil {
		t.Fatal(err)
	}
	objs, _, err := store.List(context.Background(), prefix, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 0 {
		t.Fatalf("%d objects left under %s (first %s)", len(objs), prefix, objs[0].Key)
	}
}
