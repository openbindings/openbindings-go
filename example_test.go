package openbindings_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"

	"github.com/openbindings/openbindings-go"
)

func ExampleInterface_basic() {
	data := []byte(`{
		"openbindings": "0.2.0",
		"name": "Example API",
		"operations": {
			"getUser": {
				"description": "Get a user by ID"
			}
		}
	}`)

	var iface openbindings.Interface
	if err := json.Unmarshal(data, &iface); err != nil {
		log.Fatal(err)
	}

	fmt.Println(openbindings.Value(iface.Name))
	fmt.Println(openbindings.Value(iface.Operations["getUser"].Description))
	// Output:
	// Example API
	// Get a user by ID
}

func ExampleInterface_Validate() {
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

	var iface openbindings.Interface
	if err := json.Unmarshal(data, &iface); err != nil {
		log.Fatal(err)
	}

	// The error lists every violation established, so it gates on them.
	if _, err := iface.Validate(openbindings.ValidateOptions{}); err != nil {
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
	_, report, err := openbindings.ValidateDocument(data, openbindings.ValidateOptions{})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(report.Conclusion)
	// Output: conformant
}

func ExampleInterface_Validate_unknownFields() {
	data := []byte(`{
		"openbindings": "0.2.0",
		"unknownFeild": "a typo, or a field from a later version",
		"operations": {
			"getUser": {"description": "Get a user"}
		}
	}`)

	var iface openbindings.Interface
	_ = json.Unmarshal(data, &iface)

	// An unprefixed name the specification does not define is reserved for it
	// (§12), so the document is non-conformant (OBI-D-02); a tool processing
	// it still ignores the field (OBI-T-02), which the report notes too.
	report, err := iface.Validate(openbindings.ValidateOptions{})
	fmt.Println("violation established:", err != nil)
	for _, finding := range report.Violations() {
		fmt.Println(finding.Rule, finding.Message)
	}
	for _, diagnostic := range report.Diagnostics {
		fmt.Println(diagnostic.Rule, diagnostic.Message)
	}
	// Output:
	// violation established: true
	// OBI-D-02 does not validate against the document schema: additional properties 'unknownFeild' not allowed
	// OBI-T-02 unknown field ignored: unknownFeild; extensions use the x- prefix
}

func ExampleInterface_exact() {
	data := []byte(`{
		"openbindings": "0.2.0",
		"x-custom": "preserved",
		"operations": {}
	}`)

	var iface openbindings.Interface
	_ = json.Unmarshal(data, &iface)

	// Extensions are preserved
	fmt.Println("has x-custom:", iface.Extensions["x-custom"] != nil)

	// Re-marshal preserves the extension
	out, _ := json.Marshal(iface)
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

func ExampleBindingEntry() {
	// Content is whatever the source's binding specification defines for the
	// binding, such as its target and any value adaptation; this shape is
	// illustrative.
	binding := openbindings.BindingEntry{
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
