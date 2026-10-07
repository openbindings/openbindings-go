package openbindings

import "testing"

// TestResolveURIReference checks the strict RFC 3986 §5.2 resolution OBI-13
// compares identifiers by, against the normal and abnormal examples of RFC
// 3986 §5.4.
func TestResolveURIReference(t *testing.T) {
	base := splitURI("http://a/b/c/d;p?q")
	cases := map[string]string{
		"g:h": "g:h", "g": "http://a/b/c/g", "./g": "http://a/b/c/g", "g/": "http://a/b/c/g/",
		"/g": "http://a/g", "//g": "http://g", "?y": "http://a/b/c/d;p?y", "g?y": "http://a/b/c/g?y",
		"#s": "http://a/b/c/d;p?q#s", "g#s": "http://a/b/c/g#s", "g?y#s": "http://a/b/c/g?y#s",
		";x": "http://a/b/c/;x", "g;x": "http://a/b/c/g;x", "g;x?y#s": "http://a/b/c/g;x?y#s",
		"": "http://a/b/c/d;p?q", ".": "http://a/b/c/", "./": "http://a/b/c/", "..": "http://a/b/",
		"../": "http://a/b/", "../g": "http://a/b/g", "../..": "http://a/", "../../": "http://a/",
		"../../g": "http://a/g", "../../../g": "http://a/g", "../../../../g": "http://a/g",
		"/./g": "http://a/g", "/../g": "http://a/g", "g.": "http://a/b/c/g.", ".g": "http://a/b/c/.g",
		"g..": "http://a/b/c/g..", "..g": "http://a/b/c/..g", "./../g": "http://a/b/g",
		"./g/.": "http://a/b/c/g/", "g/./h": "http://a/b/c/g/h", "g/../h": "http://a/b/c/h",
		"g;x=1/./y": "http://a/b/c/g;x=1/y", "g;x=1/../y": "http://a/b/c/y",
		"g?y/./x": "http://a/b/c/g?y/./x", "g?y/../x": "http://a/b/c/g?y/../x",
		"g#s/./x": "http://a/b/c/g#s/./x", "g#s/../x": "http://a/b/c/g#s/../x", "http:g": "http:g",
	}
	for ref, want := range cases {
		if got := resolveURIReference(base, splitURI(ref)).String(); got != want {
			t.Errorf("%q: got %q, want %q", ref, got, want)
		}
	}
}

// TestComparableID checks the identifiers OBI-13 compares: resolution
// removes dot segments and an empty fragment, and nothing else is normalized.
func TestComparableID(t *testing.T) {
	cases := []struct{ raw, enclosing, want string }{
		{"https://example.com/a/../b", "", "https://example.com/b"},
		{"https://example.com/b#", "", "https://example.com/b"},
		{"HTTPS://Example.com/b", "", "HTTPS://Example.com/b"},
		{"https://example.com/%7eb", "", "https://example.com/%7eb"},
		{"c.json", "https://example.com/a/b.json", "https://example.com/a/c.json"},
		{"c.json", "", ""},
		{"not a uri", "https://example.com/", ""},
		{"#", "https://example.com/a", "https://example.com/a"},
	}
	for _, c := range cases {
		if got := comparableID(c.raw, c.enclosing); got != c.want {
			t.Errorf("comparableID(%q, %q) = %q, want %q", c.raw, c.enclosing, got, c.want)
		}
	}
}
