package invoke

// Operation-layer tests mirroring the TS SDK's operation-invoker.test.ts:
// wiring, cardinalities, OBI-T-07/T-08, CONTEXT_REQUIRED negotiation
// (resolve-replay-retry, preflight), and metadata pass-through.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// ---------------------------------------------------------------------------
// Mock binding invoker (the design doc's reference mock, selector-dispatched)
// ---------------------------------------------------------------------------

var testDurable = true

var bearerDetails = &ContextRequiredDetails{
	Target:       "api.example.com",
	Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{{Type: "auth.bearer", Durable: &testDurable}}}},
}

type mockOpts struct {
	token               string
	nativeFailure       bool // ping returns one binding-native failure completion
	requireBearer       bool // getUser challenges when context lacks bearerToken (after reading input)
	requireServerConfig bool // getUser challenges with config.value until context.configuration.server is present
	challengeAlways     bool // getUser challenges unconditionally (tests the retry cap)
	preflight           bool // expose PrepareBinding reporting the bearer requirement
}

type mockBindingInvoker struct {
	opts mockOpts

	mu       sync.Mutex
	attempts int
	prepares int
	reads    [][]any // inputs read per attempt
	contexts []map[string]any
	lastSite *InvokeSite
}

// lastBindingKey reports the binding key of the most recent invocation's
// site — the selection outcome the operation layer stamps on the
// binding-layer args.
func (m *mockBindingInvoker) lastBindingKey() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastSite == nil {
		return ""
	}
	return m.lastSite.BindingKey
}

func (m *mockBindingInvoker) BindingSpecs() []openbindings.BindingSpecInfo {
	tok := m.opts.token
	if tok == "" {
		tok = "mock@1.0"
	}
	return []openbindings.BindingSpecInfo{{BindingSpec: tok}}
}

func (m *mockBindingInvoker) snapshot() (attempts, prepares int, reads [][]any, contexts []map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attempts, m.prepares, m.reads, m.contexts
}

func (m *mockBindingInvoker) InvokeBinding(ctx context.Context, args *BindingInvocationArgs) Invocation[any, any] {
	inv := NewInvocationImpl[any, any](ctx)
	m.mu.Lock()
	m.attempts++
	m.contexts = append(m.contexts, args.Context)
	m.lastSite = args.Site
	idx := len(m.reads)
	m.reads = append(m.reads, nil)
	m.mu.Unlock()
	record := func(v any) {
		m.mu.Lock()
		m.reads[idx] = append(m.reads[idx], v)
		m.mu.Unlock()
	}
	go func() {
		if err := m.run(ctx, args, inv, record); err != nil {
			inv.FireError(AsInvocationError(err))
		}
	}()
	return inv
}

func (m *mockBindingInvoker) run(ctx context.Context, args *BindingInvocationArgs, h *InvocationImpl[any, any], record func(any)) error {
	readFirst := func() (any, bool, error) {
		v, err := h.ReadInput(ctx)
		if err == io.EOF {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		return v, true, nil
	}

	switch args.Selector {
	case "ping":
		_ = h.CloseInput()
		if m.opts.nativeFailure {
			h.FireError(&InvocationError{
				Code: ErrCodeExecutionFailed,
			})
			return nil
		}
		if err := h.EmitOutput(map[string]any{"ok": true}); err != nil {
			return nil
		}
		h.CloseOutput()
	case "getUser":
		// Unary: read first input, then (deliberately AFTER the read — the
		// "read ≠ consumed" case) check context.
		first, ok, err := readFirst()
		if err != nil {
			return nil
		}
		if !ok {
			h.FireError(&InvocationError{Code: ErrCodeMissingInput})
			return nil
		}
		record(first)
		if m.opts.challengeAlways || (m.opts.requireBearer && ContextBearerToken(args.Context) == "") {
			h.FireError(NewContextRequiredError(bearerDetails))
			return nil
		}
		if m.opts.requireServerConfig && !hasServerConfig(args.Context) {
			h.FireError(NewContextRequiredError(
				&ContextRequiredDetails{
					Target: "api.example.com",
					Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{
						NewConfigValueRequirement("server", "/url", "supply a connection URL", nil, nil),
					}}},
				}))
			return nil
		}
		_ = h.CloseInput()
		id, _ := first.(map[string]any)["id"]
		if err := h.EmitOutput(map[string]any{"id": id, "name": "Ada"}); err != nil {
			return nil
		}
		h.CloseOutput()
	case "echoInput":
		first, _, err := readFirst()
		if err != nil {
			return nil
		}
		record(first)
		_ = h.CloseInput()
		if err := h.EmitOutput(first); err != nil {
			return nil
		}
		h.CloseOutput()
	case "watchOrders":
		first, _, err := readFirst()
		if err != nil {
			return nil
		}
		record(first)
		_ = h.CloseInput()
		for _, status := range []string{"created", "paid", "shipped"} {
			if err := h.EmitOutput(map[string]any{"id": "ord_1", "status": status}); err != nil {
				return nil
			}
		}
		h.CloseOutput()
	case "watchThenChallenge":
		// Mid-stream challenge: observable progress happened, so the
		// operation layer must surface, not retry.
		_ = h.CloseInput()
		if err := h.EmitOutput(map[string]any{"id": "ord_1", "status": "created"}); err != nil {
			return nil
		}
		h.FireError(NewContextRequiredError(bearerDetails))
	case "streamBadSecond":
		_ = h.CloseInput()
		if err := h.EmitOutput(map[string]any{"n": float64(1)}); err != nil {
			return nil
		}
		if err := h.EmitOutput(map[string]any{"bad": true}); err != nil {
			return nil
		}
		h.CloseOutput()
	case "badUser":
		first, _, err := readFirst()
		if err != nil {
			return nil
		}
		record(first)
		_ = h.CloseInput()
		if err := h.EmitOutput(map[string]any{"wrong": "shape"}); err != nil {
			return nil
		}
		h.CloseOutput()
	case "chat":
		for {
			msg, ok, err := readFirst()
			if err != nil {
				return nil
			}
			if !ok {
				break
			}
			record(msg)
			text, _ := msg.(map[string]any)["text"]
			if err := h.EmitOutput(map[string]any{"ack": text}); err != nil {
				return nil
			}
		}
		h.CloseOutput()
	case "uploadChunks":
		count := 0
		for {
			chunk, ok, err := readFirst()
			if err != nil {
				return nil
			}
			if !ok {
				break
			}
			record(chunk)
			count++
		}
		if err := h.EmitOutput(map[string]any{"count": float64(count)}); err != nil {
			return nil
		}
		h.CloseOutput()
	default:
		h.FireError(&InvocationError{Code: ErrCodeRuntime})
	}
	return nil
}

