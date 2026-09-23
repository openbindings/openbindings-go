package openbindings

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
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
			"schemas":{},"dependencies":{},"transforms":{},
			"operations":{"a":{"description":"","deprecated":false,"tags":[],"aliases":[],"idempotent":false,"examples":{}}},
			"sources":{"s":{"bindingSpec":"x@1","location":"","description":""}},
			"bindings":{"b":{"operation":"a","source":"s","selector":"","description":"","deprecated":false,"preference":0}}}`,
		"absent optional members": `{"openbindings":"0.2.0","operations":{"a":{}},"sources":{"s":{"bindingSpec":"x@1","content":{}}},
			"bindings":{"b":{"operation":"a","source":"s"}}}`,
		"operations absent": `{"openbindings":"0.2.0"}`,
		"operations empty":  `{"openbindings":"0.2.0","operations":{}}`,
		"null where null is a value": `{"openbindings":"0.2.0","operations":{"a":{"examples":{"e":{"input":null,"output":null}}}},
			"sources":{"s":{"bindingSpec":"x@1","content":null}}}`,
		"schemas of every form": `{"openbindings":"0.2.0","schemas":{"t":true,"f":false,"e":{},"n":null},
			"operations":{"a":{"input":{},"output":false}}}`,
		"transform object members": `{"openbindings":"0.2.0","operations":{"a":{}},"sources":{"s":{"bindingSpec":"x@1","content":{}}},
			"transforms":{"t":"$"},
			"bindings":{"b":{"operation":"a","source":"s","inputTransform":{"$ref":"","x-note":"kept","later":[1]},"outputTransform":""}}}`,
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
		"null transform entry":      `{"openbindings":"0.2.0","operations":{},"transforms":{"t":null}}`,
		"missing bindingSpec":       `{"openbindings":"0.2.0","operations":{},"sources":{"s":{"location":"https://example.com/x"}}}`,
		"null location":             `{"openbindings":"0.2.0","operations":{},"sources":{"s":{"bindingSpec":"x@1","location":null}}}`,
		"missing binding source":    `{"openbindings":"0.2.0","operations":{"a":{}},"bindings":{"b":{"operation":"a"}}}`,
		"null selector":             `{"openbindings":"0.2.0","operations":{"a":{}},"bindings":{"b":{"operation":"a","source":"s","selector":null}}}`,
		"null input transform":      `{"openbindings":"0.2.0","operations":{"a":{}},"bindings":{"b":{"operation":"a","source":"s","inputTransform":null}}}`,
		"null $ref":                 `{"openbindings":"0.2.0","operations":{"a":{}},"bindings":{"b":{"operation":"a","source":"s","inputTransform":{"$ref":null}}}}`,
		"$ref object without $ref":  `{"openbindings":"0.2.0","operations":{"a":{}},"bindings":{"b":{"operation":"a","source":"s","inputTransform":{"x-note":1}}}}`,
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

// Programs state presence through the typed fields alone: set a member with
// Present, remove it with nil.
func TestDocumentModel_ConstructAndRemovePresence(t *testing.T) {
	binding := BindingEntry{Operation: "a", Source: "s", Selector: Present("")}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"selector":""`) {
		t.Fatalf("a present empty selector must encode, got %s", encoded)
	}
	binding.Selector = nil
	encoded, _ = json.Marshal(binding)
	if strings.Contains(string(encoded), "selector") {
		t.Fatalf("a nil selector is absent, got %s", encoded)
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
		`{"openbindings":"0.2.0","operations":{},"sources":{"s":{"bindingSpec":"x@1","location":"","content":{}}}}`,
		`{"openbindings":"0.2.0","operations":{"a":{"aliases":[]}}}`,
		`{"openbindings":"0.2.0","operations":{"a":{}},"dependencies":{"d":{"operation":"a","bindingSpecs":[]}}}`,
	}
	for _, document := range documents {
		_, fromBytes, _ := ValidateDocument([]byte(document))
		var iface Interface
		if err := json.Unmarshal([]byte(document), &iface); err != nil {
			t.Fatalf("%s: decode: %v", document, err)
		}
		fromHost, err := iface.Validate()
		var violation *ValidationError
		if err != nil && !errors.As(err, &violation) {
			t.Fatalf("%s: %v", document, err)
		}
		if !reflect.DeepEqual(fromHost.Violated, fromBytes.Violated) {
			t.Fatalf("%s: host object violates %v, bytes violate %v", document, fromHost.Violated, fromBytes.Violated)
		}
	}
}

func TestPreparedBinding_SelectorPresenceIsKeptAndCopied(t *testing.T) {
	var iface Interface
	if err := json.Unmarshal([]byte(`{"openbindings":"0.2.0","operations":{"a":{}},
		"sources":{"s":{"bindingSpec":"x@1","content":{}}},
		"bindings":{"absent":{"operation":"a","source":"s"},"empty":{"operation":"a","source":"s","selector":""}}}`), &iface); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareInterface(&iface)
	if err != nil {
		t.Fatal(err)
	}
	absent, _ := prepared.Binding("absent")
	empty, _ := prepared.Binding("empty")
	if absent.Selector != nil || empty.Selector == nil || *empty.Selector != "" {
		t.Fatalf("selector presence lost: absent %v, empty %v", absent.Selector, empty.Selector)
	}
	*empty.Selector = "mutated"
	again, _ := prepared.Binding("empty")
	if *again.Selector != "" {
		t.Fatal("a returned descriptor must not alias the prepared snapshot")
	}
}

// Members are matched by exact name. A case variant of a typed member is an
// unknown member (OBI-T-02) and never changes the typed one.
func TestDocumentModel_MemberNamesAreExact(t *testing.T) {
	var binding BindingEntry
	if err := json.Unmarshal([]byte(`{"operation":"a","source":"s","OPERATION":"b","Selector":"x","Preference":1.5}`), &binding); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if binding.Operation != "a" || binding.Selector != nil || binding.Preference != nil {
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
		Unknown:    map[string]json.RawMessage{"selector": json.RawMessage(`"x"`), "later": json.RawMessage(`1`)},
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

func TestPreparedBindingDescriptor_Equal(t *testing.T) {
	a := PreparedBindingDescriptor{Key: "b", OperationKey: "a", SourceKey: "s", BindingSpec: "x@1", Selector: Present("")}
	b := a
	b.Selector = Present("")
	if !a.Equal(b) {
		t.Fatal("equal descriptors with distinct selector pointers must be Equal")
	}
	b.Selector = nil
	if a.Equal(b) {
		t.Fatal("an absent selector differs from a present empty one")
	}
}

// A host object whose encoding violates the document rules is refused with a
// *ValidationError, as Validate reports it.
func TestPrepareInterface_RefusesWhatValidateReports(t *testing.T) {
	iface := &Interface{OpenBindings: "0.2.0", Operations: map[string]Operation{"a": {Input: map[string]any(nil)}}}
	_, err := PrepareInterface(iface)
	var violation *ValidationError
	if !errors.As(err, &violation) {
		t.Fatalf("want a *ValidationError, got %T %v", err, err)
	}
	if _, verr := iface.Validate(); !errors.As(verr, &violation) {
		t.Fatalf("Validate must report the same document: %v", verr)
	}
}
