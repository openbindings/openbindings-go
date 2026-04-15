package schemaprofile

import "fmt"

// inputCompatible implements profile v0.1 input rules (interface schema <= candidate schema).
func inputCompatible(tgt, cand map[string]any) (bool, string, error) {
	// Trivial schema: {} is Top.
	if len(cand) == 0 {
		return true, "", nil
	}
	return compat(tgt, cand, true)
}

// outputCompatible implements profile v0.1 output/payload rules (candidate schema <= interface schema).
func outputCompatible(tgt, cand map[string]any) (bool, string, error) {
	// Trivial schema: {} is Top; allowed only if interface is also Top.
	if len(cand) == 0 {
		if len(tgt) == 0 {
			return true, "", nil
		}
		return false, "candidate is unconstrained but target is not", nil
	}
	return compat(tgt, cand, false)
}

func compat(tgt, cand map[string]any, isInput bool) (bool, string, error) {
	// If either side is Top, handle per direction.
	if len(tgt) == 0 {
		// Empty target ({}) is Top — "could send/receive anything".
		// For input:  the candidate must also be unconstrained, because the interface may
		//             send any value and the candidate must accept it all.  A narrower
		//             candidate (len > 0) cannot cover the full Top domain → incompatible.
		// For output: any candidate is a subset of Top, so always compatible.
		if isInput && len(cand) > 0 {
			return false, "candidate is constrained but target is unconstrained (Top)", nil
		}
		return true, "", nil
	}
	if len(cand) == 0 {
		// candidate Top
		if isInput {
			return true, "", nil
		}
		if len(tgt) == 0 {
			return true, "", nil
		}
		return false, "candidate is unconstrained but target is not", nil
	}

	// Type set rules.
	tgtTypes := typeSet(tgt)
	candTypes := typeSet(cand)
	if tgtTypes != nil || candTypes != nil {
		// Missing type means unconstrained; treat as all types.
		if isInput {
			// every type allowed by tgt must also be allowed by cand
			if !subsetTypes(tgtTypes, candTypes) {
				missing := missingTypes(tgtTypes, candTypes)
				return false, fmt.Sprintf("type: candidate does not allow %s", missing), nil
			}
		} else {
			// every type allowed by cand must also be allowed by tgt
			if !subsetTypes(candTypes, tgtTypes) {
				missing := missingTypes(candTypes, tgtTypes)
				return false, fmt.Sprintf("type: candidate allows %s but target does not", missing), nil
			}
		}
	}

	// const/enum rules.
	if ok, reason := compatConstEnum(tgt, cand, isInput); !ok {
		return false, reason, nil
	}

	// Object rules if type includes object.
	if hasType(tgt, "object") || hasType(cand, "object") {
		ok, reason := compatObject(tgt, cand, isInput)
		if !ok {
			return false, reason, nil
		}
	}

	// Array rules if type includes array.
	if hasType(tgt, "array") || hasType(cand, "array") {
		ok, reason := compatArray(tgt, cand, isInput)
		if !ok {
			return false, reason, nil
		}
	}

	// Numeric bounds rules (when type includes number or integer).
	if hasType(tgt, "number") || hasType(tgt, "integer") || hasType(cand, "number") || hasType(cand, "integer") {
		ok, reason := compatNumericBounds(tgt, cand, isInput)
		if !ok {
			return false, reason, nil
		}
	}

	// String bounds rules (when type includes string).
	if hasType(tgt, "string") || hasType(cand, "string") {
		ok, reason := compatStringBounds(tgt, cand, isInput)
		if !ok {
			return false, reason, nil
		}
	}

	// Array bounds rules (when type includes array).
	if hasType(tgt, "array") || hasType(cand, "array") {
		ok, reason := compatArrayBounds(tgt, cand, isInput)
		if !ok {
			return false, reason, nil
		}
	}

	// Union rules.
	if hasUnion(tgt) || hasUnion(cand) {
		ok, reason := compatUnion(tgt, cand, isInput)
		if !ok {
			return false, reason, nil
		}
	}

	return true, "", nil
}

