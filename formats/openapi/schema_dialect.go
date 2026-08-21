package openapi

// The acceptance floor's Schema Object dialect verdict (the D15 class): does a
// Schema Object's own keyword value carry the JSON type the governing edition
// line's Schema Object dialect declares for it?
//
// There are two ways to answer that from an authority. One POINTS at the
// authority's own artifact so its specificity does the discriminating; the
// other COPIES the artifact's contents into our code, which forks it and makes
// us the maintainer of a drifting duplicate. A fork fails in both directions --
// it misses real cases and can invent false ones -- and it wears the
// authority's voice while quietly taking the authority's decision away from it.
//
// This file answers per line, because the two lines have different Schema
// Object dialects and only one of them publishes a machine-consultable
// artifact:
//
//   - The 3.1 line DELEGATES. Absent `jsonSchemaDialect` (and every accepted
//     3.1 edition says so in the same words) "the OAS dialect schema id MUST be
//     used for these Schema Objects", and that dialect --
//     `https://spec.openapis.org/oas/3.1/dialect/base` -- is a published JSON
//     Schema. It is vendored here verbatim, with the OAS base vocabulary
//     meta-schema it composes; the 2020-12 meta-schemas its other `allOf`
//     branch names come from the validator's own embedded copies. Nothing about
//     which keywords exist, or what types they take, is stated in this file.
//     That is what makes the ROOT 2020-12 meta-schema's deliberate legacy
//     guards reachable: it re-declares `$recursiveAnchor` as an anchor STRING
//     ("to prevent incompatible extensions as they remain in common use"), so
//     `$recursiveAnchor: true` fails while `$recursiveAnchor: "foo"` and
//     `$recursiveRef: "#"` pass, and `required`'s `uniqueItems` fails a
//     duplicated entry. No enumeration in this package could have known either.
//
//   - The 3.0 line TRANSCRIBES, because there is nothing to point at. OAS
//     3.0.x's Schema Object is "an extended subset of the JSON Schema
//     Specification Draft Wright-00", and that draft published no meta-schema:
//     `https://json-schema.org/draft-05/schema` is 404. The OpenAPI Initiative
//     does publish a convenience JSON Schema for whole 3.0 documents, but its
//     `Schema` definition is `additionalProperties: false` over an enumerated
//     keyword list, a closure the OAS prose never states -- adopting it would
//     refuse what the edition admits. So the 3.0 cells below are a labeled
//     transcription of the edition's own sentences, and they carry the guard a
//     transcription owes: `TestOAS30SchemaObjectCellsAreGrounded` pins every
//     cell to the sentence it restates, and the shared case table exercises
//     both lines through the shipped path in every engine.
//
// Positions are reported at the granularity the floor's own walk uses: a
// keyword of the node, or a member of a keyword whose value is a map or list of
// schemas. Nothing below the schema position is reported, so two engines using
// different validators name the same position.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The OAS 3.1 Schema Object dialect, vendored verbatim from the authority and
// never fetched. Both files are pinned by digest in
// TestVendoredAuthorityArtifactDigests.
//
//go:embed authority/oas-3.1-dialect-base.json
var oas31DialectBaseJSON []byte

//go:embed authority/oas-3.1-meta-base.json
var oas31MetaBaseJSON []byte

const (
	oas31DialectURI  = "https://spec.openapis.org/oas/3.1/dialect/base"
	oas31MetaBaseURI = "https://spec.openapis.org/oas/3.1/meta/base"
)

var (
	oas31DialectOnce     sync.Once
	oas31DialectCompiled *jsonschema.Schema
	oas31DialectErr      error
)

