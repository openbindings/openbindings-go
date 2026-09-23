package openbindings

import (
	"bytes"
	"fmt"
	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"
	"math/big"
	"strconv"
	"strings"

	"github.com/openbindings/openbindings-go/jsonvalue"
)

// JSONSchema holds a JSON Schema 2020-12 value in either of its two forms:
// an object schema (map[string]any) or a boolean schema (bool) — §5.2 admits
// boolean schemas at every schema position (`true` accepts every value,
// `false` accepts none, `{}` is equivalent to `true`). It is intentionally
// untyped beyond that to avoid coupling to any one JSON Schema library.
// This preserves arbitrary keys/values structurally, but not raw JSON bytes
// (use canonicaljson.Marshal if you need stable bytes).
//
// A nil JSONSchema means the schema is unspecified (the field is absent);
// well-formedness of a present value is a document rule (OBI-D-17), enforced
// by Validate rather than by this type.
type JSONSchema any

// SchemaObjectForm returns the object form of a schema value: an object
// schema as itself, boolean `true` as `{}`, and boolean `false` as
// `{"not": {}}` (the equivalent object spellings per JSON Schema 2020-12).
// ok is false when v is neither an object nor a boolean — a malformed
// schema value (an OBI-D-17 violation, reported by Validate).
func SchemaObjectForm(v JSONSchema) (m map[string]any, ok bool) {
	switch s := v.(type) {
	case map[string]any:
		return s, true
	case bool:
		if s {
			return map[string]any{}, true
		}
		return map[string]any{"not": map[string]any{}}, true
	default:
		return nil, false
	}
}

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

// NonZero sets an optional member from a value whose zero value means "no
// value", as when a producer copies an upstream field that may be empty: it
// returns nil (absent) for the zero value and a present member otherwise.
func NonZero[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}

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

// LosslessFields is embedded in every OBI-defined object to preserve members
// the SDK does not model. Extensions holds keys starting with "x-"; Unknown
// holds all other unrecognised keys. During marshaling, typed fields always win
// over colliding Unknown/Extension entries.
//
// Together with the typed fields they make the model exact: re-encoding a
// decoded document reproduces every member. An optional member is absent
// exactly when its Go value is nil: optional strings and booleans are
// pointers, optional collections distinguish nil (absent) from empty
// (present), and a JSON null is carried only where null is itself a value
// (example values and source content, as json.RawMessage). A document the
// model cannot carry exactly fails decoding instead of being altered: a JSON
// null at any other known position, a missing required string member, or a
// preference that is not an integer in range. ValidateDocument still judges
// such a document from its bytes.
//
// Each lossless type requires a parallel wire struct for encoding — when adding
// fields to a typed struct, update both the public type and its wire counterpart.
type LosslessFields struct {
	// Extensions preserves `x-*` fields at the object level.
	// It is populated by UnmarshalJSON and included by MarshalJSON.
	Extensions map[string]json.RawMessage `json:"-"`

	// Unknown preserves other unknown fields (forward-compat).
	// It is populated by UnmarshalJSON and included by MarshalJSON.
	Unknown map[string]json.RawMessage `json:"-"`
}

var (
	operationShape = objectShape{
		name: "operation",
		known: knownSet(
			"description", "deprecated", "tags", "aliases",
			"idempotent", "input", "output", "examples",
		),
		stringLists: []string{"tags", "aliases"},
	}
	operationExampleShape = objectShape{
		name:     "example",
		known:    knownSet("description", "input", "output"),
		nullable: map[string]bool{"input": true, "output": true},
	}
	sourceShape = objectShape{
		name:     "source",
		known:    knownSet("bindingSpec", "location", "content", "description"),
		required: []string{"bindingSpec"},
		nullable: map[string]bool{"content": true},
	}
	bindingEntryShape = objectShape{
		name: "binding",
		known: knownSet(
			"operation", "source", "selector", "preference", "description", "deprecated",
			"inputTransform", "outputTransform",
		),
		required: []string{"operation", "source"},
	}
	dependencyEntryShape = objectShape{
		name:        "dependency",
		known:       knownSet("operation", "bindingSpecs"),
		required:    []string{"operation"},
		stringLists: []string{"bindingSpecs"},
	}
	transformReferenceShape = objectShape{
		name:     "transform reference",
		known:    knownSet("$ref"),
		required: []string{"$ref"},
	}
	interfaceShape = objectShape{
		name: "document",
		known: knownSet(
			"openbindings", "name", "version", "description",
			"schemas", "operations", "dependencies",
			"sources", "bindings", "transforms",
		),
		required:    []string{"openbindings"},
		stringLists: []string{"transforms"},
	}
)

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

