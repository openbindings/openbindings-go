package openbindings

import (
	"encoding/json"
	"strings"
)

// JSONSchema is intentionally untyped to avoid coupling to any one JSON Schema library.
// This preserves arbitrary keys/values structurally, but not raw JSON bytes (use canonicaljson.Marshal if you need stable bytes).
type JSONSchema map[string]any

// OperationKind represents the type of operation in an OpenBindings interface.
type OperationKind string

const (
	// OperationKindMethod represents a request-response operation (RPC-style).
	OperationKindMethod OperationKind = "method"
	// OperationKindEvent represents a one-way notification or event emission.
	OperationKindEvent OperationKind = "event"
)

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
	knownSatisfiesSet = knownSet(
		"interface", "operation",
	)
	knownOperationSet = knownSet(
		"kind", "description", "deprecated", "tags", "aliases", "satisfies",
		"idempotent", "input", "output", "payload", "examples",
	)
	knownOperationExampleSet = knownSet(
		"description", "input", "output", "payload",
	)
	knownSourceSet = knownSet(
		"format", "location", "content", "description",
	)
	knownBindingEntrySet = knownSet(
		"operation", "source", "ref", "priority", "description", "deprecated",
		"inputTransform", "outputTransform",
	)
	knownTransformSet = knownSet(
		"type", "expression",
	)
	knownInterfaceSet = knownSet(
		"openbindings", "name", "version", "description",
		"contact", "license",
		"schemas", "operations", "imports",
		"sources", "bindings", "transforms",
	)
)

type Satisfies struct {
	Interface string `json:"interface"`
	Operation string `json:"operation"`
	LosslessFields
}

type satisfiesWire struct {
	Interface string `json:"interface"`
	Operation string `json:"operation"`
}

func (s *Satisfies) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	var w satisfiesWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}

	*s = Satisfies{
		Interface: w.Interface,
		Operation: w.Operation,
	}

	s.Extensions, s.Unknown = splitLossless(raw, knownSatisfiesSet)
	return nil
}

func (s Satisfies) MarshalJSON() ([]byte, error) {
	w := satisfiesWire{
		Interface: s.Interface,
		Operation: s.Operation,
	}
	return marshalLossless(s.Unknown, s.Extensions, w)
}

// OperationExample represents an example input/output pair for an operation.
type OperationExample struct {
	Description string `json:"description,omitempty"`
	Input       any    `json:"input,omitempty"`  // method only
	Output      any    `json:"output,omitempty"` // method only
	Payload     any    `json:"payload,omitempty"` // event only

	LosslessFields
}

type operationExampleWire struct {
	Description string `json:"description,omitempty"`
	Input       any    `json:"input,omitempty"`
	Output      any    `json:"output,omitempty"`
	Payload     any    `json:"payload,omitempty"`
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
		Payload:     w.Payload,
	}

	e.Extensions, e.Unknown = splitLossless(raw, knownOperationExampleSet)
	return nil
}

func (e OperationExample) MarshalJSON() ([]byte, error) {
	w := operationExampleWire{
		Description: e.Description,
		Input:       e.Input,
		Output:      e.Output,
		Payload:     e.Payload,
	}
	return marshalLossless(e.Unknown, e.Extensions, w)
}

type Operation struct {
	Kind        OperationKind `json:"kind"`
	Description string        `json:"description,omitempty"`
	Deprecated  bool          `json:"deprecated,omitempty"`
	Tags        []string      `json:"tags,omitempty"`
	Aliases     []string      `json:"aliases,omitempty"`
	Satisfies   []Satisfies   `json:"satisfies,omitempty"`

	// method-only
	Idempotent *bool      `json:"idempotent,omitempty"`
	Input      JSONSchema `json:"input,omitempty"`
	Output     JSONSchema `json:"output,omitempty"`

	// event-only
	// Note: for kind="event", payload is optional per the spec.
	Payload JSONSchema `json:"payload,omitempty"`

	// Examples contains named example input/output pairs.
	Examples map[string]OperationExample `json:"examples,omitempty"`

	LosslessFields
}

