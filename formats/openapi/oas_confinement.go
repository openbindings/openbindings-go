package openapi

// Load-path confinement (block 8d-2), behind the fast path.
//
// The acceptance floor (acceptance_floor.go) decides which UNIT owns a defect.
// This file answers the other half of the same question: when the shipped
// loader refuses the whole artifact over a defect the floor has already
// confined to one unit, the load must not throw away every sibling unit that
// is intact. The pass neutralises the defective raw position and retries, so
// that the floor's per-unit verdicts are the ones a consumer sees.
//
// Three mechanisms, in the order (a), (c), (b). The first two are ported from
// the block 8a spike
// (`corpus-lab/go-evaluator/oas_confinement.go` and `…_seamc.go`, whose
// findings are recorded at `corpus-lab/openapi-runtime/22-…`):
//
//	(a) the ORACLE walk. The only oracle is kin-openapi's own typed unmarshal:
//
//	        probe(tree) := json.Unmarshal(render(tree), &openapi3.T{}) == nil
//
//	    which is the loader's own call with the loader's own destination type.
//	    This file holds no OAS type model and no kin-openapi struct knowledge.
//	    Localization is by RESTRICTION: to ask whether the value at pointer P
//	    carries a kind defect, build a skeleton document holding only the
//	    container chain down to P with the value at P, and ask the oracle.
//	    That is sound because kin-openapi decodes every fixed field
//	    independently of its siblings, so removing siblings can neither create
//	    a kind error nor hide one inside the retained value. Descent stops
//	    where a container's OWN kind is wrong.
//
//	(b) SEAM C. Reference-target-kind defects never reach the unmarshal
//	    oracle: kin reports them from reference resolution as
//	    `bad data in "<target>" (expecting ref to <kind> object)`, naming the
//	    TARGET and never the referencing site. The sites are recovered by a raw
//	    search for `$ref` strings denoting that target, and each site is then
//	    handled by what the OAS says about the position it occupies.
//
//	(c) the UREF ROUND (block 8g). Reference RESOLUTION failures reach neither
//	    of the above: kin accepts `{"$ref": "#/x"}` at the unmarshal oracle and
//	    then fails while resolving it, with a report that names no kind and
//	    matches no seam-C pattern. The ladder already classifies these -- every
//	    unresolvable internal reference is a URef defect at its REFERENCING
//	    site -- so the round reads `acceptanceFloor.ClimbingURefSites` and
//	    neutralises exactly the sites whose verdict CLIMBS. Sites no unit's
//	    closure walk reaches are never touched.
//
//	    Unlike (a) and (b), this round AUTHORS: deleting a `$ref` member leaves
//	    a value the artifact never wrote. So it alone is gated on EMISSION --
//	    see `confinementEmissionGate` below, and read that before changing
//	    anything here.
//
// THE RAIL THAT MAKES THIS A CONFINEMENT AND NOT SALVAGE: every position this
// pass touches must be a position the LADDER ATTRIBUTES. A located defect the
// floor cannot name is refused with the loader's original error -- behaviour
// unchanged, and never silently dropped. Removing a member the floor does not
// account for would be exactly the Scenario-C salvage the ruling forbids.
//
// Consequence, stated because it is load-bearing: the registry-scoped classes
// D14/D15 are deliberately not part of the shipped roster (block 8d-1 record
// §2, left to 8d-3). Every specimen whose located defect set includes one of
// them therefore still refuses here, by design.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// confinementSeamCRounds bounds the seam-C iteration. Each round closes one
// reference target; a document may carry several (the corpus's worst is two).
const confinementSeamCRounds = 8

// confinementProbe reports whether kin-openapi's typed unmarshal accepts the
// tree. This is the loader's own `json.Unmarshal(data, doc)` reached with the
// loader's own destination type -- no local type model.
func confinementProbe(tree any) error {
	data, err := json.Marshal(tree)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &openapi3.T{})
}

// ---- pointers --------------------------------------------------------------

type confinementToken struct {
	key     string
	index   int
	inArray bool
}

