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
  "description": "test operation",
  "x-extensionField": "extensionFieldValue",
  "unknownField": {"value": "unknownFieldValue"}
}`)

	var op Operation
	outMap := mustRoundTripToMap(t, in, &op)
	assertPreservedExtensionAndUnknown(t, outMap)
}

func TestOperation_Marshal_KnownFieldsWinOverUnknown(t *testing.T) {
	op := Operation{
		Description: "typed description",
		LosslessFields: LosslessFields{
			Unknown:    map[string]json.RawMessage{"description": json.RawMessage(`"unknown description"`)},
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

	if outMap["description"] != "typed description" {
		t.Fatalf("expected typed description to win, got %#v", outMap["description"])
	}
	if outMap["x-extensionField"] != "extensionFieldValue" {
		t.Fatalf("expected x-extensionField preserved, got %#v", outMap["x-extensionField"])
	}
}

func TestOperation_TagsRoundTrip(t *testing.T) {
	in := []byte(`{
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
	op := Operation{}

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

	if outMap["format"] != "openapi@3.1" {
		t.Fatalf("expected format preserved, got %#v", outMap["format"])
	}
	if outMap["location"] != "./openapi.json" {
		t.Fatalf("expected location preserved, got %#v", outMap["location"])
	}
}

func TestSource_Marshal_KnownFieldsWinOverUnknown(t *testing.T) {
	s := Source{
		Format:   "openapi@3.1",
		Location: "./typed-location.json",
		LosslessFields: LosslessFields{
			Unknown: map[string]json.RawMessage{
				"format":   json.RawMessage(`"grpc@1.0"`),
				"location": json.RawMessage(`"./unknown-location.json"`),
			},
			Extensions: map[string]json.RawMessage{
				"x-custom": json.RawMessage(`"kept"`),
			},
		},
	}

	out := mustMarshalJSON(t, s)
	outMap := mustUnmarshalToMap(t, out)

	if outMap["format"] != "openapi@3.1" {
		t.Fatalf("expected typed format to win, got %#v", outMap["format"])
	}
	if outMap["location"] != "./typed-location.json" {
		t.Fatalf("expected typed location to win, got %#v", outMap["location"])
	}
	if outMap["x-custom"] != "kept" {
		t.Fatalf("expected extension preserved, got %#v", outMap["x-custom"])
	}
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

	if outMap["operation"] != "logs.get" {
		t.Fatalf("expected operation preserved, got %#v", outMap["operation"])
	}
	if outMap["source"] != "publicOpenapi" {
		t.Fatalf("expected source preserved, got %#v", outMap["source"])
	}
	if outMap["ref"] != "#/paths/~1logs~1{id}/get" {
		t.Fatalf("expected ref preserved, got %#v", outMap["ref"])
	}
}

func TestBindingEntry_Marshal_KnownFieldsWinOverUnknown(t *testing.T) {
	be := BindingEntry{
		Operation:   "typed.op",
		Source:      "typedSource",
		Ref:         "#/typed/ref",
		Description: "typed description",
		LosslessFields: LosslessFields{
			Unknown: map[string]json.RawMessage{
				"operation":   json.RawMessage(`"unknown.op"`),
				"source":      json.RawMessage(`"unknownSource"`),
				"ref":         json.RawMessage(`"#/unknown/ref"`),
				"description": json.RawMessage(`"unknown description"`),
			},
			Extensions: map[string]json.RawMessage{
				"x-custom": json.RawMessage(`"kept"`),
			},
		},
	}

	out := mustMarshalJSON(t, be)
	outMap := mustUnmarshalToMap(t, out)

	if outMap["operation"] != "typed.op" {
		t.Fatalf("expected typed operation to win, got %#v", outMap["operation"])
	}
	if outMap["source"] != "typedSource" {
		t.Fatalf("expected typed source to win, got %#v", outMap["source"])
	}
	if outMap["ref"] != "#/typed/ref" {
		t.Fatalf("expected typed ref to win, got %#v", outMap["ref"])
	}
	if outMap["description"] != "typed description" {
		t.Fatalf("expected typed description to win, got %#v", outMap["description"])
	}
	if outMap["x-custom"] != "kept" {
		t.Fatalf("expected extension preserved, got %#v", outMap["x-custom"])
	}
}

