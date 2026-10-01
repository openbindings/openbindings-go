package openbindings_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"

	"github.com/openbindings/openbindings-go"
)

func ExampleDocument_basic() {
	data := []byte(`{
		"openbindings": "0.2.0",
		"name": "Example API",
		"operations": {
			"getUser": {
				"description": "Get a user by ID"
			}
		}
	}`)

	var doc openbindings.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		log.Fatal(err)
	}

	fmt.Println(openbindings.Value(doc.Name))
	fmt.Println(openbindings.Value(doc.Operations["getUser"].Description))
	// Output:
	// Example API
	// Get a user by ID
}

func ExampleDocument_Validate() {
	data := []byte(`{
		"openbindings": "0.2.0",
		"operations": {
			"getUser": {
				"description": "Get a user by ID"
			},
			"userCreated": {
				"description": "User creation event"
			}
		}
	}`)

	var doc openbindings.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		log.Fatal(err)
	}

	// The error lists every violation established, so it gates on them.
	if _, err := doc.Validate(); err != nil {
		fmt.Println("violation established:", err)
		return
	}
	fmt.Println("no violation established")
	// Output: no violation established
}

func ExampleValidateDocument() {
	data := []byte(`{
		"openbindings": "0.2.0",
		"operations": {
			"getUser": {"description": "Get a user by ID"}
		}
	}`)

	// ValidateDocument decides every document rule on the exact input bytes
	// and reports the §10.4 conclusion.
	_, report, err := openbindings.ValidateDocument(data)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(report.Conclusion)
	// Output: conformant
}

func ExampleDocument_Validate_unknownFields() {
	data := []byte(`{
		"openbindings": "0.2.0",
		"unknownFeild": "a typo, or a field from a later version",
		"operations": {
			"getUser": {"description": "Get a user"}
		}
	}`)

	var doc openbindings.Document
	_ = json.Unmarshal(data, &doc)

	// An unprefixed name the specification does not define is reserved for it
	// (§12), so the document is non-conformant (OBI-D-02).
	report, err := doc.Validate()
	fmt.Println("violation established:", err != nil)
	for _, finding := range report.Violations() {
		fmt.Println(finding.Rule, finding.Message)
	}
	// Output:
	// violation established: true
	// OBI-D-02 does not validate against the document schema: additional properties 'unknownFeild' not allowed
}

func ExampleDocument_exact() {
	data := []byte(`{
		"openbindings": "0.2.0",
		"x-custom": "preserved",
		"operations": {}
	}`)

	var doc openbindings.Document
	_ = json.Unmarshal(data, &doc)

	// Extensions are preserved
	fmt.Println("has x-custom:", doc.Extensions["x-custom"] != nil)

	// Re-marshal preserves the extension
	out, _ := json.Marshal(doc)
	fmt.Println("round-trip contains x-custom:", bytes.Contains(out, []byte("x-custom")))
	// Output:
	// has x-custom: true
	// round-trip contains x-custom: true
}

func ExampleOperation() {
	op := openbindings.Operation{
		Description: openbindings.Present("Create a new user"),
		Input: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
	}

	fmt.Println(openbindings.Value(op.Description))
	fmt.Println(op.Input.(map[string]any)["type"])
	// Output:
	// Create a new user
	// object
}

func ExampleSource() {
	// Content is whatever the source's binding specification defines; this
	// shape is illustrative.
	src := openbindings.Source{
		Kind:    "openbindings.openapi-3.1@1",
		Content: json.RawMessage(`{"location":"https://api.example.com/openapi.yaml"}`),
	}

	fmt.Println(src.Kind)
	fmt.Println(string(src.Content))
	// Output:
	// openbindings.openapi-3.1@1
	// {"location":"https://api.example.com/openapi.yaml"}
}

func ExampleBinding() {
	// Content is whatever the source's binding specification defines for the
	// binding, such as its target and any value adaptation; this shape is
	// illustrative.
	binding := openbindings.Binding{
		Operation: "processPayment",
		Source:    "stripe",
		Content:   json.RawMessage(`{"target":"#/paths/~1charges/post"}`),
	}

	fmt.Println(binding.Operation, binding.Source)
	fmt.Println(string(binding.Content))
	// Output:
	// processPayment stripe
	// {"target":"#/paths/~1charges/post"}
}

func ExampleDocument_OperationBindings() {
	doc, err := openbindings.ParseDocument([]byte(`{
		"openbindings": "0.2.0",
		"operations": {
			"createTask": {"aliases": ["tasks.create"]}
		},
		"sources": {
			"api": {"kind": "example.openapi@1"},
			"tools": {"kind": "example.mcp@1"}
		},
		"bindings": {
			"createTask.tools": {"operation": "createTask", "source": "tools"},
			"createTask.api": {"operation": "createTask", "source": "api", "preference": 10}
		}
	}`))
	if err != nil {
		log.Fatal(err)
	}

	// Resolve the name a caller gave, then find the bindings by the key it
	// resolves to (OBI-T-07): an alias finds no binding itself.
	key, _, found := doc.ResolveOperation("tasks.create")
	fmt.Println(key, found)
	for _, binding := range doc.OperationBindings(key) {
		fmt.Println(binding, doc.Sources[doc.Bindings[binding].Source].Kind)
	}
	fmt.Println(doc.OperationBindings("tasks.create"))
	// Output:
	// createTask true
	// createTask.api example.openapi@1
	// createTask.tools example.mcp@1
	// []
}
