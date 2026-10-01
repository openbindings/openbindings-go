package openbindings_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go"
)

// callerMarshaler is a caller's own schema value, outside this package,
// whose encoding fails with its error or writes its text.
type callerMarshaler struct {
	err  error
	text string
}

func (m callerMarshaler) MarshalJSON() ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	return []byte(m.text), nil
}

// outcomes names the categories and report shape an entry point's result has.
func outcomes(err error, report *openbindings.ValidationReport) string {
	var parts []string
	for name, sentinel := range map[string]error{"ErrMismatch": openbindings.ErrMismatch, "ErrNoVerdict": openbindings.ErrNoVerdict, "ErrInconclusive": openbindings.ErrInconclusive} {
		if errors.Is(err, sentinel) {
			parts = append(parts, name)
		}
	}
	slices.Sort(parts)
	switch {
	case report != nil && report.Evidence != nil:
		parts = append(parts, "report "+string(report.Conclusion))
	case err == nil:
		parts = append(parts, "no error")
	default:
		parts = append(parts, "an error")
	}
	return strings.Join(parts, ", ")
}

// Only a limit core establishes while checking a document's own
// representation is the SDK's limit. A caller's marshaler that fails is the
// marshaler's failure, matching no category and giving no report, whatever
// its error carries: a limit ParseDocument reported for other bytes, or the
// failure of encoding another, too deep, document, plain, wrapped, or joined.
// So is text a caller's marshaler writes past the decoder's depth, which
// encoding/json refuses. Text within that depth that makes the document as a
// whole too deep is the SDK's limit, which core establishes.
func TestModelLimits_CallerMarshalers(t *testing.T) {
	backslash := string(rune(92)) // built, so that no cleanup turns the escape into a character
	_, nested := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{},"x-deep":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`))
	_, lone := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{},"x-note":"` + backslash + `ud800"}`))
	deep := strings.Repeat(`{"not":`, 10001) + `{}` + strings.Repeat(`}`, 10001)
	_, deepEncoding := json.Marshal(openbindings.Document{OpenBindings: "0.2.0", Operations: map[string]openbindings.Operation{"op": {Input: json.RawMessage(deep)}}})
	for name, err := range map[string]error{"nesting": nested, "a lone surrogate": lone, "a deep document's encoding": deepEncoding} {
		if !errors.Is(err, openbindings.ErrInconclusive) && name != "a deep document's encoding" || err == nil {
			t.Fatalf("%s: the forwarded error is not the limit it should carry: %v", name, err)
		}
	}
	withInput := func(input any) *openbindings.Document {
		return &openbindings.Document{OpenBindings: "0.2.0", Operations: map[string]openbindings.Operation{"op": {Input: input}}}
	}
	documents := map[string]struct {
		doc  *openbindings.Document
		want string
	}{
		"text past the decoder's depth":    {withInput(callerMarshaler{text: strings.Repeat("[", 10001) + strings.Repeat("]", 10001)}), "an error"},
		"text the document makes too deep": {withInput(callerMarshaler{text: strings.Repeat("[", 9999) + strings.Repeat("]", 9999)}), "report conformance-undetermined"},
		"ErrInconclusive":                  {withInput(callerMarshaler{err: openbindings.ErrInconclusive}), "an error"},
		"a *NoVerdictError":                {withInput(callerMarshaler{err: &openbindings.NoVerdictError{Cause: errors.New("made by the marshaler")}}), "an error"},
		"a *VersionRefusalError":           {withInput(callerMarshaler{err: openbindings.CheckVersion("0.3.0")}), "an error"},
	}
	for limit, err := range map[string]error{"nesting": nested, "a lone surrogate": lone, "a deep document's encoding": deepEncoding} {
		documents["forwarding "+limit] = struct {
			doc  *openbindings.Document
			want string
		}{withInput(callerMarshaler{err: err}), "an error"}
		documents["forwarding "+limit+", wrapped"] = struct {
			doc  *openbindings.Document
			want string
		}{withInput(callerMarshaler{err: fmt.Errorf("marshaler: %w", err)}), "an error"}
		documents["forwarding "+limit+", joined"] = struct {
			doc  *openbindings.Document
			want string
		}{withInput(callerMarshaler{err: errors.Join(errors.New("marshaler"), err)}), "an error"}
	}
	compiler, err := openbindings.NewValueContractCompiler(typeOnly{}) // Resolve calls no evaluator
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range documents {
		report, validateErr := c.doc.Validate()
		if got := outcomes(validateErr, &report); got != c.want {
			t.Errorf("%s, Validate: %s, want %s: %v", name, got, c.want, validateErr)
		}
		if validateErr != nil && (errors.As(validateErr, new(*openbindings.VersionRefusalError)) || errors.As(validateErr, new(*openbindings.NoVerdictError))) {
			t.Errorf("%s, Validate: the marshaler's error is reachable: %v", name, validateErr)
		}
		wantError := "an error"
		if c.want != "an error" {
			wantError = "ErrInconclusive, an error"
		}
		refs, referencesErr := c.doc.References()
		if got := outcomes(referencesErr, nil); got != wantError || refs != nil {
			t.Errorf("%s, References: %s with %v, want %s: %v", name, got, refs, wantError, referencesErr)
		}
		_, resolveErr := compiler.Resolve(context.Background(), c.doc)
		if got := outcomes(resolveErr, nil); got != wantError {
			t.Errorf("%s, Resolve: %s, want %s: %v", name, got, wantError, resolveErr)
		}
	}
}
