package openbindings_test

// Consumer exercises for the core API: each example is a caller's code,
// written to show how the API serves it, and where a caller's own policy
// begins. Three callers:
//
//   - the 0.2 CLI (ob-cli-surface-lab at f7a9d16, NewNextSurfaceRoot):
//     reading a document, validating and reporting it, refusing an
//     unsupported version, editing in place, resolving an operation and
//     choosing its binding, checking a dependency's kinds, the media type,
//     the formatting boundary, and, on Document.References, schema and
//     operation rename, schema remove, and merge closure, with an
//     exact-value comparison for merge.
//     Validating values against value contracts needs an evaluator, so
//     those exercises are in schemaeval/consumer_examples_test.go;
//   - a producer: building a document in code, writing it, reading it back,
//     editing a schema with exact numbers, and amending a report;
//   - an evaluator author: a minimal evaluator here (typeOnly), and in
//     schemaeval/consumer_examples_test.go a third-party evaluator run
//     through openbindingstest.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/openbindings/openbindings-go"
)

// tasksOBI is the CLI lab's sample document (internal/cmd/surface_next_fixture.go),
// shortened to the members these exercises read.
const tasksOBI = `{
  "openbindings": "0.2.0",
  "name": "Task Manager",
  "version": "1.4.0",
  "schemas": {
    "Task": {
      "type": "object",
      "properties": { "id": { "type": "string" }, "title": { "type": "string" } },
      "required": ["id", "title"]
    }
  },
  "operations": {
    "createTask": {
      "aliases": ["acme.tasks.createTask"],
      "input": { "type": "object", "properties": { "title": { "type": "string" } }, "required": ["title"] },
      "output": { "$ref": "#/schemas/Task" }
    },
    "listTasks": {
      "input": { "type": "object", "maxProperties": 0 },
      "output": { "type": "array", "items": { "$ref": "#/schemas/Task" } }
    },
    "events.deliver": {
      "aliases": ["acme.events.deliver"],
      "input": { "type": "object" }
    }
  },
  "dependencies": {
    "notifier": { "operation": "events.deliver", "kinds": ["example.openapi@1", "example.grpc@1"] },
    "audit": { "operation": "events.deliver" }
  },
  "sources": {
    "httpApi": { "kind": "example.openapi@1", "content": { "location": "https://api.example.com/openapi.json" } },
    "mcpServer": { "kind": "example.mcp@1", "content": { "location": "https://api.example.com/mcp" } }
  },
  "bindings": {
    "createTask.http": { "operation": "createTask", "source": "httpApi", "preference": 10 },
    "createTask.mcp": { "operation": "createTask", "source": "mcpServer", "deprecated": true },
    "listTasks.http": { "operation": "listTasks", "source": "httpApi", "idempotent": true }
  }
}`

// ---------------------------------------------------------------- the CLI

// cliValidate is `ob validate <obi>`: the conclusion, the specification text
// applied (OBI-T-08), each finding located in the file, and the exit status
// the lab's table gives (0 conformant, 1 non-conformant, 3 refused, 4 no
// verdict).
func cliValidate(name string, data []byte) (exit int) {
	_, report, err := openbindings.ValidateDocument(data)
	var refusal *openbindings.VersionRefusalError
	if errors.As(err, &refusal) {
		// This SDK's version policy: reported instead of a conclusion.
		fmt.Printf("%s: refused, not judged: %s\n", name, refusal.Reason)
		return 3
	}
	// err is nil or a *ValidationError; the report already holds the same
	// violations, so the command reads the report alone.
	text := "OpenBindings " + report.Release
	if report.Revision != "" {
		text += " (working draft, spec revision " + report.Revision[:7] + ")"
	}
	rules := len(openbindings.DocumentRules())
	fmt.Printf("%s: %s\n", name, report.Conclusion)
	fmt.Printf("  checked against %s: %d of %d rules decided\n", text, rules-len(report.Inconclusive), rules)
	for _, finding := range report.Violations() {
		where := name
		if finding.Position.IsValid() {
			where += ":" + finding.Position.String()
		}
		fmt.Printf("  %s: %s: %s\n", where, finding.Rule, finding.Message)
	}
	if undecided := report.InconclusiveChecks(); len(undecided) > 0 {
		fmt.Printf("  %d checks undecided, the first %s at %q: %s\n", len(undecided), undecided[0].Rule, undecided[0].Path, undecided[0].Message)
	}
	switch report.Conclusion {
	case openbindings.ConclusionConformant:
		return 0
	case openbindings.ConclusionNonConformant:
		return 1
	default:
		return 4
	}
}

// The CLI's validate command over the four outcomes a document can have.
func Example_cliValidate() {
	documents := []struct{ name, text string }{
		{"tasks.obi.json", tasksOBI},
		{"broken.obi.json", "{\n  \"openbindings\": \"0.2.0\",\n  \"operations\": {},\n  \"bindings\": {\n    \"b\": { \"operation\": \"gone\", \"source\": \"s\" }\n  },\n  \"sources\": { \"s\": { \"kind\": \"\" } }\n}"},
		// A lone escaped surrogate: the SDK cannot read it in full.
		{"surrogate.obi.json", `{"openbindings":"0.2.0","operations":{},"x-note":"\ud800"}`},
		{"next.obi.json", `{"openbindings":"0.3.0","operations":{}}`},
	}
	for _, d := range documents {
		fmt.Println("exit", cliValidate(d.name, []byte(d.text)))
	}
	// Output:
	// tasks.obi.json: conformant
	//   checked against OpenBindings 0.2.0 (working draft, spec revision 1d5f08c): 13 of 13 rules decided
	// exit 0
	// broken.obi.json: non-conformant
	//   checked against OpenBindings 0.2.0 (working draft, spec revision 1d5f08c): 13 of 13 rules decided
	//   broken.obi.json:7:23: OBI-D-02: does not validate against the document schema: minLength: got 0, want 1
	//   broken.obi.json:5:12: OBI-D-07: references unknown operation key "gone"
	// exit 1
	// surrogate.obi.json: conformance-undetermined
	//   checked against OpenBindings 0.2.0 (working draft, spec revision 1d5f08c): 2 of 13 rules decided
	//   11 checks undecided, the first OBI-D-02 at "": a string at "/x-note" holds an escape of a lone UTF-16 surrogate, which this SDK does not carry, so this rule was not checked
	// exit 4
	// next.obi.json: refused, not judged: document declares version "0.3.0", newer than the release line this implementation supports (0.2.x)
	// exit 3
}

// cliRead is how a command that interprets a document (invoke, mcp, codegen)
// reads one: ParseDocument, then an exit status for each way it can fail.
func cliRead(data []byte) (*openbindings.Document, int, string) {
	doc, err := openbindings.ParseDocument(data)
	var refusal *openbindings.VersionRefusalError
	var violation *openbindings.ValidationError
	switch {
	case err == nil:
		return doc, 0, "read"
	case errors.As(err, &refusal):
		return nil, 3, "refused: declares " + refusal.Version
	case errors.As(err, &violation):
		first := violation.Findings[0]
		return nil, 1, fmt.Sprintf("non-conformant at %s: %s", first.Position, first.Rule)
	case errors.Is(err, openbindings.ErrInconclusive):
		// The SDK could not read the document in full (nesting past the
		// decoder, a lone surrogate, a document the model does not carry,
		// the document schema reaching no verdict): no verdict either way.
		return nil, 4, "no verdict: " + err.Error()
	}
	// ParseDocument returns no other error.
	return nil, 2, fmt.Sprintf("unexpected (%T): %v", err, err)
}

func Example_cliRead() {
	inputs := [][]byte{
		[]byte(tasksOBI),
		[]byte(`{"openbindings":"0.2.0-rc.1","operations":{}}`),
		[]byte(`{"openbindings":"0.2.0","operations":{},"unknown":1}`),
		[]byte(`{"openbindings":"0.2.0","operations":{},"x-note":"\udc00"}`),
		[]byte(`{"openbindings":"0.2.0","operations":{},"x-deep":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`),
	}
	// The unknown member's finding is located at the member itself (1:41).
	for _, data := range inputs {
		_, exit, what := cliRead(data)
		fmt.Println(exit, what)
	}
	// Output:
	// 0 read
	// 3 refused: declares 0.2.0-rc.1
	// 1 non-conformant at 1:41: OBI-D-02
	// 4 no verdict: openbindings: inconclusive: a string at "/x-note" holds an escape of a lone UTF-16 surrogate, which this SDK does not carry
	// 4 no verdict: openbindings: inconclusive: the input is nested deeper than the decoder reads (10000 levels), so it is not decoded
}

