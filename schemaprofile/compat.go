package schemaprofile

// inputCompatible implements profile v0.1 input rules (interface schema ⊆ candidate schema).
func inputCompatible(tgt, cand map[string]any) (bool, error) {
	// Trivial schema: {} is Top.
	if len(cand) == 0 {
		return true, nil
	}
	return compat(tgt, cand, true)
}

// outputCompatible implements profile v0.1 output/payload rules (candidate schema ⊆ interface schema).
func outputCompatible(tgt, cand map[string]any) (bool, error) {
	// Trivial schema: {} is Top; allowed only if interface is also Top.
	if len(cand) == 0 {
		return len(tgt) == 0, nil
	}
	return compat(tgt, cand, false)
}

func compat(tgt, cand map[string]any, isInput bool) (bool, error) {
	// If either side is Top, handle per direction.
	if len(tgt) == 0 {
		// interface Top means interface accepts/emits anything.
		// Inputs: requires tgt ⊆ cand; Top ⊆ cand only if cand is also Top.
		// Outputs: requires cand ⊆ tgt; always true because everything ⊆ Top.
		if isInput {
			return len(cand) == 0, nil
		}
		return true, nil
	}
	if len(cand) == 0 {
		// candidate Top
		if isInput {
			return true, nil
		}
		return len(tgt) == 0, nil
	}

	// Type set rules.
	tgtTypes := typeSet(tgt)
	candTypes := typeSet(cand)
	if tgtTypes != nil || candTypes != nil {
		// Missing type means unconstrained; treat as all types.
		if isInput {
			// every type allowed by tgt must also be allowed by cand
			if !subsetTypes(tgtTypes, candTypes) {
				return false, nil
			}
		} else {
			// every type allowed by cand must also be allowed by tgt
			if !subsetTypes(candTypes, tgtTypes) {
				return false, nil
			}
		}
	}

	// const/enum rules.
	if ok, err := compatConstEnum(tgt, cand, isInput); err != nil || !ok {
		return ok, err
	}

	// Object rules if type includes object.
	if hasType(tgt, "object") || hasType(cand, "object") {
		ok, err := compatObject(tgt, cand, isInput)
		if err != nil || !ok {
			return ok, err
		}
	}

	// Array rules if type includes array.
	if hasType(tgt, "array") || hasType(cand, "array") {
		ok, err := compatArray(tgt, cand, isInput)
		if err != nil || !ok {
			return ok, err
		}
	}

	// Numeric bounds rules (when type includes number or integer).
	if hasType(tgt, "number") || hasType(tgt, "integer") || hasType(cand, "number") || hasType(cand, "integer") {
		ok, err := compatNumericBounds(tgt, cand, isInput)
		if err != nil || !ok {
			return ok, err
		}
	}

	// String bounds rules (when type includes string).
	if hasType(tgt, "string") || hasType(cand, "string") {
		ok, err := compatStringBounds(tgt, cand, isInput)
		if err != nil || !ok {
			return ok, err
		}
	}

	// Array bounds rules (when type includes array).
	if hasType(tgt, "array") || hasType(cand, "array") {
		ok, err := compatArrayBounds(tgt, cand, isInput)
		if err != nil || !ok {
			return ok, err
		}
	}

	// Union rules.
	if hasUnion(tgt) || hasUnion(cand) {
		ok, err := compatUnion(tgt, cand, isInput)
		if err != nil || !ok {
			return ok, err
		}
	}

	return true, nil
}

func typeSet(schema map[string]any) map[string]struct{} {
	v, ok := schema["type"]
	if !ok {
		return nil
	}
	arr, ok := asSlice(v)
	if !ok {
		// normalizer should guarantee array; treat as unknown
		return nil
	}
	set := map[string]struct{}{}
	for _, it := range arr {
		s, ok := it.(string)
		if !ok {
			continue
		}
		set[s] = struct{}{}
	}
	return set
}

