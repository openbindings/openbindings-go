package openbindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// Validate checks a document already in memory against every document rule
// this SDK can decide. It reports the per-rule evidence, the located findings,
// and the §10.4 conformance conclusion.
//
// The rules judge the document the host object encodes, exactly as
// ValidateDocument judges bytes: a claim about a value in memory is a claim
// about its serialization as UTF-8 JSON text with no byte-order mark, each
// number written at its exact value (§10). That text is the encoding Validate
// judges, which the model writes only when it decodes back unchanged, so
// OBI-D-01 holds whenever there is a report. The report is about the value,
// not about any bytes it was decoded from: to judge a file, pass its bytes to
// ValidateDocument.
//
// The error is a *ValidationError listing every established violation, so
// `if _, err := iface.Validate(); err != nil`
// gates on violations. A nil error
// is not a conformance claim. A rule this SDK cannot decide is inconclusive,
// not violated, and the report's Conclusion says whether the document is
// conformant or conformance undetermined. No rule takes binding-specification
// knowledge: a source's and a binding's content are the binding
// specification's, and no core rule judges them. OBI-D-10 is inconclusive
// for the subschemas a schema nests deeper than 256 levels, where the
// meta-schema check meets a resource limit (§10.4).
//
// A document declaring a version outside the supported set is not interpreted:
// Validate returns a *VersionRefusalError and no report (OBI-T-04). A host
// object that cannot be encoded returns that error and no report, as does
// one holding, in a member the model carries as raw JSON, bytes decoding
// would refuse: the model encodes only what it would decode back unchanged.
func (i Interface) Validate() (ValidationReport, error) {
	if refusal := versionRefusalOf(i.OpenBindings); refusal != nil {
		return ValidationReport{}, refusal
	}
	view, err := documentView(i)
	if err != nil {
		return ValidationReport{}, err
	}
	c := ruleChecks{version: appliedRelease, revision: appliedRevision}
	checkDocument(&c, view)
	return c.conclude()
}

// ValidateDocument validates the exact input bytes of a document: OBI-D-01 on
// the bytes themselves, then every other document rule this SDK can decide on
// the JSON they hold. It returns the decoded document when the document model
// can carry it exactly (see LosslessFields), the report, and the same
// violation error Interface.Validate returns. The rules never depend on that
// decoding: a document the model cannot carry is still judged in full, except
// where the SDK cannot read it in full. Input that OBI-D-01 refuses (not JSON,
// not UTF-8, beginning with a byte-order mark, or repeating a member name) is
// reported as that rule's
// violation, with every other rule inconclusive, since which of its values
// the document holds is not established. A document holding a string that
// escapes a lone UTF-16 surrogate, or nesting deeper than encoding/json reads
// (10000 levels), has OBI-D-01 and OBI-D-09 decided, OBI-D-09 on the version
// it declares, and every other rule inconclusive.
//
// A document declaring a well-formed version outside the supported set is not
// interpreted: ValidateDocument returns a *VersionRefusalError and no report
// (OBI-T-04). The version is read first, from any input that is one JSON
// value, however deeply it nests.
func ValidateDocument(data []byte) (*Interface, ValidationReport, error) {
	c := ruleChecks{version: appliedRelease, revision: appliedRevision}
	view, err := decodeDocumentBytes(data)
	if err != nil {
		if refusal := inputVersionRefusal(data); refusal != nil {
			return nil, ValidationReport{}, refusal
		}
		var lone *loneSurrogateError
		switch {
		case errors.Is(err, errNestingLimit):
			// OBI-D-01 is decided on the input, which the exact scan reads at
			// any depth, and so is OBI-D-09, on the member the scan reads the
			// version from. The other rules read the decoded document, which
			// meets a resource limit and is no evidence either way (§10.4).
			c.inconclusiveExcept(fmt.Sprintf("the input is %v, so this rule was not checked", err), "OBI-D-01", "OBI-D-09")
			checkDeclaredVersion(&c, versionView(data))
		case errors.As(err, &lone):
			// OBI-D-01 is decided: the input is UTF-8 JSON with no repeated
			// name. So is OBI-D-09, on the member the exact scan reads the
			// version from. The other rules read values this SDK cannot
			// carry.
			c.inconclusiveExcept(fmt.Sprintf("%v, so this rule was not checked", err), "OBI-D-01", "OBI-D-09")
			checkDeclaredVersion(&c, versionView(data))
		default:
			c.findings = append(c.findings, d01Violation(data, err))
			c.inconclusiveExcept("OBI-D-01 refuses the input, so this rule was not checked", "OBI-D-01")
		}
		report, verr := c.conclude()
		return nil, report, verr
	}
	if refusal := declaredVersionRefusal(view); refusal != nil {
		return nil, ValidationReport{}, refusal
	}
	checkDocument(&c, view)
	positionFindings(data, c.findings)
	report, verr := c.conclude()
	var iface Interface
	if err := iface.decodeVerified(data); err != nil { // OBI-D-01 verified the bytes
		return nil, report, verr
	}
	return &iface, report, verr
}

