package openbindings

import (
	"encoding/json"
	"testing"
)

func preparedFixture() Interface {
	return Interface{
		OpenBindings: "0.2.0",
		Schemas: map[string]JSONSchema{
			"Item": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "pattern": "^[a-z]+$"},
				},
				"required": []any{"id"},
			},
			"Unused": map[string]any{"type": "number"},
		},
		Operations: map[string]Operation{
			"deliver": {
				Aliases: []string{"send"},
				Input:   map[string]any{"$ref": "#/schemas/Item"},
				Output:  true,
			},
		},
		Dependencies: map[string]DependencyEntry{
			"delivery": {Operation: "deliver", BindingSpecs: []string{"example.local@1"}},
		},
		Sources: map[string]Source{
			"local": {BindingSpec: "example.local@1", Location: Present("app://delivery")},
		},
		Bindings: map[string]BindingEntry{
			"local": {Operation: "deliver", Source: "local", Selector: Present("deliver")},
		},
	}
}

func TestPreparedInterfaceContentSnapshotAndIndexes(t *testing.T) {
	iface := preparedFixture()
	prepared, err := PrepareInterface(&iface)
	if err != nil {
		t.Fatalf("PrepareInterface: %v", err)
	}
	if prepared.SnapshotID() == "" {
		t.Fatal("expected local correlation and idempotent prepared receiver")
	}
	operation, ok := prepared.Operation("send")
	if !ok || operation.CanonicalKey != "deliver" || len(operation.BindingKeys) != 1 || len(operation.DependencyKeys) != 1 {
		t.Fatalf("operation descriptor = %#v, %v", operation, ok)
	}
	dependency, ok := prepared.Dependency("delivery")
	if !ok || dependency.OperationKey != "deliver" || dependency.BindingSpecs == nil {
		t.Fatalf("dependency descriptor = %#v, %v", dependency, ok)
	}
	binding, ok := prepared.Binding("local")
	if !ok || binding.BindingSpec != "example.local@1" || binding.OperationKey != "deliver" {
		t.Fatalf("binding descriptor = %#v, %v", binding, ok)
	}

	// Caller mutations cannot change the private snapshot or its identity.
	iface.Operations["deliver"] = Operation{Description: Present("mutated")}
	if got, _ := prepared.Operation("deliver"); got.CanonicalKey != "deliver" {
		t.Fatalf("prepared operation drifted: %#v", got)
	}
}

func TestPreparedInterfaceDoesNotRetainNamedJSONContainers(t *testing.T) {
	type namedObject map[string]any
	type namedArray []any
	properties := namedObject{
		"id": namedObject{"type": "string", "enum": namedArray{"one", "two"}},
	}
	input := namedObject{
		"type":       "object",
		"properties": properties,
	}
	iface := Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]Operation{
			"read": {Input: input},
		},
	}
	prepared, err := PrepareInterface(&iface)
	if err != nil {
		t.Fatal(err)
	}

	properties["id"].(namedObject)["type"] = "number"
	input["additionalProperties"] = false
	first := prepared.InterfaceSnapshot()
	firstInput := first.Operations["read"].Input.(map[string]any)
	firstProperties := firstInput["properties"].(map[string]any)
	if got := firstProperties["id"].(map[string]any)["type"]; got != "string" {
		t.Fatalf("prepared snapshot retained named map storage: %#v", got)
	}
	if _, ok := firstInput["additionalProperties"]; ok {
		t.Fatal("prepared snapshot observed a later named-map member")
	}

	firstProperties["id"].(map[string]any)["type"] = "boolean"
	second := prepared.InterfaceSnapshot()
	secondInput := second.Operations["read"].Input.(map[string]any)
	secondProperties := secondInput["properties"].(map[string]any)
	if got := secondProperties["id"].(map[string]any)["type"]; got != "string" {
		t.Fatalf("InterfaceSnapshot exposed prepared storage: %#v", got)
	}
}

// A null operation schema is not a schema, and the model cannot carry it as a
// present member (nil is absence), so decoding refuses it rather than
// dropping it.
func TestOperationNullSchemaIsRefused(t *testing.T) {
	var iface Interface
	if err := json.Unmarshal([]byte(`{"openbindings":"0.2.0","operations":{"x":{"input":null}}}`), &iface); err == nil {
		t.Fatal("a null operation schema must fail decoding")
	}
}

func TestPreparedInterfaceRetainsTypedOBIStructuralGates(t *testing.T) {
	for name, iface := range map[string]*Interface{
		"empty dependency binding specs": {
			OpenBindings: "0.2.0",
			Operations:   map[string]Operation{"op": {}},
			Dependencies: map[string]DependencyEntry{"dep": {Operation: "op", BindingSpecs: []string{}}},
		},
		"unsafe binding preference": {
			OpenBindings: "0.2.0",
			Operations:   map[string]Operation{"op": {}},
			Sources:      map[string]Source{"source": {BindingSpec: "example@1", Content: json.RawMessage(`{}`)}},
			Bindings:     map[string]BindingEntry{"binding": {Operation: "op", Source: "source", Preference: Present[int64](9007199254740992)}},
		},
		"present empty version": {
			OpenBindings: "0.2.0",
			Version:      Present(""),
			Operations:   map[string]Operation{"op": {}},
		},
		"present empty source location": {
			OpenBindings: "0.2.0",
			Operations:   map[string]Operation{"op": {}},
			Sources:      map[string]Source{"source": {BindingSpec: "example@1", Location: Present(""), Content: json.RawMessage(`{}`)}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PrepareInterface(iface); err == nil {
				t.Fatal("PrepareInterface accepted an invalid typed document")
			}
		})
	}
}
