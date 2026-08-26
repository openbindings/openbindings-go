package openapi

import "github.com/getkin/kin-openapi/openapi3"

// resolvedDeclaration is the single declaration-only view used by the
// OpenAPI 3.0/3.1 binding siblings' lane, style, shape, and member-inspection
// rules. It deliberately is not a JSON Schema evaluator: it follows already
// resolved references, conjoins allOf, collapses only a choice with exactly
// one non-null branch, ignores not/conditionals, and keeps an absent type
// typeless.
type resolvedDeclaration struct {
	conjuncts []*openapi3.Schema
	types     map[string]bool
	ambiguous bool
	oas30     bool
}

func resolveDeclaration(schema *openapi3.Schema, oas30 bool) resolvedDeclaration {
	conjuncts, ambiguous := resolvedDeclarationConjuncts(schema, oas30, map[*openapi3.Schema]bool{})
	result := resolvedDeclaration{conjuncts: conjuncts, ambiguous: ambiguous, oas30: oas30}
	if ambiguous {
		return result
	}
	var constrained bool
	for _, conjunct := range conjuncts {
		candidate, present := declarationTypeSet(conjunct, oas30)
		if !present {
			continue
		}
		if !constrained {
			result.types = candidate
			constrained = true
			continue
		}
		for member := range result.types {
			if !candidate[member] {
				delete(result.types, member)
			}
		}
	}
	if !constrained {
		result.types = nil
	}
	return result
}

func resolvedDeclarationConjuncts(schema *openapi3.Schema, oas30 bool, seen map[*openapi3.Schema]bool) ([]*openapi3.Schema, bool) {
	if schema == nil || seen[schema] {
		return nil, false
	}
	if literal, boolean := booleanSchemaLiteral(schema); boolean {
		if literal {
			return []*openapi3.Schema{{}}, false
		}
		return nil, false
	}
	seen[schema] = true
	defer delete(seen, schema)

	conjuncts := []*openapi3.Schema{schema}
	for _, choice := range []openapi3.SchemaRefs{schema.AnyOf, schema.OneOf} {
		if len(choice) == 0 {
			continue
		}
		var selected *openapi3.Schema
		for _, branch := range choice {
			if branch == nil || branch.Value == nil {
				return nil, true
			}
			resolved := resolveDeclaration(branch.Value, oas30)
			if resolved.declaresOnly("null") {
				continue
			}
			if selected != nil {
				return nil, true
			}
			selected = branch.Value
		}
		if selected == nil {
			return nil, true
		}
		members, ambiguous := resolvedDeclarationConjuncts(selected, oas30, seen)
		if ambiguous {
			return nil, true
		}
		conjuncts = append(conjuncts, members...)
	}
	for _, member := range schema.AllOf {
		if member == nil || member.Value == nil {
			continue
		}
		members, ambiguous := resolvedDeclarationConjuncts(member.Value, oas30, seen)
		if ambiguous {
			return nil, true
		}
		conjuncts = append(conjuncts, members...)
	}
	return conjuncts, false
}

func declarationTypeSet(schema *openapi3.Schema, oas30 bool) (map[string]bool, bool) {
	if schema == nil || schema.Type == nil {
		return nil, false
	}
	types := schema.Type.Slice()
	if len(types) == 0 || oas30 && len(types) != 1 {
		return nil, false
	}
	result := make(map[string]bool, len(types)+1)
	for _, member := range types {
		result[member] = true
	}
	if oas30 && schema.Nullable {
		result["null"] = true
	}
	return result, true
}

// declaresOnly implements §5.2's named predicate: the resolved type set is
// nonempty and every member belongs to allowed.
func (d resolvedDeclaration) declaresOnly(allowed ...string) bool {
	if d.ambiguous || len(d.types) == 0 {
		return false
	}
	set := make(map[string]bool, len(allowed))
	for _, member := range allowed {
		set[member] = true
	}
	for member := range d.types {
		if !set[member] {
			return false
		}
	}
	return true
}

// admitsStringAsSoleNonNullType implements §5.2's other named predicate.
func (d resolvedDeclaration) admitsStringAsSoleNonNullType() bool {
	if d.ambiguous || len(d.types) == 0 || !d.types["string"] {
		return false
	}
	for member := range d.types {
		if member != "string" && member != "null" {
			return false
		}
	}
	return true
}

func (d resolvedDeclaration) propertyNames() []string {
	if d.ambiguous {
		return nil
	}
	present := map[string]bool{}
	var names []string
	for _, conjunct := range d.conjuncts {
		for name := range conjunct.Properties {
			if !present[name] {
				present[name] = true
				names = append(names, name)
			}
		}
	}
	return names
}

func (d resolvedDeclaration) property(name string) resolvedDeclaration {
	if d.ambiguous {
		return resolvedDeclaration{ambiguous: true, oas30: d.oas30}
	}
	var matches []*openapi3.Schema
	for _, conjunct := range d.conjuncts {
		if ref := conjunct.Properties[name]; ref != nil && ref.Value != nil {
			matches = append(matches, ref.Value)
		}
	}
	return resolveDeclaration(allOfSchema(matches), d.oas30)
}

func (d resolvedDeclaration) items() resolvedDeclaration {
	if d.ambiguous {
		return resolvedDeclaration{ambiguous: true, oas30: d.oas30}
	}
	var matches []*openapi3.Schema
	for _, conjunct := range d.conjuncts {
		if conjunct.Items != nil && conjunct.Items.Value != nil {
			matches = append(matches, conjunct.Items.Value)
		}
	}
	return resolveDeclaration(allOfSchema(matches), d.oas30)
}
