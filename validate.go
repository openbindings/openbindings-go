package openbindings

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/openbindings/jsonata/go/syntax"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

type validateOptions struct {
	skipDocumentSchema bool
	versionDecided     bool
}

type validateOption func(*validateOptions)

// withoutDocumentSchemaValidation skips OBI-D-02 for a caller that has already
// validated, or deliberately does not gate on, the embedded document schema.
func withoutDocumentSchemaValidation() validateOption {
	return func(o *validateOptions) { o.skipDocumentSchema = true }
}

// withVersionDecided skips OBI-D-12 for a caller that decided it from the
// document's generic view before decoding.
func withVersionDecided() validateOption {
	return func(o *validateOptions) { o.versionDecided = true }
}

// Validate checks a document already in memory against every document rule
// this SDK can decide. It reports the per-rule evidence, the located findings,
// OBI-T-02 diagnostics, and the §10.5 conformance conclusion.
//
// The error is a *ValidationError listing every established violation, so
// `if _, err := iface.Validate(); err != nil` gates on violations. A nil error
// is not a conformance claim. A rule this SDK cannot decide is inconclusive,
// not violated, and the report's Conclusion says whether the document is
// conformant or conformance undetermined. OBI-D-01 is always inconclusive
// here, because it is decided on the exact input bytes, which a host object no
// longer carries; ValidateDocument decides it. OBI-D-13 is inconclusive for a
// document with bindings, because only each binding's governing binding
// specification decides it.
//
// A document declaring a version outside the supported set is not interpreted:
// Validate returns a *VersionRefusalError and no report (OBI-T-04).
func (i Interface) Validate() (ValidationReport, error) {
	if refusal := versionRefusalOf(i.OpenBindings); refusal != nil {
		return ValidationReport{}, refusal
	}
	var c ruleChecks
	c.inconclusive("OBI-D-01", "", "decided on the exact input bytes, which a host object no longer carries; ValidateDocument decides it")
	i.checkDocumentRules(&c, nil)
	return c.conclude()
}

// validateWithDocument is the violation gate alone, for callers that act on a
// document and need no report. docView, when supplied, is a generic view
// decoded from the document's exact bytes.
func (i Interface) validateWithDocument(docView any, opts ...validateOption) error {
	if refusal := versionRefusalOf(i.OpenBindings); refusal != nil {
		return refusal
	}
	var c ruleChecks
	i.checkDocumentRules(&c, docView, opts...)
	return c.violationError()
}

// ValidateDocument validates the exact input bytes of a document: OBI-D-01 on
// the bytes themselves, then every other document rule this SDK can decide.
// It returns the decoded document whenever the bytes decode into the document
// model, the report, and the same violation error Interface.Validate returns.
// Input that is not a JSON document at all is reported as a violation of
// OBI-D-01, with every other rule inconclusive.
//
// A document declaring a well-formed version outside the supported set is not
// interpreted: ValidateDocument returns a *VersionRefusalError and no report
// (OBI-T-04).
func ValidateDocument(data []byte) (*Interface, ValidationReport, error) {
	var c ruleChecks
	raw, err := decodeDocumentBytes(data)
	if err != nil {
		c.violated("OBI-D-01", "", fmt.Sprintf("not a JSON document this specification accepts: %v", err))
		c.inconclusiveRemaining("OBI-D-01", "the input is not a JSON document, so this rule was not checked")
		report, verr := c.conclude()
		return nil, report, verr
	}
	// OBI-T-04 precedes interpretation under this version's semantics, the
	// embedded document schema included. A missing or malformed version is
	// OBI-D-12's concern, decided with the other rules.
	if object, ok := raw.(map[string]any); ok {
		if version, ok := object["openbindings"].(string); ok {
			if refusal := versionRefusalOf(version); refusal != nil {
				return nil, ValidationReport{}, refusal
			}
		}
	}
	checkDeclaredVersion(&c, raw)
	validateAgainstOBISchema(&c, raw)
	var iface Interface
	if err := json.Unmarshal(data, &iface); err != nil {
		c.inconclusiveRemaining("OBI-D-01", fmt.Sprintf("the document could not be decoded into the document model, so this rule was not checked: %v", err))
		report, verr := c.conclude()
		return nil, report, verr
	}
	iface.checkDocumentRules(&c, raw, withoutDocumentSchemaValidation(), withVersionDecided())
	report, verr := c.conclude()
	return &iface, report, verr
}

