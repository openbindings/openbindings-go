package openapi

import (
	"bytes"
	"encoding/json"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/oasdiff/yaml"
)

// externalComposition narrows external reference composition to the used
// closure.
//
// `openbindings.openapi@1` §6 ("Reference scope") states the rule: a reference
// that leaves the current document composes the value at the referenced JSON
// Pointer together with that value's transitive closure of references, and
// nothing else — so a defect OUTSIDE the composed closure does not make the
// referencing artifact unresolvable. That is the incorporated authority's own
// answer: the OAS defines the Reference Object by JSON Reference, which denotes
// the value a pointer identifies and says nothing about the document that value
// was found in.
//
// The typed loader beneath this package composes a referenced file as a UNIT:
// reading it resolves every reference in it, so one rotten schema in a shared
// 200-schema library took down every artifact that referenced any part of it.
// Rather than reimplement that loader's resolution, this pass computes the
// closure over the RAW resource trees first and then serves each external
// resource pruned to it. The loader then resolves exactly the composed nodes,
// because nothing outside them is there to resolve — and every node it does
// resolve, and every diagnostic it produces for one, is unchanged. Under
// pointer scope that diagnostic is the only fact a user has about why an
// artifact failed, so it is deliberately left to the engine that already owns
// it.
//
// The entry document is never pruned: it is the artifact, not a resource
// something reached into.
type externalComposition struct {
	read      func(*url.URL) ([]byte, error)
	join      func(*url.URL, *url.URL) *url.URL
	entry     *url.URL
	entryKey  string
	entryData []byte

	discovered bool
	resources  map[string]*url.URL
	trees      map[string]any
	parsed     map[string]bool
	kept       map[string]*retainedPointers
	pending    []compositionTarget
	seen       map[compositionTarget]bool

	// The artifact's declared OAS edition, and the reference-traversal verdict
	// it decides. `reference_traversal.go` carries the rule and its authorities:
	// a `$ref` standing in a fragment's path is resolved on the 3.0 line and
	// makes the reference unresolvable on the 3.1 line. This pass already walks
	// every composed pointer over the raw resource trees, so it is where that
	// question is asked; the load lane reads `refusal` and reports it.
	edition          string
	entryScanned     bool
	traversalRefusal error
	references       map[compositionTarget]string
}

// compositionTarget is one composed pointer into one resource. The fragment is
// the reference's decoded fragment: a JSON Pointer with its leading "/", an
// anchor name, or "" for the resource root.
type compositionTarget struct {
	key      string
	fragment string
}

func newExternalComposition(
	read func(*url.URL) ([]byte, error),
	join func(*url.URL, *url.URL) *url.URL,
) *externalComposition {
	return &externalComposition{
		read:       read,
		join:       join,
		resources:  map[string]*url.URL{},
		trees:      map[string]any{},
		parsed:     map[string]bool{},
		kept:       map[string]*retainedPointers{},
		seen:       map[compositionTarget]bool{},
		references: map[compositionTarget]string{},
	}
}

// setEntry names the ENTRY document, spelled exactly as the calling load lane
// spells it, so a resource read under that identity is recognized as the
// artifact and never pruned. `data` is the entry's own bytes where the lane
// already holds them; a lane that only addresses the entry passes nil and the
// bytes are read from the loader's own cache if they are ever needed.
func (c *externalComposition) setEntry(entry *url.URL, data []byte) {
	if c == nil {
		return
	}
	c.entry = cloneURL(entry)
	c.entryKey = artifactResourceKey(entry)
	c.entryData = data
}

