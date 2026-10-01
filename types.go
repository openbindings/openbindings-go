package openbindings

import (
	"encoding/json"
	"fmt"
)

// JSONSchema holds a JSON Schema 2020-12 schema in either of its two forms
// (§5.2): an object schema or a boolean schema (`true` accepts every value,
// `false` accepts none, and `{}` is equivalent to `true`). It is untyped so
// that the model couples to no JSON Schema library, and the model writes it
// as encoding/json writes the value it holds.
//
// Decoding gives a map[string]any or a bool, holding generic JSON values:
// every number a json.Number, so it keeps its exact text, and every array an
// []any. A caller setting one writes any of these forms:
//   - a map[string]any or a bool, whose values encoding/json writes as the
//     schema means them. A number held as a float64 is written as Go writes
//     a float64, so one beyond the integers a float64 holds exactly (2^53) is
//     not the number intended; a json.Number keeps its text.
//   - a json.RawMessage holding the schema's JSON text, which is written as
//     given, compacted, with every number exact. It must hold one JSON value
//     that decoding accepts, or encoding fails.
//   - any other value encoding/json writes as a JSON object or boolean, such
//     as a struct. A defined type is written by its own encoding, so a named
//     byte slice without JSON methods is written as a base64 string, which is
//     no schema.
//
// A caller decoding schema text itself keeps its numbers exact by holding the
// text as a json.RawMessage, or by decoding with a json.Decoder set to
// UseNumber: json.Unmarshal into an any reads every number as a float64.
//
// Absence is a nil interface. As an operation's Input or Output, a nil
// JSONSchema means the member is absent, which states no value contract in
// that direction (§5.1). A typed nil held there, such as a nil
// json.RawMessage or a nil map[string]any, is not absent: it is written as a
// present null, which is no schema, and OBI-D-02 and OBI-D-10 report it. As an
// entry of Document.Schemas, where the entry itself says the member is
// present, nil is a JSON null, which OBI-D-10 reports. Whether a present value
// is a well-formed schema is a document rule Validate decides, not this type.
type JSONSchema any

// jsonTypeName names the JSON type of a decoded value (null, boolean,
// number, string, array, object) for diagnostics.
func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// Present returns a pointer to v, for setting an optional member. The model
// represents every optional member so that it is absent exactly when its Go
// value is nil: Present("") is a present empty string, distinct from absence.
func Present[T any](v T) *T { return &v }

// Value returns the value of an optional member, or the zero value when the
// member is absent. It suits a reader for whom absence and the zero value mean
// the same thing, such as a description shown to a person; a reader for whom
// they differ, as a binding specification's content semantics do, tests for
// nil instead.
func Value[T any](member *T) T {
	if member == nil {
		var zero T
		return zero
	}
	return *member
}

