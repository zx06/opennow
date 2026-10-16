package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zx06/opennow/internal/site"
	"github.com/zx06/opennow/internal/storage/storeerr"
)

// Sentinel errors are shared with the other backends via storeerr so callers
// can match them regardless of which backend is configured.
var (
	ErrNotFound      = storeerr.ErrNotFound
	ErrDraftNotFound = storeerr.ErrDraftNotFound
	ErrInvalidPath   = storeerr.ErrInvalidPath
)

// Backend stores sites on local disk.
type Backend struct {
	Root string
}

func New(root string) (*Backend, error) {
	if root == "" {
		root = "./data"
	}
	if err := os.MkdirAll(filepath.Join(root, "sites"), 0o755); err != nil {
		return nil, err
	}
	return &Backend{Root: root}, nil
}

func (b *Backend) Name() string { return "local" }

func (b *Backend) siteDir(slug string) string {
	return filepath.Join(b.Root, "sites", slug)
}

func (b *Backend) metaPath(slug string) string {
	return filepath.Join(b.siteDir(slug), "meta.json")
}

func (b *Backend) draftDir(revisionID string) string {
	return filepath.Join(b.Root, "drafts", revisionID)
}

func (b *Backend) versionDir(slug, versionID string) string {
	return filepath.Join(b.siteDir(slug), "versions", versionID)
}

func (b *Backend) readMeta(slug string) (*site.Meta, error) {
	data, err := os.ReadFile(b.metaPath(slug))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var m site.Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (b *Backend) writeMeta(m *site.Meta) error {
	if err := os.MkdirAll(b.siteDir(m.Slug), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.metaPath(m.Slug), data, 0o644)
}

func sanitizeRelPath(p string) (string, error) {
	p = strings.TrimPrefix(p, "/")
	p = filepath.Clean(p)
	if p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}
	return filepath.ToSlash(p), nil
}

func (b *Backend) CreateRevision(ctx context.Context, slug string) (*site.DraftRevision, error) {
	if slug == "" {
		slug = strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	}
	if strings.Contains(slug, "/") || strings.Contains(slug, "..") {
		return nil, fmt.Errorf("invalid slug")
	}
	id := uuid.New().String()
	dir := b.draftDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	meta := draftMeta{Slug: slug, CreatedAt: time.Now().UTC()}
	data, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "draft.json"), data, 0o644); err != nil {
		return nil, err
	}
	return &site.DraftRevision{ID: id, Slug: slug, CreatedAt: meta.CreatedAt}, nil
}

type draftMeta struct {
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
}

func (b *Backend) readDraft(revisionID string) (*draftMeta, string, error) {
	dir := b.draftDir(revisionID)
	data, err := os.ReadFile(filepath.Join(dir, "draft.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", ErrDraftNotFound
		}
		return nil, "", err
	}
	var m draftMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, "", err
	}
	return &m, dir, nil
}

func (b *Backend) Upload(ctx context.Context, revisionID, relPath string, r io.Reader, size int64) error {
	relPath, err := sanitizeRelPath(relPath)
	if err != nil {
		return err
	}
	_, dir, err := b.readDraft(revisionID)
	if err != nil {
		return err
	}
	dest := filepath.Join(dir, "files", relPath)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

func (b *Backend) Finalize(ctx context.Context, revisionID string) (*site.Version, string, error) {
	dm, draftDir, err := b.readDraft(revisionID)
	if err != nil {
		return nil, "", err
	}
	slug := dm.Slug
	versionID := uuid.New().String()
	vdir := b.versionDir(slug, versionID)
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return nil, "", err
	}
	srcFiles := filepath.Join(draftDir, "files")
	if err := copyTree(srcFiles, vdir); err != nil && !os.IsNotExist(err) {
		return nil, "", err
	}
	now := time.Now().UTC()
	meta, err := b.readMeta(slug)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, "", err
	}
	if meta == nil {
		meta = &site.Meta{Slug: slug, CreatedAt: now}
	}
	meta.CurrentVersion = versionID
	meta.UpdatedAt = now
	if err := b.writeMeta(meta); err != nil {
		return nil, "", err
	}
	_ = os.RemoveAll(draftDir)
	return &site.Version{ID: versionID, CreatedAt: now}, slug, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func (b *Backend) GetMeta(ctx context.Context, slug string) (*site.Meta, error) {
	return b.readMeta(slug)
}

func (b *Backend) PutMeta(ctx context.Context, meta *site.Meta) error {
	meta.UpdatedAt = time.Now().UTC()
	return b.writeMeta(meta)
}

