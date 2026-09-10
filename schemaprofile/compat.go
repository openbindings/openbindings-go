package schemaprofile

import (
	"fmt"
	"sort"
)

// InputCompatible implements profile v0.1 input rules (interface schema <=
// candidate schema). Both schemas MUST already be normalized (see
// Normalizer.Normalize): $refs are not resolved here. Callers comparing
// schemas from two documents normalize each side against its own root and
// then call this — the same shape the TypeScript SDK's free
// inputCompatible/outputCompatible functions have. Tell-tale non-normalized
// shapes (a scalar type, an unresolved $ref, an unflattened allOf) are
// refused with a NotNormalizedError rather than risking a silently
// divergent verdict.
func InputCompatible(tgt, cand map[string]any) (bool, string, error) {
	if err := assertNormalizedPair(tgt, cand); err != nil {
		return false, "", err
	}
	// Trivial schema: {} is Top.
	if len(cand) == 0 {
		return true, "", nil
	}
	return compat(tgt, cand, true)
}

// OutputCompatible implements profile v0.1 output/payload rules (candidate
// schema <= interface schema). Both schemas MUST already be normalized (see
// Normalizer.Normalize); see InputCompatible, including the loud
// NotNormalizedError refusal of tell-tale non-normalized shapes.
func OutputCompatible(tgt, cand map[string]any) (bool, string, error) {
	if err := assertNormalizedPair(tgt, cand); err != nil {
		return false, "", err
	}
	// Trivial schema: {} is Top; allowed only if interface is also Top.
	if len(cand) == 0 {
		if len(tgt) == 0 {
			return true, "", nil
		}
		return false, "candidate is unconstrained but target is not", nil
	}
	return compat(tgt, cand, false)
}

// assertNormalizedPair guards the pre-normalization contract of the two
// package-level directional checks: the target is checked first, then the
// candidate, so a violation on both sides reports deterministically.
func assertNormalizedPair(tgt, cand map[string]any) error {
	if err := assertNormalized(tgt, "target"); err != nil {
		return err
	}
	return assertNormalized(cand, "candidate")
}