func confinementPointerOf(path []confinementToken) string {
	var b strings.Builder
	for _, t := range path {
		b.WriteByte('/')
		if t.inArray {
			b.WriteString(strconv.Itoa(t.index))
			continue
		}
		b.WriteString(floorEsc(t.key))
	}
	if b.Len() == 0 {
		return "#"
	}
	return "#" + b.String()
}

func confinementParsePointer(pointer string) ([]confinementToken, error) {
	body := strings.TrimPrefix(pointer, "#")
	if body == "" {
		return nil, fmt.Errorf("root pointer is not confinable")
	}
	parts := strings.Split(strings.TrimPrefix(body, "/"), "/")
	out := make([]confinementToken, 0, len(parts))
	for _, p := range parts {
		decoded := strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
		out = append(out, confinementToken{key: decoded})
	}
	return out, nil
}

// confinementSkeleton rebuilds only the container chain down to path, with
// value at the end. A sequence token becomes a one-element sequence: an
// element's decoding context is its position's type, not its index.
func confinementSkeleton(path []confinementToken, value any) any {
	current := value
	for i := len(path) - 1; i >= 0; i-- {
		if path[i].inArray {
			current = []any{current}
			continue
		}
		current = map[string]any{path[i].key: current}
	}
	return current
}

func confinementEmptyLike(value any) (any, bool) {
	switch value.(type) {
	case map[string]any:
		return map[string]any{}, true
	case []any:
		return []any{}, true
	}
	return nil, false
}

// confinementResolveParent walks the live tree, deciding at each step from the
// CONTAINER's own runtime kind whether a token is a key or an index.
func confinementResolveParent(root any, tokens []confinementToken) (any, confinementToken, bool) {
	current := root
	for i := 0; i < len(tokens)-1; i++ {
		next, ok := confinementStep(current, &tokens[i])
		if !ok {
			return nil, confinementToken{}, false
		}
		current = next
	}
	last := tokens[len(tokens)-1]
	if arr, ok := current.([]any); ok {
		index, err := strconv.Atoi(last.key)
		if err != nil || index < 0 || index >= len(arr) {
			return nil, confinementToken{}, false
		}
		last.inArray = true
		last.index = index
	}
	return current, last, true
}

func confinementStep(container any, t *confinementToken) (any, bool) {
	switch c := container.(type) {
	case map[string]any:
		v, ok := c[t.key]
		return v, ok
	case []any:
		index, err := strconv.Atoi(t.key)
		if err != nil || index < 0 || index >= len(c) {
			return nil, false
		}
		t.inArray = true
		t.index = index
		return c[index], true
	}
	return nil, false
}

// ---- localization ----------------------------------------------------------

// confinementLocate returns the deepest positions whose own value the oracle
// rejects.
func confinementLocate(root any) []string {
	if confinementProbe(root) == nil {
		return nil
	}
	var out []string
	confinementWalk(nil, root, &out)
	return out
}

func confinementWalk(path []confinementToken, node any, out *[]string) {
	// Precondition: confinementSkeleton(path, node) is rejected by the oracle.
	if empty, isContainer := confinementEmptyLike(node); isContainer {
		if confinementProbe(confinementSkeleton(path, empty)) != nil {
			// The container's own kind is wrong at this position; its children
			// are not examined.
			*out = append(*out, confinementPointerOf(path))
			return
		}
	} else {
		*out = append(*out, confinementPointerOf(path))
		return
	}

	found := false
	switch typed := node.(type) {
	case map[string]any:
		for _, k := range floorKeys(typed) {
			child := append(append([]confinementToken{}, path...), confinementToken{key: k})
			if confinementProbe(confinementSkeleton(child, typed[k])) != nil {
				found = true
				confinementWalk(child, typed[k], out)
			}
		}
	case []any:
		for i, v := range typed {
			child := append(append([]confinementToken{}, path...), confinementToken{index: i, inArray: true})
			if confinementProbe(confinementSkeleton(child, v)) != nil {
				found = true
				confinementWalk(child, v, out)
			}
		}
	}
	if !found {
		*out = append(*out, confinementPointerOf(path))
	}
}

// ---- neutralization --------------------------------------------------------

