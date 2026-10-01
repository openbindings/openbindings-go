package openbindings

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Every message the package writes names it once, at the start; what it
// wraps from elsewhere (an evaluator's error, a context error, encoding/json's
// framing of an encoding error) is left as it is.
func TestMessagesNameThePackageOnce(t *testing.T) {
	deep := strings.Repeat("[", 10001) + strings.Repeat("]", 10001)
	parse := func(text string) error {
		_, err := ParseDocument([]byte(text))
		return err
	}
	decode := func(text string, into any) error { return json.Unmarshal([]byte(text), into) }
	contracts := contractsFor(t, mustDecodeDocument(t, `{"openbindings":"0.2.0","operations":{
	  "loop":{"input":{"$ref":"#/operations/loop/input"}},"none":{},"text":{"input":{"type":"string"}}}}`))
	standing := func(operation, direction string) error {
		compile := contracts.CompileInput
		if direction == "output" {
			compile = contracts.CompileOutput
		}
		contract, err := compile(context.Background(), operation)
		if err != nil {
			return err
		}
		return contract.Err()
	}
	text, err := contracts.CompileInput(context.Background(), "text")
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := NewValueContractCompiler(testEvaluator{})
	if err != nil {
		t.Fatal(err)
	}
	_, resolveErr := compiler.Resolve(context.Background(), &Document{OpenBindings: "0.2"})
	_, nilErr := compiler.Resolve(context.Background(), nil)
	_, referencesErr := (&Document{OpenBindings: ""}).References()
	_, unknownErr := contracts.CompileInput(context.Background(), "nope")
	for name, err := range map[string]error{
		"a refusal":                     parse(`{"openbindings":"0.3.0","operations":{}}`),
		"a violation":                   parse(`{"openbindings":"0.2.0","operations":{},"unknown":1}`),
		"an empty violation":            &ValidationError{},
		"nested past the decoder":       parse(`{"openbindings":"0.2.0","operations":{},"x-deep":` + deep + `}`),
		"a lone surrogate":              parse(`{"openbindings":"0.2.0","operations":{},"x-note":"\ud800"}`),
		"decoding a document":           decode(`{"openbindings":"0.2.0","operations":{},"operations":{}}`, new(Document)),
		"decoding a member":             decode(`{"openbindings":"0.2.0","operations":{"a":{"tags":null}}}`, new(Document)),
		"decoding an operation":         decode(`{"tags":[null]}`, new(Operation)),
		"CheckVersion":                  CheckVersion("1.0.0"),
		"no value contract":             standing("none", "output"),
		"an undefined result":           standing("loop", "input"),
		"no operation":                  unknownErr,
		"no valid version to resolve":   resolveErr,
		"no document to resolve":        nilErr,
		"no valid version to reference": referencesErr,
		"text that is not JSON":         text.ValidateJSON(context.Background(), []byte(`{`)),
		"a value that is not JSON":      text.Validate(context.Background(), []byte(`"x"`)),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if message := err.Error(); !strings.HasPrefix(message, "openbindings: ") || strings.Count(message, "openbindings:") != 1 {
			t.Errorf("%s: %q", name, message)
		}
	}
	// A cause core gives keeps its sentinel.
	if err := standing("loop", "input"); !errors.Is(err, ErrUndefined) {
		t.Errorf("an undefined result: %v", err)
	}
	if err := standing("none", "output"); !errors.Is(err, ErrNoValueContract) {
		t.Errorf("no value contract: %v", err)
	}
}
