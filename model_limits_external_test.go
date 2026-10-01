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

// omittedRaw is a caller's struct schema whose raw member encoding/json
// omits when it is empty.
type omittedRaw struct {
	Type string          `json:"type,omitempty"`
	Not  json.RawMessage `json:"not,omitempty"`
}

// addressedMarshaler encodes itself through a pointer method, which
// encoding/json calls on an addressable value, so Raw is never written.
type addressedMarshaler struct{ Raw json.RawMessage }

func (*addressedMarshaler) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

// holdsAddressed holds an addressedMarshaler, addressable through a pointer
// to it.
type holdsAddressed struct {
	Not addressedMarshaler `json:"not"`
}

// embeddedRaw is unexported and embedded in promotesRaw, whose encoding
// writes its exported member.
type embeddedRaw struct {
	Not json.RawMessage `json:"not"`
}

type promotesRaw struct{ embeddedRaw }

// marshalerField holds a value behind the json.Marshaler interface.
type marshalerField struct {
	Const json.Marshaler `json:"const"`
}

// The boundary of a document in memory, as Document.Validate states it: what
// core finds in the text the document encodes to, or this package's types
// find in the members they carry as raw JSON, is this SDK's limit; a failure
// to encode at all is an encoding failure matching no category, whatever its
// error carries, from encoding/json or from any marshaler but the model's on
// its own members, this package's own types placed inside a schema included.
// Each side is pinned through Validate, References, and Resolve, and a value
// encoding/json writes without a failure keeps its plain result.
func TestModelLimits_Boundary(t *testing.T) {
	backslash := string(rune(92)) // built, so that no cleanup turns the escape into a character
	lone := `"` + backslash + `ud800"`
	deep := json.RawMessage(strings.Repeat(`{"not":`, 10001) + `{}` + strings.Repeat(`}`, 10001))
	within := json.RawMessage(strings.Repeat(`{"not":`, 9999) + `{}` + strings.Repeat(`}`, 9999))
	loneSource := openbindings.Source{Kind: "k", Content: json.RawMessage(lone)}
	withInput := func(input any) *openbindings.Document {
		return &openbindings.Document{OpenBindings: "0.2.0", Operations: map[string]openbindings.Operation{"op": {Input: input}}}
	}
	const (
		limit   = "the SDK's limit"
		failure = "an encoding failure"
		plain   = "conformant"
	)
	documents := map[string]struct {
		doc  *openbindings.Document
		side string
	}{
		// Raw JSON past encoding/json's depth, which it refuses to compact.
		"raw JSON past the decoder's depth as a schema":                                  {withInput(deep), failure},
		"raw JSON past the decoder's depth within a schema":                              {&openbindings.Document{OpenBindings: "0.2.0", Operations: map[string]openbindings.Operation{"op": {Output: map[string]any{"properties": map[string]any{"a": deep}}}}}, failure},
		"raw JSON past the decoder's depth as a schemas entry":                           {&openbindings.Document{OpenBindings: "0.2.0", Operations: map[string]openbindings.Operation{}, Schemas: map[string]openbindings.JSONSchema{"Deep": &deep}}, failure},
		"raw JSON past the decoder's depth, promoted from an unexported embedded struct": {withInput(promotesRaw{embeddedRaw{Not: deep}}), failure},
		// Raw JSON encoding/json writes, which core's scan reads.
		"raw JSON within the decoder's depth the document makes too deep": {withInput(within), limit},
		"a lone surrogate in raw JSON as a schema":                        {withInput(json.RawMessage(`{"const":` + lone + `}`)), limit},
		"a lone surrogate in raw JSON promoted from an embedded struct":   {withInput(promotesRaw{embeddedRaw{Not: json.RawMessage(`{"const":` + lone + `}`)}}), limit},
		"a repeated name in raw JSON as a schema":                         {withInput(json.RawMessage(`{"type":"string","type":"number"}`)), failure},
		// This package's types: a limit on the model's own members, an
		// encoding failure inside a schema.
		"a lone surrogate in a source's content":                    {&openbindings.Document{OpenBindings: "0.2.0", Operations: map[string]openbindings.Operation{}, Sources: map[string]openbindings.Source{"s": loneSource}}, limit},
		"that source in a schema, behind json.Marshaler":            {withInput(marshalerField{Const: loneSource}), failure},
		"that source in a schema, behind any":                       {withInput(map[string]any{"const": loneSource}), failure},
		"that source as a schema":                                   {withInput(loneSource), failure},
		"that source as a schemas entry":                            {&openbindings.Document{OpenBindings: "0.2.0", Operations: map[string]openbindings.Operation{}, Schemas: map[string]openbindings.JSONSchema{"S": loneSource}}, failure},
		"an operation holding that source's content, in a schema":   {withInput(map[string]any{"const": openbindings.Operation{Examples: map[string]openbindings.OperationExample{"e": {Input: json.RawMessage(lone)}}}}), failure},
		"an omitempty raw member left empty":                        {withInput(omittedRaw{Type: "string", Not: json.RawMessage{}}), plain},
		"a pointer marshaler not writing raw JSON past the depth":   {withInput(&holdsAddressed{Not: addressedMarshaler{Raw: deep}}), plain},
		"a pointer marshaler not writing raw JSON that is not JSON": {withInput(&holdsAddressed{Not: addressedMarshaler{Raw: json.RawMessage(`{`)}}), plain},
	}
	want := map[string][3]string{
		limit:   {"report conformance-undetermined", "ErrInconclusive, an error", "ErrInconclusive, an error"},
		failure: {"an error", "an error", "an error"},
		plain:   {"report conformant", "no error", "no error"},
	}
	compiler, err := openbindings.NewValueContractCompiler(typeOnly{}) // Resolve calls no evaluator
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range documents {
		report, validateErr := c.doc.Validate()
		refs, referencesErr := c.doc.References()
		_, resolveErr := compiler.Resolve(context.Background(), c.doc)
		got := [3]string{outcomes(validateErr, &report), outcomes(referencesErr, nil), outcomes(resolveErr, nil)}
		if got != want[c.side] {
			t.Errorf("%s: Validate, References, Resolve gave %q, want %s %q: %v; %v; %v", name, got, c.side, want[c.side], validateErr, referencesErr, resolveErr)
		}
		var decided []string
		for _, rule := range openbindings.DocumentRules() {
			if report.Evidence[rule] != openbindings.EvidenceInconclusive {
				decided = append(decided, rule)
			}
		}
		if c.side == limit && !slices.Equal(decided, []string{"OBI-D-09"}) {
			t.Errorf("%s: the report decides %v, want OBI-D-09 alone", name, decided)
		}
		if c.side == failure {
			for entry, err := range map[string]error{"Validate": validateErr, "References": referencesErr, "Resolve": resolveErr} {
				if errors.As(err, new(*openbindings.VersionRefusalError)) || errors.As(err, new(*openbindings.ValidationError)) || errors.As(err, new(*openbindings.NoVerdictError)) {
					t.Errorf("%s, %s: a typed error is reachable: %v", name, entry, err)
				}
			}
			if refs != nil {
				t.Errorf("%s: References listed %v", name, refs)
			}
		}
	}
}