// An editor holds a document decoded with json.Unmarshal, which refuses
// nothing, so it makes the version decision itself before interpreting the
// document. CheckVersion returns the refusal ParseDocument would return, or
// nil: a text declaring no version ("0.2" is not SemVer, nor is "") is never
// refused, and OBI-D-09 reports it instead.
func Example_cliVersionDecision() {
	fmt.Println("this ob interprets OpenBindings", openbindings.SupportedVersions)
	for _, declared := range []string{`"0.2.0"`, `"0.2.7"`, `"0.2.0+build.5"`, `"0.3.0"`, `"0.2.0-rc.1"`, `"0.2"`, `""`} {
		var held openbindings.Document
		if err := json.Unmarshal([]byte(`{"openbindings":`+declared+`,"operations":{}}`), &held); err != nil {
			panic(err)
		}
		if err := openbindings.CheckVersion(held.OpenBindings); err != nil {
			fmt.Printf("%-15q %v\n", held.OpenBindings, err)
			continue
		}
		fmt.Printf("%-15q not refused\n", held.OpenBindings)
	}
	// Output:
	// this ob interprets OpenBindings 0.2.x
	// "0.2.0"         not refused
	// "0.2.7"         not refused
	// "0.2.0+build.5" not refused
	// "0.3.0"         openbindings: document declares version "0.3.0", newer than the release line this implementation supports (0.2.x)
	// "0.2.0-rc.1"    openbindings: document declares version "0.2.0-rc.1", a pre-release this implementation does not support
	// "0.2"           not refused
	// ""              not refused
}

// memberOrder lists an encoded object's top-level member names in order.
func memberOrder(data []byte) []string {
	var names []string
	dec := json.NewDecoder(bytes.NewReader(data))
	_, _ = dec.Token() // {
	for dec.More() {
		name, _ := dec.Token()
		names = append(names, name.(string))
		var skip json.RawMessage
		_ = dec.Decode(&skip)
	}
	return names
}

// cliEdit is an in-place edit (`ob operation add <obi> <name> ...`): read the
// document as it is, conformant or not, apply the change, and write it only
// when the result breaks no rule the document did not already break.
func cliEdit(data []byte, edit func(*openbindings.Document)) ([]byte, error) {
	// ParseDocument refuses a document violating the document schema, which
	// an editor must still open, so the editor reads with ValidateDocument:
	// the decoded document and the rules it already breaks.
	doc, before, err := openbindings.ValidateDocument(data)
	var refusal *openbindings.VersionRefusalError
	switch {
	case errors.As(err, &refusal):
		return nil, refusal
	case doc == nil:
		// The model does not carry the document (ValidateDocument's doc
		// lists when), whether or not a violation was established, so
		// there is nothing to edit; the report says what was decided.
		return nil, fmt.Errorf("cannot edit: %s", before.Conclusion)
	}
	had := map[string]bool{}
	for _, f := range before.Violations() {
		had[f.Rule+" "+f.Path] = true
	}
	edit(doc)
	// The edited value is judged again. Validate's error is a
	// *ValidationError (violations, compared below), a *VersionRefusalError
	// when the edit declares a version this SDK does not interpret, or an
	// encoding error when the edited value cannot be written; the last two
	// come with no report, so they end the edit here.
	after, err := doc.Validate()
	var violations *openbindings.ValidationError
	switch {
	case errors.As(err, &refusal):
		return nil, fmt.Errorf("refused: the edit declares a version this ob does not interpret: %w", refusal)
	case err != nil && !errors.As(err, &violations):
		return nil, fmt.Errorf("refused: the edited document cannot be written: %w", err)
	}
	var added []string
	for _, f := range after.Violations() {
		if !had[f.Rule+" "+f.Path] {
			added = append(added, f.Rule+" at "+f.Path)
		}
	}
	if len(added) > 0 {
		return nil, fmt.Errorf("refused: the change would add %s", strings.Join(added, ", "))
	}
	written, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("refused: the edited document cannot be written: %w", err)
	}
	return written, nil
}

func Example_cliEdit() {
	addOperation := func(key string, input openbindings.JSONSchema) func(*openbindings.Document) {
		return func(i *openbindings.Document) {
			i.Operations[key] = openbindings.Operation{Input: input}
		}
	}
	written, err := cliEdit([]byte(tasksOBI), addOperation("archiveTask", map[string]any{"$ref": "#/schemas/Task"}))
	fmt.Println(err, memberOrder(written))

	_, err = cliEdit([]byte(tasksOBI), addOperation("x", map[string]any{"type": 42}))
	fmt.Println(err)

	// An edit that declares a version this SDK does not interpret, and one
	// that holds bytes the model cannot write, end at the post-edit check.
	_, err = cliEdit([]byte(tasksOBI), func(i *openbindings.Document) { i.OpenBindings = "0.3.0" })
	fmt.Println(err)
	_, err = cliEdit([]byte(tasksOBI), func(i *openbindings.Document) {
		i.Extensions = map[string]json.RawMessage{"x-owner": json.RawMessage(`{"team":`)}
	})
	fmt.Println(err)

	// Member order: the same edit to a document holding an x- member writes
	// the typed members in field order and the kept member after them.
	withExtension := strings.Replace(tasksOBI, `"name"`, `"x-owner": "tasks-team", "name"`, 1)
	written, err = cliEdit([]byte(withExtension), addOperation("archiveTask", true))
	fmt.Println(err, memberOrder(written))

	// HTML escaping follows the caller's encoder: json.MarshalIndent writes
	// a description holding "<" as a \u003c escape, and an encoder set not
	// to escape HTML writes it as held.
	withMarkup := strings.Replace(tasksOBI, `"version": "1.4.0",`, `"version": "1.4.0", "description": "a<b",`, 1)
	written, _ = cliEdit([]byte(withMarkup), addOperation("archiveTask", true))
	escaped := `"a` + `\` + `u003cb"`
	fmt.Println(bytes.Contains(written, []byte(`"a<b"`)), bytes.Contains(written, []byte(escaped)))
	var held openbindings.Document
	if err := json.Unmarshal([]byte(withMarkup), &held); err != nil {
		panic(err)
	}
	var unescaped bytes.Buffer
	encoder := json.NewEncoder(&unescaped)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(held); err != nil {
		panic(err)
	}
	fmt.Println(bytes.Contains(unescaped.Bytes(), []byte(`"a<b"`)), bytes.Contains(unescaped.Bytes(), []byte(escaped)))
	// Output:
	// <nil> [openbindings name version schemas operations dependencies sources bindings]
	// refused: the change would add OBI-D-10 at /operations/x/input/type
	// refused: the edit declares a version this ob does not interpret: openbindings: document declares version "0.3.0", newer than the release line this implementation supports (0.2.x)
	// refused: the edited document cannot be written: openbindings: encode document: json: error calling MarshalJSON for type openbindings.Document: member "x-owner": unexpected end of JSON input
	// <nil> [openbindings name version schemas operations dependencies sources bindings x-owner]
	// false true
	// true false
}

// cliInvokeChoice is how `ob invoke <obi> <operation> [--binding B]...`
// chooses a binding: resolve the name (OBI-T-06), find the operation's
// bindings by its key, read each binding's source kind, and take the first
// named binding ob can invoke, or the sole one; otherwise refuse, listing
// the candidates with preference and deprecation, which are shown and never
// used to choose.
func cliInvokeChoice(doc *openbindings.Document, name string, canInvoke map[string]bool, named ...string) string {
	key, _, found := doc.ResolveOperation(name)
	if !found {
		return fmt.Sprintf("refused: no operation named %q", name)
	}
	bindings := doc.OperationBindings(key)
	kindOf := func(binding string) string {
		return doc.Sources[doc.Bindings[binding].Source].Kind
	}
	if len(named) > 0 {
		for _, b := range named {
			if !slices.Contains(bindings, b) {
				return fmt.Sprintf("refused: %s is not a binding of %s", b, key)
			}
		}
		for _, b := range named {
			if canInvoke[kindOf(b)] {
				return fmt.Sprintf("%s -> %s via %s (%s)", name, key, b, kindOf(b))
			}
		}
		return "refused: ob can invoke none of the named bindings"
	}
	var usable []string
	for _, b := range bindings {
		if canInvoke[kindOf(b)] {
			usable = append(usable, b)
		}
	}
	if len(usable) == 1 {
		return fmt.Sprintf("%s -> %s via %s (%s)", name, key, usable[0], kindOf(usable[0]))
	}
	var listing []string
	for _, b := range bindings {
		entry := doc.Bindings[b]
		signals := kindOf(b)
		if entry.Preference != nil {
			signals += fmt.Sprintf(", preference %d", *entry.Preference)
		}
		if openbindings.Value(entry.Deprecated) {
			signals += ", deprecated"
		}
		listing = append(listing, b+" ("+signals+")")
	}
	return fmt.Sprintf("refused: choose a binding of %s: %s", key, strings.Join(listing, "; "))
}

func Example_cliInvokeChoice() {
	doc, err := openbindings.ParseDocument([]byte(tasksOBI))
	if err != nil {
		panic(err)
	}
	httpOnly := map[string]bool{"example.openapi@1": true}
	both := map[string]bool{"example.openapi@1": true, "example.mcp@1": true}
	fmt.Println(cliInvokeChoice(doc, "acme.tasks.createTask", httpOnly))
	fmt.Println(cliInvokeChoice(doc, "createTask", both))
	fmt.Println(cliInvokeChoice(doc, "createTask", both, "createTask.mcp", "createTask.http"))
	fmt.Println(cliInvokeChoice(doc, "createTask", both, "listTasks.http"))
	fmt.Println(cliInvokeChoice(doc, "CreateTask", both))
	// Output:
	// acme.tasks.createTask -> createTask via createTask.http (example.openapi@1)
	// refused: choose a binding of createTask: createTask.http (example.openapi@1, preference 10); createTask.mcp (example.mcp@1, deprecated)
	// createTask -> createTask via createTask.mcp (example.mcp@1)
	// refused: listTasks.http is not a binding of createTask
	// refused: no operation named "CreateTask"
}

