package common

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zx06/opennow/internal/site"
)

// EnsureSoleHTMLIndex copies the sole root-level HTML file to index.html
// inside filesRoot when index.html is missing. The original file is kept.
func EnsureSoleHTMLIndex(filesRoot string) error {
	entries, err := os.ReadDir(filesRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		paths = append(paths, e.Name())
	}
	src := site.SoleRootHTML(paths)
	if src == "" {
		return nil
	}
	in, err := os.Open(filepath.Join(filesRoot, src))
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(filesRoot, "index.html"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// SoleRootHTMLFromMap is like site.SoleRootHTML for a files map keyed by path.
func SoleRootHTMLFromMap(files map[string]int64) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, strings.TrimPrefix(p, "/"))
	}
	return site.SoleRootHTML(paths)
}