// assertNormalized refuses the cheap, unambiguous shapes the Normalizer can
// never emit: an unresolved $ref (always inlined), an unflattened allOf
// (always merged away), and a non-array type (always canonicalized to a
// sorted array). These are exactly the shapes that would otherwise decide
// verdicts silently — most notably a raw scalar type, which the two
// reference SDKs historically read differently. This is NOT a full
// normalized-form validator; anything subtler stays the caller's contract.
// Nested walks visit properties (sorted), additionalProperties, items, then
// oneOf/anyOf variants — mirrored in the TypeScript SDK's assertNormalized.
func assertNormalized(schema map[string]any, path string) error {
	if _, ok := schema["$ref"]; ok {
		return &NotNormalizedError{Path: path, Keyword: "$ref", Requirement: "resolved"}
	}
	if _, ok := schema["allOf"]; ok {
		return &NotNormalizedError{Path: path, Keyword: "allOf", Requirement: "flattened"}
	}
	if v, ok := schema["type"]; ok {
		if _, isArr := asSlice(v); !isArr {
			return &NotNormalizedError{Path: path, Keyword: "type", Requirement: "an array"}
		}
	}
	if props, ok := asMap(schema["properties"]); ok {
		for _, k := range sortedMapKeys(props) {
			if vm, ok := asMap(props[k]); ok {
				if err := assertNormalized(vm, ptrJoin(path, fmt.Sprintf("properties[%s]", canonicalKey(k)))); err != nil {
					return err
				}
			}
		}
	}
	if ap, ok := asMap(schema["additionalProperties"]); ok {
		if err := assertNormalized(ap, ptrJoin(path, "additionalProperties")); err != nil {
			return err
		}
	}
	if items, ok := asMap(schema["items"]); ok {
		if err := assertNormalized(items, ptrJoin(path, "items")); err != nil {
			return err
		}
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		arr, ok := asSlice(schema[key])
		if !ok {
			continue
		}
		for i, it := range arr {
			if vm, ok := asMap(it); ok {
				if err := assertNormalized(vm, ptrJoin(path, fmt.Sprintf("%s[%d]", key, i))); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
	if ok, reason, err := compatConstEnum(tgt, cand, isInput); err != nil || !ok {
		return false, reason, err
	}

	// Object rules if type includes object.
	if hasType(tgt, "object") || hasType(cand, "object") {
		ok, reason, err := compatObject(tgt, cand, isInput)
		if err != nil || !ok {
			return false, reason, err
		}
	}

	// Array rules if type includes array.
	if hasType(tgt, "array") || hasType(cand, "array") {
		ok, reason, err := compatArray(tgt, cand, isInput)
		if err != nil || !ok {
			return false, reason, err
		}
	}

	// Numeric bounds rules (when type includes number or integer).
	if hasType(tgt, "number") || hasType(tgt, "integer") || hasType(cand, "number") || hasType(cand, "integer") {
		ok, reason, err := compatNumericBounds(tgt, cand, isInput)
		if err != nil || !ok {
			return false, reason, err
		}
	}

	// String bounds rules (when type includes string).
	if hasType(tgt, "string") || hasType(cand, "string") {
		ok, reason, err := compatStringBounds(tgt, cand, isInput)
		if err != nil || !ok {
			return false, reason, err
		}
	}

	// Array bounds rules (when type includes array).
	if hasType(tgt, "array") || hasType(cand, "array") {
		ok, reason, err := compatArrayBounds(tgt, cand, isInput)
		if err != nil || !ok {
			return false, reason, err
		}
	}

	// Union rules.
	if hasUnion(tgt) || hasUnion(cand) {
		ok, reason, err := compatUnion(tgt, cand, isInput)
		if err != nil || !ok {
			return false, reason, err
		}
	}

	return true, "", nil
}

// missingTypes returns a comma-separated list of types in a that are not in
// b, sorted lexicographically so the reason string is deterministic (type
// sets are Go maps; iteration order must never leak into diagnostics). Type
// names render via canonicalKey — the JCS (RFC 8785) string rendering member
// names and values use. For the seven legitimate JSON Schema type names this
// is byte-identical to the former %q; only pathological names reachable via
// non-normalized input render differently.
func missingTypes(a, b map[string]struct{}) string {
	if a == nil {
		return "all types"
	}
	var missing []string
	for k := range a {
		if b == nil {
			missing = append(missing, canonicalKey(k))
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
		missing = append(missing, canonicalKey(k))
	}
	sort.Strings(missing)
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

// compatConstEnum applies the const/enum rules. Reason prefixes follow the
// deciding-keyword convention: the prefix names the keyword whose constraint
// rejects the flowing value — for inputs the CANDIDATE's keyword (the target
// sends, the candidate refuses), for outputs the TARGET's (the candidate
// produces, the target refuses). Mirrored byte-for-byte in the TypeScript
// SDK's compat.ts.

// If tgt uses const, cand must accept it.

// cand unconstrained w.r.t const/enum

// If tgt uses enum, cand must accept all values in tgt.

// single const must cover all enum values

// Outputs:
// If tgt uses enum, cand must only allow values within that enum (cand subset).

// cand unconstrained but tgt constrained -> can emit values outside

// If tgt uses const, cand must only allow that constant.

// cand unconstrained but tgt const -> can emit others

// tgt unconstrained: ok

// compatObject applies the object rules. Set and property iteration is
// SORTED so the first-failing member named in the reason is deterministic
// (and byte-identical across the reference SDKs) when several members fail.
// Property and required member names interpolate via canonicalKey — the
// same JCS rendering values get — so names carrying quotes, backslashes, or
// control characters escape identically across the reference SDKs (plain
// names render exactly as a bare quoted spelling).
func compatObject(tgt, cand map[string]any, isInput bool) (bool, string, error) {
	tgtReq := stringSet(tgt["required"])
	candReq := stringSet(cand["required"])

	tgtProps, _ := asMap(tgt["properties"])
	candProps, _ := asMap(cand["properties"])

	if isInput {
		// required(cand) <= required(tgt)
		for _, k := range sortedSetKeys(candReq) {
			if _, ok := tgtReq[k]; !ok {
				return false, fmt.Sprintf("required: candidate requires %s but target does not", canonicalKey(k)), nil
			}
		}
		// For each p in properties(tgt):
		for _, p := range sortedMapKeys(tgtProps) {
			tvm, ok := asMap(tgtProps[p])
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
					return false, "", fmt.Errorf("properties[%s]: %w", canonicalKey(p), err)
				}
				if !ok2 {
					return false, fmt.Sprintf("properties[%s]: %s", canonicalKey(p), reason), nil
				}
			}
			// If cand lacks property schema, treated as unconstrained (compatible).
		}
		// additionalProperties does not restrict input compatibility in v0.1.
		return true, "", nil
	}

	// Outputs/payloads:
	// required(tgt) <= required(cand)
	for _, k := range sortedSetKeys(tgtReq) {
		if _, ok := candReq[k]; !ok {
			return false, fmt.Sprintf("required: target requires %s but candidate does not", canonicalKey(k)), nil
		}
	}

	tgtAP := tgt["additionalProperties"]

	// For each property p in properties(cand):
	for _, p := range sortedMapKeys(candProps) {
		cv := candProps[p]
		// If p not in properties(tgt), then additionalProperties(tgt) MUST NOT be false.
		if _, ok := tgtProps[p]; !ok {
			if b, ok := tgtAP.(bool); ok && b == false {
				return false, fmt.Sprintf("properties[%s]: target forbids additional properties", canonicalKey(p)), nil
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
				return false, "", fmt.Errorf("properties[%s]: %w", canonicalKey(p), err)
			}
			if !ok2 {
				return false, fmt.Sprintf("properties[%s]: %s", canonicalKey(p), reason), nil
			}
		}
	}

	// additionalProperties constraint:
	switch apTgt := tgtAP.(type) {
	case bool:
		if apTgt == false {
			if apCand, ok := cand["additionalProperties"].(bool); ok {
				if apCand == false {
					return true, "", nil
				}
				return false, "additionalProperties: target forbids but candidate allows", nil
			}
			// if cand schema or missing, it's not guaranteed false
			return false, "additionalProperties: target forbids but candidate allows", nil
		}
	case map[string]any:
		if apCand, ok := cand["additionalProperties"].(map[string]any); ok {
			ok2, reason, err := compat(apTgt, apCand, false)
			if err != nil {
				return false, "", fmt.Errorf("additionalProperties: %w", err)
			}
			if !ok2 {
				return false, fmt.Sprintf("additionalProperties: %s", reason), nil
			}
		} else if apCand, ok := cand["additionalProperties"].(bool); ok && apCand == false {
			// cand is false: more restrictive than tgt schema, allowed for output.
			return true, "", nil
		} else {
			// cand AP is true or absent: less restrictive than tgt schema constraint.
			return false, "additionalProperties: candidate is less restrictive than target", nil
		}
	}

	return true, "", nil
}

func compatArray(tgt, cand map[string]any, isInput bool) (bool, string, error) {
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
		return false, "", fmt.Errorf("items: %w", err)
	}
	if !ok {
		return false, fmt.Sprintf("items: %s", reason), nil
	}
	return true, "", nil
}

func compatUnion(tgt, cand map[string]any, isInput bool) (bool, string, error) {
	tgtVars, okTgt := unionVariants(tgt)
	candVars, okCand := unionVariants(cand)
	if !okTgt || !okCand {
		// If only one side is a union, profile doesn't define cross-form rules; treat as incompatible.
		if !okTgt {
			return false, "oneOf: target is not a union but candidate is", nil
		}
		return false, "oneOf: candidate is not a union but target is", nil
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
					return false, "", fmt.Errorf("%s: %w", unionKey, err)
				}
				if ok {
					found = true
					break
				}
			}
			if !found {
				return false, fmt.Sprintf("%s: target variant %d has no compatible candidate variant", unionKey, i), nil
			}
		}
		return true, "", nil
	}

	// Outputs/payloads:
	// For every w in cand, exists v in tgt such that OutputCompatible(v,w).
	for i, w := range candVars {
		found := false
		for _, v := range tgtVars {
			ok, _, err := compat(v, w, false)
			if err != nil {
				return false, "", fmt.Errorf("%s: %w", unionKey, err)
			}
			if ok {
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Sprintf("%s: candidate variant %d has no compatible target variant", unionKey, i), nil
		}
	}
	return true, "", nil
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

// sortedSetKeys returns a set's keys in lexicographic order — reason
// strings must never leak Go map iteration order.
func sortedSetKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedMapKeys returns a map's keys in lexicographic order (see
// sortedSetKeys).
func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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

func canonicalKey(v string) string {
	// Diagnostic member-name quoting retains the established JCS string form.
	// This helper never determines numerical equality or cache identity.
	s, err := CanonicalString(v)
	if err != nil {
		// As a fallback, use a best-effort string.
		return "<unserializable>"
	}
	return s
}

// compatNumericBounds checks minimum/maximum/exclusiveMinimum/exclusiveMaximum rules.

// Lower bounds: minimum / exclusiveMinimum

// cand's lower bound MUST be <= tgt's (accept at least as low).

// cand's upper bound MUST be >= tgt's (accept at least as high).

// cand's lower bound MUST be >= tgt's (return no lower).

// cand's upper bound MUST be <= tgt's (return no higher).

// effectiveLowerBound returns the effective lower bound value and whether it's exclusive.

// effectiveUpperBound returns the effective upper bound value and whether it's exclusive.

// Lower bound comparisons:
// For lower bounds, exclusive means the bound is HIGHER (stricter).
// exclusiveMinimum: 0 means > 0, while minimum: 0 means >= 0.
// So at equal values: exclusive > non-exclusive.

// lowerBoundLessOrEqual returns true if lower bound a <= lower bound b.

// Equal values: exclusive is stricter (higher)

// a is higher (stricter), so a > b

// lowerBoundGreaterOrEqual returns true if lower bound a >= lower bound b.

// Equal values: exclusive is stricter (higher)

// b is higher (stricter), so a < b

// Upper bound comparisons:
// For upper bounds, exclusive means the bound is LOWER (stricter).
// exclusiveMaximum: 100 means < 100, while maximum: 100 means <= 100.
// So at equal values: exclusive < non-exclusive.

// upperBoundLessOrEqual returns true if upper bound a <= upper bound b.

// Equal values: exclusive is stricter (lower)

// b is lower (stricter), so a > b

// upperBoundGreaterOrEqual returns true if upper bound a >= upper bound b.

// Equal values: exclusive is stricter (lower)

// a is lower (stricter), so a < b

// compatSimpleBounds checks a pair of min/max keywords that use simple integer
// comparisons (no exclusivity). Used for minLength/maxLength and minItems/maxItems.

// min(cand) <= min(tgt). Absent cand = unconstrained (compatible).

// max(cand) >= max(tgt). Absent cand = unconstrained (compatible).

// min(cand) >= min(tgt). Absent cand when tgt present = incompatible.

// max(cand) <= max(tgt). Absent cand when tgt present = incompatible.

// compatStringBounds checks minLength/maxLength rules.
func compatStringBounds(tgt, cand map[string]any, isInput bool) (bool, string, error) {
	return compatSimpleBounds(tgt, cand, isInput, "minLength", "maxLength")
}

// compatArrayBounds checks minItems/maxItems rules.
func compatArrayBounds(tgt, cand map[string]any, isInput bool) (bool, string, error) {
	return compatSimpleBounds(tgt, cand, isInput, "minItems", "maxItems")
}

func hasKey(m map[string]any, key string) bool {
	_, ok := m[key]
	return ok
}