// confinementNeutralize neutralises one located position in place. A mapping
// member is REMOVED; a sequence element is EMPTIED IN PLACE so that no sibling
// index moves -- the retention discipline the external-closure pruner already
// established. Every candidate neutral value is accepted only if the oracle
// then accepts the position: this pass never decides what a position may hold.
func confinementNeutralize(root any, pointer string) bool {
	tokens, err := confinementParsePointer(pointer)
	if err != nil {
		return false
	}
	parent, last, ok := confinementResolveParent(root, tokens)
	if !ok {
		return false
	}
	switch p := parent.(type) {
	case map[string]any:
		delete(p, last.key)
		return true
	case []any:
		for _, neutral := range []any{map[string]any{}, "", []any{}} {
			original := p[last.index]
			p[last.index] = neutral
			if confinementProbe(confinementSkeleton(tokens, neutral)) == nil {
				return true
			}
			p[last.index] = original
		}
	}
	return false
}

// ---- seam C ----------------------------------------------------------------

// confinementBadData matches kin-openapi's reference-target-kind report. The
// pass depends on this error TEXT, which the unmarshal oracle does not; that
// dependence is why seam C is the second mechanism and not the first.
var confinementBadData = regexp.MustCompile(`bad data in "([^"]+)" \(expecting ref to ([a-z ]+) object\)`)

// confinementRefSites returns the pointers of every raw object carrying a
// `$ref` member whose value denotes the given entry-document pointer. BOTH
// fragment spellings are matched -- with and without the leading `#` -- because
// the loader's report and the artifact's own strings do not always agree on it.
func confinementRefSites(root any, target string) []string {
	fragment := strings.TrimPrefix(target, "#")
	spellings := map[string]bool{target: true, fragment: true, "#" + fragment: true}
	var out []string
	var walkNode func(path []confinementToken, node any)
	walkNode = func(path []confinementToken, node any) {
		switch typed := node.(type) {
		case map[string]any:
			if ref, ok := typed["$ref"].(string); ok && spellings[ref] {
				out = append(out, confinementPointerOf(path))
			}
			for _, k := range floorKeys(typed) {
				walkNode(append(append([]confinementToken{}, path...), confinementToken{key: k}), typed[k])
			}
		case []any:
			for i, v := range typed {
				walkNode(append(append([]confinementToken{}, path...), confinementToken{index: i, inArray: true}), v)
			}
		}
	}
	walkNode(nil, root)
	sort.Strings(out)
	return out
}

// confinementResolveRaw resolves an entry-document JSON Pointer against the raw
// tree and returns the value at it.
func confinementResolveRaw(root any, pointer string) (any, bool) {
	tokens, err := confinementParsePointer(pointer)
	if err != nil {
		return nil, false
	}
	parent, last, ok := confinementResolveParent(root, tokens)
	if !ok {
		return nil, false
	}
	switch p := parent.(type) {
	case map[string]any:
		v, found := p[last.key]
		return v, found
	case []any:
		if last.index < 0 || last.index >= len(p) {
			return nil, false
		}
		return p[last.index], true
	}
	return nil, false
}

// confinementSetAt replaces the value at a pointer.
func confinementSetAt(root any, pointer string, value any) bool {
	tokens, err := confinementParsePointer(pointer)
	if err != nil {
		return false
	}
	parent, last, ok := confinementResolveParent(root, tokens)
	if !ok {
		return false
	}
	switch p := parent.(type) {
	case map[string]any:
		p[last.key] = value
		return true
	case []any:
		if last.index < 0 || last.index >= len(p) {
			return false
		}
		p[last.index] = value
		return true
	}
	return false
}

func confinementRemoveAt(root any, pointer string) bool {
	tokens, err := confinementParsePointer(pointer)
	if err != nil {
		return false
	}
	parent, last, ok := confinementResolveParent(root, tokens)
	if !ok {
		return false
	}
	if p, isMap := parent.(map[string]any); isMap {
		delete(p, last.key)
		return true
	}
	return false
}

