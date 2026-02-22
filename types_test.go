package openbindings

import (
	"encoding/json"
	"testing"
)

func TestInterface_LosslessRoundTrip_PreservesExtensionsAndUnknownTopLevel(t *testing.T) {
	in := []byte(`{
  "openbindings": "0.1.0",
  "operations": {},
  "x-extensionField": "extensionFieldValue",
  "unknownField": {"value": "unknownFieldValue"}
}`)

	var i Interface
	if err := json.Unmarshal(in, &i); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if i.Extensions == nil || len(i.Extensions) != 1 {
		t.Fatalf("expected 1 extension, got %#v", i.Extensions)
	}
	if i.Unknown == nil || len(i.Unknown) != 1 {
		t.Fatalf("expected 1 unknown, got %#v", i.Unknown)
	}

	out, err := json.Marshal(i)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if outMap["x-extensionField"] != "extensionFieldValue" {
		t.Fatalf("expected x-extensionField preserved, got %#v", outMap["x-extensionField"])
	}
	unknownField, ok := outMap["unknownField"].(map[string]any)
	if !ok {
		t.Fatalf("expected unknownField preserved as object, got %#v", outMap["unknownField"])
	}
	if unknownField["value"] != "unknownFieldValue" {
		t.Fatalf("expected unknownField.value preserved, got %#v", unknownField["value"])
	}
}

func TestInterface_Marshal_KnownFieldsWinOverUnknown(t *testing.T) {
	i := Interface{
		OpenBindings: "0.1.0",
		Name:         "Good Example",
		Operations:   map[string]Operation{},
		LosslessFields: LosslessFields{
			Unknown: map[string]json.RawMessage{
				"name": json.RawMessage(`"Bad Example"`),
			},
			Extensions: map[string]json.RawMessage{
				"x-extensionField": json.RawMessage(`"extensionFieldValue"`),
			},
		},
	}

	out, err := json.Marshal(i)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if outMap["name"] != "Good Example" {
		t.Fatalf("expected typed name to win, got %#v", outMap["name"])
	}
	if outMap["x-extensionField"] != "extensionFieldValue" {
		t.Fatalf("expected x-extensionField preserved, got %#v", outMap["x-extensionField"])
	}
}

func TestOperation_LosslessRoundTrip_PreservesExtensionsAndUnknown(t *testing.T) {
	in := []byte(`{
  "kind": "event",
  "payload": {"type":"object"},
  "x-extensionField": "extensionFieldValue",
  "unknownField": {"value": "unknownFieldValue"}
}`)

	var op Operation
	outMap := mustRoundTripToMap(t, in, &op)
	assertPreservedExtensionAndUnknown(t, outMap)
}

func TestOperation_Marshal_KnownFieldsWinOverUnknown(t *testing.T) {
	op := Operation{
		Kind:    OperationKindEvent,
		Payload: JSONSchema{"type": "object"},
		LosslessFields: LosslessFields{
			Unknown:    map[string]json.RawMessage{"kind": json.RawMessage(`"method"`)},
			Extensions: map[string]json.RawMessage{"x-extensionField": json.RawMessage(`"extensionFieldValue"`)},
		},
	}

	out, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if outMap["kind"] != string(OperationKindEvent) {
		t.Fatalf("expected typed kind to win, got %#v", outMap["kind"])
	}
	if outMap["x-extensionField"] != "extensionFieldValue" {
		t.Fatalf("expected x-extensionField preserved, got %#v", outMap["x-extensionField"])
	}
}

func TestOperation_TagsRoundTrip(t *testing.T) {
	in := []byte(`{
  "kind": "method",
  "description": "Create a config entry",
  "tags": ["config", "admin"]
}`)

	var op Operation
	if err := json.Unmarshal(in, &op); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(op.Tags) != 2 || op.Tags[0] != "config" || op.Tags[1] != "admin" {
		t.Fatalf("expected tags [config, admin], got %v", op.Tags)
	}

	// Round-trip.
	out, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	tags, ok := outMap["tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Fatalf("expected tags array with 2 elements, got %#v", outMap["tags"])
	}
	if tags[0] != "config" || tags[1] != "admin" {
		t.Fatalf("expected [config, admin], got %v", tags)
	}
}

