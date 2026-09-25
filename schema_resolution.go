package openbindings

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/openbindings/openbindings-go/internal/jsonpointer"
)

// reference is what a schema reference ($ref, or $dynamicRef's static
// target) names, resolved as §7 and JSON Schema 2020-12 resolve it. One
// resolution serves the reference rule (OBI-D-12), the reach of an operation's
// schema (OBI-D-10, OBI-T-08), and the bundle the schema library is given, so
// they cannot disagree.
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
// which has no URI, a same-document fragment is a JSON Pointer from the
// document root, and it may point anywhere in the document; a relative
// reference has no base (§7). Inside a resource, a fragment resolves within
// it, and any other reference against its URI; an absolute URI names the
// embedded resource declaring it, a carried meta-schema, or a resource
// outside the document.
func (d documentSchemas) resolve(ref, holder string, view any) reference {
	resource := d.resourceAt(holder)
	parsed, err := url.Parse(ref)
	if strings.HasPrefix(ref, "#") {
		// A fragment is read after URI decoding (RFC 6901 §6); one that does
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
	target := resolveURI(base, parsed)
	fragment := target.Fragment
	target.Fragment, target.RawFragment = "", ""
	id := target.String()
	if why, isAmbiguous := d.ambiguous[id]; isAmbiguous {
		// The reference names no one schema; when its fragment resolves within
		// none of the schemas declaring the URI, it resolves nowhere.
		r := reference{origin: ambiguous, uri: id, exists: missing, why: "names no one embedded schema: " + why}
		for _, claimant := range d.claimants[id] {
			if within := d.resolveFragment(claimant, fragment, view); within.exists != missing {
				r.exists = undecided
			}
		}
		return r
	}
	if embedded, isEmbedded := d.resources[id]; isEmbedded {
		return d.resolveFragment(embedded, fragment, view)
	}
	if isBuiltInMetaSchema(id) {
		return reference{origin: inMetaSchema, uri: id, exists: undecided}
	}
	return reference{origin: outside, uri: id, exists: undecided}
}

// resolveFragment resolves a fragment within a resource, or within the
// document resource when resource is nil: the resource itself, a JSON Pointer
// from it, or a plain-name anchor it declares. The document resource declares
// no anchors (§7).
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
		return reference{origin: inDocument, location: location, exists: exists, within: resource}
	}
	if resource == nil {
		return reference{origin: unresolved, exists: missing, why: "is a plain-name fragment, which §7 does not resolve from the document root"}
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