// PrepareBinding is attached via the preparerMock wrapper so that plain
// mocks do NOT implement BindingPreparer.
type preparerMock struct {
	*mockBindingInvoker
}

type malformedPreparerMock struct {
	*mockBindingInvoker
}

func (p *malformedPreparerMock) PrepareBinding(context.Context, *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	return &ContextRequiredDetails{
		Target:       "api.example.com",
		Alternatives: []ContextAlternative{{Requirements: nil}},
	}, nil
}

func (p *preparerMock) PrepareBinding(_ context.Context, args *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	p.mu.Lock()
	p.prepares++
	p.mu.Unlock()
	if ContextBearerToken(args.Context) != "" {
		return nil, nil
	}
	return bearerDetails, nil
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

type exprEvaluator struct{}

func (exprEvaluator) Evaluate(expr string, data any) (any, error) {
	switch expr {
	case "idToUserId":
		id, _ := data.(map[string]any)["id"]
		return map[string]any{"userId": id}, nil
	case "breakShape":
		return map[string]any{"broken": true}, nil
	case "boom":
		return nil, errors.New("transform exploded")
	default:
		return nil, fmt.Errorf("unknown expression: %s", expr)
	}
}

func pf(v float64) *float64 { return &v }

func opTestInterface() *openbindings.Interface {
	return &openbindings.Interface{
		OpenBindings: "0.2.0",
		Schemas: map[string]openbindings.JSONSchema{
			"UserInput": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"id": map[string]any{"type": "string"}},
				"required":             []any{"id"},
				"additionalProperties": false,
			},
		},
		Operations: map[string]openbindings.Operation{
			"ping": {},
			"getUser": {
				Aliases: []string{"fetchUser"},
				Input:   map[string]any{"$ref": "#/schemas/UserInput"},
				Output: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":   map[string]any{"type": "string"},
						"name": map[string]any{"type": "string"},
					},
					"required": []any{"id", "name"},
				},
			},
			"echo": {},
			"watchOrders": {
				Output: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":     map[string]any{"type": "string"},
						"status": map[string]any{"type": "string"},
					},
					"required": []any{"id", "status"},
				},
			},
			"watchTyped": {
				Output: map[string]any{
					"type":       "object",
					"properties": map[string]any{"n": map[string]any{"type": "number"}},
					"required":   []any{"n"},
				},
			},
			"chat":         {},
			"uploadChunks": {},
		},
		Sources: map[string]openbindings.Source{
			"mock": {BindingSpec: "mock@1.0", Location: "mem://mock"},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"ping.main":    {Operation: "ping", Source: "mock", Selector: "ping"},
			"getUser.main": {Operation: "getUser", Source: "mock", Selector: "getUser", Preference: pf(99)},
			"getUser.bad":  {Operation: "getUser", Source: "mock", Selector: "badUser", Preference: pf(1)},
			"echo.transformed": {
				Operation: "echo", Source: "mock", Selector: "echoInput",
				InputTransform: &openbindings.TransformOrRef{Inline: "idToUserId"},
			},
			"watchOrders.main": {Operation: "watchOrders", Source: "mock", Selector: "watchOrders", Preference: pf(99)},
			"watchOrders.challenge": {
				Operation: "watchOrders", Source: "mock", Selector: "watchThenChallenge", Preference: pf(1),
			},
			"watchTyped.main":   {Operation: "watchTyped", Source: "mock", Selector: "streamBadSecond"},
			"chat.main":         {Operation: "chat", Source: "mock", Selector: "chat"},
			"uploadChunks.main": {Operation: "uploadChunks", Source: "mock", Selector: "uploadChunks"},
		},
	}
}

func newOpInvoker(mock BindingInvoker, resolver ContextResolver) *OperationInvoker {
	op := NewOperationInvoker(mock)
	op.TransformEvaluator = exprEvaluator{}
	op.ContextResolver = resolver
	// Most operation-layer tests exercise validation, transforms, streaming,
	// or context rather than binding resolution. Give that test application an
	// explicit policy for its deliberately multi-bound fixtures; dedicated
	// tests below exercise the contract's policy-neutral default.
	op.BindingSelector = func(iface *openbindings.Interface, opKey string) (string, *openbindings.BindingEntry, error) {
		key := opKey + ".main"
		if b, ok := iface.Bindings[key]; ok && b.Operation == opKey {
			entry := b
			return key, &entry, nil
		}
		return DefaultBindingSelector(iface, opKey)
	}
	return op
}

func drainOutputs(t *testing.T, call Invocation[any, any]) ([]any, error) {
	t.Helper()
	return collectStream(t, call.Outputs())
}

