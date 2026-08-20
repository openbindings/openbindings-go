package invoke_test

import (
	"context"
	"fmt"
	"io"
	"log"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

// echoInvoker is a minimal BindingInvoker for the examples: it answers
// every binding with its first input, wrapped. Real invokers (openapi,
// asyncapi, graphql, grpc, mcp, usage) live in the formats/* submodules;
// this is the entire seam a format implements.
type echoInvoker struct{}

func (echoInvoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: "echo@1.0", Description: "example echo format"}}
}

func (echoInvoker) InvokeBinding(ctx context.Context, args *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	inv := invoke.NewInvocationImpl[any, any](ctx)
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

	opInv := invoke.NewOperationInvoker(echoInvoker{})
	call := invoke.Invoke(ctx, opInv, iface,
		invoke.NewOperationSignature[any, any]("echo"))
	_ = call.Write(ctx, "hello") // write errors are truthful; the verdict arrives below
	out, err := invoke.Single(ctx, call.Outputs())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(out)
	// Output: map[echoed:hello]
}