// scanEntry reads the entry document's own raw tree: it records the artifact's
// declared edition, enqueues every external value the entry composes, and
// decides reference traversal for the entry's own same-resource references.
//
// It RETRIEVES NOTHING. Everything it enqueues waits for `discover`, so an
// artifact the loader refuses before following any reference does not have its
// closure fetched on the way to that refusal — the laziness a corpus specimen
// bought at 38 seconds. The entry's own bytes are already in hand at both call
// sites, so parsing them here is the only cost, and it buys the same-resource
// half of the traversal question, which no later pass sees.
//
// Read and parse failures are silent: this pass decides what to RETAIN and
// whether one specific reference is unresolvable, and the typed loader still
// reports whatever it cannot read, parse or resolve when it reaches it.
func (c *externalComposition) scanEntry(data []byte) {
	if c == nil || c.entryScanned {
		return
	}
	c.entryScanned = true
	if data == nil && c.entry != nil && c.read != nil {
		// Already read by the loader, so this is a cache hit, not a retrieval.
		data, _ = c.read(c.entry)
	}
	if !mayReferenceAnotherResource(data) && !mayCarryAReference(data) {
		return
	}
	tree, ok := parseRawResource(data)
	if !ok {
		return
	}
	if object, isObject := tree.(map[string]any); isObject {
		c.edition, _ = object["openapi"].(string)
	}
	c.trees[c.entryKey] = tree
	c.parsed[c.entryKey] = true
	c.scan(tree, c.entryKey, c.entry)
}

// discover drains everything the entry document reaches, recording which
// pointers of which external resources the artifact composes.
//
// It runs at most once, and only when the loader actually reaches outside the
// entry document, for the retrieval reason stated on `scanEntry`.
func (c *externalComposition) discover() {
	if c == nil || c.discovered {
		return
	}
	c.discovered = true
	c.scanEntry(nil)
	for len(c.pending) > 0 {
		target := c.pending[0]
		c.pending = c.pending[1:]
		c.compose(target)
	}
}

// refusal reports a reference this pass found unresolvable. Today that is the
// 3.1-line pointer-below-a-reference rule and nothing else; the load lane calls
// it after every served resource and at the embedded-content entry.
func (c *externalComposition) refusal() error {
	if c == nil {
		return nil
	}
	return c.traversalRefusal
}

// checkTraversal applies the edition-split rule of `reference_traversal.go` to
// one composed pointer. `reference` is the artifact's own `$ref` string, kept
// for the diagnostic; `tree` is the referenced document's raw contents.
//
// The first refusal wins and is not overwritten: a later reference's verdict
// says nothing more useful, and holding the first keeps the diagnostic
// deterministic under the queue's own order.
func (c *externalComposition) checkTraversal(reference, fragment string, tree any) {
	if c == nil || c.traversalRefusal != nil || openAPIFollowsPointerBelowReference(c.edition) {
		return
	}
	standing, token, below := pointerBelowReference(tree, fragment)
	if !below {
		return
	}
	c.traversalRefusal = pointerBelowReferenceRefusal(reference, c.edition, token, standing)
}

func (c *externalComposition) compose(target compositionTarget) {
	resource := c.resources[target.key]
	tree, ok := c.tree(target.key, resource)
	if !ok {
		return
	}
	retained := c.retainedFor(target.key)

	if target.fragment == "" {
		// The reference addresses the resource root, so the whole resource is
		// the composed value.
		retained.wholeResource = true
		c.scan(tree, target.key, resource)
		return
	}

	if !strings.HasPrefix(target.fragment, "/") {
		node, pointer, found := rawAnchoredNode(tree, target.fragment)
		if !found {
			return
		}
		retained.add(pointer)
		c.scan(node, target.key, resource)
		return
	}

	retained.add(target.fragment)
	// Whether a pointer may run BELOW a reference at all is decided by the
	// artifact's own edition line, not by this implementation: the 3.0 line
	// processes `$ref` as per JSON Reference and follows, the 3.1 line makes the
	// fragment a JSON-Pointer over the referenced document's literal contents
	// and the reference is unresolvable. `reference_traversal.go` carries both
	// branches with their authorities.
	c.checkTraversal(c.references[target], target.fragment, tree)
	// A pointer that stops short — because a reference stands between it and
	// its target on the following branch, or because it names nothing — still
	// composes what it did reach: the reference standing in the way is itself
	// part of the closure, and a pointer that names nothing leaves the loader to
	// say so. Retention is unchanged on the refusing branch, so the loader's own
	// diagnostics stay available for every reference that is not this one.
	node, passed, ok := rawPointerReach(tree, target.fragment, openAPIIgnoresReferenceSiblings(c.edition))
	for _, at := range passed {
		retained.markUncomposedReference(at)
	}
	if ok {
		c.scan(node, target.key, resource)
	}
}