// ---------------------------------------------------------------------------
// Wiring & resolution
// ---------------------------------------------------------------------------

func TestOpInvokeNoInputViaSingle(t *testing.T) { // NI
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("ping"))
	v, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := v.(map[string]any)["ok"].(bool); !ok {
		t.Fatalf("got %v", v)
	}
}

func TestOpAliasResolution(t *testing.T) { // OBI-T-12
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("fetchUser"))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
		t.Fatal(err)
	}
	v, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["name"] != "Ada" {
		t.Fatalf("got %v", v)
	}
}

func TestOpWiringErrorsAreErroredHandles(t *testing.T) {
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	cases := []struct {
		name       string
		iface      *openbindings.Interface
		operation  string
		bindingKey string
		code       string
	}{
		{"nil interface", nil, "ping", "", ErrCodeOperationValidationFailed},
		{"unknown operation", opTestInterface(), "nope", "", ErrCodeOperationNotFound},
		{"unknown bindingKey", opTestInterface(), "ping", "nope", ErrCodeBindingNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := Invoke(bg(), op, tc.iface, NewOperationSignature[any, any](tc.operation), WithBindingKey(tc.bindingKey))
			_, err := drainOutputs(t, call)
			if codeOf(t, err) != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
		})
	}

	t.Run("unknown source", func(t *testing.T) {
		iface := opTestInterface()
		delete(iface.Sources, "mock")
		call := Invoke(bg(), op, iface, NewOperationSignature[any, any]("ping"), WithBindingKey("ping.main"))
		_, err := drainOutputs(t, call)
		if codeOf(t, err) != ErrCodeUnknownSource {
			t.Fatalf("expected ERR_UNKNOWN_SOURCE, got %v", err)
		}
	})

	t.Run("no invoker for format", func(t *testing.T) {
		iface := opTestInterface()
		src := iface.Sources["mock"]
		src.BindingSpec = "absent@1.0"
		iface.Sources["mock"] = src
		call := Invoke(bg(), op, iface, NewOperationSignature[any, any]("ping"), WithBindingKey("ping.main"))
		_, err := drainOutputs(t, call)
		if codeOf(t, err) != ErrCodeBindingNotFound {
			t.Fatalf("expected ERR_BINDING_NOT_FOUND, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Cardinalities through one shape
// ---------------------------------------------------------------------------

func TestOpServerStreaming(t *testing.T) { // SS
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("watchOrders"))
	if err := call.Write(bg(), map[string]any{"accountId": "a1"}); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if err != nil || len(vals) != 3 {
		t.Fatalf("expected 3 outputs, got %d err=%v", len(vals), err)
	}
}

func TestOpClientStreaming(t *testing.T) { // CS
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("uploadChunks"))
	for _, c := range []string{"a", "b", "c"} {
		if err := call.Write(bg(), map[string]any{"chunk": c}); err != nil {
			t.Fatal(err)
		}
	}
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	v, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["count"] != float64(3) {
		t.Fatalf("got %v", v)
	}
}

func TestOpBidi(t *testing.T) { // BD
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("chat"))
	go func() {
		for _, text := range []string{"hi", "there"} {
			if err := call.Write(bg(), map[string]any{"text": text}); err != nil {
				return
			}
		}
		_ = call.Close()
	}()
	vals, err := drainOutputs(t, call)
	if err != nil || len(vals) != 2 {
		t.Fatalf("expected 2 acks, got %d err=%v", len(vals), err)
	}
	if vals[0].(map[string]any)["ack"] != "hi" || vals[1].(map[string]any)["ack"] != "there" {
		t.Fatalf("got %v", vals)
	}
}

func TestOpCancelPropagatesToBinding(t *testing.T) {
	mock := &mockBindingInvoker{}
	op := newOpInvoker(mock, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("chat"))
	if err := call.Write(bg(), map[string]any{"text": "hello"}); err != nil {
		t.Fatal(err)
	}
	// Consume the first ack so the pipeline is live.
	out := call.Outputs()
	if _, err := out.Read(shortCtx(t)); err != nil {
		t.Fatal(err)
	}
	call.Cancel()
	_, err := out.Read(shortCtx(t))
	if codeOf(t, err) != ErrCodeCancelled {
		t.Fatalf("expected ERR_CANCELLED, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// OBI-T-07 — input validation (terminal + write rejects, before transform)
// ---------------------------------------------------------------------------

func TestOpT07InvalidWriteRejectsAndTerminates(t *testing.T) {
	mock := &mockBindingInvoker{}
	op := newOpInvoker(mock, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"))
	err := call.Write(bg(), map[string]any{"id": 42})
	if codeOf(t, err) != ErrCodeOperationValidationFailed {
		t.Fatalf("expected write rejection, got %v", err)
	}
	_, derr := drainOutputs(t, call)
	if codeOf(t, derr) != ErrCodeOperationValidationFailed {
		t.Fatalf("expected terminal, got %v", derr)
	}
	if _, _, reads, _ := mock.snapshot(); len(reads) > 0 && len(reads[0]) > 0 {
		t.Fatalf("binding must never see the invalid message: %v", reads)
	}
}

func TestOpT07ValidatesBeforeTransform(t *testing.T) {
	iface := opTestInterface()
	echo := iface.Operations["echo"]
	echo.Input = map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
		"required":   []any{"id"},
	}
	iface.Operations["echo"] = echo
	mock := &mockBindingInvoker{}
	op := newOpInvoker(mock, nil)
	call := Invoke(bg(), op, iface, NewOperationSignature[any, any]("echo"))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil { // validates PRE-transform shape
		t.Fatal(err)
	}
	v, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["userId"] != "u1" {
		t.Fatalf("binding must receive the transformed message, got %v", v)
	}
}

func TestOpInputTransformFailureIsTerminal(t *testing.T) {
	iface := opTestInterface()
	b := iface.Bindings["echo.transformed"]
	b.InputTransform = &openbindings.TransformOrRef{Inline: "boom"}
	iface.Bindings["echo.transformed"] = b
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, iface, NewOperationSignature[any, any]("echo"))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != ErrCodeTransformError {
		t.Fatalf("expected ERR_TRANSFORM_ERROR, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// OBI-T-08 — output validation (terminal, value not emitted, per item)
// ---------------------------------------------------------------------------

func TestOpT08InvalidOutputNotEmitted(t *testing.T) {
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"), WithBindingKey("getUser.bad"))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
		t.Fatal(err)
	}
	vals, err := drainOutputs(t, call)
	if len(vals) != 0 {
		t.Fatalf("invalid output must not be emitted, got %v", vals)
	}
	if codeOf(t, err) != ErrCodeOperationValidationFailed {
		t.Fatalf("expected ERR_OPERATION_VALIDATION_FAILED, got %v", err)
	}
}

func TestOpT08PerItemForStreaming(t *testing.T) { // SS
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("watchTyped"))
	vals, err := drainOutputs(t, call)
	if len(vals) != 1 || vals[0].(map[string]any)["n"] != float64(1) {
		t.Fatalf("valid prefix must be delivered, got %v", vals)
	}
	if codeOf(t, err) != ErrCodeOperationValidationFailed {
		t.Fatalf("expected ERR_OPERATION_VALIDATION_FAILED, got %v", err)
	}
}

func TestOpT08ValidatesAfterTransform(t *testing.T) {
	iface := opTestInterface()
	b := iface.Bindings["watchTyped.main"]
	b.OutputTransform = &openbindings.TransformOrRef{Inline: "breakShape"}
	iface.Bindings["watchTyped.main"] = b
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, iface, NewOperationSignature[any, any]("watchTyped"))
	vals, err := drainOutputs(t, call)
	if len(vals) != 0 {
		t.Fatalf("transform broke the shape; nothing should emit, got %v", vals)
	}
	if codeOf(t, err) != ErrCodeOperationValidationFailed {
		t.Fatalf("expected ERR_OPERATION_VALIDATION_FAILED, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// OBI-T-16 — claim semantics: unresolvable graph is distinct from mismatch;
// `format` is an annotation, never an assertion
// ---------------------------------------------------------------------------

// An unresolvable governing schema graph means the claim could not be
// EVALUATED: ERR_SCHEMA_UNRESOLVED, distinct from a value mismatch, never
// partial validation.
func TestOpT16ResolvesInputRefFromOBIDocumentRoot(t *testing.T) {
	iface := opTestInterface()
	op := iface.Operations["getUser"]
	op.Input = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"$ref": "#/operations/getUser/input/$defs/Identifier"},
		},
		"required":             []any{"id"},
		"additionalProperties": false,
		"$defs": map[string]any{
			"Identifier": map[string]any{"type": "string"},
		},
	}
	iface.Operations["getUser"] = op

	inv := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), inv, iface, NewOperationSignature[any, any]("getUser"))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
		t.Fatalf("document-root ref should resolve: %v", err)
	}
	v, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["name"] != "Ada" {
		t.Fatalf("got %v", v)
	}
}