// oas31Dialect compiles the vendored dialect once. Its remaining `allOf`
// branch, `https://json-schema.org/draft/2020-12/schema`, resolves from the
// validator library's own embedded meta-schemas -- the same copies the OBI
// well-formedness rule this floor must not let anything escape to is decided
// against.
func oas31Dialect() (*jsonschema.Schema, error) {
	oas31DialectOnce.Do(func() {
		var dialect, metaBase any
		if err := json.Unmarshal(oas31DialectBaseJSON, &dialect); err != nil {
			oas31DialectErr = fmt.Errorf("vendored OAS 3.1 dialect is not valid JSON: %w", err)
			return
		}
		if err := json.Unmarshal(oas31MetaBaseJSON, &metaBase); err != nil {
			oas31DialectErr = fmt.Errorf("vendored OAS 3.1 base vocabulary is not valid JSON: %w", err)
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource(oas31DialectURI, dialect); err != nil {
			oas31DialectErr = err
			return
		}
		if err := c.AddResource(oas31MetaBaseURI, metaBase); err != nil {
			oas31DialectErr = err
			return
		}
		oas31DialectCompiled, oas31DialectErr = c.Compile(oas31DialectURI)
	})
	return oas31DialectCompiled, oas31DialectErr
}

// floorSchemaObjectDefects returns the positions, relative to one Schema Object
// node, whose values are defective under the governing line's Schema Object
// dialect. An empty string in the result names the node itself.
func floorSchemaObjectDefects(node map[string]any, line string) []string {
	if line == "3.1" {
		return oas31DialectDefects(node)
	}
	return oas30SchemaObjectDefects(node)
}

// oas31DialectDefects asks the vendored dialect. The node is reduced to its own
// SHAPE first -- every subschema at a position this floor walks on its own is
// replaced by `true`, which is well-formed under every vocabulary -- so the
// answer is exactly this node's contribution and each node is decided once. A
// value that is not in schema form is left exactly where it sits, so a
// malformed `items: [ ... ]` or `properties: {"a": 3}` still fails at the node
// that declares it. Positions the floor does not walk (`contentSchema`) are
// likewise left in place and decided where they sit.
func oas31DialectDefects(node map[string]any) []string {
	dialect, err := oas31Dialect()
	if err != nil || dialect == nil {
		return nil
	}
	failure := dialect.Validate(any(floorSchemaNodeShape(node)))
	if failure == nil {
		return nil
	}
	verr, isValidation := failure.(*jsonschema.ValidationError)
	if !isValidation {
		return nil
	}
	seen := map[string]bool{}
	var positions []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			position := floorSchemaPosition(e.InstanceLocation)
			if !seen[position] {
				seen[position] = true
				positions = append(positions, position)
			}
			return
		}
		for _, cause := range e.Causes {
			walk(cause)
		}
	}
	walk(verr)
	sort.Strings(positions)
	return positions
}

// oas30SchemaObjectDefects is the 3.0 line's TRANSCRIPTION, one cell per
// sentence the edition states. Each cell's grounding sentence is pinned by
// TestOAS30SchemaObjectCellsAreGrounded; adding a cell without one is the
// drift this file exists to prevent.
//
//	`required`  -- OAS 3.0.x Section 4.7.24.1 lists `required` among the
//	               keywords "taken directly from the JSON Schema definition
//	               and follow the same specifications", and that definition
//	               (draft-wright-json-schema-validation-00 Section 6.17) is
//	               "an array. Elements of this array, if any, MUST be strings,
//	               and MUST be unique."
//	`enum`      -- the same "taken directly" list; the JSON Schema definition
//	               (Section 6.23) is "an array".
//	`items`     -- OAS 3.0.x Section 4.7.24: "items - Value MUST be an object
//	               and not an array."
//	`properties`-- OAS 3.0.x Section 4.7.24: "properties - Property definitions
//	               MUST be a Schema Object and not a standard JSON Schema",
//	               and a Schema Object on this line is an object (the boolean
//	               schema literals are not in this dialect).
//
// Deliberately NOT transcribed, and why: the `type` keyword is the D1/D1s
// class with its own citation; `exclusiveMinimum`/`exclusiveMaximum` are
// BOOLEAN on this line, which is why the 3.1 delegation is scoped to the 3.1
// line; and keywords this line declares "strictly unsupported" decide as if
// absent per this specification's stated reading, so their values are not
// judged here.
func oas30SchemaObjectDefects(node map[string]any) []string {
	var positions []string
	if value, declared := node["required"]; declared {
		list, isList := value.([]any)
		switch {
		case !isList:
			positions = append(positions, "/required")
		case floorHasDuplicateMembers(list):
			positions = append(positions, "/required")
		}
	}
	if value, declared := node["enum"]; declared {
		if _, isList := value.([]any); !isList {
			positions = append(positions, "/enum")
		}
	}
	if _, isList := node["items"].([]any); isList {
		positions = append(positions, "/items")
	}
	if properties, isMap := node["properties"].(map[string]any); isMap {
		for _, key := range floorKeys(properties) {
			if isFloorSchemaValued(properties[key], "3.0") {
				continue
			}
			positions = append(positions, "/properties/"+floorEsc(key))
		}
	}
	sort.Strings(positions)
	return positions
}

