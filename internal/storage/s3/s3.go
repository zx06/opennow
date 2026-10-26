// Package s3 stores sites in any S3-compatible object store (AWS S3, MinIO,
// Cloudflare R2, Backblaze B2, ...). It uses minio-go, which is pure Go.
//
// Request budget: serving a file costs at most one GET for meta.json (cached
// for MetaTTL, negative results included), one GET for the version manifest
// (cached forever: versions are immutable) and one GET for the file itself
// (small files are cached in an in-memory LRU). LIST is only used by admin
// operations (site listing, delete) and as a fallback when finalising a
// publish whose draft was created by another process.
package s3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zx06/opennow/internal/mimetype"
	"github.com/zx06/opennow/internal/site"
	"github.com/zx06/opennow/internal/storage/cache"
	"github.com/zx06/opennow/internal/storage/common"
	"github.com/zx06/opennow/internal/storage/storeerr"
)

// Config configures the backend.
type Config struct {
	// Name is reported by Name()/Overview ("s3" or "r2").
	Name   string
	Client ClientConfig
	Prefix string
	// CreateBucket creates the bucket on startup if missing.
	CreateBucket bool
	Cache        CacheConfig
}

// CacheConfig tunes the in-memory caches.
type CacheConfig struct {
	// MetaTTL bounds how stale site meta / version index / site list may be
	// when another instance writes to the same bucket. Writes made through
	// this process update the cache immediately.
	MetaTTL time.Duration
	// FileBytes is the total in-memory budget for hot files.
	FileBytes int64
	// FileMaxObject is the largest single file kept in memory.
	FileMaxObject int64
	// FileTTL expires cached file bodies (0 = until evicted; versions are
	// immutable so this is safe).
	FileTTL time.Duration
}

// DefaultCache returns sane cache defaults.
func DefaultCache() CacheConfig {
	return CacheConfig{MetaTTL: 30 * time.Second, FileBytes: 64 << 20, FileMaxObject: 1 << 20, FileTTL: time.Hour}
}

// CacheFromEnv overlays OPENNOW_CACHE_* settings on def.
func CacheFromEnv(def CacheConfig) CacheConfig {
	if d, err := time.ParseDuration(os.Getenv("OPENNOW_CACHE_META_TTL")); err == nil && d >= 0 {
		def.MetaTTL = d
	}
	if n, err := strconv.ParseInt(os.Getenv("OPENNOW_CACHE_FILE_MB"), 10, 64); err == nil && n >= 0 {
		def.FileBytes = n << 20
	}
	if n, err := strconv.ParseInt(os.Getenv("OPENNOW_CACHE_FILE_MAX_KB"), 10, 64); err == nil && n >= 0 {
		def.FileMaxObject = n << 10
	}
	if d, err := time.ParseDuration(os.Getenv("OPENNOW_CACHE_FILE_TTL")); err == nil && d >= 0 {
		def.FileTTL = d
	}
	return def
}

func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func envBool(k string) bool {
	v := strings.ToLower(os.Getenv(k))
	return v == "1" || v == "true" || v == "yes"
}

// ConfigFromEnv reads OPENNOW_S3_* variables.
func ConfigFromEnv() Config {
	return Config{
		Name: "s3",
		Client: ClientConfig{
			Endpoint:  envFirst("OPENNOW_S3_ENDPOINT"),
			Region:    envFirst("OPENNOW_S3_REGION", "AWS_REGION"),
			Bucket:    os.Getenv("OPENNOW_S3_BUCKET"),
			AccessKey: envFirst("OPENNOW_S3_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"),
			SecretKey: envFirst("OPENNOW_S3_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY"),
			PathStyle: os.Getenv("OPENNOW_S3_PATH_STYLE"),
			Insecure:  envBool("OPENNOW_S3_INSECURE"),
		},
		Prefix:       os.Getenv("OPENNOW_S3_PREFIX"),
		CreateBucket: envBool("OPENNOW_S3_CREATE_BUCKET"),
		Cache:        CacheFromEnv(DefaultCache()),
	}
}