// A dependency's kinds (§5.5): `ob dependency list` shows "any" for an
// omitted list, and a composition tool asks, for each binding a provider
// document offers for the dependency's operation, whether the binding's
// source kind meets the any-of constraint.
func Example_cliDependencyKinds() {
	consumer, err := openbindings.ParseDocument([]byte(tasksOBI))
	if err != nil {
		panic(err)
	}
	provider, err := openbindings.ParseDocument([]byte(`{
	  "openbindings": "0.2.0",
	  "operations": { "deliver": { "aliases": ["acme.events.deliver"] } },
	  "sources": {
	    "rest": { "kind": "example.openapi@1" },
	    "rest2": { "kind": "example.openapi@2" },
	    "tools": { "kind": "example.mcp@1" }
	  },
	  "bindings": {
	    "deliver.rest": { "operation": "deliver", "source": "rest" },
	    "deliver.rest2": { "operation": "deliver", "source": "rest2" },
	    "deliver.tools": { "operation": "deliver", "source": "tools" }
	  }
	}`))
	if err != nil {
		panic(err)
	}
	for _, name := range slices.Sorted(maps.Keys(consumer.Dependencies)) {
		dependency := consumer.Dependencies[name]
		kinds := "any"
		if dependency.Kinds != nil {
			kinds = strings.Join(dependency.Kinds, ", ")
		}
		// The provider recognizes the consumed operation by a name the
		// consumer's operation carries: its key or an alias (correspondence).
		operation := consumer.Operations[dependency.Operation]
		var providerKey string
		for _, published := range append([]string{dependency.Operation}, operation.Aliases...) {
			if key, _, found := provider.ResolveOperation(published); found {
				providerKey = key
				break
			}
		}
		var meets []string
		for _, b := range provider.OperationBindings(providerKey) {
			if dependency.AcceptsKind(provider.Sources[provider.Bindings[b].Source].Kind) {
				meets = append(meets, b)
			}
		}
		fmt.Printf("%s (kinds: %s): %v\n", name, kinds, meets)
	}
	// Output:
	// audit (kinds: any): [deliver.rest deliver.rest2 deliver.tools]
	// notifier (kinds: example.openapi@1, example.grpc@1): [deliver.rest]
}

// ------------------------------------------------------------- a producer

// A producer builds a document in code, gates it before writing, writes it,
// and reads it back.
func Example_producer() {
	doc := openbindings.Document{
		OpenBindings: openbindings.AuthoringVersion,
		Name:         openbindings.Present("Tasks"),
		// A promoted field cannot be named in a composite literal, so an
		// extension member is set through the embedded LosslessFields.
		LosslessFields: openbindings.LosslessFields{
			Extensions: map[string]json.RawMessage{"x-generator": json.RawMessage(`"tasks-codegen"`)},
		},
		Schemas: map[string]openbindings.JSONSchema{
			"Task": map[string]any{
				"type":          "object",
				"required":      []string{"id"},
				"maxProperties": 8,
			},
		},
		Operations: map[string]openbindings.Operation{
			"createTask": {
				Aliases: []string{"acme.tasks.createTask"},
				Input:   map[string]any{"type": "object"},
				Output:  map[string]any{"$ref": "#/schemas/Task"},
				Examples: map[string]openbindings.OperationExample{
					"basic": {Input: json.RawMessage(`{"title":"Plan"}`), Output: json.RawMessage(`null`)},
				},
			},
			"ping": {Input: true},
		},
		Sources: map[string]openbindings.Source{
			"httpApi": {Kind: "example.openapi@1", Content: json.RawMessage(`{"location":"https://api.example.com/openapi.json"}`)},
		},
		Bindings: map[string]openbindings.Binding{
			// Present(10) would be a *int: the preference member is *int64.
			"createTask.http": {Operation: "createTask", Source: "httpApi", Preference: openbindings.Present[int64](10), Idempotent: openbindings.Present(false)},
		},
		Dependencies: map[string]openbindings.Dependency{
			"notifier": {Operation: "ping", Kinds: []string{"example.mcp@1"}},
		},
	}
	if _, err := doc.Validate(); err != nil {
		panic(err) // the gate: a violation was established
	}
	written, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(err)
	}
	back, err := openbindings.ParseDocument(written)
	if err != nil {
		panic(err)
	}
	rewritten, _ := json.MarshalIndent(back, "", "  ")
	fmt.Println("the same document:", bytes.Equal(written, rewritten))
	// The same document is not the same Go value: a schema decodes to
	// generic JSON values, every number a json.Number and every array an
	// []any (see JSONSchema). That is representation, not loss:
	// Example_producerSchemaEdit shows the exact round trip.
	fmt.Println("the same Go value:", reflect.DeepEqual(&doc, back))
	task, readTask := doc.Schemas["Task"].(map[string]any), back.Schemas["Task"].(map[string]any)
	fmt.Printf("maxProperties: %T then %T; required: %T then %T\n", task["maxProperties"], readTask["maxProperties"], task["required"], readTask["required"])
	fmt.Println("example output:", string(back.Operations["createTask"].Examples["basic"].Output))
	fmt.Println("extension:", string(back.Extensions["x-generator"]))
	// Output:
	// the same document: true
	// the same Go value: false
	// maxProperties: int then json.Number; required: []string then []interface {}
	// example output: null
	// extension: "tasks-codegen"
}

// deeplyNested is an input schema nesting subschemas 300 levels deep, where
// OBI-D-10's check meets this SDK's limit and stays inconclusive.
var deeplyNested = strings.Repeat(`{"not":`, 300) + `{}` + strings.Repeat(`}`, 300)

// amendRule is the whole amendment workflow, under the invariants
// ConcludeConformance's doc states. A tool that decides a rule this SDK left
// inconclusive replaces that rule's evidence and findings with its own
// decision for the entire rule, and the report stays coherent:
//   - the decision must settle the rule: satisfied, violated (with the
//     violations' findings), or not applicable; another inconclusive answer
//     is no amendment;
//   - the rule's old findings go, and the tool's take their place;
//   - the conclusion and the derived lists are recomputed from the evidence
//     (ConcludeConformance), never edited;
//   - the provenance stays: the tool applied the same specification text,
//     so Version and Revision still name it (OBI-T-08). A tool applying other
//     text has no business amending this report.
func amendRule(report openbindings.ValidationReport, rule string, status openbindings.RuleEvidenceStatus, findings ...openbindings.Finding) (openbindings.ValidationReport, error) {
	if _, considered := report.Evidence[rule]; !considered {
		return report, fmt.Errorf("%s is not a rule of this report", rule)
	}
	switch status {
	case openbindings.EvidenceSatisfied, openbindings.EvidenceNotApplicable:
		if len(findings) > 0 {
			return report, fmt.Errorf("%s %s carries no findings", rule, status)
		}
	case openbindings.EvidenceViolated:
		if len(findings) == 0 {
			return report, fmt.Errorf("a violation of %s needs its findings", rule)
		}
	default:
		return report, fmt.Errorf("%s %s decides nothing", rule, status)
	}
	for _, finding := range findings {
		if finding.Rule != rule || finding.Status != openbindings.EvidenceViolated {
			return report, fmt.Errorf("a finding of %s %s is not this amendment's", finding.Rule, finding.Status)
		}
	}
	evidence := maps.Clone(report.Evidence)
	evidence[rule] = status
	amended := openbindings.ConcludeConformance(evidence)
	amended.Release, amended.Revision = report.Release, report.Revision
	for _, finding := range report.Findings {
		if finding.Rule != rule {
			amended.Findings = append(amended.Findings, finding)
		}
	}
	amended.Findings = append(amended.Findings, findings...)
	return amended, nil
}

