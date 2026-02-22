package openbindings_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"

	"github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/canonicaljson"
	"github.com/openbindings/openbindings-go/formattoken"
)

func ExampleInterface_basic() {
	data := []byte(`{
		"openbindings": "0.1.0",
		"name": "Example API",
		"operations": {
			"getUser": {
				"kind": "method",
				"description": "Get a user by ID"
			}
		}
	}`)

	var iface openbindings.Interface
	if err := json.Unmarshal(data, &iface); err != nil {
		log.Fatal(err)
	}

	fmt.Println(iface.Name)
	fmt.Println(iface.Operations["getUser"].Kind)
	// Output:
	// Example API
	// method
}

func ExampleInterface_Validate() {
	data := []byte(`{
		"openbindings": "0.1.0",
		"operations": {
			"getUser": {
				"kind": "method"
			},
			"userCreated": {
				"kind": "event",
				"payload": {"type": "object"}
			}
		}
	}`)

	var iface openbindings.Interface
	if err := json.Unmarshal(data, &iface); err != nil {
		log.Fatal(err)
	}

	if err := iface.Validate(); err != nil {
		fmt.Println("invalid:", err)
	} else {
		fmt.Println("valid")
	}
	// Output: valid
}

func ExampleInterface_Validate_strict() {
	data := []byte(`{
		"openbindings": "0.1.0",
		"unknownField": "should fail in strict mode",
		"operations": {
			"getUser": {"kind": "method"}
		}
	}`)

	var iface openbindings.Interface
	_ = json.Unmarshal(data, &iface)

	// Default: unknown fields are allowed (forward-compat)
	err := iface.Validate()
	fmt.Println("default:", err == nil)

	// Strict: unknown fields are rejected
	err = iface.Validate(openbindings.WithRejectUnknownTypedFields())
	fmt.Println("strict:", err != nil)
	// Output:
	// default: true
	// strict: true
}

func ExampleInterface_lossless() {
	data := []byte(`{
		"openbindings": "0.1.0",
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
		Kind:        openbindings.OperationKindMethod,
		Description: "Create a new user",
		Input: openbindings.JSONSchema{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
	}

	fmt.Println(op.Kind)
	fmt.Println(op.Description)
	fmt.Println(op.Input["type"])
	// Output:
	// method
	// Create a new user
	// object
}

func ExampleSource() {
	bs := openbindings.Source{
		Format:   "openapi@3.1",
		Location: "./openapi.yaml",
	}

	fmt.Println(bs.Format)
	fmt.Println(bs.Location)
	// Output:
	// openapi@3.1
	// ./openapi.yaml
}

func Example_canonicaljson() {
	data := map[string]any{
		"z": 1,
		"a": 2,
		"m": 3,
	}

	out, _ := canonicaljson.Marshal(data)
	fmt.Println(string(out))
	// Output: {"a":2,"m":3,"z":1}
}

func Example_formattoken() {
	token, _ := formattoken.Parse("OpenAPI@3.1.0")

	fmt.Println(token.Name)
	fmt.Println(token.Version)
	fmt.Println(formattoken.IsOpenBindings(token))
	// Output:
	// openapi
	// 3.1.0
	// false
}

func ExampleTransform() {
	// Define an interface with named transforms
	iface := openbindings.Interface{
		OpenBindings: "0.1.0",
		Operations: map[string]openbindings.Operation{
			"processPayment": {Kind: openbindings.OperationKindMethod},
		},
		Transforms: map[string]openbindings.Transform{
			"toStripeInput": {
				Type:       "jsonata",
				Expression: "{ charge_amount: amount * 100 }",
			},
		},
		Sources: map[string]openbindings.Source{
			"stripe": {Format: "openapi@3.1", Location: "./stripe.json"},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"processPayment.stripe": {
				Operation: "processPayment",
				Source:    "stripe",
				InputTransform: &openbindings.TransformOrRef{
					Ref: "#/transforms/toStripeInput",
				},
			},
		},
	}

	// Resolve the transform reference
	binding := iface.Bindings["processPayment.stripe"]
	transform := binding.InputTransform.Resolve(iface.Transforms)

	fmt.Println("Type:", transform.Type)
	fmt.Println("Expression:", transform.Expression)
	// Output:
	// Type: jsonata
	// Expression: { charge_amount: amount * 100 }
}

func ExampleTransformOrRef_inline() {
	// Create an inline transform (no reference)
	tor := openbindings.TransformOrRef{
		Transform: &openbindings.Transform{
			Type:       "jsonata",
			Expression: "{ total: price * quantity }",
		},
	}

	fmt.Println("IsRef:", tor.IsRef())
	fmt.Println("Expression:", tor.Transform.Expression)
	// Output:
	// IsRef: false
	// Expression: { total: price * quantity }
}