// Backend implements storage.Backend on top of an ObjectStore.
type Backend struct {
	cfg      Config
	store    ObjectStore
	lay      common.Layout
	counters *Counters

	mu        sync.Mutex
	drafts    map[string]*draftState
	slugLocks map[string]*sync.Mutex

	meta      *cache.LRU[string, metaEntry]
	versions  *cache.LRU[string, *common.VersionsIndex]
	manifests *cache.LRU[string, *common.Manifest]
	files     *cache.LRU[string, []byte]
	siteList  *cache.LRU[string, []site.Summary]
}

type metaEntry struct {
	m       *site.Meta
	missing bool
}

type draftState struct {
	common.Draft
	mu    sync.Mutex
	files map[string]int64
}

// New connects to the bucket described by cfg.
func New(ctx context.Context, cfg Config) (*Backend, error) {
	if cfg.Client.Bucket == "" {
		return nil, errors.New("s3: bucket is required (OPENNOW_S3_BUCKET)")
	}
	if cfg.Client.Region == "" {
		cfg.Client.Region = "us-east-1"
	}
	store, client, err := NewMinioStore(cfg.Client)
	if err != nil {
		return nil, err
	}
	if cfg.CreateBucket {
		if err := EnsureBucket(ctx, client, cfg.Client.Bucket, cfg.Client.Region); err != nil {
			return nil, fmt.Errorf("s3: ensure bucket: %w", err)
		}
	}
	return NewWithStore(cfg, store), nil
}

// NewWithStore builds a backend around any ObjectStore (tests use MemStore).
func NewWithStore(cfg Config, store ObjectStore) *Backend {
	if cfg.Name == "" {
		cfg.Name = "s3"
	}
	if cfg.Cache == (CacheConfig{}) {
		cfg.Cache = DefaultCache()
	}
	metaTTL := cfg.Cache.MetaTTL
	if metaTTL <= 0 {
		metaTTL = time.Nanosecond // 0 disables metadata caching
	}
	counters := &Counters{}
	fileBudget := cfg.Cache.FileBytes
	if fileBudget <= 0 {
		fileBudget = 1
	}
	return &Backend{
		cfg:       cfg,
		store:     countingStore{ObjectStore: store, c: counters},
		lay:       common.NewLayout(cfg.Prefix),
		counters:  counters,
		drafts:    map[string]*draftState{},
		slugLocks: map[string]*sync.Mutex{},
		meta:      cache.New[string, metaEntry](10000, metaTTL, nil),
		versions:  cache.New[string, *common.VersionsIndex](10000, metaTTL, nil),
		manifests: cache.New[string, *common.Manifest](4096, 0, nil),
		files:     cache.New[string, []byte](fileBudget, cfg.Cache.FileTTL, func(b []byte) int64 { return int64(len(b)) }),
		siteList:  cache.New[string, []site.Summary](1, metaTTL, nil),
	}
}

func (b *Backend) Name() string { return b.cfg.Name }

// Counters exposes request counters (tests, overview).
func (b *Backend) Counters() *Counters { return b.counters }

