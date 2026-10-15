package site

import (
	"time"
)

// Meta holds site-level settings and pointer to live version.
type Meta struct {
	Slug           string     `json:"slug"`
	CurrentVersion string     `json:"current_version"`
	PasswordHash   string     `json:"password_hash,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Version is an immutable published revision.
type Version struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

// Summary for list views.
type Summary struct {
	Slug           string     `json:"slug"`
	CurrentVersion string     `json:"current_version"`
	HasPassword    bool       `json:"has_password"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
	FileCount      int        `json:"file_count,omitempty"`
	TotalBytes     int64      `json:"total_bytes,omitempty"`
}

// DraftRevision is an in-progress publish (create → upload → finalize).
type DraftRevision struct {
	ID        string    `json:"id"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
}

// FileInfo describes a stored object.
type FileInfo struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type,omitempty"`
}

// StorageOverview aggregates backend stats for admin.
type StorageOverview struct {
	Backend    string `json:"backend"`
	SiteCount  int    `json:"site_count"`
	TotalBytes int64  `json:"total_bytes"`
	DataPath   string `json:"data_path,omitempty"`
	// Location is a short human-readable description of where data lives
	// (bucket/prefix, owner/repo@branch, directory).
	Location string `json:"location,omitempty"`
	// Details lists backend-specific configuration and runtime stats.
	Details []Detail `json:"details,omitempty"`
	// Notes lists backend limitations worth surfacing in the admin UI.
	Notes []string `json:"notes,omitempty"`
}

// Detail is one label/value row in the storage overview.
type Detail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}
