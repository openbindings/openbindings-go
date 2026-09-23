package openbindings

import (
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	json "github.com/openbindings/openbindings-go/internal/thirdparty/jsoncodec"

	"github.com/openbindings/jsonata/go/syntax"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

// Validate checks a document already in memory against every document rule
// this SDK can decide. It reports the per-rule evidence, the located findings,
// OBI-T-02 diagnostics, and the §10.5 conformance conclusion.
//
// The rules judge the document the host object encodes, exactly as
// ValidateDocument judges bytes. OBI-D-01 is always inconclusive here,
// because it is decided on the exact input bytes, which a host object no
// longer carries; ValidateDocument decides it.
//
// The error is a *ValidationError listing every established violation, so
// `if _, err := iface.Validate(); err != nil` gates on violations. A nil error
// is not a conformance claim. A rule this SDK cannot decide is inconclusive,
// not violated, and the report's Conclusion says whether the document is
// conformant or conformance undetermined. OBI-D-13 is inconclusive for a
// document with bindings, because only each binding's governing binding
// specification decides it.
//
// A document declaring a version outside the supported set is not interpreted:
// Validate returns a *VersionRefusalError and no report (OBI-T-04). A host
// object that cannot be encoded returns that error and no report.
func (i Interface) Validate() (ValidationReport, error) {
	if refusal := versionRefusalOf(i.OpenBindings); refusal != nil {
		return ValidationReport{}, refusal
	}
	view, err := documentView(i)
	if err != nil {
		return ValidationReport{}, err
	}
	var c ruleChecks
	c.inconclusive("OBI-D-01", "", "decided on the exact input bytes, which a host object no longer carries; ValidateDocument decides it")
	checkDocument(&c, view)
	return c.conclude()
}

// ValidateDocument validates the exact input bytes of a document: OBI-D-01 on
// the bytes themselves, then every other document rule this SDK can decide on
// the JSON they hold. It returns the decoded document when the document model
// can carry it exactly (see LosslessFields), the report, and the same
// violation error Interface.Validate returns. The rules never depend on that
// decoding: a document the model cannot carry is still judged in full. Input
// that is not a JSON document at all is reported as a violation of OBI-D-01,
// with every other rule inconclusive.
//
// A document declaring a well-formed version outside the supported set is not
// interpreted: ValidateDocument returns a *VersionRefusalError and no report
// (OBI-T-04).
func ValidateDocument(data []byte) (*Interface, ValidationReport, error) {
	var c ruleChecks
	view, err := decodeDocumentBytes(data)
	if err != nil {
		c.violated("OBI-D-01", "", fmt.Sprintf("not a JSON document this specification accepts: %v", err))
		c.inconclusiveExcept("the input is not a JSON document, so this rule was not checked", "OBI-D-01")
		report, verr := c.conclude()
		return nil, report, verr
	}
	if refusal := declaredVersionRefusal(view); refusal != nil {
		return nil, ValidationReport{}, refusal
	}
	checkDocument(&c, view)
	report, verr := c.conclude()
	var iface Interface
	if err := json.Unmarshal(data, &iface); err != nil {
		return nil, report, verr
	}
	return &iface, report, verr
}

// documentView encodes a host document and decodes the generic JSON view the
// document rules judge.
func documentView(i Interface) (any, error) {
	data, err := jsonvalue.Marshal(i)
	if err != nil {
		return nil, fmt.Errorf("openbindings: encode interface: %w", err)
	}
	var view any
	if err := jsonvalue.Unmarshal(data, &view); err != nil {
		return nil, fmt.Errorf("openbindings: decode encoded interface: %w", err)
	}
	return view, nil
}

// checkDeclaredVersion decides OBI-D-12 from a document's generic view, which
// holds even when the value is not a string.
func checkDeclaredVersion(c *ruleChecks, view any) {
	object, _ := view.(map[string]any)
	value, present := object["openbindings"]
	version, isString := value.(string)
	switch {
	case !present:
		c.violated("OBI-D-12", "", "missing the required openbindings member")
	case !isString:
		c.violated("OBI-D-12", "/openbindings", fmt.Sprintf("must be a SemVer 2.0.0 string; got %s", jsonTypeName(value)))
	case !IsValidSemver(version):
		c.violated("OBI-D-12", "/openbindings", fmt.Sprintf("%q is not a valid SemVer 2.0.0 string", version))
	}
}

// declaredVersionRefusal applies OBI-T-04 to the version a document's generic
// view declares. The decision precedes interpretation under this version's
// semantics, the embedded document schema included. A missing or malformed
// version is OBI-D-12's concern, decided with the other rules, so it is not a
// refusal.
func declaredVersionRefusal(view any) *VersionRefusalError {
	object, _ := view.(map[string]any)
	version, ok := object["openbindings"].(string)
	if !ok {
		return nil
	}
	return versionRefusalOf(version)
}

// versionRefusalOf applies OBI-T-04 to a declared version. It returns nil for
// an accepted version and for a malformed one, which is OBI-D-12's concern
// rather than a refusal.
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

// Known members of each OBI-defined object, for OBI-T-02's diagnostics, are
// the typed members of the document model.
var (
	rootMembersKnown         = membersOf(reflect.TypeFor[Interface]()).typed
	operationMembersKnown    = membersOf(reflect.TypeFor[Operation]()).typed
	exampleMembersKnown      = membersOf(reflect.TypeFor[OperationExample]()).typed
	dependencyMembersKnown   = membersOf(reflect.TypeFor[DependencyEntry]()).typed
	sourceMembersKnown       = membersOf(reflect.TypeFor[Source]()).typed
	bindingMembersKnown      = membersOf(reflect.TypeFor[BindingEntry]()).typed
	transformRefMembersKnown = membersOf(reflect.TypeFor[TransformReference]()).typed
)

// Rules that judge schema positions, and those that judge an operation's
// contents.
var (
	schemaPositionRules = []string{"OBI-D-05", "OBI-D-06", "OBI-D-07", "OBI-D-16", "OBI-D-17"}
	operationRules      = append([]string{"OBI-D-04", "OBI-D-11"}, schemaPositionRules...)
)

// checkDocument records evidence for OBI-D-02 through OBI-D-19 on the generic
// view of a document whose version has already been accepted.
//
// Each rule judges the members it quantifies over. A member that is absent is
// outside a rule's domain; its absence, where the document schema requires
// the member, is OBI-D-02's violation. A member that is present with the
// wrong JSON type is also OBI-D-02's violation, and the rules that needed its
// contents are inconclusive at its position: nothing was established about
// them there. Every other position is still judged.
func checkDocument(c *ruleChecks, view any) {
	checkDeclaredVersion(c, view)
	validateAgainstOBISchema(c, view)

	root, ok := view.(map[string]any)
	if !ok {
		c.inconclusiveExcept("the document is not a JSON object, so this rule was not checked", "OBI-D-01", "OBI-D-02", "OBI-D-12")
		return
	}
	d := documentCheck{c: c, view: view, wellFormed: map[string]bool{}}

	schemas, _ := d.members(root, "schemas", "", append([]string{"OBI-D-03"}, schemaPositionRules...)...)
	for _, key := range sortedKeys(schemas) {
		path := jsonpointer.Format("schemas", key)
		validateIdent(c, path, key)
		d.checkSchema(path, schemas[key])
	}

	operations, operationsKnown := d.members(root, "operations", "", append([]string{"OBI-D-03"}, operationRules...)...)
	d.checkOperations(operations)

	transforms, transformsKnown := d.members(root, "transforms", "", "OBI-D-03", "OBI-D-10", "OBI-D-18")
	for _, key := range sortedKeys(transforms) {
		path := jsonpointer.Format("transforms", key)
		validateIdent(c, path, key)
		if expression, ok := transforms[key].(string); ok {
			validateTransformExpression(c, path, expression)
		} else {
			c.inconclusive("OBI-D-18", path, "not a string, so it was not parsed as a transform expression")
		}
	}

	dependencies, _ := d.members(root, "dependencies", "", "OBI-D-03", "OBI-D-19")
	for _, key := range sortedKeys(dependencies) {
		path := jsonpointer.Format("dependencies", key)
		validateIdent(c, path, key)
		dependency, ok := d.object(dependencies[key], path, "OBI-D-19")
		if !ok {
			continue
		}
		d.checkReference(dependency, path, "operation", "OBI-D-19", operations, operationsKnown, "operation key")
		diagnoseUnknownFields(c, path, dependency, dependencyMembersKnown)
	}

	sources, sourcesKnown := d.members(root, "sources", "", "OBI-D-03", "OBI-D-05", "OBI-D-09")
	for _, key := range sortedKeys(sources) {
		path := jsonpointer.Format("sources", key)
		validateIdent(c, path, key)
		source, ok := d.object(sources[key], path, "OBI-D-05")
		if !ok {
			continue
		}
		if value, present := source["location"]; present {
			if location, ok := value.(string); ok {
				validateLocation(c, jsonpointer.Format("sources", key, "location"), location)
			} else {
				c.inconclusive("OBI-D-05", jsonpointer.Format("sources", key, "location"), "not a string, so it was not checked as a location")
			}
		}
		diagnoseUnknownFields(c, path, source, sourceMembersKnown)
	}

	bindings, _ := d.members(root, "bindings", "", "OBI-D-03", "OBI-D-08", "OBI-D-09", "OBI-D-10", "OBI-D-18")
	for _, key := range sortedKeys(bindings) {
		path := jsonpointer.Format("bindings", key)
		validateIdent(c, path, key)
		binding, ok := d.object(bindings[key], path, "OBI-D-08", "OBI-D-09", "OBI-D-10", "OBI-D-18")
		if !ok {
			continue
		}
		d.checkReference(binding, path, "operation", "OBI-D-08", operations, operationsKnown, "operation")
		d.checkReference(binding, path, "source", "OBI-D-09", sources, sourcesKnown, "source")
		for _, member := range []string{"inputTransform", "outputTransform"} {
			if value, present := binding[member]; present {
				d.checkBindingTransform(jsonpointer.Format("bindings", key, member), value, transforms, transformsKnown)
			}
		}
		diagnoseUnknownFields(c, path, binding, bindingMembersKnown)
	}

	// OBI-D-13: whether a binding is identifiable from itself and its source
	// alone is defined by the source's governing binding specification, so the
	// core cannot decide it.
	if value, present := root["bindings"]; present {
		if entries, ok := value.(map[string]any); !ok || len(entries) > 0 {
			c.inconclusive("OBI-D-13", "/bindings", "whether each binding identifies its target is decided by its binding specification, not the core")
		}
	}

	diagnoseUnknownFields(c, "", root, rootMembersKnown)

	// OBI-D-11: every provided example validates against its operation's
	// schema, where that schema's graph resolves entirely within the document.
	checkExamples(c, view, operations)
}

// documentCheck carries one document's view through its rule checks.
type documentCheck struct {
	c    *ruleChecks
	view any

	// wellFormed remembers schema objects already found well-formed, keyed by
	// their encoding, so a schema repeated across positions is checked once.
	wellFormed map[string]bool
}

// members returns the object member name of parent. known is false when the
// member is present but not an object: the rules that needed its entries are
// then inconclusive at its position. An absent member is known and empty.
func (d *documentCheck) members(parent map[string]any, name, parentPath string, rules ...string) (entries map[string]any, known bool) {
	value, present := parent[name]
	if !present {
		return nil, true
	}
	object, ok := d.object(value, parentPath+jsonpointer.Format(name), rules...)
	return object, ok
}

// object returns value as an object, or leaves each named rule inconclusive
// at path when it is not one.
func (d *documentCheck) object(value any, path string, rules ...string) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		for _, rule := range rules {
			d.c.inconclusive(rule, path, fmt.Sprintf("not an object (%s), so this rule could not be checked here", jsonTypeName(value)))
		}
	}
	return object, ok
}

