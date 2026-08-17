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

// The spellings this engine used to resolve by YAML 1.1 rules the accepted
// OAS editions do not admit — F-O1-7, closed. Under YAML 1.2.2 §10.3.2 a
// leading zero is a decimal digit ("[-+]? [0-9]+", base 10), and the
// underscore-bearing and 0b-prefixed forms match no pattern at all and are
// the strings they spell. The decode is kin-openapi's, whose DecodeOpts
// exposes only DisableTimestamps, so the conformant answer comes from this
// engine's own scalar-resolution layer (yaml_scalars.go) rather than from
// the decoder. These pin the value at the EMITTED boundary, which the
// shared case table cannot reach: it stops at the loaded document.
func TestSynthesizeInterface_YAMLScalarConvergedOnCoreResolution(t *testing.T) {
	converged := map[string]string{
		"017":     "17",
		"-017":    "-17",
		"0120":    "120",
		"010":     "10",
		"1_000":   `"1_000"`,
		"1_000.5": `"1_000.5"`,
		"0b101":   `"0b101"`,
		"-0b101":  `"-0b101"`,
		"0x_1F":   `"0x_1F"`,
		"-0x1F":   `"-0x1F"`,
		"-0o17":   `"-0o17"`,
		"-.5":     "-0.5",
	}
	for spelling, want := range converged {
		t.Run(spelling, func(t *testing.T) {
			assertYAMLScalarExample(t, spelling, want)
		})
	}
}

// A magnitude the double domain cannot hold keeps its source text. §10.3.2
// resolves it to a float, but §10.2.1.4 says "The supported range and
// accuracy depends on the implementation", which makes refusing, carrying
// ±Inf and clamping all permitted — a choice, not a deduction. Both twins
// keep the text, and the question is filed as F-O1-15. Underflow is not in
// that class and resolves to 0.
func TestSynthesizeInterface_YAMLScalarUnrepresentableMagnitude(t *testing.T) {
	for spelling, want := range map[string]string{
		"1e400":        `"1e400"`,
		"600e27371700": `"600e27371700"`,
		"1e-400":       "0",
	} {
		t.Run(spelling, func(t *testing.T) {
			assertYAMLScalarExample(t, spelling, want)
		})
	}
}

// `<<` matches no row in §10.3.2's table, so it is the string it spells
// wherever it appears — and YAML 1.1's merge type, which would otherwise
// give it meaning in key position, is outside the tag set OAS §4.2 permits.
// The KEY-position half of that (a `<<` entry merges nothing) is pinned by
// the shared twin case table and by portable scenario OAPI-SS-25; this pins
// the value crossing the emitted boundary.
func TestSynthesizeInterface_YAMLMergeSpellingIsAString(t *testing.T) {
	assertYAMLScalarExample(t, "<<", `"\u003c\u003c"`)
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