func (b *Backend) ListSites(ctx context.Context) ([]site.Summary, error) {
	sitesRoot := filepath.Join(b.Root, "sites")
	entries, err := os.ReadDir(sitesRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []site.Summary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := b.readMeta(e.Name())
		if err != nil {
			continue
		}
		sum := site.Summary{
			Slug:           m.Slug,
			CurrentVersion: m.CurrentVersion,
			HasPassword:    m.PasswordHash != "",
			ExpiresAt:      m.ExpiresAt,
			UpdatedAt:      m.UpdatedAt,
		}
		files, _ := b.ListCurrentFiles(ctx, m.Slug)
		sum.FileCount = len(files)
		for _, f := range files {
			sum.TotalBytes += f.Size
		}
		out = append(out, sum)
	}
	return out, nil
}

func (b *Backend) DeleteSite(ctx context.Context, slug string) error {
	return os.RemoveAll(b.siteDir(slug))
}

func (b *Backend) ListVersions(ctx context.Context, slug string) ([]site.Version, error) {
	vroot := filepath.Join(b.siteDir(slug), "versions")
	entries, err := os.ReadDir(vroot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []site.Version
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, site.Version{ID: e.Name(), CreatedAt: info.ModTime().UTC()})
	}
	return out, nil
}

func (b *Backend) Rollback(ctx context.Context, slug, versionID string) (*site.Version, error) {
	if !validID(slug) || !validID(versionID) {
		return nil, ErrNotFound
	}
	if _, err := os.Stat(b.versionDir(slug, versionID)); err != nil {
		return nil, ErrNotFound
	}
	meta, err := b.readMeta(slug)
	if err != nil {
		return nil, err
	}
	meta.CurrentVersion = versionID
	meta.UpdatedAt = time.Now().UTC()
	if err := b.writeMeta(meta); err != nil {
		return nil, err
	}
	return &site.Version{ID: versionID, CreatedAt: time.Now().UTC()}, nil
}

func (b *Backend) listFilesInDir(root string) ([]site.FileInfo, error) {
	var out []site.FileInfo
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, site.FileInfo{
			Path: filepath.ToSlash(rel),
			Size: info.Size(),
		})
		return nil
	})
	return out, err
}

func (b *Backend) ListCurrentFiles(ctx context.Context, slug string) ([]site.FileInfo, error) {
	meta, err := b.readMeta(slug)
	if err != nil {
		return nil, err
	}
	if meta.CurrentVersion == "" {
		return nil, nil
	}
	return b.listFilesInDir(b.versionDir(slug, meta.CurrentVersion))
}

// ListVersionFiles lists the files of a specific (possibly non-current) version.
func (b *Backend) ListVersionFiles(ctx context.Context, slug, versionID string) ([]site.FileInfo, error) {
	if !validID(slug) || !validID(versionID) {
		return nil, ErrNotFound
	}
	dir := b.versionDir(slug, versionID)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, ErrNotFound
	}
	return b.listFilesInDir(dir)
}

// validID rejects empty values and anything that could escape a directory.
func validID(s string) bool {
	return s != "" && s != "." && !strings.Contains(s, "..") && !strings.ContainsAny(s, "/\\")
}

func (b *Backend) OpenCurrentFile(ctx context.Context, slug, relPath string) (io.ReadCloser, *site.FileInfo, error) {
	meta, err := b.readMeta(slug)
	if err != nil {
		return nil, nil, err
	}
	return b.OpenVersionFile(ctx, slug, meta.CurrentVersion, relPath)
}

func (b *Backend) OpenVersionFile(ctx context.Context, slug, versionID, relPath string) (io.ReadCloser, *site.FileInfo, error) {
	if !validID(slug) || !validID(versionID) {
		return nil, nil, ErrNotFound
	}
	relPath, err := sanitizeRelPath(relPath)
	if err != nil {
		return nil, nil, err
	}
	full := filepath.Join(b.versionDir(slug, versionID), relPath)
	st, err := os.Stat(full)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	if st.IsDir() {
		return nil, nil, ErrNotFound
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, nil, err
	}
	return f, &site.FileInfo{Path: relPath, Size: st.Size()}, nil
}

func (b *Backend) Overview(ctx context.Context) (*site.StorageOverview, error) {
	sites, err := b.ListSites(ctx)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, s := range sites {
		total += s.TotalBytes
	}
	return &site.StorageOverview{
		Backend:    "local",
		SiteCount:  len(sites),
		TotalBytes: total,
		DataPath:   b.Root,
		Location:   b.Root,
		Details:    []site.Detail{{Label: "数据目录", Value: b.Root}},
	}, nil
}