// checkDeclaredVersion decides OBI-D-12 from a document's generic view, which
// holds even when the value is not a string and the document cannot decode.
func checkDeclaredVersion(c *ruleChecks, raw any) {
	object, _ := raw.(map[string]any)
	value, present := object["openbindings"]
	version, isString := value.(string)
	switch {
	case !present:
		c.violated("OBI-D-12", "openbindings", "required")
	case !isString:
		c.violated("OBI-D-12", "openbindings", fmt.Sprintf("must be a SemVer 2.0.0 string; got %s", jsonTypeName(value)))
	case strings.TrimSpace(version) == "":
		c.violated("OBI-D-12", "openbindings", "required")
	case !IsValidSemver(version):
		c.violated("OBI-D-12", "openbindings", fmt.Sprintf("%q is not a valid SemVer 2.0.0 string", version))
	}
}

// versionRefusalOf applies OBI-T-04 to a declared version. It returns nil for
// an accepted version and for a missing or malformed one, which is OBI-D-12's
// concern rather than a refusal.
func versionRefusalOf(version string) *VersionRefusalError {
	if !IsValidSemver(version) {
		return nil
	}
	msg, refused, err := versionRefusal(version)
	if err != nil || !refused {
		return nil
	}
	return &VersionRefusalError{Version: version, Reason: msg}
}

