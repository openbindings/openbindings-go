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
// not a schema: OBI-D-13 reports it. Well-formedness of a present value is a
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
// supplies the JSON value null, which OBI-D-10 validates like any other. An
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
	// name resolves to this operation (see ResolveOperation / OBI-T-07).
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

// BindingEntry is an author-declared realization of an operation through a
// source (§5.3).
type BindingEntry struct {
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
// consumption point (§5.5). Kinds, when present, is an unordered any-of list
// of exact, opaque kind strings accepted at that point. A nil slice leaves
// the dependency unconstrained by kind. Operation is
// the canonical key of an operation in the same document.
type DependencyEntry struct {
	Operation string   `json:"operation"`
	Kinds     []string `json:"kinds,omitzero"`

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

// AllowsKind reports whether a source kind meets this dependency's declared
// kind constraint (§5.5). An omitted Kinds list imposes no constraint.
// Comparison is exact and independent of whether a processor supports the
// kind. This only checks the kind constraint; it says nothing about operation
// compatibility, provider selection, or whether a binding can be used.
func (d DependencyEntry) AllowsKind(kind string) bool {
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
