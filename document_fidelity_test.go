package openbindings

import (
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/internal/schemacompiler"
)

// decodedJSON decodes data to a generic JSON value for comparing documents as
// JSON values rather than as bytes.
func decodedJSON(t *testing.T, data []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return value
}

// Every member of a document the model decodes survives re-encoding,
// including members whose value is a Go zero value.
func TestDocumentModel_RoundTripsEveryMember(t *testing.T) {
	documents := map[string]string{
		"present empty and false members": `{"openbindings":"0.2.0","name":"","version":"","description":"",
			"schemas":{},"dependencies":{},
			"operations":{"a":{"description":"","deprecated":false,"tags":[],"aliases":[],"idempotent":false,"examples":{}}},
			"sources":{"s":{"bindingSpec":"x@1","content":"","description":""}},
			"bindings":{"b":{"operation":"a","source":"s","content":"","description":"","deprecated":false,"preference":0}}}`,
		"absent optional members": `{"openbindings":"0.2.0","operations":{"a":{}},"sources":{"s":{"bindingSpec":"x@1"}},
			"bindings":{"b":{"operation":"a","source":"s"}}}`,
		"operations absent": `{"openbindings":"0.2.0"}`,
		"operations empty":  `{"openbindings":"0.2.0","operations":{}}`,
		"null where null is a value": `{"openbindings":"0.2.0","operations":{"a":{"examples":{"e":{"input":null,"output":null}}}},
			"sources":{"s":{"bindingSpec":"x@1","content":null}},
			"bindings":{"b":{"operation":"a","source":"s","content":null}}}`,
		"source and binding content of every JSON type": `{"openbindings":"0.2.0","operations":{"a":{}},
			"sources":{"o":{"bindingSpec":"x@1","content":{"location":"a.json"}},"a":{"bindingSpec":"x@1","content":[1,"two"]},
				"n":{"bindingSpec":"x@1","content":7.50},"b":{"bindingSpec":"x@1","content":false}},
			"bindings":{"o":{"operation":"a","source":"o","content":{"path":"/a"}},"a":{"operation":"a","source":"a","content":["a",1]},
				"n":{"operation":"a","source":"n","content":1e2},"b":{"operation":"a","source":"b","content":true}}}`,
		"a member the model no longer names is kept": `{"openbindings":"0.2.0","operations":{},
			"sources":{"s":{"bindingSpec":"x@1","location":null}}}`,
		"schemas of every form": `{"openbindings":"0.2.0","schemas":{"t":true,"f":false,"e":{},"n":null},
			"operations":{"a":{"input":{},"output":false}}}`,
		"members this version removed are kept": `{"openbindings":"0.2.0","operations":{"a":{}},"sources":{"s":{"bindingSpec":"x@1","content":{}}},
			"transforms":{"t":"$","n":null},
			"bindings":{"b":{"operation":"a","source":"s","selector":null,"inputTransform":{"$ref":"","x-note":"kept","later":[1]},"outputTransform":""}}}`,
		"preference spellings": `{"openbindings":"0.2.0","operations":{"a":{}},"sources":{"s":{"bindingSpec":"x@1","content":{}}},
			"bindings":{"b":{"operation":"a","source":"s","preference":-9007199254740991}}}`,
	}
	for name, document := range documents {
		t.Run(name, func(t *testing.T) {
			var iface Interface
			if err := json.Unmarshal([]byte(document), &iface); err != nil {
				t.Fatalf("decode: %v", err)
			}
			encoded, err := json.Marshal(iface)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if got, want := decodedJSON(t, encoded), decodedJSON(t, []byte(document)); !reflect.DeepEqual(got, want) {
				t.Fatalf("round trip changed the document:\n got %s\nwant %s", encoded, document)
			}
		})
	}
}

