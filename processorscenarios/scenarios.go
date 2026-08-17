// Package processorscenarios provides the language-neutral runner primitives
// for the binding-specification processor corpus published by the OpenBindings
// specification repository. Family packages supply an Adapter that turns a
// semantic scenario into one normalized Observation; this package owns corpus
// loading and assertion semantics so every Go adapter judges the same contract.
package processorscenarios

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// File is one family processor-scenario file.
type File struct {
	Format      string     `json:"format"`
	BindingSpec string     `json:"bindingSpec"`
	Family      string     `json:"family"`
	Description string     `json:"description"`
	Scenarios   []Scenario `json:"scenarios"`
}

// Scenario is one portable semantic processor scenario.
type Scenario struct {
	ID          string     `json:"id"`
	Rules       []string   `json:"rules"`
	Section     string     `json:"section"`
	Description string     `json:"description"`
	Given       Given      `json:"given"`
	Expected    []Expected `json:"expected"`
}

// Given carries source, binding, semantic configuration, invocation, peer,
// and runtime facts. Raw maps deliberately preserve family-specific content.
type Given struct {
	Source        map[string]any `json:"source"`
	Binding       map[string]any `json:"binding"`
	Configuration map[string]any `json:"configuration,omitempty"`
	Invocation    map[string]any `json:"invocation"`
	Peer          map[string]any `json:"peer,omitempty"`
	Runtime       map[string]any `json:"runtime,omitempty"`
	Resources     map[string]any `json:"resources,omitempty"`
}

// Expected is one permitted observation alternative. Alternative order has
// no preference semantics.
type Expected struct {
	Disposition string      `json:"disposition"`
	Phase       string      `json:"phase"`
	Description string      `json:"description,omitempty"`
	Assertions  []Assertion `json:"assertions"`
}

// Assertion applies one portable comparison to a JSON Pointer in Data.
type Assertion struct {
	Path      string `json:"path"`
	Equals    any    `json:"equals,omitempty"`
	Absent    bool   `json:"absent,omitempty"`
	OneOf     []any  `json:"oneOf,omitempty"`
	SetEquals []any  `json:"setEquals,omitempty"`
	Contains  any    `json:"contains,omitempty"`

	equalsPresent   bool
	containsPresent bool
}

// UnmarshalJSON preserves presence for equals:null and contains:null.
func (a *Assertion) UnmarshalJSON(data []byte) error {
	type wire Assertion
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*a = Assertion(decoded)
	_, a.equalsPresent = raw["equals"]
	_, a.containsPresent = raw["contains"]
	return nil
}

// Observation is the family-neutral result of executing one scenario.
// Data contains dispatch, outputs, trace, and any family-specific observable
// facts addressed by the corpus assertions.
type Observation struct {
	Disposition string
	Phase       string
	Data        map[string]any
}

// CorpusRoot resolves the spec conformance root using the same environment
// contract as the existing SDK corpus suites.
func CorpusRoot(fallback string) (string, error) {
	root := os.Getenv("OB_SPEC_CORPUS")
	if root == "" {
		root = fallback
	}
	if root == "" {
		return "", errors.New("OB_SPEC_CORPUS is unset and no fallback was supplied")
	}
	if _, err := os.Stat(filepath.Join(root, "binding-specs", "processor")); err != nil {
		return "", fmt.Errorf("binding-spec processor corpus not found under %s: %w", root, err)
	}
	return root, nil
}

// Load reads and minimally validates one family file.
func Load(root, family string) (*File, error) {
	return LoadPath(filepath.Join(root, "binding-specs", "processor", family+".json"), family, "openbindings.binding-spec-processor-scenarios@1")
}

// LoadPath reads a scenario file from an explicit path. It lets stronger
// project profiles reuse the same language-neutral harness without claiming
// that their assertions are published binding-specification conformance.
func LoadPath(path, family, format string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.Format != format {
		return nil, fmt.Errorf("%s: unsupported scenario format %q", family, file.Format)
	}
	if file.Family != family || len(file.Scenarios) == 0 {
		return nil, fmt.Errorf("%s: malformed family file", family)
	}
	return &file, nil
}