// checkDocumentRules records evidence for OBI-D-02 through OBI-D-19 on a
// decoded document whose version has already been accepted. docView, when
// supplied, is a generic view decoded from the document's exact bytes.
func (i Interface) checkDocumentRules(c *ruleChecks, docView any, opts ...validateOption) {
	var o validateOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	// A generic view of the whole document serves OBI-D-02 and OBI-D-16. It is
	// produced at most once, and only when a check needs it. The lossless
	// marshal of a decoded document does not fail in practice; if it does, the
	// checks that need the view record themselves inconclusive.
	viewed := docView != nil
	view := func() any {
		if !viewed {
			viewed = true
			if data, err := json.Marshal(i); err == nil {
				_ = jsonvalue.Unmarshal(data, &docView)
			}
		}
		return docView
	}

	// OBI-D-12: the openbindings field is a valid SemVer 2.0.0 string.
	if !o.versionDecided {
		if strings.TrimSpace(i.OpenBindings) == "" {
			c.violated("OBI-D-12", "openbindings", "required")
		} else if !IsValidSemver(i.OpenBindings) {
			c.violated("OBI-D-12", "openbindings", fmt.Sprintf("%q is not a valid SemVer 2.0.0 string", i.OpenBindings))
		}
	}

	// Same-document schema $refs are resolved against the document root for
	// OBI-D-16; the view is only needed when there is such a reference.
	var refView any
	if interfaceHasDocumentSchemaRef(i) {
		refView = view()
	}
	validSchemaShapes := make(map[string]bool)

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
		validateIdent(c, "schemas key", k)
		validateSchemaWellFormedness(c, fmt.Sprintf("schemas[%q]", k), i.Schemas[k], validSchemaShapes)
		walkSchema(c, fmt.Sprintf("schemas[%q]", k), i.Schemas[k], refView, false)
	}

	if i.Operations == nil {
		c.violated("OBI-D-02", "operations", "required")
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
		validateIdent(c, "operations key", k)

		// Alias checks (OBI-D-04 collisions, OBI-D-03 alias pattern).
		// Per OBI-D-04, an operation's "identifiers" = its key + aliases. All
		// identifiers across all operations must be distinct, including:
		//   - alias collides with another operation's key
		//   - same alias listed by two operations
		//   - alias listed twice within the same operation's array
		//   - alias equal to the operation's own key
		aliasPath := fmt.Sprintf("operations[%q].aliases", k)
		seenAlias := map[string]struct{}{}
		for _, a := range op.Aliases {
			if strings.TrimSpace(a) == "" {
				c.violated("OBI-D-03", aliasPath, "must not contain empty strings")
				continue
			}
			validateIdent(c, aliasPath, a)
			if a == k {
				c.violated("OBI-D-04", aliasPath, fmt.Sprintf("%q duplicates the operation's own key", a))
				continue
			}
			if _, dup := seenAlias[a]; dup {
				c.violated("OBI-D-04", aliasPath, fmt.Sprintf("%q is listed more than once", a))
				continue
			}
			seenAlias[a] = struct{}{}
			if _, isOpKey := opKeySet[a]; isOpKey {
				c.violated("OBI-D-04", aliasPath, fmt.Sprintf("%q conflicts with operation key %q", a, a))
				continue
			}
			if owner, ok := aliasOwner[a]; ok && owner != k {
				c.violated("OBI-D-04", aliasPath, fmt.Sprintf("%q is also an alias of %q", a, owner))
				continue
			}
			aliasOwner[a] = k
		}

		// Check operation input/output schemas for well-formedness (OBI-D-17)
		// and walk them for OBI-D-05/D-06/D-07/D-16.
		if op.Input != nil {
			validateSchemaWellFormedness(c, fmt.Sprintf("operations[%q].input", k), op.Input, validSchemaShapes)
			walkSchema(c, fmt.Sprintf("operations[%q].input", k), op.Input, refView, false)
		} else if op.InputPresent {
			c.violated("OBI-D-17", fmt.Sprintf("operations[%q].input", k), "a schema is a JSON Schema 2020-12 object or boolean; got null")
		}
		if op.Output != nil {
			validateSchemaWellFormedness(c, fmt.Sprintf("operations[%q].output", k), op.Output, validSchemaShapes)
			walkSchema(c, fmt.Sprintf("operations[%q].output", k), op.Output, refView, false)
		} else if op.OutputPresent {
			c.violated("OBI-D-17", fmt.Sprintf("operations[%q].output", k), "a schema is a JSON Schema 2020-12 object or boolean; got null")
		}

		// OBI-D-03: example keys must match the identifier pattern.
		exKeys := make([]string, 0, len(op.Examples))
		for ek := range op.Examples {
			exKeys = append(exKeys, ek)
		}
		sort.Strings(exKeys)
		for _, ek := range exKeys {
			validateIdent(c, fmt.Sprintf("operations[%q].examples key", k), ek)
		}

		diagnoseUnknownFields(c, fmt.Sprintf("operations[%q]", k), op.Unknown)
		for _, ek := range exKeys {
			diagnoseUnknownFields(c, fmt.Sprintf("operations[%q].examples[%q]", k, ek), op.Examples[ek].Unknown)
		}
	}

	// Validate named consumption points. Dependency keys share the document
	// identifier grammar (OBI-D-03), and operation values resolve only against
	// literal operation-map keys, never aliases (OBI-D-19). The structural
	// requirements for each entry and bindingSpecs are also enforced by the
	// embedded document schema (OBI-D-02).
	depKeys := make([]string, 0, len(i.Dependencies))
	for k := range i.Dependencies {
		depKeys = append(depKeys, k)
	}
	sort.Strings(depKeys)
	for _, k := range depKeys {
		validateIdent(c, "dependencies key", k)
		dependency := i.Dependencies[k]
		if strings.TrimSpace(dependency.Operation) == "" {
			c.violated("OBI-D-02", fmt.Sprintf("dependencies[%q].operation", k), "required")
		} else if _, ok := i.Operations[dependency.Operation]; !ok {
			c.violated("OBI-D-19", fmt.Sprintf("dependencies[%q].operation", k), fmt.Sprintf("references unknown operation key %q", dependency.Operation))
		}
		if dependency.BindingSpecs != nil {
			bindingSpecsPath := fmt.Sprintf("dependencies[%q].bindingSpecs", k)
			if len(dependency.BindingSpecs) == 0 {
				c.violated("OBI-D-02", bindingSpecsPath, "must contain at least one item")
			}
			seen := make(map[string]bool, len(dependency.BindingSpecs))
			for _, bindingSpec := range dependency.BindingSpecs {
				if bindingSpec == "" {
					c.violated("OBI-D-02", bindingSpecsPath, "must not contain an empty string")
				} else if seen[bindingSpec] {
					c.violated("OBI-D-02", bindingSpecsPath, fmt.Sprintf("%q is listed more than once", bindingSpec))
				}
				seen[bindingSpec] = true
			}
		}
		diagnoseUnknownFields(c, fmt.Sprintf("dependencies[%q]", k), dependency.Unknown)
	}

	// Validate sources.
	srcKeys := make([]string, 0, len(i.Sources))
	for k := range i.Sources {
		srcKeys = append(srcKeys, k)
	}
	sort.Strings(srcKeys)
	for _, k := range srcKeys {
		// OBI-D-03: source keys must match the identifier pattern.
		validateIdent(c, "sources key", k)
		src := i.Sources[k]
		if strings.TrimSpace(src.BindingSpec) == "" {
			c.violated("OBI-D-02", fmt.Sprintf("sources[%q].bindingSpec", k), "required")
		}
		hasLocation := strings.TrimSpace(src.Location) != ""
		hasContent := src.Content != nil
		if !hasLocation && !hasContent {
			c.violated("OBI-D-02", fmt.Sprintf("sources[%q]", k), "must have location or content")
		}
		// OBI-D-05: sources[*].location must be a well-formed, absolute reference
		// (absolute URI or a bindingSpec-defined absolute address; never relative).
		if hasLocation {
			validateLocation(c, fmt.Sprintf("sources[%q].location", k), src.Location)
		}
		diagnoseUnknownFields(c, fmt.Sprintf("sources[%q]", k), src.Unknown)
	}

	// Validate transforms. OBI-D-18: every value in the transforms map parses
	// as a syntactically valid expression of the pinned transform language
	// (the incorporated JSONata 2.1 documentation). Parse-only:
	// evaluation failures (undefined results, dynamic errors) remain
	// evaluation outcomes under OBI-T-10.
	trKeys := make([]string, 0, len(i.Transforms))
	for k := range i.Transforms {
		trKeys = append(trKeys, k)
	}
	sort.Strings(trKeys)
	for _, k := range trKeys {
		// OBI-D-03: transform keys must match the identifier pattern.
		validateIdent(c, "transforms key", k)
		validateTransformExpression(c, fmt.Sprintf("transforms[%q]", k), i.Transforms[k])
	}

	// Validate bindings.
	bndKeys := make([]string, 0, len(i.Bindings))
	for k := range i.Bindings {
		bndKeys = append(bndKeys, k)
	}
	sort.Strings(bndKeys)
	for _, k := range bndKeys {
		// OBI-D-03: binding keys must match the identifier pattern.
		validateIdent(c, "bindings key", k)
		b := i.Bindings[k]
		if b.Preference != nil && (math.IsNaN(*b.Preference) || math.IsInf(*b.Preference, 0) || math.Trunc(*b.Preference) != *b.Preference || math.Abs(*b.Preference) > 9007199254740991) {
			c.violated("OBI-D-02", fmt.Sprintf("bindings[%q].preference", k), "must be a safe integer")
		}
		// OBI-D-08: bindings[*].operation must reference an existing operation.
		if strings.TrimSpace(b.Operation) == "" {
			c.violated("OBI-D-02", fmt.Sprintf("bindings[%q].operation", k), "required")
		} else if _, ok := i.Operations[b.Operation]; !ok {
			c.violated("OBI-D-08", fmt.Sprintf("bindings[%q].operation", k), fmt.Sprintf("references unknown operation %q", b.Operation))
		}
		// OBI-D-09: bindings[*].source must reference an existing source.
		if strings.TrimSpace(b.Source) == "" {
			c.violated("OBI-D-02", fmt.Sprintf("bindings[%q].source", k), "required")
		} else if _, ok := i.Sources[b.Source]; !ok {
			c.violated("OBI-D-09", fmt.Sprintf("bindings[%q].source", k), fmt.Sprintf("references unknown source %q", b.Source))
		}

		// Validate transform references (OBI-D-10) and inline transform
		// parse-validity (OBI-D-18).
		if b.InputTransform != nil {
			if b.InputTransform.IsRef() {
				if problem := transformRefProblem(b.InputTransform.Ref, i.Transforms); problem != "" {
					c.violated("OBI-D-10", fmt.Sprintf("bindings[%q].inputTransform.$ref", k), problem)
				}
			} else {
				validateTransformExpression(c, fmt.Sprintf("bindings[%q].inputTransform", k), b.InputTransform.Inline)
			}
		}
		if b.OutputTransform != nil {
			if b.OutputTransform.IsRef() {
				if problem := transformRefProblem(b.OutputTransform.Ref, i.Transforms); problem != "" {
					c.violated("OBI-D-10", fmt.Sprintf("bindings[%q].outputTransform.$ref", k), problem)
				}
			} else {
				validateTransformExpression(c, fmt.Sprintf("bindings[%q].outputTransform", k), b.OutputTransform.Inline)
			}
		}

		diagnoseUnknownFields(c, fmt.Sprintf("bindings[%q]", k), b.Unknown)
	}

	// OBI-D-13: whether a binding is identifiable from itself and its source
	// alone is defined by the source's governing binding specification, so the
	// core cannot decide it.
	if len(i.Bindings) > 0 {
		c.inconclusive("OBI-D-13", "bindings", "whether each binding identifies its target is decided by its binding specification, not the core")
	}

	diagnoseUnknownFields(c, "", i.Unknown)

	// OBI-D-02: validate the document against openbindings.schema.json.
	if !o.skipDocumentSchema {
		validateAgainstOBISchema(c, view())
	}

	// OBI-D-11: validate every provided example against its operation's schema,
	// where that schema's graph resolves entirely within the document.
	checkExamples(c, i, view)
}