type operationWire struct {
	Kind        OperationKind `json:"kind"`
	Description string        `json:"description,omitempty"`
	Deprecated  bool          `json:"deprecated,omitempty"`
	Tags        []string      `json:"tags,omitempty"`
	Aliases     []string      `json:"aliases,omitempty"`
	Satisfies   []Satisfies   `json:"satisfies,omitempty"`

	Idempotent *bool      `json:"idempotent,omitempty"`
	Input      JSONSchema `json:"input,omitempty"`
	Output     JSONSchema `json:"output,omitempty"`

	Payload JSONSchema `json:"payload,omitempty"`

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
		Kind:        w.Kind,
		Description: w.Description,
		Deprecated:  w.Deprecated,
		Tags:        w.Tags,
		Aliases:     w.Aliases,
		Satisfies:   w.Satisfies,
		Idempotent:  w.Idempotent,
		Input:       w.Input,
		Output:      w.Output,
		Payload:     w.Payload,
		Examples:    w.Examples,
	}

	o.Extensions, o.Unknown = splitLossless(raw, knownOperationSet)
	return nil
}

func (o Operation) MarshalJSON() ([]byte, error) {
	w := operationWire{
		Kind:        o.Kind,
		Description: o.Description,
		Deprecated:  o.Deprecated,
		Tags:        o.Tags,
		Aliases:     o.Aliases,
		Satisfies:   o.Satisfies,
		Idempotent:  o.Idempotent,
		Input:       o.Input,
		Output:      o.Output,
		Payload:     o.Payload,
		Examples:    o.Examples,
	}
	return marshalLossless(o.Unknown, o.Extensions, w)
}

type Source struct {
	Format      string `json:"format"`
	Location    string `json:"location,omitempty"`
	Content     any    `json:"content,omitempty"`
	Description string `json:"description,omitempty"`

	LosslessFields
}

type sourceWire struct {
	Format      string `json:"format"`
	Location    string `json:"location,omitempty"`
	Content     any    `json:"content,omitempty"`
	Description string `json:"description,omitempty"`
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
		Format:      w.Format,
		Location:    w.Location,
		Content:     w.Content,
		Description: w.Description,
	}

	s.Extensions, s.Unknown = splitLossless(raw, knownSourceSet)
	return nil
}

func (s Source) MarshalJSON() ([]byte, error) {
	w := sourceWire{
		Format:      s.Format,
		Location:    s.Location,
		Content:     s.Content,
		Description: s.Description,
	}
	return marshalLossless(s.Unknown, s.Extensions, w)
}

// Transform represents a JSON-to-JSON transformation.
// For v0.1, Type MUST be "jsonata".
type Transform struct {
	Type       string `json:"type"`
	Expression string `json:"expression"`

	LosslessFields
}

type transformWire struct {
	Type       string `json:"type"`
	Expression string `json:"expression"`
}

func (t *Transform) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	var w transformWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}

	*t = Transform{
		Type:       w.Type,
		Expression: w.Expression,
	}

	t.Extensions, t.Unknown = splitLossless(raw, knownTransformSet)
	return nil
}

func (t Transform) MarshalJSON() ([]byte, error) {
	w := transformWire{
		Type:       t.Type,
		Expression: t.Expression,
	}
	return marshalLossless(t.Unknown, t.Extensions, w)
}

// TransformOrRef represents either an inline Transform or a $ref to a named transform.
// Check IsRef() to determine which form is present.
//
// If both Ref and Transform are populated (which should not occur in well-formed
// documents), Ref takes precedence during marshaling.
type TransformOrRef struct {
	// Ref is the JSON Pointer reference (e.g., "#/transforms/myTransform").
	// If non-empty, this is a reference, not an inline transform.
	Ref string

	// Transform is the inline transform definition.
	// Only valid when Ref is empty.
	Transform *Transform

	// RefExtensions preserves x-* fields co-located with $ref on reference objects.
	// Only populated when IsRef() is true.
	RefExtensions map[string]json.RawMessage
}

