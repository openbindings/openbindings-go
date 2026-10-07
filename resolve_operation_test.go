package openbindings

import (
	"slices"
	"testing"
)

func TestResolveOperation_DirectKey(t *testing.T) {
	iface := &Document{
		Operations: map[string]Operation{
			"createTask": {Description: Present("native")},
		},
	}
	key, op, ok := iface.ResolveOperation("createTask")
	if !ok || key != "createTask" || (op.Description == nil || *op.Description != "native") {
		t.Fatalf("direct key resolution failed: key=%q ok=%v op=%+v", key, ok, op)
	}
}

func TestResolveOperation_Alias(t *testing.T) {
	iface := &Document{
		Operations: map[string]Operation{
			"createTask": {Aliases: []string{"tasks.create"}},
		},
	}
	// Looking up by the alias resolves to the operation, returning its
	// canonical key (used for binding selection), not the alias.
	key, _, ok := iface.ResolveOperation("tasks.create")
	if !ok || key != "createTask" {
		t.Fatalf("alias resolution failed: key=%q ok=%v", key, ok)
	}
}

func TestResolveOperation_NotFound(t *testing.T) {
	iface := &Document{
		Operations: map[string]Operation{
			"createTask": {Aliases: []string{"tasks.create"}},
		},
	}
	if _, _, ok := iface.ResolveOperation("nope"); ok {
		t.Fatalf("expected no match for unknown name")
	}
}

func TestResolveOperation_KeyAndAliasEqualStanding(t *testing.T) {
	// A name that is one operation's native key, and a different name that is
	// another operation's alias, both resolve to their own operation. Key
	// matches are not privileged: OBI-05 guarantees a name belongs to one op.
	iface := &Document{
		Operations: map[string]Operation{
			"nativeThing": {Description: Present("native")},
			"otherThing":  {Aliases: []string{"sharedContract.do"}},
		},
	}
	if key, _, ok := iface.ResolveOperation("nativeThing"); !ok || key != "nativeThing" {
		t.Fatalf("native key resolution failed: key=%q ok=%v", key, ok)
	}
	if key, _, ok := iface.ResolveOperation("sharedContract.do"); !ok || key != "otherThing" {
		t.Fatalf("alias resolution failed: key=%q ok=%v", key, ok)
	}
}

// A name that several operations carry, in a document that violates
// OBI-05, resolves to none of them: no match is privileged (OBI-T-06).
func TestResolveOperation_AmbiguousNamesDoNotResolve(t *testing.T) {
	iface := &Document{Operations: map[string]Operation{
		"a": {Aliases: []string{"shared"}},
		"b": {Aliases: []string{"shared"}},
		"c": {Aliases: []string{"d"}},
		"d": {},
	}}
	for _, name := range []string{"shared", "d"} {
		if key, _, ok := iface.ResolveOperation(name); ok {
			t.Errorf("%q resolved to %q", name, key)
		}
	}
	if key, _, ok := iface.ResolveOperation("c"); !ok || key != "c" {
		t.Fatalf("an unambiguous key resolves: %q %v", key, ok)
	}
}

func TestResolveOperation_NilDocument(t *testing.T) {
	var doc *Document
	if key, op, ok := doc.ResolveOperation("createTask"); ok || key != "" || op.Description != nil {
		t.Fatalf("a nil Document resolved %q: %+v %v", key, op, ok)
	}
}

// OperationBindings finds an operation's bindings by its key alone
// (OBI-T-06), sorted for presentation.
func TestOperationBindings(t *testing.T) {
	doc := &Document{
		Operations: map[string]Operation{
			"createTask": {Aliases: []string{"tasks.create"}},
			"listTasks":  {},
			"archive":    {},
		},
		Bindings: map[string]Binding{
			"createTask.mcp":  {Operation: "createTask", Source: "mcp"},
			"createTask.http": {Operation: "createTask", Source: "http", Preference: Present[int64](10)},
			"createTask.grpc": {Operation: "createTask", Source: "grpc"},
			"listTasks.http":  {Operation: "listTasks", Source: "http"},
			// A binding naming no operation key, in a document violating
			// OBI-06, is no operation's binding.
			"orphan.http": {Operation: "gone", Source: "http"},
		},
	}
	for _, tc := range []struct {
		key  string
		want []string
	}{
		// Sorted by key, whatever the preferences say.
		{"createTask", []string{"createTask.grpc", "createTask.http", "createTask.mcp"}},
		{"listTasks", []string{"listTasks.http"}},
		// An operation no binding realizes.
		{"archive", nil},
		// An alias finds nothing: a name is resolved first.
		{"tasks.create", nil},
		// A key no operation has, even one a binding names.
		{"gone", nil},
		{"CreateTask", nil},
		{"", nil},
	} {
		got := doc.OperationBindings(tc.key)
		if !slices.Equal(got, tc.want) || (tc.want == nil) != (got == nil) {
			t.Errorf("OperationBindings(%q) = %#v, want %#v", tc.key, got, tc.want)
		}
	}
	key, _, _ := doc.ResolveOperation("tasks.create")
	if got := doc.OperationBindings(key); len(got) != 3 {
		t.Errorf("the resolved key finds the operation's bindings: %v", got)
	}
	// The result is the caller's.
	doc.OperationBindings("createTask")[0] = "changed"
	if got := doc.OperationBindings("createTask"); got[0] != "createTask.grpc" {
		t.Errorf("a caller's change reached the next result: %v", got)
	}
	var none *Document
	if got := none.OperationBindings("createTask"); got != nil {
		t.Errorf("a nil Document found %v", got)
	}
}
