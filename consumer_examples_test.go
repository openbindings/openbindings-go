package openbindings_test

// Consumer exercises for the core API (Loop C, stage C1, at 166b9b4).
//
// Each example is a caller's code at the current API, written to show
// where the API serves the caller and where it does not. Comments marked
// "C1 item" name the report item (K1 to K8, F1 to F17) an awkward step
// supports. Three callers:
//
//   - the 0.2 CLI (ob-cli-surface-lab at f7a9d16, NewNextSurfaceRoot):
//     reading a document, validating and reporting it, refusing an
//     unsupported version, editing in place, resolving an operation and
//     choosing its binding, checking a dependency's kinds, the media type,
//     the formatting boundary, and, on a reference lookup the caller writes,
//     schema and operation rename, schema remove, and merge closure, with
//     an exact-value comparison for merge.
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
	"unicode/utf8"

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
// applied (OBI-T-09), each finding located in the file, and the exit status
// the lab's table gives (0 conformant, 1 non-conformant, 3 refused, 4 no
// verdict).
func cliValidate(name string, data []byte) (exit int) {
	_, report, err := openbindings.ValidateDocument(data)
	var refusal *openbindings.VersionRefusalError
	if errors.As(err, &refusal) {
		// OBI-T-04: reported instead of a conclusion.
		fmt.Printf("%s: refused, not judged: %s\n", name, refusal.Reason)
		return 3
	}
	// err is nil or a *ValidationError; the report already holds the same
	// violations, so the command reads the report alone.
	text := "OpenBindings " + report.Version
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
	//   checked against OpenBindings 0.2.0 (working draft, spec revision 9812702): 13 of 13 rules decided
	// exit 0
	// broken.obi.json: non-conformant
	//   checked against OpenBindings 0.2.0 (working draft, spec revision 9812702): 13 of 13 rules decided
	//   broken.obi.json:7:23: OBI-D-02: does not validate against the document schema: minLength: got 0, want 1
	//   broken.obi.json:5:12: OBI-D-07: references unknown operation key "gone"
	// exit 1
	// surrogate.obi.json: conformance-undetermined
	//   checked against OpenBindings 0.2.0 (working draft, spec revision 9812702): 2 of 13 rules decided
	//   11 checks undecided, the first OBI-D-02 at "": a string at "/x-note" holds an escape of a lone UTF-16 surrogate, which this SDK does not carry, so this rule was not checked
	// exit 4
	// next.obi.json: refused, not judged: document declares version "0.3.0", newer than the release line this implementation supports (0.2.x)
	// exit 3
}

// cliRead is how a command that interprets a document (invoke, mcp, codegen)
// reads one: ParseDocument, then an exit status for each way it can fail.
func cliRead(data []byte) (*openbindings.Document, int, string) {
	iface, err := openbindings.ParseDocument(data)
	var refusal *openbindings.VersionRefusalError
	var violation *openbindings.ValidationError
	switch {
	case err == nil:
		return iface, 0, "read"
	case errors.As(err, &refusal):
		return nil, 3, "refused (OBI-T-04): declares " + refusal.Version
	case errors.As(err, &violation):
		first := violation.Findings[0]
		return nil, 1, fmt.Sprintf("non-conformant at %s: %s", first.Position, first.Rule)
	}
	// C1 item K1 (undecided parse errors): the other errors (nesting past the
	// decoder, a lone surrogate, the document schema reaching no verdict)
	// carry no type or sentinel, so a caller can tell them apart, or from
	// an internal failure, only by the message. Their prefix, "parse
	// document:", is also not the "openbindings:" prefix of the sentinels
	// (see C1 item K6 (error prefixes)).
	return nil, 2, fmt.Sprintf("unclassified (%T): %v", err, err)
}

func Example_cliRead() {
	inputs := [][]byte{
		[]byte(tasksOBI),
		[]byte(`{"openbindings":"0.2.0-rc.1","operations":{}}`),
		[]byte(`{"openbindings":"0.2.0","operations":{},"unknown":1}`),
		[]byte(`{"openbindings":"0.2.0","operations":{},"x-note":"\udc00"}`),
		[]byte(`{"openbindings":"0.2.0","operations":{},"x-deep":` + strings.Repeat("[", 10001) + strings.Repeat("]", 10001) + `}`),
	}
	// C1 item F5 (member location): the unknown member's finding is located
	// at the object that holds it (the document, 1:1), not at the member.
	for _, data := range inputs {
		_, exit, what := cliRead(data)
		fmt.Println(exit, what)
	}
	// Output:
	// 0 read
	// 3 refused (OBI-T-04): declares 0.2.0-rc.1
	// 1 non-conformant at 1:1: OBI-D-02
	// 2 unclassified (*fmt.wrapError): parse document: a string at "/x-note" holds an escape of a lone UTF-16 surrogate, which this SDK does not carry
	// 2 unclassified (*fmt.wrapError): parse document: the input is nested deeper than the decoder reads (10000 levels), so it is not decoded
}

