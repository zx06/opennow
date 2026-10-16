// Package github stores sites in a GitHub repository through the REST git
// data API.
//
// Every publish, rollback, settings change and delete is exactly one commit
// built from blobs → tree → commit → fast-forward ref update, so multi-file
// versions appear atomically. Uploads are staged on local disk until
// Finalize. Reads resolve paths through git trees and blobs, which are
// immutable and therefore cached in memory by SHA; the only thing that can go
// stale is the branch head, which is re-checked at most every MetaTTL with a
// conditional request (a 304 does not count against GitHub's rate limit).
//
// Repository layout (under an optional prefix) matches the S3 backend; see
// package common.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zx06/opennow/internal/mimetype"
	"github.com/zx06/opennow/internal/site"
	"github.com/zx06/opennow/internal/storage/cache"
	"github.com/zx06/opennow/internal/storage/common"
	"github.com/zx06/opennow/internal/storage/storeerr"
)

// Config configures the GitHub backend.
type Config struct {
	Token  string
	Owner  string
	Repo   string
	Branch string
	Prefix string
	APIURL string // default https://api.github.com (set for GitHub Enterprise)
	// DraftDir stages uploads until Finalize.
	DraftDir string
	// HeadTTL is how long the branch head is trusted before re-checking.
	HeadTTL time.Duration
	// BlobCacheBytes / BlobMaxObject bound the in-memory blob cache.
	BlobCacheBytes int64
	BlobMaxObject  int64
	// MaxFileSize rejects larger uploads (GitHub's hard limit is 100 MiB).
	MaxFileSize int64
	// WriteInterval spaces blob-creation requests to stay under GitHub's
	// secondary rate limits for content-creating requests.
	WriteInterval time.Duration
	// InlineMaxFile / InlineMaxTotal: small UTF-8 files are embedded in the
	// create-tree request instead of one create-blob request each.
	InlineMaxFile  int64
	InlineMaxTotal int64
}

const maxGitHubFile = 100 << 20

