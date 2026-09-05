// Package processorscenarios provides the language-neutral runner primitives
// for the binding-specification processor corpus published by the OpenBindings
// specification repository. Family packages supply an Adapter that turns a
// semantic scenario into one normalized Observation; this package owns corpus
// loading and assertion semantics so every Go adapter judges the same contract.
package processorscenarios

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
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
	// NotContains pins the ABSENCE of a substring or member: a header never
	// emitted, a field never serialized. Corpus revision 3.
	NotContains any `json:"notContains,omitempty"`
	// SemanticEquals interprets a complete wire representation before exact
	// JSON comparison. Corpus revision 5.
	SemanticEquals any `json:"semanticEquals,omitempty"`

	equalsPresent      bool
	containsPresent    bool
	notContainsPresent bool
	semanticPresent    bool
}

// UnmarshalJSON preserves presence for equals:null, contains:null, and
// notContains:null.
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
	_, a.notContainsPresent = raw["notContains"]
	_, a.semanticPresent = raw["semanticEquals"]
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
//
// More than one format may be accepted, and a reader that understands the
// newest one should say so for its predecessors too when the revisions are
// additive: revision 3 only ADDS the `notContains` assertion to revision 2,
// so a revision-3 reader interprets a revision-2 file exactly as a revision-2
// reader would. Naming a single format instead couples the corpus and the
// engines into lockstep — neither repository's CI can be green until both
// merge — for a difference that changes nothing about how the older file
// reads.
func LoadPath(path, family string, formats ...string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if !slices.Contains(formats, file.Format) {
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
		case assertion.notContainsPresent:
			if contains(value, assertion.NotContains) {
				return fmt.Errorf("%s = %s, want NOT to contain %s", assertion.Path, printable(value), printable(assertion.NotContains))
			}
		case assertion.semanticPresent:
			interpreted, err := semanticValue(value, assertion.SemanticEquals)
			if err != nil {
				return fmt.Errorf("%s: %w", assertion.Path, err)
			}
			semantic, ok := assertion.SemanticEquals.(map[string]any)
			if !ok || !jsonEqual(interpreted, semantic["value"]) {
				return fmt.Errorf("%s semantic value = %s, want %s", assertion.Path, printable(interpreted), printable(semantic["value"]))
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

type semanticUnit struct {
	name        string
	value       string
	contentType string
}

func semanticValue(actual, raw any) (any, error) {
	assertion, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("semanticEquals must be an object")
	}
	kind, _ := assertion["as"].(string)
	switch kind {
	case "json-lines":
		text, ok := actual.(string)
		if !ok || text == "" || !strings.HasSuffix(text, "\n") {
			return nil, fmt.Errorf("invalid JSON Lines framing")
		}
		lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		values := make([]any, len(lines))
		for index, line := range lines {
			if err := decodeSemanticJSON([]byte(line), &values[index]); err != nil {
				return nil, err
			}
		}
		return values, nil
	case "json-sequence":
		text, ok := actual.(string)
		if !ok {
			return nil, fmt.Errorf("invalid JSON sequence body")
		}
		values := []any{}
		for len(text) > 0 {
			if text[0] != 0x1e {
				return nil, fmt.Errorf("JSON sequence frame omits RS")
			}
			end := strings.IndexByte(text[1:], '\n')
			if end < 0 {
				return nil, fmt.Errorf("JSON sequence frame omits LF")
			}
			end++
			var value any
			if err := decodeSemanticJSON([]byte(text[1:end]), &value); err != nil {
				return nil, err
			}
			values = append(values, value)
			text = text[end+1:]
		}
		return values, nil
	case "querystring-json":
		text, ok := actual.(string)
		if !ok {
			return nil, fmt.Errorf("querystring assertion requires a string")
		}
		mark := strings.IndexByte(text, '?')
		if mark < 0 {
			return nil, fmt.Errorf("URL has no query component")
		}
		decoded, err := semanticPercentDecode(text[mark+1:], false)
		if err != nil {
			return nil, err
		}
		var value any
		return value, decodeSemanticJSON([]byte(decoded), &value)
	case "query-json-parameter", "form-json-field":
		text, ok := actual.(string)
		if !ok {
			return nil, fmt.Errorf("named assertion requires a string")
		}
		if kind == "query-json-parameter" {
			mark := strings.IndexByte(text, '?')
			if mark < 0 {
				return nil, fmt.Errorf("URL has no query")
			}
			text = text[mark+1:]
		}
		units, err := semanticNamedUnits(text, kind == "form-json-field")
		if err != nil {
			return nil, err
		}
		if err := semanticCheckNames(units, assertion); err != nil {
			return nil, err
		}
		unit, err := semanticSelectedUnit(units, assertion["name"])
		if err != nil {
			return nil, err
		}
		var value any
		return value, decodeSemanticJSON([]byte(unit.value), &value)
	case "multipart-json-part":
		dispatch, ok := actual.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("multipart assertion requires a dispatch object")
		}
		headers, _ := dispatch["headers"].(map[string]any)
		contentType := fmt.Sprint(headers["content-type"])
		boundary := semanticBoundary(contentType)
		body, _ := dispatch["body"].(string)
		if boundary == "" || body == "" {
			return nil, fmt.Errorf("invalid multipart wrapper")
		}
		parts, err := semanticMultipartParts(body, boundary)
		if err != nil {
			return nil, err
		}
		if err := semanticCheckNames(parts, assertion); err != nil {
			return nil, err
		}
		part, err := semanticSelectedUnit(parts, assertion["name"])
		if err != nil {
			return nil, err
		}
		if part.contentType != "application/json" {
			return nil, fmt.Errorf("multipart JSON part has wrong Content-Type %q", part.contentType)
		}
		var value any
		return value, decodeSemanticJSON([]byte(part.value), &value)
	default:
		return nil, fmt.Errorf("unknown semantic interpreter %q", kind)
	}
}

func decodeSemanticJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("JSON representation contains more than one value")
		}
		return err
	}
	return nil
}

