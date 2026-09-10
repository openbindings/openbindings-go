package openbindings

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/openbindings/openbindings-go/internal/thirdparty/jsonschema"
	"github.com/openbindings/openbindings-go/jsonvalue"
)

func TestExactValidatorPredicateCapability(t *testing.T) {
	// The enormous exponent is short, and exceeds math/big's own expansion
	// ceiling. It must never be expanded, misclassified, or panic the invoker.
	value := json.Number("1e100000000000000000000000000000000000000")
	for _, schema := range []map[string]any{{"minimum": 0}, {"type": "integer"}, {"const": value}} {
		t.Run("predicate", func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("validation panicked: %v", recovered)
				}
			}()
			err := ValidateAgainstSchema(value, schema, nil)
			var capability *jsonvalue.CapabilityError
			if !errors.As(err, &capability) {
				t.Fatalf("want explicit numeric capability refusal, got %v", err)
			}
		})
	}
	// No numeric predicate is needed for a boolean schema. Carriage alone is
	// not subject to arithmetic work limits.
	if err := ValidateAgainstSchema(value, true, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaUnicodePropertyNames(t *testing.T) {
	for _, pattern := range []string{`^\p{Letter}+$`, `^\p{L}+$`} {
		for _, data := range []string{"letters", "éλ", "123"} {
			err := ValidateAgainstSchema(data, map[string]any{"pattern": pattern}, nil)
			if (err == nil) != (data != "123") {
				t.Errorf("%s / %s: %v", pattern, data, err)
			}
		}
	}
}

type refusingSchemaRegexp struct{ jsonschema.Regexp }

func (r refusingSchemaRegexp) MatchStringError(value string) (bool, error) {
	if value == "secret-refuse" {
		return false, &jsonvalue.CapabilityError{Operation: "regular-expression matching"}
	}
	return r.MatchString(value), nil
}

func TestSchemaRegexRefusalIsNotMismatch(t *testing.T) {
	// Deterministically exercise the actual backend timeout/error seam without
	// relying on a machine-dependent catastrophic-regex duration.
	for _, entry := range []struct {
		schema any
		value  any
		valid  any
	}{
		{map[string]any{"pattern": "^ok$"}, "secret-refuse", "ok"},
		{map[string]any{"patternProperties": map[string]any{"^ok$": true}}, map[string]any{"secret-refuse": 1}, map[string]any{"ok": 1}},
		{map[string]any{"anyOf": []any{map[string]any{"pattern": "^ok$"}, true}}, "secret-refuse", "ok"},
		{map[string]any{"not": map[string]any{"pattern": "^ok$"}}, "secret-refuse", "other"},
		{map[string]any{"if": map[string]any{"pattern": "^ok$"}, "then": true, "else": false}, "secret-refuse", "ok"},
	} {
		compiler := exactCountCompiler()
		compiler.UseRegexpEngine(func(expression string) (jsonschema.Regexp, error) {
			re, err := schemaRegexpEngine(expression)
			return refusingSchemaRegexp{re}, err
		})
		if err := compiler.AddResource("urn:test:regex", entry.schema); err != nil {
			t.Fatal(err)
		}
		compiled, err := compiler.Compile("urn:test:regex")
		if err != nil {
			t.Fatal(err)
		}
		for repeat := 0; repeat < 3; repeat++ {
			err := compiled.Validate(entry.value)
			var capability *jsonvalue.CapabilityError
			if !errors.As(err, &capability) {
				t.Fatalf("regex failure became a schema verdict: %v", err)
			}
			if strings.Contains(err.Error(), "secret-refuse") {
				t.Fatal("capability diagnostic disclosed instance data")
			}
			if err := compiled.Validate(entry.valid); err != nil {
				t.Fatalf("refusal poisoned retained validator: %v", err)
			}
		}
	}
}

func TestExactCapabilityAbortsSpeculationAndAllowsReuse(t *testing.T) {
	huge := json.Number("1e100000000000000000000000000000000000000")
	for _, schema := range []map[string]any{
		{"anyOf": []any{map[string]any{"minimum": 0}, false}},
		{"oneOf": []any{map[string]any{"minimum": 0}, true}},
		{"not": map[string]any{"minimum": 0}},
		{"if": map[string]any{"minimum": 0}, "then": false, "else": true},
		{"$defs": map[string]any{"p": map[string]any{"minimum": 0}}, "$ref": "#/$defs/p"},
	} {
		compiler := exactCountCompiler()
		if err := compiler.AddResource("urn:test:predicate", schema); err != nil {
			t.Fatal(err)
		}
		compiled, err := compiler.Compile("urn:test:predicate")
		if err != nil {
			t.Fatal(err)
		}
		for repeat := 0; repeat < 3; repeat++ {
			var capability *jsonvalue.CapabilityError
			if err := compiled.Validate(huge); !errors.As(err, &capability) {
				t.Fatalf("speculative verdict: %v", err)
			}
			// A refusal must not corrupt the retained evaluator for later input.
			if err := compiled.Validate(1); errors.As(err, &capability) {
				t.Fatal(err)
			}
		}
	}
	compiler := exactCountCompiler()
	if err := compiler.AddResource("urn:test:large", map[string]any{"minimum": huge}); err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource("urn:test:ordinary", true); err != nil {
		t.Fatal(err)
	}
	for repeat := 0; repeat < 3; repeat++ {
		compiled, err := compiler.Compile("urn:test:large")
		var capability *jsonvalue.CapabilityError
		if compiled != nil || !errors.As(err, &capability) {
			t.Fatalf("compile refusal %v %v", compiled, err)
		}
		ordinary, err := compiler.Compile("urn:test:ordinary")
		if err != nil || ordinary.Validate(huge) != nil {
			t.Fatalf("poisoned compiler: %v", err)
		}
	}
}
