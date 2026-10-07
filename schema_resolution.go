package openbindings

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// sameDocumentTarget is what a same-document reference in the document
// resource identifies, looked up as OBI-12 looks it up (§7.3). One lookup
// serves OBI-12 and the value contracts that resolve such a reference
// (§7.2), so they cannot disagree.
type sameDocumentTarget struct {
	// location is the schema the reference identifies, as a JSON Pointer
	// from the document root.
	location string
	// name is the plain name the reference used, or "".
	name string
	// why says why the reference identifies no schema; "" when it
	// identifies one.
	why string
	// declaredTwice marks a plain name the document resource declares more
	// than once: JSON Schema leaves what it identifies undefined (Core
	// §8.2.2), and OBI-13, not OBI-12, reports the declarations.
	declaredTwice bool
}

// lookUpSameDocument looks up a same-document reference in the document
// resource, which has no URI anything can name (§7.2): the empty reference
// and an empty fragment name the OBI document itself; any other fragment is
// percent-decoded once, and then one beginning with / is a JSON Pointer from
// the document root (lookUpPointer), and any other a plain name the document
// resource declares. A fragment that does not decode is read as written, and
// one that decodes to a string that is not valid UTF-8 names nothing.
func (d documentSchemas) lookUpSameDocument(ref string, view any) sameDocumentTarget {
	fragment := strings.TrimPrefix(ref, "#")
	if decoded, err := url.PathUnescape(fragment); err == nil {
		fragment = decoded
	}
	switch {
	case fragment == "":
		return sameDocumentTarget{why: "names the OBI document itself, which is not a schema"}
	case strings.HasPrefix(fragment, "/"):
		return lookUpPointer(fragment, view)
	case !utf8.ValidString(fragment):
		return sameDocumentTarget{why: "does not decode to valid UTF-8, so it names nothing"}
	}
	switch found := d.anchors[fragment]; len(found) {
	case 0:
		return sameDocumentTarget{why: "names a plain name no schema in the document resource declares"}
	case 1:
		return sameDocumentTarget{location: found[0].at.from(nil), name: fragment}
	}
	return sameDocumentTarget{why: "names a plain name the document resource declares more than once", declaredTwice: true}
}

// lookUpPointer follows a JSON Pointer from the document root as OBI-12
// does: it identifies a schema at an OBI position, and never a location
// inside a schema that declares $id, whose contents a reference reaches
// through that $id. The pointer is followed step by step through the
// positions it passes, so a lookup takes time linear in the pointer.
func lookUpPointer(pointer string, view any) sameDocumentTarget {
	tokens, ok := jsonpointer.Parse(pointer)
	if !ok {
		return sameDocumentTarget{why: "is not a JSON Pointer"}
	}
	location := jsonpointer.Format(tokens...)
	target, found := jsonpointer.Resolve(view, location)
	if !found {
		return sameDocumentTarget{why: "does not resolve within the document"}
	}
	var rest []string
	positioned := false
	switch {
	case len(tokens) >= 2 && tokens[0] == "schemas":
		positioned, rest = true, tokens[2:]
	case len(tokens) >= 3 && tokens[0] == "operations" && (tokens[2] == "input" || tokens[2] == "output"):
		positioned, rest = true, tokens[3:]
	}
	passed := len(tokens) - len(rest)
	node, _ := jsonpointer.Resolve(view, jsonpointer.Format(tokens[:passed]...))
	// Each step is read in what the document holds, so a keyword holding a
	// map or an array of schemas leads to an entry only when its value is an
	// object or an array: allOf holding an object has no entries.
	for positioned && len(rest) > 0 {
		schema, isObject := node.(map[string]any)
		if !isObject {
			positioned = false
			break
		}
		if _, declares := schema["$id"]; declares {
			return sameDocumentTarget{why: fmt.Sprintf("resolves into the schema resource declared at %s, whose contents a reference reaches through its $id", jsonpointer.Format(tokens[:passed]...))}
		}
		keyword, value := rest[0], schema[rest[0]]
		step := 2
		switch {
		case singleSchemaKeywords[keyword]:
			node, step = value, 1
		case len(rest) < 2:
			positioned = false
		case schemaMapKeywords[keyword], describedMapKeywords[keyword]:
			entries, isMap := value.(map[string]any)
			node, positioned = entries[rest[1]], isMap
		case arraySchemaKeywords[keyword]:
			_, isArray := value.([]any)
			node, _ = jsonpointer.Resolve(value, jsonpointer.Format(rest[1]))
			positioned = isArray
		default:
			positioned = false
		}
		if positioned {
			rest, passed = rest[step:], passed+step
		}
	}
	switch target.(type) {
	case map[string]any, bool:
		if positioned {
			return sameDocumentTarget{location: location}
		}
	}
	return sameDocumentTarget{why: fmt.Sprintf("resolves to %s, which is not a schema at an OBI position", location)}
}
