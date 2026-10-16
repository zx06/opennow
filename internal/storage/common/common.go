// Package common holds the object layout, manifests and validation helpers
// shared by the remote (S3/R2/GitHub) storage backends.
//
// Layout under an optional prefix:
//
//	sites/<slug>/meta.json                         site.Meta (live pointer)
//	sites/<slug>/versions.json                     []VersionEntry index
//	sites/<slug>/versions/<vid>/manifest.json      Manifest (file list)
//	sites/<slug>/versions/<vid>/files/<path>       file bytes
//	drafts/<revision>.json                         Draft (S3 only)
//
// The manifest and versions index mean that serving a file and listing
// versions never require a LIST call against the remote store.
package common

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zx06/opennow/internal/site"
	"github.com/zx06/opennow/internal/storage/storeerr"
)

// Layout builds object keys under a prefix.
type Layout struct{ Prefix string }

// NewLayout normalises a prefix ("", "a", "/a/b/" → "", "a/", "a/b/").
func NewLayout(prefix string) Layout {
	prefix = strings.Trim(prefix, "/")
	if prefix != "" {
		prefix += "/"
	}
	return Layout{Prefix: prefix}
}

func (l Layout) SitesRoot() string                { return l.Prefix + "sites/" }
func (l Layout) SiteRoot(slug string) string      { return l.Prefix + "sites/" + slug + "/" }
func (l Layout) Meta(slug string) string          { return l.SiteRoot(slug) + "meta.json" }
func (l Layout) VersionsIndex(slug string) string { return l.SiteRoot(slug) + "versions.json" }
func (l Layout) VersionRoot(slug, vid string) string {
	return l.SiteRoot(slug) + "versions/" + vid + "/"
}
func (l Layout) Manifest(slug, vid string) string { return l.VersionRoot(slug, vid) + "manifest.json" }
func (l Layout) FilesRoot(slug, vid string) string {
	return l.VersionRoot(slug, vid) + "files/"
}
func (l Layout) File(slug, vid, rel string) string { return l.FilesRoot(slug, vid) + rel }
func (l Layout) Draft(rev string) string           { return l.Prefix + "drafts/" + rev + ".json" }

// ManifestFile is one entry in a version manifest.
type ManifestFile struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
	// SHA is the git blob SHA (GitHub backend only).
	SHA string `json:"sha,omitempty"`
}

// Manifest lists every file in an immutable version.
type Manifest struct {
	VersionID string         `json:"version_id"`
	Slug      string         `json:"slug"`
	CreatedAt time.Time      `json:"created_at"`
	Files     []ManifestFile `json:"files"`
}

// Sort orders files by path for deterministic output.
func (m *Manifest) Sort() {
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
}

// Lookup returns the entry for rel.
func (m *Manifest) Lookup(rel string) (ManifestFile, bool) {
	i := sort.Search(len(m.Files), func(i int) bool { return m.Files[i].Path >= rel })
	if i < len(m.Files) && m.Files[i].Path == rel {
		return m.Files[i], true
	}
	// Fall back to a linear scan in case the manifest was not sorted.
	for _, f := range m.Files {
		if f.Path == rel {
			return f, true
		}
	}
	return ManifestFile{}, false
}

// TotalBytes sums file sizes.
func (m *Manifest) TotalBytes() int64 {
	var n int64
	for _, f := range m.Files {
		n += f.Size
	}
	return n
}

// FileInfos converts the manifest into API file infos.
func (m *Manifest) FileInfos() []site.FileInfo {
	out := make([]site.FileInfo, 0, len(m.Files))
	for _, f := range m.Files {
		out = append(out, site.FileInfo{Path: f.Path, Size: f.Size, ContentType: f.ContentType})
	}
	return out
}

// VersionEntry is one row of the per-site versions index.
type VersionEntry struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	FileCount  int       `json:"file_count"`
	TotalBytes int64     `json:"total_bytes"`
}

// VersionsIndex is the per-site list of published versions (oldest first).
type VersionsIndex struct {
	Versions []VersionEntry `json:"versions"`
}

// Has reports whether vid is a known version.
func (v *VersionsIndex) Has(vid string) bool {
	for _, e := range v.Versions {
		if e.ID == vid {
			return true
		}
	}
	return false
}

// SiteVersions converts the index to API versions.
func (v *VersionsIndex) SiteVersions() []site.Version {
	out := make([]site.Version, 0, len(v.Versions))
	for _, e := range v.Versions {
		out = append(out, site.Version{ID: e.ID, CreatedAt: e.CreatedAt})
	}
	return out
}

// Draft is an in-progress publish.
type Draft struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	VersionID string    `json:"version_id"`
	CreatedAt time.Time `json:"created_at"`
}

// NewSlug returns a random 12-char slug.
func NewSlug() string { return strings.ReplaceAll(uuid.New().String(), "-", "")[:12] }

// NewID returns a random UUID string.
func NewID() string { return uuid.New().String() }

// CheckSlug validates (or generates) a slug.
func CheckSlug(slug string) (string, error) {
	if slug == "" {
		return NewSlug(), nil
	}
	if !ValidID(slug) {
		return "", fmt.Errorf("invalid slug")
	}
	return slug, nil
}

// ValidID rejects empty values and anything that could escape a key prefix.
func ValidID(s string) bool {
	return s != "" && s != "." && !strings.Contains(s, "..") && !strings.ContainsAny(s, "/\\")
}

// CleanRelPath normalises a file path inside a site and rejects traversal.
func CleanRelPath(p string) (string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return "", storeerr.ErrInvalidPath
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", storeerr.ErrInvalidPath
		}
	}
	return p, nil
}

// Summarize builds a site summary from meta and the live manifest (may be nil).
func Summarize(m *site.Meta, man *Manifest) site.Summary {
	s := site.Summary{
		Slug:           m.Slug,
		CurrentVersion: m.CurrentVersion,
		HasPassword:    m.PasswordHash != "",
		ExpiresAt:      m.ExpiresAt,
		UpdatedAt:      m.UpdatedAt,
	}
	if man != nil {
		s.FileCount = len(man.Files)
		s.TotalBytes = man.TotalBytes()
	}
	return s
}

// CountingReader counts bytes read.
type CountingReader struct {
	R interface{ Read([]byte) (int, error) }
	N int64
}

func (c *CountingReader) Read(p []byte) (int, error) {
	n, err := c.R.Read(p)
	c.N += int64(n)
	return n, err
}

// HumanBytes formats a byte count.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
