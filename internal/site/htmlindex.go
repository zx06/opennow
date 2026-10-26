package site

import "strings"

// SoleRootHTML returns the path of the single root-level HTML file when the
// site root has exactly one *.html/*.htm and no index.html/index.htm.
// paths are slash-separated and relative to the site/version root.
// Returns "" when there is already an index, zero HTML files, or more than one.
func SoleRootHTML(paths []string) string {
	var htmls []string
	for _, p := range paths {
		p = strings.TrimPrefix(p, "/")
		if p == "" || strings.Contains(p, "/") {
			continue // only root-level entries
		}
		lower := strings.ToLower(p)
		if lower == "index.html" || lower == "index.htm" {
			return ""
		}
		if strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm") {
			htmls = append(htmls, p)
		}
	}
	if len(htmls) == 1 {
		return htmls[0]
	}
	return ""
}
