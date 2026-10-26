package site

import "testing"

func TestSoleRootHTML(t *testing.T) {
	cases := []struct {
		paths []string
		want  string
	}{
		{[]string{"sre-ops-prototype.html"}, "sre-ops-prototype.html"},
		{[]string{"Page.HTM"}, "Page.HTM"},
		{[]string{"index.html", "other.html"}, ""},
		{[]string{"a.html", "b.html"}, ""},
		{[]string{"dir/a.html"}, ""},
		{[]string{"a.html", "assets/x.js"}, "a.html"},
		{[]string{"INDEX.HTML"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := SoleRootHTML(c.paths); got != c.want {
			t.Fatalf("SoleRootHTML(%v)=%q want %q", c.paths, got, c.want)
		}
	}
}
