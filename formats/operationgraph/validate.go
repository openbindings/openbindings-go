package operationgraph

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// GraphValidationIssue is one validation failure. Rule is the stable
// identifier from the format spec's Validation rules section (OG-V-##) when
// the failure maps to a numbered rule; per-type field-whitelist failures
// (the schema's structural enforcement) carry no rule unless they realize
// OG-V-17. NodeKeys names the offending node(s) when the failure is
// attributable — a graph editor can highlight them directly. The shape
// mirrors the TS SDK's GraphValidationIssue.
type GraphValidationIssue struct {
	Rule     string   `json:"rule,omitempty"`
	Message  string   `json:"message"`
	NodeKeys []string `json:"nodeKeys,omitempty"`
}

// FormatIssue renders an issue the way the throwing Validate reports it.
func FormatIssue(i GraphValidationIssue) string {
	if i.Rule != "" {
		return i.Rule + ": " + i.Message
	}
	return i.Message
}

// ValidateGraph enforces the format spec's validation rules OG-V-01 through
// OG-V-17 on one graph definition, plus the per-type field whitelists the
// format's JSON Schema expresses structurally (which is how OG-V-17 and the
// each/operation field split are caught without a separate schema pass).
// It returns structured issues (empty = valid).
//
// operationKeys is the set of operation keys the containing OBI declares;
// when nil, the OG-V-11 reference check is skipped (references then fail at
// invocation time).
func ValidateGraph(g *Graph, operationKeys map[string]bool) []GraphValidationIssue {
	if g == nil {
		return []GraphValidationIssue{{Message: "graph is nil"}}
	}
	if len(g.Nodes) == 0 {
		return []GraphValidationIssue{{Message: "graph has no nodes"}}
	}

	var issues []GraphValidationIssue
	addIssue := func(rule, message string, nodeKeys ...string) {
		issues = append(issues, GraphValidationIssue{Rule: rule, Message: message, NodeKeys: nodeKeys})
	}

	// OG-V-01: version field present and SemVer 2.0.0.
	if g.Version == "" {
		addIssue("OG-V-01", "graph must declare an openbindings.operation-graph version field")
	} else if !semverRe.MatchString(g.Version) {
		addIssue("OG-V-01", fmt.Sprintf("version %q is not a SemVer 2.0.0 string", g.Version))
	}

	// OG-V-02 / OG-V-03: exactly one input and one output node.
	var inputKeys, outputKeys []string
	for _, key := range sortedKeys(g.Nodes) {
		switch g.Nodes[key].Type {
		case "input":
			inputKeys = append(inputKeys, key)
		case "output":
			outputKeys = append(outputKeys, key)
		}
	}
	var inputKey, outputKey string
	if len(inputKeys) == 1 {
		inputKey = inputKeys[0]
	}
	if len(outputKeys) == 1 {
		outputKey = outputKeys[0]
	}
	if len(inputKeys) != 1 {
		addIssue("OG-V-02", fmt.Sprintf("expected exactly 1 input node, found %d", len(inputKeys)), inputKeys...)
	}
	if len(outputKeys) != 1 {
		addIssue("OG-V-03", fmt.Sprintf("expected exactly 1 output node, found %d", len(outputKeys)), outputKeys...)
	}

	// Build adjacency. OG-V-07 (valid endpoints) and OG-V-08 (no duplicates)
	// check the raw edge list.
	outEdges := make(map[string][]string)
	inEdges := make(map[string][]string)
	edgeSeen := make(map[string]bool)
	for _, e := range g.Edges {
		if _, ok := g.Nodes[e.From]; !ok {
			addIssue("OG-V-07", fmt.Sprintf("edge references unknown node %q in from", e.From), existingKeys(g, e.To)...)
		}
		if _, ok := g.Nodes[e.To]; !ok {
			addIssue("OG-V-07", fmt.Sprintf("edge references unknown node %q in to", e.To), existingKeys(g, e.From)...)
		}
		edgeKey := e.From + " -> " + e.To
		if edgeSeen[edgeKey] {
			addIssue("OG-V-08", "duplicate edge: "+edgeKey, existingKeys(g, e.From, e.To)...)
		}
		edgeSeen[edgeKey] = true
		outEdges[e.From] = append(outEdges[e.From], e.To)
		inEdges[e.To] = append(inEdges[e.To], e.From)
	}

	// OG-V-04 / OG-V-05: boundary edge constraints.
	if inputKey != "" && len(inEdges[inputKey]) > 0 {
		addIssue("OG-V-04", "input node must not have incoming edges", inputKey)
	}
	if outputKey != "" && len(outEdges[outputKey]) > 0 {
		addIssue("OG-V-05", "output node must not have outgoing edges", outputKey)
	}

	// OG-V-16: exit nodes have no outgoing edges.
	for key, node := range g.Nodes {
		if node.Type == "exit" && len(outEdges[key]) > 0 {
			addIssue("OG-V-16", fmt.Sprintf("exit node %q must not have outgoing edges", key), key)
		}
	}

	// succ is the control adjacency: data edges plus onError references.
	// Both OG-V-06 (reachability) and OG-V-09/OG-V-10 (cycles) follow it.
	succ := make(map[string][]string, len(g.Nodes))
	for key, node := range g.Nodes {
		succ[key] = append(succ[key], outEdges[key]...)
		if node.OnError != "" {
			succ[key] = append(succ[key], node.OnError)
		}
	}

	// OG-V-06: every node reachable from input.
	if inputKey != "" {
		reachable := make(map[string]bool)
		var walk func(string)
		walk = func(key string) {
			if reachable[key] {
				return
			}
			reachable[key] = true
			for _, next := range succ[key] {
				if _, ok := g.Nodes[next]; ok {
					walk(next)
				}
			}
		}
		walk(inputKey)
		for _, key := range sortedKeys(g.Nodes) {
			if !reachable[key] {
				addIssue("OG-V-06", fmt.Sprintf("node %q is not reachable from input", key), key)
			}
		}
	}

	// OG-V-09 / OG-V-10: cycle rules over the control adjacency.
	for _, cycle := range findCycles(g.Nodes, succ) {
		sort.Strings(cycle)
		hasBoundedEach := false
		for _, key := range cycle {
			node := g.Nodes[key]
			if node.Type == "each" && node.MaxIterations != nil {
				hasBoundedEach = true
			}
			if node.Type == "operation" {
				addIssue("OG-V-10", fmt.Sprintf("operation node %q must not participate in a cycle [%s]", key, strings.Join(cycle, " -> ")), key)
			}
		}
		if !hasBoundedEach {
			addIssue("OG-V-09", fmt.Sprintf("cycle [%s] must contain at least one each node with maxIterations", strings.Join(cycle, " -> ")), cycle...)
		}
	}

	// Per-node rules: OG-V-11 .. OG-V-15, OG-V-17, plus per-type field
	// whitelists (the schema's structural enforcement).
	for _, key := range sortedKeys(g.Nodes) {
		node := g.Nodes[key]

		// OG-V-14: defined node types only.
		fields, known := nodeFieldRules[node.Type]
		if !known {
			addIssue("OG-V-14", fmt.Sprintf("node %q has unsupported type %q", key, node.Type), key)
			continue
		}

		// Per-type field whitelist (schema-enforced in the format's JSON
		// Schema; OG-V-17 falls out of the boundary nodes' empty whitelists).
		for _, f := range presentFields(node) {
			if !fields.allowed[f] {
				rule := ""
				if f == "onError" && (node.Type == "input" || node.Type == "output") {
					rule = "OG-V-17"
				}
				addIssue(rule, fmt.Sprintf("node %q (%s) does not permit field %q", key, node.Type, f), key)
			}
		}
		for _, f := range fields.required {
			if !hasField(node, f) {
				addIssue("", fmt.Sprintf("%s node %q missing %s field", node.Type, key, f), key)
			}
		}

		// OG-V-11: operation and each references resolve in the containing OBI.
		if (node.Type == "operation" || node.Type == "each") && node.Operation != "" {
			if operationKeys != nil && !operationKeys[node.Operation] {
				addIssue("OG-V-11", fmt.Sprintf("%s node %q references unknown operation %q", node.Type, key, node.Operation), key)
			}
		}

		// OG-V-12: filter mutual exclusivity (exactly one of schema/transform).
		if node.Type == "filter" {
			hasSchema := node.Schema != nil
			hasTransform := node.Transform != nil
			if hasSchema == hasTransform {
				addIssue("OG-V-12", fmt.Sprintf("filter node %q must have exactly one of schema or transform", key), key)
			}
		}

		// OG-V-13: buffer until/through mutual exclusivity.
		if node.Type == "buffer" && node.Until != nil && node.Through != nil {
			addIssue("OG-V-13", fmt.Sprintf("buffer node %q must not have both until and through", key), key)
		}

		// OG-V-18: embedded schemas are 2020-12 objects — $schema, when
		// present, pinned to the 2020-12 URI; $vocabulary nowhere within.
		for field, raw := range map[string]*json.RawMessage{
			"schema": node.Schema, "until": node.Until, "through": node.Through,
		} {
			if raw == nil {
				continue
			}
			var embedded any
			if err := json.Unmarshal(*raw, &embedded); err != nil {
				continue // structurally invalid JSON is the schema's concern
			}
			obj, ok := embedded.(map[string]any)
			if !ok {
				addIssue("OG-V-18", fmt.Sprintf("node %q %s must be a JSON Schema 2020-12 object", key, field), key)
				continue
			}
			validateEmbeddedSchema(func(msg string) { addIssue("OG-V-18", msg, key) }, fmt.Sprintf("node %q %s", key, field), obj)
		}

		// OG-V-15: onError references an existing node.
		if node.OnError != "" {
			if _, ok := g.Nodes[node.OnError]; !ok {
				addIssue("OG-V-15", fmt.Sprintf("node %q onError references unknown node %q", key, node.OnError), key)
			}
		}
	}

	return issues
}

