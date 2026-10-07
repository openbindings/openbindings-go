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
	"unicode/utf8"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// Validate checks a document already in memory against every document rule
// this SDK can decide. It reports the per-rule evidence, the located findings,
// and the conformance conclusion (Reports and Verdicts, in the package
// documentation).
//
// The rules govern a text (§10), and Validate judges the text the host
// object encodes to, exactly as ValidateDocument judges bytes: UTF-8 JSON
// text with no byte-order mark, each number written at its exact value.
// The model writes that text only when it decodes back unchanged, so OBI-01
// holds whenever the model writes it. The report is about the value,
// not about any bytes it was decoded from: to judge a file, pass its bytes to
// ValidateDocument.
//
// The error is a *ValidationError listing every established violation, so
// `if _, err := doc.Validate(); err != nil`
// gates on violations. A nil error
// is not a conformance claim. A rule this SDK cannot decide is inconclusive,
// not violated, and the report's Conclusion says whether the document is
// conformant or conformance undetermined. No rule takes knowledge of a
// kind: a source's and a binding's content are the kind's, and no core rule
// judges them. OBI-10 is inconclusive
// for the subschemas a schema nests deeper than 256 levels, where the
// meta-schema check meets a resource limit, which is no evidence of a
// violation (Reports and Verdicts).
//
// A document declaring a version outside the supported set is not interpreted:
// Validate returns a *VersionRefusalError and no report (CheckVersion).
//
// Encoding a host document either fails or writes text, and this is the
// boundary between this SDK's own limits and an encoding failure, for
// Validate, Document.References, and ValueContractCompiler.Resolve alike:
//   - This SDK's limit is only what its own checks find: core's scan of the
//     text the document encodes to, and the refusals this package's model
//     types make of their own members carried as raw JSON (example values,
//     source and binding content, kept members). Either finds nesting deeper
//     than encoding/json reads (10000 levels), or a string escaping a lone
//     UTF-16 surrogate, which a Go string cannot carry, as ValidateDocument
//     meets them in bytes. The model has no text for such a document, so
//     Validate's report leaves OBI-01 inconclusive, decides OBI-03 on
//     the declared version when that is valid UTF-8, and leaves every other
//     rule inconclusive; References and Resolve return an error matching
//     ErrInconclusive. The declared version is a Go string the model holds
//     exactly, so it is checked with OBI-01 undecided: a failed check
//     establishes that OBI-01 or OBI-03 is violated, so the document does
//     not conform, though not which. The report records it as OBI-03
//     violated and concludes non-conformant, which does not establish that
//     OBI-01 holds (Reports and Verdicts).
//   - A failure to encode at all is an encoding failure: an error matching
//     no category, and no report, whatever the error carries. It can come
//     from a value encoding/json does not write as held (a NaN, a channel, a
//     cycle, invalid UTF-8 in a Go string, which it would replace, raw JSON
//     in a schema it will not compact, which includes raw JSON nesting past
//     its own depth), or from any marshaler outside the model's own members:
//     a caller's, or one of this package's types placed inside a schema. A
//     model type's refusal of other bytes in a raw member, and text core's
//     scan refuses for another reason (a member name repeated in raw JSON),
//     are encoding failures too: the model writes only what it would decode
//     back unchanged.
//
// So raw JSON a schema holds is this SDK's limit when it holds an escaped
// lone surrogate, or stays within encoding/json's depth but makes the whole
// document nest too deep, and an encoding failure when it nests past
// encoding/json's depth on its own; in a member the model carries as raw
// JSON, nesting past that depth is this SDK's limit, as the first bullet
// says. A document holding both kinds of defect is decided by the first that
// encoding meets. Each object the model encodes checks, in order, the
// members it carries as raw JSON; the Go strings it holds, at any depth, and
// any value that holds itself, so a document's string check covers every
// object in it before any nested raw-member check; its schemas, each encoded
// once; its other members, each object among them checked in this same
// order; and the text it wrote, with core's scan.
func (d Document) Validate() (ValidationReport, error) {
	if refusal := versionRefusalOf(d.OpenBindings); refusal != nil {
		return ValidationReport{}, refusal
	}
	view, err := documentView(d)
	if limit := (*modelLimitError)(nil); errors.As(err, &limit) {
		// Only the declared version is read, as ValidateDocument reads it
		// from such bytes; a version that is not UTF-8 would not be written
		// as held, so it is not read either.
		c := ruleChecks{release: appliedRelease, revision: appliedRevision}
		reason := fmt.Sprintf("the document %s, so the model does not write it and this rule was not checked", limit.what)
		if !utf8.ValidString(d.OpenBindings) {
			c.inconclusiveExcept(reason)
			return c.conclude()
		}
		c.inconclusiveExcept(reason, "OBI-03")
		checkDeclaredVersion(&c, map[string]any{"openbindings": d.OpenBindings})
		return c.conclude()
	}
	if err != nil {
		return ValidationReport{}, err
	}
	c := ruleChecks{release: appliedRelease, revision: appliedRevision}
	checkDocument(&c, view)
	return c.conclude()
}