func TestOpT16ResolvesStreamingOutputRefFromOBIDocumentRoot(t *testing.T) {
	iface := opTestInterface()
	op := iface.Operations["watchTyped"]
	op.Output = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"n": map[string]any{"$ref": "#/operations/watchTyped/output/$defs/Count"},
		},
		"required": []any{"n"},
		"$defs": map[string]any{
			"Count": map[string]any{"type": "number"},
		},
	}
	iface.Operations["watchTyped"] = op

	inv := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), inv, iface, NewOperationSignature[any, any]("watchTyped"))
	vals, err := drainOutputs(t, call)
	if len(vals) != 1 {
		t.Fatalf("expected valid prefix before mismatch, got %v", vals)
	}
	if codeOf(t, err) != ErrCodeOperationValidationFailed {
		t.Fatalf("expected ERR_OPERATION_VALIDATION_FAILED after valid prefix, got %v", err)
	}
}

func TestOpT16UnresolvableOutputGraphIsSchemaUnresolved(t *testing.T) {
	iface := opTestInterface()
	op := iface.Operations["watchTyped"]
	op.Output = map[string]any{"$ref": "#/schemas/DoesNotExist"}
	iface.Operations["watchTyped"] = op

	inv := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), inv, iface, NewOperationSignature[any, any]("watchTyped"))
	vals, err := drainOutputs(t, call)
	if len(vals) != 0 {
		t.Fatalf("nothing may emit under an unestablished graph, got %v", vals)
	}
	if codeOf(t, err) != ErrCodeSchemaUnresolved {
		t.Fatalf("expected ERR_SCHEMA_UNRESOLVED, got %v", err)
	}
}

func TestOpT16UnresolvableInputGraphIsSchemaUnresolved(t *testing.T) {
	iface := opTestInterface()
	op := iface.Operations["getUser"]
	op.Input = map[string]any{"$ref": "#/schemas/DoesNotExist"}
	iface.Operations["getUser"] = op

	inv := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), inv, iface, NewOperationSignature[any, any]("getUser"))
	err := call.Write(bg(), map[string]any{"id": "u1"})
	if codeOf(t, err) != ErrCodeSchemaUnresolved {
		t.Fatalf("expected ERR_SCHEMA_UNRESOLVED on write, got %v", err)
	}
}