// A document the model cannot carry exactly fails decoding instead of being
// altered.
func TestDocumentModel_RefusesWhatItCannotCarry(t *testing.T) {
	documents := map[string]string{
		"null optional string":      `{"openbindings":"0.2.0","description":null,"operations":{}}`,
		"null required string":      `{"openbindings":null,"operations":{}}`,
		"missing required version":  `{"operations":{}}`,
		"null operations":           `{"openbindings":"0.2.0","operations":null}`,
		"null operation":            `{"openbindings":"0.2.0","operations":{"a":null}}`,
		"null operation schema":     `{"openbindings":"0.2.0","operations":{"a":{"input":null}}}`,
		"null tag":                  `{"openbindings":"0.2.0","operations":{"a":{"tags":[null]}}}`,
		"null example":              `{"openbindings":"0.2.0","operations":{"a":{"examples":{"e":null}}}}`,
		"null idempotent":           `{"openbindings":"0.2.0","operations":{"a":{"idempotent":null}}}`,
		"missing bindingSpec":       `{"openbindings":"0.2.0","operations":{},"sources":{"s":{"content":"https://example.com/x"}}}`,
		"missing binding source":    `{"openbindings":"0.2.0","operations":{"a":{}},"bindings":{"b":{"operation":"a"}}}`,
		"fractional preference":     `{"openbindings":"0.2.0","operations":{"a":{}},"bindings":{"b":{"operation":"a","source":"s","preference":9007199254740990.5}}}`,
		"out-of-range preference":   `{"openbindings":"0.2.0","operations":{"a":{}},"bindings":{"b":{"operation":"a","source":"s","preference":9007199254740993}}}`,
		"null dependency spec":      `{"openbindings":"0.2.0","operations":{"a":{}},"dependencies":{"d":{"operation":"a","bindingSpecs":[null]}}}`,
		"missing dependency target": `{"openbindings":"0.2.0","operations":{"a":{}},"dependencies":{"d":{}}}`,
	}
	for name, document := range documents {
		t.Run(name, func(t *testing.T) {
			var iface Interface
			if err := json.Unmarshal([]byte(document), &iface); err == nil {
				encoded, _ := json.Marshal(iface)
				t.Fatalf("decoded a document the model cannot carry; it would re-encode as %s", encoded)
			}
		})
	}
}

func TestDocumentModel_PreferenceIsAnExactInteger(t *testing.T) {
	for spelling, want := range map[string]int64{"1": 1, "1.0": 1, "1e3": 1000, "-0": 0, "9007199254740991": 9007199254740991} {
		var binding BindingEntry
		if err := json.Unmarshal([]byte(`{"operation":"a","source":"s","preference":`+spelling+`}`), &binding); err != nil {
			t.Fatalf("%s: %v", spelling, err)
		}
		if binding.Preference == nil || *binding.Preference != want {
			t.Fatalf("%s decoded as %v, want %d", spelling, binding.Preference, want)
		}
	}
}

// preferenceValue decides the §5.3 range exactly however a number is spelled,
// with work that does not grow with its exponent.
func TestPreferenceValue(t *testing.T) {
	for token, want := range map[string]int64{
		"0": 0, "-0": 0, "0.000e-5": 0, "0e99999999999999999999": 0,
		"1": 1, "1.0": 1, "10e-1": 1, "0.0001e4": 1, "1e3": 1000, "-12.5e1": -125,
		"1." + strings.Repeat("0", 5000):           1,
		"1" + strings.Repeat("0", 5000) + "e-5000": 1,
		"9007199254740991":                         9007199254740991, "-9007199254740991": -9007199254740991,
		"9.007199254740991e15": 9007199254740991,
	} {
		if value, ok := preferenceValue(token); !ok || value != want {
			t.Errorf("%.30s: %d, %v; want %d", token, value, ok, want)
		}
	}
	for _, token := range []string{
		"9007199254740992", "-9007199254740992", "1e16", "1.5", "1e-1", "0.55e1",
		"1e10001", "1e99999999999999999999", "1e-99999999999999999999", "5e-9999999",
	} {
		if value, ok := preferenceValue(token); ok {
			t.Errorf("%s: accepted as %d", token, value)
		}
	}
}

