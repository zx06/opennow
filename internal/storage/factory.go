package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/zx06/opennow/internal/storage/github"
	"github.com/zx06/opennow/internal/storage/local"
	"github.com/zx06/opennow/internal/storage/r2"
	"github.com/zx06/opennow/internal/storage/s3"
)

// Open selects a backend by name. Remote backends read their settings from
// OPENNOW_S3_*, OPENNOW_R2_* and OPENNOW_GITHUB_* environment variables;
// dataDir is used by local storage and for GitHub draft staging.
func Open(backend, dataDir string) (Backend, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	switch backend {
	case "local", "":
		return local.New(dataDir)
	case "s3":
		return s3.New(ctx, s3.ConfigFromEnv())
	case "r2":
		return r2.New(ctx, r2.ConfigFromEnv())
	case "github":
		return github.New(ctx, github.ConfigFromEnv(dataDir))
	default:
		return nil, fmt.Errorf("unknown storage backend %q", backend)
	}
}