// ValidateDocument validates the exact input bytes of a document: OBI-01 on
// the bytes themselves, then every other document rule this SDK can decide on
// the JSON they hold. It returns the decoded document when the document model
// can carry it exactly (see LosslessFields), the report, and the same
// violation error Document.Validate returns. The rules never depend on that
// decoding: a document the model cannot carry is still judged in full, except
// where the SDK cannot read it in full. The version is read first, from any
// input that is one JSON value, however deeply it nests.
//
// Its results take five shapes:
//   - The document declares a well-formed version outside the supported set:
//     no document, the zero report, and a *VersionRefusalError. The document
//     is not interpreted (CheckVersion).
//   - OBI-01 refuses the input (not JSON, not UTF-8, beginning with a
//     byte-order mark, or repeating a member name): no document, a report
//     with OBI-01 violated and every other rule not applicable, and a
//     *ValidationError. OBI-02 through OBI-13 apply only to the JSON value
//     of a text that meets OBI-01 (§10), so on a text violating it they are
//     not applicable (Reports and Verdicts, in the package documentation),
//     and the OBI-01 violation alone establishes non-conformance. The
//     report has no finding for them.
//   - The input holds a string escaping a lone UTF-16 surrogate, or nests
//     deeper than encoding/json reads (10000 levels): no document, a report
//     with OBI-01 decided, OBI-03 decided on the version the input
//     declares, and every other rule inconclusive, and a *ValidationError
//     when OBI-03 is violated, nil otherwise.
//   - The model cannot carry the document for another reason (a null where it
//     types a value, say): no document, the full report, and a
//     *ValidationError when the report establishes a violation, nil
//     otherwise.
//   - Otherwise: the document, the full report, and a *ValidationError when
//     the report establishes a violation, nil otherwise.
//
// So a nil document beside a report says the model does not carry the input;
// the report says what was decided about it.
func ValidateDocument(data []byte) (*Document, ValidationReport, error) {
	c := ruleChecks{release: appliedRelease, revision: appliedRevision}
	view, err := decodeDocumentBytes(data)
	if err != nil {
		if refusal := inputVersionRefusal(data); refusal != nil {
			return nil, ValidationReport{}, refusal
		}
		var lone *loneSurrogateError
		switch {
		case errors.Is(err, errNestingLimit):
			// OBI-01 is decided on the input, which the exact scan reads at
			// any depth, and so is OBI-03, on the member the scan reads the
			// version from. The other rules read the decoded document, which
			// meets a resource limit and is no evidence either way
			// (Reports and Verdicts).
			c.inconclusiveExcept(fmt.Sprintf("the input is %v, so this rule was not checked", err), "OBI-01", "OBI-03")
			checkDeclaredVersion(&c, versionView(data))
		case errors.As(err, &lone):
			// OBI-01 is decided: the input is UTF-8 JSON with no repeated
			// name. So is OBI-03, on the member the exact scan reads the
			// version from. The other rules read values this SDK cannot
			// carry.
			c.inconclusiveExcept(fmt.Sprintf("%v, so this rule was not checked", err), "OBI-01", "OBI-03")
			checkDeclaredVersion(&c, versionView(data))
		default:
			// OBI-02 through OBI-13 govern the JSON value only when
			// OBI-01 holds: on a text violating it they impose nothing,
			// and the violation alone establishes non-conformance (§10).
			c.findings = append(c.findings, obi01Violation(data, err))
			c.notApplicableExcept("OBI-01")
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
	var doc Document
	if err := doc.decodeVerified(data); err != nil { // OBI-01 verified the bytes
		return nil, report, verr
	}
	return &doc, report, verr
}

// obi01Violation is the OBI-01 finding for input verifyExactJSON refuses,
// located at the object that repeats a member name when that is why.
func obi01Violation(data []byte, err error) Finding {
	if duplicate := (*duplicateNameError)(nil); errors.As(err, &duplicate) {
		return Finding{Rule: "OBI-01", Status: EvidenceViolated, Path: duplicate.location, Message: "repeats the member name " + strconv.Quote(duplicate.name), Position: obi01Position(data, err)}
	}
	return Finding{Rule: "OBI-01", Status: EvidenceViolated, Message: fmt.Sprintf("not a JSON document this specification accepts: %v", err), Position: obi01Position(data, err)}
}

// ownLimit says what limit of this SDK an encoding failure records, or "",
// reading the failure as encoding/json returned it. It follows the chain only
// through errors this package wrote: into a *json.MarshalerError only when the
// marshaler is one of this package's types, whose failure is its own check's,
// and through single wrappings. It never reads what a caller's marshaler
// returned, which may carry any error, a limit this SDK reported elsewhere
// (from ParseDocument, say) included; nor a schema's failure, which
// encodeObject keeps as text, so one of this package's types placed in a
// schema is the caller's marshaler too (encodeSchemas).
func ownLimit(err error) string {
	for err != nil {
		if failure, isMarshaler := err.(*json.MarshalerError); isMarshaler {
			if !ownType(failure.Type) {
				return ""
			}
			err = failure.Err
			continue
		}
		if err == errNestingLimit {
			return "is " + errNestingLimit.Error()
		}
		if _, isLone := err.(*loneSurrogateError); isLone {
			return "holds an escape of a lone UTF-16 surrogate, which this SDK does not carry"
		}
		err = errors.Unwrap(err)
	}
	return ""
}

// modelLimitError reports a document the model does not write because it
// holds what this SDK cannot read or carry, which is no defect of the value
// (Reports and Verdicts): it nests deeper than encoding/json reads, or holds
// a string escaping a lone UTF-16 surrogate. It matches ErrInconclusive.
type modelLimitError struct {
	// what says what the document holds, as "the document <what>".
	what string
}

func (e *modelLimitError) Error() string {
	return ErrInconclusive.Error() + ": the document " + e.what + ", so it is not read"
}

func (e *modelLimitError) Is(target error) bool { return target == ErrInconclusive }

// documentView encodes a host document and decodes the generic JSON view the
// document rules judge. A document beyond this SDK's own limits, which the
// model does not write, returns a *modelLimitError, but only when core itself
// established the limit while checking this document's representation (see
// ownLimit). Any other encoding failure is the value's, and is kept as text:
// it can be a marshaler's own error held in a schema, which may say anything,
// and it must match no category.
func documentView(d Document) (any, error) {
	data, err := json.Marshal(d)
	if what := ownLimit(err); what != "" {
		return nil, &modelLimitError{what: what}
	}
	if err != nil {
		return nil, fmt.Errorf("openbindings: encode document: %v", err)
	}
	var view any
	if err := unmarshalJSON(data, &view); err != nil {
		return nil, fmt.Errorf("openbindings: decode encoded document: %w", err)
	}
	return view, nil
}

// versionView returns what OBI-03 judges of input of any depth, read by the
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

// checkDeclaredVersion decides OBI-03 from a document's generic view, which
// holds even when the value is not a string.
func checkDeclaredVersion(c *ruleChecks, view any) {
	object, _ := view.(map[string]any)
	value, present := object["openbindings"]
	version, isString := value.(string)
	switch {
	case !present:
		c.violated("OBI-03", "", "missing the required openbindings member")
	case !isString:
		c.violated("OBI-03", "/openbindings", fmt.Sprintf("must be a SemVer 2.0.0 string; got %s", jsonTypeName(value)))
	case !isValidSemver(version):
		c.violated("OBI-03", "/openbindings", fmt.Sprintf("%q is not a valid SemVer 2.0.0 string", version))
	}
}

// inputVersionRefusal applies this SDK's version policy to the version input
// declares (§8.1), read from its bytes (see declaredVersion), for input
// OBI-01 refuses or the decoder cannot read: the version decision precedes
// interpreting a document under this version's rules, OBI-01 included. Input
// that declares no version, such as input holding an ill-formed byte, is
// governed by this line's rules (§10), so it is never refused.
func inputVersionRefusal(data []byte) *VersionRefusalError {
	version, declared := declaredVersion(data)
	if !declared {
		return nil
	}
	return versionRefusalOf(version)
}

// declaredVersionRefusal applies this SDK's version policy to the version a
// document's generic view declares. The view is of a text that meets OBI-01,
// so its root object's openbindings member, when a SemVer string, is the
// version the text declares (§8.1). The decision precedes interpretation
// under this version's semantics, the embedded document schema included. A
// missing or malformed version is OBI-03's concern, decided with the other
// rules, so it is not a refusal.
func declaredVersionRefusal(view any) *VersionRefusalError {
	object, _ := view.(map[string]any)
	version, ok := object["openbindings"].(string)
	if !ok {
		return nil
	}
	return versionRefusalOf(version)
}

// interpretable applies the version decision an entry point that interprets
// a Document in memory makes first: a *VersionRefusalError for a well-formed
// version outside the supported set (CheckVersion), and for no valid version the
// *ValidationError naming the OBI-03 violation Document.Validate also
// establishes, neither of which it interprets; nil otherwise.
func interpretable(version string) error {
	if refusal := versionRefusalOf(version); refusal != nil {
		return refusal
	}
	if !isValidSemver(version) {
		var c ruleChecks
		checkDeclaredVersion(&c, map[string]any{"openbindings": version})
		return c.violationError()
	}
	return nil
}

// versionRefusalOf applies this SDK's version policy to a declared version.
// It returns nil for an accepted version and for a malformed one, which is
// OBI-03's concern rather than a refusal.
func versionRefusalOf(version string) *VersionRefusalError {
	if !isValidSemver(version) {
		return nil
	}
	msg, refused, err := versionRefusal(version)
	if err != nil || !refused {
		return nil
	}
	return &VersionRefusalError{Version: version, Reason: msg}
}

// checkDocument records evidence for OBI-02 through OBI-13 on the generic
// view of a document whose version has already been accepted.
//
// Each rule is judged literally on the values the document holds. A rule
// quantifies over values of a kind: an absent member, or one of a JSON type
// outside the rule's domain, gives it nothing to judge there. A member whose
// type contradicts what the rule requires of it violates the rule: an
// operation reference that is a number names no operation key. Where the
// document schema requires a member or a type, OBI-02 also reports it. No
// rule evaluates a value against the document's schemas: an example is an
// author claim, whose truth is outside conformance (§5, Author claims).
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
			d.checkReference(dependency, path, "operation", "OBI-08", operations, "operation key")
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
		d.checkReference(binding, path, "operation", "OBI-06", operations, "operation key")
		d.checkReference(binding, path, "source", "OBI-07", sources, "source")
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

// checkReference decides a referential rule (OBI-06, OBI-07, OBI-08)
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
// (OBI-04, OBI-05), schemas, and example keys.
func (d *documentCheck) checkOperations(operations map[string]any) {
	aliasOwner := map[string]string{}
	for _, key := range sortedKeys(operations) {
		path := jsonpointer.Format("operations", key)
		validateIdent(d.c, path, key)
		operation, ok := operations[key].(map[string]any)
		if !ok {
			continue
		}

		// OBI-05: an operation's identifiers are its key plus its aliases,
		// and every identifier in the document is distinct.
		aliases, _ := operation["aliases"].([]any)
		seen := map[string]bool{}
		for index, element := range aliases {
			aliasPath := jsonpointer.Format("operations", key, "aliases", strconv.Itoa(index))
			alias, ok := element.(string)
			if !ok {
				d.c.violated("OBI-04", aliasPath, fmt.Sprintf("an alias is a name matching ^[A-Za-z0-9_][A-Za-z0-9_.-]*$; got %s", jsonTypeName(element)))
				continue
			}
			validateIdent(d.c, aliasPath, alias)
			switch owner, owned := aliasOwner[alias]; {
			case alias == key:
				d.c.violated("OBI-05", aliasPath, fmt.Sprintf("%q duplicates the operation's own key", alias))
			case seen[alias]:
				d.c.violated("OBI-05", aliasPath, fmt.Sprintf("%q is listed more than once", alias))
			case hasKey(operations, alias):
				d.c.violated("OBI-05", aliasPath, fmt.Sprintf("%q conflicts with operation key %q", alias, alias))
			case owned && owner != key:
				d.c.violated("OBI-05", aliasPath, fmt.Sprintf("%q is also an alias of %q", alias, owner))
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
// meta-schema validity (OBI-10) and the dialect and reference-form rules its
// walk applies (OBI-09, OBI-11, OBI-12).
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

// identPattern enforces OBI-04: every map key and every operation alias must
// match. The grammar permits a leading digit (2fa.verify): names are opaque data
// labels, not host-language identifiers, so only the start character is
// constrained to an alphanumeric or underscore.
var identPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// validateIdent records an OBI-04 violation if id does not match the identifier pattern.
func validateIdent(c *ruleChecks, prefix, id string) {
	if !identPattern.MatchString(id) {
		c.violated("OBI-04", prefix, fmt.Sprintf("%q does not match identifier pattern ^[A-Za-z0-9_][A-Za-z0-9_.-]*$", id))
	}
}

const draft202012URI = "https://json-schema.org/draft/2020-12/schema"

// walkSchema walks a schema and every subschema of it the 2020-12 meta-schema
// validates as one, the legacy definitions and the schema values of the
// legacy dependencies included, applying:
//   - OBI-09: every $schema names the 2020-12 dialect, with or without an
//     empty fragment. Its placement is JSON Schema's to report, not a document
//     rule.
//   - OBI-11 at OBI positions, in the document resource: every $ref and
//     $dynamicRef is a well-formed URI-reference (RFC 3986 §4.1) that is an
//     absolute URI or a same-document reference (empty, or a fragment
//     alone), and every $id is a well-formed absolute URI.
//   - OBI-12 at OBI positions, in the document resource: a same-document
//     $ref or $dynamicRef identifies a schema at an OBI position.
//
// A schema that declares $id is itself at an OBI position, so OBI-11 judges
// its $id, but everything else in the resource it declares, its own keywords
// included, is the resource's business, resolved per JSON Schema 2020-12
// (§7.2): OBI-11 and OBI-12 stop at its boundary. OBI-09 governs every
// schema the document contains, inside resources too.
func (d *documentCheck) walkSchema(path *schemaPath, schema any, inResource bool) {
	s, ok := schema.(map[string]any)
	if !ok {
		// Boolean schemas carry no keywords; any other value is OBI-10's.
		return
	}

	// A $schema that is not a string is already refused by the meta-schemas.
	if value, present := s["$schema"]; present && value != draft202012URI && value != draft202012URI+"#" {
		d.c.violated("OBI-09", path.at("$schema"), fmt.Sprintf("must name the 2020-12 dialect, %q, with or without an empty fragment; got %s", draft202012URI, describeJSON(value)))
	}

	if !inResource {
		if value, present := s["$id"]; present {
			id, isString := value.(string)
			if !isString {
				d.c.violated("OBI-11", path.at("$id"), fmt.Sprintf("an $id is an absolute URI string; got %s", jsonTypeName(value)))
			} else if wellFormed, hasScheme := uriReference(id); !wellFormed {
				d.c.violated("OBI-11", path.at("$id"), fmt.Sprintf("%q is not a well-formed URI-reference (RFC 3986 §4.1)", id))
			} else if !hasScheme {
				d.c.violated("OBI-11", path.at("$id"), fmt.Sprintf("%q must be an absolute URI", id))
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
				d.c.violated("OBI-11", path.at(keyword), fmt.Sprintf("a %s is a URI-reference string; got %s", keyword, jsonTypeName(value)))
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

// checkDocumentReference applies OBI-11 and OBI-12 to the $ref or
// $dynamicRef keyword names, held by the schema a walk of the document
// resource is at.
func (d *documentCheck) checkDocumentReference(path *schemaPath, keyword, ref string) {
	wellFormed, hasScheme := uriReference(ref)
	sameDocument := ref == "" || strings.HasPrefix(ref, "#")
	switch {
	case !wellFormed:
		d.c.violated("OBI-11", path.at(keyword), fmt.Sprintf("%q is not a well-formed URI-reference (RFC 3986 §4.1)", ref))
		return
	case sameDocument:
	case hasScheme:
		// An absolute URI is outside OBI-12; JSON Schema resolves it.
		return
	default:
		d.c.violated("OBI-11", path.at(keyword), fmt.Sprintf("%q must be an absolute URI or a same-document reference, not a relative reference", ref))
		return
	}
	// A plain name declared more than once is OBI-13's to report.
	if found := d.schemas.lookUpSameDocument(ref, d.view); found.why != "" && !found.declaredTwice {
		d.c.violated("OBI-12", path.at(keyword), fmt.Sprintf("the %s %q %s", keyword, ref, found.why))
	}
}

// checkUniqueness applies OBI-13: no plain name is declared more than once
// in the document resource, each $anchor and each $dynamicAnchor declaring it
// counting once, and no two schemas the document contains declare the same
// $id as OBI-13 compares identifiers (comparableID). Each declaration is a
// finding.
func (d *documentCheck) checkUniqueness() {
	for _, name := range slices.Sorted(maps.Keys(d.schemas.anchors)) {
		declarations := d.schemas.anchors[name]
		if len(declarations) < 2 {
			continue
		}
		for _, declaration := range declarations {
			d.c.violated("OBI-13", declaration.at.from(nil)+jsonpointer.Format(declaration.keyword), fmt.Sprintf("declares the plain name %q, which the document resource declares %d times", name, len(declarations)))
		}
	}
	for _, id := range slices.Sorted(maps.Keys(d.schemas.identifiers)) {
		declarations := d.schemas.identifiers[id]
		if len(declarations) < 2 {
			continue
		}
		for _, at := range declarations {
			d.c.violated("OBI-13", at.from(nil)+jsonpointer.Format("$id"), fmt.Sprintf("declares %s, which %d schemas the document contains declare", id, len(declarations)))
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