// preferenceValue agrees with math/big wherever math/big's work is small.
func FuzzPreferenceValue(f *testing.F) {
	for _, seed := range []string{"0", "-0", "1.0", "10e-1", "9007199254740991", "9007199254740992", "-12.5e1", "0.0001e4", "1e-1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, token string) {
		if !schemacompiler.IsNumber(json.Number(token)) {
			return
		}
		if i := strings.IndexAny(token, "eE"); i >= 0 {
			if e, err := strconv.Atoi(token[i+1:]); err != nil || e > 400 || e < -400 {
				return
			}
		}
		want, ok := new(big.Rat).SetString(token)
		inRange := ok && want.IsInt() && want.Num().CmpAbs(big.NewInt(maxPreference)) <= 0
		value, got := preferenceValue(token)
		if got != inRange || got && value != want.Num().Int64() {
			t.Fatalf("%s: %d, %v; math/big says %v in range %v", token, value, got, want, inRange)
		}
	})
}

// Programs state presence through the typed fields alone: set a member with
// Present, remove it with nil.
func TestDocumentModel_ConstructAndRemovePresence(t *testing.T) {
	binding := BindingEntry{Operation: "a", Source: "s", Content: json.RawMessage(`null`)}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"content":null`) {
		t.Fatalf("present null binding content must encode, got %s", encoded)
	}
	for _, absent := range []json.RawMessage{nil, {}} {
		binding.Content = absent
		encoded, _ = json.Marshal(binding)
		if strings.Contains(string(encoded), "content") {
			t.Fatalf("nil or empty binding content is absent, got %s", encoded)
		}
		encoded, _ = json.Marshal(Source{BindingSpec: "x@1", Content: absent})
		if strings.Contains(string(encoded), "content") {
			t.Fatalf("nil or empty content is absent, got %s", encoded)
		}
	}

	var iface Interface
	if err := json.Unmarshal([]byte(`{"openbindings":"0.2.0","version":"","operations":{"a":{"tags":[]}}}`), &iface); err != nil {
		t.Fatal(err)
	}
	iface.Version = nil
	operation := iface.Operations["a"]
	operation.Tags = nil
	iface.Operations["a"] = operation
	encoded, _ = json.Marshal(iface)
	if got, want := decodedJSON(t, encoded), decodedJSON(t, []byte(`{"openbindings":"0.2.0","operations":{"a":{}}}`)); !reflect.DeepEqual(got, want) {
		t.Fatalf("removing decoded members: got %s", encoded)
	}
}

// Interface.Validate and ValidateDocument agree about every document the
// model decodes: the host object carries what the bytes carried.
func TestDocumentModel_HostAndByteValidationAgree(t *testing.T) {
	documents := []string{
		`{"openbindings":"0.2.0","version":"","operations":{}}`,
		`{"openbindings":"0.2.0","operations":{},"sources":{"s":{"bindingSpec":"x@1"}}}`,
		`{"openbindings":"0.2.0","operations":{},"sources":{"s":{"bindingSpec":"x@1","content":null,"location":""}}}`,
		`{"openbindings":"0.2.0","operations":{"a":{}},"bindings":{"b":{"operation":"a","source":"s","content":{"$ref":"x.json"}}}}`,
		`{"openbindings":"0.2.0","operations":{"a":{"aliases":[]}}}`,
		`{"openbindings":"0.2.0","operations":{"a":{}},"dependencies":{"d":{"operation":"a","bindingSpecs":[]}}}`,
	}
	for _, document := range documents {
		_, fromBytes, _ := ValidateDocument([]byte(document), ValidateOptions{})
		var iface Interface
		if err := json.Unmarshal([]byte(document), &iface); err != nil {
			t.Fatalf("%s: decode: %v", document, err)
		}
		fromHost, err := iface.Validate(ValidateOptions{})
		var violation *ValidationError
		if err != nil && !errors.As(err, &violation) {
			t.Fatalf("%s: %v", document, err)
		}
		if !reflect.DeepEqual(fromHost.Violated, fromBytes.Violated) {
			t.Fatalf("%s: host object violates %v, bytes violate %v", document, fromHost.Violated, fromBytes.Violated)
		}
	}
}

func TestDocumentModel_BindingContentPresenceIsKept(t *testing.T) {
	var iface Interface
	if err := json.Unmarshal([]byte(`{"openbindings":"0.2.0","operations":{"a":{}},
		"sources":{"s":{"bindingSpec":"x@1"}},
		"bindings":{"absent":{"operation":"a","source":"s"},"empty":{"operation":"a","source":"s","content":""},
			"null":{"operation":"a","source":"s","content":null}}}`), &iface); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"absent": "", "empty": `""`, "null": "null"} {
		content := iface.Bindings[key].Content
		if string(content) != want || (want == "") != (content == nil) {
			t.Fatalf("binding content presence lost for %s: got %q, want %q", key, content, want)
		}
	}
}

// Members are matched by exact name. A case variant of a typed member is an
// unknown member (OBI-T-02) and never changes the typed one.
func TestDocumentModel_MemberNamesAreExact(t *testing.T) {
	var binding BindingEntry
	if err := json.Unmarshal([]byte(`{"operation":"a","source":"s","OPERATION":"b","Content":"x","Preference":1.5}`), &binding); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if binding.Operation != "a" || binding.Content != nil || binding.Preference != nil {
		t.Fatalf("a case variant changed a typed member: %+v", binding)
	}
	if len(binding.Unknown) != 3 {
		t.Fatalf("case variants must be carried as unknown members, got %v", binding.Unknown)
	}
	var iface Interface
	document := `{"openbindings":"0.2.0","OpenBindings":"9.9.9","operations":{}}`
	if err := json.Unmarshal([]byte(document), &iface); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if iface.OpenBindings != "0.2.0" {
		t.Fatalf("declared version = %q", iface.OpenBindings)
	}
	encoded, _ := json.Marshal(iface)
	if got, want := decodedJSON(t, encoded), decodedJSON(t, []byte(document)); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed the document: %s", encoded)
	}
}

// Duplicate member names and quoted preferences are refused, at any depth, by
// direct decoding as by ValidateDocument.
func TestDocumentModel_RefusesDuplicatesAndQuotedPreferences(t *testing.T) {
	for name, document := range map[string]string{
		"duplicate root member":    `{"openbindings":"0.2.0","operations":{},"name":"a","name":"b"}`,
		"duplicate map entry":      `{"openbindings":"0.2.0","operations":{"a":{},"a":{}}}`,
		"duplicate inside schema":  `{"openbindings":"0.2.0","operations":{"a":{"input":{"type":"string","type":"number"}}}}`,
		"quoted preference":        `{"openbindings":"0.2.0","operations":{"a":{}},"bindings":{"b":{"operation":"a","source":"s","preference":"7"}}}`,
		"invalid UTF-8 in a value": "{\"openbindings\":\"0.2.0\",\"name\":\"\xff\",\"operations\":{}}",
	} {
		var iface Interface
		if err := json.Unmarshal([]byte(document), &iface); err == nil {
			t.Errorf("%s: decoded a document the model cannot carry", name)
		}
	}
}

// Decode errors name the first offending member in declaration order, so the
// same input always fails the same way.
func TestDocumentModel_DecodeErrorsAreDeterministic(t *testing.T) {
	document := []byte(`{"description":null,"deprecated":null,"tags":null,"idempotent":null,"examples":null}`)
	var first string
	for range 50 {
		var operation Operation
		err := json.Unmarshal(document, &operation)
		if err == nil {
			t.Fatal("decoded nulls the model cannot carry")
		}
		if first == "" {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("decode error varies: %q then %q", first, err.Error())
		}
	}
}

// A typed field alone states its member: a nil field is absent even when the
// lossless maps carry an entry of the same name.
func TestDocumentModel_TypedFieldsAloneStateTheirMembers(t *testing.T) {
	binding := BindingEntry{Operation: "a", Source: "s", LosslessFields: LosslessFields{
		Unknown:    map[string]json.RawMessage{"content": json.RawMessage(`"x"`), "later": json.RawMessage(`1`)},
		Extensions: map[string]json.RawMessage{"operation": json.RawMessage(`"b"`)},
	}}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := decodedJSON(t, encoded), decodedJSON(t, []byte(`{"operation":"a","source":"s","later":1}`)); !reflect.DeepEqual(got, want) {
		t.Fatalf("encoded %s", encoded)
	}
}

// A host object whose encoding violates the document rules is refused with a
// *ValidationError.
func TestValidate_RefusesAHostObjectWhoseEncodingViolatesTheRules(t *testing.T) {
	iface := &Interface{OpenBindings: "0.2.0", Operations: map[string]Operation{"a": {Input: map[string]any(nil)}}}
	var violation *ValidationError
	if _, err := iface.Validate(ValidateOptions{}); !errors.As(err, &violation) {
		t.Fatalf("want a *ValidationError, got %T %v", err, err)
	}
}

// A nested object type decoded on its own verifies its input as a document
// does.
func TestDocumentModel_NestedTypesVerifyTheirOwnInput(t *testing.T) {
	var operation Operation
	if err := json.Unmarshal([]byte(`{"input":{"type":"string","type":"number"}}`), &operation); err == nil {
		t.Fatal("an operation with a duplicate member name inside its schema decoded")
	}
	if err := operation.UnmarshalJSON([]byte(`{"description":"a"} trailing`)); err == nil {
		t.Fatal("an operation followed by trailing input decoded")
	}
}

// The model keeps no part of the caller's input: raw members are copies.
func TestDocumentModel_RetainsNoPartOfTheInput(t *testing.T) {
	input := []byte(`{"bindingSpec":"x@1","content":{"k":"v"},"later":[1],"x-note":"n"}`)
	var source Source
	if err := json.Unmarshal(input, &source); err != nil {
		t.Fatal(err)
	}
	for i := range input {
		input[i] = ' '
	}
	if string(source.Content) != `{"k":"v"}` || string(source.Unknown["later"]) != `[1]` || string(source.Extensions["x-note"]) != `"n"` {
		t.Fatalf("decoded members alias the input: %s %s %s", source.Content, source.Unknown["later"], source.Extensions["x-note"])
	}
}

// The model encodes only what it would decode back unchanged: a member it
// carries as raw JSON holding what decoding refuses fails encoding, where
// encoding/json would write it out or alter it.
func TestDocumentModel_EncodingRefusesWhatDecodingRefuses(t *testing.T) {
	deep := json.RawMessage(strings.Repeat("[", 10001) + strings.Repeat("]", 10001))
	for name, raw := range map[string]json.RawMessage{
		"an escaped lone surrogate": json.RawMessage(`"\ud800"`),
		"a repeated name":           json.RawMessage(`{"a":1,"a":2}`),
		"invalid UTF-8":             json.RawMessage("\"\xff\""),
		"no JSON value":             json.RawMessage(`{"a":}`),
		"nesting past the decoder":  deep,
	} {
		for position, value := range map[string]any{
			"example input":     OperationExample{Input: raw},
			"source content":    Source{BindingSpec: "x@1", Content: raw},
			"an extension":      Operation{LosslessFields: LosslessFields{Extensions: map[string]json.RawMessage{"x-a": raw}}},
			"an unknown member": BindingEntry{Operation: "a", Source: "s", LosslessFields: LosslessFields{Unknown: map[string]json.RawMessage{"extra": raw}}},
		} {
			if encoded, err := json.Marshal(value); err == nil {
				t.Errorf("%s in %s encoded as %s", name, position, encoded)
			}
		}
	}
	encoded, err := json.Marshal(Operation{LosslessFields: LosslessFields{Extensions: map[string]json.RawMessage{"x-a": nil}}})
	if err != nil || string(encoded) != `{"x-a":null}` {
		t.Fatalf("a nil entry is a present null: %s, %v", encoded, err)
	}
}

// Validation of a host object judges the document it encodes, so an object
// the model cannot encode exactly is not validated: an escaped lone surrogate
// the encoding would have replaced with U+FFFD no longer passes a const of
// U+FFFD.
func TestValidate_HostObjectsEncodeExactly(t *testing.T) {
	iface := Interface{OpenBindings: "0.2.0", Operations: map[string]Operation{"op": {
		Input:    map[string]any{"const": "\ufffd"},
		Examples: map[string]OperationExample{"e": {Input: json.RawMessage(`"\ud800"`)}},
	}}}
	if report, err := iface.Validate(ValidateOptions{}); err == nil || errors.As(err, new(*ValidationError)) || report.Evidence != nil {
		t.Fatalf("want an encoding error and no report, got %v, %+v", err, report)
	}
	if _, err := CompileOperationSchema(&iface, "op", "input"); err == nil || errors.As(err, new(*SchemaGraphUnavailableError)) {
		t.Fatalf("want an encoding error, got %v", err)
	}
}

// Encoding refuses a model it could not write as held: a string or name
// holding invalid UTF-8, which encoding/json would replace with U+FFFD (two
// keys could even become one name); a member name both Extensions and Unknown
// hold; and raw JSON in a schema that decoding would refuse. Validate returns
// the encoding error, never a report on another document.
func TestMarshal_RefusesWhatWouldNotDecodeBackUnchanged(t *testing.T) {
	text := func(s string) *string { return &s }
	for name, iface := range map[string]Interface{
		"invalid UTF-8 in a typed string":    {OpenBindings: "0.2.0", Description: text("caf\xff")},
		"a lone surrogate in a typed string": {OpenBindings: "0.2.0", Name: text("\xed\xa0\x80")},
		"operation keys that would become one name": {OpenBindings: "0.2.0", Operations: map[string]Operation{
			"a\xff": {Input: map[string]any{"type": "integer"}}, "a\xfe": {Input: map[string]any{"type": "string"}}}},
		"schema member names that would become one name": {OpenBindings: "0.2.0", Operations: map[string]Operation{
			"op": {Input: map[string]any{"type\xff": "y", "type\xfe": "x"}}}},
		"invalid UTF-8 in a schema string": {OpenBindings: "0.2.0", Schemas: map[string]JSONSchema{"S": map[string]any{"const": "\xff"}}},
		"an extension name with invalid UTF-8": {OpenBindings: "0.2.0", LosslessFields: LosslessFields{
			Extensions: map[string]json.RawMessage{"x-\xff": json.RawMessage(`1`)}}},
		"a name in both Extensions and Unknown": {OpenBindings: "0.2.0", LosslessFields: LosslessFields{
			Extensions: map[string]json.RawMessage{"x-a": json.RawMessage(`1`)}, Unknown: map[string]json.RawMessage{"x-a": json.RawMessage(`2`)}}},
		"raw JSON in a schema repeating a name": {OpenBindings: "0.2.0", Operations: map[string]Operation{
			"op": {Input: json.RawMessage(`{"type":"string","type":"integer"}`)}}},
		"raw JSON in a schema escaping a lone surrogate": {OpenBindings: "0.2.0", Operations: map[string]Operation{
			"op": {Input: json.RawMessage(`{"const":"\ud800"}`)}}},
	} {
		if data, err := json.Marshal(iface); err == nil {
			t.Errorf("%s: encoded %s", name, data)
		}
		if report, err := iface.Validate(ValidateOptions{}); err == nil || errors.As(err, new(*ValidationError)) || report.Evidence != nil {
			t.Errorf("%s: want the encoding error and no report, got %v", name, err)
		}
	}

	held := Interface{OpenBindings: "0.2.0", Description: text("café �"), Operations: map[string]Operation{
		"op": {Input: json.RawMessage(`{"type":"string","const":"�"}`)}},
		LosslessFields: LosslessFields{Extensions: map[string]json.RawMessage{"x-a": json.RawMessage(`1`)}, Unknown: map[string]json.RawMessage{"other": json.RawMessage(`2`)}}}
	data, err := json.Marshal(held)
	if err != nil {
		t.Fatalf("a model holding valid UTF-8 encodes: %v", err)
	}
	var back Interface
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("and decodes back: %v", err)
	}
	again, _ := json.Marshal(back)
	var before, after any
	_ = json.Unmarshal(data, &before)
	_ = json.Unmarshal(again, &after)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("round trip changed\n%s\ninto\n%s", data, again)
	}
}
