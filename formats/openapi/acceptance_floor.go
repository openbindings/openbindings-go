package openapi

// The invalid-artifact acceptance floor (block 8d-1): one pure instrument over
// the ENTRY DOCUMENT's raw JSON image, applied identically by every engine.
//
// This file is a PORT of the reviewed corpus-lab reference implementation
// (`corpus-lab/scripts/census-oas-owning-units.mjs`, `censusOne` +
// `projectLadder`; per-class routing from
// `corpus-lab/scripts/oas-owning-units-policy.mjs`, block 8b record), not a
// re-derivation. The ladder, its three propagation predicates, and the
// per-class answers are the ratified Scenario-B ruling
// (`design/openapi-invalid-artifact-acceptance-floor-ruling.md` §8a):
// every defect confines to the smallest unit that owns it.
//
// Rungs, narrowest first: request media alternative -> response -> operation
// -> source. Only the request media alternative and the operation are UNITS
// under interface-synthesizer 0.2's inventory; response-side rungs are
// locating rungs whose defects attribute to the containing operation.
//
// Propagation predicates -- the ONLY three ways a defect climbs a rung:
//
//	P1 input:   a defect in an effective Parameter Object, or in its schema
//	            closure, makes the OPERATION invalid.
//	P2 output:  a defect leaving a success response declaration that DECLARED
//	            a body with no surviving representable media alternative makes
//	            the OPERATION invalid.
//	P3 address: a defect in the Paths Object key or in the HTTP-method member
//	            makes the OPERATION invalid.
//
// Nothing else climbs. A defective REQUEST media alternative owns its own
// entry and never invalidates its operation (the established
// `excluded`-alternative convention's shape; a REQUIRED body whose every
// declared alternative is invalid keeps the existing
// `openapi.unresolvable_request_body` / OAPI-P-04 exclusion).
//
// Whole-source refusal is `openbindings.openapi@1` §3's three-part text: part
// 1 (the closed load gates) is owned by the load path and never asked here;
// part 2 -- exactly one derived refusal -- is computed by this instrument:
// the source refuses only when no addressable target remains. Excluded
// operations are ADDRESSED targets and never trigger it ("addressable, not
// representable"). An artifact that conformantly declares no target is
// accepted with an empty interface. A Paths Object member that is an EXTERNAL
// Reference Object defers the question to resolution.
//
// The shipped defect roster is the 8b-reviewed class table
// (`corpus-lab/data/oas-owning-units/cases.json`, D1-D13 + D1s; D1n/D1a are
// referred and deliberately not emitted), plus the registry-scoped class D15 --
// a Schema Object keyword whose value violates the governing dialect's declared
// JSON type for it, whose answer is stated once in the reviewed owning-unit
// policy and is the same as D1's. Additionally, an internal reference
// that identifies no location in the entry document invalidates each unit
// whose closure reaches the referencing site (P1/P2 per position) -- the
// per-reaching-unit reading of unresolvable references (block 8d design §3);
// a pointer whose traversal stops ON another Reference Object is the
// edition-split traversal question and is never a defect here.

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const invalidUnitReasonCode = "openapi.invalid_unit"

var floorEditions30 = map[string]bool{"3.0.0": true, "3.0.1": true, "3.0.2": true, "3.0.3": true, "3.0.4": true}
var floorEditions31 = map[string]bool{"3.1.0": true, "3.1.1": true, "3.1.2": true}

// The 3.0 line: OAS 3.0.4 §4.4 enumerates the six type names and directs
// "null" to `nullable`; 3.0.0-3.0.3 state "null is not supported as a type".
var floorTypes30 = map[string]bool{"boolean": true, "object": true, "array": true, "number": true, "string": true, "integer": true}

// The 3.1 line: JSON Schema Validation 2020-12 §6.1.1.
var floorTypes31 = map[string]bool{"null": true, "boolean": true, "object": true, "array": true, "number": true, "string": true, "integer": true}

var floorComponentFields30 = []string{"schemas", "responses", "parameters", "examples", "requestBodies", "headers", "securitySchemes", "links", "callbacks"}
var floorComponentFields31 = []string{"schemas", "responses", "parameters", "examples", "requestBodies", "headers", "securitySchemes", "links", "callbacks", "pathItems"}

// "All the fixed fields declared above are objects that MUST use keys that
// match the regular expression: ^[a-zA-Z0-9\.\-_]+$" (each accepted edition).
var floorComponentKeyRE = regexp.MustCompile(`^[a-zA-Z0-9.\-_]+$`)