func TestOperation_TagsNotInKnownSet_WouldBeUnknown(t *testing.T) {
	// Verify tags is in the known set (not treated as unknown).
	in := []byte(`{
  "kind": "method",
  "tags": ["test"]
}`)

	var op Operation
	if err := json.Unmarshal(in, &op); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// tags should be on the typed field, not in Unknown.
	if len(op.Tags) != 1 || op.Tags[0] != "test" {
		t.Fatalf("expected tags [test], got %v", op.Tags)
	}
	if _, inUnknown := op.Unknown["tags"]; inUnknown {
		t.Fatal("tags should not be in Unknown (should be in knownOperationSet)")
	}
}

func TestOperation_OmitsEmptyTags(t *testing.T) {
	op := Operation{Kind: OperationKindMethod}

	out, err := json.Marshal(op)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	if _, hasTags := outMap["tags"]; hasTags {
		t.Fatal("expected tags omitted when empty")
	}
}

func TestSource_LosslessRoundTrip_PreservesExtensionsAndUnknown(t *testing.T) {
	in := []byte(`{
  "format": "openapi@3.1",
  "location": "./openapi.json",
  "x-extensionField": "extensionFieldValue",
  "unknownField": {"value": "unknownFieldValue"}
}`)

	var bs Source
	outMap := mustRoundTripToMap(t, in, &bs)
	assertPreservedExtensionAndUnknown(t, outMap)
}

func TestBindingEntry_LosslessRoundTrip_PreservesExtensionsAndUnknown(t *testing.T) {
	in := []byte(`{
  "operation": "logs.get",
  "source": "publicOpenapi",
  "ref": "#/paths/~1logs~1{id}/get",
  "x-extensionField": "extensionFieldValue",
  "unknownField": {"value": "unknownFieldValue"}
}`)

	var be BindingEntry
	outMap := mustRoundTripToMap(t, in, &be)
	assertPreservedExtensionAndUnknown(t, outMap)
}

func TestSatisfies_LosslessRoundTrip_PreservesExtensionsAndUnknown(t *testing.T) {
	in := []byte(`{
  "interface": "io.example@1.0",
  "operation": "op",
  "x-extensionField": "extensionFieldValue",
  "unknownField": {"value": "unknownFieldValue"}
}`)

	var s Satisfies
	outMap := mustRoundTripToMap(t, in, &s)
	assertPreservedExtensionAndUnknown(t, outMap)
}

