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
//	    This round AUTHORS: deleting a `$ref` member leaves a value the
//	    artifact never wrote. It is gated on EMISSION -- see
//	    `confinementEmissionGate` below, and read that before changing
//	    anything here.
//
//	    IT IS NOT THE ONLY MECHANISM THAT AUTHORS. This comment used to say
//	    "Unlike (a) and (b), this round AUTHORS", and that was FALSE:
//	    mechanism (a) authors too -- `confinementNeutralize` DELETES a mapping
//	    member, and the container that remains is not what the artifact
//	    declared. (a) is UNGATED. That is an OPEN HOLE, not a property; it is
//	    filed with reproducers, mechanism attribution and corpus incidence at
//	    `corpus-lab/openapi-runtime/102-block-8g-THIRD-RUN-LANDED-*` §3.
//
// THE RAILS, AND WHICH ONE EACH MECHANISM ACTUALLY HAS.
//
// Every position this pass touches must be a position the LADDER ATTRIBUTES.
// A located defect the floor cannot name is refused with the loader's original
// error -- behaviour unchanged, and never silently dropped.
//
// THAT IS NECESSARY AND NOT SUFFICIENT, and it must not be read as what makes
// this a confinement rather than salvage. It was offered as exactly that, and
// it has now been refuted three times over. The ladder's closure walk is not
// the emitter's traversal: a position the walk attributes to an invalid unit
// can still be READ by a SURVIVING unit through a channel the walk never
// visits -- a Parameter Object's `content` form, a success response that is a
// Reference Object, a `requestBody` that is a Reference Object, a second
// success-response media alternative. Attribution says which unit OWNS a
// defect. It says nothing about where the authored value can be read from.
//
// Only the URef round (c) is held to the sufficient condition: ask the
// EMITTER, see `confinementEmissionGate`. Mechanisms (a) and (b) still stand
// on attribution alone. For (a) that is a MEASURED hole and not an argument
// (see `confinementNeutralize`); for (b) it is unproven rather than clean.
//
// Consequence, stated because it is load-bearing: the registry-scoped classes
// D14/D15 are deliberately not part of the shipped roster (block 8d-1 record
// §2, left to 8d-3). Every specimen whose located defect set includes one of
// them therefore still refuses here, by design.