// The `format` keyword is an annotation under the core's claim semantics:
// a value that fails the named format still validates.
func TestOpT16FormatIsAnnotationNotAssertion(t *testing.T) {
	iface := opTestInterface()
	op := iface.Operations["getUser"]
	op.Input = map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string", "format": "email"}},
		"required":   []any{"id"},
	}
	iface.Operations["getUser"] = op

	inv := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), inv, iface, NewOperationSignature[any, any]("getUser"))
	if err := call.Write(bg(), map[string]any{"id": "not-an-email"}); err != nil {
		t.Fatalf("format must annotate, never assert: %v", err)
	}
	v, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["name"] != "Ada" {
		t.Fatalf("got %v", v)
	}
}

// ---------------------------------------------------------------------------
// CONTEXT_REQUIRED negotiation
// ---------------------------------------------------------------------------

func TestOpContextRequiredSurfacesWithoutResolver(t *testing.T) {
	mock := &mockBindingInvoker{opts: mockOpts{requireBearer: true}}
	op := newOpInvoker(mock, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	var ie *InvocationError
	if !errors.As(err, &ie) || ContextRequiredFrom(ie) == nil {
		t.Fatalf("expected CONTEXT_REQUIRED with details, got %v", err)
	}
}

func TestOpResolveAndRetryReplaysInput(t *testing.T) { // U — read ≠ consumed
	mock := &mockBindingInvoker{opts: mockOpts{requireBearer: true}}
	var resolverCalls int
	var rmu sync.Mutex
	resolver := func(_ context.Context, details *ContextRequiredDetails) (map[string]any, error) {
		rmu.Lock()
		resolverCalls++
		rmu.Unlock()
		if details.Target != "api.example.com" {
			return nil, fmt.Errorf("wrong target: %s", details.Target)
		}
		return map[string]any{"bearerToken": "tok-123"}, nil
	}
	op := newOpInvoker(mock, resolver)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil { // written ONCE
		t.Fatal(err)
	}
	v, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if v.(map[string]any)["name"] != "Ada" {
		t.Fatalf("got %v", v)
	}

	attempts, _, reads, contexts := mock.snapshot()
	rmu.Lock()
	rc := resolverCalls
	rmu.Unlock()
	if rc != 1 {
		t.Fatalf("resolver calls: %d", rc)
	}
	if attempts != 2 {
		t.Fatalf("attempts: %d", attempts)
	}
	// Both attempts read the same lone input: the prefix was replayed.
	if len(reads[0]) != 1 || len(reads[1]) != 1 {
		t.Fatalf("replay failed: %v", reads)
	}
	if reads[1][0].(map[string]any)["id"] != "u1" {
		t.Fatalf("replayed wrong value: %v", reads[1])
	}
	if ContextBearerToken(contexts[1]) != "tok-123" {
		t.Fatalf("retry context missing token: %v", contexts[1])
	}
}

func hasServerConfig(ctx map[string]any) bool {
	cfg, _ := ctx["configuration"].(map[string]any)
	_, ok := cfg["server"]
	return ok
}

// TestOpResolveAndRetryConfigValue proves the R1a config.value path end to
// end: a binding challenges with a config.value requirement, a resolver
// supplies the value into context.configuration under its point, and the
// retry carries it — while a configuration point the caller already supplied
// (decode) survives the point-wise merge rather than being clobbered.
func TestOpResolveAndRetryConfigValue(t *testing.T) {
	mock := &mockBindingInvoker{opts: mockOpts{requireServerConfig: true}}
	var got map[string]any
	resolver := func(_ context.Context, details *ContextRequiredDetails) (map[string]any, error) {
		req := details.Alternatives[0].Requirements[0]
		if req.Type != "config.value" || req.Extra["point"] != "server" {
			return nil, fmt.Errorf("unexpected requirement %+v", req)
		}
		return map[string]any{"configuration": map[string]any{"server": map[string]any{"url": "https://api.example.com"}}}, nil
	}
	op := newOpInvoker(mock, resolver)
	// The caller pre-supplies a DIFFERENT configuration point (decode); it must
	// survive the resolve-and-retry merge.
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"),
		WithContext(map[string]any{"configuration": map[string]any{"decode": map[string]any{"lane": "text"}}}))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
		t.Fatal(err)
	}
	v, err := Single(shortCtx(t), call.Outputs())
	if err != nil {
		t.Fatalf("resolve-and-retry did not dispatch: %v", err)
	}
	if v.(map[string]any)["name"] != "Ada" {
		t.Fatalf("got %v", v)
	}
	_, _, _, contexts := mock.snapshot()
	got = contexts[1]
	cfg, _ := got["configuration"].(map[string]any)
	if _, ok := cfg["server"]; !ok {
		t.Errorf("retry context missing resolved server config: %v", cfg)
	}
	if dec, _ := cfg["decode"].(map[string]any); dec["lane"] != "text" {
		t.Errorf("caller's decode config point was clobbered by the merge: %v", cfg)
	}
}

func TestOpResolverDeclineSurfaces(t *testing.T) {
	mock := &mockBindingInvoker{opts: mockOpts{requireBearer: true}}
	op := newOpInvoker(mock, func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
		return nil, nil
	})
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != ErrCodeContextRequired {
		t.Fatalf("expected CONTEXT_REQUIRED, got %v", err)
	}
	if attempts, _, _, _ := mock.snapshot(); attempts != 1 {
		t.Fatalf("attempts: %d", attempts)
	}
}

