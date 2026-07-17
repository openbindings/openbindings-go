package openbindings_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"

	"github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/canonicaljson"
)

// echoInvoker is a minimal BindingInvoker for the examples: it answers
// every binding with its first input, wrapped. Real invokers (openapi,
// asyncapi, graphql, grpc, mcp, usage) live in the formats/* submodules;
// this is the entire seam a format implements.
type echoInvoker struct{}

func (echoInvoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: "echo@1.0", Description: "example echo format"}}
}

func (echoInvoker) InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any] {
	inv := openbindings.NewInvocationImpl[any, any](ctx)
	go func() {
		in, err := inv.ReadInput(ctx)
		if err != nil && err != io.EOF {
			return // terminal already fired
		}
		_ = inv.CloseInput()
		if err := inv.EmitOutput(map[string]any{"echoed": in}); err != nil {
			return
		}
		inv.CloseOutput()
	}()
	return inv
}

// ExampleInvoke shows the canonical unary flow: one cardinality-agnostic
// handle serves every operation; a unary call writes one input and reads
// its single output through the blessed assertion.
func ExampleInvoke() {
	ctx := context.Background()

	iface := &openbindings.Interface{
		OpenBindings: "0.2.0",
		Name:         "Echo",
		Operations:   map[string]openbindings.Operation{"echo": {}},
		Sources:      map[string]openbindings.Source{"echo": {BindingSpec: "echo@1.0", Location: "mem://echo"}},
		Bindings:     map[string]openbindings.BindingEntry{"echo.main": {Operation: "echo", Source: "echo", Ref: "echo"}},
	}

	opInv := openbindings.NewOperationInvoker(echoInvoker{})
	call := openbindings.Invoke(ctx, opInv, iface,
		openbindings.NewOperationSignature[any, any]("echo"))
	_ = call.Write(ctx, "hello") // write errors are truthful; the verdict arrives below
	out, err := openbindings.Single(ctx, call.Outputs())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out)
	// Output: map[echoed:hello]
}

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

	fmt.Println(iface.Name)
	fmt.Println(iface.Operations["getUser"].Description)
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

	if err := iface.Validate(); err != nil {
		fmt.Println("invalid:", err)
	} else {
		fmt.Println("valid")
	}
	// Output: valid
}

func ExampleInterface_Validate_strict() {
	data := []byte(`{
		"openbindings": "0.2.0",
		"unknownField": "should fail in strict mode",
		"operations": {
			"getUser": {"description": "Get a user"}
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
		Description: "Create a new user",
		Input: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
			},
		},
	}

	fmt.Println(op.Description)
	fmt.Println(op.Input.(map[string]any)["type"])
	// Output:
	// Create a new user
	// object
}

func ExampleSource() {
	bs := openbindings.Source{
		BindingSpec: "openapi@3.1",
		Location:    "https://api.example.com/openapi.yaml",
	}

	fmt.Println(bs.BindingSpec)
	fmt.Println(bs.Location)
	// Output:
	// openapi@3.1
	// https://api.example.com/openapi.yaml
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
			"stripe": {BindingSpec: "openapi@3.1", Location: "https://api.example.com/stripe.json"},
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
	tor := openbindings.TransformOrRef{Inline: "{ total: price * quantity }"}

	fmt.Println("IsRef:", tor.IsRef())
	fmt.Println("Expression:", tor.Inline)
	// Output:
	// IsRef: false
	// Expression: { total: price * quantity }
}
