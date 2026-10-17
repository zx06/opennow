package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/zx06/opennow/internal/markdown"
	"github.com/zx06/opennow/internal/site"
	"github.com/zx06/opennow/internal/storage/storeerr"
)

// Directory index lookup order after an exact path miss.
// index.html stays first so existing sites are unchanged.
var indexFiles = []string{
	"index.html",
	"index.md",
	"index.markdown",
	"README.md",
	"README.markdown",
}

func isIndexFile(rel string) bool {
	base := path.Base(rel)
	for _, n := range indexFiles {
		if strings.EqualFold(base, n) {
			return true
		}
	}
	return false
}

func indexCandidates(dir string) []string {
	out := make([]string, 0, len(indexFiles))
	for _, n := range indexFiles {
		if dir == "" || dir == "." {
			out = append(out, n)
		} else {
			out = append(out, path.Join(dir, n))
		}
	}
	return out
}

// openVersionFile tries rel, then directory index candidates when appropriate.
func (s *Server) openVersionFile(ctx context.Context, slug, versionID, rel string) (io.ReadCloser, *site.FileInfo, error) {
	rel = strings.TrimPrefix(rel, "/")
	rc, info, err := s.store.OpenVersionFile(ctx, slug, versionID, rel)
	if err == nil {
		return rc, info, nil
	}
	if !errors.Is(err, storeerr.ErrNotFound) {
		return nil, nil, err
	}

	var candidates []string
	if isIndexFile(rel) {
		candidates = indexCandidates(path.Dir(rel))
	} else {
		// Mirror previous behavior: missing path → treat as directory and look for index.
		candidates = indexCandidates(strings.TrimSuffix(rel, "/"))
	}
	for _, c := range candidates {
		if c == rel {
			continue
		}
		rc, info, err = s.store.OpenVersionFile(ctx, slug, versionID, c)
		if err == nil {
			return rc, info, nil
		}
		if !errors.Is(err, storeerr.ErrNotFound) {
			return nil, nil, err
		}
	}
	return nil, nil, storeerr.ErrNotFound
}

func writeMarkdown(w http.ResponseWriter, filePath string, r io.Reader) error {
	src, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	html, err := markdown.Render(src, markdown.TitleFromPath(filePath))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = w.Write(html)
	return err
}