// d01Violation is the OBI-D-01 finding for input verifyExactJSON refuses,
// located at the object that repeats a member name when that is why.
func d01Violation(data []byte, err error) Finding {
	if duplicate := (*duplicateNameError)(nil); errors.As(err, &duplicate) {
		return Finding{Rule: "OBI-D-01", Status: EvidenceViolated, Path: duplicate.location, Message: "repeats the member name " + strconv.Quote(duplicate.name), Position: d01Position(data, err)}
	}
	return Finding{Rule: "OBI-D-01", Status: EvidenceViolated, Message: fmt.Sprintf("not a JSON document this specification accepts: %v", err), Position: d01Position(data, err)}
}

// documentView encodes a host document and decodes the generic JSON view the
// document rules judge.
func documentView(i Interface) (any, error) {
	data, err := json.Marshal(i)
	if err != nil {
		return nil, fmt.Errorf("openbindings: encode interface: %w", err)
	}
	var view any
	if err := unmarshalJSON(data, &view); err != nil {
		return nil, fmt.Errorf("openbindings: decode encoded interface: %w", err)
	}
	return view, nil
}

// versionView returns what OBI-D-09 judges of input of any depth, read by the
// exact scan: an object holding the root object's openbindings member when
// it has one, a value other than a string standing as an empty value of its
// JSON type.
func versionView(data []byte) any {
	raw, declared := versionMember(data)
	if !declared {
		return nil
	}
	var version any
	switch raw[0] {
	case '"':
		version, _ = exactString(raw)
	case '{':
		version = map[string]any{}
	case '[':
		version = []any{}
	case 't', 'f':
		version = raw[0] == 't'
	case 'n':
	default:
		version = json.Number(raw)
	}
	return map[string]any{"openbindings": version}
}

// checkDeclaredVersion decides OBI-D-09 from a document's generic view, which
// holds even when the value is not a string.
func checkDeclaredVersion(c *ruleChecks, view any) {
	object, _ := view.(map[string]any)
	value, present := object["openbindings"]
	version, isString := value.(string)
	switch {
	case !present:
		c.violated("OBI-D-09", "", "missing the required openbindings member")
	case !isString:
		c.violated("OBI-D-09", "/openbindings", fmt.Sprintf("must be a SemVer 2.0.0 string; got %s", jsonTypeName(value)))
	case !IsValidSemver(version):
		c.violated("OBI-D-09", "/openbindings", fmt.Sprintf("%q is not a valid SemVer 2.0.0 string", version))
	}
}

// inputVersionRefusal applies OBI-T-04 to the version input declares, read
// from its bytes (see declaredVersion), for input OBI-D-01 refuses or the
// decoder cannot read: the version decision precedes interpreting a document
// under this version's rules, OBI-D-01 included (§10.1).
func inputVersionRefusal(data []byte) *VersionRefusalError {
	version, declared := declaredVersion(data)
	if !declared {
		return nil
	}
	return versionRefusalOf(version)
}

