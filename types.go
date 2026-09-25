package openbindings

import (
	"encoding/json"
	"fmt"
)

// JSONSchema holds a JSON Schema 2020-12 value in either of its two forms:
// an object schema (map[string]any) or a boolean schema (bool) — §5.2 admits
// boolean schemas at every schema position (`true` accepts every value,
// `false` accepts none, `{}` is equivalent to `true`). It is intentionally
// untyped beyond that to avoid coupling to any one JSON Schema library.
// This preserves arbitrary keys/values structurally, but not raw JSON bytes.
// A decoded schema holds generic JSON values, with every number a
// json.Number, so a number keeps its exact text.
//
// As an operation's Input or Output, a nil JSONSchema means the schema is
// unspecified (the member is absent). As an entry of Interface.Schemas, where
// the entry itself says the member is present, nil is a JSON null, which is
// not a schema: OBI-D-17 reports it. Well-formedness of a present value is a
// document rule enforced by Validate rather than by this type.
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
// caller-facing values (§5.1). Input and Output are the example values as
// JSON: nil when the member is absent, and the bytes `null` when the example
// supplies the JSON value null, which OBI-D-11 validates like any other. An
// empty, non-nil json.RawMessage holds no value and encodes as absent.
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
// Output are nil when the document specifies no contract at that boundary;
// any schema value, including `{}` and the boolean schemas, is present.
type Operation struct {
	Description *string  `json:"description,omitempty"`
	Deprecated  *bool    `json:"deprecated,omitempty"`
	Tags        []string `json:"tags,omitzero"`
	// Aliases are additional names for this operation, equal in standing to its
	// key. The key plus aliases form one flat, document-unique namespace; every
	// name resolves to this operation (see ResolveOperation / OBI-T-12).
	Aliases []string `json:"aliases,omitzero"`

	Idempotent *bool      `json:"idempotent,omitempty"`
	Input      JSONSchema `json:"input,omitempty"`
	Output     JSONSchema `json:"output,omitempty"`

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

// Source is a binding specification's identifier and the content that
// specification defines (§5.4).
type Source struct {
	// BindingSpec is the binding-specification identifier governing this
	// source: exact and opaque (§6: never dereferenced, never range-matched).
	BindingSpec string `json:"bindingSpec"`
	// Content is what the source carries for its binding specification: any
	// JSON value, carried as raw JSON because member presence is distinct from
	// value (§5.4). Nil means the member is absent; the bytes `null` are a
	// present null. An empty, non-nil json.RawMessage holds no value and
	// encodes as absent. The core gives content no meaning; the governing
	// binding specification defines whether it may be absent, which values it
	// accepts, and what they mean.
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

// BindingEntry is an author-declared realization of an operation through a
// source (§5.3).
type BindingEntry struct {
	Operation string `json:"operation"`
	Source    string `json:"source"`
	// Content is what the binding carries for its source's binding
	// specification: any JSON value, typically which target realizes the
	// operation and how values are adapted to it (§5.3). It is carried as raw
	// JSON because member presence is distinct from value. Nil means the
	// member is absent, which the governing binding specification gives its
	// own meaning; the bytes `null` are a present null. An empty, non-nil
	// json.RawMessage holds no value and encodes as absent. The core gives
	// binding content no meaning; the source's binding specification defines
	// which values it accepts and what they mean.
	Content json.RawMessage `json:"content,omitempty"`
	// Preference is the author's signed integer preference among bindings of
	// the same operation, nil when absent (no preference, not zero).
	Preference  *int64  `json:"preference,omitempty"`
	Description *string `json:"description,omitempty"`
	Deprecated  *bool   `json:"deprecated,omitempty"`

	LosslessFields
}

type bindingEntryMembers BindingEntry

// maxPreference bounds a binding preference: the exactly representable
// interoperable integer range of §5.3.
const maxPreference = 9007199254740991

func (be *BindingEntry) UnmarshalJSON(b []byte) error { return decodeExact(b, "binding", be) }

func (be *BindingEntry) decodeVerified(b []byte) error {
	return decodeObject(b, "binding", (*bindingEntryMembers)(be))
}

func (be BindingEntry) MarshalJSON() ([]byte, error) {
	return encodeObject(bindingEntryMembers(be), be.LosslessFields)
}

// DependencyEntry names an operation contract consumed at a local
// consumption point (§5.5). BindingSpecs, when present, is an unordered any-of list
// of exact binding-specification identifiers accepted at that point. A nil
// slice leaves the dependency unconstrained by binding family. Operation is
// the canonical key of an operation in the same document.
type DependencyEntry struct {
	Operation    string   `json:"operation"`
	BindingSpecs []string `json:"bindingSpecs,omitzero"`

	LosslessFields
}

type dependencyEntryMembers DependencyEntry

func (d *DependencyEntry) UnmarshalJSON(b []byte) error { return decodeExact(b, "dependency", d) }

func (d *DependencyEntry) decodeVerified(b []byte) error {
	return decodeObject(b, "dependency", (*dependencyEntryMembers)(d))
}

func (d DependencyEntry) MarshalJSON() ([]byte, error) {
	return encodeObject(dependencyEntryMembers(d), d.LosslessFields)
}

// Interface is the OpenBindings document shape (§5). OpenBindings is the
// declared specification version. Every other member is absent exactly when
// its Go value is nil. That includes Operations, which §5 requires: the model
// carries a document that omits it, so Validate can report the omission
// (OBI-D-02) and re-encoding leaves it omitted.
type Interface struct {
	OpenBindings string  `json:"openbindings"`
	Name         *string `json:"name,omitempty"`
	Version      *string `json:"version,omitempty"`
	Description  *string `json:"description,omitempty"`

	Schemas    map[string]JSONSchema `json:"schemas,omitzero"`
	Operations map[string]Operation  `json:"operations,omitzero"`
	// Dependencies contains named consumption points. A dependency declaration
	// does not assert that a realization is installed, selected, or live.
	Dependencies map[string]DependencyEntry `json:"dependencies,omitzero"`

	Sources  map[string]Source       `json:"sources,omitzero"`
	Bindings map[string]BindingEntry `json:"bindings,omitzero"`

	LosslessFields
}

type interfaceMembers Interface

func (i *Interface) UnmarshalJSON(b []byte) error { return decodeExact(b, "document", i) }

func (i *Interface) decodeVerified(b []byte) error {
	return decodeObject(b, "document", (*interfaceMembers)(i))
}

func (i Interface) MarshalJSON() ([]byte, error) {
	return encodeObject(interfaceMembers(i), i.LosslessFields)
}