// checkReference decides a referential rule (OBI-D-08, OBI-D-09, OBI-D-19)
// for the member name of an entry: its value is a key of targets.
func (d *documentCheck) checkReference(entry map[string]any, entryPath, name, rule string, targets map[string]any, targetsKnown bool, noun string) {
	value, present := entry[name]
	if !present {
		return
	}
	path := entryPath + jsonpointer.Format(name)
	key, ok := value.(string)
	switch {
	case !ok:
		d.c.inconclusive(rule, path, "not a string, so it was not resolved")
	case !targetsKnown:
		d.c.inconclusive(rule, path, fmt.Sprintf("%q was not resolved: the map it names a key of is not an object", key))
	default:
		if _, found := targets[key]; !found {
			d.c.violated(rule, path, fmt.Sprintf("references unknown %s %q", noun, key))
		}
	}
}

// checkOperations records evidence for every operation: key and alias names
// (OBI-D-03, OBI-D-04), schemas, and example keys.
func (d *documentCheck) checkOperations(operations map[string]any) {
	keys := sortedKeys(operations)
	aliasOwner := map[string]string{}
	for _, key := range keys {
		path := jsonpointer.Format("operations", key)
		validateIdent(d.c, path, key)
		operation, ok := d.object(operations[key], path, operationRules...)
		if !ok {
			continue
		}

		// OBI-D-04: an operation's identifiers are its key plus its aliases,
		// and every identifier in the document is distinct.
		if value, present := operation["aliases"]; present {
			aliases, ok := value.([]any)
			if !ok {
				for _, rule := range []string{"OBI-D-03", "OBI-D-04"} {
					d.c.inconclusive(rule, path+jsonpointer.Format("aliases"), fmt.Sprintf("not an array (%s), so its aliases could not be checked", jsonTypeName(value)))
				}
			}
			seen := map[string]bool{}
			for index, element := range aliases {
				aliasPath := jsonpointer.Format("operations", key, "aliases", strconv.Itoa(index))
				alias, ok := element.(string)
				if !ok {
					for _, rule := range []string{"OBI-D-03", "OBI-D-04"} {
						d.c.inconclusive(rule, aliasPath, fmt.Sprintf("not a string (%s), so it could not be checked as an alias", jsonTypeName(element)))
					}
					continue
				}
				validateIdent(d.c, aliasPath, alias)
				switch owner, owned := aliasOwner[alias]; {
				case alias == key:
					d.c.violated("OBI-D-04", aliasPath, fmt.Sprintf("%q duplicates the operation's own key", alias))
				case seen[alias]:
					d.c.violated("OBI-D-04", aliasPath, fmt.Sprintf("%q is listed more than once", alias))
				case hasKey(operations, alias):
					d.c.violated("OBI-D-04", aliasPath, fmt.Sprintf("%q conflicts with operation key %q", alias, alias))
				case owned && owner != key:
					d.c.violated("OBI-D-04", aliasPath, fmt.Sprintf("%q is also an alias of %q", alias, owner))
				default:
					aliasOwner[alias] = key
				}
				seen[alias] = true
			}
		}

		for _, position := range []string{"input", "output"} {
			if schema, present := operation[position]; present {
				d.checkSchema(jsonpointer.Format("operations", key, position), schema)
			}
		}

		examples, _ := d.members(operation, "examples", path, "OBI-D-03", "OBI-D-11")
		for _, exampleKey := range sortedKeys(examples) {
			examplePath := jsonpointer.Format("operations", key, "examples", exampleKey)
			validateIdent(d.c, examplePath, exampleKey)
			if example, ok := d.object(examples[exampleKey], examplePath, "OBI-D-11"); ok {
				diagnoseUnknownFields(d.c, examplePath, example, exampleMembersKnown)
			}
		}
		diagnoseUnknownFields(d.c, path, operation, operationMembersKnown)
	}
}

