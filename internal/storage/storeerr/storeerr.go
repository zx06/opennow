// Package storeerr holds sentinel errors shared by every storage backend.
package storeerr

import "errors"

var (
	ErrNotFound      = errors.New("not found")
	ErrDraftNotFound = errors.New("draft revision not found")
	ErrInvalidPath   = errors.New("invalid path")
	ErrTooLarge      = errors.New("file too large for storage backend")
)