// An editor holds a document decoded with json.Unmarshal, which refuses
// nothing, so it makes OBI-T-04's line decision itself before interpreting
// the document.
func Example_cliVersionDecision() {
	fmt.Println("this ob interprets OpenBindings", openbindings.SupportedVersions)
	for _, declared := range []string{`"0.2.0"`, `"0.2.7"`, `"0.2.0+build.5"`, `"0.3.0"`, `"0.2.0-rc.1"`, `"0.2"`, `""`} {
		var held openbindings.Document
		if err := json.Unmarshal([]byte(`{"openbindings":`+declared+`,"operations":{}}`), &held); err != nil {
			panic(err)
		}
		declared := held.OpenBindings
		supported, err := openbindings.IsSupportedVersion(declared)
		if (err == nil) != openbindings.IsValidSemver(declared) {
			panic("IsSupportedVersion's error is IsValidSemver's answer")
		}
		// OBI-T-04: refuse a declared version outside the supported set,
		// and never refuse a text that declares no version.
		refuse := err == nil && !supported
		// C1 item K2 (version decision): the signature invites `if ok, _ :=
		// IsSupportedVersion(v); !ok { refuse }`, which refuses "0.2" and "",
		// the refusal OBI-T-04 forbids.
		naive := !supported
		fmt.Printf("%-15q refuse=%-5v naive=%v\n", declared, refuse, naive)
	}
	// Output:
	// this ob interprets OpenBindings 0.2.x
	// "0.2.0"         refuse=false naive=false
	// "0.2.7"         refuse=false naive=false
	// "0.2.0+build.5" refuse=false naive=false
	// "0.3.0"         refuse=true  naive=true
	// "0.2.0-rc.1"    refuse=true  naive=true
	// "0.2"           refuse=false naive=true
	// ""              refuse=false naive=true
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
	iface, before, err := openbindings.ValidateDocument(data)
	var refusal *openbindings.VersionRefusalError
	switch {
	case errors.As(err, &refusal):
		return nil, refusal
	case iface == nil:
		// C1 item F6 (nil document): the model cannot carry the document (or
		// OBI-D-01 refuses it), and the only sign of it is a nil
		// *Document beside a nil error for a lone surrogate.
		return nil, fmt.Errorf("cannot edit: %s", before.Conclusion)
	}
	had := map[string]bool{}
	for _, f := range before.Violations() {
		had[f.Rule+" "+f.Path] = true
	}
	edit(iface)
	// The edited value is judged again. Validate's error is a
	// *ValidationError (violations, compared below), a *VersionRefusalError
	// when the edit declares a version this SDK does not interpret, or an
	// encoding error when the edited value cannot be written; the last two
	// come with no report, so they end the edit here.
	after, err := iface.Validate()
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
	written, err := json.MarshalIndent(iface, "", "  ")
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

	// C1 item F1 (member order): the same edit to a document holding one x-
	// member writes every top-level member in sorted order instead of field
	// order, so `name` now precedes `openbindings`.
	withExtension := strings.Replace(tasksOBI, `"name"`, `"x-owner": "tasks-team", "name"`, 1)
	written, err = cliEdit([]byte(withExtension), addOperation("archiveTask", true))
	fmt.Println(err, memberOrder(written))

	// C1 item F2 (HTML escaping): a description holding "<" is rewritten as
	// a \u003c escape by any edit, whatever the encoder's SetEscapeHTML.
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
	// refused: the edit declares a version this ob does not interpret: openbindings: document declares version "0.3.0", newer than the release line this implementation supports (0.2.x) (OBI-T-04)
	// refused: the edited document cannot be written: openbindings: encode document: json: error calling MarshalJSON for type openbindings.Document: member "x-owner": unexpected end of JSON input
	// <nil> [bindings dependencies name openbindings operations schemas sources version x-owner]
	// false true
	// false true
}

