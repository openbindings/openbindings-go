package openbindings

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// reference is what a schema reference ($ref, or $dynamicRef's static
// target) names, resolved as §7 and JSON Schema 2020-12 resolve it (OBI-T-06).
// One resolution serves the reference rule (OBI-D-12), the reach of an
// operation's schema (OBI-T-08), and the bundle the schema library is given,
// so they cannot disagree.
type reference struct {
	origin origin
	// location is where in the document the reference points, when origin is
	// inDocument, as a JSON Pointer from the document root.
	location string
	// uri is the resource outside the document, the meta-schema, or the URI
	// more than one resource declares.
	uri string
	// exists is whether the location named exists, which is what OBI-D-12
	// asks of a reference the document resolves.
	exists existence
	// within is the resource an absolute reference named, or the resource a
	// fragment resolved within; nil for the document resource and for a
	// reference outside the document.
	within *schemaResource
	// why states what is wrong with a reference that names no one location.
	why string
}

// origin is where a reference points.
type origin int

const (
	inDocument   origin = iota // a location in the document
	inMetaSchema               // a JSON Schema meta-schema the library carries
	outside                    // a resource the document does not embed
	ambiguous                  // a URI or anchor more than one schema declares
	unresolved                 // nothing: no base, no such anchor, not a URI
)

type existence int

const (
	exists existence = iota
	missing
	undecided
)

// resolve resolves a reference held by the schema at location holder in the
// document view.
//
// The base is the resource holding the reference. In the document resource,
// which has no URI anything can name, a same-document reference is looked up
// as OBI-D-12 looks it up (§7.2): the empty reference and an empty fragment
// name the OBI document itself; any other fragment is percent-decoded once,
// and then one beginning with / is a JSON Pointer from the document root,
// which reaches no location inside a schema with an $id member, and any other
// a plain name the document resource declares. A relative reference there has
// no base. Inside a resource, a fragment resolves within
// it, and any other reference against its URI; an absolute URI names the
// embedded resource declaring it, a carried meta-schema, or a resource
// outside the document. A reference into a meta-schema's interior is not
// followed: this SDK does not analyze the meta-schemas the library carries.
// A reference that is not a URI-reference (RFC 3986 §4.1) resolves to
// nothing: JSON Schema requires one (Core §8.2.3.1) and leaves any other
// undefined.
func (d documentSchemas) resolve(ref, holder string, view any) reference {
	return d.resolveFrom(ref, d.resourceAt(holder), view)
}

// resolveFrom resolves a reference held in a resource, or in the document
// resource when resource is nil, as resolve does.
func (d documentSchemas) resolveFrom(ref string, resource *schemaResource, view any) reference {
	if wellFormed, _ := uriReference(ref); !wellFormed {
		return reference{origin: unresolved, exists: undecided, why: "is not a URI-reference (RFC 3986 §4.1), which JSON Schema leaves undefined"}
	}
	if ref == "" {
		return d.resolveFragment(resource, "", view)
	}
	parsed, err := url.Parse(ref)
	if strings.HasPrefix(ref, "#") {
		// A fragment is read after percent-decoding it once; one that does
		// not decode is read as written.
		fragment := ref[1:]
		if err == nil {
			fragment = parsed.Fragment
		}
		return d.resolveFragment(resource, fragment, view)
	}
	if err != nil {
		return reference{origin: unresolved, exists: undecided, why: "is not a URI reference"}
	}
	var base *url.URL
	if resource != nil {
		base = resource.uri
	}
	if !parsed.IsAbs() && base == nil {
		return reference{origin: unresolved, exists: undecided, why: "is relative, with no base to resolve against"}
	}
	target, resolved := resolveURI(base, parsed)
	if !resolved {
		return reference{origin: unresolved, exists: undecided, why: "does not resolve to a URL"}
	}
	fragment := target.Fragment
	target.Fragment, target.RawFragment = "", ""
	id := target.String()
	// The schema library resolves references with net/url, which departs from
	// RFC 3986 for some URIs (a base with no authority, an opaque path with
	// dot segments, a path beginning //). Where it does, the library would
	// evaluate another schema than the one RFC 3986 names.
	libraryBase := bundleURI
	if resource != nil && resource.libraryURI != "" {
		libraryBase = resource.libraryURI
	}
	if library, joined := libraryJoin(libraryBase, ref); !joined || library != id {
		return reference{origin: unresolved, exists: undecided, why: fmt.Sprintf("resolves to %s by RFC 3986 and to %q in the schema library", id, library)}
	}
	if why, isAmbiguous := d.ambiguous[id]; isAmbiguous {
		// The reference names no one schema. OBI-D-12 never asks whether it
		// exists: it judges only same-document references in the document
		// resource, which name no URI.
		return reference{origin: ambiguous, uri: id, exists: undecided, why: "names no one embedded schema: " + why}
	}
	if embedded, isEmbedded := d.resources[id]; isEmbedded {
		return d.resolveFragment(embedded, fragment, view)
	}
	if isBuiltInMetaSchema(id) {
		if fragment != "" {
			return reference{origin: unresolved, exists: undecided, why: fmt.Sprintf("reaches inside the meta-schema %s, whose contents this SDK does not analyze", id)}
		}
		return reference{origin: inMetaSchema, uri: id, exists: undecided}
	}
	return reference{origin: outside, uri: id, exists: undecided}
}

