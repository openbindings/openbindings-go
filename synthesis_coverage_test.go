package openbindings

import "testing"

func synthesisCoverageTestInterface() *Interface {
	return &Interface{
		OpenBindings: MaxTestedVersion,
		Operations: map[string]Operation{
			"getUser": {},
		},
		Sources: map[string]Source{
			"api": {BindingSpec: "openbindings.example@1", Location: "https://example.com/spec"},
		},
		Bindings: map[string]BindingEntry{
			"getUser.api": {Operation: "getUser", Source: "api", Ref: "#/getUser"},
		},
	}
}

func TestNewSynthesisResultDerivesCoverage(t *testing.T) {
	result, err := NewSynthesisResult(synthesisCoverageTestInterface(), []SynthesisCoverageEntry{
		{
			SourceIndex:  0,
			SourceRef:    "#/getUser",
			Scope:        SynthesisCoverageTarget,
			Status:       SynthesisRepresented,
			OperationKey: "getUser",
			BindingRef:   "#/getUser",
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
			SourceIndex:  0,
			SourceRef:    "#/getUser",
			Scope:        SynthesisCoverageTarget,
			Status:       SynthesisRepresented,
			OperationKey: "getUser",
			BindingRef:   "#/getUser",
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
			SourceIndex:  0,
			SourceRef:    "#/missing",
			Scope:        SynthesisCoverageTarget,
			Status:       SynthesisRepresented,
			OperationKey: "getUser",
			BindingRef:   "#/missing",
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