// RFC 6838 restricted-name grammar, reached through the OAS's "The key is a
// media type or media type range".
var floorMediaRE = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+/[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+(\s*;.*)?$`)

var floorSuccessCodeRE = regexp.MustCompile(`^2\d\d$`)

// The Schema Object keyword inventory the closure walk follows -- the union of
// both edition lines; a keyword absent from an edition never occurs there.
var floorSchemaSubSingle = []string{"items", "not", "additionalProperties", "propertyNames", "contains", "if", "then", "else", "unevaluatedItems", "unevaluatedProperties", "additionalItems"}
var floorSchemaSubList = []string{"allOf", "anyOf", "oneOf", "prefixItems"}
var floorSchemaSubMap = []string{"properties", "patternProperties", "definitions", "$defs", "dependentSchemas"}

// The shipped classes. D2/D3/D5 route `nowhere` (no unit reaches them; D2 and
// D5 still count as destroyed declared positions for the part-2 verdict).
// D1n/D1a are referred and never emitted. URef is the unresolvable internal
// reference at its referencing site.
const (
	floorD1   = "D1"
	floorD1s  = "D1s"
	floorD2   = "D2"
	floorD2b  = "D2b"
	floorD3   = "D3"
	floorD4   = "D4"
	floorD5   = "D5"
	floorD6   = "D6"
	floorD7   = "D7"
	floorD8   = "D8"
	floorD9   = "D9"
	floorD10  = "D10"
	floorD11  = "D11"
	floorD12  = "D12"
	floorD13  = "D13"
	floorD15  = "D15"
	floorURef = "URef"
)

// floorAuthority is the per-(class, edition-line) authority citation carried
// byte-identically by every engine's emitted entries and pinned by the shared
// case tables. Citations, not verbatim quotes; each restates the rule the
// 8b-reviewed table proves at the pinned OAS renderings.
func floorAuthority(class, line string) string {
	is30 := line == "3.0"
	switch class {
	case floorD1:
		if is30 {
			return "OAS 3.0 line, Data Types / Schema Object: `type` names one of boolean, object, array, number, string, integer; Value MUST be a string"
		}
		return "OAS 3.1 line via JSON Schema Validation 2020-12 §6.1.1: `type` names one of null, boolean, object, array, number, string, integer"
	case floorD1s:
		if is30 {
			return "OAS 3.0 line, Schema Object: `type` — Value MUST be a string"
		}
		return "JSON Schema Validation 2020-12 §6.1.1: the value of `type` MUST be either a string or an array of strings"
	case floorD2:
		return "OAS, Paths Object: each patterned field holds a Path Item Object"
	case floorD2b:
		return "OAS, Path Item Object: each HTTP-method field holds an Operation Object"
	case floorD4:
		return "OAS, Components Object: each fixed field is a map of named objects; a member map whose single key is `$ref` declares no component, so every pointer beneath it identifies no location"
	case floorD5:
		return "OAS, OpenAPI Object: the field holds its declared object; no Reference Object alternative is named at this position"
	case floorD6:
		return "OAS, Components Object / Response Object: a `components.responses` member is a Response Object, whose `description` is REQUIRED; a reference from a schema position denotes the value at the pointer"
	case floorD7:
		return "OAS, Responses Object: each member holds a Response Object or a Reference Object resolving to one"
	case floorD8:
		return "OAS, content maps: the key is a media type or media type range (RFC 6838 restricted-name grammar via RFC 7231 Appendix D)"
	case floorD9:
		if is30 {
			return "OAS 3.0 line, Response Object: `description` is REQUIRED"
		}
		return "OAS 3.1 line, Response Object: `description` is REQUIRED"
	case floorD10:
		return "OAS, Parameter Object: `name` and `in` are REQUIRED"
	case floorD11:
		return `OAS, Components Object: fixed-field maps MUST use keys matching ^[a-zA-Z0-9\.\-_]+$`
	case floorD12:
		return "OAS, content maps: each media type key's value is a Media Type Object; no Reference Object alternative is named at this position"
	case floorD13:
		return "OAS, Paths Object: each patterned field key begins with a forward slash and is appended to the Server Object's url to construct the target URL"
	case floorD15:
		if is30 {
			return "OAS 3.0 line, Schema Object: a keyword's value carries the JSON type the governing dialect declares for it -- `items` Value MUST be an object and not an array; `properties` definitions MUST be a Schema Object; `required` and `enum` are taken directly from JSON Schema, where each is an array"
		}
		return "OAS 3.1 line via JSON Schema 2020-12: a keyword's value carries the JSON type the dialect declares for it -- `required` (Validation §6.5.3) and `enum` (§6.1.2) are arrays; `properties` members and `items` and `contains` are schemas, which on this line may be objects or booleans; `exclusiveMinimum` (§6.2.5) and `exclusiveMaximum` (§6.2.3) are numbers"
	case floorURef:
		if is30 {
			return "OAS 3.0 line, Reference Object: $ref follows JSON Reference; its fragment is a JSON Pointer (RFC 6901) and identifies no location in the entry document"
		}
		return "OAS 3.1 line, Reference Object: $ref is a URI-Reference whose JSON Pointer fragment (RFC 6901) identifies no location in the entry document"
	}
	return ""
}

// floorDefect is one defective position with its per-line authority citation.
type floorDefect struct {
	Class     string
	Position  string
	Authority string
}

// floorOp is one raw-inventory operation's ladder verdict.
type floorOp struct {
	Ref         string
	Path        string
	Method      string
	Disposition string // "represented" | "invalid" | "excluded-request-media"
	// Defects: for an invalid operation, the defective positions that climbed
	// (P1/P2/P3), sorted by position.
	Defects []floorDefect
	// InvalidAlternatives: request media alternatives invalidated by the
	// ladder, altRef -> defects (each sorted by position), in declared-key
	// sorted order.
	InvalidAlternatives map[string][]floorDefect
	AltOrder            []string
	// Projections: per unit (the operation's own ref, or a request media
	// alternative's ref), the invalid positions that cost it nothing:
	// non-climbing response-rung defects plus reached projection-class
	// positions (D6/D11). Sorted by position within each unit.
	Projections map[string][]floorDefect
	ProjOrder   []string
	// RequestMediaExcluded: the ladder invalidated every declared alternative
	// of a REQUIRED request body -- the existing
	// openapi.unresolvable_request_body / OAPI-P-04 convention excludes the
	// operation; carried, not reopened.
	RequestMediaExcluded bool
}

// acceptanceFloor is the instrument's result for one entry document.
type acceptanceFloor struct {
	Edition string
	Line    string // "3.0" | "3.1"
	// Refusal is non-empty when §3 part 2's single derived rule fires: no
	// addressable target remains.
	Refusal string
	// Ops in deterministic order: sorted path keys x httpMethods order.
	Ops     map[string]*floorOp
	OpOrder []string
	// ExternalPathItemMembers counts Paths Object members that are external
	// Reference Objects: externals defer the part-2 question to resolution.
	ExternalPathItemMembers int

	// ---- the confinement pass's read-only view of this instrument ---------
	//
	// Block 8d-2's load-path confinement never decides for itself what a raw
	// position means: every question it asks is answered here, by the ladder.
	// These four maps carry no emission and change no verdict.

	// Attributed records every raw position the ladder CLASSIFIES, including
	// the classes whose owning unit is NONE and which therefore emit nothing
	// (D2, D3, and D5 at `info`/`tags`). A located defect this map cannot name
	// is never confined: the load refuses with its original error.
	Attributed map[string]string
	// SchemaPositions is the set of pointers this instrument's own closure
	// walk reads as Schema Object positions.
	SchemaPositions map[string]bool
	// ResponseMemberDefects is the set of D7 positions -- a Responses Object
	// member whose value cannot be read as a Response Object.
	ResponseMemberDefects map[string]bool
	// ConformantSchemaComponents is the set of `components.responses` members
	// D6 hits AND that are Schema-shaped in the decidable sense
	// `isFloorSchemaShaped` states, so that a schema-position reference to one
	// denotes a value that needs no edition disjunction to be read as a schema.
	// Failing D6's Response Object test is only a DISPROOF of Response
	// Object-hood; Schema-shape is tested separately and positively.
	ConformantSchemaComponents map[string]bool
	// ClimbingURefSites is the set of URef positions -- unresolvable internal
	// references at their referencing site -- whose ladder verdict CLIMBS: the
	// position invalidates the operation that owns it, or invalidates one
	// request media alternative. A URef position no unit's closure walk reaches
	// is absent: the ladder classifies nothing there.
	//
	// This set is a CANDIDATE list and nothing more. It is keyed by POSITION
	// while the ladder's verdict is per UNIT, and this instrument's own closure
	// walk is not the traversal an emitter uses, so membership here is no
	// evidence that neutralising a position leaves shipped content unchanged.
	// Two blocks tried to make it that evidence by narrowing the set -- first
	// by never adding a projecting site, then by subtracting every position a
	// surviving unit's `Projections` names -- and a position reached through a
	// channel this walk does not visit defeated both. The question is decided
	// where it actually lives: `oas_confinement.go`'s EMISSION GATE asks the
	// engine's own emitter, over the document the confinement produced, whether
	// anything the pass authored reaches emitted content. Nothing about this
	// field may be read as answering that.
	ClimbingURefSites map[string]bool
}

func (f *acceptanceFloor) opVerdict(ref string) *floorOp {
	if f == nil {
		return nil
	}
	return f.Ops[ref]
}

// attributes reports whether the ladder classifies the raw position the
// confinement oracle located. A position one level BELOW a classified
// position, at its `$ref` member, is the same finding read one level deeper
// (block 8a §2: `{}` at `#/paths` is a conforming Paths Object, so the defect
// is the string at the `$ref` member) and is accepted as the same attribution.
func (f *acceptanceFloor) attributes(pointer string) (string, bool) {
	if f == nil {
		return "", false
	}
	if class, ok := f.Attributed[pointer]; ok {
		return class, true
	}
	if parent, last, ok := floorSplitPointer(pointer); ok && last == "$ref" {
		if class, found := f.Attributed[parent]; found {
			return class, true
		}
	}
	return "", false
}

// floorSplitPointer splits a `#/a/b` pointer into `#/a` and the decoded final
// token.
func floorSplitPointer(pointer string) (string, string, bool) {
	i := strings.LastIndex(pointer, "/")
	if i < 0 {
		return "", "", false
	}
	token := pointer[i+1:]
	token = strings.ReplaceAll(token, "~1", "/")
	token = strings.ReplaceAll(token, "~0", "~")
	return pointer[:i], token, true
}

// computeAcceptanceFloorFromBytes parses the entry document's raw bytes and
// computes the floor. A byte stream that does not parse, whose root is not an
// object, or whose edition is not accepted returns nil: those verdicts belong
// to §3 part 1's closed load gates, which the load path owns.
func computeAcceptanceFloorFromBytes(data []byte) *acceptanceFloor {
	if len(data) == 0 {
		return nil
	}
	tree, ok := parseRawResource(data)
	if !ok {
		return nil
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return nil
	}
	return computeAcceptanceFloor(root)
}

// computeAcceptanceFloor computes the floor over a raw entry-document tree.
func computeAcceptanceFloor(root map[string]any) *acceptanceFloor {
	edition, _ := root["openapi"].(string)
	var line string
	switch {
	case floorEditions30[edition]:
		line = "3.0"
	case floorEditions31[edition]:
		line = "3.1"
	default:
		return nil // the edition gate (part 1) owns every other value
	}

	f := &acceptanceFloor{
		Edition:                    edition,
		Line:                       line,
		Ops:                        map[string]*floorOp{},
		Attributed:                 map[string]string{},
		SchemaPositions:            map[string]bool{},
		ResponseMemberDefects:      map[string]bool{},
		ConformantSchemaComponents: map[string]bool{},
		ClimbingURefSites:          map[string]bool{},
	}
	attribute := func(class, position string) {
		if _, seen := f.Attributed[position]; !seen {
			f.Attributed[position] = class
		}
	}

	componentFields := floorComponentFields30
	types := floorTypes30
	if line == "3.1" {
		componentFields = floorComponentFields31
		types = floorTypes31
	}

	defect := func(class, position string) floorDefect {
		return floorDefect{Class: class, Position: position, Authority: floorAuthority(class, line)}
	}

	// ---- detection: the defect set and the projection-class set ------------

	defectPositions := map[string]floorDefect{}     // carries: defect (climbs by position)
	projectionPositions := map[string]floorDefect{} // carries: projection (never climbs; reached units get entries)
	destroyedDeclared := false                      // D2 members / D5-at-paths: destroyed declared positions (part 2)

	addDefect := func(d floorDefect) {
		if _, seen := defectPositions[d.Position]; !seen {
			defectPositions[d.Position] = d
		}
		attribute(d.Class, d.Position)
	}

	// D5: paths carrying a Reference Object (info/tags route `nowhere` and
	// destroy no addressable position).
	pathsValue, pathsDeclared := root["paths"]
	pathsIsRef := isFloorRefObj(pathsValue)
	if pathsIsRef {
		destroyedDeclared = true
		attribute(floorD5, "#/paths")
	}

	// D3 and the two `nowhere` legs of D5, recorded for attribution ONLY.
	// Neither emits a coverage entry (`info` never enters the OBI and no
	// unit's emitted closure reaches it; the same holds for `tags`), so the
	// ladder's answers are unchanged -- but a load-path confinement that
	// neutralises one of these positions must be able to name what it is
	// neutralising, which is what this records.
	if infoValue, infoDeclared := root["info"]; infoDeclared {
		if isFloorRefObj(infoValue) {
			attribute(floorD5, "#/info")
		}
		if infoMap, isMap := infoValue.(map[string]any); isMap {
			if license, licenseDeclared := infoMap["license"]; licenseDeclared {
				if _, isLicenseObject := license.(map[string]any); !isLicenseObject {
					attribute(floorD3, "#/info/license")
				}
			}
		}
	}
	if isFloorRefObj(root["tags"]) {
		attribute(floorD5, "#/tags")
	}

	// D4 / D11 / D6: Components Object member maps.
	comps, _ := root["components"].(map[string]any)
	if comps != nil {
		for _, fieldName := range componentFields {
			memberMap, ok := comps[fieldName].(map[string]any)
			if !ok {
				continue
			}
			keys := floorKeys(memberMap)
			if len(keys) == 1 && keys[0] == "$ref" {
				addDefect(defect(floorD4, "#/components/"+fieldName))
				continue // D4 subsumes the key-grammar reading of the same byte
			}
			for _, key := range keys {
				if !floorComponentKeyRE.MatchString(key) {
					ptr := "#/components/" + floorEsc(fieldName) + "/" + floorEsc(key)
					projectionPositions[ptr] = defect(floorD11, ptr)
					attribute(floorD11, ptr)
				}
			}
		}
		if responses, ok := comps["responses"].(map[string]any); ok {
			for _, key := range floorKeys(responses) {
				if key == "$ref" {
					continue
				}
				value := responses[key]
				if isFloorRefObj(value) {
					continue
				}
				ptr := "#/components/responses/" + floorEsc(key)
				if !isFloorResponseObject(value) {
					projectionPositions[ptr] = defect(floorD6, ptr)
					attribute(floorD6, ptr)
					// D6's class is "Schema-SHAPED and omits the REQUIRED
					// `description`". The test above establishes only the
					// second half. A `$ref` from a SCHEMA position resolves
					// normally here only when the value is also Schema-shaped,
					// so that half is tested on its own terms.
					if isFloorSchemaShaped(value) {
						f.ConformantSchemaComponents[ptr] = true
					}
				}
			}
		}
	}

	// Paths: D13 / D2 / D2b / D10 / D7 / D9 / D8 / D12 detection, and the raw
	// operation inventory.
	paths, pathsIsObj := pathsValue.(map[string]any)
	if pathsIsRef {
		pathsIsObj = false
	}
	type rawOpRow struct {
		path, method, ref string
		value             any
		pathItem          map[string]any
		pathKeyBad        bool
	}
	var rawOps []rawOpRow
	if pathsIsObj {
		for _, pathKey := range floorKeys(paths) {
			if strings.HasPrefix(pathKey, "x-") {
				continue
			}
			pathValue := paths[pathKey]
			pathKeyBad := !strings.HasPrefix(pathKey, "/")
			if pathKeyBad {
				addDefect(defect(floorD13, "#/paths/"+floorEsc(pathKey)))
			}
			pathItem, isMap := pathValue.(map[string]any)
			if !isMap {
				destroyedDeclared = true // D2: a declared member that is not a Path Item Object
				attribute(floorD2, "#/paths/"+floorEsc(pathKey))
				continue
			}
			if isFloorRefObj(pathValue) && !strings.HasPrefix(refString(pathValue), "#") {
				f.ExternalPathItemMembers++
			}
			for _, method := range httpMethods {
				opValue, methodDeclared := pathItem[method]
				if !methodDeclared {
					continue
				}
				ref := buildJSONPointerRef(pathKey, method)
				rawOps = append(rawOps, rawOpRow{path: pathKey, method: method, ref: ref, value: opValue, pathItem: pathItem, pathKeyBad: pathKeyBad})
				opMap, opIsMap := opValue.(map[string]any)
				if !opIsMap {
					addDefect(defect(floorD2b, ref))
					continue
				}
				// D10: Parameter Objects omitting name or in.
				for _, holder := range []struct {
					list any
					ptr  string
				}{
					{pathItem["parameters"], "#/paths/" + floorEsc(pathKey) + "/parameters"},
					{opMap["parameters"], ref + "/parameters"},
				} {
					list, isList := holder.list.([]any)
					if !isList {
						continue
					}
					for i, p := range list {
						if isFloorRefObj(p) {
							continue
						}
						pm, isM := p.(map[string]any)
						if !isM {
							continue
						}
						_, hasName := pm["name"]
						_, hasIn := pm["in"]
						if !hasName || !hasIn {
							addDefect(defect(floorD10, holder.ptr+"/"+strconv.Itoa(i)))
						}
					}
				}
				// Responses: D7 / D9 / D8 / D12.
				if responses, isM := opMap["responses"].(map[string]any); isM {
					for _, code := range floorKeys(responses) {
						if strings.HasPrefix(code, "x-") {
							continue
						}
						rptr := ref + "/responses/" + floorEsc(code)
						rv := responses[code]
						if isFloorRefObj(rv) {
							target, res := floorResolveInternal(root, refString(rv))
							if res.resolved && !isFloorResponseObject(target) {
								addDefect(defect(floorD7, rptr))
								f.ResponseMemberDefects[rptr] = true
							}
							continue
						}
						rvm, rvIsMap := rv.(map[string]any)
						if !rvIsMap {
							addDefect(defect(floorD7, rptr))
							f.ResponseMemberDefects[rptr] = true
							continue
						}
						if !isFloorResponseObject(rv) {
							addDefect(defect(floorD9, rptr))
						}
						if content, isC := rvm["content"].(map[string]any); isC {
							for _, mediaKey := range floorKeys(content) {
								mptr := rptr + "/content/" + floorEsc(mediaKey)
								if !floorMediaRE.MatchString(mediaKey) {
									addDefect(defect(floorD8, mptr))
								}
								if isFloorRefObj(content[mediaKey]) {
									addDefect(defect(floorD12, mptr))
								}
							}
						}
					}
				}
				// Request body content keys: D8 / D12.
				if rb, isM := opMap["requestBody"].(map[string]any); isM && !isFloorRefObj(rb) {
					if content, isC := rb["content"].(map[string]any); isC {
						for _, mediaKey := range floorKeys(content) {
							mptr := ref + "/requestBody/content/" + floorEsc(mediaKey)
							if !floorMediaRE.MatchString(mediaKey) {
								addDefect(defect(floorD8, mptr))
							}
							if isFloorRefObj(content[mediaKey]) {
								addDefect(defect(floorD12, mptr))
							}
						}
					}
				}
			}
		}
	}

	// D1 / D1s / D15 over every reachable Schema Object position: component
	// schemas plus operation schema roots. D1n/D1a (the two 3.1-shaped null
	// spellings on the 3.0 line) are referred and deliberately NOT emitted.
	visitSchema := func(node map[string]any, ptr string) {
		// D15 -- a Schema Object keyword whose value violates the governing
		// dialect's declared JSON type for it. Same derivation as D1/D1s: the
		// nearest rung containing the Schema Object owns it, and P1/P2 decide
		// whether it climbs. Edition-scoped where the two lines differ: a
		// boolean `exclusiveMinimum` is the 3.0 line's own correct draft-4
		// spelling and is not this class there. A boolean-valued schema
		// position on the 3.0 line is REFERRED, not classified -- see
		// isFloorSchemaValued. Independent of `type`, which is why these run
		// before the type gate below.
		if value, declared := node["required"]; declared {
			if _, isList := value.([]any); !isList {
				addDefect(defect(floorD15, ptr+"/required"))
			}
		}
		if value, declared := node["enum"]; declared {
			if _, isList := value.([]any); !isList {
				addDefect(defect(floorD15, ptr+"/enum"))
			}
		}
		if _, isList := node["items"].([]any); isList {
			addDefect(defect(floorD15, ptr+"/items"))
		}
		if properties, isMap := node["properties"].(map[string]any); isMap {
			for _, key := range floorKeys(properties) {
				if isFloorSchemaValued(properties[key]) {
					continue
				}
				addDefect(defect(floorD15, ptr+"/properties/"+floorEsc(key)))
			}
		}
		if line == "3.1" {
			for _, keyword := range []string{"exclusiveMaximum", "exclusiveMinimum"} {
				if value, declared := node[keyword]; declared && !isFloorNumber(value) {
					addDefect(defect(floorD15, ptr+"/"+keyword))
				}
			}
			if value, declared := node["contains"]; declared && !isFloorSchemaValued(value) {
				addDefect(defect(floorD15, ptr+"/contains"))
			}
		}

		t, typeDeclared := node["type"]
		if !typeDeclared {
			return
		}
		switch typed := t.(type) {
		case string:
			if line == "3.0" && typed == "null" {
				return // D1n, referred
			}
			if !types[typed] {
				addDefect(defect(floorD1, ptr+"/type"))
			}
		case []any:
			if line == "3.0" {
				return // D1a, referred
			}
			for _, member := range typed {
				s, isS := member.(string)
				if !isS || !types[s] {
					addDefect(defect(floorD1, ptr+"/type"))
					break
				}
			}
		default:
			addDefect(defect(floorD1s, ptr+"/type"))
		}
	}
	type schemaRoot struct {
		value any
		ptr   string
	}
	var schemaRoots []schemaRoot
	if comps != nil {
		if schemas, ok := comps["schemas"].(map[string]any); ok {
			for _, key := range floorKeys(schemas) {
				schemaRoots = append(schemaRoots, schemaRoot{schemas[key], "#/components/schemas/" + floorEsc(key)})
			}
		}
	}
	for _, row := range rawOps {
		opMap, isM := row.value.(map[string]any)
		if !isM {
			continue
		}
		for _, holder := range []struct {
			list any
			ptr  string
		}{
			{row.pathItem["parameters"], "#/paths/" + floorEsc(row.path) + "/parameters"},
			{opMap["parameters"], row.ref + "/parameters"},
		} {
			list, isList := holder.list.([]any)
			if !isList {
				continue
			}
			for i, p := range list {
				pm, isPM := p.(map[string]any)
				if !isPM {
					continue
				}
				if schema, isS := pm["schema"].(map[string]any); isS {
					schemaRoots = append(schemaRoots, schemaRoot{schema, holder.ptr + "/" + strconv.Itoa(i) + "/schema"})
				}
			}
		}
		if rb, isRB := opMap["requestBody"].(map[string]any); isRB {
			if content, isC := rb["content"].(map[string]any); isC {
				for _, mediaKey := range floorKeys(content) {
					if mo, isMO := content[mediaKey].(map[string]any); isMO {
						if schema, isS := mo["schema"].(map[string]any); isS {
							schemaRoots = append(schemaRoots, schemaRoot{schema, row.ref + "/requestBody/content/" + floorEsc(mediaKey) + "/schema"})
						}
					}
				}
			}
		}
		if responses, isR := opMap["responses"].(map[string]any); isR {
			for _, code := range floorKeys(responses) {
				if rv, isRV := responses[code].(map[string]any); isRV {
					if content, isC := rv["content"].(map[string]any); isC {
						for _, mediaKey := range floorKeys(content) {
							if mo, isMO := content[mediaKey].(map[string]any); isMO {
								if schema, isS := mo["schema"].(map[string]any); isS {
									schemaRoots = append(schemaRoots, schemaRoot{schema, row.ref + "/responses/" + floorEsc(code) + "/content/" + floorEsc(mediaKey) + "/schema"})
								}
							}
						}
					}
				}
			}
		}
	}
	visitedSchemaPtrs := map[string]bool{}
	var walkSchemaTree func(node any, ptr string)
	walkSchemaTree = func(node any, ptr string) {
		nodeMap, isM := node.(map[string]any)
		if !isM {
			return
		}
		if visitedSchemaPtrs[ptr] {
			return
		}
		visitedSchemaPtrs[ptr] = true
		f.SchemaPositions[ptr] = true
		visitSchema(nodeMap, ptr)
		for _, key := range floorSchemaSubSingle {
			if child, childDeclared := nodeMap[key]; childDeclared {
				walkSchemaTree(child, ptr+"/"+floorEsc(key))
			}
		}
		for _, key := range floorSchemaSubList {
			if children, isList := nodeMap[key].([]any); isList {
				for i, child := range children {
					walkSchemaTree(child, ptr+"/"+floorEsc(key)+"/"+strconv.Itoa(i))
				}
			}
		}
		for _, key := range floorSchemaSubMap {
			if children, isC := nodeMap[key].(map[string]any); isC {
				for _, name := range floorKeys(children) {
					walkSchemaTree(children[name], ptr+"/"+floorEsc(key)+"/"+floorEsc(name))
				}
			}
		}
	}
	for _, sr := range schemaRoots {
		walkSchemaTree(sr.value, sr.ptr)
	}

	// ---- the whole-source gate at the paths member -------------------------

	if pathsIsRef || (pathsDeclared && pathsValue != nil && !pathsIsObj) {
		f.Refusal = floorPart2Refusal("the paths member cannot carry a Paths Object under the governing edition, so no target can be addressed")
		return f
	}
	if !pathsDeclared || pathsValue == nil {
		if line == "3.0" {
			f.Refusal = floorPart2Refusal("the 3.0 line makes paths a REQUIRED fixed field of the OpenAPI Object and the document declares none")
			return f
		}
		_, hasComponents := root["components"]
		_, hasWebhooks := root["webhooks"]
		if !hasComponents && !hasWebhooks {
			f.Refusal = floorPart2Refusal("the 3.1 line requires at least one paths, components, or webhooks field and the document declares none")
			return f
		}
		return f // accepted: a components/webhooks-only document, empty interface
	}

	// ---- the ladder projection ---------------------------------------------

	isDefective := func(ptr string) (floorDefect, bool) {
		d, ok := defectPositions[ptr]
		return d, ok
	}

	// D4 member maps: every pointer beneath one identifies no location, so a
	// schema-closure `$ref` descending into one is a defect at its REFERENCING
	// site, attributed to the class that explains it (the census's D4 leg).
	var d4Ptrs []string
	for pos, d := range defectPositions {
		if d.Class == floorD4 {
			d4Ptrs = append(d4Ptrs, pos)
		}
	}
	viaD4 := func(ref string) bool {
		for _, p := range d4Ptrs {
			if ref == p || strings.HasPrefix(ref, p+"/") {
				return true
			}
		}
		return false
	}

	// closureDefects walks a schema closure from rootPtr/rootValue, following
	// internal references, and returns every defect-class position found at or
	// below a visited pointer, every projection-class position likewise, and
	// every unresolvable internal reference at its referencing site.
	closureDefects := func(rootPtr string, rootValue any) (defs []floorDefect, projs []floorDefect) {
		type frame struct {
			node any
			ptr  string
		}
		stack := []frame{{rootValue, rootPtr}}
		walked := map[string]bool{}
		foundDef := map[string]bool{}
		foundProj := map[string]bool{}
		for len(stack) > 0 {
			fr := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if walked[fr.ptr] {
				continue
			}
			walked[fr.ptr] = true
			nodeMap, isM := fr.node.(map[string]any)
			if !isM {
				continue
			}
			for pos, d := range defectPositions {
				if (pos == fr.ptr || strings.HasPrefix(pos, fr.ptr+"/")) && !foundDef[pos] {
					foundDef[pos] = true
					defs = append(defs, d)
				}
			}
			for pos, d := range projectionPositions {
				if (pos == fr.ptr || strings.HasPrefix(pos, fr.ptr+"/")) && !foundProj[pos] {
					foundProj[pos] = true
					projs = append(projs, d)
				}
			}
			if ref := refString(nodeMap); ref != "" && strings.HasPrefix(ref, "#") {
				target, res := floorResolveInternal(root, ref)
				switch {
				case viaD4(ref) && !res.resolved:
					// The failure is the D4-defective member map's: the
					// referencing schema loses its target and the site is the
					// defective position, attributed to D4.
					if !foundDef[fr.ptr] {
						foundDef[fr.ptr] = true
						defs = append(defs, floorDefect{Class: floorD4, Position: fr.ptr, Authority: floorAuthority(floorD4, line)})
					}
				case res.resolved:
					stack = append(stack, frame{target, ref})
				case res.stoppedOnReference:
					// The edition-split traversal question; never a defect here.
				default:
					// Unresolvable internal reference: the referencing site is
					// the defective position.
					if !foundDef[fr.ptr] {
						foundDef[fr.ptr] = true
						defs = append(defs, floorDefect{Class: floorURef, Position: fr.ptr, Authority: floorAuthority(floorURef, line)})
					}
				}
			}
			for _, key := range floorSchemaSubSingle {
				if child, childDeclared := nodeMap[key]; childDeclared {
					stack = append(stack, frame{child, fr.ptr + "/" + floorEsc(key)})
				}
			}
			for _, key := range floorSchemaSubList {
				if children, isList := nodeMap[key].([]any); isList {
					for i, child := range children {
						stack = append(stack, frame{child, fr.ptr + "/" + floorEsc(key) + "/" + strconv.Itoa(i)})
					}
				}
			}
			for _, key := range floorSchemaSubMap {
				if children, isC := nodeMap[key].(map[string]any); isC {
					for _, name := range floorKeys(children) {
						stack = append(stack, frame{children[name], fr.ptr + "/" + floorEsc(key) + "/" + floorEsc(name)})
					}
				}
			}
		}
		sortFloorDefects(defs)
		sortFloorDefects(projs)
		return defs, projs
	}

	anyDestroyedOrInvalid := destroyedDeclared
	representedOrAddressed := 0

	for _, row := range rawOps {
		op := &floorOp{Ref: row.ref, Path: row.path, Method: row.method,
			InvalidAlternatives: map[string][]floorDefect{}, Projections: map[string][]floorDefect{}}
		var climbing []floorDefect
		addClimb := func(ds ...floorDefect) {
			for _, d := range ds {
				dup := false
				for _, e := range climbing {
					if e.Position == d.Position {
						dup = true
						break
					}
				}
				if !dup {
					climbing = append(climbing, d)
					if d.Class == floorURef {
						// A climbing defect always invalidates its operation
						// (see the disposition switch below), so this position
						// is confinable.
						f.ClimbingURefSites[d.Position] = true
					}
				}
			}
		}
		addProjection := func(unit string, ds ...floorDefect) {
			for _, d := range ds {
				dup := false
				for _, e := range op.Projections[unit] {
					if e.Position == d.Position {
						dup = true
						break
					}
				}
				if !dup {
					op.Projections[unit] = append(op.Projections[unit], d)
				}
			}
		}

		if row.pathKeyBad {
			if d, ok := isDefective("#/paths/" + floorEsc(row.path)); ok {
				addClimb(d) // P3: the Paths Object key
			}
		}
		opMap, opIsMap := row.value.(map[string]any)
		if !opIsMap {
			if d, ok := isDefective(op.Ref); ok {
				addClimb(d) // P3: the HTTP-method member
			}
			op.Disposition = "invalid"
			op.Defects = climbing
			sortFloorDefects(op.Defects)
			f.Ops[op.Ref] = op
			f.OpOrder = append(f.OpOrder, op.Ref)
			anyDestroyedOrInvalid = true
			continue
		}

		// P1 -- effective parameters (path-level plus operation-level).
		for _, holder := range []struct {
			list any
			ptr  string
		}{
			{row.pathItem["parameters"], "#/paths/" + floorEsc(row.path) + "/parameters"},
			{opMap["parameters"], op.Ref + "/parameters"},
		} {
			list, isList := holder.list.([]any)
			if !isList {
				continue
			}
			for i, p := range list {
				pptr := holder.ptr + "/" + strconv.Itoa(i)
				if d, ok := isDefective(pptr); ok {
					addClimb(d)
					continue
				}
				if isFloorRefObj(p) {
					if ref := refString(p); strings.HasPrefix(ref, "#") {
						if _, res := floorResolveInternal(root, ref); !res.resolved && !res.stoppedOnReference {
							addClimb(floorDefect{Class: floorURef, Position: pptr, Authority: floorAuthority(floorURef, line)})
						}
					}
					continue
				}
				pm, isPM := p.(map[string]any)
				if !isPM {
					continue
				}
				if schema, isS := pm["schema"].(map[string]any); isS {
					defs, projs := closureDefects(pptr+"/schema", schema)
					addClimb(defs...)
					addProjection(op.Ref, projs...)
				}
			}
		}

		// Request media alternatives -- units, never climbing.
		declaredAlternatives := 0
		rb, rbIsMap := opMap["requestBody"].(map[string]any)
		var rbContent map[string]any
		rbDeclaresContent := false
		if rbIsMap && !isFloorRefObj(rb) {
			rbContent, rbDeclaresContent = rb["content"].(map[string]any)
		}
		if rbDeclaresContent {
			for _, mediaKey := range floorKeys(rbContent) {
				aptr := op.Ref + "/requestBody/content/" + floorEsc(mediaKey)
				declaredAlternatives++
				if d, ok := isDefective(aptr); ok {
					op.InvalidAlternatives[aptr] = []floorDefect{d}
					op.AltOrder = append(op.AltOrder, aptr)
					continue
				}
				if mo, isMO := rbContent[mediaKey].(map[string]any); isMO {
					if schema, isS := mo["schema"].(map[string]any); isS {
						defs, projs := closureDefects(aptr+"/schema", schema)
						if len(defs) > 0 {
							op.InvalidAlternatives[aptr] = defs
							op.AltOrder = append(op.AltOrder, aptr)
							for _, d := range defs {
								if d.Class == floorURef {
									// The alternative is invalid and is never
									// emitted, so this position is confinable.
									f.ClimbingURefSites[d.Position] = true
								}
							}
						}
						addProjection(aptr, projs...)
					}
				}
			}
		}

		// P2 -- success response declarations that declared a body.
		responses, _ := opMap["responses"].(map[string]any)
		var codes []string
		for _, code := range floorKeys(responses) {
			if !strings.HasPrefix(code, "x-") {
				codes = append(codes, code)
			}
		}
		has2XX := false
		for _, code := range codes {
			if code == "2XX" {
				has2XX = true
			}
		}
		isSuccess := func(code string) bool {
			return floorSuccessCodeRE.MatchString(code) || code == "2XX" || (code == "default" && !has2XX)
		}
		for _, code := range codes {
			rptr := op.Ref + "/responses/" + floorEsc(code)
			rv := responses[code]
			respDefect, respDefective := isDefective(rptr)
			if !isSuccess(code) {
				// Non-success responses never climb; classification is the 2xx
				// rule (OAPI-P-08), so no success contract is lost.
				if respDefective {
					addProjection(op.Ref, respDefect)
				}
				continue
			}
			if isFloorRefObj(rv) {
				if ref := refString(rv); strings.HasPrefix(ref, "#") {
					if _, res := floorResolveInternal(root, ref); !res.resolved && !res.stoppedOnReference {
						// A dangling success-response reference declares no
						// readable body: an invalid declaration that loses no
						// representation.
						addProjection(op.Ref, floorDefect{Class: floorURef, Position: rptr, Authority: floorAuthority(floorURef, line)})
						continue
					}
				}
			}
			var declared []string
			var rvContent map[string]any
			if rvm, isRV := rv.(map[string]any); isRV && !isFloorRefObj(rv) {
				if content, isC := rvm["content"].(map[string]any); isC {
					rvContent = content
					declared = floorKeys(content)
				}
			}
			if respDefective && len(declared) == 0 {
				addProjection(op.Ref, respDefect)
				continue
			}
			if respDefective && len(declared) > 0 {
				addClimb(respDefect) // P2
				continue
			}
			if len(declared) == 0 {
				continue
			}
			surviving := 0
			var deadAlternativeDefects []floorDefect
			for _, mediaKey := range declared {
				aptr := rptr + "/content/" + floorEsc(mediaKey)
				if d, ok := isDefective(aptr); ok {
					deadAlternativeDefects = append(deadAlternativeDefects, d)
					continue
				}
				dead := false
				if mo, isMO := rvContent[mediaKey].(map[string]any); isMO {
					if schema, isS := mo["schema"].(map[string]any); isS {
						defs, projs := closureDefects(aptr+"/schema", schema)
						addProjection(op.Ref, projs...)
						if len(defs) > 0 {
							dead = true
							deadAlternativeDefects = append(deadAlternativeDefects, defs...)
						}
					}
				}
				if !dead {
					surviving++
				}
			}
			if surviving == 0 {
				addClimb(deadAlternativeDefects...) // P2: no surviving representable alternative
			} else if len(deadAlternativeDefects) > 0 {
				// Some response media alternatives are invalid but the response
				// survives: an at-position projection on the operation.
				addProjection(op.Ref, deadAlternativeDefects...)
			}
		}

		requiredBody := false
		if rbDeclaresContent {
			if req, isB := rb["required"].(bool); isB {
				requiredBody = req
			}
		}
		requestMediaExcluded := requiredBody && declaredAlternatives > 0 && declaredAlternatives == len(op.InvalidAlternatives)

		for unit, ds := range op.Projections {
			sortFloorDefects(ds)
			op.Projections[unit] = ds
		}
		for unit := range op.Projections {
			op.ProjOrder = append(op.ProjOrder, unit)
		}
		sort.Strings(op.ProjOrder)

		switch {
		case len(climbing) > 0:
			op.Disposition = "invalid"
			op.Defects = climbing
			sortFloorDefects(op.Defects)
			anyDestroyedOrInvalid = true
		case requestMediaExcluded:
			op.Disposition = "excluded-request-media"
			op.RequestMediaExcluded = true
			representedOrAddressed++
		default:
			op.Disposition = "represented"
			representedOrAddressed++
		}
		f.Ops[op.Ref] = op
		f.OpOrder = append(f.OpOrder, op.Ref)
	}

	// §3 part 2, the single derived rule: refuse only when no addressable
	// target remains -- zero operations survive as represented or
	// excluded-addressed AND at least one declared target was destroyed or
	// invalidated AND no Paths Object member is an external Reference Object
	// (externals defer the question to resolution).
	if representedOrAddressed == 0 && anyDestroyedOrInvalid && f.ExternalPathItemMembers == 0 {
		f.Refusal = floorPart2Refusal("every position that would have carried an operation object is defective under the governing edition")
	}
	return f
}

func floorPart2Refusal(detail string) string {
	return "whole-source refusal (OAPI-P-01, openbindings.openapi@1 §3 part 2): no addressable target remains: " + detail
}

// ---- small helpers ---------------------------------------------------------

func floorKeys(m map[string]any) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func floorEsc(token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	return strings.ReplaceAll(token, "/", "~1")
}

func isFloorRefObj(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, has := m["$ref"].(string)
	return has
}

func refString(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m["$ref"].(string)
	return s
}

// isFloorSchemaValued reports whether a value is spelled as a schema at all:
// an object, or a boolean. This asks only the JSON type the position declares,
// never whether the schema is otherwise well-formed.
//
// The 3.1 line admits the boolean schemas outright (2020-12). On the 3.0 line
// a boolean is NOT a Schema Object -- but that spelling is REFERRED rather
// than classified here, the same device D1n/D1a use: `openbindings.openapi@1`
// §9.2 already ascribes a part interpretation to a boolean-valued multipart
// part on the 3.0 line, and whether Schema-Object-hood or that part rule
// governs the position is a candidate-versus-authority question one level up.
// Zero corpus incidence either way.
func isFloorSchemaValued(v any) bool {
	if _, isObject := v.(map[string]any); isObject {
		return true
	}
	if _, isBool := v.(bool); isBool {
		return true
	}
	return false
}

// isFloorNumber reports whether a raw value carries a JSON number. The raw
// tree comes from a YAML decode, so an integral scalar may arrive under any of
// Go's integer kinds rather than as a float.
func isFloorNumber(v any) bool {
	switch v.(type) {
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	}
	return false
}

// A Response Object is REQUIRED to carry `description` in all eight accepted
// editions; its absence is a decidable proof that a value is not a Response
// Object, without a complete structural validator.
func isFloorResponseObject(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, has := m["description"].(string)
	return has
}

// floorResponseObjectFixedFields are the Response Object's fixed fields other
// than `description`. Their presence is decidable proof that a value IS a
// Response Object -- one that merely omitted its REQUIRED `description` -- and
// so is not a Schema Object: no accepted edition's Schema Object dialect
// declares any of them.
var floorResponseObjectFixedFields = []string{"content", "headers", "links"}

// floorSchemaKeywords is the union of the Schema Object vocabularies of the
// accepted editions -- the 3.0 line's JSON Schema subset plus its own
// `nullable`/`discriminator`/`xml`, and the 3.1 line's 2020-12 dialect. Shared
// vocabulary that both object kinds declare (`description`) is deliberately
// absent: it is not evidence of Schema-shape.
var floorSchemaKeywords = map[string]bool{
	"title": true, "multipleOf": true, "maximum": true, "exclusiveMaximum": true,
	"minimum": true, "exclusiveMinimum": true, "maxLength": true, "minLength": true,
	"pattern": true, "maxItems": true, "minItems": true, "uniqueItems": true,
	"maxProperties": true, "minProperties": true, "required": true, "enum": true,
	"type": true, "allOf": true, "oneOf": true, "anyOf": true, "not": true,
	"items": true, "properties": true, "additionalProperties": true,
	"format": true, "default": true, "nullable": true, "discriminator": true,
	"readOnly": true, "writeOnly": true, "xml": true, "externalDocs": true,
	"example": true, "deprecated": true,
	"$id": true, "$schema": true, "$anchor": true, "$defs": true, "$comment": true,
	"$dynamicRef": true, "$dynamicAnchor": true, "$vocabulary": true,
	"const": true, "contains": true, "maxContains": true, "minContains": true,
	"prefixItems": true, "if": true, "then": true, "else": true,
	"dependentSchemas": true, "dependentRequired": true, "patternProperties": true,
	"propertyNames": true, "unevaluatedItems": true, "unevaluatedProperties": true,
	"contentEncoding": true, "contentMediaType": true, "contentSchema": true,
	"examples": true,
}

// isFloorSchemaShaped reports whether a value can be read as a Schema Object,
// decidably and without a complete structural validator, as D6's class title
// ("Schema-shaped") requires. Two conjuncts: no Response Object fixed field is
// present (their presence disproves Schema-shape), and at least one Schema
// Object keyword IS present (positive evidence, rather than "it is an object").
func isFloorSchemaShaped(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	for _, fixed := range floorResponseObjectFixedFields {
		if _, has := m[fixed]; has {
			return false
		}
	}
	for key := range m {
		if floorSchemaKeywords[key] {
			return true
		}
	}
	return false
}

type floorResolution struct {
	resolved           bool
	stoppedOnReference bool
	malformedFragment  bool
}

// floorResolveInternal resolves a same-document reference over the raw tree.
// A walk that stops ON a node that is itself a Reference Object is reported
// distinctly and never emitted as dangling (the edition-split traversal
// question, deliberately not decided here).
func floorResolveInternal(root map[string]any, ref string) (any, floorResolution) {
	if !strings.HasPrefix(ref, "#") {
		return nil, floorResolution{}
	}
	frag := ref[1:]
	if frag == "" || frag == "/" {
		return root, floorResolution{resolved: true}
	}
	if !strings.HasPrefix(frag, "/") {
		return nil, floorResolution{malformedFragment: true}
	}
	var cur any = root
	for _, raw := range strings.Split(frag[1:], "/") {
		decoded := raw
		if strings.Contains(decoded, "%") {
			if unescaped, err := url.PathUnescape(decoded); err == nil {
				decoded = unescaped
			}
		}
		decoded = strings.ReplaceAll(decoded, "~1", "/")
		decoded = strings.ReplaceAll(decoded, "~0", "~")
		stopped := isFloorRefObj(cur)
		switch typed := cur.(type) {
		case map[string]any:
			next, ok := typed[decoded]
			if !ok {
				return nil, floorResolution{stoppedOnReference: stopped}
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(decoded)
			if err != nil || i < 0 || i >= len(typed) {
				return nil, floorResolution{stoppedOnReference: stopped}
			}
			cur = typed[i]
		default:
			return nil, floorResolution{stoppedOnReference: stopped}
		}
	}
	return cur, floorResolution{resolved: true}
}

func sortFloorDefects(ds []floorDefect) {
	sort.Slice(ds, func(i, j int) bool { return ds[i].Position < ds[j].Position })
}

// The entry messages, byte-identical in every engine.
func floorInvalidTargetMessage(n int) string {
	return fmt.Sprintf("invalid target: %d defective position(s) under the governing edition invalidate this operation", n)
}

func floorInvalidAlternativeMessage(n int) string {
	return fmt.Sprintf("invalid request media alternative: %d defective position(s) under the governing edition invalidate this alternative", n)
}

func floorProjectionMessage(n int) string {
	return fmt.Sprintf("%d invalid position(s) are reached by or recorded against this unit; the unit survives", n)
}

// floorDefectDetails renders defects for a coverage entry's details map.
func floorDefectDetails(ds []floorDefect) []any {
	out := make([]any, 0, len(ds))
	for _, d := range ds {
		out = append(out, map[string]any{"authority": d.Authority, "position": d.Position})
	}
	return out
}