// ConfigFromEnv reads OPENNOW_GITHUB_* variables.
func ConfigFromEnv(dataDir string) Config {
	c := Config{
		Token:          firstEnv("OPENNOW_GITHUB_TOKEN", "GITHUB_TOKEN"),
		Branch:         firstEnv("OPENNOW_GITHUB_BRANCH"),
		Prefix:         os.Getenv("OPENNOW_GITHUB_PREFIX"),
		APIURL:         os.Getenv("OPENNOW_GITHUB_API_URL"),
		DraftDir:       filepath.Join(dataDir, "github-drafts"),
		HeadTTL:        30 * time.Second,
		BlobCacheBytes: 128 << 20,
		BlobMaxObject:  4 << 20,
		MaxFileSize:    maxGitHubFile,
		WriteInterval:  250 * time.Millisecond,
	}
	if owner, repo, ok := strings.Cut(os.Getenv("OPENNOW_GITHUB_REPO"), "/"); ok {
		c.Owner, c.Repo = owner, repo
	}
	if d, err := time.ParseDuration(os.Getenv("OPENNOW_CACHE_META_TTL")); err == nil && d >= 0 {
		c.HeadTTL = d
	}
	if n, err := strconv.ParseInt(os.Getenv("OPENNOW_CACHE_FILE_MB"), 10, 64); err == nil && n >= 0 {
		c.BlobCacheBytes = n << 20
	}
	if n, err := strconv.ParseInt(os.Getenv("OPENNOW_GITHUB_MAX_FILE_MB"), 10, 64); err == nil && n > 0 {
		c.MaxFileSize = min(n<<20, maxGitHubFile)
	}
	if d, err := time.ParseDuration(os.Getenv("OPENNOW_GITHUB_WRITE_INTERVAL")); err == nil && d >= 0 {
		c.WriteInterval = d
	}
	return c
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// Backend implements storage.Backend on a GitHub repository.
type Backend struct {
	cfg Config
	api repoAPI
	lay common.Layout

	commitMu sync.Mutex // serialises commits from this process

	headMu sync.Mutex
	head   headState

	trees     *cache.LRU[string, []TreeEntry]
	blobs     *cache.LRU[string, []byte]
	commits   *cache.LRU[string, string]
	metas     *cache.LRU[string, *site.Meta]            // by blob sha
	indexes   *cache.LRU[string, *common.VersionsIndex] // by blob sha
	manifests *cache.LRU[string, *common.Manifest]      // by slug/vid

	lastWrite time.Time
}

type headState struct {
	commit, tree string
	at           time.Time
}

// New validates config, connects to GitHub and initialises an empty branch.
func New(ctx context.Context, cfg Config) (*Backend, error) {
	if cfg.Owner == "" || cfg.Repo == "" {
		return nil, errors.New("github: OPENNOW_GITHUB_REPO=owner/repo is required")
	}
	if cfg.Token == "" {
		return nil, errors.New("github: OPENNOW_GITHUB_TOKEN is required")
	}
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	b, err := newWithAPI(cfg, newRESTClient(cfg.APIURL, cfg.Owner, cfg.Repo, cfg.Branch, cfg.Token))
	if err != nil {
		return nil, err
	}
	if _, err := b.currentHead(ctx, true); err != nil {
		return nil, err
	}
	return b, nil
}

func newWithAPI(cfg Config, api repoAPI) (*Backend, error) {
	if cfg.DraftDir == "" {
		cfg.DraftDir = filepath.Join(os.TempDir(), "opennow-github-drafts")
	}
	if cfg.MaxFileSize <= 0 || cfg.MaxFileSize > maxGitHubFile {
		cfg.MaxFileSize = maxGitHubFile
	}
	if cfg.BlobCacheBytes <= 0 {
		cfg.BlobCacheBytes = 128 << 20
	}
	if cfg.BlobMaxObject <= 0 {
		cfg.BlobMaxObject = 4 << 20
	}
	if cfg.InlineMaxFile <= 0 {
		cfg.InlineMaxFile = 512 << 10
	}
	if cfg.InlineMaxTotal <= 0 {
		cfg.InlineMaxTotal = 16 << 20
	}
	if err := os.MkdirAll(cfg.DraftDir, 0o700); err != nil {
		return nil, err
	}
	cleanupStaleDrafts(cfg.DraftDir, 48*time.Hour)
	return &Backend{
		cfg:       cfg,
		api:       api,
		lay:       common.NewLayout(cfg.Prefix),
		trees:     cache.New[string, []TreeEntry](4096, 0, nil),
		blobs:     cache.New[string, []byte](cfg.BlobCacheBytes, 0, func(b []byte) int64 { return int64(len(b)) }),
		commits:   cache.New[string, string](1024, 0, nil),
		metas:     cache.New[string, *site.Meta](10000, 0, nil),
		indexes:   cache.New[string, *common.VersionsIndex](10000, 0, nil),
		manifests: cache.New[string, *common.Manifest](4096, 0, nil),
	}, nil
}

func cleanupStaleDrafts(dir string, age time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > age {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

func (b *Backend) Name() string { return "github" }

// ---------------------------------------------------------------------------
// Head, trees & blobs

func (b *Backend) currentHead(ctx context.Context, fresh bool) (headState, error) {
	b.headMu.Lock()
	h := b.head
	b.headMu.Unlock()
	if !fresh && h.commit != "" && time.Since(h.at) < b.cfg.HeadTTL {
		return h, nil
	}
	commit, err := b.api.GetRef(ctx)
	if errors.Is(err, ErrEmptyRepo) {
		marker := b.lay.Prefix + ".opennow"
		if ierr := b.api.InitBranch(ctx, marker, []byte("opennow storage root\n"), "opennow: initialise storage"); ierr != nil {
			return headState{}, fmt.Errorf("github: initialise branch: %w", ierr)
		}
		commit, err = b.api.GetRef(ctx)
	}
	if err != nil {
		// Serve from the last known head if GitHub is unavailable/limited.
		if h.commit != "" && !fresh {
			return h, nil
		}
		return headState{}, err
	}
	tree, ok := b.commits.Get(commit)
	if !ok {
		if tree, err = b.api.GetCommitTree(ctx, commit); err != nil {
			return headState{}, err
		}
		b.commits.Set(commit, tree)
	}
	h = headState{commit: commit, tree: tree, at: time.Now()}
	b.setHead(h)
	return h, nil
}

func (b *Backend) setHead(h headState) {
	b.headMu.Lock()
	b.head = h
	b.headMu.Unlock()
}

func (b *Backend) tree(ctx context.Context, sha string) ([]TreeEntry, error) {
	if t, ok := b.trees.Get(sha); ok {
		return t, nil
	}
	t, err := b.api.GetTree(ctx, sha)
	if err != nil {
		return nil, err
	}
	b.trees.Set(sha, t)
	return t, nil
}

// lookup resolves a slash path inside root.
func (b *Backend) lookup(ctx context.Context, root, p string) (TreeEntry, error) {
	cur := TreeEntry{Type: "tree", SHA: root, Mode: "040000"}
	p = strings.Trim(p, "/")
	if p == "" {
		return cur, nil
	}
	for _, seg := range strings.Split(p, "/") {
		if cur.Type != "tree" {
			return TreeEntry{}, storeerr.ErrNotFound
		}
		entries, err := b.tree(ctx, cur.SHA)
		if err != nil {
			return TreeEntry{}, err
		}
		found := false
		for _, e := range entries {
			if e.Path == seg {
				cur, found = e, true
				break
			}
		}
		if !found {
			return TreeEntry{}, storeerr.ErrNotFound
		}
	}
	return cur, nil
}

func (b *Backend) readBlob(ctx context.Context, sha string) ([]byte, error) {
	if d, ok := b.blobs.Get(sha); ok {
		return d, nil
	}
	rc, err := b.api.GetBlob(ctx, sha)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	d, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	if int64(len(d)) <= b.cfg.BlobMaxObject {
		b.blobs.Set(sha, d)
	}
	return d, nil
}

func (b *Backend) readJSONAt(ctx context.Context, root, p string, v any) (string, error) {
	e, err := b.lookup(ctx, root, p)
	if err != nil {
		return "", err
	}
	if e.Type != "blob" {
		return "", storeerr.ErrNotFound
	}
	data, err := b.readBlob(ctx, e.SHA)
	if err != nil {
		return "", err
	}
	return e.SHA, json.Unmarshal(data, v)
}

func (b *Backend) metaAt(ctx context.Context, root, slug string) (*site.Meta, error) {
	if !common.ValidID(slug) {
		return nil, storeerr.ErrNotFound
	}
	e, err := b.lookup(ctx, root, b.lay.Meta(slug))
	if err != nil {
		return nil, err
	}
	if m, ok := b.metas.Get(e.SHA); ok {
		c := *m
		return &c, nil
	}
	data, err := b.readBlob(ctx, e.SHA)
	if err != nil {
		return nil, err
	}
	var m site.Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	c := m
	b.metas.Set(e.SHA, &c)
	return &m, nil
}

func (b *Backend) indexAt(ctx context.Context, root, slug string) (*common.VersionsIndex, error) {
	e, err := b.lookup(ctx, root, b.lay.VersionsIndex(slug))
	if errors.Is(err, storeerr.ErrNotFound) {
		return &common.VersionsIndex{}, nil
	}
	if err != nil {
		return nil, err
	}
	if v, ok := b.indexes.Get(e.SHA); ok {
		return &common.VersionsIndex{Versions: append([]common.VersionEntry(nil), v.Versions...)}, nil
	}
	data, err := b.readBlob(ctx, e.SHA)
	if err != nil {
		return nil, err
	}
	var idx common.VersionsIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, err
	}
	b.indexes.Set(e.SHA, &common.VersionsIndex{Versions: append([]common.VersionEntry(nil), idx.Versions...)})
	return &idx, nil
}

func (b *Backend) manifestAt(ctx context.Context, root, slug, vid string) (*common.Manifest, error) {
	if !common.ValidID(slug) || !common.ValidID(vid) {
		return nil, storeerr.ErrNotFound
	}
	key := slug + "/" + vid
	if m, ok := b.manifests.Get(key); ok {
		return m, nil
	}
	var m common.Manifest
	if _, err := b.readJSONAt(ctx, root, b.lay.Manifest(slug, vid), &m); err != nil {
		return nil, err
	}
	m.Sort()
	b.manifests.Set(key, &m)
	return &m, nil
}

// ---------------------------------------------------------------------------
// Commits

// commit builds and pushes one commit, retrying when the branch moved.
func (b *Backend) commit(ctx context.Context, msg string, build func(h headState) ([]Change, error)) (headState, error) {
	b.commitMu.Lock()
	defer b.commitMu.Unlock()
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		h, err := b.currentHead(ctx, true)
		if err != nil {
			return headState{}, err
		}
		changes, err := build(h)
		if err != nil {
			return headState{}, err
		}
		tree, err := b.api.CreateTree(ctx, h.tree, changes)
		if err != nil {
			return headState{}, err
		}
		commit, err := b.api.CreateCommit(ctx, msg, tree, h.commit)
		if err != nil {
			return headState{}, err
		}
		if err := b.api.UpdateRef(ctx, commit); err != nil {
			if errors.Is(err, ErrConflict) {
				lastErr = err
				continue
			}
			return headState{}, err
		}
		nh := headState{commit: commit, tree: tree, at: time.Now()}
		b.commits.Set(commit, tree)
		b.setHead(nh)
		return nh, nil
	}
	return headState{}, lastErr
}

func jsonChange(p string, v any) (Change, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return Change{}, err
	}
	s := string(data) + "\n"
	return Change{Path: p, Mode: "100644", Type: "blob", Content: &s}, nil
}

