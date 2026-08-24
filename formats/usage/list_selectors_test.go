package usage

import (
	"context"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestInspectSource_BasicSelectors(t *testing.T) {
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
		t.Fatalf("expected root plus 2 command selectors, got %d", len(result.Targets))
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

	wantSelectors := map[string]bool{
		"config set": false,
		"config get": false,
		"config":     false,
	}
	for _, selector := range result.Targets {
		if _, ok := wantSelectors[selector.Selector]; ok {
			wantSelectors[selector.Selector] = true
		}
	}
	for selector, found := range wantSelectors {
		if !found {
			t.Errorf("expected selector %q not found", selector)
		}
	}
}

func TestInspectSource_RootCommandSelector(t *testing.T) {
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
		t.Fatal("expected at least 1 selector for root command")
	}

	found := false
	for _, selector := range result.Targets {
		if selector.Selector == "" { // the root command
			found = true
			var description string
			if selector.Operation != nil {
				description = selector.Operation.Description
			}
			if description != "Search for patterns" {
				t.Errorf("root description = %q, want %q", description, "Search for patterns")
			}
		}
	}
	if !found {
		t.Error("expected root command selector '#/units/grep'")
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
		t.Fatalf("expected root plus 3 selectors, got %d", len(result.Targets))
	}
	if result.Targets[0].Selector != "" {
		t.Errorf("first selector = %q, want root", result.Targets[0].Selector)
	}
	if result.Targets[1].Selector != "alpha" {
		t.Errorf("second selector = %q, want alpha", result.Targets[1].Selector)
	}
	if result.Targets[2].Selector != "mike" {
		t.Errorf("third selector = %q, want mike", result.Targets[2].Selector)
	}
	if result.Targets[3].Selector != "zulu" {
		t.Errorf("fourth selector = %q, want zulu", result.Targets[3].Selector)
	}
}

func TestInspectSource_SelectorsMatchSynthesizeInterface(t *testing.T) {
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

	createSelectors := map[string]bool{}
	for _, b := range iface.Bindings {
		createSelectors[b.Selector] = true
	}

	synthesizer := NewSynthesizer()
	result, err := synthesizer.InspectSource(context.Background(), &openbindings.Source{
		Content: openbindings.TextContent(content),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, selector := range result.Targets {
		if !createSelectors[selector.Selector] {
			t.Errorf("InspectSource selector %q not in SynthesizeInterface bindings", selector.Selector)
		}
	}
	if len(result.Targets) != len(createSelectors) {
		t.Errorf("selector count mismatch: InspectSource=%d, SynthesizeInterface=%d", len(result.Targets), len(createSelectors))
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

	for _, selector := range result.Targets {
		if selector.Selector == "#/units/config" {
			t.Error("did not expect selector '#/units/config' (subcommand_required)")
		}
	}

	wantSelectors := map[string]bool{
		"config get": false,
		"config set": false,
	}
	for _, selector := range result.Targets {
		if _, ok := wantSelectors[selector.Selector]; ok {
			wantSelectors[selector.Selector] = true
		}
	}
	for selector, found := range wantSelectors {
		if !found {
			t.Errorf("expected selector %q not found", selector)
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
		t.Errorf("expected 0 selectors, got %d", len(result.Targets))
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
