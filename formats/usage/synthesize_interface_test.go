package usage

import (
	"testing"
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
	spec := mustParse(t, `
name "mycli"
version "1.2.3"
about "A test CLI"
cmd "greet" help="Say hello"
`)
	iface, err := convertToInterfaceWithSpec(spec, "cli.kdl")
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
	spec := mustParse(t, `
name "mycli"
cmd "greet" help="Say hello"
cmd "farewell" help="Say goodbye"
`)
	iface, err := convertToInterfaceWithSpec(spec, "cli.kdl")
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
	spec := mustParse(t, `
name "mycli"
cmd "greet" help="Say hello"
`)
	iface, err := convertToInterfaceWithSpec(spec, "cli.kdl")
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
	spec := mustParse(t, `
name "mycli"
cmd "config" {
    cmd "set" help="Set a value"
}
`)
	iface, err := convertToInterfaceWithSpec(spec, "")
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
	spec := mustParse(t, `
name "mycli"
cmd "greet" help="Say hello" {
    flag "--name <value>" help="Who to greet" required=#true
    flag "--verbose" help="Enable verbose output"
}
`)
	iface, err := convertToInterfaceWithSpec(spec, "")
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
	spec := mustParse(t, `
name "mycli"
cmd "greet" help="Say hello" {
    arg "<name>" help="Who to greet"
}
`)
	iface, err := convertToInterfaceWithSpec(spec, "")
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
	spec := mustParse(t, `name "mycli"`)
	iface, err := convertToInterfaceWithSpec(spec, "/path/to/cli.kdl")
	if err != nil {
		t.Fatal(err)
	}
	src := iface.Sources[DefaultSourceName]
	if src.Location != "/path/to/cli.kdl" {
		t.Errorf("location = %q, want /path/to/cli.kdl", src.Location)
	}
	// Format should include the version
	if src.Format == "" {
		t.Error("expected non-empty format")
	}
}

func TestConvertToInterface_SkipsSubcommandRequired(t *testing.T) {
	spec := mustParse(t, `
name "mycli"
cmd "config" subcommand_required=#true {
    cmd "get" help="Get a value"
    cmd "set" help="Set a value"
}
`)
	iface, err := convertToInterfaceWithSpec(spec, "")
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
	spec := mustParse(t, `
name "mycli"
flag "-v --verbose" global=#true help="Enable verbose output"
cmd "deploy" help="Deploy the app"
cmd "status" help="Show status"
`)
	iface, err := convertToInterfaceWithSpec(spec, "")
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
	spec := mustParse(t, `
name "grep"
bin "grep"
about "Search for patterns"
flag "-i --ignore-case" help="Ignore case"
flag "-r --recursive" help="Recursive search"
arg "<pattern>" help="Search pattern"
arg "[file]..." help="Files to search"
`)
	iface, err := convertToInterfaceWithSpec(spec, "cli.kdl")
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

	// Binding should have empty ref for root invocation.
	binding := iface.Bindings["grep."+DefaultSourceName]
	if binding.Ref != "" {
		t.Errorf("root binding ref = %q, want empty", binding.Ref)
	}
}

func TestConvertToInterface_RootNotSynthesizedWhenOnlyGlobals(t *testing.T) {
	// If the root level only has global flags and subcommands, no root operation.
	spec := mustParse(t, `
name "mycli"
bin "mycli"
flag "-v --verbose" global=#true
cmd "deploy" help="Deploy the app"
`)
	iface, err := convertToInterfaceWithSpec(spec, "")
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
	spec := mustParse(t, `
name "mycli"
bin "mycli"
arg "[file]" help="Default file"
cmd "init" help="Initialize"
`)
	iface, err := convertToInterfaceWithSpec(spec, "")
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
