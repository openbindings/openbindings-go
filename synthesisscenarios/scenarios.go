// Package synthesisscenarios provides runner primitives for the portable
// binding-specification synthesis corpus. Family packages supply their real
// synthesizer; this package normalizes only behavior visible at the
// OpenBindings boundary.
package synthesisscenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	openbindings "github.com/openbindings/openbindings-go"
)

type File struct {
	Format      string     `json:"format"`
	BindingSpec string     `json:"bindingSpec"`
	Family      string     `json:"family"`
	Description string     `json:"description"`
	Scenarios   []Scenario `json:"scenarios"`
}

type Scenario struct {
	ID          string                        `json:"id"`
	Description string                        `json:"description"`
	Source      openbindings.SynthesizeSource `json:"source"`
	Expected    Expected                      `json:"expected"`
}

type Expected struct {
	Outcome    string            `json:"outcome"`
	Rules      []string          `json:"rules,omitempty"`
	Operations []string          `json:"operations"`
	Bindings   []BindingIdentity `json:"bindings"`
	Coverage   ExpectedCoverage  `json:"coverage"`
}

type BindingIdentity struct {
	OperationKey string `json:"operationKey"`
	BindingRef   string `json:"bindingRef"`
}

type ExpectedCoverage struct {
	Exhaustive       bool            `json:"exhaustive"`
	FullyRepresented bool            `json:"fullyRepresented"`
	Entries          []CoverageEntry `json:"entries"`
}

type CoverageEntry struct {
	SourceIndex  int      `json:"sourceIndex"`
	SourceRef    string   `json:"sourceRef"`
	Scope        string   `json:"scope"`
	Status       string   `json:"status"`
	OperationKey string   `json:"operationKey,omitempty"`
	BindingRef   string   `json:"bindingRef,omitempty"`
	ReasonCode   string   `json:"reasonCode,omitempty"`
	Rule         string   `json:"rule,omitempty"`
	Requirements []string `json:"requirements"`
}

func Load(root, family string) (*File, error) {
	data, err := os.ReadFile(filepath.Join(root, "binding-specs", "synthesis", family+".json"))
	if err != nil {
		return nil, err
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.Format != "openbindings.binding-spec-synthesis-scenarios@2" {
		return nil, fmt.Errorf("%s: unsupported synthesis scenario format %q", family, file.Format)
	}
	if file.Family != family || len(file.Scenarios) == 0 {
		return nil, fmt.Errorf("%s: malformed synthesis family file", family)
	}
	return &file, nil
}

// Verify executes every scenario for one family through its real coverage
// synthesizer and compares the normalized OpenBindings boundary.
func Verify(ctx context.Context, root, family string, synth openbindings.CoverageSynthesizer) error {
	file, err := Load(root, family)
	if err != nil {
		return err
	}
	for _, scenario := range file.Scenarios {
		result, err := synth.SynthesizeInterfaceWithCoverage(ctx, &openbindings.SynthesizeInput{
			Sources: []openbindings.SynthesizeSource{scenario.Source},
		})
		if scenario.Expected.Outcome == "refused" {
			if err == nil {
				return fmt.Errorf("%s: synthesis succeeded; the corpus requires whole-source refusal", scenario.ID)
			}
			continue
		}
		if scenario.Expected.Outcome != "synthesized" {
			return fmt.Errorf("%s: unsupported expected outcome %q", scenario.ID, scenario.Expected.Outcome)
		}
		if err != nil {
			return fmt.Errorf("%s: synthesis failed: %w", scenario.ID, err)
		}
		if err := Match(scenario, result); err != nil {
			return err
		}
	}
	return nil
}

func Match(s Scenario, result *openbindings.SynthesizeResult) error {
	if result == nil || result.Interface == nil {
		return fmt.Errorf("%s: synthesizer returned no interface", s.ID)
	}
	got := Expected{
		Outcome:    "synthesized",
		Operations: operationKeys(result.Interface),
		Bindings:   bindingIdentities(result.Interface),
		Coverage: ExpectedCoverage{
			Exhaustive:       result.Coverage.Exhaustive,
			FullyRepresented: result.Coverage.FullyRepresented,
			Entries:          coverageEntries(result.Coverage.Entries),
		},
	}
	want := normalizedExpected(s.Expected)
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		return fmt.Errorf("%s synthesis mismatch\ngot:\n%s\nwant:\n%s", s.ID, gotJSON, wantJSON)
	}
	return nil
}

func operationKeys(iface *openbindings.Interface) []string {
	out := make([]string, 0, len(iface.Operations))
	for key := range iface.Operations {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func bindingIdentities(iface *openbindings.Interface) []BindingIdentity {
	out := make([]BindingIdentity, 0, len(iface.Bindings))
	for _, binding := range iface.Bindings {
		out = append(out, BindingIdentity{OperationKey: binding.Operation, BindingRef: binding.Ref})
	}
	sortBindings(out)
	return out
}

func coverageEntries(entries []openbindings.SynthesisCoverageEntry) []CoverageEntry {
	out := make([]CoverageEntry, 0, len(entries))
	for _, entry := range entries {
		requirements := append([]string{}, entry.Requirements...)
		sort.Strings(requirements)
		out = append(out, CoverageEntry{
			SourceIndex:  entry.SourceIndex,
			SourceRef:    entry.SourceRef,
			Scope:        string(entry.Scope),
			Status:       string(entry.Status),
			OperationKey: entry.OperationKey,
			BindingRef:   entry.BindingRef,
			ReasonCode:   entry.ReasonCode,
			Rule:         entry.Rule,
			Requirements: requirements,
		})
	}
	sortCoverage(out)
	return out
}

func normalizedExpected(in Expected) Expected {
	out := in
	out.Rules = nil
	out.Operations = append([]string{}, in.Operations...)
	sort.Strings(out.Operations)
	out.Bindings = append([]BindingIdentity{}, in.Bindings...)
	sortBindings(out.Bindings)
	out.Coverage.Entries = append([]CoverageEntry{}, in.Coverage.Entries...)
	for i := range out.Coverage.Entries {
		out.Coverage.Entries[i].Requirements = append([]string{}, out.Coverage.Entries[i].Requirements...)
		sort.Strings(out.Coverage.Entries[i].Requirements)
	}
	sortCoverage(out.Coverage.Entries)
	return out
}

func sortBindings(values []BindingIdentity) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].OperationKey != values[j].OperationKey {
			return values[i].OperationKey < values[j].OperationKey
		}
		return values[i].BindingRef < values[j].BindingRef
	})
}

func sortCoverage(values []CoverageEntry) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].SourceIndex != values[j].SourceIndex {
			return values[i].SourceIndex < values[j].SourceIndex
		}
		if values[i].Scope != values[j].Scope {
			return values[i].Scope < values[j].Scope
		}
		return values[i].SourceRef < values[j].SourceRef
	})
}