func TestOpResolverFailureIsRuntimeFailure(t *testing.T) {
	t.Run("reactive challenge", func(t *testing.T) {
		mock := &mockBindingInvoker{opts: mockOpts{requireBearer: true}}
		op := newOpInvoker(mock, func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
			return nil, errors.New("credential broker unavailable")
		})
		call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"))
		if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
			t.Fatal(err)
		}
		_, err := drainOutputs(t, call)
		if codeOf(t, err) != ErrCodeRuntime {
			t.Fatalf("expected ERR_RUNTIME, got %v", err)
		}
		if attempts, _, _, _ := mock.snapshot(); attempts != 1 {
			t.Fatalf("resolver failure must not retry: %d attempts", attempts)
		}
	})

	t.Run("preflight challenge", func(t *testing.T) {
		mock := &preparerMock{&mockBindingInvoker{opts: mockOpts{requireBearer: true, preflight: true}}}
		op := newOpInvoker(mock, func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
			return nil, errors.New("credential broker unavailable")
		})
		call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"))
		_, err := drainOutputs(t, call)
		if codeOf(t, err) != ErrCodeRuntime {
			t.Fatalf("expected ERR_RUNTIME, got %v", err)
		}
		if attempts, _, _, _ := mock.snapshot(); attempts != 0 {
			t.Fatalf("preflight resolver failure must prevent dispatch: %d attempts", attempts)
		}
	})
}

func TestMalformedPreflightDoesNotReachResolver(t *testing.T) {
	mock := &malformedPreparerMock{&mockBindingInvoker{}}
	resolverCalls := 0
	op := newOpInvoker(mock, func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
		resolverCalls++
		return map[string]any{"bearerToken": "must-not-run"}, nil
	})
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"))
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != ErrCodeRuntime {
		t.Fatalf("expected ERR_RUNTIME, got %v", err)
	}
	if resolverCalls != 0 {
		t.Fatalf("malformed challenge reached resolver %d time(s)", resolverCalls)
	}
	if attempts, _, _, _ := mock.snapshot(); attempts != 0 {
		t.Fatalf("malformed preflight must prevent dispatch: %d attempts", attempts)
	}
}

func TestOpUnchangedResolutionDoesNotRetry(t *testing.T) {
	mock := &mockBindingInvoker{opts: mockOpts{challengeAlways: true}}
	var resolverCalls int
	op := newOpInvoker(mock, func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
		resolverCalls++
		return map[string]any{"bearerToken": "unchanged"}, nil
	})
	call := Invoke(
		bg(),
		op,
		opTestInterface(),
		NewOperationSignature[any, any]("getUser"),
		WithContext(map[string]any{"bearerToken": "unchanged"}),
	)
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != ErrCodeContextRequired {
		t.Fatalf("expected unchanged challenge, got %v", err)
	}
	if attempts, _, _, _ := mock.snapshot(); attempts != 1 {
		t.Fatalf("unchanged resolution must not retry: %d attempts", attempts)
	}
	if resolverCalls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolverCalls)
	}
}

func TestOpRetryRoundsAreCapped(t *testing.T) {
	mock := &mockBindingInvoker{opts: mockOpts{challengeAlways: true}}
	op := newOpInvoker(mock, func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
		return map[string]any{"bearerToken": "never-enough"}, nil
	})
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
		t.Fatal(err)
	}
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != ErrCodeContextRequired {
		t.Fatalf("expected CONTEXT_REQUIRED after cap, got %v", err)
	}
	if attempts, _, _, _ := mock.snapshot(); attempts > maxContextRounds+2 {
		t.Fatalf("retry loop not capped: %d attempts", attempts)
	}
}

func TestOpMidStreamChallengeSurfaces(t *testing.T) { // SS — no retry after progress
	mock := &mockBindingInvoker{}
	var resolverCalled bool
	op := newOpInvoker(mock, func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
		resolverCalled = true
		return map[string]any{"bearerToken": "tok"}, nil
	})
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("watchOrders"), WithBindingKey("watchOrders.challenge"))
	vals, err := drainOutputs(t, call)
	if len(vals) != 1 {
		t.Fatalf("the delivered prefix must survive, got %v", vals)
	}
	if codeOf(t, err) != ErrCodeContextRequired {
		t.Fatalf("expected CONTEXT_REQUIRED, got %v", err)
	}
	if resolverCalled {
		t.Fatal("no retry after observable progress")
	}
	if attempts, _, _, _ := mock.snapshot(); attempts != 1 {
		t.Fatalf("attempts: %d", attempts)
	}
}

