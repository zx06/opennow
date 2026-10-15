package server

import "github.com/zx06/opennow/internal/mimetype"

// ContentType returns the Content-Type for a file name.
func ContentType(name string) string { return mimetype.ForName(name) }