// A tool that decides a rule this SDK leaves inconclusive (here OBI-D-10,
// with a meta-schema check of its own that has no depth limit) amends the
// report.
func Example_producerAmendReport() {
	undetermined := []byte(`{"openbindings":"0.2.0","operations":{"op":{"input":` + deeplyNested + `}}}`)
	_, report, _ := openbindings.ValidateDocument(undetermined)
	fmt.Println(report.Conclusion, report.Inconclusive, len(report.Findings), "findings")

	// ConcludeConformance alone concludes from evidence: it keeps neither
	// the provenance nor the findings, so a report built from it alone names
	// no specification text.
	bare := openbindings.ConcludeConformance(maps.Clone(report.Evidence))
	fmt.Printf("bare: %s, release %q, %d findings\n", bare.Conclusion, bare.Release, len(bare.Findings))
	// Evidence that leaves out a document rule leaves it inconclusive
	// (OBI-T-08): no evidence concludes nothing.
	partial := openbindings.ConcludeConformance(map[string]openbindings.RuleEvidenceStatus{"OBI-D-01": openbindings.EvidenceSatisfied})
	fmt.Println("one rule satisfied:", partial.Conclusion, len(partial.Inconclusive), "inconclusive")

	amended, err := amendRule(report, "OBI-D-10", openbindings.EvidenceSatisfied)
	fmt.Println(err, amended.Conclusion, amended.Release, amended.Revision[:7], len(amended.Findings), "findings")

	// With a violation elsewhere, the other rule's findings stay.
	violating := []byte(`{"openbindings":"0.2.0","operations":{"op":{"input":` + deeplyNested + `}},
	  "bindings":{"b":{"operation":"gone","source":"s"}},"sources":{"s":{"kind":"k"}}}`)
	_, report, _ = openbindings.ValidateDocument(violating)
	amended, err = amendRule(report, "OBI-D-10", openbindings.EvidenceSatisfied)
	fmt.Println(err, amended.Conclusion, amended.Violated, amended.Inconclusive, amended.Findings[0].Rule, amended.Findings[0].Path)

	// The tool's own violation replaces the rule's undecided finding.
	amended, err = amendRule(report, "OBI-D-10", openbindings.EvidenceViolated, openbindings.Finding{
		Rule: "OBI-D-10", Status: openbindings.EvidenceViolated, Path: "/operations/op/input", Message: "found by the tool's own check",
	})
	fmt.Println(err, amended.Violated, len(amended.Violations()), "violations")

	// An amendment that leaves the rule undecided is refused.
	_, err = amendRule(report, "OBI-D-10", openbindings.EvidenceInconclusive)
	fmt.Println(err)
	// Output:
	// conformance-undetermined [OBI-D-10] 1 findings
	// bare: conformance-undetermined, release "", 0 findings
	// one rule satisfied: conformance-undetermined 12 inconclusive
	// <nil> conformant 0.2.0 1d5f08c 0 findings
	// <nil> non-conformant [OBI-D-07] [] OBI-D-07 /bindings/b/operation
	// <nil> [OBI-D-07 OBI-D-10] 2 violations
	// OBI-D-10 inconclusive decides nothing
}

// naiveReferrers is the first answer a CLI writes for `ob schema list`
// ("referenced by"), `schema rename`, `schema remove`, and `merge`: where the
// document references a named schema. It is a keyword walk, already closer
// than the lab's, which searches each operation's and schema's serialized
// text for the quoted string "#/schemas/<name>" (nxSchemaReferrers) and
// rewrites every "$ref" string equal to it anywhere, content and examples
// included (nxRewriteRefs). It still misreads §7 twice: it misses a fragment
// that is percent-decoded before lookup (§7.2), and it counts a reference
// inside a schema that declares $id, which resolves against that resource's
// base, not the document (§7.2). Document.References gets both right.
func naiveReferrers(doc *openbindings.Document, name string) []string {
	target := "#/schemas/" + name
	var found []string
	var walk func(at string, v any)
	walk = func(at string, v any) {
		switch t := v.(type) {
		case map[string]any:
			for _, k := range slices.Sorted(maps.Keys(t)) {
				if ref, ok := t[k].(string); ok && (k == "$ref" || k == "$dynamicRef") && (ref == target || strings.HasPrefix(ref, target+"/")) {
					found = append(found, at)
				}
				walk(at, t[k])
			}
		case []any:
			for _, item := range t {
				walk(at, item)
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(doc.Schemas)) {
		walk("/schemas/"+key, doc.Schemas[key])
	}
	for _, key := range slices.Sorted(maps.Keys(doc.Operations)) {
		walk("/operations/"+key+"/input", doc.Operations[key].Input)
		walk("/operations/"+key+"/output", doc.Operations[key].Output)
	}
	return found
}

func Example_cliSchemaReferrers() {
	data := []byte(`{
	  "openbindings": "0.2.0",
	  "schemas": {
	    "Task": { "type": "object", "properties": { "id": { "type": "string" } } },
	    "Wrapped": { "$id": "https://example.com/wrapped", "$defs": { "x": { "$ref": "#/schemas/Task" } } }
	  },
	  "operations": {
	    "a": { "output": { "$ref": "#/schemas/T%61sk" } },
	    "b": { "output": { "$ref": "#/schemas/Task/properties/id" } }
	  }
	}`)
	doc, report, err := openbindings.ValidateDocument(data)
	if err != nil {
		panic(err)
	}
	fmt.Println(report.Conclusion)
	// The document resource references Task from /operations/a/output and
	// /operations/b/output; Wrapped's "#/schemas/Task" names a location in
	// https://example.com/wrapped instead.
	fmt.Println("naive:", naiveReferrers(doc, "Task"))
	refs, err := doc.References()
	if err != nil {
		panic(err)
	}
	var referrers []string
	for _, r := range refs {
		if under(r.Target, "/schemas/Task") && !under(r.Location, "/schemas/Task") {
			referrers = append(referrers, r.Location)
		}
	}
	fmt.Println("References:", referrers)
	// `ob schema remove` acting on the naive answer: the edit gate catches
	// the missed reference only as a new OBI-D-12 violation.
	delete(doc.Schemas, "Task")
	after, _ := doc.Validate()
	for _, f := range after.Violations() {
		fmt.Println(f.Rule, f.Path)
	}
	// Output:
	// conformant
	// naive: [/schemas/Wrapped /operations/b/output]
	// References: [/operations/a/output/$ref /operations/b/output/$ref]
	// OBI-D-12 /operations/a/output/$ref
	// OBI-D-12 /operations/b/output/$ref
}

// `ob fetch` and `ob show <url>` ask for an OBI by its media type (§11),
// and `ob start` serves its own OBI under it. No request is sent here.
func Example_cliMediaType() {
	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/.well-known/openbindings", nil)
	if err != nil {
		panic(err)
	}
	req.Header.Set("Accept", openbindings.MediaType+", application/json;q=0.5")
	fmt.Println(req.Header.Get("Accept"))
	// Output: application/vnd.openbindings+json, application/json;q=0.5
}

// ------------------------------------------------- equality for merge

// sameJSON reports whether two JSON texts hold the same JSON value: objects
// as unordered members, strings as they decode, arrays in order, and numbers
// by exact decimal value (§10 reads numbers "by their exact decimal value").
// It is the comparison `ob merge` needs to skip identical entries; neither
// reflect.DeepEqual on the model nor comparing encodings gives it.
func sameJSON(a, b []byte) (bool, error) {
	var x, y any
	for _, side := range []struct {
		text []byte
		into *any
	}{{a, &x}, {b, &y}} {
		dec := json.NewDecoder(bytes.NewReader(side.text))
		dec.UseNumber()
		if err := dec.Decode(side.into); err != nil {
			return false, err
		}
	}
	return sameValue(x, y), nil
}

func sameValue(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for name, member := range x {
			other, present := y[name]
			if !present || !sameValue(member, other) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameValue(x[i], y[i]) {
				return false
			}
		}
		return true
	case json.Number:
		y, ok := b.(json.Number)
		return ok && canonicalDecimal(string(x)) == canonicalDecimal(string(y))
	}
	return a == b
}

// canonicalDecimal writes a JSON number's exact value as a sign, its
// significant digits, and a power of ten, so 1, 1.0, 1e0, and 10e-1 all give
// "+1e0". The exponent is a big.Int, so no spelling overflows it.
func canonicalDecimal(number string) string {
	negative := strings.HasPrefix(number, "-")
	mantissa, exponent, _ := strings.Cut(strings.ToLower(strings.TrimPrefix(number, "-")), "e")
	whole, fraction, _ := strings.Cut(mantissa, ".")
	power := new(big.Int)
	if exponent != "" {
		power.SetString(exponent, 10)
	}
	power.Sub(power, big.NewInt(int64(len(fraction))))
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return "0"
	}
	significant := strings.TrimRight(digits, "0")
	power.Add(power, big.NewInt(int64(len(digits)-len(significant))))
	sign := "+"
	if negative {
		sign = "-"
	}
	return sign + significant + "e" + power.String()
}

// `ob merge <obi> <from>` skips an entry both documents hold identically and
// refuses one they hold differently.
func Example_cliMergeIdentical() {
	// One operation and one source, written two ways: other member order,
	// an escaped "A", and 1 against 1e0 and 1.0.
	backslash := string(rune(92))
	ours, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0",
	  "operations":{"ping":{"input":{"maximum":1,"title":"A"}}},
	  "sources":{"api":{"kind":"example.openapi@1","content":{ "n" : 1.0, "location" : "https://api.example.com" }}}}`))
	if err != nil {
		panic(err)
	}
	theirs, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0",
	  "operations":{"ping":{"input":{"title":"` + backslash + `u0041","maximum":1e0}}},
	  "sources":{"api":{"kind":"example.openapi@1","content":{"location":"https://api.example.com","n":1}}}}`))
	if err != nil {
		panic(err)
	}
	compare := func(name string, a, b any) {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		same, err := sameJSON(x, y)
		fmt.Printf("%s: same Go value %v, same encoding %v, same JSON value %v %v\n", name, reflect.DeepEqual(a, b), bytes.Equal(x, y), same, err)
	}
	compare("operation", ours.Operations["ping"], theirs.Operations["ping"])
	compare("source", ours.Sources["api"], theirs.Sources["api"])
	changed := theirs.Sources["api"]
	changed.Content = json.RawMessage(`{"location":"https://api.example.com","n":1.5}`)
	compare("changed source", ours.Sources["api"], changed)
	// Output:
	// operation: same Go value false, same encoding false, same JSON value true <nil>
	// source: same Go value false, same encoding false, same JSON value true <nil>
	// changed source: same Go value false, same encoding false, same JSON value false <nil>
}