// cliInvokeChoice is how `ob invoke <obi> <operation> [--binding B]...`
// chooses a binding: resolve the name (OBI-T-07), find the operation's
// bindings by its key, read each binding's source kind, and take the first
// named binding ob can invoke, or the sole one; otherwise refuse, listing
// the candidates with preference and deprecation, which are shown and never
// used to choose.
func cliInvokeChoice(iface *openbindings.Document, name string, canInvoke map[string]bool, named ...string) string {
	key, _, found := iface.ResolveOperation(name)
	if !found {
		return fmt.Sprintf("refused: no operation named %q", name)
	}
	bindings := iface.OperationBindings(key)
	kindOf := func(binding string) string {
		return iface.Sources[iface.Bindings[binding].Source].Kind
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
		entry := iface.Bindings[b]
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
	iface, err := openbindings.ParseDocument([]byte(tasksOBI))
	if err != nil {
		panic(err)
	}
	httpOnly := map[string]bool{"example.openapi@1": true}
	both := map[string]bool{"example.openapi@1": true, "example.mcp@1": true}
	fmt.Println(cliInvokeChoice(iface, "acme.tasks.createTask", httpOnly))
	fmt.Println(cliInvokeChoice(iface, "createTask", both))
	fmt.Println(cliInvokeChoice(iface, "createTask", both, "createTask.mcp", "createTask.http"))
	fmt.Println(cliInvokeChoice(iface, "createTask", both, "listTasks.http"))
	fmt.Println(cliInvokeChoice(iface, "CreateTask", both))
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
	// C1 item K8 (schemas in the model): the same document is not the same
	// Go value. A schema decodes to generic JSON values, every number a
	// json.Number and every array an []any. That is representation, not
	// loss: Example_producerSchemaEdit shows the exact round trip.
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

// amendRule is the whole amendment workflow on the current API.
// C1 item K5 (amendment).
// A tool that decides a rule this SDK left inconclusive replaces that rule's
// evidence and findings with its own decision for the entire rule, and the
// report stays coherent:
//   - the decision must settle the rule: satisfied, violated (with the
//     violations' findings), or not applicable; another inconclusive answer
//     is no amendment;
//   - the rule's old findings go, and the tool's take their place;
//   - the conclusion and the derived lists are recomputed from the evidence
//     (ConcludeConformance), never edited;
//   - the provenance stays: the tool applied the same specification text,
//     so Version and Revision still name it (OBI-T-09). A tool applying other
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
	amended.Version, amended.Revision = report.Version, report.Revision
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
	fmt.Printf("bare: %s, version %q, %d findings\n", bare.Conclusion, bare.Version, len(bare.Findings))

	amended, err := amendRule(report, "OBI-D-10", openbindings.EvidenceSatisfied)
	fmt.Println(err, amended.Conclusion, amended.Version, amended.Revision[:7], len(amended.Findings), "findings")

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
	// bare: conformance-undetermined, version "", 0 findings
	// <nil> conformant 0.2.0 9812702 0 findings
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
// base, not the document (§7.2). C1 item F4 (references): the references
// section below writes the lookup that gets these right.
func naiveReferrers(iface *openbindings.Document, name string) []string {
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
	for _, key := range slices.Sorted(maps.Keys(iface.Schemas)) {
		walk("/schemas/"+key, iface.Schemas[key])
	}
	for _, key := range slices.Sorted(maps.Keys(iface.Operations)) {
		walk("/operations/"+key+"/input", iface.Operations[key].Input)
		walk("/operations/"+key+"/output", iface.Operations[key].Output)
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
	iface, report, err := openbindings.ValidateDocument(data)
	if err != nil {
		panic(err)
	}
	fmt.Println(report.Conclusion)
	// The document resource references Task from /operations/a/output and
	// /operations/b/output; Wrapped's "#/schemas/Task" names a location in
	// https://example.com/wrapped instead.
	fmt.Println("naive:", naiveReferrers(iface, "Task"))
	// `ob schema remove` acting on the naive answer: the edit gate catches
	// the missed reference only as a new OBI-D-12 violation.
	delete(iface.Schemas, "Task")
	after, _ := iface.Validate()
	for _, f := range after.Violations() {
		fmt.Println(f.Rule, f.Path)
	}
	// Output:
	// conformant
	// naive: [/schemas/Wrapped /operations/b/output]
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

// ------------------------- equality for merge: C1 item F7 (merge equality)

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

// ---------------------------- C1 item K8 (schemas in the model)

// A producer or editor changes a schema it read from a document. The model
// carries schema numbers as json.Number, so they come back as written; what
// the caller adds in Go is written as Go encodes it, and a json.RawMessage
// held in the any-typed member is written as given. A number is lost only
// when a caller decodes schema text into float64 itself. The any-typed
// member has its own hazard: a typed nil held there is a present null.
func Example_producerSchemaEdit() {
	iface, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"setLimit":{"input":
	  {"type":"object","properties":{"limit":{"type":"integer","maximum":9007199254740993,"multipleOf":1.0,"minimum":1e0}}}}}}`))
	if err != nil {
		panic(err)
	}
	operation := iface.Operations["setLimit"]
	input := operation.Input.(map[string]any)
	input["required"] = []any{"limit"}
	input["properties"].(map[string]any)["note"] = map[string]any{"type": "string", "maxLength": 280}
	// A schema the user typed as text is handed over as text.
	operation.Output = json.RawMessage(`{"type": "integer", "exclusiveMaximum": 18014398509481985}`)
	iface.Operations["setLimit"] = operation
	if _, err := iface.Validate(); err != nil {
		panic(err)
	}
	written, err := json.Marshal(iface)
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

	// K8's defined-type option needs JSON methods: a named byte slice
	// without them encodes as base64.
	type rawSchema json.RawMessage
	b, _ := json.Marshal(struct{ Input rawSchema }{rawSchema(`{"type":"string"}`)})
	fmt.Println(string(b))

	// The any-typed member's own hazard: absence is a nil interface, so a
	// typed nil held there (a nil json.RawMessage or a nil map) is a present
	// null, which OBI-D-02 and OBI-D-10 then report.
	for _, held := range []openbindings.JSONSchema{nil, json.RawMessage(nil), map[string]any(nil)} {
		doc := openbindings.Document{OpenBindings: openbindings.AuthoringVersion, Operations: map[string]openbindings.Operation{"o": {Input: held}}}
		out, _ := json.Marshal(doc)
		report, _ := doc.Validate()
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

// ------------------------------------------- the formatting boundary (F1)

// `ob fmt` promises "Entries keep the order you gave them" and "Values,
// including numbers, are kept exactly as written". The model cannot keep
// either: maps hold the entries, so encoding writes them in key order, and
// a preference is an int64, so 1e0 comes back as 1. Typed members come back
// in field order whatever order the input used. C1 item F1 (member order) allocates
// source order and token spelling to the CLI's own representation.
func Example_cliFormatBoundary() {
	iface, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"zeta":{},"alpha":{}},
	  "sources":{"s":{"kind":"k"}},
	  "bindings":{"zeta.s":{"operation":"zeta","source":"s","preference":1e0},"alpha.s":{"source":"s","operation":"alpha"}}}`))
	if err != nil {
		panic(err)
	}
	written, err := json.Marshal(iface)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(written))
	// Output:
	// {"openbindings":"0.2.0","operations":{"alpha":{},"zeta":{}},"sources":{"s":{"kind":"k"}},"bindings":{"alpha.s":{"operation":"alpha","source":"s"},"zeta.s":{"operation":"zeta","source":"s","preference":1}}}
}

