package openbindings

import (
	"cmp"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
	"github.com/openbindings/openbindings-go/internal/schemacompiler"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// openbindingsSchemaJSON is the OBI document schema (openbindings.schema.json),
// embedded at build time. Synced from the spec repo via scripts/sync-schema.sh.
//
//go:embed openbindings.schema.json
var openbindingsSchemaJSON []byte

// compiledOBISchema is the embedded OBI document schema, compiled once at
// init, for OBI-02 (the document validates against openbindings.schema.json).
var compiledOBISchema *jsonschema.Schema

// compiledMetaSchema is the JSON Schema 2020-12 meta-schema, compiled once at
// init from the validator library's locally embedded copy (never fetched from
// the network), for OBI-10 (every operation schema and schemas entry is
// valid against the 2020-12 meta-schemas).
var compiledMetaSchema *jsonschema.Schema

func init() {
	var doc any
	if err := json.Unmarshal(openbindingsSchemaJSON, &doc); err != nil {
		panic(fmt.Sprintf("openbindings: embedded openbindings.schema.json is not valid JSON: %v", err))
	}
	c := schemacompiler.New()
	if err := c.AddResource("openbindings:///schema", doc); err != nil {
		panic(fmt.Sprintf("openbindings: cannot register OBI schema: %v", err))
	}
	s, err := c.Compile("openbindings:///schema")
	if err != nil {
		panic(fmt.Sprintf("openbindings: cannot compile OBI schema: %v", err))
	}
	compiledOBISchema = s

	meta, err := schemacompiler.New().Compile(draft202012URI)
	if err != nil {
		panic(fmt.Sprintf("openbindings: cannot compile embedded 2020-12 meta-schema: %v", err))
	}
	compiledMetaSchema = meta
}

// validateSchemaWellFormedness records OBI-10 violations at one OBI schema
// position: the value must be a JSON Schema 2020-12 schema in object or
// boolean form, and the object form must validate against the 2020-12
// meta-schemas (which cover subschemas recursively), with format an
// annotation and the meta-schemas' patterns read as ECMA-262. The check is
// deliberately narrow, mirroring §5.2: unknown keywords, patterns an engine
// cannot compile, and unresolvable references all pass; they surface when the
// schema is used, not here. knownValid remembers schemas already
// found well-formed, by their encoding.
//
// The meta-schema validator's work grows faster than linearly with a
// schema's depth, so the subschemas a schema nests deeper than
// schemaDepthLimit are not checked, which leaves the rule inconclusive there
// (§10.4). The rest of the schema is checked all the same: the meta-schemas
// judge each subschema by what it holds, whatever its subschemas hold.
func validateSchemaWellFormedness(c *ruleChecks, prefix string, schema any, knownValid map[string]bool) {
	switch v := schema.(type) {
	case bool:
		// Boolean schemas are always well-formed.
	case map[string]any:
		key := metaSchemaCacheKey(v)
		if key != "" && knownValid[key] {
			return
		}
		checked, cut := cutSchema(v, schemaDepthLimit)
		problems, err := checkAgainstMetaSchema(checked)
		if err != nil {
			c.inconclusive("OBI-10", prefix, fmt.Sprintf("could not be checked against the 2020-12 meta-schemas: %v", err))
			return
		}
		for _, problem := range problems {
			c.violated("OBI-10", prefix+jsonpointer.Format(problem.Location...), "not a well-formed JSON Schema 2020-12 schema: "+problem.Message)
		}
		switch {
		case cut != "":
			c.inconclusive("OBI-10", prefix+cut, fmt.Sprintf("this subschema, and any other nested deeper than %d levels, was not checked against the 2020-12 meta-schemas", schemaDepthLimit))
		case len(problems) == 0 && key != "":
			knownValid[key] = true
		}
	default:
		c.violated("OBI-10", prefix, fmt.Sprintf("a schema is a JSON Schema 2020-12 object or boolean; got %s", jsonTypeName(v)))
	}
}

// checkAgainstMetaSchema validates a schema against the 2020-12 meta-schemas,
// returning the problems found, or an error when no verdict was reached. A
// number beyond the numeric limits of schema evaluation is checked as a
// stand-in (schemacompiler.Substitute): the meta-schemas tell numbers apart
// only by type, by equality, and by comparison with zero
// (TestMetaSchema_ComparesNumbersOnlyWithZero).
func checkAgainstMetaSchema(schema any) ([]schemacompiler.Problem, error) {
	checked := schemacompiler.Substitute(schema)
	err := compiledMetaSchema.Validate(checked.Value)
	if err == nil {
		return nil, nil
	}
	problems, mismatch := checked.Outcome(err)
	if !mismatch {
		return nil, err
	}
	return problems, nil
}

// cutSchema returns a schema with each subschema object it nests deeper than
// limit levels replaced by true, and the location of the first replaced, or
// the schema itself and "" when it nests none so deep. schema is not changed.
func cutSchema(schema map[string]any, limit int) (map[string]any, string) {
	if schemaDepth(schema) <= limit {
		return schema, ""
	}
	first := ""
	var path []string
	var cut func(object map[string]any, level int) map[string]any
	cut = func(object map[string]any, level int) map[string]any {
		out := maps.Clone(object)
		copied := map[string]bool{}
		forEachDescribedSubschema(object, func(child any, tokens ...string) {
			path = append(path, tokens...)
			defer func() { path = path[:len(path)-len(tokens)] }()
			childObject, isObject := child.(map[string]any)
			if !isObject {
				return
			}
			var replaced any = true
			if level < limit {
				replaced = cut(childObject, level+1)
			} else if first == "" {
				first = jsonpointer.Format(path...)
			}
			keyword := tokens[0]
			if len(tokens) == 1 {
				out[keyword] = replaced
				return
			}
			switch container := out[keyword].(type) {
			case map[string]any:
				if !copied[keyword] {
					container, copied[keyword] = maps.Clone(container), true
					out[keyword] = container
				}
				container[tokens[1]] = replaced
			case []any:
				if !copied[keyword] {
					container, copied[keyword] = slices.Clone(container), true
					out[keyword] = container
				}
				index, _ := strconv.Atoi(tokens[1])
				container[index] = replaced
			}
		})
		return out
	}
	return cut(schema, 0), first
}

// validateAgainstOBISchema records OBI-02 evidence: whether the document's
// generic view validates against openbindings.schema.json.
//
// The document schema tells numbers apart only by type and equality, except
// that it holds a binding's preference to its integer range (§5.3;
// TestDocumentSchema_ComparesNumbersOnlyAtAPreference). So the schema library
// is never handed a number beyond the numeric limits of schema evaluation: a
// preference holding one is decided exactly here, and any other is checked
// as a stand-in (schemacompiler.Substitute).
//
// Its violations are recorded in the order of where the failing keyword
// applies, by reference token (the object, for a member the schema does not
// allow; the member, for a member name it refuses; the value otherwise), then
// of their messages. The schema library finds them in the order it walks the
// document's objects, which is not fixed, and locates a refused member name
// by a location a later sibling can overwrite, so each is located here
// first.
func validateAgainstOBISchema(c *ruleChecks, view any) {
	var found []schemaFinding
	var decided [][]string
	for _, member := range membersAt(view, []string{"bindings", "*", "preference"}, nil) {
		number, isNumber := member.value.(json.Number)
		if _, err := schemacompiler.NumericLimit(number); !isNumber || err == nil {
			continue
		}
		if _, inRange := preferenceValue(string(number)); !inRange {
			found = append(found, schemaFinding{member.tokens, member.tokens, fmt.Sprintf("a preference is an integer from -%d through %d", maxPreference, maxPreference)})
		}
		decided = append(decided, member.tokens)
	}
	checked := schemacompiler.Substitute(withoutMembers(view, decided))
	verr := compiledOBISchema.Validate(checked.Value)
	var problems []schemacompiler.Problem
	if verr != nil {
		var mismatch bool
		if problems, mismatch = checked.Outcome(verr); !mismatch {
			recordSchemaFindings(c, found)
			// An exceeded resource limit is not evidence of violation (§10.4).
			c.inconclusive("OBI-02", "", fmt.Sprintf("could not be checked against the document schema: %v", verr))
			return
		}
	}
	located := map[string]bool{}
	var names map[string][][]string
	for _, problem := range problems {
		switch {
		case len(problem.Members) > 0:
			// A member the document schema does not allow is located at the
			// member, each one a finding of its own.
			for _, name := range problem.Members {
				found = append(found, schemaFinding{problem.Location, append(slices.Clone(problem.Location), name), fmt.Sprintf("additional property %q not allowed", name)})
			}
		case problem.Name == nil:
			found = append(found, schemaFinding{problem.Location, problem.Location, problem.Message})
		case !located[*problem.Name]:
			// A member name the schema refuses is located where the document
			// holds it, once per such member however often it is reported.
			// namedMaps holds every map whose names the schema constrains,
			// and all refuse the same names, so a name refused in one is
			// refused in every map holding it
			// (TestDocumentSchema_NamedMapsAreEveryConstrainedMap); a name
			// found in none is still a violation, of the whole document.
			located[*problem.Name] = true
			if names == nil {
				names = memberNames(view)
			}
			at := names[*problem.Name]
			if len(at) == 0 {
				at = [][]string{nil}
			}
			for _, tokens := range at {
				found = append(found, schemaFinding{tokens, tokens, problem.Message})
			}
		}
	}
	recordSchemaFindings(c, found)
}

// schemaFinding is an OBI-02 violation: where the failing keyword applies
// and where the violation is, as reference tokens, and what the document
// schema refused there.
type schemaFinding struct {
	applies, at []string
	message     string
}

// recordSchemaFindings records OBI-02 violations in the order of where the
// failing keyword applies, by reference token, then of their messages.
func recordSchemaFindings(c *ruleChecks, found []schemaFinding) {
	slices.SortStableFunc(found, func(a, b schemaFinding) int {
		return cmp.Or(slices.Compare(a.applies, b.applies), strings.Compare(a.message, b.message))
	})
	for _, f := range found {
		c.violated("OBI-02", jsonpointer.Format(f.at...), "does not validate against the document schema: "+f.message)
	}
}

// namedMaps are the maps whose member names the document schema constrains,
// with a pattern equal to OBI-04's: the keys of the top-level maps and of
// an operation's examples.
var namedMaps = [][]string{{"schemas"}, {"operations"}, {"dependencies"}, {"sources"}, {"bindings"}, {"operations", "*", "examples"}}

// memberNames returns, for each name a member has in one of namedMaps, where
// the document holds such a member, as reference tokens: in the order of
// namedMaps, then in sorted order within each. One pass serves every refused
// name, so locating them stays linear in the document.
func memberNames(view any) map[string][][]string {
	out := map[string][][]string{}
	for _, at := range namedMaps {
		for _, m := range membersAt(view, append(slices.Clone(at), "*"), nil) {
			name := m.tokens[len(m.tokens)-1]
			out[name] = append(out[name], m.tokens)
		}
	}
	return out
}

// member is a member of a document's generic view and where it is.
type member struct {
	tokens []string
	value  any
}

// membersAt returns the members of view that pattern names, where "*" is
// every entry of an object, in sorted order. at is the reference tokens of
// view itself.
func membersAt(view any, pattern, at []string) []member {
	if len(pattern) == 0 {
		return []member{{tokens: at, value: view}}
	}
	object, _ := view.(map[string]any)
	names := []string{pattern[0]}
	if pattern[0] == "*" {
		names = sortedKeys(object)
	}
	var out []member
	for _, name := range names {
		if value, present := object[name]; present {
			out = append(out, membersAt(value, pattern[1:], append(slices.Clip(at), name))...)
		}
	}
	return out
}

// withoutMembers returns view without the members at each of paths, copying
// each object on the way to them once, so view itself is not changed and the
// work is linear in the objects copied.
func withoutMembers(view any, paths [][]string) any {
	if len(paths) == 0 {
		return view
	}
	object, _ := view.(map[string]any)
	removed := map[string]bool{}
	within := map[string][][]string{}
	for _, tokens := range paths {
		if len(tokens) == 1 {
			removed[tokens[0]] = true
		} else {
			within[tokens[0]] = append(within[tokens[0]], tokens[1:])
		}
	}
	copied := make(map[string]any, len(object))
	for name, value := range object {
		switch {
		case removed[name]:
		case within[name] != nil:
			copied[name] = withoutMembers(value, within[name])
		default:
			copied[name] = value
		}
	}
	return copied
}

func metaSchemaCacheKey(schema map[string]any) string {
	data, err := json.Marshal(schema)
	if err != nil {
		return ""
	}
	return string(data)
}
