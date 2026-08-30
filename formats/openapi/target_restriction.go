package openapi

// Per-target restriction (Round R), the last fallback on the 3.0/3.1 load path.
//
// THIS FILE IS A TWIN. The same source, package clause aside, ships in
// `openapi-client/go` and in `openbindings-go/formats/openapi`, because the
// adapter deliberately keeps the 3.0/3.1 engines on its own loader (see
// `invoker.go`: "Keep the ratified 3.0/3.1 engines on their established
// loader"). A change here lands in both or in neither. It is written against
// plain strings and the acceptance floor's own inventory precisely so that it
// can be: it names no engine type either port would have to translate.
//
// THE PROBLEM IT SOLVES. kin-openapi's typed unmarshal decodes the WHOLE entry
// document at once, so a value it cannot represent anywhere in the document
// refuses the whole load -- and a Response Object under one operation is
// exactly such a value: `{"200": "ok"}`, `description: 123`, `headers: "nope"`,
// `content: "application/json"`, `links: "nope"`, a `headers` member that is
// not a Header Object. Every accepted edition's §3.2 says the opposite in the
// same words:
//
//	"The load gates are the following closed ordered set: accepted-representation
//	 grammar, scalar/tag/key resolution, JSON-object root shape, and exact
//	 edition discrimination; no condition outside this set is a load gate."
//	"After those gates pass, a defect confines to the smallest selected unit
//	 that owns it; an unreachable defect destroys no target, and a whole source
//	 refuses only when every position that could contain an addressable target
//	 is defective so that no conformant selector can resolve."
//
// A defect confined to one Response Object under one operation must therefore
// cost that operation and nothing else, and the acceptance floor already says
// so per unit -- it just never gets asked, because the load dies first.
//
// THE MECHANISM, AND WHY IT IS THE RATIFIED ONE. This is the OpenAPI 3.2 lane's
// `buildOpenAPI32Fallback` (openapi32_confinement.go), generalized to the 3.0
// and 3.1 lanes: load every raw operation IN ISOLATION, in an image holding
// only that operation, its Path Item's own fields, the root's document-level
// fields, and the components its closure may compose. Round R measured the same
// document, the same defect and the same kin error on both lanes:
//
//	3.2  #/paths/~1broken/get -> excluded ("selected operation closure is
//	                             unresolvable: ... cannot unmarshal string into
//	                             field Response.headers")
//	     #/paths/~1intact/get -> resolves
//	3.1  whole source -> SOURCE_LOAD_FAILED, both targets gone
//
// so the ruled behaviour was already shipping one edition over.
//
// WHY IT NEEDS NO EMISSION GATE, unlike the confinement pass next door. This
// pass NEVER AUTHORS. Every image is a RESTRICTION of the artifact's own raw
// tree: values are copied, never minted, never relocated, never removed from a
// container another unit reads out of. The emitter for target T reads T's own
// image, which holds exactly what T composes; a sibling's defect is not in it
// and cannot reach it. And where T's own closure genuinely does need the
// defective value -- a schema-position `$ref` to a Response Object, the case
// the shared case table carries as C4 -- the defect IS in T's image, T's load
// fails, and T is excluded. That is the correct outcome, decided per target
// instead of per document, and it is why `confinementEmissionGate`'s question
// ("does anything the pass authored reach emitted content?") has no instance
// here: nothing is authored, so there is nothing for an emitter to certify.
// Do not add a mechanism to this file that mints or moves a value; it would
// need the gate this one is exempt from, and this engine has no gate to give.
//
// WHAT IT IS ALLOWED TO FIX, AND WHY THE LIMIT IS NOT OPTIONAL. This pass is
// admitted ONLY when every operation whose isolated image fails to load is one
// the ladder ALREADY marks invalid over a climbing RESPONSE OBJECT defect
// (D7, D9 or D16). Under that condition the pass decides no exclusion of its
// own: every target it drops was excluded by the acceptance floor, over the
// artifact's raw image, for a stated reason, and all this pass does is stop
// kin-openapi's inability to represent an already-excluded target from killing
// that target's siblings. That is Round R's ruling exactly, and nothing wider.
//
// The limit was MEASURED, not assumed. Without it this pass is a general
// load-failure salvage, and Round R built that first: it turned ten shipped
// safety cases red, and one family of them is the reason the limit is stated
// this way rather than loosened. In
// `TestConfinement_URefEmissionReachableSitesAreNeverConfined` a surviving
// operation READS a schema carrying a dangling reference through a channel the
// ladder's closure walk does not visit -- a Parameter Object's `content` form, a
// success response that is a Reference Object, a `requestBody` that is a
// Reference Object -- so the LADDER DOES NOT KNOW that operation is defective,
// and the whole-source refusal is the only thing standing between it and
// emitting from a reference that resolves to nothing. A general salvage removes
// that protection while the ladder is still blind. Nothing about a Response
// Object defect makes the ladder blind in the same way, which is why this pass
// may fix that class and must not reach past it. Widening this predicate is a
// round of its own, with its own safety work; do not do it here.
//
// WHERE IT SITS, AND THE LINE IT KEEPS. It runs LAST: after the typed load has
// failed AND after the confinement pass has declined. §3.2's four load gates all
// run before it -- accepted-representation grammar and scalar/tag/key resolution
// in `parseRawResource`, JSON-object root shape and exact edition discrimination
// in the loader's own gate and in `computeAcceptanceFloor`, which answers
// nothing for an edition it does not accept -- so a genuinely unparseable
// document (broken JSON/YAML, a non-object root, a failed edition gate) never
// reaches this code and still refuses at load, unchanged. No document that loads
// today, and none the confinement pass confines today, reaches it either. The
// only documents whose behaviour changes are those that today die with a
// whole-source refusal, and the worst case for them is that they still do: if no
// operation's image loads, no addressable target remains and §3.2's own part-2
// refusal fires, which is that rule rather than an exception to it.