func TestInterface_LosslessRoundTrip_PreservesNestedOperationBindingAndSatisfiesFields(t *testing.T) {
	in := []byte(`{
  "openbindings": "0.1.0",
  "operations": {
    "op": {
      "kind": "method",
      "x-extensionField": "extensionFieldValue",
      "unknownField": {"value": "unknownFieldValue"},
      "satisfies": [
        {
          "interface": "io.example@1.0",
          "operation": "op",
          "x-extensionField": "extensionFieldValue",
          "unknownField": {"value": "unknownFieldValue"}
        }
      ]
    }
  },
  "sources": {
    "src": {
      "format": "openapi@3.1",
      "location": "./openapi.json",
      "x-extensionField": "extensionFieldValue",
      "unknownField": {"value": "unknownFieldValue"}
    }
  },
  "bindings": {
    "op.src": {
      "operation": "op",
      "source": "src",
      "ref": "#/paths/~1op/get",
      "x-extensionField": "extensionFieldValue",
      "unknownField": {"value": "unknownFieldValue"}
    }
  }
}`)

	var iface Interface
	if err := json.Unmarshal(in, &iface); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(iface)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	ops, ok := outMap["operations"].(map[string]any)
	if !ok {
		t.Fatalf("expected operations object, got %#v", outMap["operations"])
	}
	op, ok := ops["op"].(map[string]any)
	if !ok {
		t.Fatalf("expected operations.op object, got %#v", ops["op"])
	}
	if op["x-extensionField"] != "extensionFieldValue" {
		t.Fatalf("expected operation x-extensionField preserved, got %#v", op["x-extensionField"])
	}
	if _, ok := op["unknownField"].(map[string]any); !ok {
		t.Fatalf("expected operation unknownField preserved, got %#v", op["unknownField"])
	}
	satList, ok := op["satisfies"].([]any)
	if !ok || len(satList) != 1 {
		t.Fatalf("expected satisfies array, got %#v", op["satisfies"])
	}
	sat, ok := satList[0].(map[string]any)
	if !ok {
		t.Fatalf("expected satisfies[0] object, got %#v", satList[0])
	}
	if sat["x-extensionField"] != "extensionFieldValue" {
		t.Fatalf("expected satisfies x-extensionField preserved, got %#v", sat["x-extensionField"])
	}
	if _, ok := sat["unknownField"].(map[string]any); !ok {
		t.Fatalf("expected satisfies unknownField preserved, got %#v", sat["unknownField"])
	}

	bsMap, ok := outMap["sources"].(map[string]any)
	if !ok {
		t.Fatalf("expected sources object, got %#v", outMap["sources"])
	}
	src, ok := bsMap["src"].(map[string]any)
	if !ok {
		t.Fatalf("expected sources.src object, got %#v", bsMap["src"])
	}
	if src["x-extensionField"] != "extensionFieldValue" {
		t.Fatalf("expected source x-extensionField preserved, got %#v", src["x-extensionField"])
	}
	if _, ok := src["unknownField"].(map[string]any); !ok {
		t.Fatalf("expected source unknownField preserved, got %#v", src["unknownField"])
	}

	bindingsMap, ok := outMap["bindings"].(map[string]any)
	if !ok || len(bindingsMap) != 1 {
		t.Fatalf("expected bindings map with 1 entry, got %#v", outMap["bindings"])
	}
	b0, ok := bindingsMap["op.src"].(map[string]any)
	if !ok {
		t.Fatalf("expected bindings[\"op.src\"] object, got %#v", bindingsMap["op.src"])
	}
	if b0["x-extensionField"] != "extensionFieldValue" {
		t.Fatalf("expected bindingEntry x-extensionField preserved, got %#v", b0["x-extensionField"])
	}
	if _, ok := b0["unknownField"].(map[string]any); !ok {
		t.Fatalf("expected bindingEntry unknownField preserved, got %#v", b0["unknownField"])
	}
}

func TestTransform_LosslessRoundTrip_PreservesExtensionsAndUnknown(t *testing.T) {
	in := []byte(`{
  "type": "jsonata",
  "expression": "{ amount: total }",
  "x-extensionField": "extensionFieldValue",
  "unknownField": {"value": "unknownFieldValue"}
}`)

	var tr Transform
	outMap := mustRoundTripToMap(t, in, &tr)
	assertPreservedExtensionAndUnknown(t, outMap)

	if tr.Type != "jsonata" {
		t.Fatalf("expected type=jsonata, got %q", tr.Type)
	}
	if tr.Expression != "{ amount: total }" {
		t.Fatalf("expected expression preserved, got %q", tr.Expression)
	}
}

func TestOperationExample_LosslessRoundTrip_PreservesExtensionsAndUnknown(t *testing.T) {
	in := []byte(`{
  "description": "Example",
  "input": {"key": "value"},
  "output": {"result": true},
  "x-extensionField": "extensionFieldValue",
  "unknownField": {"value": "unknownFieldValue"}
}`)

	var ex OperationExample
	outMap := mustRoundTripToMap(t, in, &ex)
	assertPreservedExtensionAndUnknown(t, outMap)

	if ex.Description != "Example" {
		t.Fatalf("expected description=Example, got %q", ex.Description)
	}
}

func TestTransformOrRef_RefPreservesExtensions(t *testing.T) {
	in := []byte(`{
  "$ref": "#/transforms/myTransform",
  "x-custom": "preserved"
}`)

	var tor TransformOrRef
	if err := json.Unmarshal(in, &tor); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !tor.IsRef() {
		t.Fatal("expected ref")
	}
	if tor.Ref != "#/transforms/myTransform" {
		t.Fatalf("expected ref, got %q", tor.Ref)
	}
	if tor.RefExtensions == nil || len(tor.RefExtensions) != 1 {
		t.Fatalf("expected 1 ref extension, got %#v", tor.RefExtensions)
	}

	// Round-trip
	out, err := json.Marshal(tor)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if outMap["$ref"] != "#/transforms/myTransform" {
		t.Fatalf("expected $ref preserved, got %v", outMap["$ref"])
	}
	if outMap["x-custom"] != "preserved" {
		t.Fatalf("expected x-custom preserved, got %v", outMap["x-custom"])
	}
}