func TestOpPreflightCollapsesChallenge(t *testing.T) { // CS-friendly path
	mock := &preparerMock{&mockBindingInvoker{opts: mockOpts{requireBearer: true, preflight: true}}}
	var resolverCalls int
	op := newOpInvoker(mock, func(context.Context, *ContextRequiredDetails) (map[string]any, error) {
		resolverCalls++
		return map[string]any{"bearerToken": "tok-pre"}, nil
	})
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"))
	if err := call.Write(bg(), map[string]any{"id": "u1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Single(shortCtx(t), call.Outputs()); err != nil {
		t.Fatal(err)
	}
	attempts, prepares, _, contexts := mock.snapshot()
	if prepares != 1 || resolverCalls != 1 {
		t.Fatalf("prepares=%d resolverCalls=%d", prepares, resolverCalls)
	}
	if attempts != 1 {
		t.Fatalf("preflight must collapse the challenge round-trip: %d attempts", attempts)
	}
	if ContextBearerToken(contexts[0]) != "tok-pre" {
		t.Fatalf("first attempt missing preflight-resolved context: %v", contexts[0])
	}
}

func TestOpPreflightWithoutResolverSurfaces(t *testing.T) {
	mock := &preparerMock{&mockBindingInvoker{opts: mockOpts{requireBearer: true, preflight: true}}}
	op := newOpInvoker(mock, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("getUser"))
	_, err := drainOutputs(t, call)
	if codeOf(t, err) != ErrCodeContextRequired {
		t.Fatalf("expected CONTEXT_REQUIRED, got %v", err)
	}
	if attempts, _, _, _ := mock.snapshot(); attempts != 0 {
		t.Fatalf("no attempt should run after a failed preflight: %d", attempts)
	}
}

func TestOperationInvokerDoesNotExposeBindingDiagnostics(t *testing.T) {
	op := newOpInvoker(&mockBindingInvoker{opts: mockOpts{nativeFailure: true}}, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("ping"))
	_, err := drainOutputs(t, call)
	var ie *InvocationError
	if !errors.As(err, &ie) {
		t.Fatalf("expected InvocationError, got %v", err)
	}
	if ie.Code != ErrCodeExecutionFailed {
		t.Fatalf("completion code changed: %+v", ie)
	}
	if ie.HasData() {
		t.Fatalf("binding-native evidence leaked as abstract data: %+v", ie.Data)
	}
}

func TestPrepareOperation(t *testing.T) {
	// Reports the resolved binding's requirements without invoking (no attempt).
	t.Run("reports requirements without invoking", func(t *testing.T) {
		mock := &preparerMock{&mockBindingInvoker{opts: mockOpts{requireBearer: true, preflight: true}}}
		op := newOpInvoker(mock, nil)
		details, err := op.PrepareOperation(bg(), opTestInterface(), "getUser")
		if err != nil {
			t.Fatalf("PrepareOperation: %v", err)
		}
		if details == nil || details.Target != "api.example.com" {
			t.Fatalf("expected bearer requirement, got %+v", details)
		}
		if attempts, prepares, _, _ := mock.snapshot(); attempts != 0 || prepares != 1 {
			t.Fatalf("expected 1 preflight and 0 invocations, got prepares=%d attempts=%d", prepares, attempts)
		}
	})

	// Supplied context narrows the result to what remains unsatisfied.
	t.Run("WithContext narrows to satisfied", func(t *testing.T) {
		mock := &preparerMock{&mockBindingInvoker{opts: mockOpts{requireBearer: true, preflight: true}}}
		op := newOpInvoker(mock, nil)
		details, err := op.PrepareOperation(bg(), opTestInterface(), "getUser",
			WithContext(map[string]any{"bearerToken": "tok"}))
		if err != nil {
			t.Fatalf("PrepareOperation: %v", err)
		}
		if details != nil {
			t.Fatalf("bearer supplied; expected no remaining requirements, got %+v", details)
		}
	})

	// A binding whose format exposes no static preparer reports nil (the
	// always-satisfiable answer), same as PrepareBinding.
	t.Run("no preparer yields nil", func(t *testing.T) {
		op := newOpInvoker(&mockBindingInvoker{}, nil)
		details, err := op.PrepareOperation(bg(), opTestInterface(), "getUser")
		if err != nil || details != nil {
			t.Fatalf("expected (nil, nil), got (%+v, %v)", details, err)
		}
	})

	// Shares Invoke's resolution: alias-aware (OBI-T-12) + WithBindingKey pinning.
	t.Run("alias and pinned binding", func(t *testing.T) {
		mock := &preparerMock{&mockBindingInvoker{opts: mockOpts{requireBearer: true, preflight: true}}}
		op := newOpInvoker(mock, nil)
		details, err := op.PrepareOperation(bg(), opTestInterface(), "fetchUser", // alias of getUser
			WithBindingKey("getUser.main"))
		if err != nil {
			t.Fatalf("PrepareOperation(alias, pinned): %v", err)
		}
		if details == nil {
			t.Fatal("expected requirements for the pinned binding")
		}
	})

	// Wiring failures return an error, never a panic.
	t.Run("wiring failures return errors", func(t *testing.T) {
		op := newOpInvoker(&mockBindingInvoker{}, nil)
		if _, err := op.PrepareOperation(bg(), opTestInterface(), "nope"); err == nil {
			t.Fatal("expected error for unknown operation")
		}
		if _, err := op.PrepareOperation(bg(), nil, "getUser"); err == nil {
			t.Fatal("expected error for nil interface")
		}
	})
}

func TestOperationInvokerSuccessNeedsNoProtocolMetadata(t *testing.T) {
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("ping"))
	vals, err := drainOutputs(t, call)
	if err != nil || len(vals) != 1 {
		t.Fatalf("got %v err=%v", vals, err)
	}
}

// A binding that exists but needs an unregistered format must not report
// "no binding for operation" — that sends the user to audit the document,
// where they will find the binding present and correct, instead of to
// their own NewOperationInvoker call.
func TestSelectBinding_FormatSkippedNamesTheGap(t *testing.T) {
	iface := &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]openbindings.Operation{"listItems": {}},
		Sources:      map[string]openbindings.Source{"api": {BindingSpec: "openapi@3.1.0", Location: "https://x.test/spec.json"}},
		Bindings:     map[string]openbindings.BindingEntry{"listItems.api": {Operation: "listItems", Source: "api", Selector: "#/paths/~1items/get"}},
	}
	_, _, err := selectBinding(iface, "listItems", map[string]bool{"mock@1.0": true})
	if !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("want ErrBindingNotFound, got %v", err)
	}
	for _, want := range []string{"listItems.api", "openapi@3.1.0", "mock@1.0", "register the spec's invoker"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}

	// A genuinely missing binding keeps the plain message.
	_, _, err = selectBinding(iface, "listItems", nil)
	if err != nil {
		t.Fatalf("nil availableFormats should select the binding, got %v", err)
	}
}

