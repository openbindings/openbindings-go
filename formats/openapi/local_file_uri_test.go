package openapi

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalFileURIRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space %2F#é.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	encoded, err := absolutizeArtifactLocation(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded, "%252F%23") || strings.Contains(encoded, " ") || strings.Contains(encoded, "\\") {
		t.Fatalf("not encoded as URI path data: %s", encoded)
	}
	u, err := url.Parse(encoded)
	if err != nil || u.Host != "" || u.Scheme != "file" {
		t.Fatalf("file URI: %s %v", encoded, err)
	}
	decoded, err := localArtifactPath(u)
	if err != nil || decoded != path {
		t.Fatalf("round trip %q => %q, %v", path, decoded, err)
	}
	data, err := os.ReadFile(decoded)
	if err != nil || string(data) != "{}" {
		t.Fatalf("read: %s %v", data, err)
	}
	if runtime.GOOS == "windows" && !strings.HasPrefix(u.Path, "/"+filepath.VolumeName(path)+"/") {
		t.Fatalf("drive missing: %s", encoded)
	}
}

func TestLocalFileURIUnsupportedPathsAndURIPreservation(t *testing.T) {
	for _, raw := range []string{`C:relative.json`, `\\server\share\a.json`, `\\?\C:\a.json`} {
		if _, err := absolutizeArtifactLocation(raw); err == nil {
			t.Fatalf("unsupported path accepted: %s", raw)
		}
	}
	for _, raw := range []string{"file:///C:/space%20name.json", "file:/absolute/path.json", "https://example.test/a%23b.json"} {
		got, err := absolutizeArtifactLocation(raw)
		if err != nil || got != raw {
			t.Fatalf("URI changed: %q => %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{"file://untrusted.example/share/a.json", "file:relative.json"} {
		u, _ := url.Parse(raw)
		if _, err := localArtifactPath(u); err == nil {
			t.Fatalf("unsupported URI accepted: %s", raw)
		}
	}
}