// Match succeeds when an observation satisfies one complete expected
// alternative. It returns the matching alternative index for differential
// reference-SDK parity reporting.
func Match(s Scenario, got Observation) (int, error) {
	var failures []string
	for i, expected := range s.Expected {
		if err := matchAlternative(expected, got); err == nil {
			return i, nil
		} else {
			failures = append(failures, fmt.Sprintf("alternative %d: %v", i+1, err))
		}
	}
	return -1, fmt.Errorf("%s matched no permitted alternative:\n%s", s.ID, strings.Join(failures, "\n"))
}

func matchAlternative(expected Expected, got Observation) error {
	if got.Disposition != expected.Disposition {
		return fmt.Errorf("disposition = %q, want %q", got.Disposition, expected.Disposition)
	}
	if got.Phase != expected.Phase {
		return fmt.Errorf("phase = %q, want %q", got.Phase, expected.Phase)
	}
	return CheckAssertions(got.Data, expected.Assertions)
}

// CheckAssertions applies every assertion to one JSON-shaped root value. It is
// exported so the other portable-corpus runners in this module evaluate the
// shared assertion vocabulary through this evaluator instead of reimplementing
// it; the synthesis corpus addresses an emitted OBI document with the same five
// verbs this corpus addresses a normalized observation with.
func CheckAssertions(root any, assertions []Assertion) error {
	for _, assertion := range assertions {
		value, present, err := pointer(root, assertion.Path)
		if err != nil {
			return err
		}
		if assertion.Absent {
			if present {
				return fmt.Errorf("%s is present (%s), want absent", assertion.Path, printable(value))
			}
			continue
		}
		if !present {
			return fmt.Errorf("%s is absent", assertion.Path)
		}
		switch {
		case assertion.equalsPresent:
			if !jsonEqual(value, assertion.Equals) {
				return fmt.Errorf("%s = %s, want %s", assertion.Path, printable(value), printable(assertion.Equals))
			}
		case assertion.OneOf != nil:
			ok := false
			for _, candidate := range assertion.OneOf {
				if jsonEqual(value, candidate) {
					ok = true
					break
				}
			}
			if !ok {
				return fmt.Errorf("%s = %s, want one of %s", assertion.Path, printable(value), printable(assertion.OneOf))
			}
		case assertion.SetEquals != nil:
			if !setEqual(value, assertion.SetEquals) {
				return fmt.Errorf("%s = %s, want set %s", assertion.Path, printable(value), printable(assertion.SetEquals))
			}
		case assertion.containsPresent:
			if !contains(value, assertion.Contains) {
				return fmt.Errorf("%s = %s, want to contain %s", assertion.Path, printable(value), printable(assertion.Contains))
			}
		default:
			return fmt.Errorf("%s has no comparison operator", assertion.Path)
		}
	}
	return nil
}

func pointer(root any, path string) (any, bool, error) {
	if path == "" {
		return root, true, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, false, fmt.Errorf("invalid JSON Pointer %q", path)
	}
	cur := root
	for _, encoded := range strings.Split(path[1:], "/") {
		segment := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			var ok bool
			cur, ok = node[segment]
			if !ok {
				return nil, false, nil
			}
		case []any:
			var idx int
			if _, err := fmt.Sscanf(segment, "%d", &idx); err != nil || idx < 0 || idx >= len(node) {
				return nil, false, nil
			}
			cur = node[idx]
		default:
			return nil, false, nil
		}
	}
	return cur, true, nil
}

func normalized(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}

func jsonEqual(a, b any) bool { return reflect.DeepEqual(normalized(a), normalized(b)) }

func setEqual(actual any, expected []any) bool {
	list, ok := normalized(actual).([]any)
	if !ok || len(list) != len(expected) {
		return false
	}
	used := make([]bool, len(list))
	for _, want := range expected {
		found := false
		for i, got := range list {
			if !used[i] && jsonEqual(got, want) {
				used[i], found = true, true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func contains(actual, needle any) bool {
	switch value := normalized(actual).(type) {
	case string:
		part, ok := normalized(needle).(string)
		return ok && strings.Contains(value, part)
	case []any:
		for _, item := range value {
			if jsonEqual(item, needle) {
				return true
			}
		}
	case map[string]any:
		if key, ok := needle.(string); ok {
			_, ok = value[key]
			return ok
		}
	}
	return false
}

func printable(v any) string {
	b, err := json.Marshal(v)
	if err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

// CanonicalKeys is useful to adapters normalizing unordered maps into stable
// dispatch traces.
func CanonicalKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