func TestTransformOrRef_InlineTransform(t *testing.T) {
	in := []byte(`{
  "type": "jsonata",
  "expression": "{ charge_amount: amount }"
}`)

	var tor TransformOrRef
	if err := json.Unmarshal(in, &tor); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if tor.IsRef() {
		t.Fatal("expected inline transform, got ref")
	}
	if tor.Transform == nil {
		t.Fatal("expected transform to be non-nil")
	}
	if tor.Transform.Type != "jsonata" {
		t.Fatalf("expected type=jsonata, got %q", tor.Transform.Type)
	}
	if tor.Transform.Expression != "{ charge_amount: amount }" {
		t.Fatalf("expected expression preserved, got %q", tor.Transform.Expression)
	}

	// Round-trip
	out, err := json.Marshal(tor)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if outMap["type"] != "jsonata" {
		t.Fatalf("expected type=jsonata in output, got %v", outMap["type"])
	}
}

func TestTransformOrRef_Reference(t *testing.T) {
	in := []byte(`{
  "$ref": "#/transforms/myTransform"
}`)

	var tor TransformOrRef
	if err := json.Unmarshal(in, &tor); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !tor.IsRef() {
		t.Fatal("expected ref, got inline transform")
	}
	if tor.Ref != "#/transforms/myTransform" {
		t.Fatalf("expected ref=#/transforms/myTransform, got %q", tor.Ref)
	}
	if tor.Transform != nil {
		t.Fatal("expected transform to be nil for ref")
	}

	// Round-trip
	out, err := json.Marshal(tor)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if outMap["$ref"] != "#/transforms/myTransform" {
		t.Fatalf("expected $ref preserved in output, got %v", outMap["$ref"])
	}
	if _, hasType := outMap["type"]; hasType {
		t.Fatal("expected no type field in ref output")
	}
}

func TestBindingEntry_WithTransforms(t *testing.T) {
	in := []byte(`{
  "operation": "processPayment",
  "source": "paymentApi",
  "ref": "POST /charges",
  "inputTransform": {
    "type": "jsonata",
    "expression": "{ charge_amount: amount }"
  },
  "outputTransform": {
    "$ref": "#/transforms/fromApiOutput"
  }
}`)

	var be BindingEntry
	if err := json.Unmarshal(in, &be); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if be.InputTransform == nil {
		t.Fatal("expected inputTransform to be non-nil")
	}
	if be.InputTransform.IsRef() {
		t.Fatal("expected inputTransform to be inline")
	}
	if be.InputTransform.Transform.Type != "jsonata" {
		t.Fatalf("expected inputTransform.type=jsonata, got %q", be.InputTransform.Transform.Type)
	}

	if be.OutputTransform == nil {
		t.Fatal("expected outputTransform to be non-nil")
	}
	if !be.OutputTransform.IsRef() {
		t.Fatal("expected outputTransform to be a ref")
	}
	if be.OutputTransform.Ref != "#/transforms/fromApiOutput" {
		t.Fatalf("expected outputTransform.$ref=#/transforms/fromApiOutput, got %q", be.OutputTransform.Ref)
	}

	// Round-trip
	out, err := json.Marshal(be)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	inputTr, ok := outMap["inputTransform"].(map[string]any)
	if !ok {
		t.Fatalf("expected inputTransform object, got %#v", outMap["inputTransform"])
	}
	if inputTr["type"] != "jsonata" {
		t.Fatalf("expected inputTransform.type=jsonata, got %v", inputTr["type"])
	}

	outputTr, ok := outMap["outputTransform"].(map[string]any)
	if !ok {
		t.Fatalf("expected outputTransform object, got %#v", outMap["outputTransform"])
	}
	if outputTr["$ref"] != "#/transforms/fromApiOutput" {
		t.Fatalf("expected outputTransform.$ref preserved, got %v", outputTr["$ref"])
	}
}