func TestSatisfies_LosslessRoundTrip_PreservesExtensionsAndUnknown(t *testing.T) {
	in := []byte(`{
  "role": "io.example@1.0",
  "operation": "op",
  "x-extensionField": "extensionFieldValue",
  "unknownField": {"value": "unknownFieldValue"}
}`)

	var s Satisfies
	outMap := mustRoundTripToMap(t, in, &s)
	assertPreservedExtensionAndUnknown(t, outMap)

	if outMap["role"] != "io.example@1.0" {
		t.Fatalf("expected role preserved, got %#v", outMap["role"])
	}
	if outMap["operation"] != "op" {
		t.Fatalf("expected operation preserved, got %#v", outMap["operation"])
	}
}

func TestSatisfies_Marshal_KnownFieldsWinOverUnknown(t *testing.T) {
	s := Satisfies{
		Role:      "typed.role@2.0",
		Operation: "typedOp",
		LosslessFields: LosslessFields{
			Unknown: map[string]json.RawMessage{
				"role":      json.RawMessage(`"unknown.role@1.0"`),
				"operation": json.RawMessage(`"unknownOp"`),
			},
			Extensions: map[string]json.RawMessage{
				"x-custom": json.RawMessage(`"kept"`),
			},
		},
	}

	out := mustMarshalJSON(t, s)
	outMap := mustUnmarshalToMap(t, out)

	if outMap["role"] != "typed.role@2.0" {
		t.Fatalf("expected typed role to win, got %#v", outMap["role"])
	}
	if outMap["operation"] != "typedOp" {
		t.Fatalf("expected typed operation to win, got %#v", outMap["operation"])
	}
	if outMap["x-custom"] != "kept" {
		t.Fatalf("expected extension preserved, got %#v", outMap["x-custom"])
	}
}