func hasKey(object map[string]any, key string) bool {
	_, ok := object[key]
	return ok
}

// checkSchema records evidence for one schema position: well-formedness
// (OBI-D-17), and the reference, dialect, and vocabulary rules its walk
// applies (OBI-D-05, OBI-D-06, OBI-D-07, OBI-D-16).
func (d *documentCheck) checkSchema(path string, schema any) {
	validateSchemaWellFormedness(d.c, path, schema, d.wellFormed)
	walkSchema(d.c, path, schema, d.view, false)
}

// checkBindingTransform records evidence for a binding's inputTransform or
// outputTransform: an inline expression parses (OBI-D-18), and a reference
// resolves into the transforms map (OBI-D-10).
func (d *documentCheck) checkBindingTransform(path string, value any, transforms map[string]any, transformsKnown bool) {
	switch transform := value.(type) {
	case string:
		validateTransformExpression(d.c, path, transform)
	case map[string]any:
		refPath := path + jsonpointer.Format("$ref")
		switch ref, present := transform["$ref"]; {
		case !present:
		case !isString(ref):
			d.c.inconclusive("OBI-D-10", refPath, "not a string, so it was not resolved")
		case !transformsKnown:
			d.c.inconclusive("OBI-D-10", refPath, fmt.Sprintf("%q was not resolved: the transforms member is not an object", ref))
		default:
			if problem := transformRefProblem(ref.(string), transforms); problem != "" {
				d.c.violated("OBI-D-10", refPath, problem)
			}
		}
		diagnoseUnknownFields(d.c, path, transform, transformRefMembersKnown)
	default:
		for _, rule := range []string{"OBI-D-10", "OBI-D-18"} {
			d.c.inconclusive(rule, path, fmt.Sprintf("neither an expression string nor a reference object (%s), so it could not be checked", jsonTypeName(value)))
		}
	}
}