// ------------------------------------------------- schemas in the model

// A producer or editor changes a schema it read from a document. The model
// carries schema numbers as json.Number, so they come back as written; what
// the caller adds in Go is written as Go encodes it, and a json.RawMessage
// held in the any-typed member is written as given. A number is lost only
// when a caller decodes schema text into float64 itself. The any-typed
// member has its own hazard: a typed nil held there is a present null.
func Example_producerSchemaEdit() {
	doc, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"setLimit":{"input":
	  {"type":"object","properties":{"limit":{"type":"integer","maximum":9007199254740993,"multipleOf":1.0,"minimum":1e0}}}}}}`))
	if err != nil {
		panic(err)
	}
	operation := doc.Operations["setLimit"]
	input := operation.Input.(map[string]any)
	input["required"] = []any{"limit"}
	input["properties"].(map[string]any)["note"] = map[string]any{"type": "string", "maxLength": 280}
	// A schema the user typed as text is handed over as text.
	operation.Output = json.RawMessage(`{"type": "integer", "exclusiveMaximum": 18014398509481985}`)
	doc.Operations["setLimit"] = operation
	if _, err := doc.Validate(); err != nil {
		panic(err)
	}
	written, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	for _, spelling := range []string{`"maximum":9007199254740993`, `"multipleOf":1.0`, `"minimum":1e0`, `"maxLength":280`, `"exclusiveMaximum":18014398509481985`} {
		fmt.Println(spelling, bytes.Contains(written, []byte(spelling)))
	}
	back, err := openbindings.ParseDocument(written)
	if err != nil {
		panic(err)
	}
	again, _ := json.Marshal(back)
	same, _ := sameJSON(written, again)
	// The held json.RawMessage keeps its member order; read back, the
	// schema is a map, written in key order. The value is the same, and the
	// bytes are stable from the second write on.
	backAgain, err := openbindings.ParseDocument(again)
	if err != nil {
		panic(err)
	}
	settled, _ := json.Marshal(backAgain)
	fmt.Println("read back and written again: same bytes", bytes.Equal(written, again), "same value", same, "stable after", bytes.Equal(again, settled))

	// The trap is the caller's own decoding: json.Unmarshal reads a number
	// into float64, and a decoder with UseNumber keeps it.
	var lossy, exact any
	_ = json.Unmarshal([]byte(`{"maximum":9007199254740993}`), &lossy)
	dec := json.NewDecoder(strings.NewReader(`{"maximum":9007199254740993}`))
	dec.UseNumber()
	_ = dec.Decode(&exact)
	l, _ := json.Marshal(lossy)
	e, _ := json.Marshal(exact)
	fmt.Println(string(l), string(e))

	// A defined type is written by its own encoding: a named byte slice
	// without JSON methods encodes as base64, which is no schema.
	type rawSchema json.RawMessage
	b, _ := json.Marshal(struct{ Input rawSchema }{rawSchema(`{"type":"string"}`)})
	fmt.Println(string(b))

	// The any-typed member's own hazard: absence is a nil interface, so a
	// typed nil held there (a nil json.RawMessage or a nil map) is a present
	// null, which OBI-D-02 and OBI-D-10 then report.
	for _, held := range []openbindings.JSONSchema{nil, json.RawMessage(nil), map[string]any(nil)} {
		built := openbindings.Document{OpenBindings: openbindings.AuthoringVersion, Operations: map[string]openbindings.Operation{"o": {Input: held}}}
		out, _ := json.Marshal(built)
		report, _ := built.Validate()
		fmt.Printf("%T: %s %s\n", held, out, report.Conclusion)
	}
	// Output:
	// "maximum":9007199254740993 true
	// "multipleOf":1.0 true
	// "minimum":1e0 true
	// "maxLength":280 true
	// "exclusiveMaximum":18014398509481985 true
	// read back and written again: same bytes false same value true stable after true
	// {"maximum":9007199254740992} {"maximum":9007199254740993}
	// {"Input":"eyJ0eXBlIjoic3RyaW5nIn0="}
	// <nil>: {"openbindings":"0.2.0","operations":{"o":{}}} conformant
	// json.RawMessage: {"openbindings":"0.2.0","operations":{"o":{"input":null}}} non-conformant
	// map[string]interface {}: {"openbindings":"0.2.0","operations":{"o":{"input":null}}} non-conformant
}

// ------------------------------------------------- the formatting boundary

// `ob fmt` promises "Entries keep the order you gave them" and "Values,
// including numbers, are kept exactly as written". The model cannot keep
// either: maps hold the entries, so encoding writes them in key order, and
// a preference is an int64, so 1e0 comes back as 1. Typed members come back
// in field order whatever order the input used. Source order and token
// spelling belong to the CLI's own representation of the text.
func Example_cliFormatBoundary() {
	doc, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"zeta":{},"alpha":{}},
	  "sources":{"s":{"kind":"k"}},
	  "bindings":{"zeta.s":{"operation":"zeta","source":"s","preference":1e0},"alpha.s":{"source":"s","operation":"alpha"}}}`))
	if err != nil {
		panic(err)
	}
	written, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(written))
	// Output:
	// {"openbindings":"0.2.0","operations":{"alpha":{},"zeta":{}},"sources":{"s":{"kind":"k"}},"bindings":{"alpha.s":{"operation":"alpha","source":"s"},"zeta.s":{"operation":"zeta","source":"s","preference":1}}}
}

// ------------------------------------------------- the reference lookup

// describe prints a reference as `ob schema list` shows it: where it is, its
// value, and its initial target or why it has none. A $dynamicRef is marked,
// since evaluation may land elsewhere than its initial target.
func describe(r openbindings.Reference) string {
	landing := "-> " + r.Target
	if r.Target == "" {
		landing = "unresolved: " + r.Unresolved
	}
	if r.Keyword == "$dynamicRef" {
		landing += " (initial; $dynamicRef)"
	}
	return fmt.Sprintf("%s %q %s", r.Location, r.Value, landing)
}

// dynamicName is the plain name a $dynamicRef names, which it may look up in
// the dynamic scope, or "" when the reference is no $dynamicRef or its
// fragment is not a plain name. References does not decide whether the
// lookup happens; the CLI's conservative policies assume it may.
func dynamicName(r openbindings.Reference) string {
	_, fragment, _ := strings.Cut(r.Value, "#")
	name, err := url.PathUnescape(fragment)
	if r.Keyword != "$dynamicRef" || err != nil || name == "" || strings.HasPrefix(name, "/") {
		return ""
	}
	return name
}

type anchorDeclaration struct{ name, at string }

// dynamicAnchors finds the $dynamicAnchor members within a value, at any
// depth, with their locations below at. It reads every member, schema or
// not, so it can find more than JSON Schema declares, never fewer: enough
// for a policy that refuses whenever a dynamic lookup could be involved.
func dynamicAnchors(value any, at string) []anchorDeclaration {
	var found []anchorDeclaration
	switch v := value.(type) {
	case map[string]any:
		if name, ok := v["$dynamicAnchor"].(string); ok {
			found = append(found, anchorDeclaration{name, at})
		}
		for _, key := range slices.Sorted(maps.Keys(v)) {
			found = append(found, dynamicAnchors(v[key], at+pointerOf(key))...)
		}
	case []any:
		for i, item := range v {
			found = append(found, dynamicAnchors(item, at+pointerOf(strconv.Itoa(i)))...)
		}
	}
	return found
}

func pointerOf(tokens ...string) string {
	var b strings.Builder
	for _, token := range tokens {
		b.WriteString("/" + strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1"))
	}
	return b.String()
}

func genericView(doc *openbindings.Document) (map[string]any, error) {
	data, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var view map[string]any
	return view, dec.Decode(&view)
}

func under(pointer, root string) bool {
	return pointer == root || strings.HasPrefix(pointer, root+"/")
}

func setAt(view any, pointer string, value any) {
	tokens := strings.Split(pointer, "/")[1:]
	for i, token := range tokens {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node := view.(type) {
		case map[string]any:
			if i == len(tokens)-1 {
				node[token] = value
				return
			}
			view = node[token]
		case []any:
			n, _ := strconv.Atoi(token)
			if i == len(tokens)-1 {
				node[n] = value
				return
			}
			view = node[n]
		}
	}
}

// retarget rewrites, in view, the references whose initial target lies
// under from and whose spelling names it by a same-document pointer, in any
// spelling that decodes to one (#/schemas/Task, #%2Fschemas%2FTask), so they
// name the same place under to. Anchors, $id references, references inside
// $id resources, and ordinary data are left alone: they do not spell the
// renamed key. This is the CLI's rewriting policy, outside core.
func retarget(view map[string]any, refs []openbindings.Reference, from, to string) []string {
	var rewritten []string
	for _, r := range refs {
		pointer, err := url.PathUnescape(strings.TrimPrefix(r.Value, "#"))
		if r.Base != "" || !strings.HasPrefix(r.Value, "#") || err != nil || !strings.HasPrefix(pointer, "/") || r.Target == "" || !under(r.Target, from) {
			continue
		}
		spelled := (&url.URL{Fragment: to + strings.TrimPrefix(r.Target, from)}).String()
		setAt(view, r.Location, spelled)
		rewritten = append(rewritten, r.Location+": "+r.Value+" -> "+spelled)
	}
	return rewritten
}

func fromView(view map[string]any) *openbindings.Document {
	data, err := json.Marshal(view)
	if err != nil {
		panic(err)
	}
	var doc openbindings.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		panic(err)
	}
	return &doc
}

