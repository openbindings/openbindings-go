package openbindings

import (
	"encoding/json"
	"fmt"
	"strings"
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

// LosslessFields is embedded in every typed OpenBindings struct to preserve
// JSON fields that the SDK does not (yet) model. Extensions holds keys starting
// with "x-"; Unknown holds all other unrecognised keys. During marshaling,
// typed fields always win over colliding Unknown/Extension entries.
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

// Pre-computed known field sets for efficient lossless JSON unmarshaling.
// These are computed once at package init to avoid repeated allocations.
var (
	knownOperationSet = knownSet(
		"description", "deprecated", "tags", "aliases",
		"idempotent", "input", "output", "examples",
	)
	knownOperationExampleSet = knownSet(
		"description", "input", "output",
	)
	knownSourceSet = knownSet(
		"bindingSpec", "location", "content", "description",
	)
	knownBindingEntrySet = knownSet(
		"operation", "source", "ref", "preference", "description", "deprecated",
		"inputTransform", "outputTransform",
	)
	knownInterfaceSet = knownSet(
		"openbindings", "name", "version", "description",
		"schemas", "operations",
		"sources", "bindings", "transforms",
	)
)

// OperationExample represents an example input/output pair for an operation.
//
// JSON null is a meaningful example value, distinct from an absent field
// (OBI-D-11 validates an explicit null against the operation's schema; an
// absent field is not validated). Because Go's `any` cannot distinguish the
// two, InputPresent/OutputPresent record field presence: UnmarshalJSON
// populates them, and MarshalJSON re-emits an explicit null for a present
// field holding nil. When constructing examples in Go code, a non-nil
// Input/Output already implies presence; set the booleans only to express
// an explicit JSON null.
type OperationExample struct {
	Description string `json:"description,omitempty"`
	Input       any    `json:"input,omitempty"`
	Output      any    `json:"output,omitempty"`

	// InputPresent reports whether the "input" field was present in the JSON,
	// distinguishing an explicit null from an absent field.
	InputPresent bool `json:"-"`
	// OutputPresent reports whether the "output" field was present in the JSON,
	// distinguishing an explicit null from an absent field.
	OutputPresent bool `json:"-"`

	LosslessFields
}

// HasInput reports whether the example provides an input value (including an
// explicit JSON null).
func (e OperationExample) HasInput() bool { return e.InputPresent || e.Input != nil }

// HasOutput reports whether the example provides an output value (including an
// explicit JSON null).
func (e OperationExample) HasOutput() bool { return e.OutputPresent || e.Output != nil }

type operationExampleWire struct {
	Description string `json:"description,omitempty"`
	Input       any    `json:"input,omitempty"`
	Output      any    `json:"output,omitempty"`
}

func (e *OperationExample) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	var w operationExampleWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}

	*e = OperationExample{
		Description: w.Description,
		Input:       w.Input,
		Output:      w.Output,
	}
	_, e.InputPresent = raw["input"]
	_, e.OutputPresent = raw["output"]

	e.Extensions, e.Unknown = splitLossless(raw, knownOperationExampleSet)
	return nil
}

func (e OperationExample) MarshalJSON() ([]byte, error) {
	w := operationExampleWire{
		Description: e.Description,
		Input:       e.Input,
		Output:      e.Output,
	}
	// omitempty drops nil Input/Output; re-emit explicit nulls for fields
	// recorded as present so explicit-null examples round-trip.
	var overrides map[string]json.RawMessage
	if e.InputPresent && e.Input == nil {
		overrides = map[string]json.RawMessage{"input": json.RawMessage("null")}
	}
	if e.OutputPresent && e.Output == nil {
		if overrides == nil {
			overrides = map[string]json.RawMessage{}
		}
		overrides["output"] = json.RawMessage("null")
	}
	return marshalLosslessWith(e.Unknown, e.Extensions, w, overrides)
}

type Operation struct {
	Description string   `json:"description,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	// Aliases are additional names for this operation, equal in standing to its
	// key. The key plus aliases form one flat, document-unique namespace; every
	// name resolves to this operation (see ResolveOperation / OBI-T-12).
	Aliases []string `json:"aliases,omitempty"`

	Idempotent *bool      `json:"idempotent,omitempty"`
	Input      JSONSchema `json:"input,omitempty"`
	Output     JSONSchema `json:"output,omitempty"`

	// Examples contains named example input/output pairs.
	Examples map[string]OperationExample `json:"examples,omitempty"`

	LosslessFields
}

type operationWire struct {
	Description string   `json:"description,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`

	Idempotent *bool      `json:"idempotent,omitempty"`
	Input      JSONSchema `json:"input,omitempty"`
	Output     JSONSchema `json:"output,omitempty"`

	Examples map[string]OperationExample `json:"examples,omitempty"`
}

