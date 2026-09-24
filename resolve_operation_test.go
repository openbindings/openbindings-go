package openbindings

import (
	"testing"
)

func TestResolveOperation_DirectKey(t *testing.T) {
	iface := &Interface{
		Operations: map[string]Operation{
			"createTask": {Description: Present("native")},
		},
	}
	key, op, ok := ResolveOperation(iface, "createTask")
	if !ok || key != "createTask" || (op.Description == nil || *op.Description != "native") {
		t.Fatalf("direct key resolution failed: key=%q ok=%v op=%+v", key, ok, op)
	}
}

func TestResolveOperation_Alias(t *testing.T) {
	iface := &Interface{
		Operations: map[string]Operation{
			"createTask": {Aliases: []string{"tasks.create"}},
		},
	}
	// Looking up by the alias resolves to the operation, returning its
	// canonical key (used for binding selection), not the alias.
	key, _, ok := ResolveOperation(iface, "tasks.create")
	if !ok || key != "createTask" {
		t.Fatalf("alias resolution failed: key=%q ok=%v", key, ok)
	}
}

func TestResolveOperation_NotFound(t *testing.T) {
	iface := &Interface{
		Operations: map[string]Operation{
			"createTask": {Aliases: []string{"tasks.create"}},
		},
	}
	if _, _, ok := ResolveOperation(iface, "nope"); ok {
		t.Fatalf("expected no match for unknown name")
	}
}

func TestResolveOperation_KeyAndAliasEqualStanding(t *testing.T) {
	// A name that is one operation's native key, and a different name that is
	// another operation's alias, both resolve to their own operation. Key
	// matches are not privileged: OBI-D-04 guarantees a name belongs to one op.
	iface := &Interface{
		Operations: map[string]Operation{
			"nativeThing": {Description: Present("native")},
			"otherThing":  {Aliases: []string{"sharedContract.do"}},
		},
	}
	if key, _, ok := ResolveOperation(iface, "nativeThing"); !ok || key != "nativeThing" {
		t.Fatalf("native key resolution failed: key=%q ok=%v", key, ok)
	}
	if key, _, ok := ResolveOperation(iface, "sharedContract.do"); !ok || key != "otherThing" {
		t.Fatalf("alias resolution failed: key=%q ok=%v", key, ok)
	}
}

// A name that several operations carry, in a document that violates
// OBI-D-04, resolves to none of them: no match is privileged (OBI-T-12).
func TestResolveOperation_AmbiguousNamesDoNotResolve(t *testing.T) {
	iface := &Interface{Operations: map[string]Operation{
		"a": {Aliases: []string{"shared"}},
		"b": {Aliases: []string{"shared"}},
		"c": {Aliases: []string{"d"}},
		"d": {},
	}}
	for _, name := range []string{"shared", "d"} {
		if key, _, ok := ResolveOperation(iface, name); ok {
			t.Errorf("%q resolved to %q", name, key)
		}
	}
	if key, _, ok := ResolveOperation(iface, "c"); !ok || key != "c" {
		t.Fatalf("an unambiguous key resolves: %q %v", key, ok)
	}
}
