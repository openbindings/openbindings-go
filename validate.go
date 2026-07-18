package openbindings

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/recolabs/gnata"
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
// be a valid SemVer 2.0.0 string) and OBI-T-04 (refuse versions outside the
// supported range in EITHER direction: a higher major — or, pre-1.0, a
// higher minor — than MaxTestedVersion, and likewise anything below
// MinSupportedVersion; processing a document under the wrong version's
// rules misreads it both ways).
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
	} else if msg, refused, err := versionRefusal(i.OpenBindings); err != nil {
		// Reachable only if the version failed to parse, which IsValidSemver
		// above already screened out; kept as a defensive branch.
		errs = append(errs, fmt.Sprintf("openbindings: %v (OBI-T-04)", err))
	} else if refused {
		errs = append(errs, fmt.Sprintf("openbindings: %s (OBI-T-04)", msg))
	}

	// Generic-JSON view of the whole document for OBI-D-16 pointer
	// resolution (existence checks for same-document schema $refs). A marshal
	// failure leaves docView nil, which skips the D-16 check rather than
	// erroring: the lossless marshal of a decoded document does not fail in
	// practice, and D-16 is a referential-integrity check, not a parse gate.
	var docView any
	if data, jerr := json.Marshal(i); jerr == nil {
		_ = json.Unmarshal(data, &docView)
	}

	// Validate schemas: keys match identifier pattern (OBI-D-03); each schema
	// is checked for well-formedness against the 2020-12 meta-schemas
	// (OBI-D-17) and walked for OBI-D-05 ($ref URI), OBI-D-06 ($schema
	// dialect), OBI-D-07 (no $vocabulary), and OBI-D-16 (same-document $refs
	// resolve).
	schKeys := make([]string, 0, len(i.Schemas))
	for k := range i.Schemas {
		schKeys = append(schKeys, k)
	}
	sort.Strings(schKeys)
	for _, k := range schKeys {
		validateIdent(&errs, "schemas key", k)
		validateSchemaWellFormedness(&errs, fmt.Sprintf("schemas[%q]", k), i.Schemas[k])
		walkSchema(&errs, fmt.Sprintf("schemas[%q]", k), i.Schemas[k], docView, false)
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

		// Check operation input/output schemas for well-formedness (OBI-D-17)
		// and walk them for OBI-D-05/D-06/D-07/D-16.
		if op.Input != nil {
			validateSchemaWellFormedness(&errs, fmt.Sprintf("operations[%q].input", k), op.Input)
			walkSchema(&errs, fmt.Sprintf("operations[%q].input", k), op.Input, docView, false)
		}
		if op.Output != nil {
			validateSchemaWellFormedness(&errs, fmt.Sprintf("operations[%q].output", k), op.Output)
			walkSchema(&errs, fmt.Sprintf("operations[%q].output", k), op.Output, docView, false)
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
		if strings.TrimSpace(src.BindingSpec) == "" {
			errs = append(errs, fmt.Sprintf("sources[%q].bindingSpec: required", k))
		}
		hasLocation := strings.TrimSpace(src.Location) != ""
		hasContent := src.Content != nil
		if !hasLocation && !hasContent {
			errs = append(errs, fmt.Sprintf("sources[%q]: must have location or content", k))
		}
		// OBI-D-05: sources[*].location must be a well-formed, absolute reference
		// (absolute URI or a bindingSpec-defined absolute address; never relative).
		if hasLocation {
			validateLocation(&errs, fmt.Sprintf("sources[%q].location", k), src.Location)
		}
		if o.rejectUnknownTypedFields {
			appendUnknownFieldProblems(&errs, fmt.Sprintf("sources[%q]", k), src.Unknown)
		}
	}

	// Validate transforms. OBI-D-18: every value in the transforms map parses
	// as a syntactically valid expression of the pinned transform language
	// (JSONata 2.1, jsonata-js 2.1.1 parse-acceptance tiebreak). Parse-only:
	// evaluation failures (undefined results, dynamic errors) remain invoke-
	// time outcomes per OBI-T-10 / ERR_TRANSFORM_ERROR.
	trKeys := make([]string, 0, len(i.Transforms))
	for k := range i.Transforms {
		trKeys = append(trKeys, k)
	}
	sort.Strings(trKeys)
	for _, k := range trKeys {
		// OBI-D-03: transform keys must match the identifier pattern.
		validateIdent(&errs, "transforms key", k)
		validateTransformExpression(&errs, fmt.Sprintf("transforms[%q]", k), i.Transforms[k])
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

		// Validate transform references (OBI-D-10) and inline transform
		// parse-validity (OBI-D-18).
		if b.InputTransform != nil {
			if b.InputTransform.IsRef() {
				if err := validateTransformRef(b.InputTransform.Ref, i.Transforms); err != nil {
					errs = append(errs, fmt.Sprintf("bindings[%q].inputTransform.$ref: %v", k, err))
				}
			} else {
				validateTransformExpression(&errs, fmt.Sprintf("bindings[%q].inputTransform", k), b.InputTransform.Inline)
			}
		}
		if b.OutputTransform != nil {
			if b.OutputTransform.IsRef() {
				if err := validateTransformRef(b.OutputTransform.Ref, i.Transforms); err != nil {
					errs = append(errs, fmt.Sprintf("bindings[%q].outputTransform.$ref: %v", k, err))
				}
			} else {
				validateTransformExpression(&errs, fmt.Sprintf("bindings[%q].outputTransform", k), b.OutputTransform.Inline)
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

// docPointerResolves reports whether an RFC 6901 JSON Pointer (the fragment
// with its leading # removed) resolves to an existing location in doc, the
// generic-JSON view of the whole OBI document. Existence only: OBI-D-16 does
// not type-check the target.
func docPointerResolves(doc any, pointer string) bool {
	// The fragment is the pointer's URI-fragment representation (RFC 6901
	// §6): percent-decode the whole fragment first, then evaluate the
	// result as a JSON Pointer (§10). Malformed percent-encoding simply
	// fails to resolve — it is already a D-05 char-screen violation upstream.
	decoded, err := url.PathUnescape(pointer)
	if err != nil {
		return false
	}
	pointer = decoded
	if pointer == "" {
		return true // bare # addresses the document root
	}
	if !strings.HasPrefix(pointer, "/") {
		return false
	}
	cur := doc
	for _, tok := range strings.Split(pointer[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[tok]
			if !ok {
				return false
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(node) {
				return false
			}
			cur = node[idx]
		default:
			return false
		}
	}
	return true
}

// validateTransformExpression reports an OBI-D-18 violation when expr does
// not parse as a syntactically valid expression of the pinned transform
// language (§5.5: JSONata 2.1, with jsonata-js 2.1.1's parse acceptance as
// the normative tiebreak). Parse-only: membership in the language, not
// success of evaluation — undefined results and dynamic errors remain
// invoke-time outcomes.
func validateTransformExpression(errs *[]string, prefix, expr string) {
	if !jsonataParses(expr) {
		*errs = append(*errs, fmt.Sprintf("%s: not a syntactically valid JSONata expression (OBI-D-18)", prefix))
	}
}

// jsonataParses reports whether expr parses under the bundled JSONata
// parser (gnata, a JSONata 2.x engine — closer to the normative jsonata-js
// 2.1.1 parse acceptance than the prior 1.5 port). The parser is only ever
// handed document-supplied strings, so a parser panic is treated as a parse
// failure rather than crashing document validation.
func jsonataParses(expr string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	_, err := gnata.Compile(expr)
	return err == nil
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
	if !screenURIChars(errs, prefix, raw) {
		return
	}
	if _, err := url.Parse(raw); err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: %q is not a well-formed URI reference (OBI-D-05)", prefix, raw))
	}
}

// screenURIChars reports whether raw contains only characters permitted in a
// URI reference per RFC 3986, with well-formed percent-encoding. On the first
// violation it appends an error and returns false. net/url's parser is too
// permissive (it accepts whitespace, backticks, and angle brackets), so this
// screen runs before any structural parse.
func screenURIChars(errs *[]string, prefix, raw string) bool {
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '%' {
			if i+2 >= len(raw) || !isHex(raw[i+1]) || !isHex(raw[i+2]) {
				*errs = append(*errs, fmt.Sprintf("%s: %q contains malformed percent-encoding (OBI-D-05)", prefix, raw))
				return false
			}
			i += 2
			continue
		}
		if !uriRefAllowedChars[c] {
			*errs = append(*errs, fmt.Sprintf("%s: %q contains character %q not allowed in a URI reference (OBI-D-05)", prefix, raw, c))
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// referenceIsAbsolute reports whether raw is an absolute URI (has a scheme).
// Used for schema $ref/$id, which are always URI-form: a same-document fragment
// or an absolute URI. Source locations use validateLocation instead, which also
// admits non-scheme format-defined absolute addresses such as a gRPC host:port.
func referenceIsAbsolute(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.IsAbs()
}

// validateLocation checks OBI-D-05 for a sources[*].location: it MUST be an
// absolute URI or a format-defined absolute address (e.g. a gRPC host:port),
// never a relative reference. The character screen applies to every location;
// the strict RFC 3986 structural parse applies only to URI-form locations
// (those carrying a URI scheme). A scheme-less format-defined absolute address
// (an IP-literal host:port like 10.0.0.1:443 or [::1]:443) is exempt from RFC
// 3986 well-formedness: its syntax is the binding format's concern, not OBI's
// (OBI-D-05, §10). referenceIsAbsolute is deliberately not reused here: it
// treats a location as absolute only when net/url infers a scheme, so it would
// admit a hostname:port (host misread as scheme) yet reject an IP-literal one.
func validateLocation(errs *[]string, prefix, raw string) {
	if raw == "" {
		return
	}
	if isRelativeReference(raw) {
		*errs = append(*errs, fmt.Sprintf("%s: %q must be an absolute URI or a format-defined absolute address, not a relative reference (OBI-D-05); a local artifact can be embedded as the source's content instead (a file:// URL is machine-coupled and resolves only on the authoring machine)", prefix, raw))
		return
	}
	// OBI-D-05's exemption: a bindingSpec-defined absolute address (a gRPC
	// host:port, a usage exec argv vector) is well-formed per its own
	// binding specification, which the core cannot verify. The decidable
	// discriminator is the hierarchical URI form: a value whose scheme is
	// followed by "//" claims RFC 3986 URI form and is held to it; a
	// scheme-opaque non-relative value may be a bindingSpec-defined address
	// and passes core validation (per-family verification belongs to family
	// processors, per the partial-verification posture).
	if !isHierarchicalURIForm(raw) {
		return
	}
	if !screenURIChars(errs, prefix, raw) {
		return
	}
	if _, err := url.Parse(raw); err != nil {
		*errs = append(*errs, fmt.Sprintf("%s: %q is not a well-formed URI reference (OBI-D-05)", prefix, raw))
	}
}

// isHierarchicalURIForm reports whether raw is scheme://... — the RFC 3986
// hierarchical form whose well-formedness the core enforces at source
// locations. Scheme-opaque values (mailto:-style, exec:argv, host:port) are
// outside it.
func isHierarchicalURIForm(raw string) bool {
	i := strings.IndexByte(raw, ':')
	if i <= 0 || !hasURIScheme(raw) {
		return false
	}
	return strings.HasPrefix(raw[i+1:], "//")
}

// isRelativeReference reports whether raw is a relative reference per RFC 3986
// §4.2: one with no scheme and no authority, needing a base URI to resolve
// (./x, ../x, x.json, /abs/path, //host/path). The discriminator is whether a
// ':' appears before the first '/', '?', or '#'; a relative reference has none.
// Both absolute URIs (https://...) and format-defined absolute addresses
// (grpc.example.com:443, 10.0.0.1:443, [::1]:443) are therefore non-relative.
func isRelativeReference(raw string) bool {
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case ':':
			return false
		case '/', '?', '#':
			return true
		}
	}
	return true
}

// hasURIScheme reports whether raw begins with an RFC 3986 scheme followed by
// ':' (ALPHA *( ALPHA / DIGIT / "+" / "-" / "." ) ":"). URI-form locations get
// the strict structural parse; a scheme-less format-defined absolute address
// (an IP-literal host:port) does not, and is left to its format to interpret.
func hasURIScheme(raw string) bool {
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == ':' {
			return i > 0
		}
		if i == 0 {
			if !isAlpha(c) {
				return false
			}
			continue
		}
		if !isAlpha(c) && !isDigit(c) && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	return false
}

func isAlpha(c byte) bool { return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

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
//     well-formed URI references (RFC 3986 §4.1); $dynamicRef and
//     $dynamicAnchor do not appear at OBI positions at all. A nested $id
//     inside a schema that already declares one is resource-internal and
//     exempt from the absoluteness check, per §10 clause 2.
//
// Recursion follows JSON Schema keyword shapes so that property names under
// `properties`/`patternProperties`/`$defs`/etc. are not themselves treated as
// schema keywords.
func walkSchema(errs *[]string, prefix string, schema any, doc any, inID bool) {
	s, ok := schema.(map[string]any)
	if !ok || s == nil {
		// Boolean schemas carry no keywords to walk; non-schema values are
		// OBI-D-17's concern (validateSchemaWellFormedness).
		return
	}

	// wasInID captures the incoming scope before the mutation below: it
	// distinguishes an $id at an OBI position (must be absolute, OBI-D-05)
	// from a nested $id inside a schema that already declares one (resolves
	// against that resource's base per JSON Schema 2020-12 and MAY be
	// relative — resource-internal, §10 clause 2).
	wasInID := inID

	// A schema that declares its own $id is a distinct schema resource: its
	// $ref (and its subtree's) resolve against that resource's base per §10,
	// so OBI-D-16's root-context resolution check does not apply inside it.
	if v, ok := s["$id"]; ok {
		if _, isStr := v.(string); isStr {
			inID = true
		}
	}

	if v, ok := s["$schema"]; ok {
		if str, ok := v.(string); ok && str != draft202012URI {
			*errs = append(*errs, fmt.Sprintf("%s.$schema: %q must equal %q (OBI-D-06)", prefix, str, draft202012URI))
		}
	}
	if _, ok := s["$vocabulary"]; ok {
		*errs = append(*errs, fmt.Sprintf("%s: $vocabulary keyword is forbidden in OBI documents (OBI-D-07)", prefix))
	}
	// The dynamic pair does not appear at OBI positions at all: dynamic
	// resolution follows the runtime dynamic scope rather than the document
	// (§10 clause 2). Inside a schema declaring its own $id, both are that
	// resource's internal business — the same scope carve-out as $ref/$anchor.
	if !inID {
		if _, ok := s["$dynamicRef"]; ok {
			*errs = append(*errs, fmt.Sprintf("%s: $dynamicRef does not appear at OBI positions; dynamic resolution follows the runtime dynamic scope rather than the document (OBI-D-05)", prefix))
		}
		if _, ok := s["$dynamicAnchor"]; ok {
			*errs = append(*errs, fmt.Sprintf("%s: $dynamicAnchor does not appear at OBI positions; it would be a second named-schema mechanism competing with the schemas map, as $anchor would (OBI-D-05)", prefix))
		}
	}
	if v, ok := s["$ref"]; ok {
		if str, ok := v.(string); ok {
			validateURIRef(errs, prefix+".$ref", str)
			if !strings.HasPrefix(str, "#") && !referenceIsAbsolute(str) {
				*errs = append(*errs, fmt.Sprintf("%s.$ref: %q must be a same-document fragment or an absolute URI, not a relative reference (OBI-D-05)", prefix, str))
			} else if strings.HasPrefix(str, "#") && str != "#" && !strings.HasPrefix(str, "#/") && !inID {
				// Inside a schema declaring its own $id, fragments resolve
				// against that resource's base per JSON Schema — the same
				// scope carve-out as OBI-D-16.
				*errs = append(*errs, fmt.Sprintf("%s.$ref: %q is a plain-name fragment; a same-document schema $ref is a JSON Pointer fragment (bare # or #/...), and the schemas map is the document's named-schema mechanism (OBI-D-05)", prefix, str))
			} else if (str == "#" || strings.HasPrefix(str, "#/")) && !inID && doc != nil {
				if !docPointerResolves(doc, strings.TrimPrefix(str, "#")) {
					*errs = append(*errs, fmt.Sprintf("%s.$ref: %q does not resolve within the document (OBI-D-16)", prefix, str))
				}
			}
		}
	}
	if v, ok := s["$id"]; ok {
		if str, ok := v.(string); ok && !wasInID && !referenceIsAbsolute(str) {
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
					walkSchema(errs, fmt.Sprintf("%s.%s.%s", prefix, k, sk), m[sk], doc, inID)
				}
			}
		case singleSchemaKeywords[k]:
			walkSchema(errs, prefix+"."+k, v, doc, inID)
		case arraySchemaKeywords[k]:
			if arr, ok := v.([]any); ok {
				for idx, item := range arr {
					walkSchema(errs, fmt.Sprintf("%s.%s[%d]", prefix, k, idx), item, doc, inID)
				}
			}
		}
	}
}