// confinementApplySeamC handles one referencing site by what the OAS says
// about the position the site occupies. The ladder answers the position
// question; this function never decides it itself.
//
//   - a SCHEMA position: the site is INLINED with the target's raw value. A
//     JSON Reference denotes the value at the pointer (3.0: the Reference
//     Object "is defined by JSON Reference"; 3.1 §4.6 interprets the fragment
//     as a JSON Pointer per RFC 6901), so on the 3.1 line -- where any object
//     is a valid Schema Object -- there is no authority defect at all, and on
//     the 3.0 line the same holds for a target D6 proves carries a conforming
//     Schema Object. Nothing is authored: the value the artifact points at is
//     the value that lands.
//   - a RESPONSE position that the ladder has already recorded as D7: the
//     member is removed. The D7 entry the floor emits at that position is what
//     accounts for it; the response rung then decides, through P2, whether the
//     containing operation climbs.
//   - anything else: refused, so the load keeps its original error.
func confinementApplySeamC(root any, floor *acceptanceFloor, target, site string) bool {
	if floor.ResponseMemberDefects[site] {
		return confinementRemoveAt(root, site)
	}
	if floor.SchemaPositions[site] {
		if floor.Line != "3.1" && !floor.ConformantSchemaComponents[target] {
			return false
		}
		// Only a bare Reference Object is inlined. A `$ref` carrying siblings
		// is the ref-sibling composition question, which the shipped
		// normalizer owns; replacing the node would discard what it composes.
		node, ok := confinementResolveRaw(root, site)
		if !ok {
			return false
		}
		nodeMap, isMap := node.(map[string]any)
		if !isMap || len(nodeMap) != 1 {
			return false
		}
		value, found := confinementResolveRaw(root, target)
		if !found {
			return false
		}
		return confinementSetAt(root, site, value)
	}
	return false
}

// ---- the URef round --------------------------------------------------------

// confinementNeutralizeURef removes the `$ref` MEMBER at one referencing site,
// leaving any siblings in place.
//
// Only the member is removed, never the position, so the declared inventory
// the ladder counted does not move: the position stays declared and loses the
// reference that denotes nothing.
//
// What remains at the position is AUTHORED -- the artifact never wrote it --
// and nothing about the site's membership in `ClimbingURefSites` says where it
// can be read from. Its climbing unit emits nothing; another unit entirely may
// reach the same raw position and emit, and the channel it reaches it through
// need not be one the ladder's closure walk visits. That is why the round is
// admitted only through `confinementEmissionGate`, and why no reasoning here
// may stand in for it.
//
// Seam C's bare-Reference-Object restriction deliberately does NOT carry over.
// There it guards a real composition: the target exists, and replacing the node
// would discard the siblings the shipped normalizer composes with it. Here the
// target does not exist -- that is the whole finding -- so there is no
// composition to discard, and refusing a `$ref` with siblings would only
// abandon the confinement over a sibling the pass does not touch.
func confinementNeutralizeURef(root any, site string) bool {
	node, ok := confinementResolveRaw(root, site)
	if !ok {
		return false
	}
	nodeMap, isMap := node.(map[string]any)
	if !isMap {
		return false
	}
	if _, hasRef := nodeMap["$ref"]; !hasRef {
		return false
	}
	delete(nodeMap, "$ref")
	return true
}

// ---- the emission gate -------------------------------------------------------

// confinementEmissionGate answers the one question that decides whether a
// confinement whose URef round AUTHORED anything may be admitted:
//
//	does anything the confinement authored reach the content this engine emits?
//
// It is answered by the ENGINE'S OWN EMITTER, over the document the
// confinement actually produced, and never by a traversal written here or by
// the acceptance floor's closure walk. That distinction is the whole design.
// The floor walks the channels its ladder needs; an emitter walks the channels
// it emits from; those two sets are not the same, and every attempt to decide
// this question from the ladder's walk has been defeated by an ordinary OAS
// channel the walk does not visit -- a Parameter Object's `content` form, a
// success response that is a Reference Object, a `requestBody` that is a
// Reference Object. Enumerating channels answers the probes and not the class,
// so this gate enumerates nothing.
//
// The gate is handed two loads of the SAME confined tree, differing only at
// the authored positions: `shipped`, and `marked`, which carries one
// distinguishing member at each authored position. It reports whether the two
// EMIT identically. If they do, then no channel of any kind carries an
// authored position into emitted content, whatever route an emitter took to
// look for one; if they do not, the confinement is refused and the loader's
// original error stands.
//
// A nil gate means this engine cannot demonstrate emission-freedom, and the
// URef round declines -- the loader's original error, which is the behaviour
// before the round existed. `openapi-client/go` passes nil: it derives no
// interface from a document at all, so it has no emission to compare, and a
// pass that cannot show its authored values are unreachable must not admit
// them.
type confinementEmissionGate func(shipped, marked *openapi3.T, floor *acceptanceFloor) bool

