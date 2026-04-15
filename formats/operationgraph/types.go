// Package operationgraph implements the openbindings.operation-graph binding
// format for OpenBindings. It executes operation graphs: directed graphs of
// typed nodes that orchestrate OBI operations.
package operationgraph

import "encoding/json"

// Document is the top-level operation graph source document.
type Document struct {
	Version string            `json:"openbindings.operation-graph"`
	Graphs  map[string]*Graph `json:"graphs"`
}

// Graph is a single named operation graph.
type Graph struct {
	Description string           `json:"description,omitempty"`
	Nodes       map[string]*Node `json:"nodes"`
	Edges       []Edge           `json:"edges"`
}

// Node is a typed node in the graph. The Type field determines which other
// fields are meaningful.
type Node struct {
	Type string `json:"type"`

	// All nodes
	OnError string `json:"onError,omitempty"`

	// operation
	Operation     string `json:"operation,omitempty"`
	MaxIterations *int   `json:"maxIterations,omitempty"`
	Timeout       *int   `json:"timeout,omitempty"`

	// buffer
	Limit   *int             `json:"limit,omitempty"`
	Until   *json.RawMessage `json:"until,omitempty"`
	Through *json.RawMessage `json:"through,omitempty"`

	// filter (schema-based)
	Schema *json.RawMessage `json:"schema,omitempty"`

	// filter (expression-based), transform, map
	Transform *TransformDef `json:"transform,omitempty"`

	// exit
	Error *bool `json:"error,omitempty"`
}

// TransformDef is a transform expression embedded on a node.
type TransformDef struct {
	Type       string `json:"type"`
	Expression string `json:"expression"`
}

// Edge connects two nodes.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ParseDocument parses an operation graph source document from JSON.
func ParseDocument(data []byte) (*Document, error) {
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}