func subsetTypes(a, b map[string]struct{}) bool {
	// nil means "all types".
	if a == nil {
		// all ⊆ b only if b is also all
		return b == nil
	}
	if b == nil {
		// a ⊆ all
		return true
	}
	for k := range a {
		if _, ok := b[k]; ok {
			continue
		}
		// integer ⊆ number: if a has "integer", b accepting "number" covers it.
		if k == "integer" {
			if _, ok := b["number"]; ok {
				continue
			}
		}
		return false
	}
	return true
}

func hasType(schema map[string]any, t string) bool {
	set := typeSet(schema)
	if set == nil {
		return false
	}
	_, ok := set[t]
	return ok
}

func hasUnion(schema map[string]any) bool {
	_, ok1 := schema["oneOf"]
	_, ok2 := schema["anyOf"]
	return ok1 || ok2
}

func compatConstEnum(tgt, cand map[string]any, isInput bool) (bool, error) {
	tgtConst, tgtHasConst := tgt["const"]
	candConst, candHasConst := cand["const"]
	tgtEnum, tgtHasEnum := enumSet(tgt)
	candEnum, candHasEnum := enumSet(cand)

	if isInput {
		// If tgt uses const, cand must accept it.
		if tgtHasConst {
			if candHasConst {
				return equalJSONValue(tgtConst, candConst), nil
			}
			if candHasEnum {
				_, ok := candEnum[canonicalKey(tgtConst)]
				return ok, nil
			}
			// cand unconstrained w.r.t const/enum
			return true, nil
		}
		// If tgt uses enum, cand must accept all values in tgt.
		if tgtHasEnum {
			if candHasConst {
				// single const must cover all enum values
				if len(tgtEnum) != 1 {
					return false, nil
				}
				_, ok := tgtEnum[canonicalKey(candConst)]
				return ok, nil
			}
			if candHasEnum {
				for k := range tgtEnum {
					if _, ok := candEnum[k]; !ok {
						return false, nil
					}
				}
				return true, nil
			}
			return true, nil
		}
		return true, nil
	}

	// Outputs:
	// If tgt uses enum, cand must only allow values within that enum (cand subset).
	if tgtHasEnum {
		if candHasConst {
			_, ok := tgtEnum[canonicalKey(candConst)]
			return ok, nil
		}
		if candHasEnum {
			for k := range candEnum {
				if _, ok := tgtEnum[k]; !ok {
					return false, nil
				}
			}
			return true, nil
		}
		// cand unconstrained but tgt constrained -> can emit values outside
		return false, nil
	}
	// If tgt uses const, cand must only allow that constant.
	if tgtHasConst {
		if candHasConst {
			return equalJSONValue(tgtConst, candConst), nil
		}
		if candHasEnum {
			if len(candEnum) != 1 {
				return false, nil
			}
			_, ok := candEnum[canonicalKey(tgtConst)]
			return ok, nil
		}
		// cand unconstrained but tgt const -> can emit others
		return false, nil
	}
	// tgt unconstrained: ok
	return true, nil
}

func enumSet(schema map[string]any) (map[string]struct{}, bool) {
	v, ok := schema["enum"]
	if !ok {
		return nil, false
	}
	arr, ok := asSlice(v)
	if !ok {
		return nil, true
	}
	set := map[string]struct{}{}
	for _, it := range arr {
		set[canonicalKey(it)] = struct{}{}
	}
	return set, true
}

