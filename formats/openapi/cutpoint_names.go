package openapi

import (
	"net/url"
	gopath "path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Cut-point naming.
//
// A cyclic schema is emitted using the dialect's own recursion mechanism: the
// cycle participant is hoisted into the operation schema's `$defs` and every
// occurrence becomes a same-document `$ref` to it. The hoisted member needs a
// key, and that key is SYNTHESIS surface: `openbindings.openapi@1` §10 places
// deterministic generation of OBI documents from OpenAPI artifacts outside the
// specification, and the binding-specification authoring doctrine states that a
// family specification does not define a synthesis naming convention. Nothing
// in Core, in the OpenAPI specification, or in anything either incorporates
// decides it. It is therefore this implementation's convention, and its only
// hard obligation is that the Go and TypeScript engines mint identical keys.
//
// The convention, twinned in openapi-client/typescript/src/util.ts:
//
//  1. A cut point is named by the component's OWN name in the document that
//     declares it — the RFC 6901-unescaped final token of the pointer under
//     which that document declares it. For a component in the artifact itself
//     that is its `components.schemas` key; for a component reached through an
//     external `$ref` it is the same key in the target document. (This is the
//     convention the AsyncAPI family settled as F7: cut at the artifact's own
//     component name.)
//
//  2. Names are assigned over the SET of cut points minted for one operation
//     schema, never in traversal order. A name claimed by exactly one cut point
//     is used as written.
//
//  3. When two or more cut points claim one name, the artifact's own component
//     keeps it and each externally-declared claimant is qualified by the
//     document that declares it: that document's address relative to the
//     artifact's own, extension stripped, every character outside
//     [A-Za-z0-9_.-] replaced by "_", joined to the component name with "_".
//     Qualification is a function of the reference's identity alone.
//
//  4. If qualification still leaves a name claimed twice, the remaining
//     claimants are ordered by their canonical identity (document address, NUL,
//     pointer) in code-point order and suffixed "_2", "_3", … . This is a last
//     resort; no artifact shape is known to reach it, and it exists so the rule
//     is total.

// refIdentity is the identity of a component reached through an external
// reference: the address of the document that declares it and the RFC 6901
// pointer under which that document declares it.
//
// The pointer is normalized. kin-openapi records a reference path's fragment
// in two spellings — resolveRefPath (loader.go) keeps the leading '#' of an
// internal reference, while setPathRef strips it — so one component reaches a
// RefNameResolver under two spellings of one pointer and internalizes twice.
// Normalizing here gives a component exactly one identity.
type refIdentity struct {
	document string
	pointer  string
}

func (id refIdentity) canonical() string {
	return id.document + "\x00" + id.pointer
}

// componentName is the artifact's own name for the identified component.
func (id refIdentity) componentName() string {
	pointer := id.pointer
	if i := strings.LastIndex(pointer, "/"); i >= 0 {
		pointer = pointer[i+1:]
	}
	return unescapeJSONPointerSegment(pointer)
}

func normalizeReferenceFragment(fragment string) string {
	return strings.TrimPrefix(fragment, "#")
}

var invalidNameSegmentChars = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func sanitizeNameSegment(segment string) string {
	return invalidNameSegmentChars.ReplaceAllString(segment, "_")
}

// relativeDocumentName expresses a declaring document's address relative to the
// artifact's own address, with any file extension removed. Keeping it relative
// is what makes a qualified name independent of how the artifact was reached:
// the same two documents laid out the same way qualify identically whether they
// were loaded from a checkout or from a server.
func relativeDocumentName(base *url.URL, document string) string {
	parsed, err := url.Parse(document)
	if err != nil || parsed == nil {
		return document
	}
	path := parsed.Path
	switch {
	case sameArtifactOrigin(base, parsed):
		dir := gopath.Dir(base.Path)
		if dir != "/" {
			dir += "/"
		}
		path = strings.TrimPrefix(path, dir)
	case parsed.Host != "":
		path = parsed.Host + path
	}
	path = strings.TrimPrefix(path, "/")
	if ext := gopath.Ext(path); ext != "" {
		path = strings.TrimSuffix(path, ext)
	}
	if path == "" {
		return document
	}
	return path
}

// sameArtifactOrigin reports whether a declaring document sits beside the
// artifact rather than on a separate service. An absent scheme on either side
// is treated as agreeing: a resolver that resolved a relative reference against
// the artifact may record the target as a bare path.
func sameArtifactOrigin(base, document *url.URL) bool {
	if base == nil || document == nil || base.Host != document.Host {
		return false
	}
	return base.Scheme == document.Scheme || base.Scheme == "" || document.Scheme == ""
}

// cutPointNamer assigns `$defs` keys for one synthesis. `externals` maps an
// internalized component key to the identity it was internalized from; a
// registry key absent from it is the artifact's own component.
type cutPointNamer struct {
	externals map[string]refIdentity
	base      *url.URL
}

func newCutPointNamer(location string, externals map[string]refIdentity) *cutPointNamer {
	namer := &cutPointNamer{externals: externals}
	if location != "" {
		if parsed, err := url.Parse(location); err == nil {
			namer.base = parsed
		}
	}
	return namer
}

// identityOf reports the declaring identity of a registry ref, and whether the
// ref names a component reached through an external reference.
func (n *cutPointNamer) identityOf(ref string) (refIdentity, bool) {
	if n == nil {
		return refIdentity{}, false
	}
	key := ref
	if i := strings.LastIndex(key, "/"); i >= 0 {
		key = key[i+1:]
	}
	identity, external := n.externals[unescapeJSONPointerSegment(key)]
	return identity, external
}

// assign names every cut point minted for one operation schema. The result is a
// function of the ref SET, so no traversal order can reach the output.
func (n *cutPointNamer) assign(refs []string) map[string]string {
	type claim struct {
		ref      string
		desired  string
		external bool
		identity refIdentity
	}
	ordered := append([]string(nil), refs...)
	sort.Strings(ordered)

	claims := make([]claim, 0, len(ordered))
	claimed := map[string]int{}
	for _, ref := range ordered {
		current := claim{ref: ref, desired: defNameForRef(ref)}
		if identity, external := n.identityOf(ref); external {
			current.external = true
			current.identity = identity
			current.desired = identity.componentName()
		}
		claims = append(claims, current)
		claimed[current.desired]++
	}

	names := make(map[string]string, len(claims))
	taken := map[string]bool{}
	// The artifact's own components, and every uncontested name, keep the name
	// the declaring document wrote.
	remaining := make([]claim, 0, len(claims))
	for _, current := range claims {
		if claimed[current.desired] == 1 || !current.external {
			names[current.ref] = current.desired
			taken[current.desired] = true
			continue
		}
		remaining = append(remaining, current)
	}
	// Contested externally-declared claimants qualify by their declaring
	// document, in canonical-identity order so the last-resort suffix is a
	// function of the identity set rather than of the walk.
	sort.Slice(remaining, func(i, j int) bool {
		return remaining[i].identity.canonical() < remaining[j].identity.canonical()
	})
	for _, current := range remaining {
		qualified := sanitizeNameSegment(relativeDocumentName(n.base, current.identity.document)) + "_" + current.desired
		name := qualified
		for attempt := 2; taken[name]; attempt++ {
			name = qualified + "_" + strconv.Itoa(attempt)
		}
		names[current.ref] = name
		taken[name] = true
	}
	return names
}
