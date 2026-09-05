package openapi

import (
	"os"
	"sort"
	"strings"
	"testing"
)

// This inventory is intentionally closed. Adding a file requires an explicit
// review that it translates native provider/client facts rather than restoring
// a second OpenAPI planner or executor in the OpenBindings adapter.
func TestProviderBoundaryContainsOnlyReviewedAdapterFiles(t *testing.T) {
	want := []string{
		"input_routes_v2.go",
		"invoke.go",
		"invoker.go",
		"list_selectors.go",
		"native_adapter.go",
		"provider_projection.go",
		"swagger20_synthesis.go",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		got = append(got, name)
		data, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		source := string(data)
		for _, forbidden := range []string{
			"github.com/openbindings/openapi-client/go/internal/",
			"github.com/getkin/kin-openapi",
		} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s imports private OpenAPI implementation dependency %q", name, forbidden)
			}
		}
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("production adapter files:\n%s\nwant reviewed inventory:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