// OperationExample is a named, author-supplied sample of an operation's
// caller-facing values (§5.1). Each is an author claim that the value
// validates against the operation's corresponding schema; no document rule
// checks it (OBI-T-10, OBI-T-11). Input and Output are the example values as
// JSON: nil when the member is absent, and the bytes `null` when the example
// supplies the JSON value null, a value like any other. An empty, non-nil
// json.RawMessage holds no value and encodes as absent.
type OperationExample struct {
	Description *string         `json:"description,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	Output      json.RawMessage `json:"output,omitempty"`

	LosslessFields
}

type operationExampleMembers OperationExample

func (e *OperationExample) UnmarshalJSON(b []byte) error { return decodeExact(b, "example", e) }

func (e *OperationExample) decodeVerified(b []byte) error {
	return decodeObject(b, "example", (*operationExampleMembers)(e))
}

func (e OperationExample) MarshalJSON() ([]byte, error) {
	return encodeObject(operationExampleMembers(e), e.LosslessFields)
}

// Operation is a protocol-independent capability contract (§5.1). Input and
// Output state its input and output contracts; each is nil when the document
// states no value contract in that direction, and any schema value,
// including `{}` and the boolean schemas, is present.
type Operation struct {
	Description *string  `json:"description,omitempty"`
	Deprecated  *bool    `json:"deprecated,omitempty"`
	Tags        []string `json:"tags,omitzero"`
	// Aliases are additional names for this operation, equal in standing to its
	// key. The key plus aliases form one flat, document-unique namespace; every
	// name resolves to this operation (see ResolveOperation / OBI-T-07).
	Aliases []string `json:"aliases,omitzero"`

	Input  JSONSchema `json:"input,omitempty"`
	Output JSONSchema `json:"output,omitempty"`

	// Examples contains named example input/output pairs.
	Examples map[string]OperationExample `json:"examples,omitzero"`

	LosslessFields
}

type operationMembers Operation

func (o *Operation) UnmarshalJSON(b []byte) error { return decodeExact(b, "operation", o) }

func (o *Operation) decodeVerified(b []byte) error {
	return decodeObject(b, "operation", (*operationMembers)(o))
}

func (o Operation) MarshalJSON() ([]byte, error) {
	return encodeObject(operationMembers(o), o.LosslessFields)
}

// Source carries a kind and optional content read under that kind (§5.4).
type Source struct {
	// Kind is an exact, opaque, non-empty token used to select an
	// interpretation of this source and its bindings (§6). Core never
	// dereferences it or infers relationships between distinct strings.
	Kind string `json:"kind"`
	// Content is any JSON value carried under the source's kind, kept as raw
	// JSON because member presence is distinct from
	// value (§5.4). Nil means the member is absent; the bytes `null` are a
	// present null. An empty, non-nil json.RawMessage holds no value and
	// encodes as absent. Core gives content no meaning.
	Content     json.RawMessage `json:"content,omitempty"`
	Description *string         `json:"description,omitempty"`

	LosslessFields
}

type sourceMembers Source

func (s *Source) UnmarshalJSON(b []byte) error { return decodeExact(b, "source", s) }

func (s *Source) decodeVerified(b []byte) error {
	return decodeObject(b, "source", (*sourceMembers)(s))
}

func (s Source) MarshalJSON() ([]byte, error) {
	return encodeObject(sourceMembers(s), s.LosslessFields)
}

// Binding is an author-declared realization of an operation through a
// source (§5.3).
type Binding struct {
	Operation string `json:"operation"`
	Source    string `json:"source"`
	// Content is what the binding carries under its source's kind: any JSON
	// value, possibly identifying a target or describing value adaptation
	// (§5.3). It is carried as raw JSON because member presence is distinct
	// from value. Nil means the
	// member is absent; the bytes `null` are a present null. An empty, non-nil
	// json.RawMessage holds no value and encodes as absent. Core gives
	// binding content no meaning.
	Content json.RawMessage `json:"content,omitempty"`
	// Idempotent is the binding author's claim about repeating the operation
	// through this binding (§5.3): true claims repetition with the same input,
	// in context differing at most in ways the effects do not depend on, adds
	// no intended operation-level effects after the first; false claims some
	// valid repetition can; nil claims neither. No document rule checks it
	// (OBI-T-10), and it alone never makes a retry safe.
	Idempotent *bool `json:"idempotent,omitempty"`
	// Preference is the author's signed integer preference among bindings of
	// the same operation, nil when absent (no preference, not zero).
	Preference  *int64  `json:"preference,omitempty"`
	Description *string `json:"description,omitempty"`
	Deprecated  *bool   `json:"deprecated,omitempty"`

	LosslessFields
}

type bindingMembers Binding

// maxPreference bounds a binding preference: the exactly representable
// interoperable integer range of §5.3.
const maxPreference = 9007199254740991

func (b *Binding) UnmarshalJSON(data []byte) error { return decodeExact(data, "binding", b) }

func (b *Binding) decodeVerified(data []byte) error {
	return decodeObject(data, "binding", (*bindingMembers)(b))
}

func (b Binding) MarshalJSON() ([]byte, error) {
	return encodeObject(bindingMembers(b), b.LosslessFields)
}

// Dependency names an operation contract consumed at a local consumption
// point (§5.5). Kinds, when present, is an unordered any-of list of exact,
// opaque kind strings accepted at that point. A nil slice leaves the
// dependency unconstrained by kind. Operation is the canonical key of an
// operation in the same document.
type Dependency struct {
	Operation   string   `json:"operation"`
	Kinds       []string `json:"kinds,omitzero"`
	Description *string  `json:"description,omitempty"`

	LosslessFields
}

type dependencyMembers Dependency

func (d *Dependency) UnmarshalJSON(b []byte) error { return decodeExact(b, "dependency", d) }

func (d *Dependency) decodeVerified(b []byte) error {
	return decodeObject(b, "dependency", (*dependencyMembers)(d))
}

func (d Dependency) MarshalJSON() ([]byte, error) {
	return encodeObject(dependencyMembers(d), d.LosslessFields)
}

// AcceptsKind reports whether kind is acceptable at this consumption point:
// whether a binding whose source has that kind meets the dependency's any-of
// kind constraint (§5.5). A nil Kinds list declares no constraint and accepts
// every kind; a present empty list, which OBI-D-02 forbids, accepts none.
// Comparison is exact string equality, independent of whether a processor
// supports the kind (OBI-T-01). It checks the kind constraint alone: it says
// nothing about operation compatibility, provider selection, or whether a
// binding can be used.
func (d Dependency) AcceptsKind(kind string) bool {
	if d.Kinds == nil {
		return true
	}
	for _, acceptable := range d.Kinds {
		if kind == acceptable {
			return true
		}
	}
	return false
}

// Document is an OBI, an OpenBindings interface document (§3, §5), as the
// values its members hold: it models the document's meaning, not the text it
// was decoded from. Re-encoding a decoded Document writes every member it
// holds, but not the text's whitespace, member order, or string escapes, nor
// the spelling of a number the model types (a binding's preference, an
// int64); numbers in schemas, example values, content, and kept members keep
// their spelling. Encoding writes each object's typed members in field
// order, then the members LosslessFields keeps in name order, and each map's
// entries in key order; it escapes HTML only as the calling encoder does. A
// claim about a text is a claim about its bytes: ValidateDocument judges
// those.
//
// OpenBindings is the declared specification version. Every other member is
// absent exactly when its Go value is nil. That includes Operations, which §5
// requires: the model carries a document that omits it, so Validate can report
// the omission (OBI-D-02) and re-encoding leaves it omitted.
type Document struct {
	OpenBindings string  `json:"openbindings"`
	Name         *string `json:"name,omitempty"`
	Version      *string `json:"version,omitempty"`
	Description  *string `json:"description,omitempty"`

	Schemas    map[string]JSONSchema `json:"schemas,omitzero"`
	Operations map[string]Operation  `json:"operations,omitzero"`
	// Dependencies contains named consumption points. A dependency declaration
	// does not assert that a realization is installed, selected, or live.
	Dependencies map[string]Dependency `json:"dependencies,omitzero"`

	Sources  map[string]Source  `json:"sources,omitzero"`
	Bindings map[string]Binding `json:"bindings,omitzero"`

	LosslessFields
}

type documentMembers Document

func (d *Document) UnmarshalJSON(data []byte) error { return decodeExact(data, "document", d) }

func (d *Document) decodeVerified(data []byte) error {
	return decodeObject(data, "document", (*documentMembers)(d))
}

func (d Document) MarshalJSON() ([]byte, error) {
	return encodeObject(documentMembers(d), d.LosslessFields)
}