// floorHasDuplicateMembers reports JSON-value equality between any two members,
// the comparison `uniqueItems` names.
func floorHasDuplicateMembers(list []any) bool {
	seen := make(map[string]bool, len(list))
	for _, member := range list {
		encoded, err := json.Marshal(member)
		if err != nil {
			continue
		}
		if seen[string(encoded)] {
			return true
		}
		seen[string(encoded)] = true
	}
	return false
}

// floorSchemaPosition renders a validator's instance location at the floor's
// own position granularity: one keyword of the node, plus the member name when
// that keyword's value is a map or list OF SCHEMAS. Everything deeper is inside
// one schema position and names no separate position, which is what keeps two
// engines running different validators reporting identical strings (one reports
// a `uniqueItems` failure at `/required`, the other at `/required/2`).
func floorSchemaPosition(instanceLocation []string) string {
	if len(instanceLocation) == 0 {
		return ""
	}
	keep := 1
	if len(instanceLocation) > 1 && (floorSchemaSubMapSet[instanceLocation[0]] || floorSchemaSubListSet[instanceLocation[0]]) {
		keep = 2
	}
	var out strings.Builder
	for _, token := range instanceLocation[:keep] {
		out.WriteString("/")
		out.WriteString(floorEsc(token))
	}
	return out.String()
}

// floorSchemaNodeShape replaces every subschema at a position this floor walks
// on its own with `true`. `true` is a well-formed schema under every
// vocabulary, so the shape's verdict is exactly the node's own contribution and
// the replaced subschemas are decided when the walk reaches them.
func floorSchemaNodeShape(node map[string]any) map[string]any {
	shape := make(map[string]any, len(node))
	for keyword, value := range node {
		switch {
		case floorSchemaSubSingleSet[keyword] && floorIsSchemaShaped(value):
			shape[keyword] = true
		case floorSchemaSubListSet[keyword]:
			list, isList := value.([]any)
			if !isList {
				shape[keyword] = value
				continue
			}
			members := make([]any, len(list))
			for i, member := range list {
				if floorIsSchemaShaped(member) {
					members[i] = true
				} else {
					members[i] = member
				}
			}
			shape[keyword] = members
		case floorSchemaSubMapSet[keyword]:
			entries, isMap := value.(map[string]any)
			if !isMap {
				shape[keyword] = value
				continue
			}
			members := make(map[string]any, len(entries))
			for name, member := range entries {
				if floorIsSchemaShaped(member) {
					members[name] = true
				} else {
					members[name] = member
				}
			}
			shape[keyword] = members
		default:
			shape[keyword] = value
		}
	}
	return shape
}

// floorIsSchemaShaped reports the two forms a schema position admits at all.
// Only these are lifted out of a node's shape, because only these are positions
// the floor's own walk visits and decides.
func floorIsSchemaShaped(value any) bool {
	switch value.(type) {
	case map[string]any, bool:
		return true
	}
	return false
}

var (
	floorSchemaSubSingleSet = floorKeywordSet(floorSchemaSubSingle)
	floorSchemaSubListSet   = floorKeywordSet(floorSchemaSubList)
	floorSchemaSubMapSet    = floorKeywordSet(floorSchemaSubMap)
)

func floorKeywordSet(keywords []string) map[string]bool {
	set := make(map[string]bool, len(keywords))
	for _, keyword := range keywords {
		set[keyword] = true
	}
	return set
}