// --------------------------------------------- C1 item F4 (references)

// docRef is one schema reference keyword in the schemas a document contains
// (§3, §7), with where its initial lookup lands. It has the shape of the
// bounded lookup C1 item F4 (references) proposes core export: the
// keyword's location, the initial target or why there is none, and which
// keywords are $dynamicRef. Here a caller writes it, re-implementing the §7
// index core already keeps.
//
// What a caller may conclude from it: every $ref and $dynamicRef keyword in
// the schemas the document contains, where each one is; for each, the schema
// its initial lookup identifies, or why it identifies none; and which ones
// are $dynamicRef keywords that may require dynamic-scope analysis. Whether
// one does is not decided here: a $dynamicRef looks the dynamic scope up only
// when its fragment is a plain name and its initial target declares that name
// as a $dynamicAnchor (core checks both); otherwise it resolves as its initial
// target. What a caller may not conclude: that a schema no
// reference targets is unused, or that the schemas a closure of initial
// targets reaches are all an operation needs. A $dynamicRef can land on any
// schema in the dynamic scope that declares the matching $dynamicAnchor, and
// the lookup does not resolve dynamic scope; a caller that removes or copies
// schemas needs a policy for that (Example_cliSchemaRemove,
// Example_cliMergeClosure).
type docRef struct {
	At         string // RFC 6901 pointer to the keyword member
	Keyword    string // $ref or $dynamicRef
	Value      string // as written
	Base       string // "" in the document resource; otherwise the $id it resolves against
	Target     string // pointer to the schema the initial lookup identifies
	Unresolved string // why Target is empty
}

// Dynamic reports whether the keyword is a $dynamicRef, which may require
// dynamic-scope analysis: evaluation may then land elsewhere than Target
// (§7.4). It does not decide whether the lookup happens.
func (r docRef) Dynamic() bool { return r.Keyword == "$dynamicRef" }

// dynamicName is the plain name a $dynamicRef looks up in the dynamic scope,
// or "" when its fragment is not a plain name.
func (r docRef) dynamicName() string {
	_, fragment, _ := strings.Cut(r.Value, "#")
	name, err := url.PathUnescape(fragment)
	if !r.Dynamic() || err != nil || name == "" || strings.HasPrefix(name, "/") {
		return ""
	}
	return name
}

func (r docRef) String() string {
	landing := "-> " + r.Target
	if r.Target == "" {
		landing = "unresolved: " + r.Unresolved
	}
	if r.Dynamic() {
		landing += " (initial; $dynamicRef)"
	}
	return fmt.Sprintf("%s %q %s", r.At, r.Value, landing)
}

type anchorDeclaration struct{ name, at string }

type refIndex struct {
	refs      []docRef
	schemas   map[string]string   // every schema the document contains: pointer -> its resource ("" = the document resource)
	resources map[string]string   // $id resource -> pointer of its root
	declared  map[string][]string // resource + "#" + plain name -> where each $anchor and $dynamicAnchor declares it
	dynamic   []anchorDeclaration // every $dynamicAnchor
	stopped   []string            // where the walk stopped at its nesting limit
}

var (
	refMapKeywords   = []string{"$defs", "definitions", "properties", "patternProperties", "dependentSchemas", "dependencies"}
	refValueKeywords = []string{"additionalProperties", "propertyNames", "items", "contains", "not", "if", "then", "else", "unevaluatedItems", "unevaluatedProperties", "contentSchema"}
	refArrayKeywords = []string{"prefixItems", "allOf", "anyOf", "oneOf"}
)

// refNestingLimit is where the walk stops: 256 levels, core's schema index
// limit (schemaDepthLimit). A stopped walk makes the result incomplete, not
// empty. This is conservative relative to the validator: OBI-D-12's own
// reference walk goes deeper and still reports broken references there.
const refNestingLimit = 256

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

// errIncomplete marks a lookup that could not index the whole document: the
// references it returns are some, not all, so no caller may act on what is
// missing from them.
var errIncomplete = errors.New("the reference index is incomplete")