func (b *Backend) throttleWrite(ctx context.Context) error {
	if b.cfg.WriteInterval <= 0 {
		return nil
	}
	if wait := b.cfg.WriteInterval - time.Since(b.lastWrite); wait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	b.lastWrite = time.Now()
	return nil
}

// ---------------------------------------------------------------------------
// Publish flow

func (b *Backend) draftDir(rev string) string { return filepath.Join(b.cfg.DraftDir, rev) }

func (b *Backend) CreateRevision(ctx context.Context, slug string) (*site.DraftRevision, error) {
	slug, err := common.CheckSlug(slug)
	if err != nil {
		return nil, err
	}
	d := common.Draft{ID: common.NewID(), Slug: slug, VersionID: common.NewID(), CreatedAt: time.Now().UTC()}
	dir := b.draftDir(d.ID)
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o700); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(d)
	if err := os.WriteFile(filepath.Join(dir, "draft.json"), data, 0o600); err != nil {
		return nil, err
	}
	return &site.DraftRevision{ID: d.ID, Slug: slug, CreatedAt: d.CreatedAt}, nil
}

func (b *Backend) readDraft(rev string) (*common.Draft, error) {
	if !common.ValidID(rev) {
		return nil, storeerr.ErrDraftNotFound
	}
	data, err := os.ReadFile(filepath.Join(b.draftDir(rev), "draft.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, storeerr.ErrDraftNotFound
		}
		return nil, err
	}
	var d common.Draft
	return &d, json.Unmarshal(data, &d)
}