// labRewrite is the lab's nxRewriteRefs: every "$ref" string equal to from,
// anywhere in the document. It returns where it would write.
func labRewrite(v any, at []string, from string) []string {
	var hits []string
	switch node := v.(type) {
	case map[string]any:
		for _, name := range slices.Sorted(maps.Keys(node)) {
			if ref, ok := node[name].(string); ok && name == "$ref" && ref == from {
				hits = append(hits, pointerOf(append(slices.Clone(at), name)...))
				continue
			}
			hits = append(hits, labRewrite(node[name], append(slices.Clone(at), name), from)...)
		}
	case []any:
		for i, item := range node {
			hits = append(hits, labRewrite(item, append(slices.Clone(at), strconv.Itoa(i)), from)...)
		}
	}
	return hits
}

// referencesOBI holds an anchor, a percent-encoded pointer and a pointer
// whose leading slash is encoded, a self-reference (a cycle), a $dynamicRef,
// an $id resource referenced by its $id and holding a reference of its own,
// an operation-schema target, an external schema, and $ref-shaped ordinary
// data in content, an example, and an x- member.
const referencesOBI = `{
  "openbindings": "0.2.0",
  "schemas": {
    "Task": { "$anchor": "task", "type": "object", "properties": { "id": { "type": "string" }, "next": { "$ref": "#/schemas/Task" } } },
    "List": { "type": "array", "items": { "$ref": "#/schemas/T%61sk" } },
    "Tree": { "$dynamicAnchor": "node", "type": "object", "properties": { "kids": { "type": "array", "items": { "$dynamicRef": "#node" } } } },
    "Wrapped": { "$id": "https://example.com/wrapped", "$defs": { "x": { "$ref": "#/schemas/Task" } }, "properties": { "t": { "$ref": "https://example.com/wrapped#/$defs/x" } } }
  },
  "operations": {
    "a": { "input": { "$ref": "#task" }, "output": { "$ref": "#/schemas/List" }, "examples": { "e": { "input": { "$ref": "#/schemas/Task" } } } },
    "b": { "input": { "$ref": "#/operations/a/output" }, "output": { "$ref": "https://example.com/wrapped" } },
    "c": { "input": { "$ref": "https://schemas.example.com/address.json" } },
    "d": { "output": { "$ref": "#%2Fschemas%2FTask" } }
  },
  "sources": { "s": { "kind": "example.openapi@1", "content": { "$ref": "#/schemas/Task" } } },
  "x-note": { "$ref": "#/schemas/Task" }
}`

// dynamicOBI is the SDK's own scope-wrapper fixture, in full
// (value_contracts_test.go, TestBundle_ScopeWrappers): List's $dynamicRef
// initially targets List's own string schema, but an evaluation beginning in
// the document resource finds Override's $dynamicAnchor first, so
// inDocument's items must be numbers, while inResource, beginning in its own
// resource, takes strings.
const dynamicOBI = `{"openbindings":"0.2.0","operations":{
  "inDocument":{"input":{"$ref":"https://ex.test/list"}},
  "inResource":{"input":{"$id":"https://ex.test/entry","$ref":"https://ex.test/list"}}},
  "schemas":{
  "List":{"$id":"https://ex.test/list","type":"array","items":{"$dynamicRef":"#item"},"$defs":{"item":{"$dynamicAnchor":"item","type":"string"}}},
  "Override":{"$dynamicAnchor":"item","type":"number"}}}`

func lookUp(document string) (*openbindings.Document, map[string]any, []openbindings.Reference) {
	doc, err := openbindings.ParseDocument([]byte(document))
	if err != nil {
		panic(err)
	}
	refs, err := doc.References()
	if err != nil {
		panic(err)
	}
	view, err := genericView(doc)
	if err != nil {
		panic(err)
	}
	return doc, view, refs
}

// `ob schema list`'s "referenced by": every reference keyword in the schemas
// the document contains, and nothing from content, examples, or x- members.
func Example_cliReferenceLookup() {
	doc, _, refs := lookUp(referencesOBI)
	report, _ := doc.Validate()
	fmt.Println(report.Conclusion)
	for _, r := range refs {
		fmt.Println(describe(r))
	}
	// Output:
	// conformant
	// /operations/a/input/$ref "#task" -> /schemas/Task
	// /operations/a/output/$ref "#/schemas/List" -> /schemas/List
	// /operations/b/input/$ref "#/operations/a/output" -> /operations/a/output
	// /operations/b/output/$ref "https://example.com/wrapped" -> /schemas/Wrapped
	// /operations/c/input/$ref "https://schemas.example.com/address.json" unresolved: "https://schemas.example.com/address.json" names https://schemas.example.com/address.json, a resource the document does not embed (§7.4)
	// /operations/d/output/$ref "#%2Fschemas%2FTask" -> /schemas/Task
	// /schemas/List/items/$ref "#/schemas/T%61sk" -> /schemas/Task
	// /schemas/Task/properties/next/$ref "#/schemas/Task" -> /schemas/Task
	// /schemas/Tree/properties/kids/items/$dynamicRef "#node" -> /schemas/Tree (initial; $dynamicRef)
	// /schemas/Wrapped/$defs/x/$ref "#/schemas/Task" unresolved: "#/schemas/Task" reaches no schema in https://example.com/wrapped
	// /schemas/Wrapped/properties/t/$ref "https://example.com/wrapped#/$defs/x" -> /schemas/Wrapped/$defs/x
}

// The lookup against the rest of the SDK: every reference of the spec's §7.5
// table, and the boundary and duplicate-name cases, each as an operation's
// input reference. The validator's answer is OBI-D-12 and OBI-D-05 for a
// reference in the document resource, and the value contract otherwise: an
// undefined result or an unresolvable reference means the lookup identifies
// nothing.
func Example_cliReferenceParity() {
	section75 := `"Task":{"$anchor":"task","type":"object","properties":{"my type":{"type":"string"}}},
	  "Tree":{"$id":"https://example.com/schemas/tree.json","$anchor":"tree","type":"object","properties":{"children":{"type":"array","items":{"$ref":"#"}}}}`
	cases := []struct{ ref, schemas string }{
		{"#", section75},
		{"#/schemas/Task", section75},
		{"#/schemas/Task/properties/my%20type", section75},
		{"#/schemas/Task/properties/my type", section75},
		{"#/schemas/Task/properties/my%2520type", section75},
		{"#%2Fschemas%2FTask", section75},
		{"#/schemas/Task/type", section75},
		{"#/operations", section75},
		{"#/schemas/Missing", section75},
		{"#/schemas/~2", section75},
		{"#/schemas/Tree", section75},
		{"#/schemas/Tree/properties/children", section75},
		{"#task", section75},
		{"#t%61sk", section75},
		{"#tree", section75},
		{"#%FF", section75},
		{"tree.json#/properties/children", section75},
		{"https://example.com/schemas/tree.json#/properties/children", section75},
		// Both A and x declare $id: the pointer crosses A's boundary.
		{"#/schemas/A/properties/x", `"A":{"$id":"https://ex.test/a","properties":{"x":{"$id":"https://ex.test/b","type":"string"}}}`},
		// Within a resource, a pointer follows the resource's document.
		{"https://ex.test/a#/properties/x/properties/y", `"A":{"$id":"https://ex.test/a","properties":{"x":{"$id":"https://ex.test/b","properties":{"y":{"type":"string"}}}}}`},
		// A plain name declared twice inside an $id resource, in a
		// conformant document, and in the document resource.
		{"https://ex.test/a#n", `"A":{"$id":"https://ex.test/a","$defs":{"p":{"$anchor":"n","type":"string"},"q":{"$anchor":"n","type":"integer"}}}`},
		{"#n", `"P":{"$anchor":"n","type":"string"},"Q":{"$anchor":"n","type":"integer"}`},
		// $anchor and $dynamicAnchor on one schema are two declarations.
		{"#n", `"P":{"$anchor":"n","$dynamicAnchor":"n","type":"string"}`},
	}
	compiler, err := openbindings.NewValueContractCompiler(typeOnly{})
	if err != nil {
		panic(err)
	}
	agreed := 0
	for _, c := range cases {
		reference, _ := json.Marshal(c.ref)
		document := `{"openbindings":"0.2.0","schemas":{` + c.schemas + `},"operations":{"op":{"input":{"$ref":` + string(reference) + `}}}}`
		doc, report, _ := openbindings.ValidateDocument([]byte(document))
		at := "/operations/op/input/$ref"
		validator := "resolves"
		for _, finding := range report.Violations() {
			if (finding.Rule == "OBI-D-12" || finding.Rule == "OBI-D-05") && finding.Path == at {
				validator = "fails " + finding.Rule
			}
		}
		if validator == "resolves" && doc != nil {
			contracts, err := compiler.Resolve(context.Background(), doc)
			if err != nil {
				panic(err)
			}
			contract, _ := contracts.CompileInput(context.Background(), "op")
			var noVerdict *openbindings.NoVerdictError
			if errors.As(contract.Err(), &noVerdict) && noVerdict.Location != "" {
				validator = "no target (" + map[bool]string{true: "undefined", false: "core refuses"}[errors.Is(noVerdict, openbindings.ErrUndefined)] + ")"
			}
		}
		// A document the model cannot carry is read with json.Unmarshal,
		// which decodes what ParseDocument would refuse for the schema.
		var held openbindings.Document
		if err := json.Unmarshal([]byte(document), &held); err != nil {
			panic(err)
		}
		// A string that is not a URI-reference is no reference of any form
		// (§7.1), so the lookup does not list it.
		lookup := "not a reference"
		refs, err := held.References()
		if err != nil {
			panic(err)
		}
		for _, r := range refs {
			switch {
			case r.Location == at && r.Target == "":
				lookup = "no target"
			case r.Location == at:
				lookup = "resolves"
			}
		}
		if (lookup == "resolves") == (validator == "resolves") {
			agreed++
		}
		fmt.Printf("%-60s %-24s %s\n", c.ref, validator, lookup)
	}
	fmt.Println("agree:", agreed, "of", len(cases))
	// Output:
	// #                                                            fails OBI-D-12           no target
	// #/schemas/Task                                               resolves                 resolves
	// #/schemas/Task/properties/my%20type                          resolves                 resolves
	// #/schemas/Task/properties/my type                            fails OBI-D-05           not a reference
	// #/schemas/Task/properties/my%2520type                        fails OBI-D-12           no target
	// #%2Fschemas%2FTask                                           resolves                 resolves
	// #/schemas/Task/type                                          fails OBI-D-12           no target
	// #/operations                                                 fails OBI-D-12           no target
	// #/schemas/Missing                                            fails OBI-D-12           no target
	// #/schemas/~2                                                 fails OBI-D-12           no target
	// #/schemas/Tree                                               resolves                 resolves
	// #/schemas/Tree/properties/children                           fails OBI-D-12           no target
	// #task                                                        resolves                 resolves
	// #t%61sk                                                      resolves                 resolves
	// #tree                                                        fails OBI-D-12           no target
	// #%FF                                                         fails OBI-D-12           no target
	// tree.json#/properties/children                               fails OBI-D-05           no target
	// https://example.com/schemas/tree.json#/properties/children   resolves                 resolves
	// #/schemas/A/properties/x                                     fails OBI-D-12           no target
	// https://ex.test/a#/properties/x/properties/y                 resolves                 resolves
	// https://ex.test/a#n                                          no target (undefined)    no target
	// #n                                                           no target (undefined)    no target
	// #n                                                           no target (undefined)    no target
	// agree: 23 of 23
}

