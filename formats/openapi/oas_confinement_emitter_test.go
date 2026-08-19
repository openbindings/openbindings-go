package openapi

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	openbindings "github.com/openbindings/openbindings-go"
)

// confinementEmitterProbe is one document with one authorial presence fact
// spliced into a schema the emitter reaches.
const confinementEmitterProbe = `{"openapi":"3.0.3","info":{"title":"T","version":"1"},` +
	`"paths":{"/x":{"get":{"operationId":"getX","responses":{"200":{"description":"ok",` +
	`"content":{"application/json":{"schema":{"type":"object",` +
	`"properties":{"a":{"type":"string"%s}}}}}}}}}}}`

// confinementEmitterAttempt is one complete shipped load into the collector it
// is handed -- the same shape `loadDocumentWithResolverEntry`'s `attempt` has,
// and the argument `newIsolatedEmissionLoad` takes.
func confinementEmitterAttempt(data []byte, overlays *rawSchemaOverlayCollector) (*openapi3.T, error) {
	return loadDocumentWithResolverInternal(context.Background(), nil, "",
		openbindings.TextContent(string(data)), overlays)
}

// shippingEmission is the emission function that SHIPS: the gate's own
// `newIsolatedEmissionLoad` -- which is where `bindDocument` lives -- followed
// by the external-closure internalization and the conversion WITH those
// overlays, exactly as `invoker.go` spells it.
func shippingEmission(t *testing.T, text string) []byte {
	t.Helper()
	ctx := context.Background()
	doc, overlays, err := newIsolatedEmissionLoad(confinementEmitterAttempt)([]byte(text))
	if err != nil {
		t.Fatalf("the probe must load: %v", err)
	}
	overlays.setExternalComponents(internalizeExternalRefs(ctx, doc))
	image, err := emittedImage(doc, "", BindingSpec, overlays, nil)
	if err != nil {
		t.Fatalf("the probe must emit: %v", err)
	}
	return image
}

// supersededEmission is the emission function the gate used to run, restored
// here so the two can be compared: the collector filled by the load but NEVER
// BOUND -- `reload` was `schemaOverlays.reset(); attempt(data)`, with no
// `bindDocument` anywhere -- and the conversion with overlays NIL.
func supersededEmission(t *testing.T, text string) []byte {
	t.Helper()
	ctx := context.Background()
	doc, err := loadDocumentWithResolverInternal(ctx, nil, "", openbindings.TextContent(text), newRawSchemaOverlayCollector())
	if err != nil {
		t.Fatalf("the probe must load: %v", err)
	}
	image, err := emittedImage(doc, "", BindingSpec, nil, nil)
	if err != nil {
		t.Fatalf("the probe must emit: %v", err)
	}
	return image
}

