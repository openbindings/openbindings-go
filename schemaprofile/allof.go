package schemaprofile

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// flattenAllOf merges all branches of an allOf into a single schema.
func (n *Normalizer) flattenAllOf(allOf any, path string) (map[string]any, error) {
	arr, ok := asSlice(allOf)
	if !ok {
		return nil, fmt.Errorf("%s.allOf: must be array", pathOrRoot(path))
	}
	if len(arr) == 0 {
		return map[string]any{}, nil
	}

	merged := map[string]any{}
	for idx, item := range arr {
		branch, ok := asMap(item)
		if !ok {
			return nil, fmt.Errorf("%s.allOf[%d]: must be object", pathOrRoot(path), idx)
		}

		branchPath := ptrJoin(path, fmt.Sprintf("allOf[%d]", idx))

		// Check for out-of-profile keywords in branch.
		if err := assertProfileKeywords(branch, branchPath); err != nil {
			return nil, err
		}

		// oneOf/anyOf inside allOf branch: fail closed.
		if _, ok := branch["oneOf"]; ok {
			return nil, &OutsideProfileError{Path: branchPath, Keyword: "oneOf inside allOf"}
		}
		if _, ok := branch["anyOf"]; ok {
			return nil, &OutsideProfileError{Path: branchPath, Keyword: "anyOf inside allOf"}
		}

		// Resolve $ref in branch first.
		if ref, ok := branch["$ref"].(string); ok && strings.TrimSpace(ref) != "" {
			resolved, cleanup, err := n.resolveRef(ref, branchPath)
			if err != nil {
				return nil, err
			}
			cleanup() // allOf branches are merged, not recursively normalized via this ref
			rm, ok := asMap(resolved)
			if !ok {
				return nil, &RefError{Path: branchPath, Ref: ref, Err: errors.New("resolved $ref is not an object")}
			}
			branch = rm
		}

		if err := mergeAllOfBranch(merged, branch, branchPath); err != nil {
			return nil, err
		}
	}

	return merged, nil
}

// mergeAllOfBranch merges a single allOf branch into the accumulator.
//
// Keywords handled (in order):
//   - type:                  intersection (with integer ⊆ number subtype rule)
//   - properties:            union of keys; recursive merge for overlapping keys
//   - required:              union
//   - additionalProperties:  false wins; schemas merge recursively
//   - enum:                  intersection (empty → SchemaError)
//   - const:                 conflict → SchemaError
//   - items:                 recursive merge
//   - bounds:                most restrictive wins (min↑, max↓)
func mergeAllOfBranch(acc, branch map[string]any, path string) error {
	// type: intersection
	if bt, ok := branch["type"]; ok {
		bTypes, err := normalizeType(bt)
		if err != nil {
			return fmt.Errorf("%s.type: %w", path, err)
		}
		if at, ok := acc["type"]; ok {
			// acc type may be raw string or already normalized to []any
			aTypes, err := normalizeType(at)
			if err != nil {
				return fmt.Errorf("%s.type: %w", path, err)
			}
			inter := intersectTypeSlices(aTypes, bTypes)
			if len(inter) == 0 {
				return &SchemaError{Path: path, Message: "allOf type intersection is empty"}
			}
			acc["type"] = inter
		} else {
			acc["type"] = bTypes
		}
	}

	// properties: union, recursive merge for overlapping keys
	if bp, ok := branch["properties"]; ok {
		bProps, ok := asMap(bp)
		if !ok {
			return fmt.Errorf("%s.properties: must be object", path)
		}
		aProps, _ := asMap(acc["properties"])
		if aProps == nil {
			aProps = map[string]any{}
		}
		for k, bv := range bProps {
			if av, exists := aProps[k]; exists {
				avm, _ := asMap(av)
				bvm, _ := asMap(bv)
				if avm == nil {
					avm = map[string]any{}
				}
				if bvm == nil {
					bvm = map[string]any{}
				}
				merged := cloneMap(avm)
				if err := mergeAllOfBranch(merged, bvm, path+".properties[\""+k+"\"]"); err != nil {
					return err
				}
				aProps[k] = merged
			} else {
				aProps[k] = bv
			}
		}
		acc["properties"] = aProps
	}

	// required: union
	if br, ok := branch["required"]; ok {
		bReq, err := normalizeStringSet(br)
		if err != nil {
			return fmt.Errorf("%s.required: %w", path, err)
		}
		if ar, ok := acc["required"]; ok {
			// acc required may be raw or already normalized
			aReq, err := normalizeStringSet(ar)
			if err != nil {
				return fmt.Errorf("%s.required: %w", path, err)
			}
			acc["required"] = unionStringSlices(aReq, bReq)
		} else {
			acc["required"] = bReq
		}
	}

	// additionalProperties: false wins; schemas merge recursively
	if bap, ok := branch["additionalProperties"]; ok {
		switch bv := bap.(type) {
		case bool:
			if !bv {
				acc["additionalProperties"] = false
			} else if _, exists := acc["additionalProperties"]; !exists {
				acc["additionalProperties"] = true
			}
		case map[string]any:
			if aap, ok := acc["additionalProperties"]; ok {
				switch av := aap.(type) {
				case bool:
					if !av {
						// false wins
					} else {
						acc["additionalProperties"] = bv
					}
				case map[string]any:
					merged := cloneMap(av)
					if err := mergeAllOfBranch(merged, bv, path+".additionalProperties"); err != nil {
						return err
					}
					acc["additionalProperties"] = merged
				}
			} else {
				acc["additionalProperties"] = bv
			}
		}
	}

	// enum/const: intersection
	if be, ok := branch["enum"]; ok {
		bEnum, ok := asSlice(be)
		if !ok {
			return fmt.Errorf("%s.enum: must be array", path)
		}
		if ae, ok := acc["enum"]; ok {
			aEnum, _ := asSlice(ae)
			inter := intersectValues(aEnum, bEnum)
			if len(inter) == 0 {
				return &SchemaError{Path: path, Message: "allOf enum intersection is empty"}
			}
			acc["enum"] = inter
		} else {
			acc["enum"] = bEnum
		}
	}

	if bc, ok := branch["const"]; ok {
		if ac, ok := acc["const"]; ok {
			if canonicalKey(ac) != canonicalKey(bc) {
				return &SchemaError{Path: path, Message: "allOf const conflict"}
			}
		} else {
			acc["const"] = bc
		}
	}

	// items: recursive merge
	if bi, ok := branch["items"]; ok {
		bItems, ok := asMap(bi)
		if !ok {
			return fmt.Errorf("%s.items: must be object", path)
		}
		if ai, ok := acc["items"]; ok {
			aItems, _ := asMap(ai)
			if aItems == nil {
				aItems = map[string]any{}
			}
			merged := cloneMap(aItems)
			if err := mergeAllOfBranch(merged, bItems, path+".items"); err != nil {
				return err
			}
			acc["items"] = merged
		} else {
			acc["items"] = bItems
		}
	}

	// Numeric/string/array bounds: most restrictive wins.
	// Lower bounds: take the highest (most restrictive)
	for _, k := range []string{"minimum", "exclusiveMinimum", "minLength", "minItems"} {
		if bv, ok := branch[k]; ok {
			bf := toFloat64(bv)
			if av, ok := acc[k]; ok {
				af := toFloat64(av)
				if bf > af {
					acc[k] = bv
				}
			} else {
				acc[k] = bv
			}
		}
	}
	// Upper bounds: take the lowest (most restrictive)
	for _, k := range []string{"maximum", "exclusiveMaximum", "maxLength", "maxItems"} {
		if bv, ok := branch[k]; ok {
			bf := toFloat64(bv)
			if av, ok := acc[k]; ok {
				af := toFloat64(av)
				if bf < af {
					acc[k] = bv
				}
			} else {
				acc[k] = bv
			}
		}
	}

	return nil
}