func TestInterface_LosslessRoundTrip_PreservesNestedOperationBindingAndSatisfiesFields(t *testing.T) {
	in := []byte(`{
  "openbindings": "0.1.0",
  "operations": {
    "op": {
      "x-extensionField": "extensionFieldValue",
      "unknownField": {"value": "unknownFieldValue"},
      "satisfies": [
        {
          "role": "io.example@1.0",
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

func TestTransform_StringRoundTrip(t *testing.T) {
	// Per v0.2 spec §6.5, transforms are JSONata 2.0 expression strings.
	// The previous object form ({type, expression, x-*, unknown}) is gone.
	in := []byte(`"{ amount: total }"`)

	var tr Transform
	if err := json.Unmarshal(in, &tr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(tr) != "{ amount: total }" {
		t.Fatalf("expected expression preserved, got %q", string(tr))
	}

	out, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != `"{ amount: total }"` {
		t.Fatalf("expected JSON string round-trip, got %s", string(out))
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
	if outMap["description"] != "Example" {
		t.Fatalf("expected description preserved in output, got %#v", outMap["description"])
	}
	inputObj, ok := outMap["input"].(map[string]any)
	if !ok {
		t.Fatalf("expected input preserved as object, got %#v", outMap["input"])
	}
	if inputObj["key"] != "value" {
		t.Fatalf("expected input.key=value, got %#v", inputObj["key"])
	}
	outputObj, ok := outMap["output"].(map[string]any)
	if !ok {
		t.Fatalf("expected output preserved as object, got %#v", outMap["output"])
	}
	if outputObj["result"] != true {
		t.Fatalf("expected output.result=true, got %#v", outputObj["result"])
	}
}

func TestOperationExample_Marshal_KnownFieldsWinOverUnknown(t *testing.T) {
	ex := OperationExample{
		Description: "typed description",
		Input:       map[string]any{"typed": true},
		Output:      map[string]any{"typed": true},
		LosslessFields: LosslessFields{
			Unknown: map[string]json.RawMessage{
				"description": json.RawMessage(`"unknown description"`),
				"input":       json.RawMessage(`{"unknown": true}`),
				"output":      json.RawMessage(`{"unknown": true}`),
			},
			Extensions: map[string]json.RawMessage{
				"x-custom": json.RawMessage(`"kept"`),
			},
		},
	}

	out := mustMarshalJSON(t, ex)
	outMap := mustUnmarshalToMap(t, out)

	if outMap["description"] != "typed description" {
		t.Fatalf("expected typed description to win, got %#v", outMap["description"])
	}
	inputObj, ok := outMap["input"].(map[string]any)
	if !ok {
		t.Fatalf("expected input to be object, got %#v", outMap["input"])
	}
	if inputObj["typed"] != true {
		t.Fatalf("expected typed input to win, got %#v", inputObj)
	}
	if _, hasUnknown := inputObj["unknown"]; hasUnknown {
		t.Fatal("expected unknown input field to be overridden")
	}
	outputObj, ok := outMap["output"].(map[string]any)
	if !ok {
		t.Fatalf("expected output to be object, got %#v", outMap["output"])
	}
	if outputObj["typed"] != true {
		t.Fatalf("expected typed output to win, got %#v", outputObj)
	}
	if outMap["x-custom"] != "kept" {
		t.Fatalf("expected extension preserved, got %#v", outMap["x-custom"])
	}
}

func TestTransformOrRef_RefIgnoresExtraFields(t *testing.T) {
	in := []byte(`{
  "$ref": "#/transforms/myTransform",
  "x-custom": "ignored"
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

	// Round-trip produces only $ref
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
	if _, hasExtra := outMap["x-custom"]; hasExtra {
		t.Fatal("expected x-custom to be dropped per schema additionalProperties:false")
	}
}

func TestTransformOrRef_InlineTransform(t *testing.T) {
	// Per v0.2 spec §6.5, an inline transform is a bare JSONata expression string.
	in := []byte(`"{ charge_amount: amount }"`)

	var tor TransformOrRef
	if err := json.Unmarshal(in, &tor); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if tor.IsRef() {
		t.Fatal("expected inline transform, got ref")
	}
	if tor.Inline != "{ charge_amount: amount }" {
		t.Fatalf("expected expression preserved, got %q", tor.Inline)
	}

	// Round-trip: inline transforms marshal back to JSON strings.
	out, err := json.Marshal(tor)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != `"{ charge_amount: amount }"` {
		t.Fatalf("expected JSON string round-trip, got %s", string(out))
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
	if tor.Inline != "" {
		t.Fatalf("expected Inline to be empty for ref, got %q", tor.Inline)
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
  "inputTransform": "{ charge_amount: amount }",
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
	if be.InputTransform.Inline != "{ charge_amount: amount }" {
		t.Fatalf("expected inputTransform inline expression, got %q", be.InputTransform.Inline)
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

	if s, ok := outMap["inputTransform"].(string); !ok || s != "{ charge_amount: amount }" {
		t.Fatalf("expected inputTransform to round-trip as string, got %#v", outMap["inputTransform"])
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
      "input": {"type": "object"},
      "output": {"type": "object"}
    }
  },
  "transforms": {
    "toStripeInput": "{ charge_amount: amount * 100 }",
    "fromStripeOutput": "{ transactionId: id, status: status }"
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
	if string(toStripe) != "{ charge_amount: amount * 100 }" {
		t.Fatalf("expected expression preserved, got %q", string(toStripe))
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
	if s, ok := transforms["toStripeInput"].(string); !ok || s != "{ charge_amount: amount * 100 }" {
		t.Fatalf("expected toStripeInput to be a string in output, got %#v", transforms["toStripeInput"])
	}
}

func TestTransformOrRef_Resolve(t *testing.T) {
	transforms := map[string]Transform{
		"myTransform": "{ foo: bar }",
	}

	// Resolving a ref returns the named expression.
	ref := TransformOrRef{Ref: "#/transforms/myTransform"}
	expr, ok := ref.Resolve(transforms)
	if !ok {
		t.Fatal("expected ref to resolve")
	}
	if expr != "{ foo: bar }" {
		t.Fatalf("expected expression preserved, got %q", expr)
	}

	// Resolving an inline TransformOrRef returns the inline expression.
	inline := TransformOrRef{Inline: "{ inline: true }"}
	inlineExpr, ok := inline.Resolve(transforms)
	if !ok {
		t.Fatal("expected inline to resolve")
	}
	if inlineExpr != "{ inline: true }" {
		t.Fatalf("expected inline expression, got %q", inlineExpr)
	}

	// Unresolvable ref returns ok=false.
	badRef := TransformOrRef{Ref: "#/transforms/nonexistent"}
	if _, ok := badRef.Resolve(transforms); ok {
		t.Fatal("expected unresolvable ref to return ok=false")
	}

	// Malformed ref (wrong prefix) returns ok=false.
	malformed := TransformOrRef{Ref: "notavalidref"}
	if _, ok := malformed.Resolve(transforms); ok {
		t.Fatal("expected malformed ref to return ok=false")
	}
}
