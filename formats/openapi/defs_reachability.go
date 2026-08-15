package openapi

import "strings"

// pruneUnreachableDefs enforces the emitted operation schema's cut-point
// invariant: every definition synthesis itself minted is one the emitted
// schema root can reach, computed as a fixpoint (a minted definition is kept
// when the root tree references it, or when a kept definition does).
//
// The invariant needs enforcing because hoisting and direction projection run
// in that order: inlineRefsInOperationSchema materializes a `$defs` entry for
// every cycle participant it meets while walking the source-shaped tree, and
// projectOpenAPISchema then removes the readOnly (request) or writeOnly
// (response) properties. A minted definition whose only reference sat on a
// removed property survives as an orphan — a component published inside the
// operation's contract that no instance of that contract can contain, which
// is emitting something the artifact does not mean.
//
// `hoisted` is what keeps the rule narrow. An OAS 3.1 Schema Object is a JSON
// Schema 2020-12 schema and may carry its own `$defs`; those members are
// declared artifact content and stand whether or not anything references
// them. Only names this synthesis minted are subject to the closure.
//
// Dropping an unreachable minted member changes the emitted inventory and
// nothing else: `$defs` is definitional only (JSON Schema 2020-12 §8.2.4 —
// the keyword "does not directly affect the validation result"), so no
// instance changes its validation outcome. The fixpoint is what keeps every
// surviving `$ref` resolvable inside the emitted document (OBI-D-16).
func pruneUnreachableDefs(schema map[string]any, refBase string, hoisted map[string]bool) map[string]any {
	if schema == nil || len(hoisted) == 0 {
		return schema
	}
	defs, ok := schema["$defs"].(map[string]any)
	if !ok || len(defs) == 0 {
		return schema
	}

	prefix := refBase + "/$defs/"
	reachable := make(map[string]bool, len(defs))
	pending := make([]string, 0, len(defs))
	visit := func(node any) {
		for _, ref := range collectSchemaRefStrings(node) {
			if !strings.HasPrefix(ref, prefix) {
				continue
			}
			encoded := strings.TrimPrefix(ref, prefix)
			if slash := strings.IndexByte(encoded, '/'); slash >= 0 {
				encoded = encoded[:slash]
			}
			name := unescapeJSONPointerSegment(encoded)
			if _, defined := defs[name]; !defined || reachable[name] {
				continue
			}
			reachable[name] = true
			pending = append(pending, name)
		}
	}

	// Seeds: the schema root outside `$defs`, plus every definition that
	// stands regardless of reachability — the artifact's own. A minted
	// definition referenced only from an authorial one is still reached.
	root := make(map[string]any, len(schema))
	for key, value := range schema {
		if key != "$defs" {
			root[key] = value
		}
	}
	visit(root)
	for name, definition := range defs {
		if !hoisted[name] {
			visit(definition)
		}
	}
	for len(pending) > 0 {
		name := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		visit(defs[name])
	}

	kept := make(map[string]any, len(defs))
	for name, definition := range defs {
		if hoisted[name] && !reachable[name] {
			continue
		}
		kept[name] = definition
	}
	if len(kept) == len(defs) {
		return schema
	}
	if len(kept) == 0 {
		delete(schema, "$defs")
		return schema
	}
	schema["$defs"] = kept
	return schema
}

// collectSchemaRefStrings gathers every `$ref` string value anywhere in a
// node. It is deliberately position-blind: keeping a definition that only an
// annotation payload happens to name costs one unreferenced member, while
// dropping one a validator would follow would leave a same-document fragment
// that does not resolve (OBI-D-16). Reachability errs toward keeping.
func collectSchemaRefStrings(node any) []string {
	var refs []string
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "$ref" {
					if ref, ok := child.(string); ok {
						refs = append(refs, ref)
						continue
					}
				}
				walk(child)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(node)
	return refs
}