func (o *Operation) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	var w operationWire
	if err := json.Unmarshal(b, &w); err != nil {
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

	o.Extensions, o.Unknown = splitLossless(raw, knownOperationSet)
	return nil
}

func (o Operation) MarshalJSON() ([]byte, error) {
	w := operationWire{
		Description: o.Description,
		Deprecated:  o.Deprecated,
		Tags:        o.Tags,
		Aliases:     o.Aliases,
		Idempotent:  o.Idempotent,
		Input:       o.Input,
		Output:      o.Output,
		Examples:    o.Examples,
	}
	// The spec distinguishes an empty {} schema (accepts any value) and a
	// boolean schema from an absent one (contract unspecified). JSONSchema
	// is interface-typed, so omitempty drops only nil (absent) values —
	// {}, true, and false all round-trip without special handling.
	return marshalLosslessWith(o.Unknown, o.Extensions, w, nil)
}

type Source struct {
	// BindingSpec is the binding-specification identifier governing this
	// source — exact and opaque (core §6: never dereferenced, never
	// range-matched).
	BindingSpec string `json:"bindingSpec"`
	Location    string `json:"location,omitempty"`
	// Content is the embedded source material: ANY JSON value, carried as
	// raw JSON because member PRESENCE is distinct from value (core §7) —
	// nil means the member is absent, a `null` literal is a PRESENT null.
	// The core carries content opaquely; the governing binding-spec family
	// interprets it per its own pins (see ContentToBytes).
	Content     json.RawMessage `json:"content,omitempty"`
	Description string          `json:"description,omitempty"`

	LosslessFields
}

// ContentPresent reports whether the content member is present at all —
// including a present `null` (distinct from an absent member, core §7).
func (s Source) ContentPresent() bool {
	return s.Content != nil
}

type sourceWire struct {
	BindingSpec string          `json:"bindingSpec"`
	Location    string          `json:"location,omitempty"`
	Content     json.RawMessage `json:"content,omitempty"`
	Description string          `json:"description,omitempty"`
}

func (s *Source) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	var w sourceWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}

	*s = Source{
		BindingSpec: w.BindingSpec,
		Location:    w.Location,
		Content:     w.Content,
		Description: w.Description,
	}

	s.Extensions, s.Unknown = splitLossless(raw, knownSourceSet)
	return nil
}

func (s Source) MarshalJSON() ([]byte, error) {
	w := sourceWire{
		BindingSpec: s.BindingSpec,
		Location:    s.Location,
		Content:     s.Content,
		Description: s.Description,
	}
	return marshalLossless(s.Unknown, s.Extensions, w)
}

// Transform is a JSONata 2.0 expression string per OpenBindings v0.2 spec §6.5.
// Tools that evaluate transforms MUST do so according to the JSONata 2.0
// specification (OBI-T-10).
type Transform = string

// TransformOrRef represents either an inline JSONata transform expression or
// a $ref to a named transform in the document's `transforms` map.
//
// Per the v0.2 spec §6.5, the inline form is a JSONata expression string;
// the reference form is an object {"$ref": "#/transforms/<name>"} with no
// additional properties.
type TransformOrRef struct {
	// Inline is the JSONata expression string when this is an inline transform.
	// Empty when IsRef() returns true.
	Inline string

	// Ref is the JSON Pointer reference (e.g., "#/transforms/myTransform")
	// when this is a reference. Empty for inline transforms.
	Ref string
}

// IsRef returns true if this is a reference to a named transform.
func (t TransformOrRef) IsRef() bool {
	return t.Ref != ""
}

// Resolve returns the JSONata expression string this transform refers to.
// For inline transforms, returns the inline expression directly.
// For references, looks up the named transform in the provided map.
// Returns ("", false) if the reference cannot be resolved.
func (t TransformOrRef) Resolve(transforms map[string]string) (string, bool) {
	if !t.IsRef() {
		return t.Inline, true
	}
	const prefix = "#/transforms/"
	if !strings.HasPrefix(t.Ref, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(t.Ref, prefix)
	if name == "" {
		return "", false
	}
	expr, ok := transforms[name]
	return expr, ok
}

func (t *TransformOrRef) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*t = TransformOrRef{Inline: s}
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("transform: must be a JSONata expression string or a $ref object: %w", err)
	}
	refRaw, ok := raw["$ref"]
	if !ok {
		return fmt.Errorf("transform: object form requires a $ref field")
	}
	var ref string
	if err := json.Unmarshal(refRaw, &ref); err != nil {
		return fmt.Errorf("transform.$ref: %w", err)
	}
	*t = TransformOrRef{Ref: ref}
	return nil
}

