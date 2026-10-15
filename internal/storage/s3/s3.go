package s3

import (
	"context"
	"errors"
	"io"

	"github.com/zx06/opennow/internal/site"
)

var ErrNotImplemented = errors.New("s3 backend: not implemented in MVP")

// Backend is a placeholder for S3-compatible storage.
type Backend struct{}

func New(_ string, _ string, _ string, _ string) *Backend { return &Backend{} }

func (b *Backend) Name() string { return "s3" }

func (b *Backend) CreateRevision(ctx context.Context, slug string) (*site.DraftRevision, error) {
	return nil, ErrNotImplemented
}
func (b *Backend) Upload(ctx context.Context, revisionID, relPath string, r io.Reader, size int64) error {
	return ErrNotImplemented
}
func (b *Backend) Finalize(ctx context.Context, revisionID string) (*site.Version, string, error) {
	return nil, "", ErrNotImplemented
}
func (b *Backend) GetMeta(ctx context.Context, slug string) (*site.Meta, error) {
	return nil, ErrNotImplemented
}
func (b *Backend) PutMeta(ctx context.Context, meta *site.Meta) error { return ErrNotImplemented }
func (b *Backend) ListSites(ctx context.Context) ([]site.Summary, error) {
	return nil, ErrNotImplemented
}
func (b *Backend) DeleteSite(ctx context.Context, slug string) error { return ErrNotImplemented }
func (b *Backend) ListVersions(ctx context.Context, slug string) ([]site.Version, error) {
	return nil, ErrNotImplemented
}
func (b *Backend) Rollback(ctx context.Context, slug, versionID string) (*site.Version, error) {
	return nil, ErrNotImplemented
}
func (b *Backend) OpenCurrentFile(ctx context.Context, slug, relPath string) (io.ReadCloser, *site.FileInfo, error) {
	return nil, nil, ErrNotImplemented
}
func (b *Backend) OpenVersionFile(ctx context.Context, slug, versionID, relPath string) (io.ReadCloser, *site.FileInfo, error) {
	return nil, nil, ErrNotImplemented
}
func (b *Backend) ListCurrentFiles(ctx context.Context, slug string) ([]site.FileInfo, error) {
	return nil, ErrNotImplemented
}
func (b *Backend) Overview(ctx context.Context) (*site.StorageOverview, error) {
	return nil, ErrNotImplemented
}