import "encoding/json"

// restrictedResponseDefectClasses are the climbing Response Object defect
// classes: the member is not a Response Object at all (D7), it omits its
// REQUIRED `description` while declaring content (D9), or it violates another
// of the Response Object's fixed-field constraints (D16). They are the whole of
// what Round R ruled on, and therefore the whole of what this pass may act on.
var restrictedResponseDefectClasses = map[string]bool{floorD7: true, floorD9: true, floorD16: true}

// restrictedResponseDefectiveTargets is the set of operations the ladder marks
// invalid over a climbing Response Object defect.
func restrictedResponseDefectiveTargets(floor *acceptanceFloor) map[string]bool {
	out := map[string]bool{}
	if floor == nil {
		return out
	}
	for ref, op := range floor.Ops {
		if op == nil || op.Disposition != "invalid" {
			continue
		}
		for _, defect := range op.Defects {
			if restrictedResponseDefectClasses[defect.Class] {
				out[ref] = true
				break
			}
		}
	}
	return out
}

// restrictedOperationImage renders one operation's isolated image.
//
// Everything it writes is COPIED from the artifact's own raw tree; nothing is
// minted. The document-level fields are the ones a target's own composition
// reads (servers, security, the info the edition gate gauges) plus the edition
// discriminator itself, which the reload's own gate re-checks.
//
// THE COMPONENTS ARE ALWAYS CARRIED WHOLE, and that is the soundness argument.
// The ONLY thing an image omits is OTHER OPERATIONS' Operation Objects. Every
// position a reference could reach through the components map is still present
// and still exactly as the artifact wrote it, so an operation whose closure
// composes a DEFECTIVE component still fails here and is still excluded --
// which is the correct answer, not a cost.
//
// The 3.2 lane prunes components on a second attempt; this pass deliberately
// does not, and the difference is measured rather than stylistic. Round R built
// the pruning attempt first and it made four shipped safety cases go green in
// the wrong direction: in `TestConfinement_URefEmissionReachableSitesAreNeverConfined`
// a surviving unit READS a schema carrying a dangling reference, and the pruned
// image -- with the whole components map gone -- loaded, so a target that must
// be excluded was kept. A restriction that removes something a surviving unit
// reads is no longer only a restriction. Do not reintroduce it here.
//
// The tree is READ, never written: the assembled image holds references to the
// artifact's own values and is serialized immediately, so no copy is needed and
// no caller's tree is disturbed.
func restrictedOperationImage(root map[string]any, path, method string) ([]byte, bool) {
	paths, _ := root["paths"].(map[string]any)
	rawPathItem, _ := paths[path].(map[string]any)
	if rawPathItem == nil {
		return nil, false
	}
	rawOperation, present := rawPathItem[method]
	if !present {
		return nil, false
	}
	pathItem := restrictedPathItemFields(rawPathItem)
	pathItem[method] = rawOperation

	image := restrictedDocumentFields(root)
	image["paths"] = map[string]any{path: pathItem}
	if components, ok := root["components"]; ok {
		image["components"] = components
	}
	data, err := json.Marshal(image)
	if err != nil {
		return nil, false
	}
	return data, true
}