// diagnoseUnknownFields surfaces OBI-T-02's advice for unknown non-`x-`
// fields of an OBI-defined object. They are ignored, never rejected.
func diagnoseUnknownFields(c *ruleChecks, path string, unknown map[string]json.RawMessage) {
	if len(unknown) == 0 {
		return
	}
	keys := make([]string, 0, len(unknown))
	for k := range unknown {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	noun := "fields"
	if len(keys) == 1 {
		noun = "field"
	}
	c.diagnose("OBI-T-02", path, fmt.Sprintf("unknown %s ignored: %s; extensions use the x- prefix", noun, strings.Join(keys, ", ")))
}

func interfaceHasDocumentSchemaRef(i Interface) bool {
	for _, schema := range i.Schemas {
		if schemaHasDocumentRef(schema) {
			return true
		}
	}
	for _, operation := range i.Operations {
		if schemaHasDocumentRef(operation.Input) || schemaHasDocumentRef(operation.Output) {
			return true
		}
	}
	return false
}

func schemaHasDocumentRef(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		if ref, ok := value["$ref"].(string); ok && strings.HasPrefix(ref, "#") {
			return true
		}
		for _, child := range value {
			if schemaHasDocumentRef(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if schemaHasDocumentRef(child) {
				return true
			}
		}
	}
	return false
}

// docPointerResolves reports whether an RFC 6901 JSON Pointer (the fragment
// with its leading # removed) resolves to an existing location in doc, the
// generic-JSON view of the whole OBI document. Existence only: OBI-D-16 does
// not type-check the target.
func docPointerResolves(doc any, pointer string) bool {
	_, ok := resolveDocPointer(doc, pointer)
	return ok
}

// resolveDocPointer evaluates an RFC 6901 JSON Pointer (a same-document
// fragment with its leading # removed) against doc, the generic-JSON view of
// the whole OBI document, returning the addressed value.
func resolveDocPointer(doc any, pointer string) (any, bool) {
	if pointer == "" {
		return doc, true // bare # addresses the document root
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, false
	}
	cur := doc
	for _, tok := range strings.Split(pointer[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[tok]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(tok)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// validateTransformExpression records an OBI-D-18 violation when expr does
// not parse as a syntactically valid expression of the pinned transform
// language (§5.5: the incorporated JSONata 2.1 documentation).
// Parse-only: membership in the language, not
// success of evaluation — undefined results and dynamic errors remain
// evaluation outcomes.
func validateTransformExpression(c *ruleChecks, prefix, expr string) {
	if !jsonataParses(expr) {
		c.violated("OBI-D-18", prefix, "not a syntactically valid JSONata expression")
	}
}

// jsonataParses reports whether expr parses under the bundled JSONata
// syntax package, without importing the evaluator or initializing its
// standard library. The parser is only ever
// handed document-supplied strings, so a parser panic is treated as a parse
// failure rather than crashing document validation.
func jsonataParses(expr string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	return syntax.Validate(expr) == nil
}

// transformRefProblem states why a named-transform $ref does not resolve to a
// key in the document's transforms map (OBI-D-10), or returns "" when it does.
func transformRefProblem(ref string, transforms map[string]Transform) string {
	const prefix = "#/transforms/"
	if !strings.HasPrefix(ref, prefix) {
		return fmt.Sprintf("must start with %q", prefix)
	}
	name := strings.TrimPrefix(ref, prefix)
	if name == "" {
		return "transform name is empty"
	}
	if _, ok := transforms[name]; !ok {
		return fmt.Sprintf("references unknown transform %q", name)
	}
	return ""
}

// identPattern enforces OBI-D-03: every map key and every operation alias must
// match. The grammar permits a leading digit (2fa.verify): names are opaque data
// labels, not host-language identifiers, so only the start character is
// constrained to an alphanumeric or underscore.
var identPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// validateIdent records an OBI-D-03 violation if id does not match the identifier pattern.
func validateIdent(c *ruleChecks, prefix, id string) {
	if !identPattern.MatchString(id) {
		c.violated("OBI-D-03", prefix, fmt.Sprintf("%q does not match identifier pattern ^[A-Za-z0-9_][A-Za-z0-9_.-]*$", id))
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
func validateURIRef(c *ruleChecks, prefix, raw string) {
	if raw == "" {
		return
	}
	if !screenURIChars(c, prefix, raw) {
		return
	}
	if _, err := url.Parse(raw); err != nil {
		c.violated("OBI-D-05", prefix, fmt.Sprintf("%q is not a well-formed URI reference", raw))
	}
}

// screenURIChars reports whether raw contains only characters permitted in a
// URI reference per RFC 3986, with well-formed percent-encoding. On the first
// departure it records an OBI-D-05 violation and returns false. net/url's
// parser is too permissive (it accepts whitespace, backticks, and angle
// brackets), so this screen runs before any structural parse.
func screenURIChars(c *ruleChecks, prefix, raw string) bool {
	if problem := uriCharsProblem(raw); problem != "" {
		c.violated("OBI-D-05", prefix, problem)
		return false
	}
	return true
}

// uriCharsProblem states the first way raw departs from RFC 3986's character
// set and percent-encoding, or returns "" when it does not.
func uriCharsProblem(raw string) string {
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '%' {
			if i+2 >= len(raw) || !isHex(raw[i+1]) || !isHex(raw[i+2]) {
				return fmt.Sprintf("%q contains malformed percent-encoding", raw)
			}
			i += 2
			continue
		}
		if !uriRefAllowedChars[c] {
			return fmt.Sprintf("%q contains character %q not allowed in a URI reference", raw, c)
		}
	}
	return ""
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
// absolute URI or a binding-specification-defined absolute address, never a
// relative reference.
//
// Three cases, following the rule's validation note:
//   - A location with no ':' before its first '/', '?', or '#' is relative in
//     form and violates the rule everywhere (./openapi.json, bare example.com).
//   - A location in hierarchical URI form (scheme://...) claims RFC 3986 URI
//     form and is held to it.
//   - Any other colon-bearing location is satisfied when it is a well-formed
//     absolute URI (grpc.example.com:443 parses as one). One that is not
//     (10.0.0.1:443, [::1]:443) can only be an address its binding
//     specification defines, which the core cannot decide, so the check is
//     left inconclusive rather than passed.
func validateLocation(c *ruleChecks, prefix, raw string) {
	if raw == "" {
		return
	}
	if isRelativeReference(raw) {
		c.violated("OBI-D-05", prefix, fmt.Sprintf("%q must be an absolute URI or a binding-specification-defined absolute address, not a relative reference; a local artifact can be embedded as the source's content instead (a file:// URL is machine-coupled and resolves only on the authoring machine)", raw))
		return
	}
	if isHierarchicalURIForm(raw) {
		if !screenURIChars(c, prefix, raw) {
			return
		}
		if _, err := url.Parse(raw); err != nil {
			c.violated("OBI-D-05", prefix, fmt.Sprintf("%q is not a well-formed URI reference", raw))
		}
		return
	}
	if isWellFormedAbsoluteURI(raw) {
		return
	}
	c.inconclusive("OBI-D-05", prefix, fmt.Sprintf("%q is neither relative nor a well-formed URI; whether it is an absolute address its binding specification defines is that specification's to decide", raw))
}

// isWellFormedAbsoluteURI reports whether raw is an RFC 3986 absolute URI:
// a scheme, then only permitted characters, parsing structurally.
func isWellFormedAbsoluteURI(raw string) bool {
	if !hasURIScheme(raw) || uriCharsProblem(raw) != "" {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.IsAbs()
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
func walkSchema(c *ruleChecks, prefix string, schema any, doc any, inID bool) {
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
			c.violated("OBI-D-06", prefix+".$schema", fmt.Sprintf("%q must equal %q", str, draft202012URI))
		}
	}
	if _, ok := s["$vocabulary"]; ok {
		c.violated("OBI-D-07", prefix, "$vocabulary keyword is forbidden in OBI documents")
	}
	// The dynamic pair does not appear at OBI positions at all: dynamic
	// resolution follows the runtime dynamic scope rather than the document
	// (§10 clause 2). Inside a schema declaring its own $id, both are that
	// resource's internal business — the same scope carve-out as $ref/$anchor.
	if !inID {
		if _, ok := s["$dynamicRef"]; ok {
			c.violated("OBI-D-05", prefix, "$dynamicRef does not appear at OBI positions; dynamic resolution follows the runtime dynamic scope rather than the document")
		}
		if _, ok := s["$dynamicAnchor"]; ok {
			c.violated("OBI-D-05", prefix, "$dynamicAnchor does not appear at OBI positions; it would be a second named-schema mechanism competing with the schemas map, as $anchor would")
		}
	}
	if v, ok := s["$ref"]; ok {
		if str, ok := v.(string); ok {
			validateURIRef(c, prefix+".$ref", str)
			if !strings.HasPrefix(str, "#") && !referenceIsAbsolute(str) {
				c.violated("OBI-D-05", prefix+".$ref", fmt.Sprintf("%q must be a same-document fragment or an absolute URI, not a relative reference", str))
			} else if strings.HasPrefix(str, "#") && !inID && strings.Contains(str, "%") {
				// Literal form (§7): same-document fragments are written with
				// the pointer's characters unencoded, so every addressable
				// location has exactly one conformant spelling. A percent-encoded
				// fragment is not a conformant OBI-defined reference — reported
				// here rather than silently decoded and resolved. Inside a schema
				// declaring its own $id the fragment is resource-internal and
				// resolves per JSON Schema 2020-12, so this gate is OBI-position
				// only (!inID).
				c.violated("OBI-D-05", prefix+".$ref", fmt.Sprintf("%q is not in literal form; a same-document fragment is written with the pointer's characters unencoded (percent-encoding is not a conformant OBI reference)", str))
			} else if strings.HasPrefix(str, "#") && str != "#" && !strings.HasPrefix(str, "#/") && !inID {
				// Inside a schema declaring its own $id, fragments resolve
				// against that resource's base per JSON Schema — the same
				// scope carve-out as OBI-D-16.
				c.violated("OBI-D-05", prefix+".$ref", fmt.Sprintf("%q is a plain-name fragment; a same-document schema $ref is a JSON Pointer fragment (bare # or #/...), and the schemas map is the document's named-schema mechanism", str))
			} else if (str == "#" || strings.HasPrefix(str, "#/")) && !inID {
				if doc == nil {
					c.inconclusive("OBI-D-16", prefix+".$ref", fmt.Sprintf("%q was not resolved: no generic view of the document could be produced", str))
				} else if !docPointerResolves(doc, strings.TrimPrefix(str, "#")) {
					c.violated("OBI-D-16", prefix+".$ref", fmt.Sprintf("%q does not resolve within the document", str))
				}
			}
		}
	}
	if v, ok := s["$id"]; ok {
		if str, ok := v.(string); ok && !wasInID && !referenceIsAbsolute(str) {
			c.violated("OBI-D-05", prefix+".$id", fmt.Sprintf("%q must be an absolute URI", str))
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
					walkSchema(c, fmt.Sprintf("%s.%s.%s", prefix, k, sk), m[sk], doc, inID)
				}
			}
		case singleSchemaKeywords[k]:
			walkSchema(c, prefix+"."+k, v, doc, inID)
		case arraySchemaKeywords[k]:
			if arr, ok := v.([]any); ok {
				for idx, item := range arr {
					walkSchema(c, fmt.Sprintf("%s.%s[%d]", prefix, k, idx), item, doc, inID)
				}
			}
		}
	}
}
