package usage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func mustParse(t *testing.T, kdl string) *Spec {
	t.Helper()
	spec, err := ParseKDL([]byte(kdl))
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestConvertToInterface_CopiesMetadata(t *testing.T) {
	iface, err := synthesizeFromArtifactText(`
name "mycli"
version "1.2.3"
about "A test CLI"
cmd "greet" help="Say hello"
`)
	if err != nil {
		t.Fatal(err)
	}
	if iface.Name != "mycli" {
		t.Errorf("Name = %q, want mycli", iface.Name)
	}
	if iface.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", iface.Version)
	}
	if iface.Description != "A test CLI" {
		t.Errorf("Description = %q, want 'A test CLI'", iface.Description)
	}
}

func TestConvertToInterface_CreatesOperations(t *testing.T) {
	iface, err := synthesizeFromArtifactText(`
name "mycli"
cmd "greet" help="Say hello"
cmd "farewell" help="Say goodbye"
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(iface.Operations) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(iface.Operations))
	}
	if _, ok := iface.Operations["greet"]; !ok {
		t.Error("expected operation 'greet'")
	}
	if _, ok := iface.Operations["farewell"]; !ok {
		t.Error("expected operation 'farewell'")
	}
}

func TestConvertToInterface_BindingRefs(t *testing.T) {
	iface, err := synthesizeFromArtifactText(`
name "mycli"
cmd "greet" help="Say hello"
`)
	if err != nil {
		t.Fatal(err)
	}
	key := "greet." + DefaultSourceName
	binding, ok := iface.Bindings[key]
	if !ok {
		t.Fatalf("expected binding %q", key)
	}
	if binding.Ref != "greet" {
		t.Errorf("ref = %q, want greet", binding.Ref)
	}
	if binding.Operation != "greet" {
		t.Errorf("operation = %q, want greet", binding.Operation)
	}
}

func TestConvertToInterface_SubcommandRefs(t *testing.T) {
	iface, err := synthesizeFromArtifactText(`
name "mycli"
cmd "config" {
    cmd "set" help="Set a value"
}
`)
	if err != nil {
		t.Fatal(err)
	}
	// "config" has no help and requires a subcommand only if SubcommandRequired is set.
	// Both "config" and "config.set" should appear as operations.
	if _, ok := iface.Operations["config.set"]; !ok {
		t.Error("expected operation 'config.set'")
	}
	binding := iface.Bindings["config.set."+DefaultSourceName]
	if binding.Ref != "config set" {
		t.Errorf("ref = %q, want 'config set'", binding.Ref)
	}
}

func TestConvertToInterface_FlagSchema(t *testing.T) {
	iface, err := synthesizeFromArtifactText(`
name "mycli"
cmd "greet" help="Say hello" {
    flag "--name <value>" help="Who to greet" required=#true
    flag "--verbose" help="Enable verbose output"
}
`)
	if err != nil {
		t.Fatal(err)
	}
	op := iface.Operations["greet"]
	if op.Input == nil {
		t.Fatal("expected input schema")
	}
	props, ok := op.Input["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties")
	}
	nameSchema, ok := props["name"].(map[string]any)
	if !ok {
		t.Fatal("expected name property")
	}
	if nameSchema["type"] != "string" {
		t.Errorf("name type = %v, want string", nameSchema["type"])
	}
	verboseSchema, ok := props["verbose"].(map[string]any)
	if !ok {
		t.Fatal("expected verbose property")
	}
	if verboseSchema["type"] != "boolean" {
		t.Errorf("verbose type = %v, want boolean", verboseSchema["type"])
	}

	// required=#true flag should appear in the schema's required array
	req, _ := op.Input["required"].([]string)
	foundRequired := false
	for _, r := range req {
		if r == "name" {
			foundRequired = true
		}
	}
	if !foundRequired {
		t.Errorf("expected 'name' in required (flag has required=#true), got %v", req)
	}
}

func TestConvertToInterface_ArgSchema(t *testing.T) {
	iface, err := synthesizeFromArtifactText(`
name "mycli"
cmd "greet" help="Say hello" {
    arg "<name>" help="Who to greet"
}
`)
	if err != nil {
		t.Fatal(err)
	}
	op := iface.Operations["greet"]
	props := op.Input["properties"].(map[string]any)
	nameSchema := props["name"].(map[string]any)
	if nameSchema["type"] != "string" {
		t.Errorf("type = %v, want string", nameSchema["type"])
	}
	// Required arg should be in required list
	req, _ := op.Input["required"].([]string)
	found := false
	for _, r := range req {
		if r == "name" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'name' in required, got %v", req)
	}
}

func TestConvertToInterface_SourceEntry(t *testing.T) {
	iface, err := synthesizeFromArtifactText(`name "mycli"`)
	if err != nil {
		t.Fatal(err)
	}
	src := iface.Sources[DefaultSourceName]
	// A bare artifact synthesizes to a PRISTINE embedded source: the kdl
	// text verbatim, under the bare usage token.
	if src.Location != "" {
		t.Errorf("location = %q, want empty (embedded artifact)", src.Location)
	}
	if src.Format != "usage@"+MaxTestedVersion {
		t.Errorf("format = %q, want the bare usage token", src.Format)
	}
	if src.Content != `name "mycli"` {
		t.Errorf("expected the pristine kdl text, got %v", src.Content)
	}
}

func TestConvertToInterface_SkipsSubcommandRequired(t *testing.T) {
	iface, err := synthesizeFromArtifactText(`
name "mycli"
cmd "config" subcommand_required=#true {
    cmd "get" help="Get a value"
    cmd "set" help="Set a value"
}
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := iface.Operations["config"]; ok {
		t.Error("did not expect operation 'config' (subcommand_required)")
	}
	if _, ok := iface.Operations["config.get"]; !ok {
		t.Error("expected operation 'config.get'")
	}
	if _, ok := iface.Operations["config.set"]; !ok {
		t.Error("expected operation 'config.set'")
	}
}