// referencesOf is the lookup with its whole-call failure contract, the
// error-bearing result C1 item F4 (references) proposes for References: a
// version this SDK does not interpret is refused, and a document declaring
// no valid version is not interpreted (as ValueContractCompiler.Resolve
// does); a document that cannot be encoded is an error; and a walk that
// meets the nesting limit returns what it found with an error matching
// errIncomplete. A nil error means the index is complete, so an empty
// result with a nil error means the document holds no reference; a nil
// document is such a result.
func referencesOf(doc *openbindings.Document) ([]docRef, *refIndex, error) {
	if doc == nil {
		return nil, nil, nil // no document holds no reference
	}
	switch supported, err := openbindings.IsSupportedVersion(doc.OpenBindings); {
	case err != nil:
		return nil, nil, fmt.Errorf("the document declares no valid version (%q), so it is not interpreted", doc.OpenBindings)
	case !supported:
		return nil, nil, &openbindings.VersionRefusalError{Version: doc.OpenBindings, Reason: "a version this lookup does not interpret"}
	}
	view, err := genericView(doc)
	if err != nil {
		return nil, nil, fmt.Errorf("the document cannot be encoded: %w", err)
	}
	ix := indexReferences(view)
	if len(ix.stopped) > 0 {
		return ix.refs, ix, fmt.Errorf("%w: the walk stopped at %d levels at %v", errIncomplete, refNestingLimit, ix.stopped)
	}
	return ix.refs, ix, nil
}

// indexReferences walks the OBI positions (§7): schemas entries and
// operation input and output, through the keywords §7 lists, entering $id
// resources. Source and binding content, examples, and x- members are not
// schemas, so a $ref-shaped member there is ordinary data and never listed.
// It follows no reference, so a cycle cannot trap it.
func indexReferences(view map[string]any) *refIndex {
	ix := &refIndex{schemas: map[string]string{}, resources: map[string]string{}, declared: map[string][]string{}}
	if schemas, ok := view["schemas"].(map[string]any); ok {
		for _, name := range slices.Sorted(maps.Keys(schemas)) {
			ix.walk(schemas[name], []string{"schemas", name}, "", 0)
		}
	}
	if operations, ok := view["operations"].(map[string]any); ok {
		for _, key := range slices.Sorted(maps.Keys(operations)) {
			operation, _ := operations[key].(map[string]any)
			for _, side := range []string{"input", "output"} {
				if schema, present := operation[side]; present {
					ix.walk(schema, []string{"operations", key, side}, "", 0)
				}
			}
		}
	}
	for i := range ix.refs {
		ix.resolve(&ix.refs[i])
	}
	slices.SortFunc(ix.refs, func(a, b docRef) int { return strings.Compare(a.At, b.At) })
	return ix
}

func (ix *refIndex) walk(v any, at []string, base string, depth int) {
	here := pointerOf(at...)
	if depth > refNestingLimit {
		ix.stopped = append(ix.stopped, here)
		return
	}
	switch schema := v.(type) {
	case bool:
		ix.schemas[here] = base
	case map[string]any:
		if id, ok := schema["$id"].(string); ok {
			base = resolveReference(base, id)
			ix.resources[base] = here
		}
		ix.schemas[here] = base
		// Each $anchor and each $dynamicAnchor declaration counts, even two
		// on one schema (OBI-D-13 counts them so, and so does core).
		for _, keyword := range []string{"$anchor", "$dynamicAnchor"} {
			if name, ok := schema[keyword].(string); ok {
				key := base + "#" + name
				ix.declared[key] = append(ix.declared[key], here)
				if keyword == "$dynamicAnchor" {
					ix.dynamic = append(ix.dynamic, anchorDeclaration{name, here})
				}
			}
		}
		for _, keyword := range []string{"$ref", "$dynamicRef"} {
			if value, ok := schema[keyword].(string); ok {
				ix.refs = append(ix.refs, docRef{At: pointerOf(append(slices.Clone(at), keyword)...), Keyword: keyword, Value: value, Base: base})
			}
		}
		for _, keyword := range refMapKeywords {
			members, _ := schema[keyword].(map[string]any)
			for _, name := range slices.Sorted(maps.Keys(members)) {
				if _, isArray := members[name].([]any); isArray {
					continue // a legacy dependencies array lists names, not a schema
				}
				ix.walk(members[name], append(slices.Clone(at), keyword, name), base, depth+1)
			}
		}
		for _, keyword := range refValueKeywords {
			if sub, ok := schema[keyword]; ok {
				ix.walk(sub, append(slices.Clone(at), keyword), base, depth+1)
			}
		}
		for _, keyword := range refArrayKeywords {
			items, _ := schema[keyword].([]any)
			for i, sub := range items {
				ix.walk(sub, append(slices.Clone(at), keyword, strconv.Itoa(i)), base, depth+1)
			}
		}
	}
}

// resolveReference resolves ref against base (RFC 3986 §5.2) and drops an
// empty fragment.
func resolveReference(base, ref string) string {
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	if b, err := url.Parse(base); base != "" && err == nil {
		r = b.ResolveReference(r)
	}
	return strings.TrimSuffix(r.String(), "#")
}

