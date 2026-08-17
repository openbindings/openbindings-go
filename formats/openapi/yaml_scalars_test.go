package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// A declared example crosses into the OBI as the value the artifact wrote.
// String content parses as YAML 1.2.2, whose core tag resolution (§10.3.2)
// resolves the null/bool/int/float patterns and makes every other plain
// scalar a string; the OAS requires exactly that restriction, since "Tags
// MUST be limited to those allowed by [YAML's] JSON schema ruleset" (§4.2)
// and YAML 1.1's timestamp tag is outside it. The bare-date and
// space-separated spellings are the sensitive cases: a timestamp resolution
// does not even round-trip their text. The TypeScript twin pins the same
// table in packages/openapi/src/yaml-scalars.test.ts.
func TestSynthesizeInterface_YAMLCoreScalarsAtTheBoundary(t *testing.T) {
	stringResolved := []string{
		"2020-01-01T12:00:00Z",
		"2020-01-01",
		"2020-01-01 12:00:00",
		"12:30:45",
		"yes",
		"off",
	}
	for _, spelling := range stringResolved {
		t.Run(spelling, func(t *testing.T) {
			assertYAMLScalarExample(t, spelling, fmt.Sprintf("%q", spelling))
		})
	}

	patterned := map[string]string{
		"true":  "true",
		"~":     "null",
		"0o17":  "15",
		"0x1F":  "31",
		"+12.3": "12.3",
		".5":    "0.5",
	}
	for spelling, want := range patterned {
		t.Run(spelling, func(t *testing.T) {
			assertYAMLScalarExample(t, spelling, want)
		})
	}
}

// Three spellings this decoder resolves by YAML 1.1 rules the accepted OAS
// editions do not admit, recorded here so the divergence is named rather
// than absent. Under YAML 1.2.2 §10.3.2 a leading zero is decimal
// ("[-+]? [0-9]+", base 10), the underscore-bearing and 0b-prefixed forms
// match no pattern at all and are strings, and the TypeScript twin agrees
// with the authority on the first two. The decoder is kin-openapi's, whose
// DecodeOpts exposes only DisableTimestamps, so converging costs a
// conformant YAML-to-JSON layer of our own in both twins; that is filed as
// F-O1-7 in corpus-lab/OPENAPI-RUNTIME.md rather than half-fixed here,
// since fixing one twin alone would trade a conformance gap for a parity
// gap. No corpus specimen's emitted OBI depends on any of the three.
func TestSynthesizeInterface_YAMLScalarKnownNonConformance(t *testing.T) {
	known := map[string]struct{ emitted, authority string }{
		"017":   {emitted: "15", authority: "17 (base 10)"},
		"1_000": {emitted: "1000", authority: `"1_000" (matches no pattern)`},
		"0b101": {emitted: "5", authority: `"0b101" (matches no pattern)`},
	}
	for spelling, want := range known {
		t.Run(spelling, func(t *testing.T) {
			tree, err := synthesizeYAMLScalarDocument(t, spelling)
			if err != nil {
				t.Fatalf("synthesize %s: %v", spelling, err)
			}
			if got := yamlScalarExample(t, tree); got != want.emitted {
				t.Fatalf("example %s emitted %s, want the recorded %s (YAML 1.2.2 §10.3.2 says %s)",
					spelling, got, want.emitted, want.authority)
			}
		})
	}
}

// ±.inf and .nan resolve to floats JSON cannot spell. The operation value
// domain is JSON (core §5) and the OAS admits YAML to "preserve the ability
// to round-trip between YAML and JSON formats" (§4.2), so the artifact is
// refused whole rather than emitted as a null the author never wrote. The
// TypeScript twin refuses the same documents.
func TestSynthesizeInterface_YAMLScalarWithoutJSONImageRefuses(t *testing.T) {
	for _, spelling := range []string{".inf", "-.inf", ".nan"} {
		t.Run(spelling, func(t *testing.T) {
			_, err := synthesizeYAMLScalarDocument(t, spelling)
			if err == nil {
				t.Fatalf("expected refusal for example %s", spelling)
			}
		})
	}
}

func assertYAMLScalarExample(t *testing.T, spelling, wantJSON string) {
	t.Helper()
	tree, err := synthesizeYAMLScalarDocument(t, spelling)
	if err != nil {
		t.Fatalf("synthesize %s: %v", spelling, err)
	}
	got := yamlScalarExample(t, tree)
	if got != wantJSON {
		t.Fatalf("example %s emitted %s, want %s", spelling, got, wantJSON)
	}
}

func synthesizeYAMLScalarDocument(t *testing.T, spelling string) (map[string]any, error) {
	t.Helper()
	document := fmt.Sprintf(`openapi: 3.0.0
info:
  title: scalars
  version: 1.0.0
servers:
  - url: https://scalars.example
paths:
  /probe:
    get:
      operationId: probe
      parameters:
        - name: value
          in: query
          schema:
            type: string
            example: %s
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                type: string
`, spelling)
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	result, err := NewSynthesizer().SynthesizeInterfaceWithCoverage(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
			BindingSpec: "openbindings.openapi@1",
			Location:    "https://scalars.example/openapi.yaml",
			Content:     content,
		}},
	})
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result.Interface)
	if err != nil {
		return nil, err
	}
	var tree map[string]any
	if err := json.Unmarshal(encoded, &tree); err != nil {
		t.Fatalf("unmarshal emitted interface: %v", err)
	}
	return tree, nil
}

func yamlScalarExample(t *testing.T, tree map[string]any) string {
	t.Helper()
	node := any(tree)
	for _, step := range []string{"operations", "probe", "input", "properties", "value", "example"} {
		object, ok := node.(map[string]any)
		if !ok {
			t.Fatalf("emitted interface has no %s", strings.Join([]string{step}, ""))
		}
		next, present := object[step]
		if !present {
			t.Fatalf("emitted interface has no %s", step)
		}
		node = next
	}
	encoded, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("marshal example: %v", err)
	}
	return string(encoded)
}
