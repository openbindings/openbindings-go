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
	// and reports the §10.5 conclusion.
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

	// Unknown fields are ignored, never rejected (OBI-T-02)...
	report, err := iface.Validate(openbindings.ValidateOptions{})
	fmt.Println("violation established:", err != nil)

	// ...but the report surfaces them as diagnostics so a typo is not silent.
	for _, diagnostic := range report.Diagnostics {
		fmt.Println(diagnostic.Rule, diagnostic.Message)
	}
	// Output:
	// violation established: false
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
	bs := openbindings.Source{
		BindingSpec: "openapi@3.1",
		Location:    openbindings.Present("https://api.example.com/openapi.yaml"),
	}

	fmt.Println(bs.BindingSpec)
	fmt.Println(*bs.Location)
	// Output:
	// openapi@3.1
	// https://api.example.com/openapi.yaml
}

func ExampleTransform() {
	// Per v0.2 §5.5, transforms are JSONata 2.1 expression strings.
	iface := openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations: map[string]openbindings.Operation{
			"processPayment": {},
		},
		Transforms: map[string]openbindings.Transform{
			"toStripeInput": "{ charge_amount: amount * 100 }",
		},
		Sources: map[string]openbindings.Source{
			"stripe": {BindingSpec: "openapi@3.1", Location: openbindings.Present("https://api.example.com/stripe.json")},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"processPayment.stripe": {
				Operation:      "processPayment",
				Source:         "stripe",
				InputTransform: &openbindings.TransformReference{Ref: "#/transforms/toStripeInput"},
			},
		},
	}

	binding := iface.Bindings["processPayment.stripe"]
	expr, ok := binding.InputTransform.Resolve(iface.Transforms)

	fmt.Println("Resolved:", ok)
	fmt.Println("Expression:", expr)
	// Output:
	// Resolved: true
	// Expression: { charge_amount: amount * 100 }
}

func ExampleTransformOrRef_inline() {
	// An inline transform is a bare JSONata expression string.
	tor := openbindings.InlineTransform("{ total: price * quantity }")

	expression, _ := tor.Resolve(nil)
	fmt.Println("Expression:", expression)
	// Output:
	// Expression: { total: price * quantity }
}