func compatObject(tgt, cand map[string]any, isInput bool) (bool, error) {
	tgtReq := stringSet(tgt["required"])
	candReq := stringSet(cand["required"])

	tgtProps, _ := asMap(tgt["properties"])
	candProps, _ := asMap(cand["properties"])

	if isInput {
		// required(cand) ⊆ required(tgt)
		for k := range candReq {
			if _, ok := tgtReq[k]; !ok {
				return false, nil
			}
		}
		// For each p in properties(tgt):
		for p, tv := range tgtProps {
			tvm, ok := asMap(tv)
			if !ok {
				continue
			}
			if cv, ok := candProps[p]; ok {
				cvm, ok := asMap(cv)
				if !ok {
					continue
				}
				ok2, err := compat(tvm, cvm, true)
				if err != nil || !ok2 {
					return ok2, err
				}
			}
			// If cand lacks property schema, treated as unconstrained (compatible).
		}
		// additionalProperties does not restrict input compatibility in v0.1.
		return true, nil
	}

	// Outputs/payloads:
	// required(tgt) ⊆ required(cand)
	for k := range tgtReq {
		if _, ok := candReq[k]; !ok {
			return false, nil
		}
	}

	tgtAP := tgt["additionalProperties"]

	// For each property p in properties(cand):
	for p, cv := range candProps {
		// If p not in properties(tgt), then additionalProperties(tgt) MUST NOT be false.
		if _, ok := tgtProps[p]; !ok {
			if b, ok := tgtAP.(bool); ok && b == false {
				return false, nil
			}
		}
		// If both present, OutputCompatible must hold.
		if tv, ok := tgtProps[p]; ok {
			tvm, ok := asMap(tv)
			if !ok {
				continue
			}
			cvm, ok := asMap(cv)
			if !ok {
				continue
			}
			ok2, err := compat(tvm, cvm, false)
			if err != nil || !ok2 {
				return ok2, err
			}
		}
	}

	// additionalProperties constraint:
	switch apTgt := tgtAP.(type) {
	case bool:
		if apTgt == false {
			if apCand, ok := cand["additionalProperties"].(bool); ok {
				return apCand == false, nil
			}
			// if cand schema or missing, it's not guaranteed false
			return false, nil
		}
	case map[string]any:
		if apCand, ok := cand["additionalProperties"].(map[string]any); ok {
			ok2, err := compat(apTgt, apCand, false)
			if err != nil || !ok2 {
				return ok2, err
			}
		} else if apCand, ok := cand["additionalProperties"].(bool); ok && apCand == false {
			// cand is false: more restrictive than tgt schema, allowed for output.
			return true, nil
		} else {
			// cand AP is true or absent: less restrictive than tgt schema constraint.
			return false, nil
		}
	}

	return true, nil
}

func compatArray(tgt, cand map[string]any, isInput bool) (bool, error) {
	tv, okTgt := asMap(tgt["items"])
	cv, okCand := asMap(cand["items"])
	if !okTgt || !okCand {
		// If one side lacks items, treat as Top for items.
		if !okTgt {
			tv = map[string]any{}
		}
		if !okCand {
			cv = map[string]any{}
		}
	}
	return compat(tv, cv, isInput)
}