func (t TransformOrRef) MarshalJSON() ([]byte, error) {
	if !t.IsRef() {
		return json.Marshal(t.Inline)
	}
	return json.Marshal(map[string]string{"$ref": t.Ref})
}

type BindingEntry struct {
	Operation   string   `json:"operation"`
	Source      string   `json:"source"`
	Ref         string   `json:"ref,omitempty"`
	Preference  *float64 `json:"preference,omitempty"`
	Description string   `json:"description,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`

	// InputTransform transforms operation input to binding input structure.
	InputTransform *TransformOrRef `json:"inputTransform,omitempty"`
	// OutputTransform transforms binding output to operation output structure.
	OutputTransform *TransformOrRef `json:"outputTransform,omitempty"`

	LosslessFields
}

type bindingEntryWire struct {
	Operation   string   `json:"operation"`
	Source      string   `json:"source"`
	Ref         string   `json:"ref,omitempty"`
	Preference  *float64 `json:"preference,omitempty"`
	Description string   `json:"description,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`

	InputTransform  *TransformOrRef `json:"inputTransform,omitempty"`
	OutputTransform *TransformOrRef `json:"outputTransform,omitempty"`
}

func (be *BindingEntry) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	var w bindingEntryWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}

	*be = BindingEntry{
		Operation:       w.Operation,
		Source:          w.Source,
		Ref:             w.Ref,
		Preference:      w.Preference,
		Description:     w.Description,
		Deprecated:      w.Deprecated,
		InputTransform:  w.InputTransform,
		OutputTransform: w.OutputTransform,
	}

	be.Extensions, be.Unknown = splitLossless(raw, knownBindingEntrySet)
	return nil
}

func (be BindingEntry) MarshalJSON() ([]byte, error) {
	w := bindingEntryWire{
		Operation:       be.Operation,
		Source:          be.Source,
		Ref:             be.Ref,
		Preference:      be.Preference,
		Description:     be.Description,
		Deprecated:      be.Deprecated,
		InputTransform:  be.InputTransform,
		OutputTransform: be.OutputTransform,
	}
	return marshalLossless(be.Unknown, be.Extensions, w)
}

// Interface is the OpenBindings document shape.
type Interface struct {
	OpenBindings string `json:"openbindings"`
	Name         string `json:"name,omitempty"`
	Version      string `json:"version,omitempty"`
	Description  string `json:"description,omitempty"`

	Schemas    map[string]JSONSchema `json:"schemas,omitempty"`
	Operations map[string]Operation  `json:"operations"`

	Sources  map[string]Source       `json:"sources,omitempty"`
	Bindings map[string]BindingEntry `json:"bindings,omitempty"`

	// Transforms contains named transforms that can be referenced by bindings.
	Transforms map[string]Transform `json:"transforms,omitempty"`

	LosslessFields
}

type interfaceWire struct {
	OpenBindings string `json:"openbindings"`
	Name         string `json:"name,omitempty"`
	Version      string `json:"version,omitempty"`
	Description  string `json:"description,omitempty"`

	Schemas    map[string]JSONSchema `json:"schemas,omitempty"`
	Operations map[string]Operation  `json:"operations"`

	Sources  map[string]Source       `json:"sources,omitempty"`
	Bindings map[string]BindingEntry `json:"bindings,omitempty"`

	Transforms map[string]Transform `json:"transforms,omitempty"`
}

func (i *Interface) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	var w interfaceWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}

	*i = Interface{
		OpenBindings: w.OpenBindings,
		Name:         w.Name,
		Version:      w.Version,
		Description:  w.Description,
		Schemas:      w.Schemas,
		Operations:   w.Operations,
		Sources:      w.Sources,
		Bindings:     w.Bindings,
		Transforms:   w.Transforms,
	}

	i.Extensions, i.Unknown = splitLossless(raw, knownInterfaceSet)
	return nil
}

func (i Interface) MarshalJSON() ([]byte, error) {
	w := interfaceWire{
		OpenBindings: i.OpenBindings,
		Name:         i.Name,
		Version:      i.Version,
		Description:  i.Description,
		Schemas:      i.Schemas,
		Operations:   i.Operations,
		Sources:      i.Sources,
		Bindings:     i.Bindings,
		Transforms:   i.Transforms,
	}
	return marshalLossless(i.Unknown, i.Extensions, w)
}
