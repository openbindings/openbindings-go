package openbindings

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
)

// nestedSchema is a schema nesting levels deep, past encoding/json's 10000
// levels when levels is.
func nestedSchema(levels int) any {
	var schema any = map[string]any{}
	for range levels {
		schema = map[string]any{"not": schema}
	}
	return schema
}

// decidedRules lists the rules a report does not leave inconclusive.
func decidedRules(report ValidationReport) []string {
	var decided []string
	for _, rule := range DocumentRules() {
		if report.Evidence[rule] != EvidenceInconclusive {
			decided = append(decided, rule)
		}
	}
	return decided
}

// A document nesting deeper than encoding/json reads is beyond this SDK's own
// limit whether it arrives as bytes or in memory: in memory, Validate reports
// conformance undetermined, deciding OBI-D-09 alone, and References and
// Resolve return an error matching ErrInconclusive, as ParseDocument does for
// the bytes.
func TestNestingLimit_InMemory(t *testing.T) {
	deepSchema := nestedSchema(10001)
	deepRaw := json.RawMessage(strings.Repeat("[", 10001) + strings.Repeat("]", 10001))
	compiler, err := NewValueContractCompiler(testEvaluator{})
	if err != nil {
		t.Fatal(err)
	}
	for name, build := range map[string]func(version string) *Document{
		"a schema": func(version string) *Document {
			return &Document{OpenBindings: version, Operations: map[string]Operation{"op": {Input: deepSchema}}}
		},
		"source content": func(version string) *Document {
			return &Document{OpenBindings: version, Operations: map[string]Operation{}, Sources: map[string]Source{"s": {Kind: "k", Content: deepRaw}}}
		},
	} {
		report, err := build("0.2.0").Validate()
		if err != nil || report.Conclusion != ConclusionConformanceUndetermined || !slices.Equal(decidedRules(report), []string{"OBI-D-09"}) {
			t.Errorf("%s: %s deciding %v, %v", name, report.Conclusion, decidedRules(report), err)
		}
		report, err = build("0.2").Validate()
		if !errors.As(err, new(*ValidationError)) || report.Conclusion != ConclusionNonConformant || !slices.Equal(report.Violated, []string{"OBI-D-09"}) {
			t.Errorf("%s, no valid version: %s, violated %v, %v", name, report.Conclusion, report.Violated, err)
		}
		if _, err := build("0.3.0").Validate(); !errors.As(err, new(*VersionRefusalError)) {
			t.Errorf("%s, an unsupported version: %v", name, err)
		}
		refs, err := build("0.2.0").References()
		if refs != nil || !errors.Is(err, ErrInconclusive) || errors.As(err, new(*VersionRefusalError)) {
			t.Errorf("%s: References gave %v, %v", name, refs, err)
		}
		if _, err := compiler.Resolve(context.Background(), build("0.2.0")); !errors.Is(err, ErrInconclusive) || errors.As(err, new(*VersionRefusalError)) {
			t.Errorf("%s: Resolve gave %v", name, err)
		}
	}

	// The same schema as bytes.
	text := []byte(`{"openbindings":"0.2.0","operations":{"op":{"input":` + strings.Repeat(`{"not":`, 10001) + `{}` + strings.Repeat(`}`, 10001) + `}}}`)
	if _, err := ParseDocument(text); !errors.Is(err, ErrInconclusive) {
		t.Errorf("ParseDocument: %v", err)
	}
	if _, report, err := ValidateDocument(text); err != nil || report.Conclusion != ConclusionConformanceUndetermined {
		t.Errorf("ValidateDocument: %s, %v", report.Conclusion, err)
	}
}

// A value that cannot be encoded for a reason of its own is the caller's
// defect, not this SDK's limit: Validate, References, and Resolve return an
// error matching no category, whatever a marshaler in the document says, and
// no report.
func TestNestingLimit_EncodingDefectsMatchNoCategory(t *testing.T) {
	compiler, err := NewValueContractCompiler(testEvaluator{})
	if err != nil {
		t.Fatal(err)
	}
	withInput := func(input any) *Document {
		return &Document{OpenBindings: "0.2.0", Operations: map[string]Operation{"op": {Input: input}}}
	}
	documents := map[string]*Document{
		"a NaN":         withInput(map[string]any{"maximum": math.NaN()}),
		"a channel":     withInput(map[string]any{"const": make(chan int)}),
		"invalid UTF-8": {OpenBindings: "0.2.0", Operations: map[string]Operation{}, Description: Present("caf\xff")},
	}
	for _, said := range []error{ErrInconclusive, ErrNoVerdict, ErrMismatch, &NoVerdictError{Cause: errors.New("made by the marshaler")}} {
		documents["a marshaler in a schema saying "+said.Error()] = withInput(failingMarshaler{said})
	}
	for name, doc := range documents {
		report, validateErr := doc.Validate()
		refs, referencesErr := doc.References()
		_, resolveErr := compiler.Resolve(context.Background(), doc)
		if report.Evidence != nil || refs != nil {
			t.Errorf("%s: a report or references for a value with no encoding: %+v %v", name, report, refs)
		}
		for entry, err := range map[string]error{"Validate": validateErr, "References": referencesErr, "Resolve": resolveErr} {
			if err == nil || len(categories(err)) != 0 || errors.As(err, new(*VersionRefusalError)) || errors.As(err, new(*ValidationError)) || errors.As(err, new(*NoVerdictError)) {
				t.Errorf("%s, %s: %v matches %v", name, entry, err, categories(err))
			}
		}
	}
	if _, err := compiler.Resolve(context.Background(), nil); err == nil || len(categories(err)) != 0 {
		t.Errorf("a nil document: %v", err)
	}
}
