// Package workersrpc registers the `workers-rpc@^1.0.0` binding format
// with the OpenBindings Go SDK.
//
// Workers RPC bindings dispatch operation calls from one Cloudflare
// Worker to a sibling Worker that exposes a `WorkerEntrypoint` class
// via a service binding declared in `wrangler.toml`. The "transport"
// is `env[bindingName][methodName](args)` with structured-clone
// serialization handled by the Cloudflare runtime — there is no HTTP,
// no JSON, no URL involved at runtime.
//
// **The Go side cannot dispatch Workers RPC calls.** Cloudflare's
// Workers runtime is a JavaScript/WASM environment; Go programs (the
// `ob` CLI, server-side tooling, codegen) live outside the runtime
// and have no way to invoke a sibling Worker's RPC method. The Go
// implementations of `Executor` and `Creator` here exist solely to:
//
//  1. Make the format token recognized by `ob`. Without this package,
//     `ob create`, `ob sync`, and `ob diff` would reject any OBI that
//     declared a workers-rpc source as "unknown format".
//  2. Allow `ob codegen` to generate clients for workers-rpc bindings.
//     The TypeScript codegen produces a typed client class that takes
//     a `BindingExecutor[]` at construction; consumers wire in the
//     real `WorkersRpcExecutor` from `@openbindings/workers-rpc` at
//     runtime, inside the Worker. Codegen itself only needs to know
//     the format token + the operation/binding shapes; it does not
//     need to dispatch.
//  3. Allow hand-authored OBIs to validate. A workers-rpc OBI is
//     usually authored by hand (the contract IS the TypeScript class
//     on the target Worker, not a machine-readable spec file), so
//     there's no source artifact for the creator to derive from.
//
// Both `Executor.ExecuteBinding` and `Creator.CreateInterface` return
// errors with helpful messages directing the caller to the TypeScript
// runtime if they actually try to dispatch.
package workersrpc

import (
	"context"
	"fmt"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// FormatToken is the canonical format token for Cloudflare Workers RPC
// bindings. The semver range matches anything in the 1.x line.
const FormatToken = "workers-rpc@^1.0.0"

// DefaultSourceName is the conventional source key used when registering
// a workers-rpc source in an OBInterface. Consumers can override.
const DefaultSourceName = "workersRpc"

// Executor is the Go-side stub registration for the workers-rpc format.
// It satisfies the openbindings.BindingExecutor interface but rejects
// any actual dispatch attempt with a clear error — Workers RPC calls
// can only be made from inside the Workers runtime, where the
// TypeScript `WorkersRpcExecutor` from `@openbindings/workers-rpc`
// handles dispatch.
type Executor struct{}

// NewExecutor creates a new workers-rpc executor stub.
func NewExecutor() *Executor {
	return &Executor{}
}

// Formats returns the format tokens this executor recognizes.
func (e *Executor) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{
		Token:       FormatToken,
		Description: "Cloudflare Workers RPC bindings (Go-side stub; dispatch requires the Workers runtime)",
	}}
}

// ExecuteBinding always yields an error event: Go can't dispatch Workers RPC.
// Use the `WorkersRpcExecutor` from `@openbindings/workers-rpc` from
// inside a Cloudflare Worker instead.
func (e *Executor) ExecuteBinding(_ context.Context, _ *openbindings.BindingExecutionInput) (<-chan openbindings.StreamEvent, error) {
	return openbindings.SingleEventChannel(openbindings.FailedOutput(
		time.Now(),
		openbindings.ErrCodeSourceConfigError,
		"workers-rpc bindings cannot be dispatched from Go: "+
			"these bindings only work from inside a Cloudflare Worker. "+
			"Use the WorkersRpcExecutor from @openbindings/workers-rpc "+
			"in your Worker entrypoint to make actual RPC calls",
	)), nil
}

// Creator is the Go-side stub for creating an interface from a
// workers-rpc source. Workers RPC sources are hand-authored — the
// contract is the TypeScript class on the target Worker, not a
// machine-readable spec — so there's nothing to "create" from. This
// stub returns a clear error directing users to write the OBI manually.
type Creator struct{}

// NewCreator creates a new workers-rpc creator stub.
func NewCreator() *Creator {
	return &Creator{}
}

// Formats returns the format tokens this creator recognizes.
func (c *Creator) Formats() []openbindings.FormatInfo {
	return []openbindings.FormatInfo{{
		Token:       FormatToken,
		Description: "Cloudflare Workers RPC bindings (hand-authored; no source synthesis)",
	}}
}

// CreateInterface returns an error explaining that workers-rpc OBIs are
// hand-authored. There's no source artifact to derive operations from
// because the contract is the WorkerEntrypoint class on the target
// Worker — its method signatures live in TypeScript, not in a spec file.
func (c *Creator) CreateInterface(_ context.Context, _ *openbindings.CreateInput) (*openbindings.Interface, error) {
	return nil, fmt.Errorf(
		"workers-rpc interfaces are hand-authored: declare operations " +
			"and bindings directly in your OBI's `operations` and `bindings` " +
			"sections, with each binding's `ref` set to the WorkerEntrypoint " +
			"method name. There is no source artifact to synthesize from " +
			"because the contract is the TypeScript class on the target Worker",
	)
}
