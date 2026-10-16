package common

import "testing"

func TestCleanRelPath(t *testing.T) {
	ok := map[string]string{"index.html": "index.html", "/a/b.css": "a/b.css", "a/./b": "a/b", "a\\b": "a/b"}
	for in, want := range ok {
		got, err := CleanRelPath(in)
		if err != nil || got != want {
			t.Errorf("%q → %q, %v (want %q)", in, got, err, want)
		}
	}
	// "../x" is cleaned to "x" because it is rooted first; it cannot escape.
	if got, err := CleanRelPath("../../x"); err != nil || got != "x" {
		t.Errorf("traversal: %q %v", got, err)
	}
	for _, bad := range []string{"", ".", "/"} {
		if _, err := CleanRelPath(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestLayoutAndManifest(t *testing.T) {
	l := NewLayout("/p/q/")
	if got := l.File("s", "v", "a/b.js"); got != "p/q/sites/s/versions/v/files/a/b.js" {
		t.Fatal(got)
	}
	if NewLayout("").Meta("s") != "sites/s/meta.json" {
		t.Fatal("empty prefix")
	}
	m := Manifest{Files: []ManifestFile{{Path: "b", Size: 2}, {Path: "a", Size: 1}}}
	m.Sort()
	if f, ok := m.Lookup("b"); !ok || f.Size != 2 || m.TotalBytes() != 3 {
		t.Fatal("lookup")
	}
	if _, ok := m.Lookup("c"); ok {
		t.Fatal("missing should not be found")
	}
}
