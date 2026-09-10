package schemaprofile

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This is optional comparison-profile conformance, not Core conformance.
// Exact JSON text reaches the existing engine; red cases are implementation
// gaps, not expected failures. No exact comparator is implemented in this test.
type exactCase struct {
	ID, Rule, Mode, Direction, LeftJSON, RightJSON, Reason string
	Expected                                               struct{ Verdict, Error string }
	NumberTokens                                           map[string][]string
	LeftUnionConstOrder                                    []string
}

func exactDecode(t *testing.T, raw string, tokens []string) map[string]any {
	t.Helper()
	if !json.Valid([]byte(raw)) {
		t.Fatal("invalid fixture JSON")
	}
	var doc map[string]any
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.UseNumber()
	if err := d.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	got := []string{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case json.Number:
			got = append(got, x.String())
		case map[string]any:
			for _, v := range x {
				walk(v)
			}
		case []any:
			for _, v := range x {
				walk(v)
			}
		}
	}
	walk(doc)
	sort.Strings(got)
	if !reflect.DeepEqual(got, tokens) {
		t.Fatalf("fixture ingress changed tokens: %v != %v", got, tokens)
	}
	return doc
}
func loadExactCases(t *testing.T) []exactCase {
	t.Helper()
	dir := filepath.Join(comparisonCorpusDir(t), "comparison")
	var m struct{ ExactValues string }
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.ExactValues != "exact-values.json" {
		t.Fatal("missing required exact-value pack")
	}
	data, err = os.ReadFile(filepath.Join(dir, m.ExactValues))
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		Scope, Profile, ProfileVersion string
		Cases                          []exactCase
	}
	if err = json.Unmarshal(data, &pack); err != nil {
		t.Fatal(err)
	}
	if pack.Scope != "comparison-profile" || pack.Profile != "OB-2020-12" || pack.ProfileVersion != "0.1" || len(pack.Cases) == 0 {
		t.Fatal("invalid profile corpus identity")
	}
	return pack.Cases
}

func TestComparisonProfile_ExactValues(t *testing.T) {
	for _, c := range loadExactCases(t) {
		t.Run(c.ID, func(t *testing.T) {
			left := exactDecode(t, c.LeftJSON, c.NumberTokens["left"])
			right := exactDecode(t, c.RightJSON, c.NumberTokens["right"])
			slot := func(d map[string]any) map[string]any {
				return d["operations"].(map[string]any)["test"].(map[string]any)[c.Direction].(map[string]any)
			}
			a, err := (&Normalizer{Root: left}).Normalize(slot(left))
			var b map[string]any
			if err == nil {
				b, err = (&Normalizer{Root: right}).Normalize(slot(right))
			}
			if c.Expected.Error == "schema" {
				var se *SchemaError
				if !errors.As(err, &se) {
					t.Fatalf("wanted schema merge error, got %v", err)
				}
				return
			}
			if c.Expected.Verdict == "indeterminate" {
				var oe *OutsideProfileError
				if !errors.As(err, &oe) {
					t.Fatalf("wanted outside-profile result, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unmet exact comparison capability: %v", err)
			}
			if c.LeftUnionConstOrder != nil {
				got := []string{}
				for _, v := range a["anyOf"].([]any) {
					n := v.(map[string]any)["const"]
					raw, _ := json.Marshal(n)
					got = append(got, string(raw))
				}
				if !reflect.DeepEqual(got, c.LeftUnionConstOrder) {
					t.Errorf("authored union order: %v != %v", got, c.LeftUnionConstOrder)
				}
			}
			var compatible bool
			var reason string
			if c.Mode == "identical" {
				compatible, err = EqualNormalizedSchemas(a, b)
			} else if c.Direction == "input" {
				compatible, reason, err = InputCompatible(a, b)
			} else {
				compatible, reason, err = OutputCompatible(a, b)
			}
			if err != nil {
				t.Fatalf("unmet exact comparison capability: %v", err)
			}
			got := "incompatible"
			if compatible {
				got = "compatible"
			}
			if got != c.Expected.Verdict {
				t.Errorf("%s: got %s, want %s", c.Rule, got, c.Expected.Verdict)
			}
			if c.Reason != "" && !strings.HasPrefix(reason, strings.SplitN(c.Reason, ":", 2)[0]+":") {
				t.Errorf("profile deciding-keyword diagnostic: %q", reason)
			}
		})
	}
}

func TestOfficialSDKQualification_ExactComparisonDiagnostics(t *testing.T) {
	for _, c := range loadExactCases(t) {
		if c.Reason == "" {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			normalize := func(raw string, tokens []string) map[string]any {
				doc := exactDecode(t, raw, tokens)
				schema := doc["operations"].(map[string]any)["test"].(map[string]any)[c.Direction].(map[string]any)
				out, err := (&Normalizer{Root: doc}).Normalize(schema)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			a := normalize(c.LeftJSON, c.NumberTokens["left"])
			b := normalize(c.RightJSON, c.NumberTokens["right"])
			var reason string
			var err error
			if c.Direction == "input" {
				_, reason, err = InputCompatible(a, b)
			} else {
				_, reason, err = OutputCompatible(a, b)
			}
			if err != nil {
				t.Fatal(err)
			}
			if reason != c.Reason {
				t.Errorf("official SDK diagnostic qualification: %q != %q", reason, c.Reason)
			}
		})
	}
}
