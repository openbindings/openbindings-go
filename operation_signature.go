package openbindings

import (
	"context"
	"fmt"
	"strings"
)

// OperationSignature is the typed identity of an operation: its key, plus its
// input/output types as phantom parameters. It is inert and interface-free.
// Codegen emits one per operation under the generated OperationSignatures
// namespace, instantiated with concrete I/O types; dynamic callers instantiate
// it as [any, any]. The type is named "Signature" (not "Key") so sig.Key() reads
// without stutter.
//
// I and O carry the operation's input/output contract at compile time and have
// no runtime footprint, so the typed and untyped flavors share an identical
// runtime value — just the key. A signature is the thing codegen produces; the
// document-level operation definition (Operation, in Interface.Operations) is
// runtime data and is never generated.
type OperationSignature[I, O any] struct {
	// key is the operation key this signature names. It is unexported so a
	// package-level signature (codegen emits these as shared globals) cannot be
	// mutated in place; read it via Key(). The key is resolved against a target
	// interface at invoke time (alias-aware, OBI-T-12), which keeps the signature
	// provider-agnostic: the same signature can be invoked against any interface
	// that declares the key.
	key string
}

// Key returns the operation key this signature names. A signature is immutable;
// there is no setter — build one with NewOperationSignature.
func (s OperationSignature[I, O]) Key() string { return s.key }

// NewOperationSignature builds an OperationSignature for an operation key. It is
// the one general constructor: codegen calls it with concrete types
// (NewOperationSignature[ReindexInput, ReindexOutput]("reindex")), and dynamic
// callers use the [any, any] instantiation
// (NewOperationSignature[any, any]("reindex")).
func NewOperationSignature[I, O any](key string) OperationSignature[I, O] {
	return OperationSignature[I, O]{key: key}
}

// InvokeOption configures a single Invoke call. Options are rarely needed:
// invocation context is normally resolved by the invoker's ContextResolver via
// the reactive CONTEXT_REQUIRED path, and the binding is normally selected by
// OBI-T-09. The variadic-functional-option shape matches the rest of the SDK
// (FetchOption, ValidateOption), so the common call passes no options at all.
type InvokeOption func(*invokeConfig)

// invokeConfig is the resolved set of per-call options applied to one Invoke.
type invokeConfig struct {
	context    map[string]any
	bindingKey string
}

// WithContext supplies a per-call OB invocation-context override (auth/credentials
// as opaque well-known fields). Usually unnecessary; prefer the invoker's
// ContextResolver. The argument is the OB invocation context (a value map), not
// Go's context.Context.
func WithContext(values map[string]any) InvokeOption {
	return func(c *invokeConfig) { c.context = values }
}

// WithBindingKey bypasses binding selection (OBI-T-09) and uses this binding
// directly. The binding must belong to the resolved operation.
func WithBindingKey(key string) InvokeOption {
	return func(c *invokeConfig) { c.bindingKey = key }
}

// Invoke runs sig against obi using invoker, returning a typed invocation handle.
// Input flows through the handle's Write and output through
// Single(handle.Outputs()); the verb is not a unary in/out shortcut, so one
// shape serves unary, server-stream, client-stream, and bidi.
//
// The interface (obi) is a runtime argument, never part of the signature, so a
// provider-agnostic signature can be pointed at any obi; the result is typed by
// the signature's I/O (an [any, any] signature yields *TypedInvocation[any, any]).
//
// The handle is returned synchronously and creation is inert: no I/O happens
// until the invocation is driven. ctx is the invocation's lifetime (the Go
// analog of an abort signal); per-message deadlines belong on the ctx passed to
// Write/Read. Wiring failures (nil obi, unknown operation, unknown binding or
// source) surface as an already-errored handle, never a panic.
//
// Invoke is a free function rather than a method on OperationInvoker because Go
// methods cannot declare their own type parameters. When generic methods land it
// becomes invoker.Invoke(ctx, obi, sig, opts) with no other change.
func Invoke[I, O any](
	ctx context.Context,
	invoker *OperationInvoker,
	obi *Interface,
	sig OperationSignature[I, O],
	opts ...InvokeOption,
) *TypedInvocation[I, O] {
	var cfg invokeConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	// Wiring failures resolve to an already-errored typed handle so the caller
	// drives one uniform shape whether setup or the binding failed. OBI-T-12
	// (operation resolution) and OBI-T-09 (binding selection) happen here; the
	// handle is inert until driven, and no I/O runs until then.
	fail := func(e *InvocationError) *TypedInvocation[I, O] {
		return NewTypedInvocation[I, O](NewErroredInvocation[any, any](e))
	}

	if obi == nil {
		return fail(&InvocationError{
			Code: ErrCodeValidationFailed, Message: ErrNilInterface.Error(),
		})
	}

	// OBI-T-12: resolve against the flat key+aliases namespace. Bindings are
	// selected by the resolved canonical key, not the name the caller used.
	opKey, opVal, ok := ResolveOperation(obi, sig.key)
	op := &opVal
	if !ok {
		return fail(&InvocationError{
			Code: ErrCodeOperationNotFound,
			Message: fmt.Sprintf("%v: %s; searched operation identifiers (keys and aliases): [%s]",
				ErrOperationNotFound, sig.key, strings.Join(AllOperationIdentifiers(obi), ", ")),
		})
	}

	var bindingKey string
	var binding *BindingEntry
	if cfg.bindingKey != "" {
		b, ok := obi.Bindings[cfg.bindingKey]
		if !ok {
			return fail(&InvocationError{
				Code:    ErrCodeBindingNotFound,
				Message: fmt.Sprintf("binding %q is not defined on this interface", cfg.bindingKey),
			})
		}
		// The explicit binding must belong to the resolved operation. Without
		// this guard a mismatched op/binding pair would validate input/output
		// against the resolved operation's contract while invoking a different
		// operation's binding.
		if b.Operation != opKey {
			return fail(&InvocationError{
				Code: ErrCodeBindingNotFound,
				Message: fmt.Sprintf("binding %q targets operation %q, not the resolved operation %q",
					cfg.bindingKey, b.Operation, opKey),
			})
		}
		bindingKey = cfg.bindingKey
		binding = &b
	} else {
		selector := invoker.BindingSelector
		if selector == nil {
			selector = func(iface *Interface, opKey string) (string, *BindingEntry, error) {
				return selectBinding(iface, opKey, invoker.availableFormats())
			}
		}
		var err error
		bindingKey, binding, err = selector(obi, opKey)
		if err != nil {
			return fail(&InvocationError{
				Code: ErrCodeBindingNotFound, Message: err.Error(),
			})
		}
	}

	source, ok := obi.Sources[binding.Source]
	if !ok {
		return fail(&InvocationError{
			Code:    ErrCodeUnknownSource,
			Message: fmt.Sprintf("%v: binding %q references %q", ErrUnknownSource, bindingKey, binding.Source),
		})
	}

	caller := NewInvocationImpl[any, any](ctx)
	caller.validateInput = makeInputValidator(op, obi, sig.key)

	go func() {
		// A panicking third-party invoker must not kill the process: it
		// terminates this invocation loudly instead.
		defer func() {
			if r := recover(); r != nil {
				caller.FireError(&InvocationError{
					Code:    ErrCodeRuntime,
					Message: fmt.Sprintf("openbindings: invoker panic: %v", r),
				})
			}
		}()
		invoker.run(ctx, caller, obi, op, binding, bindingKey, &source, cfg.context)
	}()

	return NewTypedInvocation[I, O](caller)
}