import (
	"encoding/json"
	"fmt"
	"reflect"
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
//
// BOTH BRANCHES AUTHOR, and both register with the ledger. `delete(p,
// last.key)` leaves a container the artifact never wrote; replacing a sequence
// element with an empty value leaves an element the artifact never wrote. The
// ledger is not optional and is not a log: `confinementAdmit` refuses every
// confinement it cannot clear, so a position registered here is a position the
// engine's own emitter must certify unreachable before anything ships.
//
// Block 8g gated only the URef round and left this mechanism on the ladder's
// attribution, which is the rail both 8g parks refuted; block 8h measured the
// cost of that (three constructed OAS 3.0.3 documents synthesized here with a
// declared member silently gone while TypeScript refused all three under
// `OBI-D-17`) and moved this mechanism onto the same rail.
func confinementNeutralize(root any, pointer string, ledger *confinementLedger) bool {
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
		ledger.author(pointer)
		delete(p, last.key)
		return true
	case []any:
		for _, neutral := range []any{map[string]any{}, "", []any{}} {
			original := p[last.index]
			p[last.index] = neutral
			if confinementProbe(confinementSkeleton(tokens, neutral)) == nil {
				ledger.author(pointer)
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
	spellings := confinementRefSpellings(target)
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

// confinementRefSpellings is the set of `$ref` strings that name `target`. The
// pass reads a pointer out of the loader's own `bad data in "…"` report, which
// spells it with a leading `#`, while an artifact may write either spelling.
func confinementRefSpellings(target string) map[string]bool {
	fragment := strings.TrimPrefix(target, "#")
	return map[string]bool{target: true, fragment: true, "#" + fragment: true}
}

// confinementDenotes reports whether `node` is a Reference Object whose `$ref`
// names `target`. It is the check that makes the denotation exemption a
// property of the TREE rather than of the caller's intent; see
// `confinementInlineDenoted`.
func confinementDenotes(node any, target string) bool {
	nodeMap, isMap := node.(map[string]any)
	if !isMap {
		return false
	}
	ref, isString := nodeMap["$ref"].(string)
	if !isString {
		return false
	}
	return confinementRefSpellings(target)[ref]
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

// confinementInlineDenoted is the ONLY change this pass may make to the raw
// tree that is not authoring, and the exemption is checked at BOTH ends.
//
// The caller hands over the artifact's own POINTER, never a value, and this
// function resolves it -- so no minted value can enter through the signature.
// That half replaces a `confinementDenotation{why: …}` warrant struct which
// block 8h's first verification defeated in one line: any in-package caller
// could write `confinementDenotation{why: "no reason at all"}` and ship an
// arbitrary value past an admitting gate (record 104 §4). A warrant whose only
// content is prose at the call site verifies nothing.
//
// The signature half is not sufficient on its own, and the claim that it was is
// the third thing in this file a verifier has refuted by execution. "There is no
// signature through which a caller can put a MINTED value at a position under a
// denotation claim" is true; "the exemption cannot be borrowed by a mechanism
// that is not denoting" does not follow from it, because nothing in the
// signature says `site` has anything to do with `target`. Block 8h's second
// verification added one line to `confineEntryDocument` --
// `confinementInlineDenoted(tree, "#/info/title", "#/paths/~1survive/get/operationId", ledger)`
// -- and shipped `info.title = "getSurvive"` past an admitting gate, accounted
// as DENOTED (record 107 §6.3). Nothing was minted. A value the artifact owns
// was RELOCATED to a position the artifact never put it, which is a difference
// an emitter is never asked about.
//
// So the site is checked too: it must BE a Reference Object whose `$ref` names
// `target`. That is exactly what the exemption's justification asserts -- a JSON
// Reference DENOTES the value at its pointer, so replacing the reference by that
// value mints nothing -- and until now the justification was a property of the
// one call site rather than of the function. `confinementApplySeamC` satisfies
// it by construction: it selects the site through `confinementRefSites`, which
// finds sites by their `$ref` naming the target, and it inlines only a node of
// exactly one member. A relocation satisfies it never.
//
// What the exemption rests on, restated with the check in place: when a bare
// Reference Object is replaced by the value ITS OWN pointer names, nothing is
// minted and nothing moves that was not already denoted at that position, so
// there is nothing for an emitter to certify unreachable.
//
// The position is still recorded on the ledger as DENOTED, because the
// unledgered-change check at admission compares the shipped tree against the
// artifact's own and must be able to account for every difference. Denoted
// positions are exempt from the emission obligation, not from the accounting.
func confinementInlineDenoted(root any, site, target string, ledger *confinementLedger) bool {
	value, found := confinementResolveRaw(root, target)
	if !found {
		return false
	}
	node, resolved := confinementResolveRaw(root, site)
	if !resolved || !confinementDenotes(node, target) {
		return false
	}
	tokens, err := confinementParsePointer(site)
	if err != nil {
		return false
	}
	parent, last, ok := confinementResolveParent(root, tokens)
	if !ok {
		return false
	}
	switch p := parent.(type) {
	case map[string]any:
		ledger.denote(site)
		p[last.key] = value
		return true
	case []any:
		if last.index < 0 || last.index >= len(p) {
			return false
		}
		ledger.denote(site)
		p[last.index] = value
		return true
	}
	return false
}

// confinementRemoveAt removes a mapping member. It AUTHORS -- the container
// that remains is not what the artifact declared -- so it takes the ledger for
// the same reason `confinementNeutralize` does.
func confinementRemoveAt(root any, pointer string, ledger *confinementLedger) bool {
	tokens, err := confinementParsePointer(pointer)
	if err != nil {
		return false
	}
	parent, last, ok := confinementResolveParent(root, tokens)
	if !ok {
		return false
	}
	if p, isMap := parent.(map[string]any); isMap {
		ledger.author(pointer)
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
func confinementApplySeamC(root any, floor *acceptanceFloor, target, site string, ledger *confinementLedger) bool {
	if floor.ResponseMemberDefects[site] {
		return confinementRemoveAt(root, site, ledger)
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
		return confinementInlineDenoted(root, site, target, ledger)
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
//
// WHAT IS RECORDED IS THE SITE, NOT THE `$ref` MEMBER, and that distinction is
// load-bearing rather than cosmetic. Both are true statements about the tree --
// the value at the member is gone, and the value at the site is no longer what
// the artifact declared -- but only one of them gives the gate a position it can
// ask a question at.
//
// Recording `site + "/$ref"` puts every candidate where a `$ref` KEY's value
// goes, and kin reads only a STRING there. Five of the six candidates are
// objects or a sequence, which kin decodes into the containing object's
// `Extensions`; a Parameter Object's, a Response Object's, a Path Item's
// extensions do not reach emitted content, so those rounds report IDENTICAL
// whether or not anything reads the position -- inert by construction, not
// evidence of unreachability. The sixth, the scalar, is the only value kin reads
// at a `$ref`, and it makes the site a Reference Object denoting nothing, so the
// marked image does not load and the round is INCONCLUSIVE. The gate then marks
// the position shown off vacuous rounds and admits.
//
// That is not a hypothesis. Block 8h's second verification built it -- `n12`,
// stored beside `n10` -- and measured `690086c` shipping
// `getSurvive.input.properties.AUTHORED`, a name no reader of the artifact could
// obtain, past an ADMITTING gate with the unit `represented` and
// `exhaustive=true`, while both heads it superseded refused the same document
// (record 107 §1). The failure had the same shape as the one that made the
// ANCESTOR rule unsound: the false negative was systematically aligned with the
// rule rather than incidental.
//
// Recording the SITE puts the candidate AT the Reference Object's own position,
// so the two images differ by the presence of a whole member in a decoding
// context an emitter reads -- a Parameter Object slot, a Schema Object position
// -- rather than by an extension key kin drops. Nothing about the ladder, the
// oracle or the AND rule changes; the position does.
//
// Note what this does NOT close, so that a later block attacks it rather than
// inheriting it as settled: a `$ref` authored position reached by mechanism (a)
// rather than by this round -- a literal `$ref` KEY inside a Components map --
// still has the vacuity property, because its last token is `$ref` for a
// different reason. No harm is measured there, because a Components map emits
// nothing; the evidence is vacuous all the same.
func confinementNeutralizeURef(root any, site string, ledger *confinementLedger) bool {
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
	ledger.author(site)
	delete(nodeMap, "$ref")
	return true
}

// ---- the authoring ledger ----------------------------------------------------

// confinementLedger is this pass's record of every position at which it left a
// value the artifact did not write.
//
// It exists so that the emission rail attaches ONCE, AT THE ACT OF AUTHORING,
// rather than once per mechanism. Block 8g built the rail and wired it to one
// of the three mechanisms; the two it did not wire kept the ladder's
// attribution, which is the quantity both 8g parks refuted. A rail wired
// per-mechanism has to be re-wired for the fourth mechanism nobody has written
// yet, and the history of this file is that the thing nobody wired is the thing
// that ships a value the artifact never wrote.
//
// So the ledger is not a log and it is not optional. It is a required parameter
// of every function in this file that mutates the raw tree
// (`confinementNeutralize`, `confinementRemoveAt`, `confinementNeutralizeURef`),
// and the one non-authoring change has no signature that accepts a minted value
// at all (`confinementInlineDenoted`).
//
// WHAT THAT IS AND IS NOT AN OBLIGATION TO DO. Requiring a parameter binds only
// the code that calls these functions. Go has no way to stop a mechanism added
// later from writing `delete(m, k)` in a new function of its own, and block 8h's
// verification proved the point by doing exactly that: a fifth authoring act
// compiled cleanly and shipped an `info.title` the artifact never wrote, past an
// ADMITTING gate, because the gate read a ledger the act was not on (record 104
// §4). "A mechanism added later cannot author without holding a ledger" was a
// claim the language does not support, and it is not made here.
//
// What IS enforced, and is enforced over the tree rather than over the code, is
// `confinementUnledgeredDifference`: at admission the shipped tree is compared
// against a second parse of the artifact's own entry bytes, and EVERY difference
// must lie at or beneath a recorded position. A mechanism that authors without a
// ledger does not fail to compile; it fails to be ADMITTED, which is the
// property the pass actually needs. The check is mechanism-blind and asks
// nothing about which code made the change.
//
// WHAT AN ENTRY IS: the AUTHORED POSITION ITSELF, and nothing above it.
//
// An earlier design recorded the nearest ancestor the ladder had seen as a
// Schema Object -- a "markable anchor" -- on the reasoning that a schema's value
// is what an emitter carries verbatim, extensions included, so an emitter that
// emits nothing from the anchor emits nothing beneath it. The reasoning is
// sound; its instrument was not. Marking an ANCESTOR asks the emitter whether it
// carries an extension key at that ancestor's ROOT, and block 8h's verification
// measured one ordinary lane that does not: the whole-body input route rebuilds
// an operation's input from a request-body schema's MEMBERS, so the body
// schema's own root keys never reach `input`. The mark was carried at every one
// of that schema's descendants and dropped at its root -- and the anchor rule
// picks exactly that root for a removed `properties/<name>` member, so the false
// negative was systematically aligned with the rule rather than incidental
// (record 104 §1, reproducer `n10`).
//
// Marking AT the position removes the mismatch, because the question and the
// instrument become the same question. The pass authored at a position; the gate
// puts a distinguishable value AT that position and asks the emitter whether
// anything changed. A removed member is not an obstacle to this -- the position
// is exactly where the value goes back. What the shipped and marked images
// differ by is then the PRESENCE OF A WHOLE MEMBER, not the survival of an
// extension key at some ancestor's root, so no carriage assumption is being
// made about the mark at all.
//
// It still enumerates nothing. `confinementMarkCandidates` is a fixed ladder of
// four OAS object kinds, chosen because an emitter reads them on different
// routes, and which of them is legal at a position is decided by the pass's own
// unmarshal oracle in that position's decoding context -- never by a table of
// positions. The parks' channels -- a Parameter Object's `content` form, a
// Reference-Object success response, a Reference-Object request body, a second
// response media alternative -- appear nowhere in this file.
//
// AN AUTHORED POSITION AT WHICH THE ORACLE ACCEPTS NO CANDIDATE MAKES THE PASS
// DECLINE. That is the same judgement `openapi-client/go` already makes with a
// nil gate: a pass that cannot show its authored values are unreachable must not
// admit them.
type confinementLedger struct {
	authored map[string]bool
	// denoted holds positions changed under `confinementInlineDenoted`. They
	// carry no emission obligation -- the value that landed is the artifact's
	// own -- but the unledgered-difference check must still account for them.
	denoted map[string]bool
}

func newConfinementLedger() *confinementLedger {
	return &confinementLedger{authored: map[string]bool{}, denoted: map[string]bool{}}
}

// author records that the value at `pointer` is no longer what the artifact
// declared.
func (l *confinementLedger) author(pointer string) {
	if l == nil {
		return
	}
	l.authored[pointer] = true
}

// denote records a position changed under a denotation.
func (l *confinementLedger) denote(pointer string) {
	if l == nil {
		return
	}
	l.denoted[pointer] = true
}

func (l *confinementLedger) authoredNothing() bool {
	return l == nil || len(l.authored) == 0
}

func (l *confinementLedger) sortedAuthored() []string {
	if l == nil {
		return nil
	}
	return confinementSortedSites(l.authored)
}

// records reports whether `pointer` is at or beneath a position this ledger
// holds, in either list.
func (l *confinementLedger) records(pointer string) bool {
	if l == nil {
		return false
	}
	for _, set := range []map[string]bool{l.authored, l.denoted} {
		for site := range set {
			if pointer == site || strings.HasPrefix(pointer, site+"/") {
				return true
			}
		}
	}
	return false
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

// confinementMarkKey is the extension key every candidate value carries, inside
// a Schema Object, so that a value reaching emitted content is recognisable
// there. It is an extension key, so it is legal wherever the OAS admits
// specification extensions.
//
// The mark is NOT what the gate compares. What the two images differ by is the
// PRESENCE of a whole member at the authored position; the mark exists so that
// the difference cannot be one the artifact could have written itself, which is
// why `confinementAdmit` refuses outright when the shipped bytes already carry
// the key.
const confinementMarkKey = "x-openbindings-confinement-authored"

// confinementMarkValue is fixed rather than random so that two runs over the
// same artifact produce the same bytes.
const confinementMarkValue = "confinement-authored-position"

// confinementMarkCandidates is the fixed, position-blind ladder of values the
// gate places at an authored position.
//
// WHY MORE THAN ONE. A position's decoding context decides what an emitter can
// read from it, and a value of the wrong kind can be inert where a value of the
// right kind is read. An Operation Object carrying only an extension emits
// nothing an emitter must carry; a Response Object with content does. One
// candidate therefore proves nothing in general, and the first four span the
// object kinds this engine's emitter reads on DIFFERENT ROUTES: a Schema Object
// (schemas, properties, items, request and response bodies), a Parameter Object
// (the parameter lane, including its `content` form), a Response Object (the
// output lane, including success selection), and a Path Item (the unit lane).
//
// The last two are not OAS object kinds but JSON ones, and they are here
// because a position's decoding context is not always an object. The pass
// authors at Schema Object KEYWORDS -- `required` is a `[]string`, `enum` is a
// `[]any` -- and at such a position every object candidate is a decode error, so
// a ladder of objects alone accepts nothing and the pass declines for want of an
// instrument rather than for anything an emitter said. Block 8h's re-run
// measured that residue at three specimens and 16 operations (`kaldb`,
// `lancedb`, `rswag`), all four of `kaldb`'s positions and both of the others'
// being a `required` or an `enum`. A one-element marked sequence and a marked
// scalar close it, and the ladder's shape is unchanged: still fixed, still
// position-blind, still chosen by the oracle in the position's own context.
//
// WHY EVERY ACCEPTED ONE MUST AGREE. Block 8h's verification measured a
// first-accepted-wins ladder and found it UNSOUND on `Kong/kong`, where three of
// four accepted candidates report no emitted difference and the fourth reports
// one (record 104 §2.2). A ladder that stops at the first acceptance is a false
// negative generator. `confinementAdmit` therefore runs every candidate the
// oracle accepts and admits only if all of them agree.
//
// WHY NOT MORE. The set must stay small, fixed and position-blind, or it becomes
// the channel enumeration this whole chain exists to avoid. Adding a candidate
// is adding a ROUTE KIND, never a position.
func confinementMarkCandidates() []any {
	mark := func() map[string]any {
		return map[string]any{confinementMarkKey: confinementMarkValue}
	}
	body := func() map[string]any {
		return map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": mark()}}}
	}
	response := func() map[string]any {
		out := body()
		out["description"] = confinementMarkValue
		return out
	}
	return []any{
		mark(),
		map[string]any{"name": confinementMarkValue, "in": "query", "schema": mark()},
		response(),
		map[string]any{"get": map[string]any{"responses": map[string]any{"200": response()}}},
		[]any{confinementMarkValue},
		confinementMarkValue,
	}
}

// confinementPlacement remembers what stood at a position so that a candidate
// can be withdrawn exactly, leaving the shipped tree byte-identical to what it
// was. A removed member is restored as removed.
type confinementPlacement struct {
	parent  any
	last    confinementToken
	existed bool
	prior   any
}

// confinementTokensAt parses a pointer and resolves each token's container kind
// against the live tree, including the last one, so that
// `confinementSkeleton` rebuilds the position's true decoding context.
func confinementTokensAt(root any, pointer string) ([]confinementToken, any, confinementToken, bool) {
	tokens, err := confinementParsePointer(pointer)
	if err != nil {
		return nil, nil, confinementToken{}, false
	}
	parent, last, ok := confinementResolveParent(root, tokens)
	if !ok {
		return nil, nil, confinementToken{}, false
	}
	tokens[len(tokens)-1] = last
	return tokens, parent, last, true
}

// confinementCandidatesAt returns the candidates the pass's own unmarshal oracle
// accepts in this position's decoding context. It asks the oracle over
// `confinementSkeleton` -- the container chain alone -- which is the same
// instrument `confinementNeutralize` already uses to decide what a position may
// hold, and it decides nothing itself.
func confinementCandidatesAt(root any, pointer string) []any {
	tokens, _, _, ok := confinementTokensAt(root, pointer)
	if !ok {
		return nil
	}
	var out []any
	for _, candidate := range confinementMarkCandidates() {
		if confinementProbe(confinementSkeleton(tokens, candidate)) == nil {
			out = append(out, candidate)
		}
	}
	return out
}

func confinementPlaceAt(root any, pointer string, value any) (confinementPlacement, bool) {
	_, parent, last, ok := confinementTokensAt(root, pointer)
	if !ok {
		return confinementPlacement{}, false
	}
	switch p := parent.(type) {
	case map[string]any:
		prior, existed := p[last.key]
		p[last.key] = value
		return confinementPlacement{parent: parent, last: last, existed: existed, prior: prior}, true
	case []any:
		if last.index < 0 || last.index >= len(p) {
			return confinementPlacement{}, false
		}
		prior := p[last.index]
		p[last.index] = value
		return confinementPlacement{parent: parent, last: last, existed: true, prior: prior}, true
	}
	return confinementPlacement{}, false
}

func (p confinementPlacement) withdraw() {
	switch parent := p.parent.(type) {
	case map[string]any:
		if p.existed {
			parent[p.last.key] = p.prior
			return
		}
		delete(parent, p.last.key)
	case []any:
		if p.last.index >= 0 && p.last.index < len(parent) {
			parent[p.last.index] = p.prior
		}
	}
}

// confinementDifferences returns every position at which two raw trees differ,
// stopping the descent at each difference. A position present on one side and
// absent on the other is a difference AT that position.
func confinementDifferences(original, confined any) []string {
	var out []string
	var walk func(path []confinementToken, a, b any)
	walk = func(path []confinementToken, a, b any) {
		am, aIsMap := a.(map[string]any)
		bm, bIsMap := b.(map[string]any)
		if aIsMap && bIsMap {
			keys := map[string]bool{}
			for k := range am {
				keys[k] = true
			}
			for k := range bm {
				keys[k] = true
			}
			names := make([]string, 0, len(keys))
			for k := range keys {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, k := range names {
				child := append(append([]confinementToken{}, path...), confinementToken{key: k})
				av, aHas := am[k]
				bv, bHas := bm[k]
				if aHas != bHas {
					out = append(out, confinementPointerOf(child))
					continue
				}
				walk(child, av, bv)
			}
			return
		}
		as, aIsSeq := a.([]any)
		bs, bIsSeq := b.([]any)
		if aIsSeq && bIsSeq {
			if len(as) != len(bs) {
				out = append(out, confinementPointerOf(path))
				return
			}
			for i := range as {
				child := append(append([]confinementToken{}, path...), confinementToken{index: i, inArray: true})
				walk(child, as[i], bs[i])
			}
			return
		}
		if !reflect.DeepEqual(a, b) {
			out = append(out, confinementPointerOf(path))
		}
	}
	walk(nil, original, confined)
	return out
}

// confinementUnledgeredDifference reports the first position at which the tree
// this pass is about to hand back differs from the artifact's own, and which no
// ledger entry accounts for.
//
// This is the property that a required ledger parameter cannot supply. A
// parameter binds the callers of three functions; this binds the TREE. A fifth
// mechanism that reaches into the raw tree with its own `delete` -- which Go
// will always let it do -- authors a difference that no entry covers, and the
// pass declines instead of shipping it. Block 8h's verification shipped exactly
// such an act past an admitting gate (record 104 §4); it does not pass here.
//
// It compares against a SECOND PARSE of the entry bytes rather than a copy taken
// earlier, so nothing the mechanisms did can have reached the reference image.
//
// OVER TREE MUTATIONS IT IS TOTAL, and that was established by execution rather
// than by reading: `confinementDifferences` covers presence asymmetry in a map,
// length change in a sequence, elementwise descent, and `reflect.DeepEqual` for
// every remaining pair including map-against-sequence and scalar-against-
// container; both sides come from the same `parseRawResource` over the same
// bytes, so nothing can be smoothed between them; and the check runs BEFORE the
// gate's rounds, over the bytes that are marshaled and handed back. Two
// verifications built a fifth authoring act that reaches into the raw tree with
// the language's own map operations, and both times the pass declined (record
// 106 §7, record 107 §5.1).
//
// FOUR THINGS IT IS NOT TOTAL OVER, none of them a mutation of the tree, all
// four named here so a later block attacks them rather than rediscovering them:
//
//  1. The parse/serialize ROUND TRIP is on the wrong side of the comparison.
//     Both sides are parsed trees, so anything introduced between the tree and
//     the shipped bytes -- scalar canonicalization, a YAML 1.1-against-1.2
//     scalar reading, a number's representation -- is invisible here by
//     construction. The claim this check supports is about trees; the bytes are
//     not the tree.
//  2. `records` matches a PREFIX, so a ledgered site blankets everything beneath
//     it. Harmless while every recorded site is a removed member with nothing
//     beneath it, which is the case today, but it is a prefix and not a
//     position.
//  3. NON-TREE CHANNELS are outside it entirely. A future mechanism that writes
//     into lane state -- `schemaOverlays` and its kind -- rather than into the
//     tree authors nothing this check can see.
//  4. The DENOTED exemption. Narrowed by `confinementInlineDenoted`'s
//     site-denotes-target check, which is what stops it from being borrowed to
//     relocate a value, but it remains an exemption: a position on the denoted
//     list is accounted without being put to the emitter.
func confinementUnledgeredDifference(entry []byte, confined any, ledger *confinementLedger) (string, bool) {
	original, parsed := parseRawResource(entry)
	if !parsed {
		return "#", true
	}
	for _, pointer := range confinementDifferences(original, confined) {
		if !ledger.records(pointer) {
			return pointer, true
		}
	}
	return "", false
}

// confinementAdmit is the pass's ONE admission point. Every exit that hands
// back a document goes through it, whatever mechanism produced that document
// and whatever mechanisms authored along the way; it never asks which one did.
// It returns the pass's three results directly: the document to hand back, or a
// decline that keeps the loader's original error.
//
// `loaded` is the document the caller has already loaded from the confined
// tree.
//
// Three obligations, in order:
//
//  1. ACCOUNTING. Every difference between the artifact's own tree and the tree
//     about to be handed back must lie at or beneath a recorded position. This
//     runs even when the ledger is EMPTY, because an empty ledger is exactly the
//     state a mechanism that forgot to record leaves behind.
//
//  2. PROVABILITY. At every authored position the oracle must accept at least
//     one candidate. At a position where it accepts none the pass cannot ask the
//     emitter anything, so it declines rather than guess.
//
//  3. EMISSION. For every candidate the oracle accepts, load a marked image and
//     ask the gate whether it emits identically to the shipped one. EVERY
//     accepted candidate must agree. The shipped image is loaded LAST so that
//     every piece of lane state the surrounding load collected describes the
//     document this pass returns.
//
// A confinement that authored NOTHING skips 2 and 3: there is nothing for an
// emitter to certify, and paying two more loads to compare a document against
// itself would be a cost with no question behind it. It does not skip 1.
func confinementAdmit(entry []byte, tree any, ledger *confinementLedger, floor *acceptanceFloor,
	reload func([]byte) (*openapi3.T, error), gate confinementEmissionGate, loaded *openapi3.T) (*openapi3.T, error, bool) {
	if _, unaccounted := confinementUnledgeredDifference(entry, tree, ledger); unaccounted {
		return nil, nil, false
	}
	if ledger.authoredNothing() {
		return loaded, nil, true
	}
	if gate == nil {
		return nil, nil, false
	}
	authored := ledger.sortedAuthored()
	shippedData, err := json.Marshal(tree)
	if err != nil {
		return nil, nil, false
	}
	// A mark the artifact could have written itself would not discriminate.
	// Both spellings are checked: the object candidates are recognised by the
	// key, the sequence and scalar candidates only by the value.
	if strings.Contains(string(shippedData), confinementMarkKey) ||
		strings.Contains(string(shippedData), confinementMarkValue) {
		return nil, nil, false
	}

	// Obligation 2, and the widest accepted set decides how many rounds
	// obligation 3 needs.
	rounds := 0
	available := make(map[string][]any, len(authored))
	for _, site := range authored {
		candidates := confinementCandidatesAt(tree, site)
		if len(candidates) == 0 {
			return nil, nil, false
		}
		available[site] = candidates
		if len(candidates) > rounds {
			rounds = len(candidates)
		}
	}

	// Obligation 3. Every authored position is marked in every round, so a
	// difference anywhere refuses and an identical round clears every position
	// the round covered. Rounds are indexed so that after `rounds` of them every
	// (position, accepted candidate) pair has been put to the emitter at least
	// once; a position whose accepted set is shorter repeats its first.
	//
	// A round whose MARKED image does not load is INCONCLUSIVE: a document that
	// does not load emits nothing, so there is no emission to compare and the
	// round is evidence in neither direction. It does not refuse, and it does
	// not clear anything -- `shown` is what records the difference, and a
	// position no conclusive round covered declines below. The case is real
	// rather than hypothetical: the scalar candidate placed at a `$ref` member
	// makes the site a Reference Object denoting nothing, and treating that
	// mechanical failure as a refusal cost three specimens and 114 operations
	// when it was measured.
	//
	// THE RESIDUAL THIS LEAVES, stated so a later block attacks it: a round is
	// discarded WHOLE, so one position's unloadable candidate suppresses the
	// evidence of every other position's candidate at the same round index. A
	// construction with positions P and Q where P's DIFFERING candidate and Q's
	// UNLOADABLE candidate share an index would be admitted here and refused
	// under per-position isolation. It is structurally real and has zero measured
	// incidence: over the 260-artifact corpus four specimens have an inconclusive
	// round and in every one of them the ladder is aligned, so the scalar is
	// unloadable at all of that specimen's positions at once, and no position is
	// left uncovered by a conclusive round (record 107 §1.6). Per-position
	// marking would close it and would cost a load per position per round rather
	// than a load per round; that trade has not been measured and is not made
	// here.
	// The shipped image is the same bytes in every round, so it is loaded ONCE
	// for the comparison. `reload` resets the surrounding lane state and hands
	// back a fresh document without touching an earlier one, and the gate reads
	// no lane state (it passes nil overlays), so one shipped document serves
	// every round. It is loaded AGAIN at the exit, last, so that the state the
	// surrounding load collected describes the document this pass returns --
	// which is why this one is not simply reused as the return value.
	comparisonDoc, comparisonErr := reload(shippedData)
	if comparisonErr != nil {
		return nil, nil, false
	}
	shown := make(map[string]bool, len(authored))
	for round := 0; round < rounds; round++ {
		placements := make([]confinementPlacement, 0, len(authored))
		covered := make([]string, 0, len(authored))
		placed := true
		for _, site := range authored {
			candidates := available[site]
			pick := candidates[0]
			if round < len(candidates) {
				pick = candidates[round]
			}
			placement, ok := confinementPlaceAt(tree, site, pick)
			if !ok {
				placed = false
				break
			}
			placements = append(placements, placement)
			covered = append(covered, site)
		}
		markedData, markErr := json.Marshal(tree)
		for i := len(placements) - 1; i >= 0; i-- {
			placements[i].withdraw()
		}
		if !placed || markErr != nil {
			return nil, nil, false
		}
		markedDoc, markedLoadErr := reload(markedData)
		if markedLoadErr != nil {
			continue
		}
		if !gate(comparisonDoc, markedDoc, floor) {
			return nil, nil, false
		}
		for _, site := range covered {
			shown[site] = true
		}
	}
	for _, site := range authored {
		if !shown[site] {
			return nil, nil, false
		}
	}

	shippedDoc, shippedLoadErr := reload(shippedData)
	if shippedLoadErr != nil {
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
// `gate` is this engine's emission rail; see `confinementEmissionGate`. EVERY
// mechanism is now held to it, because every mechanism that authors registers
// with the same `confinementLedger` and every exit that hands back a document
// goes through the same `confinementAdmit`. There is no per-mechanism rail left
// in this file to get out of step.
//
// What holds a mechanism added LATER is not the ledger parameter -- Go cannot
// make that binding, and a verifier proved it by writing a fifth authoring act
// that compiled cleanly (record 104 §4). It is `confinementUnledgeredDifference`
// at admission, which compares the tree about to be handed back against a second
// parse of the artifact's own bytes and declines on any difference no entry
// covers. A mechanism that authors without a ledger does not fail to compile; it
// fails to be admitted.
//
// One exit does NOT reach that check: the seam-C loop returns
// `(nil, loadErr, true)` when the load error stops matching
// `confinementBadData`. No document is handed back on that path, so nothing
// authored can ship through it -- but the error text it reports is derived from a
// confined tree, and the accounting is never consulted before reporting it.
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
	ledger := newConfinementLedger()

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
		if !confinementNeutralize(tree, pointer, ledger) {
			return nil, nil, false
		}
	}

	data, err := json.Marshal(tree)
	if err != nil {
		return nil, nil, false
	}
	doc, loadErr := reload(data)
	if loadErr == nil {
		return confinementAdmit(entry, tree, ledger, floor, reload, gate, doc)
	}

	// (c) the URef round. Reference RESOLUTION failures never reach the
	// unmarshal oracle -- kin accepts `{"$ref": "#/x"}` and then fails while
	// resolving it -- and they carry no `bad data in "…"` report, so neither
	// earlier mechanism can see them. The ladder already does: every
	// unresolvable internal reference is a URef defect at its referencing
	// site. The climbing ones are neutralised here, once, together.
	if len(floor.ClimbingURefSites) > 0 {
		for _, site := range confinementSortedSites(floor.ClimbingURefSites) {
			if !confinementNeutralizeURef(tree, site, ledger) {
				return nil, nil, false
			}
		}
		if data, err = json.Marshal(tree); err != nil {
			return nil, nil, false
		}
		if doc, loadErr = reload(data); loadErr == nil {
			return confinementAdmit(entry, tree, ledger, floor, reload, gate, doc)
		}
	}

	// (b) seam-C rounds. Whatever earlier mechanisms authored is still in the
	// tree a seam-C round hands back, and seam C authors on its own account when
	// it removes a D7 response member -- so this exit is the same admission
	// point as the other two, reached with the same ledger.
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
			if !confinementApplySeamC(tree, floor, match[1], site, ledger) {
				return nil, nil, false
			}
		}
		data, err = json.Marshal(tree)
		if err != nil {
			return nil, nil, false
		}
		doc, loadErr = reload(data)
		if loadErr == nil {
			return confinementAdmit(entry, tree, ledger, floor, reload, gate, doc)
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