func (b *Backend) Upload(ctx context.Context, revisionID, relPath string, r io.Reader, size int64) error {
	rel, err := common.CleanRelPath(relPath)
	if err != nil {
		return err
	}
	if _, err := b.readDraft(revisionID); err != nil {
		return err
	}
	if size > b.cfg.MaxFileSize {
		return fmt.Errorf("%w: %s is %d bytes, limit %d", storeerr.ErrTooLarge, rel, size, b.cfg.MaxFileSize)
	}
	dest := filepath.Join(b.draftDir(revisionID), "files", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, b.cfg.MaxFileSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > b.cfg.MaxFileSize {
		_ = os.Remove(dest)
		return fmt.Errorf("%w: %s exceeds %d bytes", storeerr.ErrTooLarge, rel, b.cfg.MaxFileSize)
	}
	return err
}

func isText(data []byte) bool {
	return utf8.Valid(data) && !bytes.ContainsRune(data, 0)
}

func (b *Backend) Finalize(ctx context.Context, revisionID string) (*site.Version, string, error) {
	d, err := b.readDraft(revisionID)
	if err != nil {
		return nil, "", err
	}
	filesDir := filepath.Join(b.draftDir(revisionID), "files")
	var rels []string
	err = filepath.WalkDir(filesDir, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		rel, err := filepath.Rel(filesDir, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	uploaded := map[string]bool{} // blobs created during this publish (kept across retries)
	var man *common.Manifest
	_, err = b.commit(ctx, fmt.Sprintf("opennow: publish %s (%s)", d.Slug, d.VersionID), func(h headState) ([]Change, error) {
		meta, err := b.metaAt(ctx, h.tree, d.Slug)
		if err != nil && !errors.Is(err, storeerr.ErrNotFound) {
			return nil, err
		}
		known := map[string]bool{}
		if meta != nil && meta.CurrentVersion != "" {
			if prev, err := b.manifestAt(ctx, h.tree, d.Slug, meta.CurrentVersion); err == nil {
				for _, f := range prev.Files {
					if f.SHA != "" {
						known[f.SHA] = true
					}
				}
			}
		}
		man = &common.Manifest{VersionID: d.VersionID, Slug: d.Slug, CreatedAt: now}
		var changes []Change
		var inlined int64
		for _, rel := range rels {
			data, err := os.ReadFile(filepath.Join(filesDir, filepath.FromSlash(rel)))
			if err != nil {
				return nil, err
			}
			sha := GitBlobSHA(data)
			ch := Change{Path: b.lay.File(d.Slug, d.VersionID, rel), Mode: "100644", Type: "blob", SHA: sha}
			switch {
			case known[sha] || uploaded[sha]:
				// Unchanged since the previous version: reuse the blob, no upload.
			case isText(data) && int64(len(data)) <= b.cfg.InlineMaxFile && inlined+int64(len(data)) <= b.cfg.InlineMaxTotal:
				s := string(data)
				ch.Content, ch.SHA = &s, ""
				inlined += int64(len(data))
			default:
				if err := b.throttleWrite(ctx); err != nil {
					return nil, err
				}
				got, err := b.api.CreateBlob(ctx, data)
				if err != nil {
					return nil, fmt.Errorf("upload %s: %w", rel, err)
				}
				if got != sha {
					return nil, fmt.Errorf("github: blob sha mismatch for %s: %s != %s", rel, got, sha)
				}
				uploaded[sha] = true
			}
			if int64(len(data)) <= b.cfg.BlobMaxObject {
				b.blobs.Set(sha, data)
			}
			changes = append(changes, ch)
			man.Files = append(man.Files, common.ManifestFile{Path: rel, Size: int64(len(data)), ContentType: mimetype.ForName(rel), SHA: sha})
		}
		man.Sort()
		idx, err := b.indexAt(ctx, h.tree, d.Slug)
		if err != nil {
			return nil, err
		}
		idx.Versions = append(idx.Versions, common.VersionEntry{ID: d.VersionID, CreatedAt: now, FileCount: len(man.Files), TotalBytes: man.TotalBytes()})
		if meta == nil {
			meta = &site.Meta{Slug: d.Slug, CreatedAt: now}
		}
		meta.CurrentVersion = d.VersionID
		meta.UpdatedAt = now
		for _, jc := range []struct {
			p string
			v any
		}{{b.lay.Manifest(d.Slug, d.VersionID), man}, {b.lay.VersionsIndex(d.Slug), idx}, {b.lay.Meta(d.Slug), meta}} {
			c, err := jsonChange(jc.p, jc.v)
			if err != nil {
				return nil, err
			}
			changes = append(changes, c)
		}
		return changes, nil
	})
	if err != nil {
		return nil, "", err
	}
	b.manifests.Set(d.Slug+"/"+d.VersionID, man)
	_ = os.RemoveAll(b.draftDir(revisionID))
	return &site.Version{ID: d.VersionID, CreatedAt: now}, d.Slug, nil
}

// ---------------------------------------------------------------------------
// Meta & versions

func (b *Backend) GetMeta(ctx context.Context, slug string) (*site.Meta, error) {
	if !common.ValidID(slug) {
		return nil, storeerr.ErrNotFound
	}
	h, err := b.currentHead(ctx, false)
	if err != nil {
		return nil, err
	}
	return b.metaAt(ctx, h.tree, slug)
}

func (b *Backend) PutMeta(ctx context.Context, meta *site.Meta) error {
	if !common.ValidID(meta.Slug) {
		return fmt.Errorf("invalid slug")
	}
	meta.UpdatedAt = time.Now().UTC()
	_, err := b.commit(ctx, "opennow: update settings for "+meta.Slug, func(h headState) ([]Change, error) {
		if _, err := b.metaAt(ctx, h.tree, meta.Slug); err != nil {
			return nil, err
		}
		c, err := jsonChange(b.lay.Meta(meta.Slug), meta)
		return []Change{c}, err
	})
	return err
}

func (b *Backend) ListSites(ctx context.Context) ([]site.Summary, error) {
	h, err := b.currentHead(ctx, false)
	if err != nil {
		return nil, err
	}
	out := []site.Summary{}
	e, err := b.lookup(ctx, h.tree, strings.TrimSuffix(b.lay.SitesRoot(), "/"))
	if errors.Is(err, storeerr.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := b.tree(ctx, e.SHA)
	if err != nil {
		return nil, err
	}
	for _, se := range entries {
		if se.Type != "tree" {
			continue
		}
		m, err := b.metaAt(ctx, h.tree, se.Path)
		if err != nil {
			continue
		}
		var man *common.Manifest
		if m.CurrentVersion != "" {
			man, _ = b.manifestAt(ctx, h.tree, m.Slug, m.CurrentVersion)
		}
		out = append(out, common.Summarize(m, man))
	}
	return out, nil
}

func (b *Backend) DeleteSite(ctx context.Context, slug string) error {
	if !common.ValidID(slug) {
		return storeerr.ErrNotFound
	}
	_, err := b.commit(ctx, "opennow: delete "+slug, func(h headState) ([]Change, error) {
		if _, err := b.lookup(ctx, h.tree, strings.TrimSuffix(b.lay.SiteRoot(slug), "/")); err != nil {
			return nil, err
		}
		// A null SHA on a tree path removes the whole directory in one request.
		return []Change{{Path: strings.TrimSuffix(b.lay.SiteRoot(slug), "/"), Mode: "040000", Type: "tree", Delete: true}}, nil
	})
	if errors.Is(err, storeerr.ErrNotFound) {
		return nil
	}
	pfx := slug + "/"
	b.manifests.DeleteFunc(func(k string) bool { return strings.HasPrefix(k, pfx) })
	return err
}

func (b *Backend) ListVersions(ctx context.Context, slug string) ([]site.Version, error) {
	if !common.ValidID(slug) {
		return nil, storeerr.ErrNotFound
	}
	h, err := b.currentHead(ctx, false)
	if err != nil {
		return nil, err
	}
	idx, err := b.indexAt(ctx, h.tree, slug)
	if err != nil {
		return nil, err
	}
	return idx.SiteVersions(), nil
}

func (b *Backend) Rollback(ctx context.Context, slug, versionID string) (*site.Version, error) {
	if !common.ValidID(slug) || !common.ValidID(versionID) {
		return nil, storeerr.ErrNotFound
	}
	now := time.Now().UTC()
	_, err := b.commit(ctx, fmt.Sprintf("opennow: rollback %s to %s", slug, versionID), func(h headState) ([]Change, error) {
		idx, err := b.indexAt(ctx, h.tree, slug)
		if err != nil {
			return nil, err
		}
		if !idx.Has(versionID) {
			return nil, storeerr.ErrNotFound
		}
		meta, err := b.metaAt(ctx, h.tree, slug)
		if err != nil {
			return nil, err
		}
		meta.CurrentVersion = versionID
		meta.UpdatedAt = now
		c, err := jsonChange(b.lay.Meta(slug), meta)
		return []Change{c}, err
	})
	if err != nil {
		return nil, err
	}
	return &site.Version{ID: versionID, CreatedAt: now}, nil
}

// ---------------------------------------------------------------------------
// Files

func (b *Backend) OpenCurrentFile(ctx context.Context, slug, relPath string) (io.ReadCloser, *site.FileInfo, error) {
	m, err := b.GetMeta(ctx, slug)
	if err != nil {
		return nil, nil, err
	}
	return b.OpenVersionFile(ctx, slug, m.CurrentVersion, relPath)
}

func (b *Backend) OpenVersionFile(ctx context.Context, slug, versionID, relPath string) (io.ReadCloser, *site.FileInfo, error) {
	rel, err := common.CleanRelPath(relPath)
	if err != nil {
		return nil, nil, storeerr.ErrNotFound
	}
	h, err := b.currentHead(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	man, err := b.manifestAt(ctx, h.tree, slug, versionID)
	if err != nil {
		return nil, nil, err
	}
	f, ok := man.Lookup(rel)
	if !ok {
		return nil, nil, storeerr.ErrNotFound
	}
	info := &site.FileInfo{Path: rel, Size: f.Size, ContentType: f.ContentType}
	sha := f.SHA
	if sha == "" {
		e, err := b.lookup(ctx, h.tree, b.lay.File(slug, versionID, rel))
		if err != nil {
			return nil, nil, err
		}
		sha = e.SHA
	}
	if d, ok := b.blobs.Get(sha); ok {
		return io.NopCloser(bytes.NewReader(d)), info, nil
	}
	if f.Size <= b.cfg.BlobMaxObject {
		d, err := b.readBlob(ctx, sha)
		if err != nil {
			return nil, nil, err
		}
		return io.NopCloser(bytes.NewReader(d)), info, nil
	}
	rc, err := b.api.GetBlob(ctx, sha) // stream large blobs
	if err != nil {
		return nil, nil, err
	}
	return rc, info, nil
}

func (b *Backend) ListCurrentFiles(ctx context.Context, slug string) ([]site.FileInfo, error) {
	m, err := b.GetMeta(ctx, slug)
	if err != nil {
		return nil, err
	}
	if m.CurrentVersion == "" {
		return nil, nil
	}
	return b.ListVersionFiles(ctx, slug, m.CurrentVersion)
}

func (b *Backend) ListVersionFiles(ctx context.Context, slug, versionID string) ([]site.FileInfo, error) {
	h, err := b.currentHead(ctx, false)
	if err != nil {
		return nil, err
	}
	man, err := b.manifestAt(ctx, h.tree, slug, versionID)
	if err != nil {
		return nil, err
	}
	return man.FileInfos(), nil
}

// ---------------------------------------------------------------------------
// Overview

func (b *Backend) Overview(ctx context.Context) (*site.StorageOverview, error) {
	sites, err := b.ListSites(ctx)
	if err != nil {
		return nil, err
	}
	var total int64
	for _, s := range sites {
		total += s.TotalBytes
	}
	b.headMu.Lock()
	h := b.head
	b.headMu.Unlock()
	ri := b.api.RateInfo()
	rate := "—"
	if ri.Limit > 0 {
		rate = fmt.Sprintf("%d / %d（%s 重置）", ri.Remaining, ri.Limit, ri.Reset.Local().Format("15:04:05"))
	}
	if time.Now().Before(ri.BlockedUntil) {
		rate += "，限流至 " + ri.BlockedUntil.Local().Format("15:04:05")
	}
	bs := b.blobs.Stats()
	short := h.commit
	if len(short) > 12 {
		short = short[:12]
	}
	repo := b.cfg.Owner + "/" + b.cfg.Repo
	api := b.cfg.APIURL
	if api == "" {
		api = "https://api.github.com"
	}
	return &site.StorageOverview{
		Backend:    "github",
		SiteCount:  len(sites),
		TotalBytes: total,
		Location:   repo + "@" + b.cfg.Branch + "/" + b.lay.Prefix,
		Details: []site.Detail{
			{Label: "仓库", Value: repo},
			{Label: "分支", Value: b.cfg.Branch},
			{Label: "前缀", Value: orDash(b.lay.Prefix)},
			{Label: "API", Value: api},
			{Label: "HEAD", Value: orDash(short)},
			{Label: "API 配额", Value: rate},
			{Label: "API 请求（本进程）", Value: fmt.Sprintf("%d 次，其中 %d 次 304 未计配额", ri.Requests, ri.NotModified)},
			{Label: "HEAD 缓存 TTL", Value: b.cfg.HeadTTL.String()},
			{Label: "Blob 缓存", Value: fmt.Sprintf("%d 个，%s / %s（命中 %d / %d）", bs.Entries, common.HumanBytes(bs.Cost), common.HumanBytes(bs.MaxCost), bs.Hits, bs.Hits+bs.Misses)},
			{Label: "单文件上限", Value: common.HumanBytes(b.cfg.MaxFileSize)},
		},
		Notes: []string{
			"每次发布/回滚/设置修改/删除都是一个 commit；历史永久保留在 git 中，删除站点不会缩小仓库。",
			"单文件不超过 100 MiB；仓库建议保持在 1–5 GiB 以内。",
			"meta.json 含密码哈希：请使用私有仓库。",
		},
	}, nil
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