func (b *Backend) lockSlug(slug string) func() {
	b.mu.Lock()
	l, ok := b.slugLocks[slug]
	if !ok {
		l = &sync.Mutex{}
		b.slugLocks[slug] = l
	}
	b.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// ---------------------------------------------------------------------------
// JSON helpers

func (b *Backend) getJSON(ctx context.Context, key string, v any) error {
	rc, _, err := b.store.Get(ctx, key)
	if err != nil {
		return err
	}
	defer rc.Close()
	return json.NewDecoder(rc).Decode(v)
}

func (b *Backend) putJSON(ctx context.Context, key string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return b.store.Put(ctx, key, bytes.NewReader(data), int64(len(data)), "application/json")
}

func copyMeta(m *site.Meta) *site.Meta {
	c := *m
	if m.ExpiresAt != nil {
		t := *m.ExpiresAt
		c.ExpiresAt = &t
	}
	return &c
}

// ---------------------------------------------------------------------------
// Publish flow

func (b *Backend) CreateRevision(ctx context.Context, slug string) (*site.DraftRevision, error) {
	slug, err := common.CheckSlug(slug)
	if err != nil {
		return nil, err
	}
	d := &draftState{
		Draft: common.Draft{ID: common.NewID(), Slug: slug, VersionID: common.NewID(), CreatedAt: time.Now().UTC()},
		files: map[string]int64{},
	}
	// Persist the draft so another instance (or a restart) can finish it.
	if err := b.putJSON(ctx, b.lay.Draft(d.ID), d.Draft); err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.drafts[d.ID] = d
	b.mu.Unlock()
	return &site.DraftRevision{ID: d.ID, Slug: slug, CreatedAt: d.CreatedAt}, nil
}

func (b *Backend) draft(ctx context.Context, rev string) (*draftState, error) {
	if !common.ValidID(rev) {
		return nil, storeerr.ErrDraftNotFound
	}
	b.mu.Lock()
	d, ok := b.drafts[rev]
	b.mu.Unlock()
	if ok {
		return d, nil
	}
	var dr common.Draft
	if err := b.getJSON(ctx, b.lay.Draft(rev), &dr); err != nil {
		if errors.Is(err, storeerr.ErrNotFound) {
			return nil, storeerr.ErrDraftNotFound
		}
		return nil, err
	}
	// files == nil marks "uploaded elsewhere": Finalize falls back to LIST.
	d = &draftState{Draft: dr}
	b.mu.Lock()
	b.drafts[rev] = d
	b.mu.Unlock()
	return d, nil
}

func (b *Backend) Upload(ctx context.Context, revisionID, relPath string, r io.Reader, size int64) error {
	rel, err := common.CleanRelPath(relPath)
	if err != nil {
		return err
	}
	d, err := b.draft(ctx, revisionID)
	if err != nil {
		return err
	}
	cr := &common.CountingReader{R: r}
	if err := b.store.Put(ctx, b.lay.File(d.Slug, d.VersionID, rel), cr, size, mimetype.ForName(rel)); err != nil {
		return err
	}
	d.mu.Lock()
	if d.files != nil {
		d.files[rel] = cr.N
	}
	d.mu.Unlock()
	return nil
}

func (b *Backend) Finalize(ctx context.Context, revisionID string) (*site.Version, string, error) {
	d, err := b.draft(ctx, revisionID)
	if err != nil {
		return nil, "", err
	}
	if err := b.ensureSoleHTMLIndex(ctx, d); err != nil {
		return nil, "", err
	}
	man := &common.Manifest{VersionID: d.VersionID, Slug: d.Slug, CreatedAt: time.Now().UTC()}
	d.mu.Lock()
	tracked := d.files
	d.mu.Unlock()
	if tracked != nil {
		for p, n := range tracked {
			man.Files = append(man.Files, common.ManifestFile{Path: p, Size: n, ContentType: mimetype.ForName(p)})
		}
	} else {
		root := b.lay.FilesRoot(d.Slug, d.VersionID)
		objs, _, err := b.store.List(ctx, root, false)
		if err != nil {
			return nil, "", err
		}
		for _, o := range objs {
			p := strings.TrimPrefix(o.Key, root)
			man.Files = append(man.Files, common.ManifestFile{Path: p, Size: o.Size, ContentType: mimetype.ForName(p)})
		}
	}
	man.Sort()

	unlock := b.lockSlug(d.Slug)
	defer unlock()
	if err := b.putJSON(ctx, b.lay.Manifest(d.Slug, d.VersionID), man); err != nil {
		return nil, "", err
	}
	b.manifests.Set(d.Slug+"/"+d.VersionID, man)

	idx, err := b.versionsIndex(ctx, d.Slug, true)
	if err != nil {
		return nil, "", err
	}
	idx.Versions = append(idx.Versions, common.VersionEntry{
		ID: d.VersionID, CreatedAt: man.CreatedAt, FileCount: len(man.Files), TotalBytes: man.TotalBytes(),
	})
	if err := b.putJSON(ctx, b.lay.VersionsIndex(d.Slug), idx); err != nil {
		return nil, "", err
	}
	b.versions.Set(d.Slug, idx)

	meta, err := b.readMeta(ctx, d.Slug, true)
	if err != nil && !errors.Is(err, storeerr.ErrNotFound) {
		return nil, "", err
	}
	if meta == nil {
		meta = &site.Meta{Slug: d.Slug, CreatedAt: man.CreatedAt}
	}
	meta.CurrentVersion = d.VersionID
	meta.UpdatedAt = man.CreatedAt
	// meta.json is the commit point: the new version goes live here.
	if err := b.writeMeta(ctx, meta); err != nil {
		return nil, "", err
	}
	_ = b.store.DeleteMany(ctx, []string{b.lay.Draft(d.ID)})
	b.mu.Lock()
	delete(b.drafts, d.ID)
	b.mu.Unlock()
	return &site.Version{ID: d.VersionID, CreatedAt: man.CreatedAt}, d.Slug, nil
}

// ---------------------------------------------------------------------------
// Meta & versions

func (b *Backend) readMeta(ctx context.Context, slug string, fresh bool) (*site.Meta, error) {
	if !common.ValidID(slug) {
		return nil, storeerr.ErrNotFound
	}
	if !fresh {
		if e, ok := b.meta.Get(slug); ok {
			if e.missing {
				return nil, storeerr.ErrNotFound
			}
			return copyMeta(e.m), nil
		}
	}
	var m site.Meta
	if err := b.getJSON(ctx, b.lay.Meta(slug), &m); err != nil {
		if errors.Is(err, storeerr.ErrNotFound) {
			b.meta.Set(slug, metaEntry{missing: true})
		}
		return nil, err
	}
	b.meta.Set(slug, metaEntry{m: copyMeta(&m)})
	return &m, nil
}

func (b *Backend) writeMeta(ctx context.Context, m *site.Meta) error {
	if err := b.putJSON(ctx, b.lay.Meta(m.Slug), m); err != nil {
		return err
	}
	b.meta.Set(m.Slug, metaEntry{m: copyMeta(m)})
	b.siteList.Purge()
	return nil
}

func (b *Backend) versionsIndex(ctx context.Context, slug string, fresh bool) (*common.VersionsIndex, error) {
	if !fresh {
		if v, ok := b.versions.Get(slug); ok {
			c := *v
			c.Versions = append([]common.VersionEntry(nil), v.Versions...)
			return &c, nil
		}
	}
	var idx common.VersionsIndex
	if err := b.getJSON(ctx, b.lay.VersionsIndex(slug), &idx); err != nil {
		if !errors.Is(err, storeerr.ErrNotFound) {
			return nil, err
		}
	}
	c := idx
	c.Versions = append([]common.VersionEntry(nil), idx.Versions...)
	b.versions.Set(slug, &c)
	return &idx, nil
}

func (b *Backend) manifest(ctx context.Context, slug, vid string) (*common.Manifest, error) {
	if !common.ValidID(slug) || !common.ValidID(vid) {
		return nil, storeerr.ErrNotFound
	}
	key := slug + "/" + vid
	if m, ok := b.manifests.Get(key); ok {
		return m, nil
	}
	var m common.Manifest
	if err := b.getJSON(ctx, b.lay.Manifest(slug, vid), &m); err != nil {
		return nil, err
	}
	m.Sort()
	b.manifests.Set(key, &m)
	return &m, nil
}

func (b *Backend) GetMeta(ctx context.Context, slug string) (*site.Meta, error) {
	return b.readMeta(ctx, slug, false)
}

func (b *Backend) PutMeta(ctx context.Context, meta *site.Meta) error {
	if !common.ValidID(meta.Slug) {
		return fmt.Errorf("invalid slug")
	}
	unlock := b.lockSlug(meta.Slug)
	defer unlock()
	meta.UpdatedAt = time.Now().UTC()
	return b.writeMeta(ctx, meta)
}

func (b *Backend) ListSites(ctx context.Context) ([]site.Summary, error) {
	if s, ok := b.siteList.Get(""); ok {
		return append([]site.Summary(nil), s...), nil
	}
	_, prefixes, err := b.store.List(ctx, b.lay.SitesRoot(), true)
	if err != nil {
		return nil, err
	}
	out := []site.Summary{}
	for _, p := range prefixes {
		slug := strings.TrimSuffix(strings.TrimPrefix(p, b.lay.SitesRoot()), "/")
		m, err := b.readMeta(ctx, slug, false)
		if err != nil {
			continue
		}
		var man *common.Manifest
		if m.CurrentVersion != "" {
			man, _ = b.manifest(ctx, slug, m.CurrentVersion)
		}
		out = append(out, common.Summarize(m, man))
	}
	b.siteList.Set("", out)
	return append([]site.Summary(nil), out...), nil
}

func (b *Backend) DeleteSite(ctx context.Context, slug string) error {
	if !common.ValidID(slug) {
		return storeerr.ErrNotFound
	}
	unlock := b.lockSlug(slug)
	defer unlock()
	objs, _, err := b.store.List(ctx, b.lay.SiteRoot(slug), false)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.Key)
	}
	// Delete meta.json first so the site disappears even if a later batch fails.
	if err := b.store.DeleteMany(ctx, []string{b.lay.Meta(slug)}); err != nil {
		return err
	}
	b.meta.Set(slug, metaEntry{missing: true})
	b.siteList.Purge()
	for i := 0; i < len(keys); i += 1000 {
		j := min(i+1000, len(keys))
		if err := b.store.DeleteMany(ctx, keys[i:j]); err != nil {
			return err
		}
	}
	b.versions.Delete(slug)
	pfx := slug + "/"
	b.manifests.DeleteFunc(func(k string) bool { return strings.HasPrefix(k, pfx) })
	filePfx := b.lay.SiteRoot(slug)
	b.files.DeleteFunc(func(k string) bool { return strings.HasPrefix(k, filePfx) })
	return nil
}