// confinementMarkKey is the distinguishing member the gate's `marked` image
// carries at every authored position. It is an extension key, so it is legal
// wherever the OAS admits specification extensions, and it is carried into
// emitted content by any emitter that carries the position's value at all.
const confinementMarkKey = "x-openbindings-confinement-authored"

// confinementMarkValue is fixed rather than random so that two runs over the
// same artifact produce the same bytes.
const confinementMarkValue = "confinement-authored-position"

func confinementMarkAt(root any, site string, mark bool) bool {
	node, ok := confinementResolveRaw(root, site)
	if !ok {
		return false
	}
	nodeMap, isMap := node.(map[string]any)
	if !isMap {
		return false
	}
	if mark {
		nodeMap[confinementMarkKey] = confinementMarkValue
		return true
	}
	delete(nodeMap, confinementMarkKey)
	return true
}

// confinementAdmit gates a confined tree whose URef round authored at
// `authored`. It returns the pass's three results directly: the document to
// hand back, or a decline that keeps the loader's original error.
//
// Two obligations, in order:
//
//  1. PROVABILITY. Every authored position must be one the ladder recorded as
//     a Schema Object position. A schema's value is what an emitter carries
//     verbatim, so a mark placed there is visible in emitted content exactly
//     when the value is. At any other position the pass cannot demonstrate
//     what an emitter would do with what it authored, so it declines rather
//     than guess. This is a restriction on where the pass may AUTHOR, not an
//     enumeration of how a unit may reach it.
//
//  2. EMISSION. Load the marked image and the shipped image through the same
//     shipped `reload`, and ask the gate whether they emit identically. The
//     shipped image is loaded LAST so that every piece of lane state the
//     surrounding load collected describes the document this pass returns.
func confinementAdmit(tree any, authored []string, floor *acceptanceFloor,
	reload func([]byte) (*openapi3.T, error), gate confinementEmissionGate) (*openapi3.T, error, bool) {
	if gate == nil {
		return nil, nil, false
	}
	for _, site := range authored {
		if !floor.SchemaPositions[site] {
			return nil, nil, false
		}
	}
	shippedData, err := json.Marshal(tree)
	if err != nil {
		return nil, nil, false
	}
	// A mark the artifact could have written itself would not discriminate.
	if strings.Contains(string(shippedData), confinementMarkKey) {
		return nil, nil, false
	}
	marked := true
	for _, site := range authored {
		if !confinementMarkAt(tree, site, true) {
			marked = false
			break
		}
	}
	markedData, markErr := json.Marshal(tree)
	for _, site := range authored {
		confinementMarkAt(tree, site, false)
	}
	if !marked || markErr != nil {
		return nil, nil, false
	}
	markedDoc, markedLoadErr := reload(markedData)
	shippedDoc, shippedLoadErr := reload(shippedData)
	if markedLoadErr != nil || shippedLoadErr != nil {
		return nil, nil, false
	}
	if !gate(shippedDoc, markedDoc, floor) {
		return nil, nil, false
	}
	return shippedDoc, nil, true
}

func confinementSortedSites(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for site := range set {
		out = append(out, site)
	}
	sort.Strings(out)
	return out
}

// ---- the pass --------------------------------------------------------------