func isString(value any) bool {
	_, ok := value.(string)
	return ok
}

// sortedKeys returns an object's keys in order, so evidence is reported
// deterministically.
func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// diagnoseUnknownFields surfaces OBI-T-02's advice for the unknown non-`x-`
// members of an OBI-defined object. They are ignored, never rejected.
func diagnoseUnknownFields(c *ruleChecks, path string, object map[string]any, known map[string]bool) {
	var unknown []string
	for _, name := range sortedKeys(object) {
		if !known[name] && !strings.HasPrefix(name, "x-") {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return
	}
	noun := "fields"
	if len(unknown) == 1 {
		noun = "field"
	}
	c.diagnose("OBI-T-02", path, fmt.Sprintf("unknown %s ignored: %s; extensions use the x- prefix", noun, strings.Join(unknown, ", ")))
}

// validateTransformExpression records an OBI-D-18 violation when expr does
// not parse as a syntactically valid expression of the pinned transform
// language (§5.5: the incorporated JSONata 2.1 documentation). Parse-only:
// membership in the language, not success of evaluation; undefined results
// and dynamic errors remain evaluation outcomes.
func validateTransformExpression(c *ruleChecks, prefix, expr string) {
	if !jsonataParses(expr) {
		c.violated("OBI-D-18", prefix, "not a syntactically valid JSONata expression")
	}
}

// jsonataParses reports whether expr parses under the bundled JSONata
// syntax package, without importing the evaluator or initializing its
// standard library. The parser is only ever handed document-supplied strings,
// so a parser panic is treated as a parse failure rather than crashing
// document validation.
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
func transformRefProblem(ref string, transforms map[string]any) string {
	name, problem := transformReferenceName(ref)
	if problem != "" {
		return problem
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

// validateURIRef checks that raw is a well-formed URI reference per RFC 3986
// §4.1, records an OBI-D-05 violation when it is not, and reports whether it
// is. The empty string is a well-formed (relative) reference.
//
// The check enforces the RFC 3986 character set strictly: only unreserved,
// reserved, and percent-encoded octets are permitted. net/url's parser is too
// permissive for this rule (it accepts whitespace, backticks, and angle
// brackets) so a character-class screen runs before it parses the structure.
func validateURIRef(c *ruleChecks, prefix, raw string) bool {
	if !screenURIChars(c, prefix, raw) {
		return false
	}
	if _, err := url.Parse(raw); err != nil {
		c.violated("OBI-D-05", prefix, fmt.Sprintf("%q is not a well-formed URI reference", raw))
		return false
	}
	return true
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
//   - OBI-D-05 at OBI positions: every $ref is a well-formed URI reference
//     (RFC 3986 §4.1) that is a same-document JSON Pointer fragment in
//     literal form or an absolute URI, an $id is an absolute, well-formed
//     URI, and $dynamicRef and $dynamicAnchor do not appear.
//   - OBI-D-16: a same-document fragment at an OBI position resolves.
//
// A schema that declares its own $id is a schema resource whose references,
// nested $ids, anchors, and dynamic pair are its internal business, resolved
// per JSON Schema 2020-12 exactly as for an externally fetched schema (§7), so
// OBI-D-05 and OBI-D-16 stop at its boundary: they judge its own $id and
// nothing inside it. OBI-D-06 and OBI-D-07 govern every schema in the
// document, inside resources too.
//
// Recursion follows JSON Schema keyword shapes so that property names under
// `properties`/`patternProperties`/`$defs`/etc. are not themselves treated as
// schema keywords.
func walkSchema(c *ruleChecks, prefix string, schema any, doc any, inResource bool) {
	s, ok := schema.(map[string]any)
	if !ok {
		// Boolean schemas carry no keywords to walk; non-schema values are
		// OBI-D-17's concern (validateSchemaWellFormedness).
		return
	}

	if v, ok := s["$schema"]; ok {
		if str, ok := v.(string); ok && str != draft202012URI {
			c.violated("OBI-D-06", prefix+jsonpointer.Format("$schema"), fmt.Sprintf("%q must equal %q", str, draft202012URI))
		}
	}
	if _, ok := s["$vocabulary"]; ok {
		c.violated("OBI-D-07", prefix, "$vocabulary keyword is forbidden in OBI documents")
	}

	if !inResource {
		if id, ok := s["$id"].(string); ok {
			// This schema's own $id is at an OBI position; everything inside
			// the resource it declares is the resource's business.
			idPath := prefix + jsonpointer.Format("$id")
			if validateURIRef(c, idPath, id) && !referenceIsAbsolute(id) {
				c.violated("OBI-D-05", idPath, fmt.Sprintf("%q must be an absolute URI", id))
			}
			inResource = true
		}
	}

	if !inResource {
		// The dynamic pair does not appear at OBI positions: dynamic
		// resolution follows the runtime dynamic scope rather than the
		// document (§7 item 2).
		if _, ok := s["$dynamicRef"]; ok {
			c.violated("OBI-D-05", prefix, "$dynamicRef does not appear at OBI positions; dynamic resolution follows the runtime dynamic scope rather than the document")
		}
		if _, ok := s["$dynamicAnchor"]; ok {
			c.violated("OBI-D-05", prefix, "$dynamicAnchor does not appear at OBI positions; it would be a second named-schema mechanism competing with the schemas map, as $anchor would")
		}
		if ref, ok := s["$ref"].(string); ok {
			checkDocumentReference(c, prefix+jsonpointer.Format("$ref"), ref, doc)
		}
	}

	for _, k := range sortedKeys(s) {
		v := s[k]
		switch {
		case schemaMapKeywords[k]:
			if m, ok := v.(map[string]any); ok {
				for _, sk := range sortedKeys(m) {
					walkSchema(c, prefix+jsonpointer.Format(k, sk), m[sk], doc, inResource)
				}
			}
		case singleSchemaKeywords[k]:
			walkSchema(c, prefix+jsonpointer.Format(k), v, doc, inResource)
		case arraySchemaKeywords[k]:
			if arr, ok := v.([]any); ok {
				for idx, item := range arr {
					walkSchema(c, prefix+jsonpointer.Format(k, strconv.Itoa(idx)), item, doc, inResource)
				}
			}
		}
	}
}

// checkDocumentReference applies OBI-D-05 and OBI-D-16 to a schema $ref at an
// OBI position: a well-formed URI reference that is an absolute URI or a
// same-document JSON Pointer fragment in literal form, and, for a fragment, a
// pointer that resolves from the document root.
func checkDocumentReference(c *ruleChecks, path, ref string, doc any) {
	if !validateURIRef(c, path, ref) {
		return
	}
	if !strings.HasPrefix(ref, "#") {
		if !referenceIsAbsolute(ref) {
			c.violated("OBI-D-05", path, fmt.Sprintf("%q must be a same-document fragment or an absolute URI, not a relative reference", ref))
		}
		return
	}
	pointer := ref[1:]
	switch {
	case strings.Contains(pointer, "%"):
		// Literal form (§7): a same-document fragment is written with the
		// pointer's characters unencoded, so every addressable location has
		// exactly one conformant spelling.
		c.violated("OBI-D-05", path, fmt.Sprintf("%q is not in literal form; a same-document fragment is written with the pointer's characters unencoded (percent-encoding is not a conformant OBI reference)", ref))
	case pointer != "" && !strings.HasPrefix(pointer, "/"):
		c.violated("OBI-D-05", path, fmt.Sprintf("%q is a plain-name fragment; a same-document schema $ref is a JSON Pointer fragment (bare # or #/...), and the schemas map is the document's named-schema mechanism", ref))
	default:
		if _, ok := jsonpointer.Parse(pointer); !ok {
			c.violated("OBI-D-05", path, fmt.Sprintf("%q is not a JSON Pointer fragment: ~ must be followed by 0 or 1 (RFC 6901)", ref))
		} else if _, ok := jsonpointer.Resolve(doc, pointer); !ok {
			c.violated("OBI-D-16", path, fmt.Sprintf("%q does not resolve within the document", ref))
		}
	}
}
