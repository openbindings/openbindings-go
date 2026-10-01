package openbindings_test

// Consumer exercises for the core API (Loop C, stage C1, at 166b9b4).
//
// Each example is a caller's code at the current API, written to show
// where the API serves the caller and where it does not. Comments marked
// "C1 item" name the report item an awkward step supports. Three callers:
//
//   - the 0.2 CLI (ob-cli-surface-lab at f7a9d16, NewNextSurfaceRoot):
//     reading a document, validating and reporting it, refusing an
//     unsupported version, editing in place, resolving an operation and
//     choosing its binding, checking a dependency's kinds, finding a
//     schema's referrers, comparing entries for merge, and the media type.
//     Validating values against value contracts needs an evaluator, so
//     those exercises are in schemaeval/consumer_examples_test.go;
//   - a producer: building a document in code, writing it, reading it back,
//     and amending a report;
//   - an evaluator author: see schemaeval/consumer_examples_test.go, which
//     runs a third-party evaluator through openbindingstest.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
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
func cliRead(data []byte) (*openbindings.Interface, int, string) {
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
	// C1 item ParseDocument-undecided: the other errors (nesting past the
	// decoder, a lone surrogate, the document schema reaching no verdict)
	// carry no type or sentinel, so a caller can tell them apart, or from
	// an internal failure, only by the message. Their prefix, "parse
	// document:", is also not the "openbindings:" prefix of the sentinels
	// (C1 item error-prefix).
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
	// C1 item d02-member-location: the unknown member's finding is located
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
		var held openbindings.Interface
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
		// C1 item IsSupportedVersion: the signature invites `if ok, _ :=
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
func cliEdit(data []byte, edit func(*openbindings.Interface)) ([]byte, error) {
	// ParseDocument refuses a document violating the document schema, which
	// an editor must still open, so the editor reads with ValidateDocument:
	// the decoded document and the rules it already breaks.
	iface, before, err := openbindings.ValidateDocument(data)
	var refusal *openbindings.VersionRefusalError
	switch {
	case errors.As(err, &refusal):
		return nil, refusal
	case iface == nil:
		// C1 item nil-document: the model cannot carry the document (or
		// OBI-D-01 refuses it), and the only sign of it is a nil
		// *Interface beside a nil error for a lone surrogate.
		return nil, fmt.Errorf("cannot edit: %s", before.Conclusion)
	}
	had := map[string]bool{}
	for _, f := range before.Violations() {
		had[f.Rule+" "+f.Path] = true
	}
	edit(iface)
	after, _ := iface.Validate()
	var added []string
	for _, f := range after.Violations() {
		if !had[f.Rule+" "+f.Path] {
			added = append(added, f.Rule+" at "+f.Path)
		}
	}
	if len(added) > 0 {
		return nil, fmt.Errorf("refused: the change would add %s", strings.Join(added, ", "))
	}
	return json.MarshalIndent(iface, "", "  ")
}

func Example_cliEdit() {
	addOperation := func(key string, input openbindings.JSONSchema) func(*openbindings.Interface) {
		return func(i *openbindings.Interface) {
			i.Operations[key] = openbindings.Operation{Input: input}
		}
	}
	written, err := cliEdit([]byte(tasksOBI), addOperation("archiveTask", map[string]any{"$ref": "#/schemas/Task"}))
	fmt.Println(err, memberOrder(written))

	_, err = cliEdit([]byte(tasksOBI), addOperation("x", map[string]any{"type": 42}))
	fmt.Println(err)

	// C1 item member-order: the same edit to a document holding one x-
	// member writes every top-level member in sorted order instead of field
	// order, so `name` now precedes `openbindings`.
	withExtension := strings.Replace(tasksOBI, `"name"`, `"x-owner": "tasks-team", "name"`, 1)
	written, err = cliEdit([]byte(withExtension), addOperation("archiveTask", true))
	fmt.Println(err, memberOrder(written))

	// C1 item html-escape: a description holding "<" is rewritten as
	// a \u003c escape by any edit, whatever the encoder's SetEscapeHTML.
	withMarkup := strings.Replace(tasksOBI, `"version": "1.4.0",`, `"version": "1.4.0", "description": "a<b",`, 1)
	written, _ = cliEdit([]byte(withMarkup), addOperation("archiveTask", true))
	escaped := `"a` + `\` + `u003cb"`
	fmt.Println(bytes.Contains(written, []byte(`"a<b"`)), bytes.Contains(written, []byte(escaped)))
	var held openbindings.Interface
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
	// <nil> [bindings dependencies name openbindings operations schemas sources version x-owner]
	// false true
	// false true
}

// bindingsOf finds a resolved operation's bindings by its key (OBI-T-07), in
// key order. C1 item bindings-by-key: the core has no helper for it, so the
// CLI (invoke, show, operation list, mcp), Loop A's adapter, and the spec's
// Go runner (tool_scenarios.go, resolve-operation) each write this loop.
func bindingsOf(iface *openbindings.Interface, key string) []string {
	var keys []string
	for bindingKey, binding := range iface.Bindings {
		if binding.Operation == key {
			keys = append(keys, bindingKey)
		}
	}
	slices.Sort(keys)
	return keys
}

// cliInvokeChoice is how `ob invoke <obi> <operation> [--binding B]...`
// chooses a binding: resolve the name (OBI-T-07), find the operation's
// bindings by its key, read each binding's source kind, and take the first
// named binding ob can invoke, or the sole one; otherwise refuse, listing
// the candidates with preference and deprecation, which are shown and never
// used to choose.
func cliInvokeChoice(iface *openbindings.Interface, name string, canInvoke map[string]bool, named ...string) string {
	key, _, found := openbindings.ResolveOperation(iface, name)
	if !found {
		return fmt.Sprintf("refused: no operation named %q", name)
	}
	bindings := bindingsOf(iface, key)
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
			if key, _, found := openbindings.ResolveOperation(provider, published); found {
				providerKey = key
				break
			}
		}
		var meets []string
		for _, b := range bindingsOf(provider, providerKey) {
			if dependency.AllowsKind(provider.Sources[provider.Bindings[b].Source].Kind) {
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
	doc := openbindings.Interface{
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
		Bindings: map[string]openbindings.BindingEntry{
			// Present(10) would be a *int: the preference member is *int64.
			"createTask.http": {Operation: "createTask", Source: "httpApi", Preference: openbindings.Present[int64](10), Idempotent: openbindings.Present(false)},
		},
		Dependencies: map[string]openbindings.DependencyEntry{
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
	// C1 item JSONSchema-any: the same document is not the same Go value.
	// A schema decodes to generic JSON values, every number a json.Number
	// and every array an []any, so the producer's own values do not come
	// back.
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

// A tool that decides a rule this SDK leaves inconclusive (here OBI-D-10,
// with a meta-schema check of its own that has no depth limit) amends the
// evidence and concludes again, as ConcludeConformance's documentation
// suggests.
func Example_producerAmendReport() {
	data := []byte(`{"openbindings":"0.2.0","operations":{"op":{"input":` + deeplyNested + `}},
	  "bindings":{"b":{"operation":"gone","source":"s"}},"sources":{"s":{"kind":"k"}}}`)
	_, report, _ := openbindings.ValidateDocument(data)
	fmt.Println(report.Conclusion, report.Violated, report.Inconclusive, len(report.Findings), "findings")

	evidence := maps.Clone(report.Evidence)
	evidence["OBI-D-10"] = openbindings.EvidenceSatisfied // the tool's own check
	amended := openbindings.ConcludeConformance(evidence)
	fmt.Println(amended.Conclusion, amended.Violated, amended.Inconclusive)
	// C1 item amend-report: the amended report names no specification text
	// (OBI-T-09 requires it) and has lost every finding, the OBI-D-07
	// violation's location included. The caller restores them by hand.
	fmt.Printf("version %q, revision %q, %d findings\n", amended.Version, amended.Revision, len(amended.Findings))
	amended.Version, amended.Revision = report.Version, report.Revision
	for _, finding := range report.Findings {
		if finding.Rule != "OBI-D-10" {
			amended.Findings = append(amended.Findings, finding)
		}
	}
	fmt.Println(amended.Version, len(amended.Findings), "findings:", amended.Findings[0].Rule, amended.Findings[0].Path)
	// Output:
	// non-conformant [OBI-D-07] [OBI-D-10] 2 findings
	// non-conformant [OBI-D-07] []
	// version "", revision "", 0 findings
	// 0.2.0 1 findings: OBI-D-07 /bindings/b/operation
}

// naiveReferrers is what `ob schema list` ("referenced by"), `schema rename`,
// `schema remove`, and `merge` (which brings the named schemas a merged
// operation references) need: where the document references a named schema.
// C1 item references: the core resolves §7 references internally (OBI-D-12,
// value contracts) but exports no lookup, so the CLI walks the JSONSchema
// values itself. This walk is the lab's (surface_next_fixture.go,
// nxSchemaReferrers), and it misreads §7 twice: it misses a fragment that
// is percent-decoded before lookup (§7.2), and it counts a reference inside
// a schema that declares $id, which resolves against that resource's base,
// not the document (§7.2).
func naiveReferrers(iface *openbindings.Interface, name string) []string {
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

// `ob merge <obi> <from>` skips an entry both documents hold identically and
// refuses one they hold differently, so it compares entries as JSON values.
// C1 item json-equality: the model keeps raw members as written, so two
// decodings of one JSON value differ as Go values when their whitespace
// differs; the caller compares encodings instead, which compacts raw
// members but keeps number spellings (1.0 and 1) and string escapes.
func Example_cliMergeIdentical() {
	ours, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"ping":{"input":{"maximum":1}}},
	  "sources":{"api":{"kind":"example.openapi@1","content":{ "location" : "https://api.example.com" }}}}`))
	if err != nil {
		panic(err)
	}
	theirs, err := openbindings.ParseDocument([]byte(`{"openbindings":"0.2.0","operations":{"ping":{"input":{"maximum":1.0}}},
	  "sources":{"api":{"kind":"example.openapi@1","content":{"location":"https://api.example.com"}}}}`))
	if err != nil {
		panic(err)
	}
	encodedEqual := func(a, b any) bool {
		x, errX := json.Marshal(a)
		y, errY := json.Marshal(b)
		return errX == nil && errY == nil && bytes.Equal(x, y)
	}
	fmt.Println("source: same Go value", reflect.DeepEqual(ours.Sources["api"], theirs.Sources["api"]), "same encoding", encodedEqual(ours.Sources["api"], theirs.Sources["api"]))
	fmt.Println("operation: same Go value", reflect.DeepEqual(ours.Operations["ping"], theirs.Operations["ping"]), "same encoding", encodedEqual(ours.Operations["ping"], theirs.Operations["ping"]))
	// Output:
	// source: same Go value false same encoding true
	// operation: same Go value false same encoding false
}
