package openbindings

// Operation-layer tests mirroring the TS SDK's operation-invoker.test.ts:
// wiring, cardinalities, OBI-T-07/T-08, CONTEXT_REQUIRED negotiation
// (resolve-replay-retry, preflight), and metadata pass-through.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Mock binding invoker (the design doc's reference mock, ref-dispatched)
// ---------------------------------------------------------------------------

var bearerDetails = &ContextRequiredDetails{
	Target:       "api.example.com",
	Alternatives: []ContextAlternative{{Requirements: []ContextRequirement{{Type: "auth.bearer"}}}},
}

type mockOpts struct {
	token           string
	requireBearer   bool // getUser challenges when context lacks bearerToken (after reading input)
	challengeAlways bool // getUser challenges unconditionally (tests the retry cap)
	preflight       bool // expose PrepareBinding reporting the bearer requirement
}

type mockBindingInvoker struct {
	opts mockOpts

	mu       sync.Mutex
	attempts int
	prepares int
	reads    [][]any // inputs read per attempt
	contexts []map[string]any
}

func (m *mockBindingInvoker) Formats() []FormatInfo {
	tok := m.opts.token
	if tok == "" {
		tok = "mock@1.0"
	}
	return []FormatInfo{{Token: tok}}
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

	switch args.Ref {
	case "ping":
		_ = h.CloseInput()
		_ = h.SetHeader(Metadata{"x-mock": {"ping"}})
		if err := h.EmitOutput(map[string]any{"ok": true}); err != nil {
			return nil
		}
		h.SetTrailer(Metadata{"x-mock-trailer": {"done"}})
		h.CloseOutput()
	case "getUser":
		// Unary: read first input, then (deliberately AFTER the read — the
		// "read ≠ consumed" case) check context.
		first, ok, err := readFirst()
		if err != nil {
			return nil
		}
		if !ok {
			h.FireError(&InvocationError{Code: ErrCodeMissingInput, Message: "getUser requires input"})
			return nil
		}
		record(first)
		if m.opts.challengeAlways || (m.opts.requireBearer && ContextBearerToken(args.Context) == "") {
			h.FireError(NewContextRequiredError("bearer token required", bearerDetails))
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
		h.FireError(NewContextRequiredError("token expired mid-stream", bearerDetails))
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
		h.FireError(&InvocationError{Code: ErrCodeRuntime, Message: fmt.Sprintf("unknown ref: %s", args.Ref)})
	}
	return nil
}

// PrepareBinding is attached via the preparerMock wrapper so that plain
// mocks do NOT implement BindingPreparer.
type preparerMock struct {
	*mockBindingInvoker
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

func opTestInterface() *Interface {
	return &Interface{
		OpenBindings: "0.2.0",
		Schemas: map[string]JSONSchema{
			"UserInput": {
				"type":                 "object",
				"properties":           map[string]any{"id": map[string]any{"type": "string"}},
				"required":             []any{"id"},
				"additionalProperties": false,
			},
		},
		Operations: map[string]Operation{
			"ping": {},
			"getUser": {
				Aliases: []string{"fetchUser"},
				Input:   JSONSchema{"$ref": "#/schemas/UserInput"},
				Output: JSONSchema{
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
				Output: JSONSchema{
					"type": "object",
					"properties": map[string]any{
						"id":     map[string]any{"type": "string"},
						"status": map[string]any{"type": "string"},
					},
					"required": []any{"id", "status"},
				},
			},
			"watchTyped": {
				Output: JSONSchema{
					"type":       "object",
					"properties": map[string]any{"n": map[string]any{"type": "number"}},
					"required":   []any{"n"},
				},
			},
			"chat":         {},
			"uploadChunks": {},
		},
		Sources: map[string]Source{
			"mock": {Format: "mock@1.0", Location: "mem://mock"},
		},
		Bindings: map[string]BindingEntry{
			"ping.main":    {Operation: "ping", Source: "mock", Ref: "ping"},
			"getUser.main": {Operation: "getUser", Source: "mock", Ref: "getUser", Preference: pf(99)},
			"getUser.bad":  {Operation: "getUser", Source: "mock", Ref: "badUser", Preference: pf(1)},
			"echo.transformed": {
				Operation: "echo", Source: "mock", Ref: "echoInput",
				InputTransform: &TransformOrRef{Inline: "idToUserId"},
			},
			"watchOrders.main": {Operation: "watchOrders", Source: "mock", Ref: "watchOrders", Preference: pf(99)},
			"watchOrders.challenge": {
				Operation: "watchOrders", Source: "mock", Ref: "watchThenChallenge", Preference: pf(1),
			},
			"watchTyped.main":   {Operation: "watchTyped", Source: "mock", Ref: "streamBadSecond"},
			"chat.main":         {Operation: "chat", Source: "mock", Ref: "chat"},
			"uploadChunks.main": {Operation: "uploadChunks", Source: "mock", Ref: "uploadChunks"},
		},
	}
}

func newOpInvoker(mock BindingInvoker, resolver ContextResolver) *OperationInvoker {
	op := NewOperationInvoker(mock)
	op.TransformEvaluator = exprEvaluator{}
	op.ContextResolver = resolver
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
		iface      *Interface
		operation  string
		bindingKey string
		code       string
	}{
		{"nil interface", nil, "ping", "", ErrCodeValidationFailed},
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
		src.Format = "absent@1.0"
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
	if codeOf(t, err) != ErrCodeValidationFailed {
		t.Fatalf("expected write rejection, got %v", err)
	}
	_, derr := drainOutputs(t, call)
	if codeOf(t, derr) != ErrCodeValidationFailed {
		t.Fatalf("expected terminal, got %v", derr)
	}
	if _, _, reads, _ := mock.snapshot(); len(reads) > 0 && len(reads[0]) > 0 {
		t.Fatalf("binding must never see the invalid message: %v", reads)
	}
}

func TestOpT07ValidatesBeforeTransform(t *testing.T) {
	iface := opTestInterface()
	echo := iface.Operations["echo"]
	echo.Input = JSONSchema{
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
	b.InputTransform = &TransformOrRef{Inline: "boom"}
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
	if codeOf(t, err) != ErrCodeValidationFailed {
		t.Fatalf("expected ERR_VALIDATION_FAILED, got %v", err)
	}
}

func TestOpT08PerItemForStreaming(t *testing.T) { // SS
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("watchTyped"))
	vals, err := drainOutputs(t, call)
	if len(vals) != 1 || vals[0].(map[string]any)["n"] != float64(1) {
		t.Fatalf("valid prefix must be delivered, got %v", vals)
	}
	if codeOf(t, err) != ErrCodeValidationFailed {
		t.Fatalf("expected ERR_VALIDATION_FAILED, got %v", err)
	}
}

func TestOpT08ValidatesAfterTransform(t *testing.T) {
	iface := opTestInterface()
	b := iface.Bindings["watchTyped.main"]
	b.OutputTransform = &TransformOrRef{Inline: "breakShape"}
	iface.Bindings["watchTyped.main"] = b
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, iface, NewOperationSignature[any, any]("watchTyped"))
	vals, err := drainOutputs(t, call)
	if len(vals) != 0 {
		t.Fatalf("transform broke the shape; nothing should emit, got %v", vals)
	}
	if codeOf(t, err) != ErrCodeValidationFailed {
		t.Fatalf("expected ERR_VALIDATION_FAILED, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// OBI-T-16 — claim semantics: unresolvable graph is distinct from mismatch;
// `format` is an annotation, never an assertion
// ---------------------------------------------------------------------------

// An unresolvable governing schema graph means the claim could not be
// EVALUATED: ERR_SCHEMA_UNRESOLVED, distinct from a value mismatch, never
// partial validation.
func TestOpT16UnresolvableOutputGraphIsSchemaUnresolved(t *testing.T) {
	iface := opTestInterface()
	op := iface.Operations["watchTyped"]
	op.Output = JSONSchema{"$ref": "#/schemas/DoesNotExist"}
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
	op.Input = JSONSchema{"$ref": "#/schemas/DoesNotExist"}
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
	op.Input = JSONSchema{
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

// ---------------------------------------------------------------------------
// Metadata pass-through
// ---------------------------------------------------------------------------

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

func TestOpMetadataPassThrough(t *testing.T) {
	op := newOpInvoker(&mockBindingInvoker{}, nil)
	call := Invoke(bg(), op, opTestInterface(), NewOperationSignature[any, any]("ping"))
	vals, err := drainOutputs(t, call)
	if err != nil || len(vals) != 1 {
		t.Fatalf("got %v err=%v", vals, err)
	}
	md, err := call.Header(shortCtx(t))
	if err != nil || len(md["x-mock"]) != 1 {
		t.Fatalf("header: %v err=%v", md, err)
	}
	if tr := call.Trailer(); len(tr["x-mock-trailer"]) != 1 {
		t.Fatalf("trailer: %v", tr)
	}
}

// A binding that exists but needs an unregistered format must not report
// "no binding for operation" — that sends the user to audit the document,
// where they will find the binding present and correct, instead of to
// their own NewOperationInvoker call.
func TestSelectBinding_FormatSkippedNamesTheGap(t *testing.T) {
	iface := &Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{"listItems": {}},
		Sources:      map[string]Source{"api": {Format: "openapi@3.1.0", Location: "https://x.test/spec.json"}},
		Bindings:     map[string]BindingEntry{"listItems.api": {Operation: "listItems", Source: "api", Ref: "#/paths/~1items/get"}},
	}
	_, _, err := selectBinding(iface, "listItems", map[string]bool{"mock@1.0": true})
	if !errors.Is(err, ErrBindingNotFound) {
		t.Fatalf("want ErrBindingNotFound, got %v", err)
	}
	for _, want := range []string{"listItems.api", "openapi@3.1.0", "mock@1.0", "register the format's invoker"} {
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

// selectionTestInterface builds a three-binding operation for exercising the
// operation-invoker contract's default selection policy and its
// context.configuration.selection override.
func selectionTestInterface() *Interface {
	return &Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{"op": {}},
		Sources: map[string]Source{
			"a": {Format: "mock@1.0", Location: "https://a.test"},
			"b": {Format: "mock@1.0", Location: "https://b.test"},
		},
		Bindings: map[string]BindingEntry{
			"op.declared":    {Operation: "op", Source: "a", Ref: "r1", Preference: pf(-5)},
			"op.undeclared":  {Operation: "op", Source: "b", Ref: "r2"},
			"op.undeclared2": {Operation: "op", Source: "b", Ref: "r3"},
		},
	}
}

// The ruled default policy: a candidate with no declared preference ranks
// below EVERY candidate with one — a declared negative beats undeclared —
// and remaining ties break by lexicographic binding key.
func TestSelectBinding_UndeclaredRanksBelowAllDeclared(t *testing.T) {
	key, _, err := selectBinding(selectionTestInterface(), "op", nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "op.declared" {
		t.Fatalf("declared preference -5 must beat undeclared, got %q", key)
	}

	// With no declared preferences at all, lexicographic key decides.
	iface := selectionTestInterface()
	entry := iface.Bindings["op.declared"]
	entry.Preference = nil
	iface.Bindings["op.declared"] = entry
	key, _, err = selectBinding(iface, "op", nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "op.declared" {
		t.Fatalf("lexicographic tiebreak, got %q", key)
	}
}

// The core defines no source-level preference: nothing is inherited from a
// binding's source, and an undeclared binding preference stays undeclared.
func TestSelectBinding_NoSourcePreferenceInheritance(t *testing.T) {
	iface := selectionTestInterface()
	// A source-level preference (retired core vocabulary) must be inert:
	// op.undeclared's source declaring 100 must not outrank op.declared's -5.
	src := iface.Sources["b"]
	src.Preference = pf(100)
	iface.Sources["b"] = src
	key, _, err := selectBinding(iface, "op", nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "op.declared" {
		t.Fatalf("source preference must not be inherited, got %q", key)
	}
}

// Non-deprecated candidates rank before deprecated ones regardless of
// declared preference.
func TestSelectBinding_DeprecationTierBeforePreference(t *testing.T) {
	iface := selectionTestInterface()
	entry := iface.Bindings["op.declared"]
	entry.Deprecated = true
	iface.Bindings["op.declared"] = entry
	key, _, err := selectBinding(iface, "op", nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "op.undeclared" {
		t.Fatalf("non-deprecated must outrank deprecated even undeclared-vs-declared, got %q", key)
	}
}

// The context.configuration.selection override: an ordered list of binding
// keys, the first invocable entry winning; entries that are undefined or
// target another operation are skipped; when none is invocable the default
// policy applies.
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

	// No override listed: not ok, default policy applies.
	if _, _, ok = selectionOverride(iface, "op", nil, nil); ok {
		t.Fatal("empty override must not select")
	}
}

// End-to-end: WithContext carrying configuration.selection displaces the
// default policy on Invoke, and an explicit binding key bypasses both.
func TestInvoke_SelectionOverrideViaConfiguration(t *testing.T) {
	mock := &mockBindingInvoker{}
	op := newOpInvoker(mock, nil)
	iface := opTestInterface()
	iface.Bindings["ping.alt"] = BindingEntry{Operation: "ping", Source: "mock", Ref: "ping", Preference: pf(-1)}

	// Default policy: ping.alt declares a preference, ping.main does not —
	// the declared one wins.
	plan, err := op.PlanOperation(bg(), iface, "ping")
	if err != nil {
		t.Fatal(err)
	}
	if plan.BindingKey != "ping.alt" {
		t.Fatalf("declared preference must win by default, got %q", plan.BindingKey)
	}

	// The consumer override displaces the policy.
	plan, err = op.PlanOperation(bg(), iface, "ping",
		WithContext(map[string]any{"configuration": map[string]any{"selection": []any{"ping.main"}}}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.BindingKey != "ping.main" {
		t.Fatalf("configuration.selection must displace the default, got %q", plan.BindingKey)
	}

	// An explicit binding key bypasses selection (and the override) entirely.
	plan, err = op.PlanOperation(bg(), iface, "ping",
		WithBindingKey("ping.alt"),
		WithContext(map[string]any{"configuration": map[string]any{"selection": []any{"ping.main"}}}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.BindingKey != "ping.alt" {
		t.Fatalf("an explicit binding key bypasses the override, got %q", plan.BindingKey)
	}
}

// A format outside the consultation seam (not a BuiltinHooksProvider) must
// plan as not-consulted on the decode/classify axes even when an
// invoker-level hook is attached — the plan is the one diagnostic surface
// for "will my hook run?", and claiming "hook" for a format that ignores
// the seam is exactly the wrong answer.
func TestPlanOperation_NonSeamFormatReportsNotConsulted(t *testing.T) {
	iface := &Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]Operation{"op": {}},
		Sources:      map[string]Source{"s": {Format: "mock@1.0", Location: "mem://x"}},
		Bindings:     map[string]BindingEntry{"op.s": {Operation: "op", Source: "s", Ref: "op"}},
	}
	opInv := NewOperationInvoker(&mockBindingInvoker{}) // no BuiltinHooksProvider
	opInv.OutputDecoder = func(InvokeSite, RawResult) (any, error) { return nil, nil }

	plan, err := opInv.PlanOperation(context.Background(), iface, "op")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Decode.Chain) != 1 || plan.Decode.Chain[0] != "not-consulted" {
		t.Errorf("Decode.Chain = %v, want [not-consulted]", plan.Decode.Chain)
	}
	if len(plan.Classify.Chain) != 1 || plan.Classify.Chain[0] != "not-consulted" {
		t.Errorf("Classify.Chain = %v, want [not-consulted]", plan.Classify.Chain)
	}
}