func semanticNamedUnits(raw string, plusAsSpace bool) ([]semanticUnit, error) {
	if raw == "" {
		return nil, nil
	}
	result := []semanticUnit{}
	for _, rawUnit := range strings.Split(raw, "&") {
		name, value := rawUnit, ""
		if split := strings.IndexByte(rawUnit, '='); split >= 0 {
			name, value = rawUnit[:split], rawUnit[split+1:]
		}
		decodedName, err := semanticPercentDecode(name, plusAsSpace)
		if err != nil {
			return nil, err
		}
		decodedValue, err := semanticPercentDecode(value, plusAsSpace)
		if err != nil {
			return nil, err
		}
		result = append(result, semanticUnit{name: decodedName, value: decodedValue})
	}
	return result, nil
}

func semanticPercentDecode(value string, plusAsSpace bool) (string, error) {
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			continue
		}
		if index+2 >= len(value) || !strings.ContainsRune("0123456789ABCDEF", rune(value[index+1])) || !strings.ContainsRune("0123456789ABCDEF", rune(value[index+2])) {
			return "", fmt.Errorf("percent triplets must use uppercase hex")
		}
		index += 2
	}
	if plusAsSpace {
		value = strings.ReplaceAll(value, "+", " ")
	}
	return url.PathUnescape(value)
}

func semanticBoundary(contentType string) string {
	for _, part := range strings.Split(contentType, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), "boundary=") {
			return strings.Trim(strings.TrimSpace(part[len("boundary="):]), "\"")
		}
	}
	return ""
}

var semanticPartName = regexp.MustCompile(`(?i); name="((?:[^"\\]|\\.)*)"$`)

func semanticMultipartParts(body, boundary string) ([]semanticUnit, error) {
	delimiter := "--" + boundary
	if !strings.HasPrefix(body, delimiter+"\r\n") || !strings.HasSuffix(body, delimiter+"--\r\n") {
		return nil, fmt.Errorf("multipart body does not consume the exact wrapper")
	}
	rawParts := strings.Split(body, delimiter)
	result := []semanticUnit{}
	for _, raw := range rawParts[1:] {
		if raw == "--\r\n" {
			continue
		}
		if !strings.HasPrefix(raw, "\r\n") || !strings.HasSuffix(raw, "\r\n") {
			return nil, fmt.Errorf("malformed multipart delimiter framing")
		}
		raw = strings.TrimPrefix(raw, "\r\n")
		split := strings.Index(raw, "\r\n\r\n")
		if split < 0 {
			return nil, fmt.Errorf("multipart part has no header boundary")
		}
		headers := strings.Split(raw[:split], "\r\n")
		disposition, contentType := "", ""
		for _, header := range headers {
			switch {
			case strings.HasPrefix(strings.ToLower(header), "content-disposition:"):
				disposition = header
			case strings.HasPrefix(strings.ToLower(header), "content-type:"):
				contentType = strings.TrimSpace(header[len("content-type:"):])
			}
		}
		match := semanticPartName.FindStringSubmatch(disposition)
		if len(match) != 2 || strings.Contains(strings.ToLower(disposition), "filename=") || strings.Contains(strings.ToLower(disposition), "filename*=") {
			return nil, fmt.Errorf("multipart part name is not exact generated form")
		}
		value := strings.TrimSuffix(raw[split+4:], "\r\n")
		name := strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(match[1])
		result = append(result, semanticUnit{name: name, value: value, contentType: contentType})
	}
	return result, nil
}

func semanticCheckNames(units []semanticUnit, assertion map[string]any) error {
	wantedRaw, _ := assertion["names"].([]any)
	wanted := make([]string, len(wantedRaw))
	for index := range wantedRaw {
		wanted[index] = fmt.Sprint(wantedRaw[index])
	}
	actual := make([]string, len(units))
	for index := range units {
		actual[index] = units[index].name
	}
	sort.Strings(actual)
	sort.Strings(wanted)
	if !reflect.DeepEqual(actual, wanted) {
		return fmt.Errorf("contribution names = %v, want %v", actual, wanted)
	}
	return nil
}

func semanticSelectedUnit(units []semanticUnit, rawName any) (semanticUnit, error) {
	name := fmt.Sprint(rawName)
	selected := []semanticUnit{}
	for _, unit := range units {
		if unit.name == name {
			selected = append(selected, unit)
		}
	}
	if len(selected) != 1 {
		return semanticUnit{}, fmt.Errorf("expected one %q contribution, got %d", name, len(selected))
	}
	return selected[0], nil
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
