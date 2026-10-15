package storage

import (
	"github.com/zx06/opennow/internal/storage/github"
	"github.com/zx06/opennow/internal/storage/local"
	"github.com/zx06/opennow/internal/storage/s3"
)

// Compile-time checks that every backend satisfies the interface. (R2 is
// served by *s3.Backend.)
var (
	_ Backend = (*local.Backend)(nil)
	_ Backend = (*s3.Backend)(nil)
	_ Backend = (*github.Backend)(nil)
)