// resolveFragment resolves a decoded fragment within a resource, or within the
// document resource when resource is nil: the resource itself (the OBI
// document, for the document resource), a JSON Pointer from it, or a plain
// name it declares. A fragment that is not valid UTF-8 names nothing
// (OBI-D-12).
func (d documentSchemas) resolveFragment(resource *schemaResource, fragment string, view any) reference {
	root := ""
	if resource != nil {
		root = resource.location
	}
	if fragment == "" || strings.HasPrefix(fragment, "/") {
		tokens, ok := jsonpointer.Parse(fragment)
		if !ok {
			return reference{origin: unresolved, exists: missing, within: resource, why: "is not a JSON Pointer"}
		}
		location := root + jsonpointer.Format(tokens...)
		if _, ok := jsonpointer.Resolve(view, location); !ok {
			return reference{origin: unresolved, exists: missing, within: resource, why: "does not resolve within " + describeBase(resource)}
		}
		if end := strings.LastIndexByte(location, '/'); resource == nil && end >= 0 {
			// What a schema with an $id member encloses is reached through
			// that $id (§7.2).
			if enclosing := d.resourceAt(location[:end]); enclosing != nil {
				return reference{origin: unresolved, exists: missing, why: fmt.Sprintf("resolves into the schema resource declared at %s, whose contents a reference reaches through its $id", enclosing.location)}
			}
		}
		return reference{origin: inDocument, location: location, exists: exists, within: resource}
	}
	if resource == nil {
		switch found := d.anchors[fragment]; {
		case !utf8.ValidString(fragment):
			return reference{origin: unresolved, exists: missing, why: "does not decode to valid UTF-8, so it names nothing"}
		case len(found) == 0:
			return reference{origin: unresolved, exists: missing, why: "names a plain name no schema in the document resource declares"}
		case len(found) == 1:
			return reference{origin: inDocument, location: found[0].at.from(nil), exists: exists}
		default:
			return reference{origin: ambiguous, exists: undecided, why: "names a plain name the document resource declares more than once"}
		}
	}
	switch found := resource.anchors[fragment]; len(found) {
	case 0:
		return reference{origin: unresolved, exists: missing, within: resource, why: fmt.Sprintf("names an anchor %s does not declare", resourceName(resource))}
	case 1:
		return reference{origin: inDocument, location: found[0].from(nil), exists: exists, within: resource}
	default:
		return reference{origin: ambiguous, exists: undecided, within: resource, why: fmt.Sprintf("names an anchor more than one schema in %s declares", resourceName(resource))}
	}
}

// describeBase names what a fragment resolves within, for a message.
func describeBase(resource *schemaResource) string {
	if resource == nil {
		return "the document"
	}
	if resource.uri != nil {
		return "the schema the document embeds as " + resource.uri.String()
	}
	return "the schema at " + resource.location
}

// resourceName names a resource, for a message: its URI, or where it is.
func resourceName(resource *schemaResource) string {
	if resource.uri != nil {
		return resource.uri.String()
	}
	return "the schema at " + resource.location
}
