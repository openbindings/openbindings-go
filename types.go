package openbindings

import (
	"encoding/json"
	"fmt"
	"strings"
)

// JSONSchema is intentionally untyped to avoid coupling to any one JSON Schema library.
// This preserves arbitrary keys/values structurally, but not raw JSON bytes (use canonicaljson.Marshal if you need stable bytes).
type JSONSchema map[string]any

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
		"role", "operation",
	)
	knownOperationSet = knownSet(
		"description", "deprecated", "tags", "aliases", "satisfies",
		"idempotent", "input", "output", "examples",
	)
	knownOperationExampleSet = knownSet(
		"description", "input", "output",
	)
	knownSourceSet = knownSet(
		"format", "location", "content", "description", "priority",
	)
	knownBindingEntrySet = knownSet(
		"operation", "source", "ref", "priority", "description", "deprecated",
		"security", "inputTransform", "outputTransform",
	)
	knownInterfaceSet = knownSet(
		"openbindings", "name", "version", "description",
		"schemas", "operations", "roles",
		"sources", "bindings", "security", "transforms",
	)
)

type Satisfies struct {
	Role      string `json:"role"`
	Operation string `json:"operation"`
	LosslessFields
}

type satisfiesWire struct {
	Role      string `json:"role"`
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
		Role:      w.Role,
		Operation: w.Operation,
	}

	s.Extensions, s.Unknown = splitLossless(raw, knownSatisfiesSet)
	return nil
}

func (s Satisfies) MarshalJSON() ([]byte, error) {
	w := satisfiesWire{
		Role:      s.Role,
		Operation: s.Operation,
	}
	return marshalLossless(s.Unknown, s.Extensions, w)
}

// OperationExample represents an example input/output pair for an operation.
type OperationExample struct {
	Description string `json:"description,omitempty"`
	Input       any    `json:"input,omitempty"`
	Output      any    `json:"output,omitempty"`

	LosslessFields
}

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

	e.Extensions, e.Unknown = splitLossless(raw, knownOperationExampleSet)
	return nil
}

func (e OperationExample) MarshalJSON() ([]byte, error) {
	w := operationExampleWire{
		Description: e.Description,
		Input:       e.Input,
		Output:      e.Output,
	}
	return marshalLossless(e.Unknown, e.Extensions, w)
}

type Operation struct {
	Description string      `json:"description,omitempty"`
	Deprecated  bool        `json:"deprecated,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Aliases     []string    `json:"aliases,omitempty"`
	Satisfies   []Satisfies `json:"satisfies,omitempty"`

	Idempotent *bool      `json:"idempotent,omitempty"`
	Input      JSONSchema `json:"input,omitempty"`
	Output     JSONSchema `json:"output,omitempty"`

	// Examples contains named example input/output pairs.
	Examples map[string]OperationExample `json:"examples,omitempty"`

	LosslessFields
}

type operationWire struct {
	Description string      `json:"description,omitempty"`
	Deprecated  bool        `json:"deprecated,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Aliases     []string    `json:"aliases,omitempty"`
	Satisfies   []Satisfies `json:"satisfies,omitempty"`

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
		Satisfies:   w.Satisfies,
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
		Satisfies:   o.Satisfies,
		Idempotent:  o.Idempotent,
		Input:       o.Input,
		Output:      o.Output,
		Examples:    o.Examples,
	}
	return marshalLossless(o.Unknown, o.Extensions, w)
}

type Source struct {
	Format      string   `json:"format"`
	Location    string   `json:"location,omitempty"`
	Content     any      `json:"content,omitempty"`
	Description string   `json:"description,omitempty"`
	Priority    *float64 `json:"priority,omitempty"`

	LosslessFields
}

type sourceWire struct {
	Format      string   `json:"format"`
	Location    string   `json:"location,omitempty"`
	Content     any      `json:"content,omitempty"`
	Description string   `json:"description,omitempty"`
	Priority    *float64 `json:"priority,omitempty"`
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
		Priority:    w.Priority,
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
		Priority:    s.Priority,
	}
	return marshalLossless(s.Unknown, s.Extensions, w)
}

// Transform is a JSONata 2.0 expression string per OpenBindings v0.2 spec §6.5.
// Tools claiming Invoking-class conformance MUST evaluate transforms according
// to the JSONata 2.0 specification (OBI-T-11).
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
	Priority    *float64 `json:"priority,omitempty"`
	Description string   `json:"description,omitempty"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	Security    string   `json:"security,omitempty"`

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
	Security    string   `json:"security,omitempty"`

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
		Security:        w.Security,
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
		Security:        be.Security,
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

	// Roles is an optional role table mapping local aliases to URLs/paths
	// of other OpenBindings interfaces. Used by satisfies references.
	Roles map[string]string `json:"roles,omitempty"`

	Sources  map[string]Source       `json:"sources,omitempty"`
	Bindings map[string]BindingEntry `json:"bindings,omitempty"`

	// Security contains named security entries referenced by bindings.
	Security map[string][]SecurityMethod `json:"security,omitempty"`

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

	Roles map[string]string `json:"roles,omitempty"`

	Sources  map[string]Source       `json:"sources,omitempty"`
	Bindings map[string]BindingEntry `json:"bindings,omitempty"`

	Security map[string][]SecurityMethod `json:"security,omitempty"`

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
		Roles:        w.Roles,
		Sources:      w.Sources,
		Bindings:     w.Bindings,
		Security:     w.Security,
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
		Roles:        i.Roles,
		Sources:      i.Sources,
		Bindings:     i.Bindings,
		Security:     i.Security,
		Transforms:   i.Transforms,
	}
	return marshalLossless(i.Unknown, i.Extensions, w)
}