// wellFormedFragment reports whether s is an RFC 3986 fragment: pchar, "/",
// and "?", with every "%" starting an escape. OBI-D-05 requires it.
func wellFormedFragment(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%':
			if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
				return false
			}
			i += 2
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', strings.IndexByte("-._~!$&'()*+,;=:@/?", c) >= 0:
		default:
			return false
		}
	}
	return true
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// plainName looks a plain name up in a resource: it identifies a schema only
// when exactly one declaration names it (§7.4: a plain name declared twice
// leaves the result undefined).
func (ix *refIndex) plainName(r *docRef, resource, name string) {
	switch at := ix.declared[resource+"#"+name]; len(at) {
	case 0:
		where := "the document resource"
		if resource != "" {
			where = resource
		}
		r.Unresolved = "no plain name " + name + " in " + where
	case 1:
		r.Target = at[0]
	default:
		r.Unresolved = "the plain name " + name + " is declared more than once, so the result is undefined (§7.4)"
	}
}

func (ix *refIndex) resolve(r *docRef) {
	if r.Base == "" && (r.Value == "" || strings.HasPrefix(r.Value, "#")) {
		ix.lookUpInDocument(r) // OBI-D-12's lookup (§7.2, §7.3)
		return
	}
	parsed, err := url.Parse(r.Value)
	if err != nil || r.Base == "" && !parsed.IsAbs() {
		r.Unresolved = "neither absolute nor same-document (OBI-D-05)"
		return
	}
	resource, fragment, _ := strings.Cut(resolveReference(r.Base, r.Value), "#")
	root, inDocument := ix.resources[resource]
	if !inDocument {
		r.Unresolved = "outside the document, an external schema (§7.4)"
		return
	}
	name, err := url.PathUnescape(fragment)
	switch {
	case err != nil:
		r.Unresolved = "its fragment does not percent-decode"
	case name == "":
		r.Target = root
	case strings.HasPrefix(name, "/"):
		// Within a resource, JSON Schema follows the pointer through the
		// resource's document (RFC 6901), nested resources included, as core does.
		if _, isSchema := ix.schemas[root+name]; isSchema {
			r.Target = root + name
		} else {
			r.Unresolved = "no schema of " + resource + " there"
		}
	default:
		ix.plainName(r, resource, name)
	}
}

