// Package r2 stores sites in Cloudflare R2 through its S3-compatible API.
//
// It reuses the s3 backend with R2 defaults (endpoint
// https://<account_id>.r2.cloudflarestorage.com, region "auto", path-style
// addressing) and more aggressive caching, because R2's free tier meters
// Class A (PUT/LIST/DELETE) and Class B (GET/HEAD) operations per account.
package r2

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/zx06/opennow/internal/storage/s3"
)

// Config holds R2 settings.
type Config struct {
	AccountID string
	Bucket    string
	AccessKey string
	SecretKey string
	Prefix    string
	// Endpoint overrides the derived endpoint (EU jurisdiction, tests).
	Endpoint     string
	CreateBucket bool
	Cache        s3.CacheConfig
}

// DefaultCache keeps request counts low: meta is cached for 5 minutes and up
// to 128 MiB of hot files (≤2 MiB each) are kept in memory.
func DefaultCache() s3.CacheConfig {
	return s3.CacheConfig{MetaTTL: 5 * time.Minute, FileBytes: 128 << 20, FileMaxObject: 2 << 20, FileTTL: 6 * time.Hour}
}

// ConfigFromEnv reads OPENNOW_R2_* variables.
func ConfigFromEnv() Config {
	return Config{
		AccountID:    os.Getenv("OPENNOW_R2_ACCOUNT_ID"),
		Bucket:       os.Getenv("OPENNOW_R2_BUCKET"),
		AccessKey:    os.Getenv("OPENNOW_R2_ACCESS_KEY_ID"),
		SecretKey:    os.Getenv("OPENNOW_R2_SECRET_ACCESS_KEY"),
		Prefix:       os.Getenv("OPENNOW_R2_PREFIX"),
		Endpoint:     os.Getenv("OPENNOW_R2_ENDPOINT"),
		CreateBucket: os.Getenv("OPENNOW_R2_CREATE_BUCKET") == "true",
		Cache:        s3.CacheFromEnv(DefaultCache()),
	}
}

// Endpoint returns the S3 API endpoint for an account.
func Endpoint(accountID string) string {
	return fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
}

// S3Config translates an R2 config into the underlying s3 config.
func (c Config) S3Config() (s3.Config, error) {
	ep := c.Endpoint
	if ep == "" {
		if c.AccountID == "" {
			return s3.Config{}, errors.New("r2: OPENNOW_R2_ACCOUNT_ID (or OPENNOW_R2_ENDPOINT) is required")
		}
		ep = Endpoint(c.AccountID)
	}
	if c.Bucket == "" {
		return s3.Config{}, errors.New("r2: OPENNOW_R2_BUCKET is required")
	}
	cache := c.Cache
	if cache == (s3.CacheConfig{}) {
		cache = DefaultCache()
	}
	return s3.Config{
		Name: "r2",
		Client: s3.ClientConfig{
			Endpoint:  ep,
			Region:    "auto",
			Bucket:    c.Bucket,
			AccessKey: c.AccessKey,
			SecretKey: c.SecretKey,
			PathStyle: "true",
		},
		Prefix:       c.Prefix,
		CreateBucket: c.CreateBucket,
		Cache:        cache,
	}, nil
}

// New connects to R2.
func New(ctx context.Context, c Config) (*s3.Backend, error) {
	sc, err := c.S3Config()
	if err != nil {
		return nil, err
	}
	return s3.New(ctx, sc)
}