// intersectTypeSlices computes the intersection of two type sets, accounting for
// the JSON Schema rule that "integer" is a subtype of "number".
//
// Each side's numeric acceptance is classified as:
//   - "number" present → accepts all numeric values (integers included)
//   - "integer" present (without "number") → accepts only integers
//   - neither → does not accept numeric values
//
// The intersection of numeric acceptance is then:
//   - both "number" → result includes "number"
//   - one "number" + one "integer" → result includes "integer" (the narrower type)
//   - both "integer" → result includes "integer"
//   - either "none" → no numeric type in result
//
// Non-numeric types are intersected with a plain set intersection.
func intersectTypeSlices(a, b []any) []any {
	aSet := map[string]struct{}{}
	for _, v := range a {
		if s, ok := v.(string); ok {
			aSet[s] = struct{}{}
		}
	}
	bSet := map[string]struct{}{}
	for _, v := range b {
		if s, ok := v.(string); ok {
			bSet[s] = struct{}{}
		}
	}

	result := map[string]struct{}{}

	// Direct intersection of non-numeric types.
	for s := range aSet {
		if s == "number" || s == "integer" {
			continue
		}
		if _, ok := bSet[s]; ok {
			result[s] = struct{}{}
		}
	}

	// Classify each side's numeric acceptance level.
	_, aNum := aSet["number"]
	_, bNum := bSet["number"]
	_, aInt := aSet["integer"]
	_, bInt := bSet["integer"]

	aAcceptsNumbers := aNum || aInt // side A accepts some numeric values
	bAcceptsNumbers := bNum || bInt // side B accepts some numeric values

	if aAcceptsNumbers && bAcceptsNumbers {
		if aNum && bNum {
			// Both accept all numbers → result is "number".
			result["number"] = struct{}{}
		} else {
			// At least one side is integer-only → narrow to "integer".
			result["integer"] = struct{}{}
		}
	}

	out := make([]any, 0, len(result))
	for s := range result {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].(string) < out[j].(string) })
	return out
}

func unionStringSlices(a, b []any) []any {
	set := map[string]struct{}{}
	for _, v := range a {
		if s, ok := v.(string); ok {
			set[s] = struct{}{}
		}
	}
	for _, v := range b {
		if s, ok := v.(string); ok {
			set[s] = struct{}{}
		}
	}
	out := make([]any, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].(string) < out[j].(string) })
	return out
}

func intersectValues(a, b []any) []any {
	bSet := map[string]any{}
	for _, v := range b {
		bSet[canonicalKey(v)] = v
	}
	var out []any
	for _, v := range a {
		if _, ok := bSet[canonicalKey(v)]; ok {
			out = append(out, v)
		}
	}
	return out
}
