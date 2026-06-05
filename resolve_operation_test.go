package openbindings

import (
	"reflect"
	"testing"
)

func TestResolveOperation_DirectKey(t *testing.T) {
	iface := &Interface{
		Operations: map[string]Operation{
			"createTask": {Description: "native"},
		},
	}
	key, op, ok := ResolveOperation(iface, "createTask")
	if !ok || key != "createTask" || op.Description != "native" {
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
	// matches are not privileged: OBI-D-05 guarantees a name belongs to one op.
	iface := &Interface{
		Operations: map[string]Operation{
			"nativeThing": {Description: "native"},
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

func TestAllOperationIdentifiers(t *testing.T) {
	iface := &Interface{
		Operations: map[string]Operation{
			"createTask": {Aliases: []string{"tasks.create", "addTask"}},
			"listTasks":  {},
		},
	}
	got := AllOperationIdentifiers(iface)
	want := []string{"addTask", "createTask", "listTasks", "tasks.create"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("identifiers = %v, want %v", got, want)
	}
}