func (b *Backend) ListVersions(ctx context.Context, slug string) ([]site.Version, error) {
	if !common.ValidID(slug) {
		return nil, storeerr.ErrNotFound
	}
	idx, err := b.versionsIndex(ctx, slug, false)
	if err != nil {
		return nil, err
	}
	return idx.SiteVersions(), nil
}

func (b *Backend) Rollback(ctx context.Context, slug, versionID string) (*site.Version, error) {
	if !common.ValidID(slug) || !common.ValidID(versionID) {
		return nil, storeerr.ErrNotFound
	}
	unlock := b.lockSlug(slug)
	defer unlock()
	idx, err := b.versionsIndex(ctx, slug, true)
	if err != nil {
		return nil, err
	}
	if !idx.Has(versionID) {
		return nil, storeerr.ErrNotFound
	}
	meta, err := b.readMeta(ctx, slug, true)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	meta.CurrentVersion = versionID
	meta.UpdatedAt = now
	if err := b.writeMeta(ctx, meta); err != nil {
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
	man, err := b.manifest(ctx, slug, versionID)
	if err != nil {
		return nil, nil, err
	}
	f, ok := man.Lookup(rel)
	if !ok {
		// Answered from the cached manifest: no request for missing files.
		return nil, nil, storeerr.ErrNotFound
	}
	info := &site.FileInfo{Path: rel, Size: f.Size, ContentType: f.ContentType}
	key := b.lay.File(slug, versionID, rel)
	if data, ok := b.files.Get(key); ok {
		return io.NopCloser(bytes.NewReader(data)), info, nil
	}
	rc, oi, err := b.store.Get(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	if oi.ContentType != "" && info.ContentType == "" {
		info.ContentType = oi.ContentType
	}
	max := b.cfg.Cache.FileMaxObject
	if f.Size <= max && b.cfg.Cache.FileBytes > 0 {
		data, err := io.ReadAll(io.LimitReader(rc, max+1))
		rc.Close()
		if err != nil {
			return nil, nil, err
		}
		if int64(len(data)) <= max {
			b.files.Set(key, data)
		}
		return io.NopCloser(bytes.NewReader(data)), info, nil
	}
	return rc, info, nil // stream large files straight from the bucket
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
	man, err := b.manifest(ctx, slug, versionID)
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
	cc := b.cfg.Client
	loc := cc.Bucket + "/" + b.lay.Prefix
	ep := cc.Endpoint
	if ep == "" {
		ep = "s3.amazonaws.com"
	}
	ps := cc.PathStyle
	if ps == "" {
		ps = "auto"
	}
	fs := b.files.Stats()
	ms := b.meta.Stats()
	c := b.counters
	details := []site.Detail{
		{Label: "Endpoint", Value: ep},
		{Label: "Region", Value: cc.Region},
		{Label: "Bucket", Value: cc.Bucket},
		{Label: "前缀", Value: orDash(b.lay.Prefix)},
		{Label: "Path-style", Value: ps},
		{Label: "元数据缓存 TTL", Value: b.cfg.Cache.MetaTTL.String()},
		{Label: "元数据缓存命中", Value: fmt.Sprintf("%d / %d", ms.Hits, ms.Hits+ms.Misses)},
		{Label: "文件缓存", Value: fmt.Sprintf("%d 个，%s / %s（命中 %d / %d）", fs.Entries, common.HumanBytes(fs.Cost), common.HumanBytes(fs.MaxCost), fs.Hits, fs.Hits+fs.Misses)},
		{Label: "请求计数（本进程）", Value: fmt.Sprintf("PUT %d · GET %d · LIST %d · DELETE %d", c.Put.Load(), c.Get.Load(), c.List.Load(), c.Delete.Load())},
	}
	return &site.StorageOverview{
		Backend:    b.cfg.Name,
		SiteCount:  len(sites),
		TotalBytes: total,
		Location:   loc,
		Details:    details,
		Notes: []string{
			fmt.Sprintf("多实例共享同一 bucket 时，元数据最多滞后 %s（缓存 TTL）。", b.cfg.Cache.MetaTTL),
		},
	}, nil
}

func (b *Backend) ensureSoleHTMLIndex(ctx context.Context, d *draftState) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.files == nil {
		root := b.lay.FilesRoot(d.Slug, d.VersionID)
		objs, _, err := b.store.List(ctx, root, false)
		if err != nil {
			return err
		}
		d.files = make(map[string]int64, len(objs))
		for _, o := range objs {
			rel := strings.TrimPrefix(o.Key, root)
			d.files[rel] = o.Size
		}
	}
	src := common.SoleRootHTMLFromMap(d.files)
	if src == "" {
		return nil
	}
	rc, info, err := b.store.Get(ctx, b.lay.File(d.Slug, d.VersionID, src))
	if err != nil {
		return err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	ct := info.ContentType
	if ct == "" {
		ct = mimetype.ForName("index.html")
	}
	if err := b.store.Put(ctx, b.lay.File(d.Slug, d.VersionID, "index.html"), bytes.NewReader(data), int64(len(data)), ct); err != nil {
		return err
	}
	d.files["index.html"] = int64(len(data))
	return nil
}

func (b *Backend) RenameSite(ctx context.Context, oldSlug, newSlug string) (*site.Meta, error) {
	if !common.ValidID(oldSlug) || !common.ValidID(newSlug) {
		return nil, storeerr.ErrInvalidPath
	}
	if oldSlug == newSlug {
		return b.readMeta(ctx, oldSlug, true)
	}
	// Lock in sorted order to avoid deadlock.
	first, second := oldSlug, newSlug
	if first > second {
		first, second = second, first
	}
	u1 := b.lockSlug(first)
	defer u1()
	u2 := b.lockSlug(second)
	defer u2()

	meta, err := b.readMeta(ctx, oldSlug, true)
	if err != nil {
		return nil, err
	}
	if _, err := b.readMeta(ctx, newSlug, true); err == nil {
		return nil, storeerr.ErrConflict
	} else if !errors.Is(err, storeerr.ErrNotFound) {
		return nil, err
	}

	oldRoot := b.lay.SiteRoot(oldSlug)
	newRoot := b.lay.SiteRoot(newSlug)
	objs, _, err := b.store.List(ctx, oldRoot, false)
	if err != nil {
		return nil, err
	}
	oldKeys := make([]string, 0, len(objs))
	for _, o := range objs {
		oldKeys = append(oldKeys, o.Key)
		rel := strings.TrimPrefix(o.Key, oldRoot)
		newKey := newRoot + rel
		rc, info, err := b.store.Get(ctx, o.Key)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, err
		}
		ct := info.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		if err := b.store.Put(ctx, newKey, bytes.NewReader(data), int64(len(data)), ct); err != nil {
			return nil, err
		}
	}

	meta.Slug = newSlug
	meta.UpdatedAt = time.Now().UTC()
	if err := b.writeMeta(ctx, meta); err != nil {
		return nil, err
	}

	// Remove old objects (meta first so old slug disappears immediately).
	_ = b.store.DeleteMany(ctx, []string{b.lay.Meta(oldSlug)})
	b.meta.Set(oldSlug, metaEntry{missing: true})
	for i := 0; i < len(oldKeys); i += 1000 {
		j := min(i+1000, len(oldKeys))
		_ = b.store.DeleteMany(ctx, oldKeys[i:j])
	}
	b.versions.Delete(oldSlug)
	oldPfx := oldSlug + "/"
	b.manifests.DeleteFunc(func(k string) bool { return strings.HasPrefix(k, oldPfx) })
	b.files.DeleteFunc(func(k string) bool { return strings.HasPrefix(k, oldRoot) })
	b.siteList.Purge()
	return meta, nil
}


func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