// selectionTestInterface builds a three-binding operation for exercising
// ambiguity refusal and context.configuration.selection.
func selectionTestInterface() *openbindings.Interface {
	return &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]openbindings.Operation{"op": {}},
		Sources: map[string]openbindings.Source{
			"a": {BindingSpec: "mock@1.0", Location: "https://a.test"},
			"b": {BindingSpec: "mock@1.0", Location: "https://b.test"},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"op.declared":    {Operation: "op", Source: "a", Selector: "r1", Preference: pf(-5)},
			"op.undeclared":  {Operation: "op", Source: "b", Selector: "r2"},
			"op.undeclared2": {Operation: "op", Source: "b", Selector: "r3"},
		},
	}
}

func TestSelectBinding_RefusesAmbiguityWithoutInventedPolicy(t *testing.T) {
	iface := selectionTestInterface()
	if _, _, err := selectBinding(iface, "op", nil); !errors.Is(err, ErrBindingSelectionRequired) {
		t.Fatalf("multiple candidates must require selection, got %v", err)
	}

	// Preference, deprecation, and lexicographic order remain metadata, not
	// implicit authority to choose.
	entry := iface.Bindings["op.declared"]
	entry.Deprecated = true
	entry.Preference = pf(1000)
	iface.Bindings["op.declared"] = entry
	if _, _, err := selectBinding(iface, "op", nil); !errors.Is(err, ErrBindingSelectionRequired) {
		t.Fatalf("metadata must not resolve ambiguity, got %v", err)
	}
}

// The core defines no source-level preference: the model no longer carries
// the field at all, so a document written with the retired vocabulary
// parses with it as an unknown field and selection structurally cannot
// consult it.
func TestSelectBinding_NoSourcePreferenceInheritance(t *testing.T) {
	doc := `{
		"openbindings": "0.2.0",
		"operations": {"op": {}},
		"sources": {
			"a": {"bindingSpec": "mock@1.0", "location": "https://a.test"},
			"b": {"bindingSpec": "mock@1.0", "location": "https://b.test", "preference": 100}
		},
		"bindings": {
			"op.declared":   {"operation": "op", "source": "a", "selector": "r1", "preference": -5},
			"op.undeclared": {"operation": "op", "source": "b", "selector": "r2"}
		}
	}`
	var iface openbindings.Interface
	if err := json.Unmarshal([]byte(doc), &iface); err != nil {
		t.Fatal(err)
	}
	// The retired key survives losslessly but is not model surface.
	if _, isModel := iface.Sources["b"].Unknown["preference"]; !isModel {
		t.Fatal("retired source preference should land in Unknown")
	}
	if _, _, err := selectBinding(&iface, "op", nil); !errors.Is(err, ErrBindingSelectionRequired) {
		t.Fatalf("source preference must not select a binding, got %v", err)
	}
}

// The context.configuration.selection override: an ordered list of binding
// keys, the first invocable entry winning; entries that are undefined or
// target another operation are skipped.
func TestSelectionOverride_FirstInvocableWins(t *testing.T) {
	iface := selectionTestInterface()

	key, b, ok := selectionOverride(iface, "op",
		[]string{"nope", "op.undeclared2", "op.declared"}, nil)
	if !ok || key != "op.undeclared2" || b == nil {
		t.Fatalf("first invocable listed key must win, got %q ok=%v", key, ok)
	}

	// A listed key whose format the invoker cannot act on is not invocable.
	key, _, ok = selectionOverride(iface, "op",
		[]string{"op.undeclared2", "op.declared"}, map[string]bool{"other@1.0": true})
	if ok {
		t.Fatalf("no listed key is invocable under other@1.0, got %q", key)
	}

	// No override listed: not ok, so sole-candidate/ambiguity resolution applies.
	if _, _, ok = selectionOverride(iface, "op", nil, nil); ok {
		t.Fatal("empty override must not select")
	}
}

// End-to-end: no choice refuses ambiguity, configuration.selection supplies
// an ordered choice, and an explicit binding key bypasses both.
func TestInvoke_SelectionOverrideViaConfiguration(t *testing.T) {
	mock := &mockBindingInvoker{}
	op := newOpInvoker(mock, nil)
	iface := opTestInterface()
	iface.Bindings["ping.alt"] = openbindings.BindingEntry{Operation: "ping", Source: "mock", Selector: "ping", Preference: pf(-1)}

	op.BindingSelector = nil
	if _, err := drainOutputs(t, Invoke(bg(), op, iface, NewOperationSignature[any, any]("ping"))); codeOf(t, err) != ErrCodeBindingSelectionRequired {
		t.Fatalf("ambiguous invocation must require selection, got %v", err)
	}

	// The consumer override displaces the policy.
	call := Invoke(bg(), op, iface, NewOperationSignature[any, any]("ping"),
		WithContext(map[string]any{"configuration": map[string]any{"selection": []any{"ping.main"}}}))
	if _, err := drainOutputs(t, call); err != nil {
		t.Fatal(err)
	}
	if got := mock.lastBindingKey(); got != "ping.main" {
		t.Fatalf("configuration.selection must choose the first effective candidate, got %q", got)
	}

	// An explicit binding key bypasses selection (and the override) entirely.
	call = Invoke(bg(), op, iface, NewOperationSignature[any, any]("ping"),
		WithBindingKey("ping.alt"),
		WithContext(map[string]any{"configuration": map[string]any{"selection": []any{"ping.main"}}}))
	if _, err := drainOutputs(t, call); err != nil {
		t.Fatal(err)
	}
	if got := mock.lastBindingKey(); got != "ping.alt" {
		t.Fatalf("an explicit binding key bypasses the override, got %q", got)
	}
}
