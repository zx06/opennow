// Package markdown renders untrusted Markdown to a safe HTML document.
package markdown

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

var (
	md = goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.Typographer,
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		// No html.WithUnsafe(): raw HTML in Markdown stays escaped.
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
		),
	)
	// UGCPolicy strips scripts, event handlers, javascript: URLs, etc.
	sanitize = bluemonday.UGCPolicy()
	h1RE     = regexp.MustCompile(`(?i)<h1[^>]*>(.*?)</h1>`)
	tagRE    = regexp.MustCompile(`<[^>]+>`)
)

// IsMarkdown reports whether name looks like a Markdown file.
func IsMarkdown(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".markdown"
}

// Render converts Markdown source into a complete, XSS-sanitized HTML page.
// titleFallback is used when the document has no H1 (typically the file name).
func Render(src []byte, titleFallback string) ([]byte, error) {
	var body bytes.Buffer
	if err := md.Convert(src, &body); err != nil {
		return nil, err
	}
	safe := sanitize.SanitizeBytes(body.Bytes())
	title := titleFromHTML(safe)
	if title == "" {
		title = strings.TrimSpace(titleFallback)
	}
	if title == "" {
		title = "Untitled"
	}
	var out bytes.Buffer
	out.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	out.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	out.WriteString("<title>")
	out.WriteString(htmlEscape(title))
	out.WriteString("</title>")
	out.WriteString(pageCSS)
	out.WriteString("</head><body><article class=\"markdown-body\">")
	out.Write(safe)
	out.WriteString("</article></body></html>")
	return out.Bytes(), nil
}

func titleFromHTML(safe []byte) string {
	m := h1RE.FindSubmatch(safe)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(tagRE.ReplaceAllString(string(m[1]), ""))
}

func htmlEscape(s string) string {
	replacer := strings.NewReplacer(
		`&`, "&amp;",
		`<`, "&lt;",
		`>`, "&gt;",
		`"`, "&quot;",
	)
	return replacer.Replace(s)
}

// TitleFromPath returns a readable title from a file path (e.g. "readme" from README.md).
func TitleFromPath(name string) string {
	base := filepath.Base(name)
	ext := filepath.Ext(base)
	base = strings.TrimSuffix(base, ext)
	if base == "" {
		return ""
	}
	return base
}

const pageCSS = `<style>
:root{color-scheme:light dark;--bg:#fff;--fg:#1a1a1a;--muted:#64748b;--line:#e2e8f0;--code-bg:#f1f5f9;--link:#2563eb;--pre-fg:#e2e8f0;--pre-bg:#0f172a}
@media(prefers-color-scheme:dark){:root{--bg:#0b1020;--fg:#e2e8f0;--muted:#94a3b8;--line:#243049;--code-bg:#1e293b;--link:#93c5fd;--pre-fg:#e2e8f0;--pre-bg:#020617}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:16px/1.65 system-ui,-apple-system,"PingFang SC","Microsoft YaHei",sans-serif}
.markdown-body{max-width:48rem;margin:0 auto;padding:2rem 1.25rem 4rem}
.markdown-body h1,.markdown-body h2,.markdown-body h3{line-height:1.25;margin:1.6em 0 .6em;font-weight:700}
.markdown-body h1{font-size:1.75rem;border-bottom:1px solid var(--line);padding-bottom:.35rem}
.markdown-body h2{font-size:1.35rem}.markdown-body h3{font-size:1.15rem}
.markdown-body p,.markdown-body ul,.markdown-body ol,.markdown-body blockquote,.markdown-body pre,.markdown-body table{margin:0 0 1rem}
.markdown-body a{color:var(--link)}
.markdown-body code{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:.9em;background:var(--code-bg);padding:.15em .35em;border-radius:4px}
.markdown-body pre{background:var(--pre-bg);color:var(--pre-fg);padding:1rem 1.1rem;border-radius:10px;overflow:auto}
.markdown-body pre code{background:transparent;padding:0;color:inherit;font-size:.85rem}
.markdown-body blockquote{margin-left:0;padding:.25rem 0 .25rem 1rem;border-left:4px solid var(--line);color:var(--muted)}
.markdown-body img{max-width:100%;height:auto}
.markdown-body table{border-collapse:collapse;width:100%;overflow:auto;display:block}
.markdown-body th,.markdown-body td{border:1px solid var(--line);padding:.4rem .65rem}
.markdown-body hr{border:0;border-top:1px solid var(--line);margin:2rem 0}
</style>`