func TestConvertToInterface_TopLevelGlobalFlags(t *testing.T) {
	iface, err := synthesizeFromArtifactText(`
name "mycli"
flag "-v --verbose" global=#true help="Enable verbose output"
cmd "deploy" help="Deploy the app"
cmd "status" help="Show status"
`)
	if err != nil {
		t.Fatal(err)
	}

	// Both commands should inherit the top-level global --verbose flag.
	for _, opKey := range []string{"deploy", "status"} {
		op := iface.Operations[opKey]
		if op.Input == nil {
			t.Errorf("%s: expected input schema with inherited --verbose", opKey)
			continue
		}
		props, _ := op.Input["properties"].(map[string]any)
		if _, ok := props["verbose"]; !ok {
			t.Errorf("%s: expected 'verbose' property from top-level global flag", opKey)
		}
	}
}

func TestConvertToInterface_RootOperation(t *testing.T) {
	// Single-command CLI with no subcommands should produce a root operation.
	iface, err := synthesizeFromArtifactText(`
name "grep"
bin "grep"
about "Search for patterns"
flag "-i --ignore-case" help="Ignore case"
flag "-r --recursive" help="Recursive search"
arg "<pattern>" help="Search pattern"
arg "[file]..." help="Files to search"
`)
	if err != nil {
		t.Fatal(err)
	}

	// Should have a root operation keyed by the bin name.
	op, ok := iface.Operations["grep"]
	if !ok {
		t.Fatalf("expected root operation 'grep', got operations: %v", mapKeys(iface.Operations))
	}
	if op.Description != "Search for patterns" {
		t.Errorf("description = %q, want 'Search for patterns'", op.Description)
	}
	if op.Input == nil {
		t.Fatal("expected input schema")
	}
	props := op.Input["properties"].(map[string]any)
	for _, expected := range []string{"ignore-case", "recursive", "pattern", "file"} {
		if _, ok := props[expected]; !ok {
			t.Errorf("expected property %q in root operation input", expected)
		}
	}

	// Required arg should be in required list.
	req, _ := op.Input["required"].([]string)
	found := false
	for _, r := range req {
		if r == "pattern" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'pattern' in required, got %v", req)
	}

	// The root operation binds via its unit; the unit's command is empty.
	binding := iface.Bindings["grep."+DefaultSourceName]
	if binding.Ref != "" {
		t.Errorf("root binding ref = %q, want \"\" (the root command)", binding.Ref)
	}
}

func TestConvertToInterface_RootNotSynthesizedWhenOnlyGlobals(t *testing.T) {
	// If the root level only has global flags and subcommands, no root operation.
	iface, err := synthesizeFromArtifactText(`
name "mycli"
bin "mycli"
flag "-v --verbose" global=#true
cmd "deploy" help="Deploy the app"
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := iface.Operations["mycli"]; ok {
		t.Error("did not expect root operation when only global flags exist")
	}
	if _, ok := iface.Operations["deploy"]; !ok {
		t.Error("expected operation 'deploy'")
	}
}

func TestConvertToInterface_RootAndSubcommands(t *testing.T) {
	// CLI with both root-level args and subcommands.
	iface, err := synthesizeFromArtifactText(`
name "mycli"
bin "mycli"
arg "[file]" help="Default file"
cmd "init" help="Initialize"
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := iface.Operations["mycli"]; !ok {
		t.Error("expected root operation 'mycli'")
	}
	if _, ok := iface.Operations["init"]; !ok {
		t.Error("expected operation 'init'")
	}
}

// A relative artifact path is authoring convenience: intake absolutizes it,
// so the strict loader accepts it AND the emitted source's location is
// invocable as written (the invoke lane never resolves against a base).
func TestSynthesizeInterface_RelativeLocationAbsolutized(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cli.kdl"), []byte(testSpecKDL()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{Format: "usage@" + MaxTestedVersion, Location: "cli.kdl"}},
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	var loc string
	for _, src := range iface.Sources {
		loc = src.Location
	}
	if !filepath.IsAbs(loc) {
		t.Fatalf("emitted location must be absolute (invocable as written), got %q", loc)
	}
	if _, err := artifactText(context.Background(), loc, nil); err != nil {
		t.Fatalf("emitted location must satisfy the strict invoke-lane loader: %v", err)
	}
}

// URLs get a clear refusal (usage artifacts are local files or exec:
// locators), not an os.ReadFile("https://...") error.
func TestArtifactText_URLRefusedClearly(t *testing.T) {
	_, err := artifactText(context.Background(), "https://example.com/cli.kdl", nil)
	if err == nil || !strings.Contains(err.Error(), "not fetchable") {
		t.Fatalf("want the clear not-fetchable refusal, got %v", err)
	}
}