func TestInterface_WithTransforms(t *testing.T) {
	in := []byte(`{
  "openbindings": "0.1.0",
  "operations": {
    "processPayment": {
      "kind": "method",
      "input": {"type": "object"},
      "output": {"type": "object"}
    }
  },
  "transforms": {
    "toStripeInput": {
      "type": "jsonata",
      "expression": "{ charge_amount: amount * 100 }"
    },
    "fromStripeOutput": {
      "type": "jsonata",
      "expression": "{ transactionId: id, status: status }"
    }
  },
  "sources": {
    "stripe": {
      "format": "openapi@3.1",
      "location": "./stripe.json"
    }
  },
  "bindings": {
    "processPayment.stripe": {
      "operation": "processPayment",
      "source": "stripe",
      "ref": "POST /charges",
      "inputTransform": { "$ref": "#/transforms/toStripeInput" },
      "outputTransform": { "$ref": "#/transforms/fromStripeOutput" }
    }
  }
}`)

	var iface Interface
	if err := json.Unmarshal(in, &iface); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify transforms parsed
	if len(iface.Transforms) != 2 {
		t.Fatalf("expected 2 transforms, got %d", len(iface.Transforms))
	}
	toStripe, ok := iface.Transforms["toStripeInput"]
	if !ok {
		t.Fatal("expected toStripeInput transform")
	}
	if toStripe.Type != "jsonata" {
		t.Fatalf("expected type=jsonata, got %q", toStripe.Type)
	}
	if toStripe.Expression != "{ charge_amount: amount * 100 }" {
		t.Fatalf("expected expression preserved, got %q", toStripe.Expression)
	}

	// Verify binding with transforms
	if len(iface.Bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(iface.Bindings))
	}
	b := iface.Bindings["processPayment.stripe"]
	if b.InputTransform == nil || !b.InputTransform.IsRef() {
		t.Fatal("expected inputTransform to be a ref")
	}
	if b.InputTransform.Ref != "#/transforms/toStripeInput" {
		t.Fatalf("expected inputTransform ref, got %q", b.InputTransform.Ref)
	}

	// Round-trip
	out, err := json.Marshal(iface)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var outMap map[string]any
	if err := json.Unmarshal(out, &outMap); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	transforms, ok := outMap["transforms"].(map[string]any)
	if !ok {
		t.Fatalf("expected transforms object, got %#v", outMap["transforms"])
	}
	if len(transforms) != 2 {
		t.Fatalf("expected 2 transforms in output, got %d", len(transforms))
	}
}

func TestTransformOrRef_Resolve(t *testing.T) {
	transforms := map[string]Transform{
		"myTransform": {
			Type:       "jsonata",
			Expression: "{ foo: bar }",
		},
	}

	// Test resolving a ref
	ref := TransformOrRef{Ref: "#/transforms/myTransform"}
	resolved := ref.Resolve(transforms)
	if resolved == nil {
		t.Fatal("expected ref to resolve")
	}
	if resolved.Type != "jsonata" {
		t.Fatalf("expected type=jsonata, got %q", resolved.Type)
	}
	if resolved.Expression != "{ foo: bar }" {
		t.Fatalf("expected expression preserved, got %q", resolved.Expression)
	}

	// Test resolving an inline transform
	inline := TransformOrRef{
		Transform: &Transform{
			Type:       "jsonata",
			Expression: "{ inline: true }",
		},
	}
	resolvedInline := inline.Resolve(transforms)
	if resolvedInline == nil {
		t.Fatal("expected inline to resolve")
	}
	if resolvedInline.Expression != "{ inline: true }" {
		t.Fatalf("expected inline expression, got %q", resolvedInline.Expression)
	}

	// Test unresolvable ref
	badRef := TransformOrRef{Ref: "#/transforms/nonexistent"}
	if badRef.Resolve(transforms) != nil {
		t.Fatal("expected unresolvable ref to return nil")
	}

	// Test malformed ref
	malformed := TransformOrRef{Ref: "notavalidref"}
	if malformed.Resolve(transforms) != nil {
		t.Fatal("expected malformed ref to return nil")
	}
}
