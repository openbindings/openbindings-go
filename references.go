package openbindings

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// Reference is one $ref or $dynamicRef keyword in the schemas a document
// contains (§7), with the schema its initial lookup identifies, or why it
// identifies none. Document.References lists them.
type Reference struct {
	// Location is where the keyword is: an RFC 6901 JSON Pointer from the
	// document root to the $ref or $dynamicRef member.
	Location string
	// Keyword is "$ref" or "$dynamicRef".
	Keyword string
	// Value is the keyword's value, as written.
	Value string
	// Base is the identifier of the schema resource the keyword lies in,
	// which Value resolves against (§7.2). It is "" in the document resource,
	// whose base the document names nowhere, and also in a resource whose $id
	// gives it no identifier, so an empty Base does not by itself place the
	// keyword in the document resource: Location says where the keyword
	// lies, and a schema at or above it that declares $id begins a resource
	// of its own.
	Base string
	// Target is where the schema the initial lookup identifies lies in the
	// document, as an RFC 6901 JSON Pointer from the document root; "" when
	// the lookup identifies no schema there.
	Target string
	// Unresolved says why Target is "", for reasons such as these: the
	// reference names a resource outside the document, a JSON Schema
	// meta-schema included; no schema lies where it points; it points inside
	// a schema resource from outside it; it names a plain name declared
	// nowhere or more than once; its fragment does not decode to UTF-8; it is
	// relative where its resource has no base to resolve it against; it is
	// not a well-formed URI-reference; it names an identifier more than one
	// resource carries in normal form, which this SDK's conservative policy
	// refuses; or what it names lies deeper than the 256 levels this SDK
	// indexes. The text is advisory. It is "" when Target is set.
	Unresolved string
}

// References lists every $ref and $dynamicRef keyword with a string value in
// the schemas the document contains (§7), sorted by Location, each with the
// schema its initial lookup identifies, or why it identifies none. Each is
// looked up as this SDK resolves references everywhere: a same-document
// reference in the document resource by OBI-D-12's own lookup (§7.2, §7.3),
// and any other by JSON Schema 2020-12 resolution among the document's own
// schema resources, whose identifiers are compared character for character
// (§7.4). A reference naming an identifier that more than one resource
// carries in normal form is refused by this SDK's conservative policy, even
// when one resource carries it character for character; §7.4 leaves that
// case to JSON Schema. It fetches nothing, evaluates nothing, and changes
// nothing. Like Validate, it reads the document the Document encodes.
//
// The schemas a document contains are those at its OBI positions (an
// operation's input or output, an entry of schemas) and every subschema the
// 2020-12 meta-schema validates as one, inside $id resources and the legacy
// definitions and dependencies included. A $ref-shaped member anywhere else
// (source or binding content, an example, an x- member) is data, not a
// reference, and is not listed; neither is a $ref or $dynamicRef whose value
// is not a string, which OBI-D-10 reports, and OBI-D-05 too in the document
// resource.
//
// What a caller may conclude, given a nil error: every reference keyword in
// the schemas the document contains, and where each one is; for each, the
// schema its initial lookup identifies, or why it identifies none; and which
// keywords are $dynamicRef, which may need dynamic-scope analysis.
//
// What a caller may not conclude: that a schema no reference targets is
// unused; that the schemas a closure of targets reaches are all an operation
// needs; or that a $dynamicRef lands on its Target when evaluated. A
// $dynamicRef whose fragment is a plain name its Target declares as a
// $dynamicAnchor looks the name up in the dynamic scope (JSON Schema Core
// §8.2.3.2), and may land on any schema declaring it there; References does
// not decide whether that happens, or where. A caller that removes, renames,
// or copies schemas needs its own policy for $dynamicRef: refusing whenever
// one could be involved is the conservative one.
//
// The whole call fails, listing no reference, for a document declaring a
// well-formed version outside the supported set (a *VersionRefusalError,
// OBI-T-04); for one declaring no valid version (OBI-D-09), or nesting
// deeper than encoding/json reads (10000 levels), this SDK's own limit (an
// error matching ErrInconclusive); and for one that cannot be encoded
// otherwise, a NaN, say (an error matching no category). Where a schema
// nests subschemas deeper than 256 levels, which this SDK does not index, it
// returns the references it found with an error matching ErrInconclusive:
// what is missing from them is not absent. A nil error means the list is
// complete, so an empty list with a nil error means the document holds no
// reference. A nil Document holds none.
func (d *Document) References() ([]Reference, error) {
	if d == nil {
		return nil, nil
	}
	if err := interpretable(d.OpenBindings); err != nil {
		return nil, err
	}
	view, err := documentView(*d)
	if err != nil {
		return nil, err
	}
	space := newSchemaSpace(view, nil)
	var refs []Reference
	for location, resource := range space.obi.schemas {
		object, isObject := mustResolve(view, location).(map[string]any)
		if !isObject {
			continue
		}
		for _, keyword := range []string{"$ref", "$dynamicRef"} {
			value, isString := object[keyword].(string)
			if !isString {
				continue
			}
			ref := Reference{Location: location + jsonpointer.Format(keyword), Keyword: keyword, Value: value}
			if !resource.document {
				ref.Base = resource.id
			}
			switch target, f := space.resolve(value, resource); {
			case f != nil:
				ref.Unresolved = f.reason
			case target.doc != space.obi:
				ref.Unresolved = fmt.Sprintf("%q names a schema in the JSON Schema meta-schema %s, outside the document", value, target.doc.uri)
			default:
				ref.Target = target.location
			}
			refs = append(refs, ref)
		}
	}
	slices.SortFunc(refs, func(a, b Reference) int { return strings.Compare(a.Location, b.Location) })
	var deep []string
	for _, location := range slices.Sorted(maps.Keys(space.obi.units)) {
		if space.obi.units[location].deep {
			deep = append(deep, location)
		}
	}
	if len(deep) > 0 {
		return refs, fmt.Errorf("%w: the schemas at %s nest subschemas deeper than %d levels, which this SDK does not index, so the references there are not all listed", ErrInconclusive, strings.Join(deep, ", "), schemaDepthLimit)
	}
	return refs, nil
}
