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
		read:      read,
		join:      join,
		resources: map[string]*url.URL{},
		trees:     map[string]any{},
		parsed:    map[string]bool{},
		kept:      map[string]*retainedPointers{},
		seen:      map[compositionTarget]bool{},
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

// discover walks the entry document's raw bytes and every composed value
// reachable from them, recording which pointers of which external resources the
// artifact composes. Read and parse failures are silent here: this pass decides
// only what to RETAIN, and the typed loader still reports whatever it cannot
// read or resolve when it reaches the same reference.
//
// It runs at most once, and only when the loader actually reaches outside the
// entry document. That laziness is not an optimization detail: an artifact the
// loader refuses before following any reference — an edition it does not
// accept, a component map it cannot even unmarshal — must not have its
// reference closure retrieved on its way to that refusal.
func (c *externalComposition) discover() {
	if c == nil || c.discovered {
		return
	}
	c.discovered = true
	data := c.entryData
	if data == nil && c.entry != nil && c.read != nil {
		// Already read by the loader, so this is a cache hit, not a retrieval.
		data, _ = c.read(c.entry)
	}
	if !mayReferenceAnotherResource(data) {
		return
	}
	tree, ok := parseRawResource(data)
	if !ok {
		return
	}
	c.scan(tree, c.entryKey, c.entry)
	for len(c.pending) > 0 {
		target := c.pending[0]
		c.pending = c.pending[1:]
		c.compose(target)
	}
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
	// A pointer that stops short — because a reference stands between it and
	// its target, or because it names nothing — still composes what it did
	// reach: the reference standing in the way is itself part of the closure,
	// and a pointer that names nothing leaves the loader to say so.
	if node, ok := rawPointerReach(tree, target.fragment); ok {
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
				c.enqueue(resourceKey, resource, parsed.Fragment)
			}
			return
		}
		resolved := c.resolvePath(resource, parsed)
		if resolved == nil {
			return
		}
		key := artifactResourceKey(resolved)
		if key == "" || key == c.entryKey {
			// The entry document is composed whole and was scanned whole; a
			// reference back into it adds nothing to retain.
			return
		}
		c.enqueue(key, resolved, parsed.Fragment)
	})
}

func (c *externalComposition) enqueue(key string, resource *url.URL, fragment string) {
	if _, known := c.resources[key]; !known && resource != nil {
		c.resources[key] = cloneURL(resource)
	}
	target := compositionTarget{key: key, fragment: fragment}
	if c.seen[target] {
		return
	}
	c.seen[target] = true
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

type retentionNode struct {
	whole    bool
	children map[string]*retentionNode
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

// pruneToRetained keeps every composed value whole, keeps the members that lead
// to one, and keeps every scalar member on the way — a scalar cannot carry a
// reference, and dropping one would take an `openapi`, `$schema`, `$id`,
// `$anchor` or `$ref` that decides how the resource itself is read. Everything
// else is dropped: it is outside the composed closure, so under §6 it is not
// part of this artifact at all.
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
		// An index-addressed member cannot be dropped without renumbering its
		// siblings, so a sequence any pointer enters is retained whole.
		return value, false
	default:
		return value, false
	}
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
// which can only OVER-approximate what the typed loader resolves. The direction
// matters — over-approximation retains a node the loader never looks at, while
// under-approximation would drop one it does.
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

// rawPointerReach evaluates a JSON Pointer as far as the raw resource takes it
// and returns the deepest value reached. A pointer that runs through a
// reference stops AT that reference, whose own target the caller then composes;
// a pointer that names nothing stops where the artifact stops describing it.
func rawPointerReach(root any, pointer string) (any, bool) {
	current := root
	for _, token := range rawPointerTokens(pointer) {
		switch typed := current.(type) {
		case map[string]any:
			child, ok := typed[token]
			if !ok {
				return current, true
			}
			current = child
		case []any:
			index, ok := rawSequenceIndex(token, len(typed))
			if !ok {
				return current, true
			}
			current = typed[index]
		default:
			return current, true
		}
	}
	return current, true
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
