package asyncapi

import "strings"

// Cyclic-reference hoisting (the openapi family's rev-2a precedent, applied
// to AsyncAPI). resolveSchemaRefs inlines every acyclic internal reference;
// a cycle necessarily leaves a `{"$ref": "#/..."}` node behind, which would
// dangle in the emitted OBI (OBI-D-16: the pointer has no target inside the
// interface document). This pass materializes each surviving resolvable ref
// exactly once into the operation schema's `$defs` and rewrites every
// occurrence to point there, so the recursion the artifact declared survives
// the projection intact. A ref whose target does not resolve is left as-is:
// that is the artifact's dangling pointer, confined per-operation by the
// schema-defect gate rather than silently repaired.

type decycleState struct {
	rawDoc  map[string]any
	refBase string
	defs    map[string]any
	names   map[string]string // source ref -> assigned $defs name
}

// decycleOperationSchema hoists surviving internal refs of one operation
// direction. refBase is the emitted schema's own location, for example
// "#/operations/sendUserSignedUp/input".
func decycleOperationSchema(schema map[string]any, rawDoc map[string]any, refBase string) map[string]any {
	if schema == nil || rawDoc == nil {
		return schema
	}
	// The walk rewrites in place; the boundary schema may still alias
	// document memory through keyword positions translation carried by
	// reference, so hoisting always works on its own copy.
	schema = deepCopyMap(schema)
	state := &decycleState{rawDoc: rawDoc, refBase: refBase, defs: map[string]any{}, names: map[string]string{}}
	// Seed name uniquification with the schema's own $defs members so a
	// hoisted component never shadows an artifact-authored definition.
	if existing, ok := schema["$defs"].(map[string]any); ok {
		for name := range existing {
			state.names["\x00taken:"+name] = name
		}
	}
	result, _ := decycleNode(schema, state, "", state.refBase).(map[string]any)
	if result == nil {
		return schema
	}
	if len(state.defs) > 0 {
		merged := map[string]any{}
		if existing, ok := result["$defs"].(map[string]any); ok {
			for name, member := range existing {
				merged[name] = member
			}
		}
		for name, member := range state.defs {
			merged[name] = member
		}
		result["$defs"] = merged
	}
	return result
}

// decycleNode walks one node. path is the node's JSON Pointer below the
// operation direction (refBase); anchor is the absolute pointer of the
// nearest ancestor (or self) carrying a $defs map — the base a
// derivation-emitted "#/$defs/<name>" ref is relative to. A derived schema
// rides inside the routed envelope at properties.payload (or .headers), so
// its self-relative refs must rebase onto ITS location, not the direction
// root.
func decycleNode(node any, state *decycleState, path string, anchor string) any {
	schema, ok := node.(map[string]any)
	if !ok {
		if arr, ok := node.([]any); ok {
			for i, member := range arr {
				arr[i] = decycleNode(member, state, path+"/"+itoaZ(i), anchor)
			}
		}
		return node
	}
	if _, carries := schema["$defs"].(map[string]any); carries {
		anchor = state.refBase + path
	}
	if ref, ok := schema["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
		// A derivation-emitted local ref ("#/$defs/<name>", the Avro
		// correspondence's named-type spelling) rebases onto its carrying
		// schema's pointer; artifact refs materialize as before. The
		// walk then continues into sibling members: 2020-12 evaluates
		// keywords beside $ref, and the derived root is exactly
		// {$ref, $defs} — returning here would leave every ref inside
		// that $defs unrebased (dangling in the emitted document).
		if strings.HasPrefix(ref, "#/$defs/") {
			schema["$ref"] = anchor + strings.TrimPrefix(ref, "#")
		} else if name, materialized := state.materialize(ref); materialized {
			schema["$ref"] = state.refBase + "/$defs/" + escapeDefsPointerSegment(name)
		}
	}
	for key, value := range schema {
		if schemaMapContainerKeys[key] {
			if members, ok := value.(map[string]any); ok {
				for name, member := range members {
					members[name] = decycleNode(member, state, path+"/"+escapeDefsPointerSegment(key)+"/"+escapeDefsPointerSegment(name), anchor)
				}
				continue
			}
		}
		if isLiteralSchemaValueKey(key) || strings.HasPrefix(strings.ToLower(key), "x-") {
			continue
		}
		schema[key] = decycleNode(value, state, path+"/"+escapeDefsPointerSegment(key), anchor)
	}
	return schema
}

func itoaZ(n int) string {
	if n == 0 {
		return "0"
	}
	return itoa(n)
}

// materialize resolves ref from the raw document into $defs (once), applying
// the same dialect pipeline the payload itself went through, and returns the
// assigned name. A target that does not resolve to a schema object reports
// false and the caller leaves the artifact's dangling ref untouched.
func (state *decycleState) materialize(ref string) (string, bool) {
	if name, done := state.names[ref]; done {
		return name, true
	}
	target := resolveJSONPointer(state.rawDoc, ref)
	resolved, ok := target.(map[string]any)
	if !ok {
		return "", false
	}
	name := state.uniqueDefName(defsNameForRef(ref))
	state.names[ref] = name
	state.defs[name] = nil // reserve before expansion: terminates self-reference

	copied := deepCopyMap(resolved)
	// components.schemas entries in a v3 document may be Multi Format Schema
	// Objects; the wrapper is never schema vocabulary.
	format := ""
	if declared, isWrapper := copied["schemaFormat"].(string); isWrapper {
		inner, _ := copied["schema"].(map[string]any)
		copied, format = inner, declared
	}
	switch classifySchemaFormat(format) {
	case schemaFormatForeign:
		// The direction-level rule (§9.2): a foreign representation enters
		// an OBI schema position only as the unconstrained schema.
		state.defs[name] = map[string]any{}
		return name, true
	case schemaFormatTranslate:
		copied = translateSchemaDialect(resolveSchemaRefs(copied, state.rawDoc, map[string]bool{ref: true}))
	default:
		copied = resolveSchemaRefs(copied, state.rawDoc, map[string]bool{ref: true})
	}
	state.defs[name] = decycleNode(copied, state, "/$defs/"+escapeDefsPointerSegment(name), state.refBase)
	return name, true
}

func (state *decycleState) uniqueDefName(base string) string {
	if base == "" {
		base = "schema"
	}
	name := base
	for suffix := 2; state.defNameTaken(name); suffix++ {
		name = base + "_" + itoa(suffix)
	}
	state.names["\x00taken:"+name] = name
	return name
}

func (state *decycleState) defNameTaken(name string) bool {
	if _, taken := state.names["\x00taken:"+name]; taken {
		return true
	}
	_, taken := state.defs[name]
	return taken
}

// defsNameForRef derives a readable $defs key: the trailing pointer segment
// of the source ref, unescaped.
func defsNameForRef(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return strings.ReplaceAll(strings.ReplaceAll(ref[i+1:], "~1", "/"), "~0", "~")
	}
	return ref
}

func escapeDefsPointerSegment(segment string) string {
	return strings.ReplaceAll(strings.ReplaceAll(segment, "~", "~0"), "/", "~1")
}

func itoa(n int) string {
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