func (ix *refIndex) lookUpInDocument(r *docRef) {
	fragment := strings.TrimPrefix(r.Value, "#")
	if !wellFormedFragment(fragment) {
		r.Unresolved = "not a well-formed URI-reference (OBI-D-05)"
		return
	}
	name, err := url.PathUnescape(fragment)
	switch {
	case err != nil || !utf8.ValidString(name):
		r.Unresolved = "its fragment does not decode to UTF-8"
	case name == "":
		r.Unresolved = "it names the document, not a schema (§7.2)"
	case strings.HasPrefix(name, "/"):
		if _, isSchema := ix.schemas[name]; !isSchema {
			r.Unresolved = "no schema at an OBI position there (§7.3)"
			return
		}
		// A pointer may land on a schema that declares $id, never pass
		// through one: every enclosing boundary is checked (§7.3).
		for _, root := range ix.resources {
			if strings.HasPrefix(name, root+"/") {
				r.Unresolved = "inside a schema that declares $id (§7.3)"
				return
			}
		}
		r.Target = name
	default:
		ix.plainName(r, "", name)
	}
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
func retarget(view map[string]any, refs []docRef, from, to string) []string {
	var rewritten []string
	for _, r := range refs {
		pointer, err := url.PathUnescape(strings.TrimPrefix(r.Value, "#"))
		if r.Base != "" || !strings.HasPrefix(r.Value, "#") || err != nil || !strings.HasPrefix(pointer, "/") || r.Target == "" || !under(r.Target, from) {
			continue
		}
		spelled := (&url.URL{Fragment: to + strings.TrimPrefix(r.Target, from)}).String()
		setAt(view, r.At, spelled)
		rewritten = append(rewritten, r.At+": "+r.Value+" -> "+spelled)
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

func lookUp(document string) (*openbindings.Document, map[string]any, []docRef, *refIndex) {
	doc, err := openbindings.ParseDocument([]byte(document))
	if err != nil {
		panic(err)
	}
	refs, ix, err := referencesOf(doc)
	if err != nil {
		panic(err)
	}
	view, err := genericView(doc)
	if err != nil {
		panic(err)
	}
	return doc, view, refs, ix
}

// The lookup over the document: every reference keyword in the schemas the
// document contains, and nothing from content, examples, or x- members.
func Example_cliReferenceLookup() {
	doc, _, refs, _ := lookUp(referencesOBI)
	report, _ := doc.Validate()
	fmt.Println(report.Conclusion)
	for _, r := range refs {
		fmt.Println(r)
	}
	// Output:
	// conformant
	// /operations/a/input/$ref "#task" -> /schemas/Task
	// /operations/a/output/$ref "#/schemas/List" -> /schemas/List
	// /operations/b/input/$ref "#/operations/a/output" -> /operations/a/output
	// /operations/b/output/$ref "https://example.com/wrapped" -> /schemas/Wrapped
	// /operations/c/input/$ref "https://schemas.example.com/address.json" unresolved: outside the document, an external schema (§7.4)
	// /operations/d/output/$ref "#%2Fschemas%2FTask" -> /schemas/Task
	// /schemas/List/items/$ref "#/schemas/T%61sk" -> /schemas/Task
	// /schemas/Task/properties/next/$ref "#/schemas/Task" -> /schemas/Task
	// /schemas/Tree/properties/kids/items/$dynamicRef "#node" -> /schemas/Tree (initial; $dynamicRef)
	// /schemas/Wrapped/$defs/x/$ref "#/schemas/Task" unresolved: no schema of https://example.com/wrapped there
	// /schemas/Wrapped/properties/t/$ref "https://example.com/wrapped#/$defs/x" -> /schemas/Wrapped/$defs/x
}

// The lookup against the SDK: every reference of the spec's §7.5 table,
// and the boundary and duplicate-name cases, each as an operation's input
// reference. The SDK's answer is OBI-D-12 and OBI-D-05 for a reference in the
// document resource, and the value contract otherwise: an undefined result
// or an unresolvable reference means the lookup identifies nothing.
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
		iface, report, _ := openbindings.ValidateDocument([]byte(document))
		at := "/operations/op/input/$ref"
		sdk := "resolves"
		for _, finding := range report.Violations() {
			if (finding.Rule == "OBI-D-12" || finding.Rule == "OBI-D-05") && finding.Path == at {
				sdk = "fails " + finding.Rule
			}
		}
		if sdk == "resolves" && iface != nil {
			contracts, err := compiler.Resolve(context.Background(), iface)
			if err != nil {
				panic(err)
			}
			contract, _ := contracts.CompileInput(context.Background(), "op")
			var noVerdict *openbindings.NoVerdictError
			if errors.As(contract.Err(), &noVerdict) && noVerdict.Location != "" {
				sdk = "no target (" + map[bool]string{true: "undefined", false: "core refuses"}[errors.Is(noVerdict, openbindings.ErrUndefined)] + ")"
			}
		}
		var iface2 openbindings.Document
		if err := json.Unmarshal([]byte(document), &iface2); err != nil {
			panic(err)
		}
		mine := "resolves"
		refs, _, err := referencesOf(&iface2)
		if err != nil {
			panic(err)
		}
		for _, r := range refs {
			if r.At == at && r.Target == "" {
				mine = "no target: " + r.Unresolved
			}
		}
		if (mine == "resolves") == (sdk == "resolves") {
			agreed++
		}
		fmt.Printf("%-60s SDK %-24s lookup %s\n", c.ref, sdk, mine)
	}
	fmt.Println("agree:", agreed, "of", len(cases))
	// Output:
	// #                                                            SDK fails OBI-D-12           lookup no target: it names the document, not a schema (§7.2)
	// #/schemas/Task                                               SDK resolves                 lookup resolves
	// #/schemas/Task/properties/my%20type                          SDK resolves                 lookup resolves
	// #/schemas/Task/properties/my type                            SDK fails OBI-D-05           lookup no target: not a well-formed URI-reference (OBI-D-05)
	// #/schemas/Task/properties/my%2520type                        SDK fails OBI-D-12           lookup no target: no schema at an OBI position there (§7.3)
	// #%2Fschemas%2FTask                                           SDK resolves                 lookup resolves
	// #/schemas/Task/type                                          SDK fails OBI-D-12           lookup no target: no schema at an OBI position there (§7.3)
	// #/operations                                                 SDK fails OBI-D-12           lookup no target: no schema at an OBI position there (§7.3)
	// #/schemas/Missing                                            SDK fails OBI-D-12           lookup no target: no schema at an OBI position there (§7.3)
	// #/schemas/~2                                                 SDK fails OBI-D-12           lookup no target: no schema at an OBI position there (§7.3)
	// #/schemas/Tree                                               SDK resolves                 lookup resolves
	// #/schemas/Tree/properties/children                           SDK fails OBI-D-12           lookup no target: inside a schema that declares $id (§7.3)
	// #task                                                        SDK resolves                 lookup resolves
	// #t%61sk                                                      SDK resolves                 lookup resolves
	// #tree                                                        SDK fails OBI-D-12           lookup no target: no plain name tree in the document resource
	// #%FF                                                         SDK fails OBI-D-12           lookup no target: its fragment does not decode to UTF-8
	// tree.json#/properties/children                               SDK fails OBI-D-05           lookup no target: neither absolute nor same-document (OBI-D-05)
	// https://example.com/schemas/tree.json#/properties/children   SDK resolves                 lookup resolves
	// #/schemas/A/properties/x                                     SDK fails OBI-D-12           lookup no target: inside a schema that declares $id (§7.3)
	// https://ex.test/a#/properties/x/properties/y                 SDK resolves                 lookup resolves
	// https://ex.test/a#n                                          SDK no target (undefined)    lookup no target: the plain name n is declared more than once, so the result is undefined (§7.4)
	// #n                                                           SDK no target (undefined)    lookup no target: the plain name n is declared more than once, so the result is undefined (§7.4)
	// #n                                                           SDK no target (undefined)    lookup no target: the plain name n is declared more than once, so the result is undefined (§7.4)
	// agree: 23 of 23
}

// `ob schema rename <obi> Task Todo` and `ob operation rename <obi> a alpha`
// on the lookup, against the lab's rewrite.
func Example_cliSchemaRename() {
	_, view, refs, _ := lookUp(referencesOBI)
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
	_, view, refs, _ = lookUp(referencesOBI)
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
	_, view, _, _ = lookUp(referencesOBI)
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
	removable := func(ix *refIndex, refs []docRef, name string) string {
		root := pointerOf("schemas", name)
		var by []string
		for _, r := range refs {
			if !under(r.At, root) && r.Target != "" && under(r.Target, root) {
				by = append(by, r.At)
			}
		}
		if len(by) > 0 {
			return fmt.Sprintf("refused: referenced from %v", by)
		}
		for _, declaration := range ix.dynamic {
			if !under(declaration.at, root) {
				continue
			}
			for _, r := range refs {
				if !under(r.At, root) && r.dynamicName() == declaration.name {
					return fmt.Sprintf("refused: %s declares $dynamicAnchor %s, which %s may resolve to dynamically", declaration.at, declaration.name, r.At)
				}
			}
		}
		return "removable"
	}
	_, _, refs, ix := lookUp(referencesOBI)
	for _, name := range []string{"Task", "Wrapped", "Tree", "List"} {
		fmt.Println(name, removable(ix, refs, name))
	}
	// The SDK's scope-wrapper fixture: no reference targets Override, yet
	// removing it changes what inDocument accepts, in a document that stays
	// conformant.
	_, _, refs, ix = lookUp(dynamicOBI)
	fmt.Println("Override", removable(ix, refs, "Override"))
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
// conservative policy refuses the merge and names the candidates.
func Example_cliMergeClosure() {
	closure := func(ix *refIndex, refs []docRef, key string) string {
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
				if !under(r.At, here) {
					continue
				}
				if name := r.dynamicName(); name != "" {
					var candidates []string
					for _, declaration := range ix.dynamic {
						if declaration.name == name {
							candidates = append(candidates, declaration.at)
						}
					}
					dynamic = append(dynamic, fmt.Sprintf("%s may resolve to %v", r.At, candidates))
				}
				tokens := strings.Split(r.Target, "/")
				switch {
				case r.Target == "":
					unresolved = append(unresolved, r.At)
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
	_, _, refs, ix := lookUp(referencesOBI)
	for _, key := range []string{"a", "b", "c", "d"} {
		fmt.Printf("%s: %s\n", key, closure(ix, refs, key))
	}
	_, _, refs, ix = lookUp(dynamicOBI)
	fmt.Printf("inDocument: %s\n", closure(ix, refs, "inDocument"))
	fmt.Printf("inResource: %s\n", closure(ix, refs, "inResource"))
	// Output:
	// a: schemas [List Task], other operations [], unresolved []
	// b: schemas [Wrapped], other operations [/operations/a/output], unresolved [/schemas/Wrapped/$defs/x/$ref]
	// c: schemas [], other operations [], unresolved [/operations/c/input/$ref]
	// d: schemas [Task], other operations [], unresolved []
	// inDocument: refused, incomplete: [/schemas/List/items/$dynamicRef may resolve to [/schemas/List/$defs/item /schemas/Override]]
	// inResource: refused, incomplete: [/schemas/List/items/$dynamicRef may resolve to [/schemas/List/$defs/item /schemas/Override]]
}

// The lookup's whole-call failures, which per-reference Unresolved cannot
// carry: a version this lookup does not interpret, a document declaring no
// valid version, a document that cannot be encoded, and an index cut short
// at the nesting limit, which returns what it found and an error. Only a nil
// error makes an empty result mean "no references", as for a nil document.
func Example_cliReferenceFailures() {
	report := func(name string, doc *openbindings.Document) {
		refs, _, err := referencesOf(doc)
		var refusal *openbindings.VersionRefusalError
		switch {
		case errors.As(err, &refusal):
			fmt.Println(name+": refused, version", refusal.Version)
		case errors.Is(err, errIncomplete):
			fmt.Println(name+":", len(refs), "references found, and incomplete")
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
	// no version: the document declares no valid version ("0.2"), so it is not interpreted
	// unencodable: the document cannot be encoded: json: error calling MarshalJSON for type *openbindings.Document: json: error calling MarshalJSON for type openbindings.Operation: json: unsupported value: NaN
	// deep: 1 references found, and incomplete
	// no references: 0 references, complete
	// nil document: 0 references, complete
}

// ---------------------------------- C1 item F17 (evaluator cost)

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
	iface, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","schemas":{"Name":{"type":"string"}},"operations":{
	  "name":{"input":{"$ref":"#/schemas/Name"}},
	  "count":{"input":{"type":"integer"}},
	  "short":{"input":{"type":"string","maxLength":3}}}}`))
	if err != nil {
		panic(err)
	}
	contracts, err := compiler.Resolve(ctx, iface)
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