// Validate is the throwing (error-returning) form of ValidateGraph: the
// OG-T-01 gate used by the invoker. It joins every issue, one per line.
func Validate(g *Graph, operationKeys map[string]bool) error {
	issues := ValidateGraph(g, operationKeys)
	if len(issues) == 0 {
		return nil
	}
	if len(issues) == 1 && issues[0].Rule == "" && issues[0].NodeKeys == nil {
		// Shape preconditions ("graph is nil", "graph has no nodes") return
		// bare, matching the historical behavior.
		return fmt.Errorf("%s", issues[0].Message)
	}
	lines := make([]string, len(issues))
	for i, issue := range issues {
		lines[i] = FormatIssue(issue)
	}
	return fmt.Errorf("validation errors:\n  %s", strings.Join(lines, "\n  "))
}

// existingKeys filters node keys to those present in the graph (issue
// attribution never points at nonexistent nodes).
func existingKeys(g *Graph, keys ...string) []string {
	var out []string
	for _, k := range keys {
		if _, ok := g.Nodes[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

// semverRe is the SemVer 2.0.0 pattern (the same pattern the format schema
// carries).
var semverRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// The format version this implementation supports. OG-T-02 (mirroring the
// core spec's OBI-T-04): refuse a higher major version or one below the
// supported minimum (pre-1.0, both bounds apply to minors), and refuse
// prereleases absent declared support.
const (
	supportedMajor = 0
	supportedMinor = 2
)


// Keyword-shape tables for the OG-V-18 walk: recursion follows JSON Schema
// 2020-12 keyword shapes so that property NAMES under `properties`/`$defs`/
// etc. are schema content, never treated as keywords (the same discipline as
// the core SDK's schema walk).
var embeddedSchemaMapKeywords = map[string]bool{
	"properties": true, "patternProperties": true, "$defs": true,
	"definitions": true, "dependentSchemas": true,
}

var embeddedSchemaSingleKeywords = map[string]bool{
	"items": true, "additionalItems": true, "unevaluatedItems": true,
	"contains": true, "additionalProperties": true,
	"unevaluatedProperties": true, "propertyNames": true, "not": true,
	"if": true, "then": true, "else": true, "contentSchema": true,
}

var embeddedSchemaArrayKeywords = map[string]bool{
	"prefixItems": true, "allOf": true, "anyOf": true, "oneOf": true,
}

// validateEmbeddedSchema enforces OG-V-18 on one embedded schema object:
// $schema, when present, equals the 2020-12 dialect URI, and $vocabulary
// appears at no schema position within the tree. Recursion is
// keyword-shape-aware: only values in schema positions are walked.
func validateEmbeddedSchema(report func(string), prefix string, s map[string]any) {
	const draft202012 = "https://json-schema.org/draft/2020-12/schema"
	if v, ok := s["$schema"]; ok {
		if str, isStr := v.(string); isStr && str != draft202012 {
			report(fmt.Sprintf("%s: $schema %q must equal %q (OG-V-18)", prefix, str, draft202012))
		}
	}
	if _, ok := s["$vocabulary"]; ok {
		report(fmt.Sprintf("%s: $vocabulary is forbidden in embedded schemas (OG-V-18)", prefix))
	}
	if _, ok := s["$ref"]; ok {
		report(fmt.Sprintf("%s: $ref is forbidden in embedded schemas — they are self-contained (OG-V-18)", prefix))
	}
	for k, v := range s {
		switch {
		case embeddedSchemaMapKeywords[k]:
			if m, ok := v.(map[string]any); ok {
				for mk, mv := range m {
					if sub, ok := mv.(map[string]any); ok {
						validateEmbeddedSchema(report, fmt.Sprintf("%s.%s.%s", prefix, k, mk), sub)
					}
				}
			}
		case embeddedSchemaSingleKeywords[k]:
			if sub, ok := v.(map[string]any); ok {
				validateEmbeddedSchema(report, prefix+"."+k, sub)
			}
		case embeddedSchemaArrayKeywords[k]:
			if arr, ok := v.([]any); ok {
				for i, item := range arr {
					if sub, ok := item.(map[string]any); ok {
						validateEmbeddedSchema(report, fmt.Sprintf("%s.%s[%d]", prefix, k, i), sub)
					}
				}
			}
		}
	}
}

// checkVersion applies OG-T-02 to a graph's declared format version.
func checkVersion(version string) error {
	m := semverRe.FindStringSubmatch(version)
	if m == nil {
		return fmt.Errorf("version %q is not a SemVer 2.0.0 string", version)
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major > supportedMajor || (supportedMajor == 0 && major == 0 && minor > supportedMinor) {
		return fmt.Errorf("OG-T-02: graph declares openbindings.operation-graph %s; this implementation supports up to %d.%d.x", version, supportedMajor, supportedMinor)
	}
	if major < supportedMajor || (supportedMajor == 0 && major == 0 && minor < supportedMinor) {
		return fmt.Errorf("OG-T-02: graph declares openbindings.operation-graph %s; this implementation supports no lower than %d.%d.x", version, supportedMajor, supportedMinor)
	}
	if m[4] != "" {
		return fmt.Errorf("OG-T-02: graph declares prerelease openbindings.operation-graph %s; this implementation declares no prerelease support", version)
	}
	return nil
}

// fieldRules is a node type's permitted and required fields, mirroring the
// format schema's per-type whitelists.
type fieldRules struct {
	allowed  map[string]bool
	required []string
}

func rules(required []string, allowed ...string) fieldRules {
	m := make(map[string]bool, len(allowed)+len(required))
	for _, f := range allowed {
		m[f] = true
	}
	for _, f := range required {
		m[f] = true
	}
	return fieldRules{allowed: m, required: required}
}

var nodeFieldRules = map[string]fieldRules{
	"input":     rules(nil),
	"output":    rules(nil),
	"operation": rules([]string{"operation"}, "timeout", "onError"),
	"each":      rules([]string{"operation"}, "maxIterations", "timeout", "onError"),
	"transform": rules([]string{"transform"}, "onError"),
	"filter":    rules(nil, "schema", "transform", "onError"),
	"map":       rules([]string{"transform"}, "onError"),
	"buffer":    rules(nil, "limit", "until", "through", "onError"),
	"combine":   rules(nil, "onError"),
	"exit":      rules(nil, "error", "onError"),
}

// presentFields lists which spec fields are present on a node (the flat Node
// struct can represent every field; presence is what the whitelist judges).
func presentFields(n *Node) []string {
	var out []string
	if n.OnError != "" {
		out = append(out, "onError")
	}
	if n.Operation != "" {
		out = append(out, "operation")
	}
	if n.MaxIterations != nil {
		out = append(out, "maxIterations")
	}
	if n.Timeout != nil {
		out = append(out, "timeout")
	}
	if n.Limit != nil {
		out = append(out, "limit")
	}
	if n.Until != nil {
		out = append(out, "until")
	}
	if n.Through != nil {
		out = append(out, "through")
	}
	if n.Schema != nil {
		out = append(out, "schema")
	}
	if n.Transform != nil {
		out = append(out, "transform")
	}
	if n.Error != nil {
		out = append(out, "error")
	}
	return out
}

func hasField(n *Node, field string) bool {
	for _, f := range presentFields(n) {
		if f == field {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// findCycles returns all strongly connected components with more than one
// node, plus single-node SCCs with a self-loop, over the given adjacency
// (data edges plus onError references). Tarjan's algorithm.
func findCycles(nodes map[string]*Node, succ map[string][]string) [][]string {
	index := 0
	stack := []string{}
	onStack := map[string]bool{}
	indices := map[string]int{}
	lowlinks := map[string]int{}
	visited := map[string]bool{}
	var result [][]string

	var strongConnect func(v string)
	strongConnect = func(v string) {
		indices[v] = index
		lowlinks[v] = index
		index++
		visited[v] = true
		stack = append(stack, v)
		onStack[v] = true

		for _, w := range succ[v] {
			if _, ok := nodes[w]; !ok {
				continue // dangling reference; OG-V-07/OG-V-15 report it
			}
			if !visited[w] {
				strongConnect(w)
				if lowlinks[w] < lowlinks[v] {
					lowlinks[v] = lowlinks[w]
				}
			} else if onStack[w] && indices[w] < lowlinks[v] {
				lowlinks[v] = indices[w]
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
			} else if len(scc) == 1 {
				for _, to := range succ[scc[0]] {
					if to == scc[0] {
						result = append(result, scc)
						break
					}
				}
			}
		}
	}

	for _, key := range sortedKeys(nodes) {
		if !visited[key] {
			strongConnect(key)
		}
	}
	return result
}
