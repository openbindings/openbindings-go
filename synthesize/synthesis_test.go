package synthesize

import (
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestSynthesisSkeletonRejectsInvalidTargetVersion(t *testing.T) {
	if _, err := SynthesisSkeleton(&SynthesizeInput{OpenBindingsVersion: "not-semver"}); err == nil {
		t.Fatal("expected an invalid target version to refuse rather than emit an invalid OBI")
	}
}

func TestFinalizeSynthesisAppliesSharedAuthoringDirectives(t *testing.T) {
	iface := openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Operations:   map[string]openbindings.Operation{"run": {}},
		Sources:      map[string]openbindings.Source{"default": {BindingSpec: "example.spec@1", Location: "https://old.example/spec"}},
		Bindings:     map[string]openbindings.BindingEntry{"run.default": {Operation: "run", Source: "default", Ref: "run"}},
	}
	in := &SynthesizeInput{
		OpenBindingsVersion: openbindings.MaxTestedVersion,
		Name:                "contract",
		Version:             "v1",
		Description:         "interface description",
		Sources: []SynthesizeSource{{
			BindingSpec:    "example.spec@1",
			Name:           "artifact",
			OutputLocation: "https://published.example/spec",
			Description:    "source description",
		}},
	}
	if err := FinalizeSynthesis(&iface, in, "default", "example.spec@1"); err != nil {
		t.Fatal(err)
	}
	if iface.Name != "contract" || iface.Version != "v1" || iface.Description != "interface description" {
		t.Fatalf("interface overrides were not applied: %+v", iface)
	}
	source, ok := iface.Sources["artifact"]
	if !ok || source.Location != "https://published.example/spec" || source.Description != "source description" {
		t.Fatalf("source directives were not applied: %+v", iface.Sources)
	}
	if iface.Bindings["run.default"].Source != "artifact" {
		t.Fatalf("binding source was not renamed: %+v", iface.Bindings)
	}
}

func TestFinalizeSynthesisRejectsWrongExactBindingSpec(t *testing.T) {
	iface := openbindings.Interface{OpenBindings: openbindings.MaxTestedVersion, Operations: map[string]openbindings.Operation{}, Sources: map[string]openbindings.Source{"default": {BindingSpec: "example.spec@1"}}}
	err := FinalizeSynthesis(&iface, &SynthesizeInput{Sources: []SynthesizeSource{{BindingSpec: "example.spec@2"}}}, "default", "example.spec@1")
	if err == nil {
		t.Fatal("expected exact binding-specification mismatch to refuse")
	}
}
