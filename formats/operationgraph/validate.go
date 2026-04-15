package operationgraph

import (
	"fmt"
	"sort"
	"strings"
)

// Validate checks the well-formedness rules defined in the operation graph spec.
// operationKeys is the set of valid operation keys from the containing OBI.
func Validate(g *Graph, operationKeys map[string]bool) error {
	if g == nil {
		return fmt.Errorf("graph is nil")
	}
	if len(g.Nodes) == 0 {
		return fmt.Errorf("graph has no nodes")
	}

	var errs []string

	// Rule 1 & 2: exactly one input and one output node.
	var inputKey, outputKey string
	inputCount, outputCount := 0, 0
	for key, node := range g.Nodes {
		switch node.Type {
		case "input":
			inputCount++
			inputKey = key
		case "output":
			outputCount++
			outputKey = key
		}
	}
	if inputCount != 1 {
		errs = append(errs, fmt.Sprintf("expected exactly 1 input node, found %d", inputCount))
	}
	if outputCount != 1 {
		errs = append(errs, fmt.Sprintf("expected exactly 1 output node, found %d", outputCount))
	}

	// Build adjacency lists and incoming edge counts.
	outEdges := make(map[string][]string) // from -> [to]
	inEdges := make(map[string][]string)  // to -> [from]
	edgeSeen := make(map[string]bool)
	for _, e := range g.Edges {
		// Rule 6: edges reference valid node keys.
		if _, ok := g.Nodes[e.From]; !ok {
			errs = append(errs, fmt.Sprintf("edge references unknown node %q in from", e.From))
		}
		if _, ok := g.Nodes[e.To]; !ok {
			errs = append(errs, fmt.Sprintf("edge references unknown node %q in to", e.To))
		}
		// Rule 7: no duplicate edges.
		edgeKey := e.From + " -> " + e.To
		if edgeSeen[edgeKey] {
			errs = append(errs, fmt.Sprintf("duplicate edge: %s", edgeKey))
		}
		edgeSeen[edgeKey] = true

		outEdges[e.From] = append(outEdges[e.From], e.To)
		inEdges[e.To] = append(inEdges[e.To], e.From)
	}

	// Rule 3: input has no incoming edges.
	if inputKey != "" && len(inEdges[inputKey]) > 0 {
		errs = append(errs, "input node must not have incoming edges")
	}

	// Rule 4: output has no outgoing edges.
	if outputKey != "" && len(outEdges[outputKey]) > 0 {
		errs = append(errs, "output node must not have outgoing edges")
	}

	// Rule 14: exit nodes have no outgoing edges.
	for key, node := range g.Nodes {
		if node.Type == "exit" && len(outEdges[key]) > 0 {
			errs = append(errs, fmt.Sprintf("exit node %q must not have outgoing edges", key))
		}
	}

	// Rule 5: every node reachable from input via edges or onError references.
	if inputKey != "" {
		reachable := make(map[string]bool)
		var walk func(string)
		walk = func(key string) {
			if reachable[key] {
				return
			}
			reachable[key] = true
			for _, to := range outEdges[key] {
				walk(to)
			}
			// Transitive via onError.
			if node, ok := g.Nodes[key]; ok && node.OnError != "" {
				walk(node.OnError)
			}
		}
		walk(inputKey)
		for key := range g.Nodes {
			if !reachable[key] {
				errs = append(errs, fmt.Sprintf("node %q is not reachable from input", key))
			}
		}
	}

	// Rule 8: every cycle must contain at least one operation node with maxIterations.
	cycles := findCycles(g)
	for _, cycle := range cycles {
		sort.Strings(cycle)
		hasGuard := false
		for _, key := range cycle {
			node := g.Nodes[key]
			if node.Type == "operation" && node.MaxIterations != nil {
				hasGuard = true
				break
			}
		}
		if !hasGuard {
			errs = append(errs, fmt.Sprintf("cycle [%s] must contain at least one operation node with maxIterations", strings.Join(cycle, " -> ")))
		}
	}

	// Per-node validation.
	for key, node := range g.Nodes {
		// Rule 12: valid type.
		switch node.Type {
		case "input", "output", "operation", "buffer", "filter", "transform", "map", "combine", "exit":
			// ok
		default:
			errs = append(errs, fmt.Sprintf("node %q has unsupported type %q", key, node.Type))
		}

		// Rule 9: operation nodes reference valid operations.
		if node.Type == "operation" {
			if node.Operation == "" {
				errs = append(errs, fmt.Sprintf("operation node %q missing operation field", key))
			} else if operationKeys != nil && !operationKeys[node.Operation] {
				errs = append(errs, fmt.Sprintf("operation node %q references unknown operation %q", key, node.Operation))
			}
		}

		// Rule 10: filter mutual exclusivity.
		if node.Type == "filter" {
			hasSchema := node.Schema != nil
			hasTransform := node.Transform != nil
			if !hasSchema && !hasTransform {
				errs = append(errs, fmt.Sprintf("filter node %q must have schema or transform", key))
			}
			if hasSchema && hasTransform {
				errs = append(errs, fmt.Sprintf("filter node %q must have exactly one of schema or transform", key))
			}
		}

		// Rule 11: buffer mutual exclusivity.
		if node.Type == "buffer" && node.Until != nil && node.Through != nil {
			errs = append(errs, fmt.Sprintf("buffer node %q must not have both until and through", key))
		}

		// Rule 13: onError references valid node.
		if node.OnError != "" {
			if _, ok := g.Nodes[node.OnError]; !ok {
				errs = append(errs, fmt.Sprintf("node %q onError references unknown node %q", key, node.OnError))
			}
		}

		// transform and map nodes require a transform field.
		if (node.Type == "transform" || node.Type == "map") && node.Transform == nil {
			errs = append(errs, fmt.Sprintf("%s node %q missing transform field", node.Type, key))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("validation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// findCycles returns all strongly connected components with more than one node,
// using Tarjan's algorithm.
func findCycles(g *Graph) [][]string {
	outEdges := make(map[string][]string)
	for _, e := range g.Edges {
		outEdges[e.From] = append(outEdges[e.From], e.To)
	}

	var (
		index    int
		stack    []string
		onStack  = make(map[string]bool)
		indices  = make(map[string]int)
		lowlinks = make(map[string]int)
		visited  = make(map[string]bool)
		result   [][]string
	)

	var strongConnect func(string)
	strongConnect = func(v string) {
		indices[v] = index
		lowlinks[v] = index
		index++
		visited[v] = true
		stack = append(stack, v)
		onStack[v] = true

		for _, w := range outEdges[v] {
			if !visited[w] {
				strongConnect(w)
				if lowlinks[w] < lowlinks[v] {
					lowlinks[v] = lowlinks[w]
				}
			} else if onStack[w] {
				if indices[w] < lowlinks[v] {
					lowlinks[v] = indices[w]
				}
			}
		}

		if lowlinks[v] == indices[v] {
			var scc []string
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			if len(scc) > 1 {
				result = append(result, scc)
			}
			// Also detect self-loops.
			if len(scc) == 1 {
				for _, to := range outEdges[scc[0]] {
					if to == scc[0] {
						result = append(result, scc)
						break
					}
				}
			}
		}
	}

	for key := range g.Nodes {
		if !visited[key] {
			strongConnect(key)
		}
	}
	return result
}
