package openbindings

import "testing"

func TestLookupDependency(t *testing.T) {
	iface := &Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"deliver":     {Aliases: []string{"events.deliver"}},
			"constructor": {},
		},
		Dependencies: map[string]DependencyEntry{
			"customerDelivery": {
				Operation:    "deliver",
				BindingSpecs: []string{"openbindings.openapi@1"},
			},
			"constructor": {Operation: "constructor"},
		},
	}

	resolved, ok := LookupDependency(iface, "customerDelivery")
	if !ok {
		t.Fatal("expected dependency to resolve")
	}
	if resolved.Key != "customerDelivery" || resolved.OperationKey != "deliver" {
		t.Fatalf("unexpected resolution: %#v", resolved)
	}
	if len(resolved.Dependency.BindingSpecs) != 1 || resolved.Dependency.BindingSpecs[0] != "openbindings.openapi@1" {
		t.Fatalf("unexpected dependency entry: %#v", resolved.Dependency)
	}

	if _, ok := LookupDependency(iface, "events.deliver"); ok {
		t.Fatal("operation alias must not resolve as a dependency key")
	}
	if resolved, ok := LookupDependency(iface, "constructor"); !ok || resolved.OperationKey != "constructor" {
		t.Fatalf("exact constructor key did not resolve: %#v, %v", resolved, ok)
	}
	if _, ok := LookupDependency(nil, "customerDelivery"); ok {
		t.Fatal("nil interface must fail closed")
	}
}

func TestLookupDependencyFailsClosedForDanglingReference(t *testing.T) {
	iface := &Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{"deliver": {}},
		Dependencies: map[string]DependencyEntry{
			"delivery": {Operation: "missing"},
		},
	}
	if _, ok := LookupDependency(iface, "delivery"); ok {
		t.Fatal("dangling dependency must fail closed")
	}
}
