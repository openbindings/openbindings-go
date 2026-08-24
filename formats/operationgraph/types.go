// Package operationgraph implements the openbindings.operation-graph@1 binding
// specification. It executes operation graphs: directed graphs of typed nodes
// that compose OBI operations, governed by the identity law —
// `input → operation(y) → output` is observationally indistinguishable from
// invoking y directly.
//
// Two versions travel together and must not be confused. The binding
// specification identifier is openbindings.operation-graph@1 — opaque and
// exact, carried in a source's bindingSpec field (see BindingSpec). The
// graph-unit format has its own edition, which each graph declares in its
// openbindings.operation-graph field; this package targets edition 0.2.0, the
// ground-up rewrite around the transparency principle. The
// openbindings.operation-graph@0.2.0 token is not the identifier: it is the
// pre-bindingSpec form the @1 identifier replaced.
package operationgraph

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Spec-defined error identifiers (SCREAMING_SNAKE_CASE, per the format spec's
// Error identifiers table). They appear as the `error` member of in-graph
// error events and as terminal-error codes for failures the format defines.
// Failures originating in an inner invocation surface the inner terminal
// error verbatim instead.
const (
	TimeoutExceeded            = "TIMEOUT_EXCEEDED"
	WriteRejected              = "WRITE_REJECTED"
	MapNotArray                = "MAP_NOT_ARRAY"
	TransformUndefined         = "TRANSFORM_UNDEFINED"
	ExpressionEvaluationFailed = "EXPRESSION_EVALUATION_FAILED"
)

// Graph is one operation graph definition: the addressable unit of this
// binding format (a binding's selector resolves to it via JSON Pointer). The JSON
// document hosting it is unconstrained and carries no type here.
type Graph struct {
	// Version is the graph's own format version declaration
	// ("openbindings.operation-graph" field). Each graph in a document
	// declares its own version independently (OG-V-01, OG-T-02).
	Version       string           `json:"openbindings.operation-graph"`
	Description   string           `json:"description,omitempty"`
	Nodes         map[string]*Node `json:"nodes"`
	Edges         []Edge           `json:"edges"`
	unknownFields []string
}

// Node is a typed node in the graph. The Type field is the discriminator
// determining which other fields are valid (enforced by Validate's per-type
// field whitelists, mirroring the format schema).
type Node struct {
	Type string `json:"type"`

	// All processing nodes (not input/output; OG-V-17).
	OnError string `json:"onError,omitempty"`

	// operation (the conduit) and each.
	Operation string `json:"operation,omitempty"`
	Timeout   *int   `json:"timeout,omitempty"`

	// each only.
	MaxIterations *int `json:"maxIterations,omitempty"`

	// buffer.
	Limit   *int             `json:"limit,omitempty"`
	Until   *json.RawMessage `json:"until,omitempty"`
	Through *json.RawMessage `json:"through,omitempty"`

	// filter (schema-based).
	Schema *json.RawMessage `json:"schema,omitempty"`

	// filter (expression-based), transform, map.
	Transform *string `json:"transform,omitempty"`

	// exit.
	Error         *bool `json:"error,omitempty"`
	unknownFields []string
}

// Edge connects two nodes. Edges carry event streams and no logic.
type Edge struct {
	From          string `json:"from"`
	To            string `json:"to"`
	unknownFields []string
}

func (g *Graph) UnmarshalJSON(data []byte) error {
	type wire Graph
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*g = Graph(decoded)
	g.unknownFields = unknownFields(raw, map[string]bool{
		"openbindings.operation-graph": true, "description": true, "nodes": true, "edges": true,
	})
	return nil
}

func (n *Node) UnmarshalJSON(data []byte) error {
	type wire Node
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*n = Node(decoded)
	allowed := map[string]bool{"type": true}
	if fields, ok := nodeFieldRules[n.Type]; ok {
		for field := range fields.allowed {
			allowed[field] = true
		}
	}
	n.unknownFields = unknownFields(raw, allowed)
	return nil
}

func (e *Edge) UnmarshalJSON(data []byte) error {
	type wire Edge
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*e = Edge(decoded)
	e.unknownFields = unknownFields(raw, map[string]bool{"from": true, "to": true})
	return nil
}

func unknownFields(raw map[string]json.RawMessage, allowed map[string]bool) []string {
	var fields []string
	for field := range raw {
		if !allowed[field] && !strings.HasPrefix(field, "x-") {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	return fields
}

// graphFromValue converts a JSON value (the target of a resolved selector) into a
// Graph. The value must be a JSON object; structural validity beyond that is
// Validate's concern.
func graphFromValue(v any) (*Graph, error) {
	if _, ok := v.(map[string]any); !ok {
		return nil, fmt.Errorf("resolved selector value is not a JSON object")
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode resolved graph: %w", err)
	}
	var g Graph
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, fmt.Errorf("decode operation graph: %w", err)
	}
	return &g, nil
}