// `ob schema rename <obi> Task Todo` and `ob operation rename <obi> a alpha`
// on the lookup, against the lab's rewrite.
func Example_cliSchemaRename() {
	_, view, refs := lookUp(referencesOBI)
	for _, line := range retarget(view, refs, "/schemas/Task", "/schemas/Todo") {
		fmt.Println(line)
	}
	schemas := view["schemas"].(map[string]any)
	schemas["Todo"] = schemas["Task"]
	delete(schemas, "Task")
	renamed := fromView(view)
	report, _ := renamed.Validate()
	fmt.Println("after schema rename:", report.Conclusion)
	fmt.Println("ordinary data untouched:", string(renamed.Sources["s"].Content), string(renamed.Operations["a"].Examples["e"].Input), string(renamed.Extensions["x-note"]))

	// Renaming an operation moves the schemas at its input and output too.
	_, view, refs = lookUp(referencesOBI)
	for _, line := range retarget(view, refs, "/operations/a", "/operations/alpha") {
		fmt.Println(line)
	}
	operations := view["operations"].(map[string]any)
	operations["alpha"] = operations["a"]
	delete(operations, "a")
	report, _ = fromView(view).Validate()
	fmt.Println("after operation rename:", report.Conclusion)

	// The lab's rewrite of "#/schemas/Task": it writes into content, an
	// example, an x- member, and a reference inside the $id resource, and
	// misses both percent-encoded spellings.
	_, view, _ = lookUp(referencesOBI)
	fmt.Println("lab rewrite writes:", labRewrite(view, nil, "#/schemas/Task"))
	// Output:
	// /operations/d/output/$ref: #%2Fschemas%2FTask -> #/schemas/Todo
	// /schemas/List/items/$ref: #/schemas/T%61sk -> #/schemas/Todo
	// /schemas/Task/properties/next/$ref: #/schemas/Task -> #/schemas/Todo
	// after schema rename: conformant
	// ordinary data untouched: {"$ref":"#/schemas/Task"} {"$ref":"#/schemas/Task"} {"$ref":"#/schemas/Task"}
	// /operations/b/input/$ref: #/operations/a/output -> #/operations/alpha/output
	// after operation rename: conformant
	// lab rewrite writes: [/operations/a/examples/e/input/$ref /schemas/Task/properties/next/$ref /schemas/Wrapped/$defs/x/$ref /sources/s/content/$ref /x-note/$ref]
}

// `ob schema remove <obi> <name>` refuses while a reference outside the
// schema lands in it, whatever the spelling: a pointer, an anchor, or an $id.
// Initial targets are not the whole answer: a $dynamicRef can resolve to any
// schema in the dynamic scope that declares its $dynamicAnchor, so the CLI's
// conservative policy also refuses to remove a schema declaring a
// $dynamicAnchor that a $dynamicRef outside it names.
func Example_cliSchemaRemove() {
	removable := func(doc *openbindings.Document, refs []openbindings.Reference, name string) string {
		root := pointerOf("schemas", name)
		var by []string
		for _, r := range refs {
			if !under(r.Location, root) && r.Target != "" && under(r.Target, root) {
				by = append(by, r.Location)
			}
		}
		if len(by) > 0 {
			return fmt.Sprintf("refused: referenced from %v", by)
		}
		for _, declaration := range dynamicAnchors(doc.Schemas[name], root) {
			for _, r := range refs {
				if !under(r.Location, root) && dynamicName(r) == declaration.name {
					return fmt.Sprintf("refused: %s declares $dynamicAnchor %s, which %s may resolve to dynamically", declaration.at, declaration.name, r.Location)
				}
			}
		}
		return "removable"
	}
	doc, _, refs := lookUp(referencesOBI)
	for _, name := range []string{"Task", "Wrapped", "Tree", "List"} {
		fmt.Println(name, removable(doc, refs, name))
	}
	// The SDK's scope-wrapper fixture: no reference targets Override, yet
	// removing it changes what inDocument accepts, in a document that stays
	// conformant.
	doc, _, refs = lookUp(dynamicOBI)
	fmt.Println("Override", removable(doc, refs, "Override"))
	// Output:
	// Task refused: referenced from [/operations/a/input/$ref /operations/d/output/$ref /schemas/List/items/$ref]
	// Wrapped refused: referenced from [/operations/b/output/$ref]
	// Tree removable
	// List refused: referenced from [/operations/a/output/$ref]
	// Override refused: /schemas/Override declares $dynamicAnchor item, which /schemas/List/items/$dynamicRef may resolve to dynamically
}

// `ob merge <obi> <from> --operation <key>` brings the named schemas an
// operation's schemas reach, transitively; a cycle ends where it began. A
// target in another operation's schema, and an unresolved reference, are the
// merge policy's to decide, so the closure only reports them. A $dynamicRef
// in the closure makes it incomplete: the schemas it may resolve to depend on
// the dynamic scope, which the lookup does not resolve, so the CLI's
// conservative policy refuses the merge and names every candidate it finds.
func Example_cliMergeClosure() {
	closure := func(view map[string]any, refs []openbindings.Reference, key string) string {
		visited := map[string]bool{}
		var schemas, operations, unresolved, dynamic []string
		queue := []string{pointerOf("operations", key, "input"), pointerOf("operations", key, "output")}
		for len(queue) > 0 {
			here := queue[0]
			queue = queue[1:]
			if visited[here] {
				continue
			}
			visited[here] = true
			for _, r := range refs {
				if !under(r.Location, here) {
					continue
				}
				if name := dynamicName(r); name != "" {
					var candidates []string
					for _, declaration := range dynamicAnchors(view["schemas"], "/schemas") {
						if declaration.name == name {
							candidates = append(candidates, declaration.at)
						}
					}
					dynamic = append(dynamic, fmt.Sprintf("%s may resolve to %v", r.Location, candidates))
				}
				tokens := strings.Split(r.Target, "/")
				switch {
				case r.Target == "":
					unresolved = append(unresolved, r.Location)
				case tokens[1] == "schemas":
					root := pointerOf("schemas", tokens[2])
					if !visited[root] && !slices.Contains(schemas, tokens[2]) {
						schemas = append(schemas, tokens[2])
						queue = append(queue, root)
					}
				case tokens[1] == "operations" && tokens[2] != key:
					operations = append(operations, r.Target)
				}
			}
		}
		slices.Sort(schemas)
		if len(dynamic) > 0 {
			return fmt.Sprintf("refused, incomplete: %v", dynamic)
		}
		return fmt.Sprintf("schemas %v, other operations %v, unresolved %v", schemas, operations, unresolved)
	}
	_, view, refs := lookUp(referencesOBI)
	for _, key := range []string{"a", "b", "c", "d"} {
		fmt.Printf("%s: %s\n", key, closure(view, refs, key))
	}
	_, view, refs = lookUp(dynamicOBI)
	fmt.Printf("inDocument: %s\n", closure(view, refs, "inDocument"))
	fmt.Printf("inResource: %s\n", closure(view, refs, "inResource"))
	// Output:
	// a: schemas [List Task], other operations [], unresolved []
	// b: schemas [Wrapped], other operations [/operations/a/output], unresolved [/schemas/Wrapped/$defs/x/$ref]
	// c: schemas [], other operations [], unresolved [/operations/c/input/$ref]
	// d: schemas [Task], other operations [], unresolved []
	// inDocument: refused, incomplete: [/schemas/List/items/$dynamicRef may resolve to [/schemas/List/$defs/item /schemas/Override]]
	// inResource: refused, incomplete: [/schemas/List/items/$dynamicRef may resolve to [/schemas/List/$defs/item /schemas/Override]]
}

