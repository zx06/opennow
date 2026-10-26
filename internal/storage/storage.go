package storage

import (
	"context"
	"io"

	"github.com/zx06/opennow/internal/site"
)

// Backend implements create → upload → finalize publish flow and site lifecycle.
type Backend interface {
	Name() string

	CreateRevision(ctx context.Context, slug string) (*site.DraftRevision, error)
	Upload(ctx context.Context, revisionID, relPath string, r io.Reader, size int64) error
	Finalize(ctx context.Context, revisionID string) (*site.Version, string, error)

	GetMeta(ctx context.Context, slug string) (*site.Meta, error)
	PutMeta(ctx context.Context, meta *site.Meta) error
	ListSites(ctx context.Context) ([]site.Summary, error)
	DeleteSite(ctx context.Context, slug string) error
	// RenameSite moves a site to newSlug. Old slug is removed (no dual-live).
	// Returns ErrConflict if newSlug already exists, ErrNotFound if old missing,
	// ErrInvalidPath if either slug is illegal.
	RenameSite(ctx context.Context, oldSlug, newSlug string) (*site.Meta, error)

	ListVersions(ctx context.Context, slug string) ([]site.Version, error)
	Rollback(ctx context.Context, slug, versionID string) (*site.Version, error)

	OpenCurrentFile(ctx context.Context, slug, relPath string) (io.ReadCloser, *site.FileInfo, error)
	OpenVersionFile(ctx context.Context, slug, versionID, relPath string) (io.ReadCloser, *site.FileInfo, error)
	ListCurrentFiles(ctx context.Context, slug string) ([]site.FileInfo, error)
	ListVersionFiles(ctx context.Context, slug, versionID string) ([]site.FileInfo, error)

	Overview(ctx context.Context) (*site.StorageOverview, error)
}