func compatUnion(tgt, cand map[string]any, isInput bool) (bool, error) {
	tgtVars, okTgt := unionVariants(tgt)
	candVars, okCand := unionVariants(cand)
	if !okTgt || !okCand {
		// If only one side is a union, profile doesn't define cross-form rules; treat as incompatible.
		return false, nil
	}

	if isInput {
		// For every v in tgt, exists w in cand such that InputCompatible(v,w).
		for _, v := range tgtVars {
			found := false
			for _, w := range candVars {
				ok, err := compat(v, w, true)
				if err != nil {
					return false, err
				}
				if ok {
					found = true
					break
				}
			}
			if !found {
				return false, nil
			}
		}
		return true, nil
	}

	// Outputs/payloads:
	// For every w in cand, exists v in tgt such that OutputCompatible(v,w).
	for _, w := range candVars {
		found := false
		for _, v := range tgtVars {
			ok, err := compat(v, w, false)
			if err != nil {
				return false, err
			}
			if ok {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

func unionVariants(schema map[string]any) ([]map[string]any, bool) {
	var key string
	if _, ok := schema["oneOf"]; ok {
		key = "oneOf"
	} else if _, ok := schema["anyOf"]; ok {
		key = "anyOf"
	} else {
		return nil, false
	}
	arr, ok := asSlice(schema[key])
	if !ok {
		return nil, false // malformed union value; treat as non-union
	}
	out := make([]map[string]any, 0, len(arr))
	for _, it := range arr {
		m, ok := asMap(it)
		if !ok {
			return nil, false // malformed variant; treat as non-union
		}
		out = append(out, m)
	}
	return out, true
}

func stringSet(v any) map[string]struct{} {
	arr, ok := asSlice(v)
	if !ok {
		return map[string]struct{}{}
	}
	set := map[string]struct{}{}
	for _, it := range arr {
		s, ok := it.(string)
		if !ok {
			continue
		}
		set[s] = struct{}{}
	}
	return set
}

func canonicalKey(v any) string {
	// Use JCS for stable equivalence keys across primitive/object representations.
	s, err := CanonicalString(v)
	if err != nil {
		// As a fallback, use a best-effort string.
		return "<unserializable>"
	}
	return s
}

func equalJSONValue(a, b any) bool {
	return canonicalKey(a) == canonicalKey(b)
}

// compatNumericBounds checks minimum/maximum/exclusiveMinimum/exclusiveMaximum rules.
func compatNumericBounds(tgt, cand map[string]any, isInput bool) (bool, error) {
	// Lower bounds: minimum / exclusiveMinimum
	tgtLo, tgtLoExcl := effectiveLowerBound(tgt)
	candLo, candLoExcl := effectiveLowerBound(cand)
	tgtHi, tgtHiExcl := effectiveUpperBound(tgt)
	candHi, candHiExcl := effectiveUpperBound(cand)

	tgtHasLo := hasKey(tgt, "minimum") || hasKey(tgt, "exclusiveMinimum")
	tgtHasHi := hasKey(tgt, "maximum") || hasKey(tgt, "exclusiveMaximum")
	candHasLo := hasKey(cand, "minimum") || hasKey(cand, "exclusiveMinimum")
	candHasHi := hasKey(cand, "maximum") || hasKey(cand, "exclusiveMaximum")

	if isInput {
		// cand's lower bound MUST be ≤ tgt's (accept at least as low).
		// If cand has no bound, unconstrained (compatible).
		if tgtHasLo && candHasLo {
			if !lowerBoundLessOrEqual(candLo, candLoExcl, tgtLo, tgtLoExcl) {
				return false, nil
			}
		}
		// cand's upper bound MUST be ≥ tgt's (accept at least as high).
		if tgtHasHi && candHasHi {
			if !upperBoundGreaterOrEqual(candHi, candHiExcl, tgtHi, tgtHiExcl) {
				return false, nil
			}
		}
	} else {
		// cand's lower bound MUST be ≥ tgt's (return no lower).
		// If cand has no bound where tgt has one → incompatible.
		if tgtHasLo {
			if !candHasLo {
				return false, nil
			}
			if !lowerBoundGreaterOrEqual(candLo, candLoExcl, tgtLo, tgtLoExcl) {
				return false, nil
			}
		}
		// cand's upper bound MUST be ≤ tgt's (return no higher).
		if tgtHasHi {
			if !candHasHi {
				return false, nil
			}
			if !upperBoundLessOrEqual(candHi, candHiExcl, tgtHi, tgtHiExcl) {
				return false, nil
			}
		}
	}
	return true, nil
}

// effectiveLowerBound returns the effective lower bound value and whether it's exclusive.
func effectiveLowerBound(schema map[string]any) (float64, bool) {
	min, hasMin := schema["minimum"]
	eMin, hasEMin := schema["exclusiveMinimum"]
	if hasMin && hasEMin {
		mv := toFloat64(min)
		ev := toFloat64(eMin)
		if ev >= mv {
			return ev, true
		}
		return mv, false
	}
	if hasEMin {
		return toFloat64(eMin), true
	}
	if hasMin {
		return toFloat64(min), false
	}
	return 0, false
}

// effectiveUpperBound returns the effective upper bound value and whether it's exclusive.
func effectiveUpperBound(schema map[string]any) (float64, bool) {
	max, hasMax := schema["maximum"]
	eMax, hasEMax := schema["exclusiveMaximum"]
	if hasMax && hasEMax {
		mv := toFloat64(max)
		ev := toFloat64(eMax)
		if ev <= mv {
			return ev, true
		}
		return mv, false
	}
	if hasEMax {
		return toFloat64(eMax), true
	}
	if hasMax {
		return toFloat64(max), false
	}
	return 0, false
}

// Lower bound comparisons:
// For lower bounds, exclusive means the bound is HIGHER (stricter).
// exclusiveMinimum: 0 means > 0, while minimum: 0 means >= 0.
// So at equal values: exclusive > non-exclusive.

// lowerBoundLessOrEqual returns true if lower bound a ≤ lower bound b.
func lowerBoundLessOrEqual(a float64, aExcl bool, b float64, bExcl bool) bool {
	if a < b {
		return true
	}
	if a > b {
		return false
	}
	// Equal values: exclusive is stricter (higher)
	if aExcl && !bExcl {
		return false // a is higher (stricter), so a > b
	}
	return true
}

// lowerBoundGreaterOrEqual returns true if lower bound a ≥ lower bound b.
func lowerBoundGreaterOrEqual(a float64, aExcl bool, b float64, bExcl bool) bool {
	if a > b {
		return true
	}
	if a < b {
		return false
	}
	// Equal values: exclusive is stricter (higher)
	if bExcl && !aExcl {
		return false // b is higher (stricter), so a < b
	}
	return true
}

// Upper bound comparisons:
// For upper bounds, exclusive means the bound is LOWER (stricter).
// exclusiveMaximum: 100 means < 100, while maximum: 100 means <= 100.
// So at equal values: exclusive < non-exclusive.

// upperBoundLessOrEqual returns true if upper bound a ≤ upper bound b.
func upperBoundLessOrEqual(a float64, aExcl bool, b float64, bExcl bool) bool {
	if a < b {
		return true
	}
	if a > b {
		return false
	}
	// Equal values: exclusive is stricter (lower)
	if bExcl && !aExcl {
		return false // b is lower (stricter), so a > b
	}
	return true
}

// upperBoundGreaterOrEqual returns true if upper bound a ≥ upper bound b.
func upperBoundGreaterOrEqual(a float64, aExcl bool, b float64, bExcl bool) bool {
	if a > b {
		return true
	}
	if a < b {
		return false
	}
	// Equal values: exclusive is stricter (lower)
	if aExcl && !bExcl {
		return false // a is lower (stricter), so a < b
	}
	return true
}

// compatSimpleBounds checks a pair of min/max keywords that use simple integer
// comparisons (no exclusivity). Used for minLength/maxLength and minItems/maxItems.
func compatSimpleBounds(tgt, cand map[string]any, isInput bool, minKey, maxKey string) (bool, error) {
	if isInput {
		// min(cand) ≤ min(tgt). Absent cand = unconstrained (compatible).
		if hasKey(tgt, minKey) && hasKey(cand, minKey) {
			if toFloat64(cand[minKey]) > toFloat64(tgt[minKey]) {
				return false, nil
			}
		}
		// max(cand) ≥ max(tgt). Absent cand = unconstrained (compatible).
		if hasKey(tgt, maxKey) && hasKey(cand, maxKey) {
			if toFloat64(cand[maxKey]) < toFloat64(tgt[maxKey]) {
				return false, nil
			}
		}
	} else {
		// min(cand) ≥ min(tgt). Absent cand when tgt present = incompatible.
		if hasKey(tgt, minKey) {
			if !hasKey(cand, minKey) {
				return false, nil
			}
			if toFloat64(cand[minKey]) < toFloat64(tgt[minKey]) {
				return false, nil
			}
		}
		// max(cand) ≤ max(tgt). Absent cand when tgt present = incompatible.
		if hasKey(tgt, maxKey) {
			if !hasKey(cand, maxKey) {
				return false, nil
			}
			if toFloat64(cand[maxKey]) > toFloat64(tgt[maxKey]) {
				return false, nil
			}
		}
	}
	return true, nil
}

// compatStringBounds checks minLength/maxLength rules.
func compatStringBounds(tgt, cand map[string]any, isInput bool) (bool, error) {
	return compatSimpleBounds(tgt, cand, isInput, "minLength", "maxLength")
}

// compatArrayBounds checks minItems/maxItems rules.
func compatArrayBounds(tgt, cand map[string]any, isInput bool) (bool, error) {
	return compatSimpleBounds(tgt, cand, isInput, "minItems", "maxItems")
}

func hasKey(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}
