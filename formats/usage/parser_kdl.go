package usage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sblinch/kdl-go"
	"github.com/sblinch/kdl-go/document"
)

// ParseKDL parses a Usage spec from KDL bytes.
// Include directives are not resolved (no base path available).
// Use ParseFile for file-based parsing with automatic include resolution.
func ParseKDL(b []byte) (*Spec, error) {
	doc, err := kdl.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}

	raw := rawFromDocument(doc)
	return &Spec{Nodes: raw}, nil
}

// ParseFile reads and parses a Usage spec from a file, resolving include directives.
func ParseFile(path string) (*Spec, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return parseFileResolving(absPath, nil)
}

func parseFileResolving(absPath string, visited map[string]bool) (*Spec, error) {
	if visited == nil {
		visited = make(map[string]bool)
	}
	if visited[absPath] {
		return nil, fmt.Errorf("include cycle detected: %s", absPath)
	}
	visited[absPath] = true

	b, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}

	doc, err := kdl.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}

	raw := rawFromDocument(doc)
	baseDir := filepath.Dir(absPath)

	resolved, err := resolveIncludes(raw, baseDir, visited)
	if err != nil {
		return nil, err
	}

	return &Spec{Nodes: resolved}, nil
}

// resolveIncludes replaces include nodes with the contents of the referenced files.
func resolveIncludes(nodes []Node, baseDir string, visited map[string]bool) ([]Node, error) {
	var out []Node
	for _, n := range nodes {
		if n.Name != "include" {
			out = append(out, n)
			continue
		}

		// Read file path from property (spec canonical) or positional arg (compat)
		filePath := ""
		if v, ok := n.Props["file"]; ok {
			filePath = v.String()
		}
		if filePath == "" && len(n.Args) > 0 {
			filePath = n.Args[0].String()
		}
		if filePath == "" {
			continue
		}

		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(baseDir, filePath)
		}
		filePath = filepath.Clean(filePath)

		// Normalize path separators before cycle check
		filePath, err := filepath.Abs(filePath)
		if err != nil {
			return nil, fmt.Errorf("include %q: %w", filePath, err)
		}

		included, err := parseFileResolving(filePath, visited)
		if err != nil {
			return nil, fmt.Errorf("include %q: %w", filePath, err)
		}

		out = append(out, included.Nodes...)
	}
	return out, nil
}

// rawFromDocument converts a KDL document to Node slices.
func rawFromDocument(doc *document.Document) []Node {
	raw := make([]Node, 0, len(doc.Nodes))
	for _, n := range doc.Nodes {
		raw = append(raw, rawFromNode(n))
	}
	return raw
}

func rawFromNode(n *document.Node) Node {
	node := Node{
		Name:  nodeNameString(n),
		Args:  make([]Value, 0, len(n.Arguments)),
		Props: map[string]Value{},
	}

	for _, a := range n.Arguments {
		node.Args = append(node.Args, Value{Raw: a.Value})
	}

	for k, v := range n.Properties {
		node.Props[k] = Value{Raw: v.Value}
	}

	if n.Children != nil {
		for _, c := range n.Children {
			node.Children = append(node.Children, rawFromNode(c))
		}
	}

	return node
}

// nodeNameString extracts the name string from a KDL node.
func nodeNameString(n *document.Node) string {
	if n.Name == nil {
		return ""
	}
	if s, ok := n.Name.Value.(string); ok {
		return s
	}
	return n.Name.String()
}
