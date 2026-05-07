package openbindings

import "testing"

func TestCanonicalizeLocation(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase scheme", "HTTPS://example.com/foo", "https://example.com/foo"},
		{"lowercase host", "https://Example.COM/Foo", "https://example.com/Foo"},
		{"path case preserved", "https://example.com/Foo/Bar", "https://example.com/Foo/Bar"},
		{"query case preserved", "https://example.com/x?Bar=Baz", "https://example.com/x?Bar=Baz"},
		{"empty path with authority", "https://example.com", "https://example.com/"},
		{"https default port stripped", "https://example.com:443/foo", "https://example.com/foo"},
		{"http default port stripped", "http://example.com:80/foo", "http://example.com/foo"},
		{"non-default port preserved", "https://example.com:8443/foo", "https://example.com:8443/foo"},
		{"fragment stripped", "https://example.com/foo#bar", "https://example.com/foo"},
		{"dot segments removed", "https://example.com/a/./b/../c", "https://example.com/a/c"},
		{"leading dot segments removed", "https://example.com/./a/b", "https://example.com/a/b"},
		{"unreserved percent decoded", "https://example.com/foo%2Dbar", "https://example.com/foo-bar"},
		{"reserved percent uppercased", "https://example.com/foo%2fbar", "https://example.com/foo%2Fbar"},
		{"space stays encoded", "https://example.com/foo%20bar", "https://example.com/foo%20bar"},
		{"absolute filesystem path lifted", "/etc/passwd", "file:///etc/passwd"},
		{"trailing slash significant", "https://example.com/x/", "https://example.com/x/"},
		{"http and https distinct", "http://example.com/x", "http://example.com/x"},
		{"IDN punycoded", "https://bücher.example/x", "https://xn--bcher-kva.example/x"},
		{"IDN already punycoded", "https://xn--bcher-kva.example/x", "https://xn--bcher-kva.example/x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalizeLocation(tt.in)
			if err != nil {
				t.Fatalf("CanonicalizeLocation(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("CanonicalizeLocation(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCanonicalizeLocation_Errors(t *testing.T) {
	cases := []string{
		"",
		"no-scheme-no-slash",
		"::not a url",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			if _, err := CanonicalizeLocation(c); err == nil {
				t.Errorf("CanonicalizeLocation(%q) expected error, got nil", c)
			}
		})
	}
}

func TestCanonicalizeLocation_EqualityPairs(t *testing.T) {
	// Pairs that should canonicalize to the same value.
	equal := [][2]string{
		{"https://Example.com/foo", "https://example.com/foo"},
		{"https://example.com:443/foo", "https://example.com/foo"},
		{"https://example.com/a/./b/../c", "https://example.com/a/c"},
		{"https://example.com/foo#anchor", "https://example.com/foo"},
		{"https://example.com/foo%2Dbar", "https://example.com/foo-bar"},
	}
	for _, p := range equal {
		a, err := CanonicalizeLocation(p[0])
		if err != nil {
			t.Fatalf("CanonicalizeLocation(%q): %v", p[0], err)
		}
		b, err := CanonicalizeLocation(p[1])
		if err != nil {
			t.Fatalf("CanonicalizeLocation(%q): %v", p[1], err)
		}
		if a != b {
			t.Errorf("expected canonical equality: %q -> %q vs %q -> %q", p[0], a, p[1], b)
		}
	}

	// Pairs that should NOT canonicalize to the same value.
	distinct := [][2]string{
		{"https://example.com/foo", "http://example.com/foo"},     // scheme
		{"https://example.com/x", "https://example.com/x/"},       // trailing slash
		{"https://example.com/Foo", "https://example.com/foo"},    // path case
		{"https://example.com/?a=1", "https://example.com/?a=2"},  // query
		{"https://example.com/", "https://example.com:8443/"},     // non-default port
	}
	for _, p := range distinct {
		a, err := CanonicalizeLocation(p[0])
		if err != nil {
			t.Fatalf("CanonicalizeLocation(%q): %v", p[0], err)
		}
		b, err := CanonicalizeLocation(p[1])
		if err != nil {
			t.Fatalf("CanonicalizeLocation(%q): %v", p[1], err)
		}
		if a == b {
			t.Errorf("expected canonical distinctness: %q and %q both -> %q", p[0], p[1], a)
		}
	}
}

func TestResolveRef(t *testing.T) {
	tests := []struct {
		name string
		base string
		ref  string
		want string
	}{
		{
			"directory-relative same dir",
			"https://example.com/interfaces/host.json",
			"./foo.json",
			"https://example.com/interfaces/foo.json",
		},
		{
			"parent dir reference",
			"https://example.com/interfaces/host.json",
			"../other/foo.json",
			"https://example.com/other/foo.json",
		},
		{
			"absolute path reference",
			"https://example.com/interfaces/host.json",
			"/foo.json",
			"https://example.com/foo.json",
		},
		{
			"absolute reference passes through",
			"https://example.com/interfaces/host.json",
			"https://other.example.com/foo.json",
			"https://other.example.com/foo.json",
		},
		{
			"fragment retained on relative",
			"https://example.com/interfaces/host.json",
			"./foo.json#/components/schemas/Task",
			"https://example.com/interfaces/foo.json#/components/schemas/Task",
		},
		{
			"plain relative with no leading ./",
			"https://example.com/a/b.json",
			"c.json",
			"https://example.com/a/c.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveRef(tt.base, tt.ref)
			if err != nil {
				t.Fatalf("ResolveRef(%q, %q) error: %v", tt.base, tt.ref, err)
			}
			if got != tt.want {
				t.Errorf("ResolveRef(%q, %q) = %q, want %q", tt.base, tt.ref, got, tt.want)
			}
		})
	}
}

func TestResolveRef_Errors(t *testing.T) {
	t.Run("empty ref", func(t *testing.T) {
		if _, err := ResolveRef("https://example.com/", ""); err == nil {
			t.Error("expected error for empty ref")
		}
	})
	t.Run("relative ref with empty base", func(t *testing.T) {
		if _, err := ResolveRef("", "./foo.json"); err == nil {
			t.Error("expected error for relative ref without base")
		}
	})
	t.Run("non-absolute base", func(t *testing.T) {
		if _, err := ResolveRef("not/an/absolute/uri", "./foo.json"); err == nil {
			t.Error("expected error for non-absolute base")
		}
	})
}

func TestRemoveDotSegments(t *testing.T) {
	// RFC 3986 §5.2.4 examples.
	tests := []struct {
		in   string
		want string
	}{
		{"/a/b/c/./../../g", "/a/g"},
		{"mid/content=5/../6", "mid/6"},
		{"/a/b/../c", "/a/c"},
		{"/./a", "/a"},
		{"/a/.", "/a/"},
		{"/a/..", "/"},
		{"", ""},
		{"/", "/"},
		{"/a/b/c", "/a/b/c"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := removeDotSegments(tt.in); got != tt.want {
				t.Errorf("removeDotSegments(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
