package openbindings

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// ValidateOptions configures validation. It has no fields: no document rule
// takes a capability an application supplies (§10.2).
type ValidateOptions struct{}

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
// `if _, err := iface.Validate(openbindings.ValidateOptions{}); err != nil`
// gates on violations. A nil error
// is not a conformance claim. A rule this SDK cannot decide is inconclusive,
// not violated, and the report's Conclusion says whether the document is
// conformant or conformance undetermined. No rule takes binding-specification
// knowledge: a source's and a binding's content are the binding
// specification's, and no core rule judges them. OBI-D-17 is inconclusive
// for the subschemas a schema nests deeper than 256 levels, where the
// meta-schema check meets a resource limit (§10.5).
//
// A document declaring a version outside the supported set is not interpreted:
// Validate returns a *VersionRefusalError and no report (OBI-T-04). A host
// object that cannot be encoded returns that error and no report, as does
// one holding, in a member the model carries as raw JSON, bytes decoding
// would refuse: the model encodes only what it would decode back unchanged.
func (i Interface) Validate(options ValidateOptions) (ValidationReport, error) {
	if refusal := versionRefusalOf(i.OpenBindings); refusal != nil {
		return ValidationReport{}, refusal
	}
	view, err := documentView(i)
	if err != nil {
		return ValidationReport{}, err
	}
	var c ruleChecks
	c.inconclusive("OBI-D-01", "", "decided on the exact input bytes, which a host object no longer carries; ValidateDocument decides it")
	checkDocument(&c, view, options)
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
// escapes a lone UTF-16 surrogate has OBI-D-01 decided and every other rule
// inconclusive; one nesting deeper than encoding/json reads (10000 levels)
// has OBI-D-12 decided as well, on the version it declares.
//
// A document declaring a well-formed version outside the supported set is not
// interpreted: ValidateDocument returns a *VersionRefusalError and no report
// (OBI-T-04). The version is read first, from any input that is one JSON
// value, however deeply it nests.
// options gives the capabilities validation does not carry itself, as for
// Interface.Validate.
func ValidateDocument(data []byte, options ValidateOptions) (*Interface, ValidationReport, error) {
	var c ruleChecks
	view, err := decodeDocumentBytes(data)
	if err != nil {
		if refusal := inputVersionRefusal(data); refusal != nil {
			return nil, ValidationReport{}, refusal
		}
		var lone *loneSurrogateError
		switch {
		case errors.Is(err, errNestingLimit):
			// OBI-D-01 is decided on the input, which the exact scan reads at
			// any depth, and so is OBI-D-12, on the member the scan reads the
			// version from. The other rules read the decoded document, which
			// meets a resource limit and is no evidence either way (§10.5).
			c.inconclusiveExcept(fmt.Sprintf("the input is %v, so this rule was not checked", err), "OBI-D-01", "OBI-D-12")
			checkDeclaredVersion(&c, versionView(data))
		case errors.As(err, &lone):
			// OBI-D-01 is decided: the input is UTF-8 JSON with no repeated
			// name. The other rules read values this SDK cannot carry.
			c.inconclusiveExcept(fmt.Sprintf("%v, so this rule was not checked", err), "OBI-D-01")
		default:
			c.findings = append(c.findings, d01Violation(err))
			c.inconclusiveExcept("OBI-D-01 refuses the input, so this rule was not checked", "OBI-D-01")
		}
		report, verr := c.conclude()
		return nil, report, verr
	}
	if refusal := declaredVersionRefusal(view); refusal != nil {
		return nil, ValidationReport{}, refusal
	}
	checkDocument(&c, view, options)
	report, verr := c.conclude()
	var iface Interface
	if err := iface.decodeVerified(data); err != nil { // OBI-D-01 verified the bytes
		return nil, report, verr
	}
	return &iface, report, verr
}

// d01Violation is the OBI-D-01 finding for input verifyExactJSON refuses,
// located at the object that repeats a member name when that is why.
func d01Violation(err error) Finding {
	if duplicate := (*duplicateNameError)(nil); errors.As(err, &duplicate) {
		return Finding{Rule: "OBI-D-01", Status: EvidenceViolated, Path: duplicate.location, Message: "repeats the member name " + strconv.Quote(duplicate.name)}
	}
	return Finding{Rule: "OBI-D-01", Status: EvidenceViolated, Message: fmt.Sprintf("not a JSON document this specification accepts: %v", err)}
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

// versionView returns what OBI-D-12 judges of input of any depth, read by the
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
	rootMembersKnown       = membersOf(reflect.TypeFor[Interface]()).typed
	operationMembersKnown  = membersOf(reflect.TypeFor[Operation]()).typed
	exampleMembersKnown    = membersOf(reflect.TypeFor[OperationExample]()).typed
	dependencyMembersKnown = membersOf(reflect.TypeFor[DependencyEntry]()).typed
	sourceMembersKnown     = membersOf(reflect.TypeFor[Source]()).typed
	bindingMembersKnown    = membersOf(reflect.TypeFor[BindingEntry]()).typed
)

// checkDocument records evidence for OBI-D-02 through OBI-D-19 on the generic
// view of a document whose version has already been accepted.
//
// Each rule is judged literally on the values the document holds. A rule
// quantifies over values of a kind: an absent member, or one of a JSON type
// outside the rule's domain, gives it nothing to judge there. A member whose
// type contradicts what the rule requires of it violates the rule: an
// operation reference that is a number names no operation key. Where the
// document schema requires a member or a type, OBI-D-02 also reports it.
func checkDocument(c *ruleChecks, view any, options ValidateOptions) {
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
	d.checkDuplicateIDs()

	dependencies, _ := root["dependencies"].(map[string]any)
	for _, key := range sortedKeys(dependencies) {
		path := jsonpointer.Format("dependencies", key)
		validateIdent(c, path, key)
		if dependency, ok := dependencies[key].(map[string]any); ok {
			d.checkReference(dependency, path, "operation", "OBI-D-19", operations, "operation key")
			diagnoseUnknownFields(c, path, dependency, dependencyMembersKnown)
		}
	}

	sources, _ := root["sources"].(map[string]any)
	for _, key := range sortedKeys(sources) {
		path := jsonpointer.Format("sources", key)
		validateIdent(c, path, key)
		source, ok := sources[key].(map[string]any)
		if !ok {
			continue
		}
		diagnoseUnknownFields(c, path, source, sourceMembersKnown)
	}

	bindings, _ := root["bindings"].(map[string]any)
	for _, key := range sortedKeys(bindings) {
		path := jsonpointer.Format("bindings", key)
		validateIdent(c, path, key)
		binding, ok := bindings[key].(map[string]any)
		if !ok {
			continue
		}
		d.checkReference(binding, path, "operation", "OBI-D-08", operations, "operation key")
		d.checkReference(binding, path, "source", "OBI-D-09", sources, "source")
		diagnoseUnknownFields(c, path, binding, bindingMembersKnown)
	}

	if root != nil {
		diagnoseUnknownFields(c, "", root, rootMembersKnown)
	}

	// OBI-D-11: every provided example validates against its operation's
	// schema, where that schema's graph resolves entirely within the document.
	checkExamples(c, view, operations, d.schemas)
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

// checkReference decides a referential rule (OBI-D-08, OBI-D-09, OBI-D-19)
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
			if example, ok := examples[exampleKey].(map[string]any); ok {
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
	d.walkSchema(&schemaPath{start: path}, schema, false, false)
}

// literalFragmentProblem states why ref is not a same-document fragment in
// JSON Pointer form and literal form (§7), or returns "" when it is one.
func literalFragmentProblem(ref string) string {
	if !strings.HasPrefix(ref, "#") {
		return fmt.Sprintf("%q must be a same-document fragment", ref)
	}
	if wellFormed, _ := uriReference(ref); !wellFormed {
		return fmt.Sprintf("%q is not a well-formed URI reference (RFC 3986 §4.1)", ref)
	}
	pointer := ref[1:]
	switch {
	case strings.Contains(pointer, "%"):
		return fmt.Sprintf("%q is not in literal form; a same-document fragment is written with the pointer's characters unencoded (percent-encoding is not a conformant OBI reference)", ref)
	case pointer != "" && !strings.HasPrefix(pointer, "/"):
		return fmt.Sprintf("%q is a plain-name fragment; a same-document reference is a JSON Pointer fragment (bare # or #/...)", ref)
	}
	if _, ok := jsonpointer.Parse(pointer); !ok {
		return fmt.Sprintf("%q is not a JSON Pointer fragment: ~ must be followed by 0 or 1 (RFC 6901)", ref)
	}
	return ""
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
// members of an OBI-defined object: processing ignores them. The document
// schema also refuses them (OBI-D-02, §12), since unprefixed names are
// reserved for the specification.
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

// walkSchema walks a schema and every subschema of it, applying:
//   - OBI-D-06: $schema, where present, equals the 2020-12 dialect URI.
//   - OBI-D-07: $vocabulary does not appear.
//   - OBI-D-05 at OBI positions: every $ref is a well-formed URI reference
//     (RFC 3986 §4.1) that is a same-document JSON Pointer fragment in
//     literal form or an absolute URI; an $id is an absolute, well-formed
//     URI; and $dynamicRef and $dynamicAnchor do not appear.
//   - OBI-D-16 at OBI positions: a same-document fragment resolves from the
//     document root, and an absolute reference to a resource the document
//     embeds resolves within that resource.
//
// A schema that declares its own $id is a schema resource whose references,
// nested $ids, anchors, and dynamic pair are its internal business, resolved
// per JSON Schema 2020-12 exactly as for an externally fetched schema (§7), so
// OBI-D-05 and OBI-D-16 stop at its boundary: they judge its own $id and
// nothing inside it. They also stop at definitions and dependencies, whose
// entries 2020-12 does not evaluate as subschemas: described marks a schema
// below one. OBI-D-06 and OBI-D-07 govern every schema the meta-schema
// describes, inside resources and those two keywords too.
func (d *documentCheck) walkSchema(path *schemaPath, schema any, inResource, described bool) {
	s, ok := schema.(map[string]any)
	if !ok {
		// Boolean schemas carry no keywords; any other value is OBI-D-17's.
		return
	}

	// §5.2's dialect constraints are also part of well-formedness
	// (OBI-D-17), so a schema breaking one violates both rules. A $schema
	// that is not a string is already refused by the meta-schemas.
	if value, present := s["$schema"]; present && value != draft202012URI {
		d.c.violated("OBI-D-06", path.at("$schema"), fmt.Sprintf("must equal %q; got %s", draft202012URI, describeJSON(value)))
		if _, isString := value.(string); isString {
			d.c.violated("OBI-D-17", path.at("$schema"), "not well-formed: §5.2 requires the 2020-12 dialect")
		}
	}
	if _, present := s["$vocabulary"]; present {
		d.c.violated("OBI-D-07", path.at(), "$vocabulary keyword is forbidden in OBI documents")
		d.c.violated("OBI-D-17", path.at(), "not well-formed: §5.2 forbids $vocabulary")
	}

	if !inResource && !described {
		if value, present := s["$id"]; present {
			// This schema's own $id is at an OBI position; everything inside
			// the resource it declares is the resource's business.
			idPath := path.at("$id")
			id, isString := value.(string)
			if !isString {
				d.c.violated("OBI-D-05", idPath, fmt.Sprintf("an $id is an absolute URI string; got %s", jsonTypeName(value)))
			} else if wellFormed, hasScheme := uriReference(id); !wellFormed {
				d.c.violated("OBI-D-05", idPath, fmt.Sprintf("%q is not a well-formed URI reference (RFC 3986 §4.1)", id))
			} else if !hasScheme {
				d.c.violated("OBI-D-05", idPath, fmt.Sprintf("%q must be an absolute URI", id))
			}
			// An $id empty once its fragment is removed declares no resource.
			_, inResource = declaredID(s)
		}
	}

	if !inResource && !described {
		// The dynamic pair does not appear at OBI positions: dynamic
		// resolution follows the runtime dynamic scope rather than the
		// document (§7 item 2).
		if _, present := s["$dynamicRef"]; present {
			d.c.violated("OBI-D-05", path.at(), "$dynamicRef does not appear at OBI positions; dynamic resolution follows the runtime dynamic scope rather than the document")
		}
		if _, present := s["$dynamicAnchor"]; present {
			d.c.violated("OBI-D-05", path.at(), "$dynamicAnchor does not appear at OBI positions; dynamic resolution follows the runtime dynamic scope rather than the document")
		}
		if value, present := s["$ref"]; present {
			refPath := path.at("$ref")
			if ref, ok := value.(string); ok {
				d.checkDocumentReference(refPath, path.at(), ref)
			} else {
				d.c.violated("OBI-D-05", refPath, fmt.Sprintf("a $ref is a URI reference string; got %s", jsonTypeName(value)))
			}
		}
	}

	forEachDescribedSubschema(s, func(child any, tokens ...string) {
		path.below = append(path.below, tokens...)
		d.walkSchema(path, child, inResource, described || describedMapKeywords[tokens[0]])
		path.below = path.below[:len(path.below)-len(tokens)]
	})
}

// schemaPath is where a walk of a schema is: the location of the schema it
// began at, and the reference tokens from there to the schema it is at. A
// location is formatted only when a finding needs one, so the walk itself
// does no work per node that grows with depth; each finding costs its path.
type schemaPath struct {
	start string
	below []string
}

// at returns the location of the schema the walk is at, or of the member
// tokens name within it.
func (p *schemaPath) at(tokens ...string) string {
	return p.start + jsonpointer.Format(slices.Concat(p.below, tokens)...)
}

// checkDocumentReference applies OBI-D-05 and OBI-D-16 to a schema $ref at an
// OBI position, held by the schema at holder.
func (d *documentCheck) checkDocumentReference(path, holder, ref string) {
	wellFormed, hasScheme := uriReference(ref)
	switch {
	case !wellFormed:
		d.c.violated("OBI-D-05", path, fmt.Sprintf("%q is not a well-formed URI reference (RFC 3986 §4.1)", ref))
		if !strings.HasPrefix(ref, "#") {
			return
		}
	case !strings.HasPrefix(ref, "#"):
		if !hasScheme {
			d.c.violated("OBI-D-05", path, fmt.Sprintf("%q must be a same-document fragment or an absolute URI, not a relative reference", ref))
			return
		}
		d.checkEmbeddedReference(path, holder, ref)
		return
	default:
		if problem := literalFragmentProblem(ref); problem != "" {
			d.c.violated("OBI-D-05", path, problem)
		}
	}
	// OBI-D-16 judges the fragment whatever its spelling: URI semantics
	// decode it before it is read as a JSON Pointer (RFC 6901 §6), and one
	// that is not a pointer resolves to no location from the document root.
	switch r := d.schemas.resolve(ref, holder, d.view); {
	case r.exists == missing:
		d.c.violated("OBI-D-16", path, fmt.Sprintf("%q does not resolve within the document", ref))
	case r.origin == inDocument:
		d.checkFragmentTarget(path, ref, r.location)
	}
}

// checkEmbeddedReference applies OBI-D-16 to an absolute $ref: one that
// matches the $id of a schema the document embeds is in the rule's scope and
// resolves within that resource; any other is external and outside it.
func (d *documentCheck) checkEmbeddedReference(path, holder, ref string) {
	r := d.schemas.resolve(ref, holder, d.view)
	switch {
	case r.origin == ambiguous && r.uri != "":
		// The reference names no one schema, but when its fragment resolves
		// within none of the schemas declaring the URI, it resolves nowhere.
		why := d.schemas.ambiguous[r.uri]
		if r.exists == missing {
			d.c.violated("OBI-D-16", path, fmt.Sprintf("%q does not resolve within any of the schemas that declare %s: %s", ref, r.uri, why))
		} else {
			d.c.inconclusive("OBI-D-16", path, fmt.Sprintf("%q names no one embedded schema: %s", ref, why))
		}
	case r.within == nil:
		// External, or a meta-schema: outside the rule.
	case r.exists == missing:
		d.c.violated("OBI-D-16", path, fmt.Sprintf("%q does not resolve within %s", ref, describeBase(r.within)))
	case r.origin == ambiguous:
		d.c.inconclusive("OBI-D-16", path, fmt.Sprintf("%q names an anchor more than one schema in %s declares", ref, resourceName(r.within)))
	case r.origin == inDocument:
		d.checkEmbeddedTarget(path, ref, r.within, r.location)
	}
}

// checkFragmentTarget applies OBI-D-16's target clause to a same-document
// fragment $ref at an OBI position: it resolves to a schema at an OBI
// position, never to the document or anything else that is not a schema
// (JSON Schema 2020-12 §9.4.2 leaves such a target undefined), and never into
// a schema resource that declares its own $id, whose contents a reference
// reaches through that $id (§9.2.1). A schemas entry declaring $id is itself
// at an OBI position.
func (d *documentCheck) checkFragmentTarget(path, ref, target string) {
	var enclosing *schemaResource
	if end := strings.LastIndexByte(target, '/'); end >= 0 {
		enclosing = d.schemas.resourceAt(target[:end])
	}
	switch {
	case enclosing != nil:
		d.c.violated("OBI-D-16", path, fmt.Sprintf("%q resolves into the schema resource declared at %s, whose contents a reference reaches through its $id", ref, enclosing.location))
	case !atSchemaPosition(target):
		d.c.violated("OBI-D-16", path, fmt.Sprintf("%q resolves to %s, which is not a schema position", ref, describeLocation(target)))
	}
}

// checkEmbeddedTarget applies OBI-D-16's target clause to an absolute $ref
// that matches an embedded schema's $id: it resolves to that schema or to a
// subschema JSON Schema 2020-12 defines below it.
func (d *documentCheck) checkEmbeddedTarget(path, ref string, within *schemaResource, target string) {
	tokens, _ := jsonpointer.Parse(strings.TrimPrefix(target, within.location))
	if !covers(within.location, target) || !keywordPath(tokens, false) {
		d.c.violated("OBI-D-16", path, fmt.Sprintf("%q resolves to %s, which is not a schema within the resource declared at %s", ref, describeLocation(target), within.location))
	}
}

// describeLocation names a document location for a message.
func describeLocation(location string) string {
	if location == "" {
		return "the OBI document"
	}
	return location
}

// checkDuplicateIDs applies OBI-D-05's identity clause: no two schema
// resources the document embeds declare the same $id once each is resolved,
// since a URI identifies only one schema (JSON Schema 2020-12 §9.1.2). Each
// declaration is a finding.
func (d *documentCheck) checkDuplicateIDs() {
	for _, id := range slices.Sorted(maps.Keys(d.schemas.ambiguous)) {
		for _, resource := range d.schemas.claimants[id] {
			d.c.violated("OBI-D-05", resource.location+jsonpointer.Format("$id"), fmt.Sprintf("declares %s, which more than one schema declares: %s", id, d.schemas.ambiguous[id]))
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