// declaredVersionRefusal applies OBI-T-04 to the version a document's generic
// view declares. The decision precedes interpretation under this version's
// semantics, the embedded document schema included. A missing or malformed
// version is OBI-D-09's concern, decided with the other rules, so it is not a
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
// an accepted version and for a malformed one, which is OBI-D-09's concern
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

// checkDocument records evidence for OBI-D-02 through OBI-D-13 on the generic
// view of a document whose version has already been accepted.
//
// Each rule is judged literally on the values the document holds. A rule
// quantifies over values of a kind: an absent member, or one of a JSON type
// outside the rule's domain, gives it nothing to judge there. A member whose
// type contradicts what the rule requires of it violates the rule: an
// operation reference that is a number names no operation key. Where the
// document schema requires a member or a type, OBI-D-02 also reports it. No
// rule evaluates a value against the document's schemas: an example is an
// author claim, which no document rule checks (OBI-T-10).
func checkDocument(c *ruleChecks, view any) {
	checkDeclaredVersion(c, view)
	validateAgainstOBISchema(c, view)

	root, _ := view.(map[string]any)
	d := documentCheck{c: c, view: view, wellFormed: map[string]bool{}, schemas: collectDocumentSchemas(view)}

	schemas, _ := root["schemas"].(map[string]any)
	for _, key := range sortedKeys(schemas) {
		path := jsonpointer.Format("schemas", key)
		validateIdent(c, path, key)
		d.checkSchema(path, schemas[key])
	}

	operations, _ := root["operations"].(map[string]any)
	d.checkOperations(operations)
	d.checkUniqueness()

	dependencies, _ := root["dependencies"].(map[string]any)
	for _, key := range sortedKeys(dependencies) {
		path := jsonpointer.Format("dependencies", key)
		validateIdent(c, path, key)
		if dependency, ok := dependencies[key].(map[string]any); ok {
			d.checkReference(dependency, path, "operation", "OBI-D-11", operations, "operation key")
		}
	}

	sources, _ := root["sources"].(map[string]any)
	for _, key := range sortedKeys(sources) {
		validateIdent(c, jsonpointer.Format("sources", key), key)
		// Source content belongs to the source's kind.
	}

	bindings, _ := root["bindings"].(map[string]any)
	for _, key := range sortedKeys(bindings) {
		path := jsonpointer.Format("bindings", key)
		validateIdent(c, path, key)
		binding, ok := bindings[key].(map[string]any)
		if !ok {
			continue
		}
		d.checkReference(binding, path, "operation", "OBI-D-07", operations, "operation key")
		d.checkReference(binding, path, "source", "OBI-D-08", sources, "source")
	}
}

// documentCheck carries one document's view through its rule checks.
type documentCheck struct {
	c    *ruleChecks
	view any

	// schemas is what the document holds as schemas.
	schemas documentSchemas

	// wellFormed remembers schema objects already found well-formed, keyed by
	// their encoding, so a schema repeated across positions is checked once.
	wellFormed map[string]bool
}

// checkReference decides a referential rule (OBI-D-07, OBI-D-08, OBI-D-11)
// for the member name of an entry: its value is a key of targets, the entries
// of a map the document may lack.
func (d *documentCheck) checkReference(entry map[string]any, entryPath, name, rule string, targets map[string]any, noun string) {
	value, present := entry[name]
	if !present {
		return
	}
	path := entryPath + jsonpointer.Format(name)
	key, ok := value.(string)
	if !ok {
		d.c.violated(rule, path, fmt.Sprintf("names no %s: a reference is a key string; got %s", noun, jsonTypeName(value)))
		return
	}
	if _, found := targets[key]; !found {
		d.c.violated(rule, path, fmt.Sprintf("references unknown %s %q", noun, key))
	}
}