// scan records every reference a composed value declares. `resource` is the
// value's own resource, which is the base for its relative references and the
// target of its fragment-only ones.
func (c *externalComposition) scan(node any, resourceKey string, resource *url.URL) {
	rawReferenceStrings(node, func(ref string) {
		if ref == "" {
			return
		}
		parsed, err := url.Parse(ref)
		if err != nil {
			return
		}
		if strings.HasPrefix(ref, "#") {
			if resourceKey != c.entryKey {
				c.enqueue(resourceKey, resource, parsed.Fragment, ref)
				return
			}
			// The entry document is composed whole and needs no retention, but
			// its own same-resource references ask the traversal question like
			// any other, and no later pass revisits them.
			c.checkTraversal(ref, parsed.Fragment, c.trees[c.entryKey])
			return
		}
		resolved := c.resolvePath(resource, parsed)
		if resolved == nil {
			return
		}
		key := artifactResourceKey(resolved)
		if key == "" || key == c.entryKey {
			// The entry document is composed whole and was scanned whole; a
			// reference back into it adds nothing to retain — but it addresses
			// the entry's contents, so it asks the traversal question there.
			if key == c.entryKey {
				c.checkTraversal(ref, parsed.Fragment, c.trees[c.entryKey])
			}
			return
		}
		c.enqueue(key, resolved, parsed.Fragment, ref)
	})
}

// enqueue records one composed pointer. `reference` is the artifact's own `$ref`
// string, kept only for a diagnostic: two spellings can address one target, so
// deduplication stays keyed on the resource and fragment and the first spelling
// seen is the one a refusal names.
func (c *externalComposition) enqueue(key string, resource *url.URL, fragment, reference string) {
	if _, known := c.resources[key]; !known && resource != nil {
		c.resources[key] = cloneURL(resource)
	}
	target := compositionTarget{key: key, fragment: fragment}
	if c.seen[target] {
		return
	}
	c.seen[target] = true
	c.references[target] = reference
	c.pending = append(c.pending, target)
}

func (c *externalComposition) retainedFor(key string) *retainedPointers {
	retained := c.kept[key]
	if retained == nil {
		retained = &retainedPointers{}
		c.kept[key] = retained
	}
	return retained
}

func (c *externalComposition) tree(key string, resource *url.URL) (any, bool) {
	if c.parsed[key] {
		tree, ok := c.trees[key]
		return tree, ok
	}
	c.parsed[key] = true
	if resource == nil || c.read == nil {
		return nil, false
	}
	data, err := c.read(resource)
	if err != nil {
		return nil, false
	}
	tree, ok := parseRawResource(data)
	if !ok {
		return nil, false
	}
	c.trees[key] = tree
	return tree, true
}

// resolvePath mirrors the typed loader's own base-URI join so a reference
// identifies the same resource in both passes.
func (c *externalComposition) resolvePath(base *url.URL, reference *url.URL) *url.URL {
	isFile := reference.Path != "" && reference.Host == "" &&
		(reference.Scheme == "" || reference.Scheme == "file")
	if !isFile {
		return reference
	}
	if filepath.IsAbs(reference.Path) {
		return reference
	}
	if c.join == nil || base == nil {
		return nil
	}
	return c.join(base, reference)
}

// prune returns the resource's bytes reduced to the composed closure. It
// returns the bytes unchanged for the entry document, for a resource composed
// whole, and for one this pass never reached — the last so that a reference the
// raw scan did not recognize still resolves exactly as it did before.
func (c *externalComposition) prune(resource *url.URL, data []byte) []byte {
	if c == nil {
		return data
	}
	key := artifactResourceKey(resource)
	if key == c.entryKey {
		// The entry is never pruned, but its bytes are in hand here and nowhere
		// else on this lane, so this is where its own tree is read.
		c.scanEntry(data)
		return data
	}
	// The loader has left the entry document, so the closure is now worth
	// computing. It is computed once, before the first resource outside the
	// entry is served.
	c.discover()
	if key == "" {
		return data
	}
	retained := c.kept[key]
	if retained == nil || retained.wholeResource || retained.root == nil {
		return data
	}
	tree, ok := c.trees[key]
	if !ok {
		if tree, ok = parseRawResource(data); !ok {
			return data
		}
	}
	pruned, changed := pruneToRetained(tree, retained.root)
	if !changed {
		return data
	}
	encoded, err := json.Marshal(pruned)
	if err != nil {
		return data
	}
	return encoded
}