// THE GATE'S EMITTER IS THE SHIPPING EMITTER, and the gap it used to leave is
// measured here rather than argued about.
//
// Record 112 §6 ground 1, the ground its author called the hardest of the four
// and the one still genuinely open: the gate built both comparison images with
// `schemaOverlays` nil while the shipped interface is built with them, so the
// closure property it certified was a property of a strictly narrower function
// than the one that ships. The defence was a sentence in a comment -- "overlays
// only ADD members to a schema the emitter already carries, so their absence
// can hide no mark" -- and one 214/214 measurement with a disclosed instrument
// caveat. Neither the block that shipped it nor the verifier that ruled on it
// could construct an exploit.
//
// THE EXPLOIT EXISTS AND THIS IS IT. The raw schema overlay collector exists
// precisely to carry the authorial presence facts kin-openapi's typed model
// erases (`schema_overlay.go`'s `erasedSchemaPresence`), and they only reach
// emitted content once `bindDocument` has paired them with typed identity --
// which the gate's loads never did. So a difference spelled in ANY of them was
// invisible to the gate and visible to what ships: the old gate would have
// certified such a position UNREACHABLE and the difference would have shipped.
//
// The table below is that measurement. Each row is one fact spliced into a
// schema the emitter reaches; the assertion is that the SHIPPING emitter sees
// it and the SUPERSEDED one does not.
func TestSynthesisEmissionGate_TheSupersededEmitterWasBlindToTheOverlay(t *testing.T) {
	baseShipping := shippingEmission(t, fmt.Sprintf(confinementEmitterProbe, ""))
	baseSuperseded := supersededEmission(t, fmt.Sprintf(confinementEmitterProbe, ""))

	// Every one of these is a fact `erasedSchemaPresence` names, at a schema
	// position the emitter reaches.
	blind := []struct{ name, fragment string }{
		{"title", `,"title":""`},
		{"description", `,"description":""`},
		{"format", `,"format":""`},
		{"pattern", `,"pattern":""`},
		{"default", `,"default":null`},
		{"example", `,"example":null`},
		{"enum", `,"enum":[]`},
		{"minLength", `,"minLength":0`},
		{"uniqueItems", `,"uniqueItems":false`},
		{"readOnly", `,"readOnly":false`},
		{"deprecated", `,"deprecated":false`},
	}
	for _, probe := range blind {
		text := fmt.Sprintf(confinementEmitterProbe, probe.fragment)
		if bytes.Equal(shippingEmission(t, text), baseShipping) {
			t.Fatalf("%s: the SHIPPING emitter must carry this fact into emitted content; "+
				"if it no longer does, this row is no longer a member of the class and must be removed with its reason", probe.name)
		}
		if !bytes.Equal(supersededEmission(t, text), baseSuperseded) {
			t.Fatalf("%s: the SUPERSEDED emitter is not blind to this fact, so it does not demonstrate the gap", probe.name)
		}
	}

	// The control that keeps the row above from being a property of splicing
	// anything at all: the mark the pass ACTUALLY places is an `x-` extension,
	// and BOTH emitters carry it. That is why the gap costs the corpus nothing
	// today and why it was never going to be found by running the corpus.
	marked := fmt.Sprintf(confinementEmitterProbe,
		`,"`+confinementMarkKey+`":"`+confinementMarkValue+`"`)
	if bytes.Equal(shippingEmission(t, marked), baseShipping) ||
		bytes.Equal(supersededEmission(t, marked), baseSuperseded) {
		t.Fatalf("the pass's own mark must be visible to BOTH emitters; the corpus-cost argument depends on it")
	}
}

// THE GATE ITSELF, driven end to end, on a difference only the shipping
// emission function carries. Red if `synthesisEmissionGate` stops running that
// function -- passing nil overlays to `emittedImage`, or dropping
// `bindDocument` from its isolated load, each reddens the IDENTICAL assertion.
func TestSynthesisEmissionGate_ComparesUnderTheShippingEmissionFunction(t *testing.T) {
	ctx := context.Background()
	// The gate's own isolated load, which is the SHIPPED one:
	// `newIsolatedEmissionLoad` is the function the shipped path hands to
	// `synthesisEmissionGate`, and it is where `bindDocument` lives.
	gate := synthesisEmissionGate(ctx, "", confinementEmitterAttempt)

	shipped := []byte(fmt.Sprintf(confinementEmitterProbe, ""))
	marked := []byte(fmt.Sprintf(confinementEmitterProbe, `,"title":""`))

	identical, conclusive := gate(shipped, marked, nil)
	if !conclusive {
		t.Fatalf("both images load, so the round must be conclusive")
	}
	if identical {
		t.Fatalf("the gate reported IDENTICAL on a difference the SHIPPED emitter carries into emitted content: " +
			"the gate is not running the shipping emission function")
	}

	// Anti-vacuity: a gate that always refuses would pass the assertion above.
	if identical, conclusive = gate(shipped, shipped, nil); !conclusive || !identical {
		t.Fatalf("a gate that cannot report IDENTICAL proves nothing; identical=%v conclusive=%v", identical, conclusive)
	}

	// An image that does not load is INCONCLUSIVE, not a refusal, and that rule
	// moved inside the gate with the load.
	if _, conclusive = gate(shipped, []byte(`{"openapi":"3.0.3","paths":`), nil); conclusive {
		t.Fatalf("a marked image that does not load must be inconclusive")
	}

	// A SHIPPED image this engine cannot load is CONCLUSIVE and not identical:
	// the pass has proposed something it cannot emit from, which is a failure
	// to make the showing rather than an absence of evidence.
	identical, conclusive = gate([]byte(`{"openapi":"3.0.3","paths":`), marked, nil)
	if !conclusive || identical {
		t.Fatalf("an unloadable SHIPPED image must be a conclusive refusal; identical=%v conclusive=%v", identical, conclusive)
	}
}