// IsRef returns true if this is a reference to a named transform.
func (t TransformOrRef) IsRef() bool {
	return t.Ref != ""
}

// Resolve returns the Transform, resolving $ref if necessary using the provided transforms map.
// Returns nil if the reference cannot be resolved.
// For inline transforms, returns the Transform directly.
func (t TransformOrRef) Resolve(transforms map[string]Transform) *Transform {
	if !t.IsRef() {
		return t.Transform
	}

	// Parse the $ref - expected format: #/transforms/<name>
	const prefix = "#/transforms/"
	if !strings.HasPrefix(t.Ref, prefix) {
		return nil
	}
	name := strings.TrimPrefix(t.Ref, prefix)
	if name == "" {
		return nil
	}
	if tr, ok := transforms[name]; ok {
		return &tr
	}
	return nil
}

func (t *TransformOrRef) UnmarshalJSON(b []byte) error {
	// First, try to detect if this is a $ref
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}

	if refRaw, ok := raw["$ref"]; ok {
		var ref string
		if err := json.Unmarshal(refRaw, &ref); err != nil {
			return err
		}
		tor := TransformOrRef{Ref: ref}
		// Preserve x-* fields co-located with $ref.
		for k, v := range raw {
			if k == "$ref" {
				continue
			}
			if strings.HasPrefix(k, "x-") {
				if tor.RefExtensions == nil {
					tor.RefExtensions = map[string]json.RawMessage{}
				}
				tor.RefExtensions[k] = v
			}
		}
		*t = tor
		return nil
	}

	// Otherwise, parse as Transform
	var tr Transform
	if err := json.Unmarshal(b, &tr); err != nil {
		return err
	}
	*t = TransformOrRef{Transform: &tr}
	return nil
}

func (t TransformOrRef) MarshalJSON() ([]byte, error) {
	if t.Ref != "" {
		out := map[string]json.RawMessage{}
		for k, v := range t.RefExtensions {
			out[k] = v
		}
		refBytes, err := json.Marshal(t.Ref)
		if err != nil {
			return nil, err
		}
		out["$ref"] = refBytes
		return json.Marshal(out)
	}
	if t.Transform != nil {
		return json.Marshal(t.Transform)
	}
	return []byte("null"), nil
}

type BindingEntry struct {
	Operation   string   `json:"operation"`
	Source      string   `json:"source"`
	Ref         string   `json:"ref,omitempty"`
	Priority    *float64 `json:"priority,omitempty"`
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
	Priority    *float64 `json:"priority,omitempty"`
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
		Priority:        w.Priority,
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
		Priority:        be.Priority,
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

	Contact map[string]any `json:"contact,omitempty"`
	License map[string]any `json:"license,omitempty"`

	Schemas    map[string]JSONSchema `json:"schemas,omitempty"`
	Operations map[string]Operation  `json:"operations"`

	// Imports is an optional import table mapping local aliases to URLs/paths
	// of other OpenBindings interfaces. Used by satisfies references.
	Imports map[string]string `json:"imports,omitempty"`

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

	Contact map[string]any `json:"contact,omitempty"`
	License map[string]any `json:"license,omitempty"`

	Schemas    map[string]JSONSchema `json:"schemas,omitempty"`
	Operations map[string]Operation  `json:"operations"`

	Imports map[string]string `json:"imports,omitempty"`

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
		Contact:      w.Contact,
		License:      w.License,
		Schemas:      w.Schemas,
		Operations:   w.Operations,
		Imports:      w.Imports,
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
		Contact:      i.Contact,
		License:      i.License,
		Schemas:      i.Schemas,
		Operations:   i.Operations,
		Imports:      i.Imports,
		Sources:      i.Sources,
		Bindings:     i.Bindings,
		Transforms:   i.Transforms,
	}
	return marshalLossless(i.Unknown, i.Extensions, w)
}