// confineEntryDocument runs behind the fast path: it is reached only after the
// shipped load has already failed.
//
// `entry` is the entry document's own raw image (pre-normalization), which is
// also what the acceptance floor classifies against; `reload` re-runs the
// shipped load with those bytes substituted for the entry document, so every
// lane keeps its own base-URI and retrieval semantics.
//
// The third result is whether the pass reached a verdict at all:
//
//   - false: the caller keeps its ORIGINAL error. This is what a located
//     defect with no ladder attribution produces, and what an unplaceable
//     seam-C site produces -- behaviour unchanged, and never a silent drop.
//   - true with a nil error: the confined document, whose per-unit accounting
//     is the floor's.
//   - true with a non-nil error: the entry document was fully confined and
//     accounted, and the load still failed for a reason that is not the entry
//     document's shape -- offline retrieval of an external resource, in the
//     corpus's two instances. That error is the honest one to report, and
//     reporting the pre-confinement unmarshal error instead would name a
//     defect that is no longer what blocks the load.
//
// `gate` is this engine's emission rail; see `confinementEmissionGate`. Only
// the URef round is gated, because only the URef round authors a value.
func confineEntryDocument(entry []byte, reload func([]byte) (*openapi3.T, error), originalErr error, gate confinementEmissionGate) (*openapi3.T, error, bool) {
	if len(entry) == 0 || reload == nil {
		return nil, nil, false
	}
	tree, parsed := parseRawResource(entry)
	if !parsed {
		return nil, nil, false
	}
	root, isObject := tree.(map[string]any)
	if !isObject {
		return nil, nil, false
	}
	// The edition gate is part 1's, owned by the load path: an artifact this
	// instrument declines to read is never confined.
	floor := computeAcceptanceFloor(root)
	if floor == nil {
		return nil, nil, false
	}

	// (a) the oracle walk.
	located := confinementLocate(tree)
	if len(located) == 0 &&
		!confinementBadData.MatchString(errorText(originalErr)) &&
		len(floor.ClimbingURefSites) == 0 {
		// Nothing for any mechanism to do; do not pay for a second load.
		return nil, nil, false
	}
	for _, pointer := range located {
		if _, attributed := floor.attributes(pointer); !attributed {
			return nil, nil, false
		}
		if !confinementNeutralize(tree, pointer) {
			return nil, nil, false
		}
	}

	data, err := json.Marshal(tree)
	if err != nil {
		return nil, nil, false
	}
	doc, loadErr := reload(data)
	if loadErr == nil {
		return doc, nil, true
	}

	// (c) the URef round. Reference RESOLUTION failures never reach the
	// unmarshal oracle -- kin accepts `{"$ref": "#/x"}` and then fails while
	// resolving it -- and they carry no `bad data in "…"` report, so neither
	// earlier mechanism can see them. The ladder already does: every
	// unresolvable internal reference is a URef defect at its referencing
	// site. The climbing ones are neutralised here, once, together.
	var authored []string
	if len(floor.ClimbingURefSites) > 0 {
		authored = confinementSortedSites(floor.ClimbingURefSites)
		for _, site := range authored {
			if !confinementNeutralizeURef(tree, site) {
				return nil, nil, false
			}
		}
		if data, err = json.Marshal(tree); err != nil {
			return nil, nil, false
		}
		if doc, loadErr = reload(data); loadErr == nil {
			return confinementAdmit(tree, authored, floor, reload, gate)
		}
	}

	// (b) seam-C rounds. A seam-C round that succeeds after (c) has run returns
	// a document that still carries (c)'s authored values, so it is gated too.
	for round := 0; round < confinementSeamCRounds; round++ {
		match := confinementBadData.FindStringSubmatch(loadErr.Error())
		if match == nil {
			return nil, loadErr, true
		}
		sites := confinementRefSites(tree, match[1])
		if len(sites) == 0 {
			return nil, nil, false
		}
		for _, site := range sites {
			if !confinementApplySeamC(tree, floor, match[1], site) {
				return nil, nil, false
			}
		}
		data, err = json.Marshal(tree)
		if err != nil {
			return nil, nil, false
		}
		doc, loadErr = reload(data)
		if loadErr == nil {
			if len(authored) == 0 {
				return doc, nil, true
			}
			return confinementAdmit(tree, authored, floor, reload, gate)
		}
	}
	return nil, nil, false
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
