package openbindings

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

type validateOptions struct {
	rejectUnknownTypedFields bool
}

// Option configures Interface validation.
type ValidateOption func(*validateOptions)

// WithRejectUnknownTypedFields treats unknown (non-`x-`) fields in typed OpenBindings objects as errors.
// Default behavior is forward-compatible (unknowns allowed/ignored), so this is an opt-in "strict" mode.
func WithRejectUnknownTypedFields() ValidateOption {
	return func(o *validateOptions) { o.rejectUnknownTypedFields = true }
}

// Validate performs shape-level checks useful for tooling correctness.
// It is intentionally not full JSON Schema validation; OBI-D-02 (schema
// validation) is a separate concern handled by a JSON Schema validator
// against openbindings.schema.json.
//
// Validate unconditionally enforces OBI-D-12 (openbindings field must
// be a valid SemVer 2.0.0 string) and OBI-T-04 (refuse to load when the
// document's major version is higher than this SDK's MaxTestedVersion, or --
// while MaxTestedVersion is pre-1.0 -- when its minor is higher). Versions
// outside the supported range in the other direction (older minor pre-1.0,
// etc.) are accepted for forward compatibility; the spec's SHOULD-warn behavior
// is left to higher-level tools.
func (i Interface) Validate(opts ...ValidateOption) error {
	o := validateOptions{
		rejectUnknownTypedFields: false,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	var errs []string

	// OBI-D-12: openbindings field MUST be a valid SemVer 2.0.0 string.
	// OBI-T-04: refuse higher major (or pre-1.0 higher minor) than MaxTested.
	if strings.TrimSpace(i.OpenBindings) == "" {
		errs = append(errs, "openbindings: required (OBI-D-12)")
	} else if !IsValidSemver(i.OpenBindings) {
		errs = append(errs, fmt.Sprintf("openbindings: %q is not a valid SemVer 2.0.0 string (OBI-D-12)", i.OpenBindings))
	} else if higher, err := IsHigherMajorOrPre1MinorThanMaxTested(i.OpenBindings); err != nil {
		errs = append(errs, fmt.Sprintf("openbindings: %v (OBI-T-04)", err))
	} else if higher {
		errs = append(errs, fmt.Sprintf("openbindings: %q exceeds this SDK's MaxTestedVersion %q (OBI-T-04)", i.OpenBindings, MaxTestedVersion))
	} else if pre, _ := IsUnsupportedPrerelease(i.OpenBindings); pre {
		// Reachable only when the version parsed (IsValidSemver passed above), so the error is ignored.
		errs = append(errs, fmt.Sprintf("openbindings: %q is a pre-release this SDK does not declare support for (OBI-T-04)", i.OpenBindings))
	}

	// Validate schemas: keys match identifier pattern (OBI-D-03); each schema
	// is walked for OBI-D-05 ($ref URI), OBI-D-06 ($schema dialect), OBI-D-07
	// (no $vocabulary).
	schKeys := make([]string, 0, len(i.Schemas))
	for k := range i.Schemas {
		schKeys = append(schKeys, k)
	}
	sort.Strings(schKeys)
	for _, k := range schKeys {
		validateIdent(&errs, "schemas key", k)
		walkSchema(&errs, fmt.Sprintf("schemas[%q]", k), i.Schemas[k])
	}

	if i.Operations == nil {
		errs = append(errs, "operations: required")
	}

	opKeys := make([]string, 0, len(i.Operations))
	for k := range i.Operations {
		opKeys = append(opKeys, k)
	}
	sort.Strings(opKeys)

	// aliases MUST NOT be shared across different operations (avoids ambiguous matching).
	aliasOwner := map[string]string{}
	opKeySet := map[string]struct{}{}
	for _, k := range opKeys {
		opKeySet[k] = struct{}{}
	}

	for _, k := range opKeys {
		op := i.Operations[k]

		// OBI-D-03: operation keys must match the identifier pattern.
		validateIdent(&errs, "operations key", k)

		// Alias checks (OBI-D-04 collisions, OBI-D-03 alias pattern).
		// Per OBI-D-04, an operation's "identifiers" = its key + aliases. All
		// identifiers across all operations must be distinct, including:
		//   - alias collides with another operation's key
		//   - same alias listed by two operations
		//   - alias listed twice within the same operation's array
		//   - alias equal to the operation's own key
		seenAlias := map[string]struct{}{}
		for _, a := range op.Aliases {
			if strings.TrimSpace(a) == "" {
				errs = append(errs, fmt.Sprintf("operations[%q].aliases: must not contain empty strings", k))
				continue
			}
			validateIdent(&errs, fmt.Sprintf("operations[%q].aliases", k), a)
			if a == k {
				errs = append(errs, fmt.Sprintf("operations[%q].aliases: %q duplicates the operation's own key (OBI-D-04)", k, a))
				continue
			}
			if _, dup := seenAlias[a]; dup {
				errs = append(errs, fmt.Sprintf("operations[%q].aliases: %q is listed more than once (OBI-D-04)", k, a))
				continue
			}
			seenAlias[a] = struct{}{}
			if _, isOpKey := opKeySet[a]; isOpKey {
				errs = append(errs, fmt.Sprintf("operations[%q].aliases: %q conflicts with operation key %q (OBI-D-04)", k, a, a))
				continue
			}
			if owner, ok := aliasOwner[a]; ok && owner != k {
				errs = append(errs, fmt.Sprintf("operations[%q].aliases: %q is also an alias of %q (OBI-D-04)", k, a, owner))
				continue
			}
			aliasOwner[a] = k
		}

		// Walk operation input/output schemas for OBI-D-05/D-07/D-08.
		if op.Input != nil {
			walkSchema(&errs, fmt.Sprintf("operations[%q].input", k), op.Input)
		}
		if op.Output != nil {
			walkSchema(&errs, fmt.Sprintf("operations[%q].output", k), op.Output)
		}

		// OBI-D-03: example keys must match the identifier pattern.
		exKeys := make([]string, 0, len(op.Examples))
		for ek := range op.Examples {
			exKeys = append(exKeys, ek)
		}
		sort.Strings(exKeys)
		for _, ek := range exKeys {
			validateIdent(&errs, fmt.Sprintf("operations[%q].examples key", k), ek)
		}

		if o.rejectUnknownTypedFields {
			appendUnknownFieldProblems(&errs, fmt.Sprintf("operations[%q]", k), op.Unknown)
			for ek, ex := range op.Examples {
				appendUnknownFieldProblems(&errs, fmt.Sprintf("operations[%q].examples[%q]", k, ek), ex.Unknown)
			}
		}
	}

	// Validate sources.
	srcKeys := make([]string, 0, len(i.Sources))
	for k := range i.Sources {
		srcKeys = append(srcKeys, k)
	}
	sort.Strings(srcKeys)
	for _, k := range srcKeys {
		// OBI-D-03: source keys must match the identifier pattern.
		validateIdent(&errs, "sources key", k)
		src := i.Sources[k]
		fmtVal := strings.TrimSpace(src.Format)
		if fmtVal == "" {
			errs = append(errs, fmt.Sprintf("sources[%q].format: required", k))
		}
		hasLocation := strings.TrimSpace(src.Location) != ""
		hasContent := src.Content != nil
		if !hasLocation && !hasContent {
			errs = append(errs, fmt.Sprintf("sources[%q]: must have location or content", k))
		}
		// OBI-D-05: sources[*].location must be a well-formed, absolute reference
		// (absolute URI or a format-defined absolute address; never relative).
		if hasLocation {
			validateURIRef(&errs, fmt.Sprintf("sources[%q].location", k), src.Location)
			if !referenceIsAbsolute(src.Location) {
				errs = append(errs, fmt.Sprintf("sources[%q].location: %q must be an absolute URI or a format-defined absolute address, not a relative reference (OBI-D-05)", k, src.Location))
			}
		}
		if o.rejectUnknownTypedFields {
			appendUnknownFieldProblems(&errs, fmt.Sprintf("sources[%q]", k), src.Unknown)
		}
	}

	// Validate transforms. No document rule constrains the expression's
	// content (an empty string is schema-valid); evaluation failures,
	// including empty expressions, surface at invoke time per OBI-T-10
	// (ErrEmptyTransformExpression / ERR_TRANSFORM_ERROR).
	trKeys := make([]string, 0, len(i.Transforms))
	for k := range i.Transforms {
		trKeys = append(trKeys, k)
	}
	sort.Strings(trKeys)
	for _, k := range trKeys {
		// OBI-D-03: transform keys must match the identifier pattern.
		validateIdent(&errs, "transforms key", k)
	}

	// Validate bindings.
	bndKeys := make([]string, 0, len(i.Bindings))
	for k := range i.Bindings {
		bndKeys = append(bndKeys, k)
	}
	sort.Strings(bndKeys)
	for _, k := range bndKeys {
		// OBI-D-03: binding keys must match the identifier pattern.
		validateIdent(&errs, "bindings key", k)
		b := i.Bindings[k]
		// OBI-D-08: bindings[*].operation must reference an existing operation.
		if strings.TrimSpace(b.Operation) == "" {
			errs = append(errs, fmt.Sprintf("bindings[%q].operation: required", k))
		} else if _, ok := i.Operations[b.Operation]; !ok {
			errs = append(errs, fmt.Sprintf("bindings[%q].operation: references unknown operation %q (OBI-D-08)", k, b.Operation))
		}
		// OBI-D-09: bindings[*].source must reference an existing source.
		if strings.TrimSpace(b.Source) == "" {
			errs = append(errs, fmt.Sprintf("bindings[%q].source: required", k))
		} else if _, ok := i.Sources[b.Source]; !ok {
			errs = append(errs, fmt.Sprintf("bindings[%q].source: references unknown source %q (OBI-D-09)", k, b.Source))
		}

		// Validate transform references.
		if b.InputTransform != nil && b.InputTransform.IsRef() {
			if err := validateTransformRef(b.InputTransform.Ref, i.Transforms); err != nil {
				errs = append(errs, fmt.Sprintf("bindings[%q].inputTransform.$ref: %v", k, err))
			}
		}
		if b.OutputTransform != nil && b.OutputTransform.IsRef() {
			if err := validateTransformRef(b.OutputTransform.Ref, i.Transforms); err != nil {
				errs = append(errs, fmt.Sprintf("bindings[%q].outputTransform.$ref: %v", k, err))
			}
		}

		if o.rejectUnknownTypedFields {
			appendUnknownFieldProblems(&errs, fmt.Sprintf("bindings[%q]", k), b.Unknown)
		}
	}

	if o.rejectUnknownTypedFields {
		appendUnknownFieldProblems(&errs, "", i.Unknown)
	}

	// OBI-D-02: validate the document against openbindings.schema.json.
	validateAgainstOBISchema(&errs, i)

	// OBI-D-11: validate every example's input/output against its operation's
	// input/output schema, when the respective schema is specified.
	validateExamplesAgainstOpSchemas(&errs, i)

	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{Problems: errs}
}

func appendUnknownFieldProblems(errs *[]string, prefix string, unknown map[string]json.RawMessage) {
	if len(unknown) == 0 {
		return
	}
	keys := make([]string, 0, len(unknown))
	for k := range unknown {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if prefix == "" {
		*errs = append(*errs, fmt.Sprintf("unknown fields: %s", strings.Join(keys, ", ")))
		return
	}
	*errs = append(*errs, fmt.Sprintf("%s: unknown fields: %s", prefix, strings.Join(keys, ", ")))
}

// validateTransformRef validates that a $ref resolves to a named transform per OBI-D-10.
func validateTransformRef(ref string, transforms map[string]Transform) error {
	const prefix = "#/transforms/"
	if !strings.HasPrefix(ref, prefix) {
		return fmt.Errorf("must start with %q (OBI-D-10)", prefix)
	}
	name := strings.TrimPrefix(ref, prefix)
	if name == "" {
		return fmt.Errorf("transform name is empty (OBI-D-10)")
	}
	if _, ok := transforms[name]; !ok {
		return fmt.Errorf("references unknown transform %q (OBI-D-10)", name)
	}
	return nil
}

// identPattern enforces OBI-D-03: every map key and every operation alias must match.
var identPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

// validateIdent appends an OBI-D-03 violation if id does not match the identifier pattern.
func validateIdent(errs *[]string, prefix, id string) {
	if !identPattern.MatchString(id) {
		*errs = append(*errs, fmt.Sprintf("%s: %q does not match identifier pattern ^[A-Za-z_][A-Za-z0-9_.-]*$ (OBI-D-03)", prefix, id))
	}
}

// uriRefAllowedChars holds the unreserved + reserved characters allowed in
// a URI-reference per RFC 3986. Percent-encoded triplets (%HH) are validated
// separately. Anything outside this set (whitespace, `, <, >, |, \, {, }, ",
// ^, etc.) makes the reference malformed.
var uriRefAllowedChars = func() [256]bool {
	var t [256]bool
	for c := byte('A'); c <= 'Z'; c++ {
		t[c] = true
	}
	for c := byte('a'); c <= 'z'; c++ {
		t[c] = true
	}
	for c := byte('0'); c <= '9'; c++ {
		t[c] = true
	}
	for _, c := range []byte("-._~:/?#[]@!$&'()*+,;=") {
		t[c] = true
	}
	return t
}()

// validateURIRef checks that raw is a well-formed URI reference per RFC 3986 §4.1.
// Empty strings are not validated here; callers handle emptiness separately.
//
// The check enforces the RFC 3986 character set strictly: only unreserved,
// reserved, and percent-encoded octets are permitted. net/url's parser is too
// permissive for this rule (it accepts whitespace, backticks, and angle
// brackets) so we apply a character-class screen before delegating to it for
// structural validation.
func validateURIRef(errs *[]string, prefix, raw string) {
	if raw == "" {
		return
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '%' {
			if i+2 >= len(raw) || !isHex(raw[i+1]) || !isHex(raw[i+2]) {
				*errs = append(*errs, fmt.Sprintf("%s: %q contains malformed percent-encoding (OBI-D-05)", prefix, raw))
				return
			}
			i += 2
			continue
		}
		if !uriRefAllowedChars[c] {
			*errs = append(*errs, fmt.Sprintf("%s: %q contains character %q not allowed in a URI reference (OBI-D-05)", prefix, raw, c))
			return
		}
	}
	if _, err := url.Parse(raw); err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: %q is not a well-formed URI reference (OBI-D-05)", prefix, raw))
	}
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// referenceIsAbsolute reports whether raw is an absolute reference for OBI-D-05
// purposes: an absolute URI (has a scheme) or a format-defined absolute address
// (e.g. a gRPC host:port, which parses with a scheme). Same-document fragments
// and relative-path references are not absolute.
func referenceIsAbsolute(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.IsAbs()
}

const draft202012URI = "https://json-schema.org/draft/2020-12/schema"

// JSON Schema 2020-12 keywords whose values are { name -> schema } maps.
var schemaMapKeywords = map[string]bool{
	"properties":        true,
	"patternProperties": true,
	"$defs":             true,
	"definitions":       true,
	"dependentSchemas":  true,
}

// JSON Schema 2020-12 keywords whose value is itself a schema.
var singleSchemaKeywords = map[string]bool{
	"additionalProperties":  true,
	"propertyNames":         true,
	"unevaluatedProperties": true,
	"items":                 true,
	"contains":              true,
	"unevaluatedItems":      true,
	"not":                   true,
	"if":                    true,
	"then":                  true,
	"else":                  true,
	"contentSchema":         true,
}

// JSON Schema 2020-12 keywords whose value is an array of schemas.
var arraySchemaKeywords = map[string]bool{
	"allOf":       true,
	"anyOf":       true,
	"oneOf":       true,
	"prefixItems": true,
}

// walkSchema walks a JSON Schema 2020-12 value and applies:
//   - OBI-D-06: $schema, where present, MUST equal the 2020-12 dialect URI.
//   - OBI-D-07: $vocabulary keyword is forbidden anywhere in any schema.
//   - OBI-D-05: $ref and $id values MUST be absolute or same-document and
//     well-formed URI references (RFC 3986 §4.1).
//
// Recursion follows JSON Schema keyword shapes so that property names under
// `properties`/`patternProperties`/`$defs`/etc. are not themselves treated as
// schema keywords.
func walkSchema(errs *[]string, prefix string, schema any) {
	var s map[string]any
	switch v := schema.(type) {
	case map[string]any:
		s = v
	case JSONSchema:
		s = map[string]any(v)
	default:
		return
	}
	if s == nil {
		return
	}

	if v, ok := s["$schema"]; ok {
		if str, ok := v.(string); ok && str != draft202012URI {
			*errs = append(*errs, fmt.Sprintf("%s.$schema: %q must equal %q (OBI-D-06)", prefix, str, draft202012URI))
		}
	}
	if _, ok := s["$vocabulary"]; ok {
		*errs = append(*errs, fmt.Sprintf("%s: $vocabulary keyword is forbidden in OBI documents (OBI-D-07)", prefix))
	}
	if v, ok := s["$ref"]; ok {
		if str, ok := v.(string); ok {
			validateURIRef(errs, prefix+".$ref", str)
			if !strings.HasPrefix(str, "#") && !referenceIsAbsolute(str) {
				*errs = append(*errs, fmt.Sprintf("%s.$ref: %q must be a same-document fragment or an absolute URI, not a relative reference (OBI-D-05)", prefix, str))
			}
		}
	}
	if v, ok := s["$id"]; ok {
		if str, ok := v.(string); ok && !referenceIsAbsolute(str) {
			*errs = append(*errs, fmt.Sprintf("%s.$id: %q must be an absolute URI (OBI-D-05)", prefix, str))
		}
	}

	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := s[k]
		switch {
		case schemaMapKeywords[k]:
			if m, ok := v.(map[string]any); ok {
				subKeys := make([]string, 0, len(m))
				for sk := range m {
					subKeys = append(subKeys, sk)
				}
				sort.Strings(subKeys)
				for _, sk := range subKeys {
					walkSchema(errs, fmt.Sprintf("%s.%s.%s", prefix, k, sk), m[sk])
				}
			}
		case singleSchemaKeywords[k]:
			walkSchema(errs, prefix+"."+k, v)
		case arraySchemaKeywords[k]:
			if arr, ok := v.([]any); ok {
				for idx, item := range arr {
					walkSchema(errs, fmt.Sprintf("%s.%s[%d]", prefix, k, idx), item)
				}
			}
		}
	}
}