// The lookup's whole-call failures, which a reference's Unresolved cannot
// carry: a version this SDK does not interpret, a document declaring no valid
// version, a document that cannot be encoded, and an index cut short at the
// nesting limit, which returns what it found and an error. Only a nil error
// makes an empty result mean "no references", as for a nil document.
func Example_cliReferenceFailures() {
	report := func(name string, doc *openbindings.Document) {
		refs, err := doc.References()
		var refusal *openbindings.VersionRefusalError
		var violation *openbindings.ValidationError
		switch {
		case errors.As(err, &refusal):
			fmt.Println(name+": refused, version", refusal.Version)
		case errors.As(err, &violation):
			// An established violation: the document is non-conformant, and
			// a document declaring no valid version is not interpreted.
			fmt.Println(name+": non-conformant,", violation.Findings[0].Rule)
		case errors.Is(err, openbindings.ErrInconclusive):
			// The references found, if any, are some, not all: nothing may
			// be concluded from what is missing.
			fmt.Printf("%s: %d references found, inconclusive: %v\n", name, len(refs), err)
		case err != nil:
			fmt.Println(name+":", err)
		default:
			fmt.Println(name+":", len(refs), "references, complete")
		}
	}
	report("next version", &openbindings.Document{OpenBindings: "0.3.0", Operations: map[string]openbindings.Operation{}})
	report("no version", &openbindings.Document{OpenBindings: "0.2", Operations: map[string]openbindings.Operation{}})
	report("unencodable", &openbindings.Document{OpenBindings: "0.2.0", Operations: map[string]openbindings.Operation{"op": {Input: map[string]any{"maximum": math.NaN()}}}})
	deep := strings.Repeat(`{"not":`, 300) + `{"$ref":"#/schemas/S"}` + strings.Repeat(`}`, 300)
	var deepDoc openbindings.Document
	if err := json.Unmarshal([]byte(`{"openbindings":"0.2.0","schemas":{"S":{"type":"string"}},"operations":{"op":{"input":`+deep+`,"output":{"$ref":"#/schemas/S"}}}}`), &deepDoc); err != nil {
		panic(err)
	}
	report("deep", &deepDoc)
	report("no references", &openbindings.Document{OpenBindings: "0.2.0", Operations: map[string]openbindings.Operation{"op": {Input: true}}})
	report("nil document", nil)
	// Output:
	// next version: refused, version 0.3.0
	// no version: non-conformant, OBI-D-09
	// unencodable: openbindings: encode document: json: error calling MarshalJSON for type openbindings.Document: json: error calling MarshalJSON for type openbindings.Operation: json: unsupported value: NaN
	// deep: 1 references found, inconclusive: openbindings: inconclusive: the schemas at /operations/op/input nest subschemas deeper than 256 levels, which this SDK does not index, so the references there are not all listed
	// no references: 0 references, complete
	// nil document: 0 references, complete
}

// ------------------------------------------------- a minimal evaluator

// typeOnly is the smallest evaluator that keeps the evaluator contract: it
// decides a schema whose only assertion is "type" with one type name,
// constructs a *MismatchError for a failure, gives no verdict for anything
// else with an ordinary error, and returns the ctx's own error when the ctx
// is done. It would pass the kit only with an exemption for nearly every
// case, since the kit covers all of 2020-12.
type typeOnly struct{}

func (typeOnly) Compile(ctx context.Context, bundle openbindings.SchemaBundle) (openbindings.CompiledSchema, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(bundle.Document, &root); err != nil {
		return nil, fmt.Errorf("typeOnly: the bundle is not JSON: %v", err)
	}
	defs, _ := root["$defs"].(map[string]any)
	structural := map[string]bool{"$schema": true, "$id": true, "$defs": true, "$ref": true}
	entry := root
	for hops := 0; ; hops++ {
		ref, isRef := entry["$ref"].(string)
		if !isRef {
			break
		}
		for _, keyword := range slices.Sorted(maps.Keys(entry)) {
			if !structural[keyword] {
				return nil, fmt.Errorf("typeOnly: cannot decide %s beside $ref", keyword)
			}
		}
		// Core writes a reference to a resource's root as its $id (SchemaBundle).
		var next map[string]any
		for _, def := range defs {
			if schema, ok := def.(map[string]any); ok && schema["$id"] == ref {
				next = schema
			}
		}
		if next == nil || hops > len(defs) {
			return nil, fmt.Errorf("typeOnly: cannot follow %s", ref)
		}
		entry = next
	}
	for _, keyword := range slices.Sorted(maps.Keys(entry)) {
		if keyword != "$id" && keyword != "type" {
			return nil, fmt.Errorf("typeOnly: cannot decide %s", keyword)
		}
	}
	want, ok := entry["type"].(string)
	if !ok {
		return nil, errors.New("typeOnly: decides only one type name")
	}
	return typeCheck{want}, nil
}

type typeCheck struct{ want string }

func (c typeCheck) Validate(ctx context.Context, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	got := jsonTypeOf(value)
	if got == c.want || c.want == "number" && got == "integer" {
		return nil
	}
	return &openbindings.MismatchError{Problems: []openbindings.SchemaProblem{{
		InstanceLocation: "",
		Message:          fmt.Sprintf("type: got %s, want %s", got, c.want),
	}}}
}

// jsonTypeOf names a JSON value's type as JSON Schema does: a number with no
// fractional part is an integer, whatever its spelling (1.0 included).
func jsonTypeOf(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case json.Number:
		canonical := canonicalDecimal(string(v))
		_, power, _ := strings.Cut(canonical, "e")
		if canonical == "0" || !strings.HasPrefix(power, "-") {
			return "integer"
		}
		return "number"
	}
	return fmt.Sprintf("%T", value)
}

func Example_evaluatorAuthorMinimal() {
	ctx := context.Background()
	compiler, err := openbindings.NewValueContractCompiler(typeOnly{})
	if err != nil {
		panic(err)
	}
	doc, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","schemas":{"Name":{"type":"string"}},"operations":{
	  "name":{"input":{"$ref":"#/schemas/Name"}},
	  "count":{"input":{"type":"integer"}},
	  "short":{"input":{"type":"string","maxLength":3}}}}`))
	if err != nil {
		panic(err)
	}
	contracts, err := compiler.Resolve(ctx, doc)
	if err != nil {
		panic(err)
	}
	check := func(operation, value string) {
		contract, err := contracts.CompileInput(ctx, operation)
		if err != nil {
			panic(err)
		}
		var mismatch *openbindings.MismatchError
		var noVerdict *openbindings.NoVerdictError
		switch err := contract.ValidateJSON(ctx, []byte(value)); {
		case err == nil:
			fmt.Println(operation, value, "valid")
		case errors.As(err, &mismatch):
			fmt.Println(operation, value, "mismatch at", strconv.Quote(mismatch.Problems[0].InstanceLocation)+":", mismatch.Problems[0].Message)
		case errors.As(err, &noVerdict):
			fmt.Println(operation, value, "no verdict:", noVerdict.Cause)
		}
	}
	check("name", `"Ada"`)
	check("name", `5`)
	check("count", `2.0`)
	check("count", `2.5`)
	check("short", `"abc"`)

	// Cancellation: through core, a done ctx is a no-verdict holding the
	// ctx's error; on the evaluator directly, the ctx's error itself.
	done, cancel := context.WithCancel(ctx)
	cancel()
	contract, _ := contracts.CompileInput(ctx, "name")
	err = contract.ValidateJSON(done, []byte(`"Ada"`))
	fmt.Println("through core:", errors.Is(err, openbindings.ErrNoVerdict), errors.Is(err, context.Canceled))
	compiled, err := typeOnly{}.Compile(ctx, openbindings.SchemaBundle{Document: json.RawMessage(
		`{"$id":"https://e.invalid/","$ref":"https://e.invalid/n","$defs":{"n":{"$id":"https://e.invalid/n","type":"string"}}}`)})
	if err != nil {
		panic(err)
	}
	err = compiled.Validate(done, "Ada")
	fmt.Println("directly:", err == context.Canceled)
	// Output:
	// name "Ada" valid
	// name 5 mismatch at "": type: got integer, want string
	// count 2.0 valid
	// count 2.5 mismatch at "": type: got number, want integer
	// short "abc" no verdict: typeOnly: cannot decide maxLength
	// through core: true true
	// directly: true
}