// retainedPointers is the set of composed pointers of one resource, as a path
// trie over RFC 6901 reference tokens.
type retainedPointers struct {
	wholeResource bool
	root          *retentionNode
}

func (r *retainedPointers) add(pointer string) {
	if r.root == nil {
		r.root = &retentionNode{}
	}
	r.root.add(rawPointerTokens(pointer))
}

// markUncomposedReference records that a composed pointer PASSED THROUGH the
// reference at this position on its way to a member of the same node, so the
// reference is not itself part of the closure.
func (r *retainedPointers) markUncomposedReference(pointer string) {
	if r.root == nil {
		r.root = &retentionNode{}
	}
	r.root.markUncomposedReference(rawPointerTokens(pointer))
}

type retentionNode struct {
	whole bool
	// uncomposedReference marks a position whose `$ref` member a composed
	// pointer walked past rather than into. It is never set on a node the
	// closure composes: a composed value is retained whole, and `pruneToRetained`
	// returns such a subtree untouched before it can look at this flag.
	uncomposedReference bool
	children            map[string]*retentionNode
}

func (n *retentionNode) add(tokens []string) {
	if n.whole {
		return
	}
	if len(tokens) == 0 {
		n.whole = true
		n.children = nil
		return
	}
	if n.children == nil {
		n.children = map[string]*retentionNode{}
	}
	child := n.children[tokens[0]]
	if child == nil {
		child = &retentionNode{}
		n.children[tokens[0]] = child
	}
	child.add(tokens[1:])
}

func (n *retentionNode) markUncomposedReference(tokens []string) {
	if n.whole {
		return
	}
	if len(tokens) == 0 {
		n.uncomposedReference = true
		return
	}
	if n.children == nil {
		n.children = map[string]*retentionNode{}
	}
	child := n.children[tokens[0]]
	if child == nil {
		child = &retentionNode{}
		n.children[tokens[0]] = child
	}
	child.markUncomposedReference(tokens[1:])
}

