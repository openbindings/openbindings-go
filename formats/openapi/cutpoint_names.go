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
//  3. When two or more cut points claim one name, AT MOST ONE claimant keeps it
//     unqualified — the artifact's own component, or, where no artifact
//     component is in the contest, the first claimant in canonical-identity
//     order among those with no declaring document to qualify by. Every other
//     claimant is qualified by the document that declares it: that document's
//     address relative to the artifact's own, extension stripped, every
//     character outside [A-Za-z0-9_.-] replaced by "_", joined to the component
//     name with "_". Qualification is a function of the reference's identity
//     alone. A claimant whose declaring document is unknown — absent, or
//     recorded as an empty address — cannot be qualified by it and is treated
//     exactly as the artifact's own component here: qualifying it by nothing
//     would mint a name derived from nothing.
//
//     relativeDocumentName decides "relative to the artifact's own" in three
//     cases, because an address is not always a hierarchical path:
//
//       - An address with an OPAQUE path — a scheme whose remainder does not
//         begin with "/", as in `urn:example:one` — has no hierarchical path at
//         all. There is nothing to make relative, no extension to strip, and no
//         part of it that can be dropped without losing what distinguishes it
//         (`urn:example:one` and `tag:example:one` are different documents). It
//         is used verbatim.
//       - A RELATIVE address — no scheme, no authority, no leading "/" — is
//         already expressed relative to the artifact, so the artifact's own
//         address plays no part in reading it. Leading "./" segments carry no
//         information (RFC 3986 spells "no prefix" both ways) and are removed,
//         so one document referenced both ways qualifies identically. "../" is
//         NOT removed: it denotes a different place.
//       - Every other address is hierarchical: it is expressed relative to the
//         artifact's directory when it shares the artifact's origin, prefixed
//         with its authority when it does not, and then has its leading "/" and
//         file extension removed.
//
//  4. If qualification still leaves a name taken, the claimant is suffixed
//     "_2", "_3", … . Claimants are processed in canonical-identity order
//     (document address, NUL, pointer) in code-point order, so the suffix is a
//     function of the identity set rather than of the walk. Rules 2-4 together
//     are TOTAL and INJECTIVE: every cut point receives exactly one key and no
//     two cut points in one operation schema receive the same key. That is not
//     an aesthetic property — `$defs` is a map, so a repeated key drops one
//     definition and silently resolves the other cut point's `$ref` to the
//     survivor.

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
//
// The three address cases are rule 3's; the TypeScript twin
// (openapi-client/typescript/src/util.ts) reads an address the same way, and
// cutpoint_names_test.go pins the whole base x document matrix in both engines.
func relativeDocumentName(base *url.URL, document string) string {
	parsed, err := url.Parse(document)
	if err != nil || parsed == nil {
		return document
	}
	// An opaque path is not a path: nothing to make relative, nothing to strip.
	if parsed.Opaque != "" {
		return document
	}
	path := parsed.Path
	switch {
	case isRelativeAddress(parsed):
		// Already relative to the artifact; reading it does not involve the
		// artifact's own address at all.
		for strings.HasPrefix(path, "./") {
			path = path[len("./"):]
		}
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

// isRelativeAddress reports whether an address is a relative reference with a
// relative path: no scheme, no authority, and no leading "/".
func isRelativeAddress(address *url.URL) bool {
	return address.Scheme == "" && address.Host == "" && !strings.HasPrefix(address.Path, "/")
}

// sameArtifactOrigin reports whether a declaring document sits beside the
// artifact rather than on a separate service. An absent scheme on either side
// is treated as agreeing: a resolver that resolved a relative reference against
// the artifact may record the target as a bare path. An artifact addressed
// opaquely has no directory for anything to sit beside.
func sameArtifactOrigin(base, document *url.URL) bool {
	if base == nil || document == nil || base.Opaque != "" || base.Host != document.Host {
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
// function of the ref SET, so no traversal order can reach the output, and it is
// injective: `$defs` is a map, so two cut points sharing a key would drop one
// definition and resolve the other's `$ref` to the survivor.
func (n *cutPointNamer) assign(refs []string) map[string]string {
	type claim struct {
		ref string
		// desired is the name the declaring document wrote.
		desired string
		// own marks the artifact's own component, which rule 3 lets keep a
		// contested name ahead of any composed document's claimant.
		own bool
		// qualifiable reports whether a declaring document address is known, so
		// rule 3's qualification has something to qualify BY. The artifact's own
		// components are not qualifiable, and neither is an external reference
		// whose resolver recorded no document address.
		qualifiable bool
		identity    refIdentity
	}
	ordered := append([]string(nil), refs...)
	sort.Strings(ordered)

	claims := make([]claim, 0, len(ordered))
	claimed := map[string]int{}
	for _, ref := range ordered {
		// The artifact's own components carry the identity their registry key
		// spells, so every claimant has one and the ordering below is total.
		current := claim{
			ref:      ref,
			desired:  defNameForRef(ref),
			own:      true,
			identity: refIdentity{pointer: normalizeReferenceFragment(ref)},
		}
		if identity, external := n.identityOf(ref); external {
			current.own = false
			current.identity = identity
			current.desired = identity.componentName()
			current.qualifiable = identity.document != ""
		}
		claims = append(claims, current)
		claimed[current.desired]++
	}
	// The artifact's own components first, then canonical-identity order: which
	// claimant keeps a contested name unqualified must not depend on the walk
	// either. Stable over the ref-sorted input, so two claimants that agree on
	// document AND pointer — the same component by every fact recorded here —
	// still order determinately.
	sort.SliceStable(claims, func(i, j int) bool {
		if claims[i].own != claims[j].own {
			return claims[i].own
		}
		return claims[i].identity.canonical() < claims[j].identity.canonical()
	})

	names := make(map[string]string, len(claims))
	taken := map[string]bool{}
	kept := map[string]bool{}
	// Every uncontested name is the declaring document's own. Of the contested
	// ones, the claimants that cannot be qualified take the bare name — but only
	// the first of them, or the rule would not be injective.
	remaining := make([]claim, 0, len(claims))
	for _, current := range claims {
		if claimed[current.desired] == 1 || (!current.qualifiable && !kept[current.desired]) {
			names[current.ref] = current.desired
			taken[current.desired] = true
			kept[current.desired] = true
			continue
		}
		remaining = append(remaining, current)
	}
	// Contested claimants qualify by their declaring document where they have
	// one, and fall through to rule 4's suffix where they do not or where
	// qualification still lands on a taken name.
	for _, current := range remaining {
		stem := current.desired
		if current.qualifiable {
			stem = sanitizeNameSegment(relativeDocumentName(n.base, current.identity.document)) + "_" + current.desired
		}
		name := stem
		for attempt := 2; taken[name]; attempt++ {
			name = stem + "_" + strconv.Itoa(attempt)
		}
		names[current.ref] = name
		taken[name] = true
	}
	return names
}
