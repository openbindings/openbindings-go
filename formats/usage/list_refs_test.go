package usage

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestInspectSource_BasicRefs(t *testing.T) {
	content := `
name "mycli"
bin "mycli"
cmd "greet" help="Say hello"
cmd "farewell" help="Say goodbye"
`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 3 {
		t.Fatalf("expected root plus 2 command refs, got %d", len(result.Targets))
	}
	if !result.Exhaustive {
		t.Error("expected Exhaustive = true")
	}
}

func TestInspectSource_SpaceSeparatedPaths(t *testing.T) {
	content := `
name "mycli"
bin "mycli"
cmd "config" {
    cmd "set" help="Set a value"
    cmd "get" help="Get a value"
}
`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	wantRefs := map[string]bool{
		"config set": false,
		"config get": false,
		"config":     false,
	}
	for _, ref := range result.Targets {
		if _, ok := wantRefs[ref.Ref]; ok {
			wantRefs[ref.Ref] = true
		}
	}
	for ref, found := range wantRefs {
		if !found {
			t.Errorf("expected ref %q not found", ref)
		}
	}
}

func TestInspectSource_RootCommandRef(t *testing.T) {
	content := `
name "grep"
bin "grep"
about "Search for patterns"
flag "-i --ignore-case" help="Ignore case"
arg "<pattern>" help="Search pattern"
`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) < 1 {
		t.Fatal("expected at least 1 ref for root command")
	}

	found := false
	for _, ref := range result.Targets {
		if ref.Ref == "" { // the root command
			found = true
			var description string
			if ref.Operation != nil {
				description = ref.Operation.Description
			}
			if description != "Search for patterns" {
				t.Errorf("root description = %q, want %q", description, "Search for patterns")
			}
		}
	}
	if !found {
		t.Error("expected root command ref '#/units/grep'")
	}
}

func TestInspectSource_AlphabeticallySorted(t *testing.T) {
	content := `
name "mycli"
bin "mycli"
cmd "zulu" help="Z"
cmd "alpha" help="A"
cmd "mike" help="M"
`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 4 {
		t.Fatalf("expected root plus 3 refs, got %d", len(result.Targets))
	}
	if result.Targets[0].Ref != "" {
		t.Errorf("first ref = %q, want root", result.Targets[0].Ref)
	}
	if result.Targets[1].Ref != "alpha" {
		t.Errorf("second ref = %q, want alpha", result.Targets[1].Ref)
	}
	if result.Targets[2].Ref != "mike" {
		t.Errorf("third ref = %q, want mike", result.Targets[2].Ref)
	}
	if result.Targets[3].Ref != "zulu" {
		t.Errorf("fourth ref = %q, want zulu", result.Targets[3].Ref)
	}
}

func TestInspectSource_RefsMatchSynthesizeInterface(t *testing.T) {
	content := `
name "mycli"
bin "mycli"
cmd "greet" help="Say hello"
cmd "farewell" help="Say goodbye"
`

	iface, err := synthesizeFromArtifactText(content)
	if err != nil {
		t.Fatal(err)
	}

	createRefs := map[string]bool{}
	for _, b := range iface.Bindings {
		createRefs[b.Ref] = true
	}

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, ref := range result.Targets {
		if !createRefs[ref.Ref] {
			t.Errorf("InspectSource ref %q not in SynthesizeInterface bindings", ref.Ref)
		}
	}
	if len(result.Targets) != len(createRefs) {
		t.Errorf("ref count mismatch: InspectSource=%d, SynthesizeInterface=%d", len(result.Targets), len(createRefs))
	}
}

func TestInspectSource_SkipsSubcommandRequired(t *testing.T) {
	content := `
name "mycli"
bin "mycli"
cmd "config" subcommand_required=#true {
    cmd "get" help="Get a value"
    cmd "set" help="Set a value"
}
`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, ref := range result.Targets {
		if ref.Ref == "#/units/config" {
			t.Error("did not expect ref '#/units/config' (subcommand_required)")
		}
	}

	wantRefs := map[string]bool{
		"config get": false,
		"config set": false,
	}
	for _, ref := range result.Targets {
		if _, ok := wantRefs[ref.Ref]; ok {
			wantRefs[ref.Ref] = true
		}
	}
	for ref, found := range wantRefs {
		if !found {
			t.Errorf("expected ref %q not found", ref)
		}
	}
}

func TestInspectSource_EmptySpec(t *testing.T) {
	content := `name "mycli"`

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Targets) != 0 {
		t.Errorf("expected 0 refs, got %d", len(result.Targets))
	}
}

func TestInspectSource_DoesNotInventTargetIdentityFromName(t *testing.T) {
	content := `name "mycli"
cmd "run" help="Run it"`

	result, err := NewSynthesizer().InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Targets) != 0 || !result.Exhaustive {
		t.Fatalf("descriptor without bin must have an exhaustive empty target set, got %+v", result)
	}
}

func TestInspectSource_NilContent(t *testing.T) {
	synthesizer := NewSynthesizer()
	_, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{})
	if err == nil {
		t.Error("expected error for empty source")
	}
}