// pruneToRetained keeps every composed value whole, keeps the members that lead
// to one, and keeps every scalar member on the way — a scalar cannot carry a
// reference, and dropping one would take an `openapi`, `$schema`, `$id` or
// `$anchor` that decides how the resource itself is read. Everything else is
// dropped: it is outside the composed closure, so under §6 it is not part of
// this artifact at all.
//
// The one scalar exception is a `$ref` at a position the closure only walked
// PAST — recorded by `markUncomposedReference` and possible on the 3.1 line
// alone. Unlike the four keywords above, a reference on an ancestor does not
// decide how a descendant is read: JSON Schema 2020-12 §8.2.3.1 makes it an
// applicator on ITS node, whose results are the referenced schema's, and the
// composed value here is a member of that node rather than the node. Serving it
// would put a target §6's "and nothing else" excludes into the artifact, and a
// defect there would refuse a source §6 says resolves.
//
// Retention is per MEMBER in both kinds of container. A sequence is addressed
// by index rather than by name, so its members cannot be dropped — a JSON
// Pointer denotes an element by position, and removing one would silently
// rename every element after it. An index outside the closure is therefore
// REPLACED in place instead: the position survives, its content does not.
//
// What it is replaced BY is decided by the kinds of the indices the closure
// does reach, not by the kind of the element being discarded — the
// implementations' convention, since §10 of the candidate places synthesis and
// document translation outside the specification. A sequence position in the
// OAS holds Objects, and the retained element is this pass's only evidence of
// which kind of value the position holds, so:
//
//   - a retained index holding a mapping makes every non-composed index the
//     empty mapping `{}`, whatever kind it had;
//   - failing that, a retained index holding a sequence makes them `[]`;
//   - where the closure reaches no container at all — every retained index is
//     a scalar, or the trie reaches none of them — nothing is substituted for a
//     scalar, and a container is emptied to its own kind.
//
// The scalar-retention rule the mapping branch applies deliberately does NOT
// carry over to a sequence in the first two cases. A mapping member is kept
// because it is NAMED: dropping it would take an `openapi`, `$schema`, `$id`,
// `$anchor` or `$ref` that decides how the resource is read. A sequence element
// has no name and can be none of those, so the reason does not transfer — while
// the cost of keeping it does: a string, a number, `null`, a sequence, and (at
// an Object-only position such as `parameters`) a boolean are all values the
// typed loader cannot read where it expects an Object, so keeping one refuses
// an artifact `openbindings.openapi@1` §6 says resolves, "however the
// referenced document is stored".
//
// `null` is not usable as the neutral element: kin-openapi refuses
// `allOf: [{…}, null]` and `parameters: [{…}, null]` with "value MUST be an
// object", so it would invent a refusal of its own. Tested, not assumed.
func pruneToRetained(value any, retained *retentionNode) (any, bool) {
	if retained == nil || retained.whole {
		return value, false
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		changed := false
		for key, child := range typed {
			if sub, ok := retained.children[key]; ok {
				prunedChild, childChanged := pruneToRetained(child, sub)
				out[key] = prunedChild
				changed = changed || childChanged
				continue
			}
			if key == "$ref" && retained.uncomposedReference {
				// The pointer walked PAST this reference into a member of the
				// same node, which only the 3.1 line admits. There the node
				// keeps its own members (JSON Schema 2020-12 §8.2.3.1's note),
				// the composed value is the member, and the reference is
				// outside the "and nothing else" of §6's reference scope.
				// Serving it would ask the loader to resolve a target this
				// artifact never composed — and refuse on its defects.
				changed = true
				continue
			}
			if isRawScalar(child) {
				out[key] = child
				continue
			}
			changed = true
		}
		if !changed {
			return value, false
		}
		return out, true
	case []any:
		neutral := neutralSequenceElement(typed, retained)
		out := make([]any, len(typed))
		changed := false
		for index, child := range typed {
			if sub, ok := retained.children[strconv.Itoa(index)]; ok {
				prunedChild, childChanged := pruneToRetained(child, sub)
				out[index] = prunedChild
				changed = changed || childChanged
				continue
			}
			if neutral != nil {
				out[index] = neutral()
				changed = true
				continue
			}
			if isRawScalar(child) {
				out[index] = child
				continue
			}
			out[index] = emptyRawContainer(child)
			changed = true
		}
		if !changed {
			return value, false
		}
		return out, true
	default:
		return value, false
	}
}

// neutralSequenceElement reports what a sequence's non-composed indices are
// replaced by, read off the indices the closure DOES reach. It returns nil when
// the closure reaches no container in this sequence, which is the only case in
// which a scalar element is left as written.
//
// It returns a constructor rather than a value so no two indices ever share one
// map or slice: the pruned tree is marshalled, and aliasing an empty container
// across positions would be a latent bug the marshaller happens not to expose.
func neutralSequenceElement(elements []any, retained *retentionNode) func() any {
	sawSequence := false
	for index, child := range elements {
		if _, ok := retained.children[strconv.Itoa(index)]; !ok {
			continue
		}
		switch child.(type) {
		case map[string]any:
			// A mapping wins outright: every OAS sequence position holds
			// Objects, so this is the common and the decisive case.
			return func() any { return map[string]any{} }
		case []any:
			sawSequence = true
		}
	}
	if sawSequence {
		return func() any { return []any{} }
	}
	return nil
}

// emptyRawContainer is the empty value of a composite's own JSON kind. It
// stands in for a sequence element outside the composed closure when the
// closure reaches no container in that sequence to read a kind from: the
// element's position has to survive so its siblings keep their indices, its
// content does not, and preserving the kind is the most conservative stand-in
// available with no evidence to the contrary.
func emptyRawContainer(value any) any {
	if _, isSequence := value.([]any); isSequence {
		return []any{}
	}
	return map[string]any{}
}

