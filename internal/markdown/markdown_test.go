package markdown

import (
	"strings"
	"testing"
)

func TestRenderBasic(t *testing.T) {
	src := []byte("# Hello\n\nThis is **bold** and a [link](https://example.com).\n\n- one\n- two\n\n```go\nfmt.Println(\"hi\")\n```\n\n![alt](https://example.com/a.png)\n")
	out, err := Render(src, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	for _, want := range []string{
		"<!DOCTYPE html>",
		"<title>Hello</title>",
		"<h1",
		"Hello",
		"<strong>bold</strong>",
		`href="https://example.com"`,
		"<li>",
		"<pre>",
		"<code",
		`src="https://example.com/a.png"`,
		"text/html", // shouldn't appear as content-type in body; just ensure page structure
		"markdown-body",
	} {
		if want == "text/html" {
			continue
		}
		if !strings.Contains(html, want) {
			t.Fatalf("missing %q in:\n%s", want, html)
		}
	}
	if !strings.Contains(html, "<html") || !strings.Contains(html, "</body></html>") {
		t.Fatal("not a complete HTML page")
	}
}

func TestRenderTitleFallback(t *testing.T) {
	out, err := Render([]byte("no heading here"), "notes")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<title>notes</title>") {
		t.Fatalf("title fallback missing: %s", out)
	}
}

func TestRenderXSS(t *testing.T) {
	payloads := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`[click](javascript:alert(1))`,
		`<a href="javascript:alert(1)">x</a>`,
		`<iframe src="https://evil.test"></iframe>`,
		`<svg onload=alert(1)>`,
		`[x](data:text/html,<script>alert(1)</script>)`,
	}
	for _, p := range payloads {
		out, err := Render([]byte("# T\n\n"+p+"\n"), "t")
		if err != nil {
			t.Fatal(err)
		}
		html := strings.ToLower(string(out))
		for _, bad := range []string{
			"<script",
			"javascript:",
			"onerror=",
			"onload=",
			"<iframe",
		} {
			if strings.Contains(html, bad) {
				t.Fatalf("payload %q left %q in output:\n%s", p, bad, out)
			}
		}
	}
}

func TestIsMarkdown(t *testing.T) {
	cases := map[string]bool{
		"README.md":       true,
		"docs/a.markdown": true,
		"A.MD":            true,
		"index.html":      false,
		"file.txt":        false,
		"readme":          false,
	}
	for name, want := range cases {
		if got := IsMarkdown(name); got != want {
			t.Fatalf("%s: got %v want %v", name, got, want)
		}
	}
}

func TestTitleFromPath(t *testing.T) {
	if got := TitleFromPath("docs/README.md"); got != "README" {
		t.Fatalf("got %q", got)
	}
}
