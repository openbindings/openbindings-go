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

// emissionTestKDL is a minimal artifact carrying name+version so the
// synthesized document exercises Interface.Validate end to end.
const emissionTestKDL = `name "mycli"
version "1.0.0"
about "A test CLI"
cmd "greet" help="Say hello"
`

// A local FILE path — relative or absolute — is a relative reference under
// OBI-D-05 and must never ride the emitted location: synthesis takes the
// embed-default lane (pristine artifact as content, machine-coupled path
// kept out of the document), and the emitted document passes the SDK's own
// validator. Intake still absolutizes relative paths so the strict loader
// (one loader for every lane) reads them.
func TestSynthesizeInterface_FilePathEmitsEmbeddedContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cli.kdl")
	if err := os.WriteFile(path, []byte(emissionTestKDL), 0o644); err != nil {
		t.Fatal(err)
	}

	for name, location := range map[string]string{"relative": "cli.kdl", "absolute": path} {
		t.Run(name, func(t *testing.T) {
			if name == "relative" {
				t.Chdir(dir)
			}
			iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
				Sources: []openbindings.SynthesizeSource{{Format: "usage@" + MaxTestedVersion, Location: location}},
			})
			if err != nil {
				t.Fatalf("synthesize: %v", err)
			}
			src := iface.Sources[DefaultSourceName]
			if src.Location != "" {
				t.Errorf("emitted location = %q, want empty (a file path is not a conformant OBI-D-05 location)", src.Location)
			}
			if src.Content != emissionTestKDL {
				t.Errorf("emitted content must be the pristine artifact text, got %v", src.Content)
			}
			if err := iface.Validate(); err != nil {
				t.Errorf("synthesized document must pass Interface.Validate: %v", err)
			}
		})
	}
}

// writeEmitterScript writes an executable that prints emissionTestKDL on
// stdout (ignoring any arguments), returning its absolute path.
func writeEmitterScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "emitter")
	script := "#!/bin/sh\ncat <<'EOF'\n" + emissionTestKDL + "EOF\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// An exec: locator WITHOUT arguments is a well-formed URI, so it survives
// OBI-D-05 verbatim: synthesis preserves it as the emitted location (the
// live-generated-descriptor lane) and the document validates.
func TestSynthesizeInterface_SpacelessExecLocatorEmitsLocation(t *testing.T) {
	locator := "exec:" + writeEmitterScript(t)
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{Format: "usage@" + MaxTestedVersion, Location: locator}},
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	src := iface.Sources[DefaultSourceName]
	if src.Location != locator {
		t.Errorf("emitted location = %q, want the exec: locator %q", src.Location, locator)
	}
	if src.Content != nil {
		t.Errorf("a URI-valid exec: locator emits by reference, got embedded content %v", src.Content)
	}
	if err := iface.Validate(); err != nil {
		t.Errorf("synthesized document must pass Interface.Validate: %v", err)
	}
}

// An exec: locator WITH arguments contains spaces and is not a well-formed
// URI (OBI-D-05 rejects it as a document location). Synthesis runs the
// locator and embeds the generated artifact instead; the conformant
// *reference* form for spaced locators (percent-encoding vs refusal) is a
// recorded open point in the conventions doc, deliberately not invented
// here.
func TestSynthesizeInterface_SpacedExecLocatorEmbeds(t *testing.T) {
	locator := "exec:" + writeEmitterScript(t) + " --spec"
	iface, err := NewSynthesizer().SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{Format: "usage@" + MaxTestedVersion, Location: locator}},
	})
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	src := iface.Sources[DefaultSourceName]
	if src.Location != "" {
		t.Errorf("emitted location = %q, want empty (a spaced exec: locator is not a conformant OBI-D-05 location)", src.Location)
	}
	if src.Content != emissionTestKDL {
		t.Errorf("emitted content must be the generated artifact text, got %v", src.Content)
	}
	if err := iface.Validate(); err != nil {
		t.Errorf("synthesized document must pass Interface.Validate: %v", err)
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

// A usage `alias` is command-scoped CLI shorthand; an OBI alias is a
// satisfaction identifier in the document's FLAT namespace (OBI-D-04).
// Mapping CLI aliases across produced invalid documents the moment two
// command groups both declared `alias "ls"` — synthesize output must always
// pass the SDK's own validator.
func TestSynthesizeInterface_CLIAliasesAreNotSatisfactionAliases(t *testing.T) {
	iface, err := synthesizeFromArtifactText(`
name "mycli"
version "1.0.0"
cmd "context" {
  cmd "list" help="List contexts" {
    alias "ls"
  }
}
cmd "source" {
  cmd "list" help="List sources" {
    alias "ls"
  }
}
`)
	if err != nil {
		t.Fatal(err)
	}
	for key, op := range iface.Operations {
		if len(op.Aliases) != 0 {
			t.Errorf("operation %q carries CLI shorthand as satisfaction aliases: %v", key, op.Aliases)
		}
	}
	if err := iface.Validate(); err != nil {
		t.Errorf("synthesize output must validate: %v", err)
	}
}
