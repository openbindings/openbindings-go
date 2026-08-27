package openapi

import (
	"sort"

	"github.com/dlclark/regexp2"
	"github.com/getkin/kin-openapi/openapi3"
)

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
		// Preserve the false literal as an explicit unsatisfiable conjunct.
		// It has no lane and must not collapse into the same typeless view as
		// an omitted or assertion-free declaration.
		return []*openapi3.Schema{schema}, false
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

func (d resolvedDeclaration) typeless() bool {
	if d.ambiguous || len(d.types) != 0 {
		return false
	}
	for _, conjunct := range d.conjuncts {
		if literal, boolean := booleanSchemaLiteral(conjunct); boolean && !literal {
			return false
		}
		if conjunct != nil && conjunct.Type != nil {
			// An explicit empty type set, or mutually contradictory conjoined
			// type sets, admits no instance. It is not an absent declaration.
			return false
		}
	}
	return true
}

func (d resolvedDeclaration) admitsNull() bool {
	return !d.ambiguous && d.types["null"]
}

func (d resolvedDeclaration) soleNonNullType() (string, bool) {
	if d.ambiguous || len(d.types) == 0 {
		return "", false
	}
	member := ""
	for candidate := range d.types {
		if candidate == "null" {
			continue
		}
		if member != "" {
			return "", false
		}
		member = candidate
	}
	return member, member != ""
}

// format returns the one declaration-level format contributed by the
// resolved conjuncts. Conflicting values have no single carriage meaning.
func (d resolvedDeclaration) format() (string, bool) {
	values := map[string]bool{}
	for _, conjunct := range d.conjuncts {
		if conjunct != nil && conjunct.Format != "" {
			values[conjunct.Format] = true
		}
	}
	if len(values) > 1 {
		return "", true
	}
	for value := range values {
		return value, false
	}
	return "", false
}

// keywordString returns the one resolved string annotation used by §9.
// OAS 3.0 callers deliberately do not consult contentEncoding or
// contentMediaType because those keywords are outside that line's dialect.
func (d resolvedDeclaration) keywordString(key string) (string, bool) {
	if d.oas30 && (key == "contentEncoding" || key == "contentMediaType") {
		return "", false
	}
	values := map[string]bool{}
	for _, conjunct := range d.conjuncts {
		if conjunct == nil {
			continue
		}
		var value string
		switch key {
		case "contentEncoding":
			value = conjunct.ContentEncoding
		case "contentMediaType":
			value = conjunct.ContentMediaType
		}
		if value == "" && conjunct.Extensions != nil {
			value, _ = conjunct.Extensions[key].(string)
		}
		if value != "" {
			values[value] = true
		}
	}
	if len(values) > 1 {
		return "", true
	}
	for value := range values {
		return value, false
	}
	return "", false
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
		matched := false
		if ref := conjunct.Properties[name]; ref != nil && ref.Value != nil {
			matches = append(matches, ref.Value)
			matched = true
		}
		if !d.oas30 {
			patterns := make([]string, 0, len(conjunct.PatternProperties))
			for pattern := range conjunct.PatternProperties {
				patterns = append(patterns, pattern)
			}
			sort.Strings(patterns)
			for _, pattern := range patterns {
				re, err := regexp2.Compile(pattern, regexp2.ECMAScript)
				if err != nil {
					continue
				}
				ok, err := re.MatchString(name)
				if err != nil || !ok {
					continue
				}
				matched = true
				if ref := conjunct.PatternProperties[pattern]; ref != nil && ref.Value != nil {
					matches = append(matches, ref.Value)
				}
			}
		}
		if matched {
			continue
		}
		switch {
		case conjunct.AdditionalProperties.Schema != nil && conjunct.AdditionalProperties.Schema.Value != nil:
			if literal, boolean := booleanSchemaLiteral(conjunct.AdditionalProperties.Schema.Value); !boolean || literal {
				matches = append(matches, conjunct.AdditionalProperties.Schema.Value)
			}
		case conjunct.AdditionalProperties.Has == nil || *conjunct.AdditionalProperties.Has:
			matches = append(matches, &openapi3.Schema{})
		}
	}
	return resolveDeclaration(allOfSchema(matches), d.oas30)
}

func (d resolvedDeclaration) requiresProperty(name string) bool {
	if d.ambiguous {
		return false
	}
	for _, conjunct := range d.conjuncts {
		for _, required := range conjunct.Required {
			if required == name {
				return true
			}
		}
	}
	return false
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