func isRawScalar(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return false
	default:
		return true
	}
}

// rawReferenceStrings visits every reference string a raw value declares.
//
// It is deliberately position-blind: every `$ref` string member and every
// Discriminator Object mapping value that looks like a reference is visited,
// which can only OVER-approximate what the typed loader resolves.
//
// The direction matters, though not because an over-approximated node goes
// unread: everything retained IS served to the loader, which resolves it. It
// matters because of what each error costs. Over-retaining composes a node §6
// does not put in this artifact, and the worst that can do is reproduce the
// whole-document behavior this pass replaced — a refusal on a defect outside
// the closure. Under-retaining would drop a node the loader must resolve,
// inventing a failure that neither scope produces, or changing what is emitted.
// One error is the old behavior; the other is a new wrong answer.
func rawReferenceStrings(value any, visit func(string)) {
	switch typed := value.(type) {
	case map[string]any:
		if ref, ok := typed["$ref"].(string); ok {
			visit(ref)
		}
		if discriminator, ok := typed["discriminator"].(map[string]any); ok {
			if mapping, ok := discriminator["mapping"].(map[string]any); ok {
				for _, target := range mapping {
					// A mapping value is a schema NAME unless it is a
					// reference; the OAS distinguishes them by the "/" the
					// typed loader also keys on.
					if ref, ok := target.(string); ok && strings.Contains(ref, "/") {
						visit(ref)
					}
				}
			}
		}
		for _, child := range typed {
			rawReferenceStrings(child, visit)
		}
	case []any:
		for _, child := range typed {
			rawReferenceStrings(child, visit)
		}
	}
}

// openAPIIgnoresReferenceSiblings reports whether the artifact's declared OAS
// edition makes a `$ref` node's other members undenotable, so that a pointer
// token naming one of them addresses nothing on that node and the reference
// itself is what the pointer's path runs into.
//
// It is the SAME edition split `reference_traversal.go` states, read from its
// other side, and it is deliberately expressed as that one predicate rather
// than a second enumeration that could drift from it. On the 3.0 line the
// Reference Object is defined by JSON Reference, whose §3 says "Any members
// other than "$ref" in a JSON Reference object SHALL be ignored" — so such a
// node has no other denotable members at all, and a pointer continuing past it
// continues into the referenced value (which is why that line FOLLOWS). On the
// 3.1 line JSON Schema 2020-12 §8.2.3.1's note has "other keywords can appear
// alongside of "$ref" in the same schema object", the node keeps its own
// members, and a token naming none of them resolves nothing (which is why that
// line REFUSES).
func openAPIIgnoresReferenceSiblings(edition string) bool {
	return openAPIFollowsPointerBelowReference(edition)
}

// rawPointerReach evaluates a JSON Pointer as far as the raw resource takes it
// and returns the deepest value reached. A pointer that runs through a
// reference stops AT that reference, whose own target the caller then composes;
// a pointer that names nothing stops where the artifact stops describing it.
//
// `ignoreSiblings` is the edition's answer above. When it holds, a `$ref` node
// standing in the pointer's path ends the walk WHETHER OR NOT it also declares
// the next token: the token cannot name an ignored member, so what the pointer
// runs into is the reference, and composing the node is what puts the
// reference's own target in the closure. Reading the sibling instead would
// under-retain — the served resource would keep a `$ref` whose target the
// closure never reached, and the typed loader would then be unable to resolve a
// reference this artifact's own edition says it must follow.
//
// It also reports every pointer prefix at which a reference was PASSED THROUGH
// rather than composed. That happens only on the other branch, where the token
// does name a member of the node in hand: the composed value is that member,
// the node's own `$ref` is outside the closure §6 defines, and serving it would
// compose a value that section's "and nothing else" excludes.
func rawPointerReach(root any, pointer string, ignoreSiblings bool) (any, []string, bool) {
	current := root
	var passed []string
	prefix := ""
	for _, token := range rawPointerTokens(pointer) {
		if object, isObject := current.(map[string]any); isObject {
			if _, isReference := object["$ref"].(string); isReference {
				if ignoreSiblings {
					return current, passed, true
				}
				if _, sibling := object[token]; sibling {
					passed = append(passed, prefix)
				}
			}
		}
		prefix += "/" + escapeRawPointerToken(token)
		switch typed := current.(type) {
		case map[string]any:
			child, ok := typed[token]
			if !ok {
				return current, passed, true
			}
			current = child
		case []any:
			index, ok := rawSequenceIndex(token, len(typed))
			if !ok {
				return current, passed, true
			}
			current = typed[index]
		default:
			return current, passed, true
		}
	}
	return current, passed, true
}

