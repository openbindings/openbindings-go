package synthesize

import (
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func synthesisCoverageTestInterface() *openbindings.Interface {
	return &openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Operations: map[string]openbindings.Operation{
			"getUser": {},
		},
		Sources: map[string]openbindings.Source{
			"api": {BindingSpec: "openbindings.example@1", Location: "https://example.com/spec"},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"getUser.api": {Operation: "getUser", Source: "api", Selector: "#/getUser"},
		},
	}
}

func TestNewSynthesisResultDerivesCoverage(t *testing.T) {
	result, err := NewSynthesisResult(synthesisCoverageTestInterface(), []SynthesisCoverageEntry{
		{
			SourceIndex:     0,
			SourceRef:       "#/getUser",
			Scope:           SynthesisCoverageTarget,
			Status:          SynthesisRepresented,
			OperationKey:    "getUser",
			BindingSelector: "#/getUser",
		},
		{
			SourceIndex: 0,
			SourceRef:   "#/callbacks/onUser",
			Scope:       SynthesisCoverageTarget,
			Status:      SynthesisExcluded,
			ReasonCode:  "example.reverse_direction",
			Rule:        "EXAMPLE-P-07",
			Message:     "reverse-direction callbacks are outside revision 1",
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Coverage.Exhaustive {
		t.Fatal("coverage must be exhaustive")
	}
	if result.Coverage.FullyRepresented {
		t.Fatal("an excluded upstream-valid interaction prevents full representation")
	}
	represented := result.Coverage.Entries[0]
	if represented.SourceKey != "api" || represented.BindingKey != "getUser.api" {
		t.Fatalf("represented evidence must identify emitted source and binding, got %+v", represented)
	}
}

// An invalid entry clears FullyRepresented exactly like every other
// non-represented status: an upstream-invalid unit is still an inventoried
// unit the emitted OBI does not represent. Pinned by MC5 seal-1 finding
// F-V3-1, where a document whose every target was invalid reported
// fullyRepresented true.
func TestNewSynthesisResultInvalidEntryClearsFullyRepresented(t *testing.T) {
	result, err := NewSynthesisResult(synthesisCoverageTestInterface(), []SynthesisCoverageEntry{
		{
			SourceIndex:     0,
			SourceRef:       "#/getUser",
			Scope:           SynthesisCoverageTarget,
			Status:          SynthesisRepresented,
			OperationKey:    "getUser",
			BindingSelector: "#/getUser",
		},
		{
			SourceIndex: 0,
			SourceRef:   "#/operations/broken",
			Scope:       SynthesisCoverageTarget,
			Status:      SynthesisInvalid,
			ReasonCode:  "example.invalid_target",
			Rule:        "EXAMPLE-D-03",
			Message:     "the target does not resolve to an operation object",
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage.FullyRepresented {
		t.Fatal("an invalid entry must clear FullyRepresented")
	}
}

func TestNewSynthesisResultRejectsUnbackedRepresentation(t *testing.T) {
	_, err := NewSynthesisResult(synthesisCoverageTestInterface(), []SynthesisCoverageEntry{
		{
			SourceIndex:     0,
			SourceRef:       "#/missing",
			Scope:           SynthesisCoverageTarget,
			Status:          SynthesisRepresented,
			OperationKey:    "getUser",
			BindingSelector: "#/missing",
		},
	}, true)
	if err == nil {
		t.Fatal("expected represented entry without a matching binding to fail")
	}
}

func TestNewSynthesisResultDoesNotCallNonExhaustiveFull(t *testing.T) {
	result, err := NewSynthesisResult(synthesisCoverageTestInterface(), RepresentedCoverageEntries(synthesisCoverageTestInterface(), 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Coverage.FullyRepresented {
		t.Fatal("non-exhaustive coverage cannot be fully represented")
	}
	if result.Coverage.Limitation == nil || result.Coverage.Limitation.Code != "synthesis.inventory_incomplete" {
		t.Fatalf("non-exhaustive coverage must explain its limitation, got %+v", result.Coverage.Limitation)
	}
}

func TestNewSynthesisResultWithLimitationValidatesExhaustivenessEvidence(t *testing.T) {
	if _, err := NewSynthesisResultWithLimitation(
		synthesisCoverageTestInterface(),
		RepresentedCoverageEntries(synthesisCoverageTestInterface(), 0),
		false,
		&SynthesisCoverageLimitation{Code: "example.bounded_listing", Message: "the live listing stopped at its declared page bound"},
	); err != nil {
		t.Fatalf("valid limitation: %v", err)
	}
	if _, err := NewSynthesisResultWithLimitation(
		synthesisCoverageTestInterface(),
		RepresentedCoverageEntries(synthesisCoverageTestInterface(), 0),
		false,
		nil,
	); err == nil {
		t.Fatal("non-exhaustive coverage without limitation must fail")
	}
}

// An alternative is a unit AT ITS OPERATION. The published contract defines a
// unit as an independently selectable alternative "whose omission would remove
// a source-permitted invocation path", so one source declaration inherited by
// two operations (an OAS 2.0 root-level `consumes` member) is two units with
// two dispositions, and the duplicate check must key an alternative on its
// operation and binding as well as its source unit. Before this rule both SDKs
// failed the whole coverage call on any 2.0 document with two body operations
// inheriting root `consumes`.
func TestNewSynthesisResultKeysAlternativesByOperation(t *testing.T) {
	iface := &openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Operations: map[string]openbindings.Operation{
			"a": {},
			"b": {},
		},
		Sources: map[string]openbindings.Source{
			"api": {BindingSpec: "openbindings.example@1", Location: "https://example.com/spec"},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"a.api": {Operation: "a", Source: "api", Selector: "#/a"},
			"b.api": {Operation: "b", Source: "api", Selector: "#/b"},
		},
	}
	target := func(op string) SynthesisCoverageEntry {
		return SynthesisCoverageEntry{SourceIndex: 0, SourceRef: "#/" + op, Scope: SynthesisCoverageTarget, Status: SynthesisRepresented, OperationKey: op, BindingSelector: "#/" + op}
	}
	alternative := func(op string) SynthesisCoverageEntry {
		return SynthesisCoverageEntry{SourceIndex: 0, SourceRef: "#/consumes/0", Scope: SynthesisCoverageAlternative, Status: SynthesisRepresented, OperationKey: op, BindingKey: op + ".api", BindingSelector: "#/" + op}
	}

	result, err := NewSynthesisResult(iface, []SynthesisCoverageEntry{target("a"), alternative("a"), target("b"), alternative("b")}, true)
	if err != nil {
		t.Fatalf("one inherited alternative per operation must be accepted: %v", err)
	}
	if !result.Coverage.FullyRepresented || len(result.Coverage.Entries) != 4 {
		t.Fatalf("expected four represented entries and full representation, got %+v", result.Coverage)
	}

	// The same alternative at the same operation is still one unit.
	if _, err := NewSynthesisResult(iface, []SynthesisCoverageEntry{target("a"), alternative("a"), alternative("a"), target("b")}, true); err == nil || !strings.Contains(err.Error(), `duplicate synthesis coverage entry for source 0 alternative "#/consumes/0" at operation "a" binding "a.api"`) {
		t.Fatalf("the same alternative at one operation must be rejected as a duplicate, got %v", err)
	}

	// Excluded alternatives that keep their operation identity are distinct
	// per operation (the 3.x adapters' shape for a root-level server or
	// security alternative).
	excluded := func(op string) SynthesisCoverageEntry {
		return SynthesisCoverageEntry{SourceIndex: 0, SourceRef: "#/servers/0", Scope: SynthesisCoverageAlternative, Status: SynthesisExcluded, ReasonCode: "example.server_url_excluded", Rule: "EXAMPLE-P-04", Message: "unusable", OperationKey: op, BindingSelector: "#/" + op}
	}
	if _, err := NewSynthesisResult(iface, []SynthesisCoverageEntry{target("a"), excluded("a"), target("b"), excluded("b")}, true); err != nil {
		t.Fatalf("an excluded alternative identified per operation must be accepted: %v", err)
	}

	// Two entries for one source unit with no operation identity remain
	// indistinguishable, and remain duplicates.
	anonymous := SynthesisCoverageEntry{SourceIndex: 0, SourceRef: "#/servers/0", Scope: SynthesisCoverageAlternative, Status: SynthesisExcluded, ReasonCode: "example.server_url_excluded", Rule: "EXAMPLE-P-04", Message: "unusable"}
	if _, err := NewSynthesisResult(iface, []SynthesisCoverageEntry{target("a"), anonymous, target("b"), anonymous}, true); err == nil || !strings.Contains(err.Error(), `duplicate synthesis coverage entry for source 0 alternative "#/servers/0"`) {
		t.Fatalf("identity-less duplicate alternatives must still be rejected, got %v", err)
	}

	// A target is identified by its source unit alone: the key extension does
	// not reach target scope.
	if _, err := NewSynthesisResult(iface, []SynthesisCoverageEntry{target("a"), target("a"), target("b")}, true); err == nil || !strings.Contains(err.Error(), `duplicate synthesis coverage entry for source 0 target "#/a"`) {
		t.Fatalf("duplicate targets must still be rejected, got %v", err)
	}
}