// checkOperations records evidence for every operation: key and alias names
// (OBI-D-03, OBI-D-04), schemas, and example keys.
func (d *documentCheck) checkOperations(operations map[string]any) {
	aliasOwner := map[string]string{}
	for _, key := range sortedKeys(operations) {
		path := jsonpointer.Format("operations", key)
		validateIdent(d.c, path, key)
		operation, ok := operations[key].(map[string]any)
		if !ok {
			continue
		}

		// OBI-D-04: an operation's identifiers are its key plus its aliases,
		// and every identifier in the document is distinct.
		aliases, _ := operation["aliases"].([]any)
		seen := map[string]bool{}
		for index, element := range aliases {
			aliasPath := jsonpointer.Format("operations", key, "aliases", strconv.Itoa(index))
			alias, ok := element.(string)
			if !ok {
				d.c.violated("OBI-D-03", aliasPath, fmt.Sprintf("an alias is a name matching ^[A-Za-z0-9_][A-Za-z0-9_.-]*$; got %s", jsonTypeName(element)))
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

		for _, position := range []string{"input", "output"} {
			if schema, present := operation[position]; present {
				d.checkSchema(jsonpointer.Format("operations", key, position), schema)
			}
		}

		examples, _ := operation["examples"].(map[string]any)
		for _, exampleKey := range sortedKeys(examples) {
			examplePath := jsonpointer.Format("operations", key, "examples", exampleKey)
			validateIdent(d.c, examplePath, exampleKey)
		}
	}
}

func hasKey(object map[string]any, key string) bool {
	_, ok := object[key]
	return ok
}

// checkSchema records evidence for one schema at an OBI position:
// meta-schema validity (OBI-D-10) and the dialect and reference-form rules its
// walk applies (OBI-D-05, OBI-D-06, OBI-D-12).
func (d *documentCheck) checkSchema(path string, schema any) {
	validateSchemaWellFormedness(d.c, path, schema, d.wellFormed)
	d.walkSchema(&schemaPath{start: path}, schema, false)
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

const draft202012URI = "https://json-schema.org/draft/2020-12/schema"

// walkSchema walks a schema and every subschema of it the 2020-12 meta-schema
// validates as one, the legacy definitions and the schema values of the
// legacy dependencies included, applying:
//   - OBI-D-06: every $schema names the 2020-12 dialect, with or without an
//     empty fragment. Its placement is JSON Schema's to report, not a document
//     rule.
//   - OBI-D-05 at OBI positions, in the document resource: every $ref and
//     $dynamicRef is a well-formed URI-reference (RFC 3986 §4.1) that is an
//     absolute URI or a same-document reference (empty, or a fragment
//     alone), and every $id is a well-formed absolute URI.
//   - OBI-D-12 at OBI positions, in the document resource: a same-document
//     $ref or $dynamicRef identifies a schema at an OBI position.
//
// A schema that declares $id is itself at an OBI position, so OBI-D-05 judges
// its $id, but everything else in the resource it declares, its own keywords
// included, is the resource's business, resolved per JSON Schema 2020-12
// (§7.2): OBI-D-05 and OBI-D-12 stop at its boundary. OBI-D-06 governs every
// schema the document contains, inside resources too.
func (d *documentCheck) walkSchema(path *schemaPath, schema any, inResource bool) {
	s, ok := schema.(map[string]any)
	if !ok {
		// Boolean schemas carry no keywords; any other value is OBI-D-10's.
		return
	}

	// A $schema that is not a string is already refused by the meta-schemas.
	if value, present := s["$schema"]; present && value != draft202012URI && value != draft202012URI+"#" {
		d.c.violated("OBI-D-06", path.at("$schema"), fmt.Sprintf("must name the 2020-12 dialect, %q, with or without an empty fragment; got %s", draft202012URI, describeJSON(value)))
	}

	if !inResource {
		if value, present := s["$id"]; present {
			id, isString := value.(string)
			if !isString {
				d.c.violated("OBI-D-05", path.at("$id"), fmt.Sprintf("an $id is an absolute URI string; got %s", jsonTypeName(value)))
			} else if wellFormed, hasScheme := uriReference(id); !wellFormed {
				d.c.violated("OBI-D-05", path.at("$id"), fmt.Sprintf("%q is not a well-formed URI-reference (RFC 3986 §4.1)", id))
			} else if !hasScheme {
				d.c.violated("OBI-D-05", path.at("$id"), fmt.Sprintf("%q must be an absolute URI", id))
			}
			// A schema with an $id member is a boundary, whatever the
			// member's value (§7).
			inResource = true
		}
	}

	if !inResource {
		for _, keyword := range []string{"$ref", "$dynamicRef"} {
			value, present := s[keyword]
			if !present {
				continue
			}
			if ref, ok := value.(string); ok {
				d.checkDocumentReference(path, keyword, ref)
			} else {
				d.c.violated("OBI-D-05", path.at(keyword), fmt.Sprintf("a %s is a URI-reference string; got %s", keyword, jsonTypeName(value)))
			}
		}
	}

	forEachDescribedSubschema(s, func(child any, tokens ...string) {
		path.below = append(path.below, tokens...)
		d.walkSchema(path, child, inResource)
		path.below = path.below[:len(path.below)-len(tokens)]
	})
}

// schemaPath is where a walk of a schema is: the location of the schema it
// began at, and the reference tokens from there to the schema it is at. A
// location is formatted only when a finding needs one, so the walk itself,
// references included, does no work per node that grows with depth; each
// finding costs its path.
type schemaPath struct {
	start string
	below []string
}

// at returns the location of the schema the walk is at, or of the member
// tokens name within it.
func (p *schemaPath) at(tokens ...string) string {
	return p.start + jsonpointer.Format(slices.Concat(p.below, tokens)...)
}

// checkDocumentReference applies OBI-D-05 and OBI-D-12 to the $ref or
// $dynamicRef keyword names, held by the schema a walk of the document
// resource is at.
func (d *documentCheck) checkDocumentReference(path *schemaPath, keyword, ref string) {
	wellFormed, hasScheme := uriReference(ref)
	sameDocument := ref == "" || strings.HasPrefix(ref, "#")
	switch {
	case !wellFormed:
		d.c.violated("OBI-D-05", path.at(keyword), fmt.Sprintf("%q is not a well-formed URI-reference (RFC 3986 §4.1)", ref))
		return
	case sameDocument:
	case hasScheme:
		// An absolute URI is outside OBI-D-12; JSON Schema resolves it.
		return
	default:
		d.c.violated("OBI-D-05", path.at(keyword), fmt.Sprintf("%q must be an absolute URI or a same-document reference, not a relative reference", ref))
		return
	}
	// A plain name declared more than once is OBI-D-13's to report.
	if found := d.schemas.lookUpSameDocument(ref, d.view); found.why != "" && !found.declaredTwice {
		d.c.violated("OBI-D-12", path.at(keyword), fmt.Sprintf("the %s %q %s", keyword, ref, found.why))
	}
}

// checkUniqueness applies OBI-D-13: no plain name is declared more than once
// in the document resource, each $anchor and each $dynamicAnchor declaring it
// counting once, and no two schemas the document contains declare the same
// $id as OBI-D-13 compares identifiers (comparableID). Each declaration is a
// finding.
func (d *documentCheck) checkUniqueness() {
	for _, name := range slices.Sorted(maps.Keys(d.schemas.anchors)) {
		declarations := d.schemas.anchors[name]
		if len(declarations) < 2 {
			continue
		}
		for _, declaration := range declarations {
			d.c.violated("OBI-D-13", declaration.at.from(nil)+jsonpointer.Format(declaration.keyword), fmt.Sprintf("declares the plain name %q, which the document resource declares %d times", name, len(declarations)))
		}
	}
	for _, id := range slices.Sorted(maps.Keys(d.schemas.identifiers)) {
		declarations := d.schemas.identifiers[id]
		if len(declarations) < 2 {
			continue
		}
		for _, at := range declarations {
			d.c.violated("OBI-D-13", at.from(nil)+jsonpointer.Format("$id"), fmt.Sprintf("declares %s, which %d schemas the document contains declare", id, len(declarations)))
		}
	}
}

// describeJSON renders a JSON value for a message: a string quoted, anything
// else by its JSON type.
func describeJSON(value any) string {
	if text, ok := value.(string); ok {
		return strconv.Quote(text)
	}
	return jsonTypeName(value)
}