// rawSequenceIndex applies RFC 6901 §4's array-index grammar: base ten, no
// leading zeroes, in range.
func rawSequenceIndex(token string, length int) (int, bool) {
	if token == "" || (token != "0" && strings.HasPrefix(token, "0")) {
		return 0, false
	}
	index := 0
	for _, digit := range token {
		if digit < '0' || digit > '9' {
			return 0, false
		}
		index = index*10 + int(digit-'0')
		if index >= length {
			return 0, false
		}
	}
	return index, true
}

// rawAnchoredNode finds the value an `$anchor` or `$dynamicAnchor` names and
// reports the pointer it is declared under, which is what retention keys on.
func rawAnchoredNode(root any, anchor string) (any, string, bool) {
	var walk func(value any, pointer string) (any, string, bool)
	walk = func(value any, pointer string) (any, string, bool) {
		switch typed := value.(type) {
		case map[string]any:
			if named, ok := typed["$anchor"].(string); ok && named == anchor {
				return typed, pointer, true
			}
			if named, ok := typed["$dynamicAnchor"].(string); ok && named == anchor {
				return typed, pointer, true
			}
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if node, at, ok := walk(typed[key], pointer+"/"+escapeRawPointerToken(key)); ok {
					return node, at, ok
				}
			}
		case []any:
			for index, child := range typed {
				if node, at, ok := walk(child, pointer+"/"+strconv.Itoa(index)); ok {
					return node, at, ok
				}
			}
		}
		return nil, "", false
	}
	return walk(root, "")
}

func rawPointerTokens(pointer string) []string {
	if !strings.HasPrefix(pointer, "/") {
		return nil
	}
	parts := strings.Split(pointer[1:], "/")
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.ReplaceAll(part, "~1", "/")
		part = strings.ReplaceAll(part, "~0", "~")
		tokens = append(tokens, part)
	}
	return tokens
}

func escapeRawPointerToken(token string) string {
	token = strings.ReplaceAll(token, "~", "~0")
	return strings.ReplaceAll(token, "/", "~1")
}

func parseRawResource(data []byte) (any, bool) {
	var root any
	if _, err := yaml.Unmarshal(data, &root, yaml.DecodeOpts{DisableTimestamps: true}); err != nil {
		return nil, false
	}
	return root, true
}

// mayCarryAReference is the byte-level pre-check for the traversal question,
// which a single-file artifact asks too: a document with no `$ref` anywhere has
// no fragment to evaluate against anything.
func mayCarryAReference(data []byte) bool {
	return bytes.Contains(data, []byte("$ref"))
}

// mayReferenceAnotherResource is a byte-level pre-check that skips the closure
// pass for the overwhelmingly common single-file artifact. It errs toward
// running the pass: a document whose every `$ref` is plainly fragment-only, and
// which declares no Discriminator Object, can compose nothing external.
func mayReferenceAnotherResource(data []byte) bool {
	if bytes.Contains(data, []byte("discriminator")) {
		return true
	}
	rest := data
	for {
		at := bytes.Index(rest, []byte("$ref"))
		if at < 0 {
			return false
		}
		rest = rest[at+4:]
		index := 0
		for index < len(rest) {
			switch rest[index] {
			case '"', '\'', ' ', '\t', '\r', '\n', ':':
				index++
				continue
			}
			break
		}
		if index >= len(rest) || rest[index] != '#' {
			return true
		}
	}
}