// missingTypes returns a quoted comma-separated list of types in a that are not in b.
func missingTypes(a, b map[string]struct{}) string {
	if a == nil {
		return "all types"
	}
	var missing []string
	for k := range a {
		if b == nil {
			missing = append(missing, fmt.Sprintf("%q", k))
			continue
		}
		if _, ok := b[k]; ok {
			continue
		}
		if k == "integer" {
			if _, ok := b["number"]; ok {
				continue
			}
		}
		missing = append(missing, fmt.Sprintf("%q", k))
	}
	if len(missing) == 1 {
		return missing[0]
	}
	result := missing[0]
	for _, m := range missing[1:] {
		result += ", " + m
	}
	return result
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
		// all <= b only if b is also all
		return b == nil
	}
	if b == nil {
		// a <= all
		return true
	}
	for k := range a {
		if _, ok := b[k]; ok {
			continue
		}
		// integer <= number: if a has "integer", b accepting "number" covers it.
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

func compatConstEnum(tgt, cand map[string]any, isInput bool) (bool, string) {
	tgtConst, tgtHasConst := tgt["const"]
	candConst, candHasConst := cand["const"]
	tgtEnum, tgtHasEnum := enumSet(tgt)
	candEnum, candHasEnum := enumSet(cand)

	if isInput {
		// If tgt uses const, cand must accept it.
		if tgtHasConst {
			if candHasConst {
				if !equalJSONValue(tgtConst, candConst) {
					return false, fmt.Sprintf("const: candidate const %s does not match target const %s", canonicalKey(candConst), canonicalKey(tgtConst))
				}
				return true, ""
			}
			if candHasEnum {
				_, ok := candEnum[canonicalKey(tgtConst)]
				if !ok {
					return false, fmt.Sprintf("const: target const %s not in candidate enum", canonicalKey(tgtConst))
				}
				return true, ""
			}
			// cand unconstrained w.r.t const/enum
			return true, ""
		}
		// If tgt uses enum, cand must accept all values in tgt.
		if tgtHasEnum {
			if candHasConst {
				// single const must cover all enum values
				if len(tgtEnum) != 1 {
					return false, fmt.Sprintf("enum: candidate const %s cannot cover %d target enum values", canonicalKey(candConst), len(tgtEnum))
				}
				_, ok := tgtEnum[canonicalKey(candConst)]
				if !ok {
					return false, fmt.Sprintf("enum: candidate const %s not in target enum", canonicalKey(candConst))
				}
				return true, ""
			}
			if candHasEnum {
				for k := range tgtEnum {
					if _, ok := candEnum[k]; !ok {
						return false, fmt.Sprintf("enum: target value %s not in candidate enum", k)
					}
				}
				return true, ""
			}
			return true, ""
		}
		return true, ""
	}

	// Outputs:
	// If tgt uses enum, cand must only allow values within that enum (cand subset).
	if tgtHasEnum {
		if candHasConst {
			_, ok := tgtEnum[canonicalKey(candConst)]
			if !ok {
				return false, fmt.Sprintf("const: candidate const %s not in target enum", canonicalKey(candConst))
			}
			return true, ""
		}
		if candHasEnum {
			for k := range candEnum {
				if _, ok := tgtEnum[k]; !ok {
					return false, fmt.Sprintf("enum: candidate value %s not in target enum", k)
				}
			}
			return true, ""
		}
		// cand unconstrained but tgt constrained -> can emit values outside
		return false, "enum: candidate is unconstrained but target has enum"
	}
	// If tgt uses const, cand must only allow that constant.
	if tgtHasConst {
		if candHasConst {
			if !equalJSONValue(tgtConst, candConst) {
				return false, fmt.Sprintf("const: candidate const %s does not match target const %s", canonicalKey(candConst), canonicalKey(tgtConst))
			}
			return true, ""
		}
		if candHasEnum {
			if len(candEnum) != 1 {
				return false, fmt.Sprintf("const: candidate enum has %d values but target allows only const %s", len(candEnum), canonicalKey(tgtConst))
			}
			_, ok := candEnum[canonicalKey(tgtConst)]
			if !ok {
				return false, fmt.Sprintf("const: candidate enum value does not match target const %s", canonicalKey(tgtConst))
			}
			return true, ""
		}
		// cand unconstrained but tgt const -> can emit others
		return false, fmt.Sprintf("const: candidate is unconstrained but target requires const %s", canonicalKey(tgtConst))
	}
	// tgt unconstrained: ok
	return true, ""
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

func compatObject(tgt, cand map[string]any, isInput bool) (bool, string) {
	tgtReq := stringSet(tgt["required"])
	candReq := stringSet(cand["required"])

	tgtProps, _ := asMap(tgt["properties"])
	candProps, _ := asMap(cand["properties"])

	if isInput {
		// required(cand) <= required(tgt)
		for k := range candReq {
			if _, ok := tgtReq[k]; !ok {
				return false, fmt.Sprintf("required: candidate requires %q but target does not", k)
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
				ok2, reason, err := compat(tvm, cvm, true)
				if err != nil {
					// Wrap error as reason (should not happen in practice).
					return false, fmt.Sprintf("properties[%q]: error: %v", p, err)
				}
				if !ok2 {
					return false, fmt.Sprintf("properties[%q]: %s", p, reason)
				}
			}
			// If cand lacks property schema, treated as unconstrained (compatible).
		}
		// additionalProperties does not restrict input compatibility in v0.1.
		return true, ""
	}

	// Outputs/payloads:
	// required(tgt) <= required(cand)
	for k := range tgtReq {
		if _, ok := candReq[k]; !ok {
			return false, fmt.Sprintf("required: target requires %q but candidate does not", k)
		}
	}

	tgtAP := tgt["additionalProperties"]

	// For each property p in properties(cand):
	for p, cv := range candProps {
		// If p not in properties(tgt), then additionalProperties(tgt) MUST NOT be false.
		if _, ok := tgtProps[p]; !ok {
			if b, ok := tgtAP.(bool); ok && b == false {
				return false, fmt.Sprintf("properties[%q]: target forbids additional properties", p)
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
			ok2, reason, err := compat(tvm, cvm, false)
			if err != nil {
				return false, fmt.Sprintf("properties[%q]: error: %v", p, err)
			}
			if !ok2 {
				return false, fmt.Sprintf("properties[%q]: %s", p, reason)
			}
		}
	}

	// additionalProperties constraint:
	switch apTgt := tgtAP.(type) {
	case bool:
		if apTgt == false {
			if apCand, ok := cand["additionalProperties"].(bool); ok {
				if apCand == false {
					return true, ""
				}
				return false, "additionalProperties: target forbids but candidate allows"
			}
			// if cand schema or missing, it's not guaranteed false
			return false, "additionalProperties: target forbids but candidate allows"
		}
	case map[string]any:
		if apCand, ok := cand["additionalProperties"].(map[string]any); ok {
			ok2, reason, err := compat(apTgt, apCand, false)
			if err != nil {
				return false, fmt.Sprintf("additionalProperties: error: %v", err)
			}
			if !ok2 {
				return false, fmt.Sprintf("additionalProperties: %s", reason)
			}
		} else if apCand, ok := cand["additionalProperties"].(bool); ok && apCand == false {
			// cand is false: more restrictive than tgt schema, allowed for output.
			return true, ""
		} else {
			// cand AP is true or absent: less restrictive than tgt schema constraint.
			return false, "additionalProperties: candidate is less restrictive than target"
		}
	}

	return true, ""
}

func compatArray(tgt, cand map[string]any, isInput bool) (bool, string) {
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
	ok, reason, err := compat(tv, cv, isInput)
	if err != nil {
		return false, fmt.Sprintf("items: error: %v", err)
	}
	if !ok {
		return false, fmt.Sprintf("items: %s", reason)
	}
	return true, ""
}

func compatUnion(tgt, cand map[string]any, isInput bool) (bool, string) {
	tgtVars, okTgt := unionVariants(tgt)
	candVars, okCand := unionVariants(cand)
	if !okTgt || !okCand {
		// If only one side is a union, profile doesn't define cross-form rules; treat as incompatible.
		if !okTgt {
			return false, "oneOf: target is not a union but candidate is"
		}
		return false, "oneOf: candidate is not a union but target is"
	}

	unionKey := "oneOf"
	if _, ok := tgt["anyOf"]; ok {
		unionKey = "anyOf"
	}

	if isInput {
		// For every v in tgt, exists w in cand such that InputCompatible(v,w).
		for i, v := range tgtVars {
			found := false
			for _, w := range candVars {
				ok, _, err := compat(v, w, true)
				if err != nil {
					return false, fmt.Sprintf("%s: error: %v", unionKey, err)
				}
				if ok {
					found = true
					break
				}
			}
			if !found {
				return false, fmt.Sprintf("%s: target variant %d has no compatible candidate variant", unionKey, i)
			}
		}
		return true, ""
	}

	// Outputs/payloads:
	// For every w in cand, exists v in tgt such that OutputCompatible(v,w).
	for i, w := range candVars {
		found := false
		for _, v := range tgtVars {
			ok, _, err := compat(v, w, false)
			if err != nil {
				return false, fmt.Sprintf("%s: error: %v", unionKey, err)
			}
			if ok {
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Sprintf("%s: candidate variant %d has no compatible target variant", unionKey, i)
		}
	}
	return true, ""
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
func compatNumericBounds(tgt, cand map[string]any, isInput bool) (bool, string) {
	// Lower bounds: minimum / exclusiveMinimum
	tgtLo, tgtLoExcl := effectiveLowerBound(tgt)
	candLo, candLoExcl := effectiveLowerBound(cand)
	tgtHi, tgtHiExcl := effectiveUpperBound(tgt)
	candHi, candHiExcl := effectiveUpperBound(cand)

	tgtHasLo := hasKey(tgt, "minimum") || hasKey(tgt, "exclusiveMinimum")
	tgtHasHi := hasKey(tgt, "maximum") || hasKey(tgt, "exclusiveMaximum")
	candHasLo := hasKey(cand, "minimum") || hasKey(cand, "exclusiveMinimum")
	candHasHi := hasKey(cand, "maximum") || hasKey(cand, "exclusiveMaximum")

	fmtBound := func(v float64, excl bool) string {
		if excl {
			return fmt.Sprintf("exclusive %g", v)
		}
		return fmt.Sprintf("%g", v)
	}

	if isInput {
		// cand's lower bound MUST be <= tgt's (accept at least as low).
		if tgtHasLo && candHasLo {
			if !lowerBoundLessOrEqual(candLo, candLoExcl, tgtLo, tgtLoExcl) {
				return false, fmt.Sprintf("minimum: candidate minimum %s is greater than target minimum %s", fmtBound(candLo, candLoExcl), fmtBound(tgtLo, tgtLoExcl))
			}
		}
		// cand's upper bound MUST be >= tgt's (accept at least as high).
		if tgtHasHi && candHasHi {
			if !upperBoundGreaterOrEqual(candHi, candHiExcl, tgtHi, tgtHiExcl) {
				return false, fmt.Sprintf("maximum: candidate maximum %s is less than target maximum %s", fmtBound(candHi, candHiExcl), fmtBound(tgtHi, tgtHiExcl))
			}
		}
	} else {
		// cand's lower bound MUST be >= tgt's (return no lower).
		if tgtHasLo {
			if !candHasLo {
				return false, fmt.Sprintf("minimum: target has minimum %s but candidate has none", fmtBound(tgtLo, tgtLoExcl))
			}
			if !lowerBoundGreaterOrEqual(candLo, candLoExcl, tgtLo, tgtLoExcl) {
				return false, fmt.Sprintf("minimum: candidate minimum %s is less than target minimum %s", fmtBound(candLo, candLoExcl), fmtBound(tgtLo, tgtLoExcl))
			}
		}
		// cand's upper bound MUST be <= tgt's (return no higher).
		if tgtHasHi {
			if !candHasHi {
				return false, fmt.Sprintf("maximum: target has maximum %s but candidate has none", fmtBound(tgtHi, tgtHiExcl))
			}
			if !upperBoundLessOrEqual(candHi, candHiExcl, tgtHi, tgtHiExcl) {
				return false, fmt.Sprintf("maximum: candidate maximum %s is greater than target maximum %s", fmtBound(candHi, candHiExcl), fmtBound(tgtHi, tgtHiExcl))
			}
		}
	}
	return true, ""
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

// lowerBoundLessOrEqual returns true if lower bound a <= lower bound b.
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

// lowerBoundGreaterOrEqual returns true if lower bound a >= lower bound b.
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

// upperBoundLessOrEqual returns true if upper bound a <= upper bound b.
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

// upperBoundGreaterOrEqual returns true if upper bound a >= upper bound b.
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
func compatSimpleBounds(tgt, cand map[string]any, isInput bool, minKey, maxKey string) (bool, string) {
	if isInput {
		// min(cand) <= min(tgt). Absent cand = unconstrained (compatible).
		if hasKey(tgt, minKey) && hasKey(cand, minKey) {
			if toFloat64(cand[minKey]) > toFloat64(tgt[minKey]) {
				return false, fmt.Sprintf("%s: candidate %s %g is greater than target %s %g", minKey, minKey, toFloat64(cand[minKey]), minKey, toFloat64(tgt[minKey]))
			}
		}
		// max(cand) >= max(tgt). Absent cand = unconstrained (compatible).
		if hasKey(tgt, maxKey) && hasKey(cand, maxKey) {
			if toFloat64(cand[maxKey]) < toFloat64(tgt[maxKey]) {
				return false, fmt.Sprintf("%s: candidate %s %g is less than target %s %g", maxKey, maxKey, toFloat64(cand[maxKey]), maxKey, toFloat64(tgt[maxKey]))
			}
		}
	} else {
		// min(cand) >= min(tgt). Absent cand when tgt present = incompatible.
		if hasKey(tgt, minKey) {
			if !hasKey(cand, minKey) {
				return false, fmt.Sprintf("%s: target has %s %g but candidate has none", minKey, minKey, toFloat64(tgt[minKey]))
			}
			if toFloat64(cand[minKey]) < toFloat64(tgt[minKey]) {
				return false, fmt.Sprintf("%s: candidate %s %g is less than target %s %g", minKey, minKey, toFloat64(cand[minKey]), minKey, toFloat64(tgt[minKey]))
			}
		}
		// max(cand) <= max(tgt). Absent cand when tgt present = incompatible.
		if hasKey(tgt, maxKey) {
			if !hasKey(cand, maxKey) {
				return false, fmt.Sprintf("%s: target has %s %g but candidate has none", maxKey, maxKey, toFloat64(tgt[maxKey]))
			}
			if toFloat64(cand[maxKey]) > toFloat64(tgt[maxKey]) {
				return false, fmt.Sprintf("%s: candidate %s %g is greater than target %s %g", maxKey, maxKey, toFloat64(cand[maxKey]), maxKey, toFloat64(tgt[maxKey]))
			}
		}
	}
	return true, ""
}

// compatStringBounds checks minLength/maxLength rules.
func compatStringBounds(tgt, cand map[string]any, isInput bool) (bool, string) {
	return compatSimpleBounds(tgt, cand, isInput, "minLength", "maxLength")
}

// compatArrayBounds checks minItems/maxItems rules.
func compatArrayBounds(tgt, cand map[string]any, isInput bool) (bool, string) {
	return compatSimpleBounds(tgt, cand, isInput, "minItems", "maxItems")
}

func hasKey(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}