// The value lane's side of the boundary, for values a caller builds: raw JSON
// in a value is encoded by encoding/json like any Go value, so what it
// refuses is not a JSON value (ErrInconclusive) and what it omits or never
// writes is not read.
func TestCategories_RawJSONInCallerValues(t *testing.T) {
	ctx := context.Background()
	doc, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"op":{"input":{"type":"object"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := openbindings.NewValueContractCompiler(typeOnly{})
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := compiler.Resolve(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := contracts.CompileInput(ctx, "op")
	if err != nil {
		t.Fatal(err)
	}
	deep := json.RawMessage(strings.Repeat(`{"not":`, 10001) + `{}` + strings.Repeat(`}`, 10001))
	for name, value := range map[string]any{
		"an omitempty raw member left empty":                        omittedRaw{Not: json.RawMessage{}},
		"a pointer marshaler not writing raw JSON past the depth":   &holdsAddressed{Not: addressedMarshaler{Raw: deep}},
		"a pointer marshaler not writing raw JSON that is not JSON": &holdsAddressed{Not: addressedMarshaler{Raw: json.RawMessage(`{`)}},
	} {
		if err := contract.Validate(ctx, value); err != nil {
			t.Errorf("%s: %v, want a verdict that it is valid", name, err)
		}
	}
	for name, c := range map[string]struct {
		value any
		text  string // the text whose ValidateJSON category it shares, if any
	}{
		"raw JSON past the depth, promoted from an unexported embedded struct": {promotesRaw{embeddedRaw{Not: deep}}, ""},
		"a repeated name beside two values":                                    {[]any{json.RawMessage(`{"a":1,"a":2}`), json.RawMessage(`1 2`)}, `[{"a":1,"a":2},1 2]`},
	} {
		got := contract.Validate(ctx, c.value)
		if !errors.Is(got, openbindings.ErrInconclusive) || errors.Is(got, openbindings.ErrNoVerdict) || errors.Is(got, openbindings.ErrMismatch) {
			t.Errorf("%s: %v, want ErrInconclusive alone", name, got)
		}
		if c.text != "" {
			if whole := contract.ValidateJSON(ctx, []byte(c.text)); outcomes(whole, nil) != outcomes(got, nil) {
				t.Errorf("%s: %v, want the category ValidateJSON gives %s: %v", name, got, c.text, whole)
			}
		}
	}
}
