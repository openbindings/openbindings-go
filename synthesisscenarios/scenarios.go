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
	"github.com/openbindings/openbindings-go/processorscenarios"
)

// Format is the exact portable synthesis-scenario format this runner
// implements. A file naming any other revision is refused rather than run:
// a runner that silently skips what it does not understand reports green
// having verified none of it.
const Format = "openbindings.binding-spec-synthesis-scenarios@3"

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
	// Resources is the scenario's closed, immutable companion-document set,
	// keyed by absolute retrieval URI. Harness input only: the family adapter
	// serves it through its ordinary artifact resolver, and it adds no
	// comparison semantics.
	Resources map[string]any `json:"resources,omitempty"`
	Expected  Expected       `json:"expected"`
}

type Expected struct {
	Outcome    string                         `json:"outcome"`
	Rules      []string                       `json:"rules,omitempty"`
	Operations []string                       `json:"operations"`
	Bindings   []BindingIdentity              `json:"bindings"`
	Assertions []processorscenarios.Assertion `json:"assertions,omitempty"`
	Coverage   ExpectedCoverage               `json:"coverage"`
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
	if file.Format != Format {
		return nil, fmt.Errorf("%s: unsupported synthesis scenario format %q", family, file.Format)
	}
	if file.Family != family || len(file.Scenarios) == 0 {
		return nil, fmt.Errorf("%s: malformed synthesis family file", family)
	}
	return &file, nil
}

// SynthesizerFactory builds the synthesizer that executes one scenario. A
// scenario may declare companion documents, so the synthesizer is constructed
// per scenario rather than once per family: the family adapter wires
// scenario.Resources into its ordinary artifact resolver seam and serves them
// offline.
type SynthesizerFactory func(scenario Scenario) (openbindings.CoverageSynthesizer, error)

// Fixed adapts one synthesizer for families whose corpus sources are
// self-contained. A scenario declaring companion documents is refused loudly
// rather than executed against a resolver that would never see them.
//
// The refusal is deliberately outside the expected-outcome handling in Verify:
// a scenario the runner cannot serve is a runner defect, and reporting it as a
// satisfied "refused" outcome would be the silent green this whole revision
// exists to prevent. `fixedSynthesizer` in @openbindings/sdk is the twin, with
// the same message and the same placement.
func Fixed(synth openbindings.CoverageSynthesizer) SynthesizerFactory {
	return func(scenario Scenario) (openbindings.CoverageSynthesizer, error) {
		if len(scenario.Resources) > 0 {
			return nil, fmt.Errorf("%s: declares companion resources, which this family's runner does not serve", scenario.ID)
		}
		return synth, nil
	}
}

// Verify executes every scenario for one family through its real coverage
// synthesizer and compares the normalized OpenBindings boundary.
func Verify(ctx context.Context, root, family string, factory SynthesizerFactory) error {
	file, err := Load(root, family)
	if err != nil {
		return err
	}
	for _, scenario := range file.Scenarios {
		synth, err := factory(scenario)
		if err != nil {
			return err
		}
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
	if err := checkAssertions(s, result.Interface); err != nil {
		return err
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

// checkAssertions evaluates a scenario's optional pointer-addressed assertions
// against the emitted OBI document, through the same evaluator the processor
// corpus uses. Each assertion pins exactly the fact its finding is about; the
// compared surface for everything else stays operations, bindings and coverage.
func checkAssertions(s Scenario, iface *openbindings.Interface) error {
	if len(s.Expected.Assertions) == 0 {
		return nil
	}
	encoded, err := json.Marshal(iface)
	if err != nil {
		return fmt.Errorf("%s: marshal emitted interface: %w", s.ID, err)
	}
	var document any
	if err := json.Unmarshal(encoded, &document); err != nil {
		return fmt.Errorf("%s: reparse emitted interface: %w", s.ID, err)
	}
	if err := processorscenarios.CheckAssertions(document, s.Expected.Assertions); err != nil {
		return fmt.Errorf("%s emitted-document assertion failed: %w", s.ID, err)
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
	// Assertions are evaluated against the emitted document, not diffed as
	// part of the normalized identity/coverage surface.
	out.Assertions = nil
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