// restrictedSurvivingImage renders ONE image holding every operation whose own
// isolated image loads, and returns its bytes.
//
// It exists because not every consumer of a load can carry a document per
// target. The standalone engine can -- `Artifact.operationTargets` is exactly
// that map, and `ResolveOperation` consults it -- but the adapter's 3.0/3.1
// loader hands back a single document that its synthesis walks and its
// invocation resolves against, so the restriction it needs is the artifact
// minus the operations that cannot be represented, not a document per
// operation.
//
// It returns BYTES rather than a loaded document so the caller loads them
// through its OWN entry point with its own lane state: an adapter that collects
// a raw schema overlay while loading must collect it from the image it is going
// to hand back, not from a load this function ran for its own purposes.
// `probe` is asked one question only, per operation: does this isolated image
// load at all?
//
// A false result means "no better answer than the caller already has", and the
// caller keeps its original error. That includes the case where every operation
// loads in isolation: the load then failed somewhere no operation owns, and the
// artifact's own error is still the honest one to report.
func restrictedSurvivingImage(root map[string]any, floor *acceptanceFloor, probe func([]byte) error) ([]byte, bool) {
	if root == nil || floor == nil || probe == nil {
		return nil, false
	}
	if paths, _ := root["paths"].(map[string]any); paths == nil {
		return nil, false
	}
	admitted := restrictedResponseDefectiveTargets(floor)
	surviving := map[string]any{}
	excluded := 0
	for _, ref := range floor.OpOrder {
		op := floor.Ops[ref]
		if op == nil {
			continue
		}
		image, rendered := restrictedOperationImage(root, op.Path, op.Method)
		if !rendered || probe(image) != nil {
			if !admitted[ref] {
				// A failure this pass has no ruling about. It decides nothing
				// here and hands the whole question back.
				return nil, false
			}
			excluded++
			continue
		}
		item, _ := surviving[op.Path].(map[string]any)
		if item == nil {
			paths, _ := root["paths"].(map[string]any)
			rawPathItem, _ := paths[op.Path].(map[string]any)
			item = restrictedPathItemFields(rawPathItem)
			surviving[op.Path] = item
		}
		paths, _ := root["paths"].(map[string]any)
		if rawPathItem, _ := paths[op.Path].(map[string]any); rawPathItem != nil {
			if value, ok := rawPathItem[op.Method]; ok {
				item[op.Method] = value
			}
		}
	}
	if excluded == 0 {
		return nil, false
	}
	image := restrictedDocumentFields(root)
	image["paths"] = surviving
	if components, ok := root["components"]; ok {
		image["components"] = components
	}
	data, err := json.Marshal(image)
	if err != nil {
		return nil, false
	}
	return data, true
}

// restrictedDocumentFields copies the root members every restricted image
// carries: the edition discriminator, and the document-level declarations a
// target's own composition reads.
func restrictedDocumentFields(root map[string]any) map[string]any {
	image := map[string]any{}
	for _, field := range []string{"openapi", "info", "jsonSchemaDialect", "servers", "security", "tags", "externalDocs"} {
		if value, ok := root[field]; ok {
			image[field] = value
		}
	}
	return image
}

// restrictedPathItemFields copies a Path Item's own members -- the ones its
// operations compose with -- without any operation.
func restrictedPathItemFields(rawPathItem map[string]any) map[string]any {
	item := map[string]any{}
	if rawPathItem == nil {
		return item
	}
	for _, field := range []string{"summary", "description", "parameters", "servers", "$ref"} {
		if value, ok := rawPathItem[field]; ok {
			item[field] = value
		}
	}
	return item
}