type operationExampleWire struct {
	Description *string         `json:"description,omitempty"`
	Input       json.RawMessage `json:"input,omitempty"`
	Output      json.RawMessage `json:"output,omitempty"`
}

func (e *OperationExample) UnmarshalJSON(b []byte) error {
	raw, err := decodeObject(b, operationExampleShape)
	if err != nil {
		return err
	}
	var w operationExampleWire
	if err := jsonvalue.Unmarshal(b, &w); err != nil {
		return err
	}
	*e = OperationExample{Description: w.Description, Input: raw["input"], Output: raw["output"]}
	e.Extensions, e.Unknown = splitLossless(raw, operationExampleShape.known)
	return nil
}

func (e OperationExample) MarshalJSON() ([]byte, error) {
	return marshalLossless(e.Unknown, e.Extensions, operationExampleWire{
		Description: e.Description,
		Input:       e.Input,
		Output:      e.Output,
	})
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

type operationWire struct {
	Description *string  `json:"description,omitempty"`
	Deprecated  *bool    `json:"deprecated,omitempty"`
	Tags        []string `json:"tags,omitzero"`
	Aliases     []string `json:"aliases,omitzero"`

	Idempotent *bool      `json:"idempotent,omitempty"`
	Input      JSONSchema `json:"input,omitempty"`
	Output     JSONSchema `json:"output,omitempty"`

	Examples map[string]OperationExample `json:"examples,omitzero"`
}

func (o *Operation) UnmarshalJSON(b []byte) error {
	raw, err := decodeObject(b, operationShape)
	if err != nil {
		return err
	}
	var w operationWire
	if err := jsonvalue.Unmarshal(b, &w); err != nil {
		return err
	}
	*o = Operation{
		Description: w.Description,
		Deprecated:  w.Deprecated,
		Tags:        w.Tags,
		Aliases:     w.Aliases,
		Idempotent:  w.Idempotent,
		Input:       w.Input,
		Output:      w.Output,
		Examples:    w.Examples,
	}
	o.Extensions, o.Unknown = splitLossless(raw, operationShape.known)
	return nil
}

func (o Operation) MarshalJSON() ([]byte, error) {
	return marshalLossless(o.Unknown, o.Extensions, operationWire{
		Description: o.Description,
		Deprecated:  o.Deprecated,
		Tags:        o.Tags,
		Aliases:     o.Aliases,
		Idempotent:  o.Idempotent,
		Input:       o.Input,
		Output:      o.Output,
		Examples:    o.Examples,
	})
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

type sourceWire struct {
	BindingSpec string          `json:"bindingSpec"`
	Location    *string         `json:"location,omitempty"`
	Content     json.RawMessage `json:"content,omitempty"`
	Description *string         `json:"description,omitempty"`
}

func (s *Source) UnmarshalJSON(b []byte) error {
	raw, err := decodeObject(b, sourceShape)
	if err != nil {
		return err
	}
	var w sourceWire
	if err := jsonvalue.Unmarshal(b, &w); err != nil {
		return err
	}
	*s = Source{BindingSpec: w.BindingSpec, Location: w.Location, Content: raw["content"], Description: w.Description}
	s.Extensions, s.Unknown = splitLossless(raw, sourceShape.known)
	return nil
}

func (s Source) MarshalJSON() ([]byte, error) {
	return marshalLossless(s.Unknown, s.Extensions, sourceWire{
		BindingSpec: s.BindingSpec,
		Location:    s.Location,
		Content:     s.Content,
		Description: s.Description,
	})
}

// Transform is a JSONata expression string in the transform language §5.5
// pins. Tools that evaluate transforms do so under that language contract
// (OBI-T-10).
type Transform = string

// TransformOrRef is a binding's inputTransform or outputTransform: either an
// inline JSONata expression or a reference object naming an entry of the
// document's transforms map (§5.5). Exactly one form is active: a non-nil
// Reference is the object form, and Inline is then empty.
type TransformOrRef struct {
	// Inline is the JSONata expression of an inline transform.
	Inline string

	// Reference is the object form, {"$ref": "#/transforms/<name>"}.
	Reference *TransformReference
}

// TransformReference is the object form of a binding transform. Its members
// beyond $ref, extensions and unknown fields alike, are preserved (§12,
// OBI-T-02).
type TransformReference struct {
	// Ref is the same-document fragment naming a transforms entry.
	Ref string `json:"$ref"`

	LosslessFields
}

type transformReferenceWire struct {
	Ref string `json:"$ref"`
}

func (r *TransformReference) UnmarshalJSON(b []byte) error {
	raw, err := decodeObject(b, transformReferenceShape)
	if err != nil {
		return err
	}
	var w transformReferenceWire
	if err := jsonvalue.Unmarshal(b, &w); err != nil {
		return err
	}
	*r = TransformReference{Ref: w.Ref}
	r.Extensions, r.Unknown = splitLossless(raw, transformReferenceShape.known)
	return nil
}

func (r TransformReference) MarshalJSON() ([]byte, error) {
	return marshalLossless(r.Unknown, r.Extensions, transformReferenceWire{Ref: r.Ref})
}

// IsRef reports whether this is the reference object form.
func (t TransformOrRef) IsRef() bool {
	return t.Reference != nil
}

// Resolve returns the JSONata expression this transform denotes: the inline
// expression, or the transforms entry its reference names. It returns
// ("", false) when a reference does not resolve.
func (t TransformOrRef) Resolve(transforms map[string]string) (string, bool) {
	if t.Reference == nil {
		return t.Inline, true
	}
	const prefix = "#/transforms/"
	if !strings.HasPrefix(t.Reference.Ref, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(t.Reference.Ref, prefix)
	if name == "" {
		return "", false
	}
	expr, ok := transforms[name]
	return expr, ok
}

func (t *TransformOrRef) UnmarshalJSON(b []byte) error {
	trimmed := bytes.TrimSpace(b)
	switch {
	case len(trimmed) > 0 && trimmed[0] == '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		*t = TransformOrRef{Inline: s}
		return nil
	case len(trimmed) > 0 && trimmed[0] == '{':
		var reference TransformReference
		if err := reference.UnmarshalJSON(trimmed); err != nil {
			return err
		}
		*t = TransformOrRef{Reference: &reference}
		return nil
	default:
		return fmt.Errorf("transform: must be a JSONata expression string or a $ref object")
	}
}

func (t TransformOrRef) MarshalJSON() ([]byte, error) {
	if t.Reference == nil {
		return json.Marshal(t.Inline)
	}
	if t.Inline != "" {
		return nil, fmt.Errorf("transform: both an inline expression and a reference are set")
	}
	return t.Reference.MarshalJSON()
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
	InputTransform *TransformOrRef `json:"inputTransform,omitempty"`
	// OutputTransform maps each source output value toward the operation's
	// output contract (§5.5).
	OutputTransform *TransformOrRef `json:"outputTransform,omitempty"`

	LosslessFields
}

type bindingEntryWire struct {
	Operation   string       `json:"operation"`
	Source      string       `json:"source"`
	Selector    *string      `json:"selector,omitempty"`
	Preference  *json.Number `json:"preference,omitempty"`
	Description *string      `json:"description,omitempty"`
	Deprecated  *bool        `json:"deprecated,omitempty"`

	InputTransform  *TransformOrRef `json:"inputTransform,omitempty"`
	OutputTransform *TransformOrRef `json:"outputTransform,omitempty"`
}

// maxPreference bounds a binding preference: the exactly representable
// interoperable integer range of §5.3.
const maxPreference = 9007199254740991

func (be *BindingEntry) UnmarshalJSON(b []byte) error {
	raw, err := decodeObject(b, bindingEntryShape)
	if err != nil {
		return err
	}
	var w bindingEntryWire
	if err := jsonvalue.Unmarshal(b, &w); err != nil {
		return err
	}
	var preference *int64
	if w.Preference != nil {
		value, err := exactPreference(*w.Preference)
		if err != nil {
			return err
		}
		preference = &value
	}
	*be = BindingEntry{
		Operation:       w.Operation,
		Source:          w.Source,
		Selector:        w.Selector,
		Preference:      preference,
		Description:     w.Description,
		Deprecated:      w.Deprecated,
		InputTransform:  w.InputTransform,
		OutputTransform: w.OutputTransform,
	}
	be.Extensions, be.Unknown = splitLossless(raw, bindingEntryShape.known)
	return nil
}

// exactPreference converts a preference number exactly: an integer value in
// the §5.3 range, in any JSON spelling (1, 1.0, 1e3). Any other number is one
// the model cannot carry exactly, so decoding fails.
func exactPreference(n json.Number) (int64, error) {
	value, ok := new(big.Rat).SetString(n.String())
	if !ok || !value.IsInt() || value.Num().CmpAbs(big.NewInt(maxPreference)) > 0 {
		return 0, fmt.Errorf("binding: preference %s is not an integer from -%d through %d", n, maxPreference, maxPreference)
	}
	return value.Num().Int64(), nil
}

func (be BindingEntry) MarshalJSON() ([]byte, error) {
	w := bindingEntryWire{
		Operation:       be.Operation,
		Source:          be.Source,
		Selector:        be.Selector,
		Description:     be.Description,
		Deprecated:      be.Deprecated,
		InputTransform:  be.InputTransform,
		OutputTransform: be.OutputTransform,
	}
	if be.Preference != nil {
		preference := json.Number(strconv.FormatInt(*be.Preference, 10))
		w.Preference = &preference
	}
	return marshalLossless(be.Unknown, be.Extensions, w)
}

// DependencyEntry names an operation contract consumed at a local
// composition point. BindingSpecs, when present, is an unordered any-of list
// of exact binding-specification identifiers accepted at that point. A nil
// slice leaves the dependency unconstrained by binding family. Operation is
// the canonical key of an operation in the same document.
type DependencyEntry struct {
	Operation    string   `json:"operation"`
	BindingSpecs []string `json:"bindingSpecs,omitzero"`

	LosslessFields
}

type dependencyEntryWire struct {
	Operation    string   `json:"operation"`
	BindingSpecs []string `json:"bindingSpecs,omitzero"`
}

func (d *DependencyEntry) UnmarshalJSON(b []byte) error {
	raw, err := decodeObject(b, dependencyEntryShape)
	if err != nil {
		return err
	}
	var w dependencyEntryWire
	if err := jsonvalue.Unmarshal(b, &w); err != nil {
		return err
	}
	*d = DependencyEntry{Operation: w.Operation, BindingSpecs: w.BindingSpecs}
	d.Extensions, d.Unknown = splitLossless(raw, dependencyEntryShape.known)
	return nil
}

func (d DependencyEntry) MarshalJSON() ([]byte, error) {
	return marshalLossless(d.Unknown, d.Extensions, dependencyEntryWire{Operation: d.Operation, BindingSpecs: d.BindingSpecs})
}

// Interface is the OpenBindings document shape (§5). OpenBindings is the
// required specification version; every other member is optional and absent
// exactly when nil, Operations included, so a document that omits it
// re-encodes without it.
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

type interfaceWire struct {
	OpenBindings string  `json:"openbindings"`
	Name         *string `json:"name,omitempty"`
	Version      *string `json:"version,omitempty"`
	Description  *string `json:"description,omitempty"`

	Schemas      map[string]JSONSchema      `json:"schemas,omitzero"`
	Operations   map[string]Operation       `json:"operations,omitzero"`
	Dependencies map[string]DependencyEntry `json:"dependencies,omitzero"`

	Sources  map[string]Source       `json:"sources,omitzero"`
	Bindings map[string]BindingEntry `json:"bindings,omitzero"`

	Transforms map[string]Transform `json:"transforms,omitzero"`
}

func (i *Interface) UnmarshalJSON(b []byte) error {
	raw, err := decodeObject(b, interfaceShape)
	if err != nil {
		return err
	}
	var w interfaceWire
	if err := jsonvalue.Unmarshal(b, &w); err != nil {
		return err
	}
	*i = Interface{
		OpenBindings: w.OpenBindings,
		Name:         w.Name,
		Version:      w.Version,
		Description:  w.Description,
		Schemas:      w.Schemas,
		Operations:   w.Operations,
		Dependencies: w.Dependencies,
		Sources:      w.Sources,
		Bindings:     w.Bindings,
		Transforms:   w.Transforms,
	}
	i.Extensions, i.Unknown = splitLossless(raw, interfaceShape.known)
	return nil
}

func (i Interface) MarshalJSON() ([]byte, error) {
	return marshalLossless(i.Unknown, i.Extensions, interfaceWire{
		OpenBindings: i.OpenBindings,
		Name:         i.Name,
		Version:      i.Version,
		Description:  i.Description,
		Schemas:      i.Schemas,
		Operations:   i.Operations,
		Dependencies: i.Dependencies,
		Sources:      i.Sources,
		Bindings:     i.Bindings,
		Transforms:   i.Transforms,
	})
}
