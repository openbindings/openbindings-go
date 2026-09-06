package openbindings

import (
	"bytes"
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
			"local": {BindingSpec: "example.local@1", Location: "app://delivery"},
		},
		Bindings: map[string]BindingEntry{
			"local": {Operation: "deliver", Source: "local", Selector: "deliver"},
		},
	}
}

func TestPreparedInterfaceContentSnapshotAndIndexes(t *testing.T) {
	iface := preparedFixture()
	prepared, err := PrepareInterface(&iface)
	if err != nil {
		t.Fatalf("PrepareInterface: %v", err)
	}
	if prepared.Revision() == "" || prepared.Prepared() != prepared {
		t.Fatal("expected content revision and idempotent prepared receiver")
	}
	operation, ok := prepared.Operation("send")
	if !ok || operation.CanonicalKey != "deliver" || len(operation.BindingKeys) != 1 || len(operation.DependencyKeys) != 1 {
		t.Fatalf("operation descriptor = %#v, %v", operation, ok)
	}
	dependency, ok := prepared.Dependency("delivery")
	if !ok || dependency.OperationKey != "deliver" || !dependency.BindingSpecsPresent {
		t.Fatalf("dependency descriptor = %#v, %v", dependency, ok)
	}
	binding, ok := prepared.Binding("local")
	if !ok || binding.BindingSpec != "example.local@1" || binding.OperationKey != "deliver" {
		t.Fatalf("binding descriptor = %#v, %v", binding, ok)
	}

	// Caller mutations cannot change the private snapshot or its identity.
	iface.Operations["deliver"] = Operation{Description: "mutated"}
	if got, _ := prepared.Operation("deliver"); got.CanonicalKey != "deliver" {
		t.Fatalf("prepared operation drifted: %#v", got)
	}
	canonical := prepared.CanonicalJSON()
	canonical[0] = 'x'
	if bytes.Equal(canonical, prepared.CanonicalJSON()) {
		t.Fatal("CanonicalJSON exposed internal bytes")
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

func TestPreparedInterfaceBoundaryContractReachability(t *testing.T) {
	base := preparedFixture()
	prepared, err := PrepareInterface(&base)
	if err != nil {
		t.Fatal(err)
	}
	contract, ok, err := prepared.BoundaryContract("deliver")
	if err != nil || !ok || !contract.Complete {
		t.Fatalf("contract = %#v, %v, %v", contract, ok, err)
	}

	irrelevant := preparedFixture()
	irrelevant.Schemas["Unused"] = map[string]any{"type": "integer"}
	irrelevantPrepared, err := PrepareInterface(&irrelevant)
	if err != nil {
		t.Fatal(err)
	}
	irrelevantContract, _, err := irrelevantPrepared.BoundaryContract("deliver")
	if err != nil {
		t.Fatal(err)
	}
	if irrelevantContract.Revision != contract.Revision {
		t.Fatal("unreachable schema changed boundary identity")
	}

	relevant := preparedFixture()
	relevant.Schemas["Item"] = map[string]any{"type": "string"}
	relevantPrepared, err := PrepareInterface(&relevant)
	if err != nil {
		t.Fatal(err)
	}
	relevantContract, _, err := relevantPrepared.BoundaryContract("deliver")
	if err != nil {
		t.Fatal(err)
	}
	if relevantContract.Revision == contract.Revision {
		t.Fatal("reachable schema did not change boundary identity")
	}
}

func TestPreparedInterfaceExternalClosureIsExplicit(t *testing.T) {
	iface := preparedFixture()
	op := iface.Operations["deliver"]
	op.Input = map[string]any{"$ref": "https://schemas.example/Item"}
	iface.Operations["deliver"] = op
	prepared, err := PrepareInterface(&iface)
	if err != nil {
		t.Fatal(err)
	}
	contract, _, err := prepared.BoundaryContract("deliver")
	if err != nil {
		t.Fatal(err)
	}
	if contract.Complete || len(contract.UnavailableReferences) != 1 {
		t.Fatalf("contract = %#v", contract)
	}
}

func TestOperationExplicitNullPresenceRoundTrip(t *testing.T) {
	var iface Interface
	if err := json.Unmarshal([]byte(`{"openbindings":"0.2.0","operations":{"x":{"input":null}}}`), &iface); err != nil {
		t.Fatal(err)
	}
	op := iface.Operations["x"]
	if !op.InputPresent || op.OutputPresent {
		t.Fatalf("presence = input %v output %v", op.InputPresent, op.OutputPresent)
	}
	roundTrip, err := json.Marshal(iface)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(roundTrip, []byte(`"input":null`)) {
		t.Fatalf("round trip = %s", roundTrip)
	}
}

func TestPreparedInterfaceRetainsTypedOBIStructuralGates(t *testing.T) {
	unsafePreference := 9007199254740992.0
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
			Bindings:     map[string]BindingEntry{"binding": {Operation: "op", Source: "source", Preference: &unsafePreference}},
		},
		"explicit null operation schema": {
			OpenBindings: "0.2.0",
			Operations:   map[string]Operation{"op": {InputPresent: true}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PrepareInterface(iface); err == nil {
				t.Fatal("PrepareInterface accepted an invalid typed document")
			}
		})
	}
}
