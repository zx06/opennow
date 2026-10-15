package storage

import (
	"fmt"

	"github.com/zx06/opennow/internal/storage/github"
	"github.com/zx06/opennow/internal/storage/local"
	"github.com/zx06/opennow/internal/storage/r2"
	"github.com/zx06/opennow/internal/storage/s3"
)

// Open selects a backend by name.
func Open(backend, dataDir string) (Backend, error) {
	switch backend {
	case "local", "":
		return local.New(dataDir)
	case "s3":
		return s3.New("", "", "", ""), nil
	case "r2":
		return r2.New("", "", "", "", ""), nil
	case "github":
		return github.New("", "", "", ""), nil
	default:
		return nil, fmt.Errorf("unknown storage backend %q", backend)
	}
}
