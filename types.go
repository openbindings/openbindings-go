package openbindings

import (
	"bytes"
	"fmt"

	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
)

// JSONSchema holds a JSON Schema 2020-12 value in either of its two forms:
// an object schema (map[string]any) or a boolean schema (bool) — §5.2 admits
// boolean schemas at every schema position (`true` accepts every value,
// `false` accepts none, `{}` is equivalent to `true`). It is intentionally
// untyped beyond that to avoid coupling to any one JSON Schema library.
// This preserves arbitrary keys/values structurally, but not raw JSON bytes.
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
// they differ, as a binding specification's selector semantics do, tests for
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
// supplies the JSON value null, which OBI-D-11 validates like any other.
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

// Source is a binding-specification-governed carrier or address (§5.4).
type Source struct {
	// BindingSpec is the binding-specification identifier governing this
	// source: exact and opaque (§6: never dereferenced, never range-matched).
	BindingSpec string `json:"bindingSpec"`
	// Location is the binding-specification-defined absolute address, nil
	// when the member is absent.
	Location *string `json:"location,omitempty"`
	// Content is the embedded source-artifact representation: any JSON value,
	// carried as raw JSON because member presence is distinct from value
	// (§5.4). Nil means the member is absent; the bytes `null` are a present
	// null. The core carries content opaquely; the governing binding
	// specification determines which values are accepted and what they mean.
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

// Transform is a JSONata expression string in the transform language §5.5
// pins. Tools that evaluate transforms do so under that language contract
// (OBI-T-10).
type Transform = string

// TransformOrRef is a binding's inputTransform or outputTransform (§5.5): an
// InlineTransform expression, or a *TransformReference naming an entry of the
// document's transforms map. A nil TransformOrRef is an absent member. A
// binding holding any other type that satisfies the interface, as one
// embedding InlineTransform would, does not encode.
type TransformOrRef interface {
	// Resolve returns the JSONata expression the transform denotes: an inline
	// expression itself, or the transforms entry a reference names. It
	// reports false when a reference does not resolve.
	Resolve(transforms map[string]Transform) (expression string, ok bool)

	transformOrRef()
}

// InlineTransform is a transform written in place, as a JSONata expression.
type InlineTransform string

// Resolve returns the expression itself.
func (t InlineTransform) Resolve(map[string]Transform) (string, bool) { return string(t), true }

func (InlineTransform) transformOrRef() {}

// TransformReference is the object form of a binding transform,
// {"$ref": "#/transforms/<name>"}. Its members beyond $ref, extensions and
// unknown fields alike, are preserved (§12, OBI-T-02).
type TransformReference struct {
	// Ref is the same-document fragment naming a transforms entry.
	Ref string `json:"$ref"`

	LosslessFields
}

type transformReferenceMembers TransformReference

// Resolve returns the transforms entry Ref names.
func (r *TransformReference) Resolve(transforms map[string]Transform) (string, bool) {
	if r == nil {
		return "", false
	}
	name, problem := transformReferenceName(r.Ref)
	if problem != "" {
		return "", false
	}
	expression, ok := transforms[name]
	return expression, ok
}

func (*TransformReference) transformOrRef() {}

func (r *TransformReference) UnmarshalJSON(b []byte) error {
	return decodeExact(b, "transform reference", r)
}

func (r *TransformReference) decodeVerified(b []byte) error {
	return decodeObject(b, "transform reference", (*transformReferenceMembers)(r))
}

func (r TransformReference) MarshalJSON() ([]byte, error) {
	return encodeObject(transformReferenceMembers(r), r.LosslessFields)
}

// decodeTransform decodes a binding transform member: a string is an inline
// expression, an object a reference.
func decodeTransform(raw json.RawMessage) (TransformOrRef, error) {
	trimmed := bytes.TrimSpace(raw)
	switch {
	case len(trimmed) > 0 && trimmed[0] == '"':
		var expression string
		if err := json.Unmarshal(trimmed, &expression); err != nil {
			return nil, err
		}
		return InlineTransform(expression), nil
	case len(trimmed) > 0 && trimmed[0] == '{':
		reference := &TransformReference{}
		if err := reference.decodeVerified(trimmed); err != nil {
			return nil, err
		}
		return reference, nil
	default:
		return nil, fmt.Errorf("a transform is a JSONata expression string or a $ref object")
	}
}

// BindingEntry is an author-declared realization of an operation through a
// target in a source (§5.3).
type BindingEntry struct {
	Operation string `json:"operation"`
	Source    string `json:"source"`
	// Selector identifies the target within the source. Nil when the member is
	// absent, which the governing binding specification gives its own
	// meaning, distinct from any present value, the empty string included.
	Selector *string `json:"selector,omitempty"`
	// Preference is the author's signed integer preference among bindings of
	// the same operation, nil when absent (no preference, not zero).
	Preference  *int64  `json:"preference,omitempty"`
	Description *string `json:"description,omitempty"`
	Deprecated  *bool   `json:"deprecated,omitempty"`

	// InputTransform maps each caller-facing input value toward the source's
	// expected input representation (§5.5).
	InputTransform TransformOrRef `json:"inputTransform,omitempty"`
	// OutputTransform maps each source output value toward the operation's
	// output contract (§5.5).
	OutputTransform TransformOrRef `json:"outputTransform,omitempty"`

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
	for _, member := range []struct {
		name      string
		transform TransformOrRef
	}{{"inputTransform", be.InputTransform}, {"outputTransform", be.OutputTransform}} {
		switch transform := member.transform.(type) {
		case nil, InlineTransform:
		case *TransformReference:
			if transform == nil {
				return nil, fmt.Errorf("binding: %s holds a nil *TransformReference, which is neither transform form", member.name)
			}
		default:
			// A type embedding InlineTransform satisfies the interface but is
			// neither form (§5.5).
			return nil, fmt.Errorf("binding: %s holds a %T, which is neither transform form", member.name, transform)
		}
	}
	return encodeObject(bindingEntryMembers(be), be.LosslessFields)
}

// DependencyEntry names an operation contract consumed at a local
// consumption point (§5.6). BindingSpecs, when present, is an unordered any-of list
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

	// Transforms contains named transforms that can be referenced by bindings.
	Transforms map[string]Transform `json:"transforms,omitzero"`

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
