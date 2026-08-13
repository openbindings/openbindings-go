package usage

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

// This file activates the exec routing subsystem and the HookTable, which
// the review found at ~0% coverage — the exact place the non-object
// input-drop bug (C1f) hid. It drives the purpose-built testcli subcommands
// (slurp/drink/statfile/num/prose) through the public InvokeBinding path via
// a consumer FieldRouter, plus direct unit tests for the pure routing
// helpers and the HookTable.

// ---------------------------------------------------------------------------
// C1f regression: a present non-object input is refused before spawn.
// ---------------------------------------------------------------------------

// TestRouting_NonObjectInputRefused pins USAGE-P-04 / §9.1: the caller-facing
// input is one JSON object (or absent). A present non-object input (a bare
// string here) is out of contract and is refused BEFORE spawn — loudly, never
// silently dropped. The regression it guards: a typed-nil fields map used to
// slip past buildCLIArgs's object guard, running the bare command with the
// payload discarded.
func TestRouting_NonObjectInputRefused(t *testing.T) {
	for _, in := range []any{"not-an-object", []any{"a", "b"}, float64(3)} {
		_, ierr := invokeUsage(t, NewInvoker(), &openbindings.BindingInvocationArgs{
			Source: testSource(),
			Ref:    "echo",
		}, in)
		if ierr == nil {
			t.Fatalf("input %v (%T): expected a loud refusal (USAGE-P-04 §9.1), got success", in, in)
		}
		if ierr.Code != openbindings.ErrCodeValidationFailed {
			t.Fatalf("input %v: expected ERR_VALIDATION_FAILED, got %s: %s", in, ierr.Code, ierr.Error())
		}
		if ierr.HasData() {
			t.Errorf("native routing evidence crossed as abstract data: %#v", ierr.Data)
		}
	}
}

// TestRouting_DirectLaneNonObjectRefused pins the same rule on the SDK-only
// direct-binary lane (buildDirectArgsFromRef), which had the identical drop.
func TestRouting_DirectLaneNonObjectRefused(t *testing.T) {
	if _, err := buildDirectArgsFromRef("echo", "not-an-object"); err == nil {
		t.Fatal("direct lane must refuse a non-object input, got nil error")
	}
	if _, err := buildDirectArgsFromRef("echo", []any{"a", "b"}); err == nil {
		t.Fatal("direct lane must refuse an array input, got nil error")
	}
	args, err := buildDirectArgsFromRef("echo", map[string]any{"flag": "v"})
	if err != nil {
		t.Fatalf("object input must assemble, got %v", err)
	}
	if len(args) == 0 || args[0] != "echo" {
		t.Fatalf("expected the command path to lead, got %v", args)
	}
}

// ---------------------------------------------------------------------------
// End-to-end routing through the testcli subcommands.
// ---------------------------------------------------------------------------

// routerDriver wraps the fixture invoker with a per-field FieldRouter (and,
// optionally, a strict-JSON output decoder), the consumer-configuration shape
// that elects exec channels.
func routerDriver(routes map[string]string, decodeJSON bool) driver {
	return hooked(func(op *openbindings.OperationInvoker) {
		op.FieldRouter = func(site openbindings.InvokeSite, field string, _ any) string {
			if site.FamilyName() != "usage" {
				return ""
			}
			return routes[field]
		}
		if decodeJSON {
			op.OutputDecoder = func(_ openbindings.InvokeSite, raw openbindings.RawResult) (any, error) {
				if len(raw.Body) == 0 {
					return nil, nil
				}
				var v any
				if err := json.Unmarshal(raw.Body, &v); err != nil {
					return nil, err
				}
				return v, nil
			}
		}
	})
}

func TestRouting_StdinDash(t *testing.T) {
	// The `doc` field rides stdin, with `-` in its positional slot (the
	// filter lane's majority route). slurp echoes stdin + argv as JSON.
	out, ierr := invokeUsage(t, routerDriver(map[string]string{"doc": RouteStdinDash}, true), &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "slurp",
	}, map[string]any{"doc": "piped payload"})
	if ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Error())
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON map, got %T: %v", out, out)
	}
	if m["stdin"] != "piped payload" {
		t.Errorf("stdin = %v, want the routed bytes", m["stdin"])
	}
	if args, _ := m["args"].([]any); len(args) != 1 || args[0] != "-" {
		t.Errorf("args = %v, want the slot to carry \"-\"", m["args"])
	}
}

func TestRouting_SlotlessStdin(t *testing.T) {
	// `payload` maps to no slot on drink; the slotless pure channel delivers
	// its bytes to stdin with nothing on argv.
	out, ierr := invokeUsage(t, routerDriver(map[string]string{"payload": RouteStdin}, true), &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "drink",
	}, map[string]any{"payload": "slotless bytes"})
	if ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Error())
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON map, got %T: %v", out, out)
	}
	if m["stdin"] != "slotless bytes" {
		t.Errorf("stdin = %v, want the routed bytes", m["stdin"])
	}
	if args, _ := m["args"].([]any); len(args) != 0 {
		t.Errorf("args = %v, want empty (nothing on argv)", m["args"])
	}
}

func TestRouting_FileMaterialization(t *testing.T) {
	// `doc` materializes into a temp file whose PATH substitutes into the
	// slot; statfile reads it back and reports its mode — asserting both the
	// round-trip and the 0600 the routing lane creates it with.
	out, ierr := invokeUsage(t, routerDriver(map[string]string{"doc": RouteFile}, true), &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "statfile",
	}, map[string]any{"doc": "materialized contents"})
	if ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Error())
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON map, got %T: %v", out, out)
	}
	if m["content"] != "materialized contents" {
		t.Errorf("content = %v, want the routed bytes", m["content"])
	}
	if m["mode"] != "0600" {
		t.Errorf("mode = %v, want 0600 (routed temp files are 0600)", m["mode"])
	}
}

func TestRouting_JSONLaneAndTextLane(t *testing.T) {
	// num: a bare number under a declared JSON lane parses to a number.
	out, ierr := invokeUsage(t, routerDriver(nil, true), &openbindings.BindingInvocationArgs{
		Source: testSource(), Ref: "num",
	}, nil)
	if ierr != nil {
		t.Fatalf("num: unexpected error: %s", ierr.Error())
	}
	if n, _ := out.(float64); n != 42 {
		t.Errorf("num JSON lane = %v, want 42", out)
	}
	// prose: human text under the default text lane is the raw stdout string.
	out, ierr = invokeUsage(t, NewInvoker(), &openbindings.BindingInvocationArgs{
		Source: testSource(), Ref: "prose",
	}, nil)
	if ierr != nil {
		t.Fatalf("prose: unexpected error: %s", ierr.Error())
	}
	if out != "all good" {
		t.Errorf("prose text lane = %#v, want %q", out, "all good")
	}
}

// ---------------------------------------------------------------------------
// Impossible-routing refusals — all before spawn.
// ---------------------------------------------------------------------------

func refusalCode(t *testing.T, d driver, ref string, routes map[string]string, input any) *openbindings.InvocationError {
	t.Helper()
	_, ierr := invokeUsage(t, d, &openbindings.BindingInvocationArgs{Source: testSource(), Ref: ref}, input)
	if ierr == nil {
		t.Fatalf("%s: expected a pre-spawn refusal, got success", ref)
	}
	return ierr
}

func TestRouting_TwoFieldsToStdinRefused(t *testing.T) {
	// Two slotless fields both route to stdin: at most one may ride it.
	d := routerDriver(map[string]string{"a": RouteStdin, "b": RouteStdin}, false)
	ierr := refusalCode(t, d, "drink", nil, map[string]any{"a": "x", "b": "y"})
	if ierr.Code != openbindings.ErrCodeValidationFailed || ierr.HasData() {
		t.Fatalf("want code-only ERR_VALIDATION_FAILED, got %#v", ierr)
	}
}

func TestRouting_StdinOnSlottedFieldRefused(t *testing.T) {
	// The slotless pure channel requires a field mapping to NO slot; `tag`
	// maps to --tag, so it is refused with the stdin-dash teaching hint.
	d := routerDriver(map[string]string{"tag": RouteStdin}, false)
	ierr := refusalCode(t, d, "slurp", nil, map[string]any{"tag": "x", "doc": "input"})
	if ierr.Code != openbindings.ErrCodeValidationFailed || ierr.HasData() {
		t.Fatalf("want code-only ERR_VALIDATION_FAILED, got %#v", ierr)
	}
}

func TestRouting_UnknownChannelTokenRefused(t *testing.T) {
	d := routerDriver(map[string]string{"doc": "teleport"}, false)
	ierr := refusalCode(t, d, "slurp", nil, map[string]any{"doc": "x"})
	if ierr.Code != openbindings.ErrCodeValidationFailed || ierr.HasData() {
		t.Fatalf("want code-only ERR_VALIDATION_FAILED, got %#v", ierr)
	}
}

func TestRouting_ByteCapRefused(t *testing.T) {
	big := strings.Repeat("x", maxRouteBytes+1)
	d := routerDriver(map[string]string{"payload": RouteStdin}, false)
	ierr := refusalCode(t, d, "drink", nil, map[string]any{"payload": big})
	if ierr.Code != openbindings.ErrCodeValidationFailed || ierr.HasData() {
		t.Fatalf("want code-only ERR_VALIDATION_FAILED, got %#v", ierr)
	}
}

// sourceKDL builds a bare-kdl fixture source from inline KDL text.
func sourceKDL(kdl string) openbindings.InvocationSource {
	return openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(kdl)}
}

func TestRouting_BoolFlagSlotRefused(t *testing.T) {
	kdl := `bin "` + testBinary + `"
cmd "toggle" {
    flag "--on" help="a boolean flag"
}
`
	d := routerDriver(map[string]string{"on": RouteStdinDash}, false)
	_, ierr := invokeUsage(t, d, &openbindings.BindingInvocationArgs{Source: sourceKDL(kdl), Ref: "toggle"}, map[string]any{"on": true})
	if ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed || ierr.HasData() {
		t.Fatalf("want ERR_VALIDATION_FAILED for a bool-flag slot, got %v", ierr)
	}
}

func TestRouting_StdinDashChoicesExcludeDashRefused(t *testing.T) {
	kdl := `bin "` + testBinary + `"
cmd "pick" {
    arg "<mode>" {
        choices "trace" "debug" "info"
    }
}
`
	d := routerDriver(map[string]string{"mode": RouteStdinDash}, false)
	_, ierr := invokeUsage(t, d, &openbindings.BindingInvocationArgs{Source: sourceKDL(kdl), Ref: "pick"}, map[string]any{"mode": "trace"})
	if ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed || ierr.HasData() {
		t.Fatalf("want ERR_VALIDATION_FAILED for choices excluding \"-\", got %v", ierr)
	}
}

// ---------------------------------------------------------------------------
// Pure routing helpers.
// ---------------------------------------------------------------------------

func TestRouteBytes(t *testing.T) {
	if data, isStr := routeBytes("hello"); !isStr || string(data) != "hello" {
		t.Errorf("string: got (%q, %v), want raw string", data, isStr)
	}
	if data, isStr := routeBytes(map[string]any{"k": "v"}); isStr || string(data) != `{"k":"v"}` {
		t.Errorf("object: got (%q, %v), want compact JSON", data, isStr)
	}
	if data, isStr := routeBytes(float64(42)); isStr || string(data) != "42" {
		t.Errorf("number: got (%q, %v), want compact JSON", data, isStr)
	}
}

func TestRouteFileName(t *testing.T) {
	if got := routeFileName("payload", false); got != "payload.json" {
		t.Errorf("json value: got %q, want payload.json", got)
	}
	if got := routeFileName("payload", true); got != "payload" {
		t.Errorf("string value: got %q, want bare payload", got)
	}
	if got := routeFileName("weird/name", false); strings.ContainsAny(got, "/") {
		t.Errorf("path-hostile runes not sanitized: %q", got)
	}
	if got := routeFileName("data.json", true); strings.HasSuffix(got, ".json") {
		t.Errorf("string value ending in .json must not keep the suffix: %q", got)
	}
}

func TestFindSlotAndChoices(t *testing.T) {
	cmd := &Command{
		Flags: []Flag{
			{Usage: "--tag <value>"},
			{Usage: "--on"},
			{Usage: "--level <lvl>", Choices: []string{"a", "b"}},
		},
		Args: []Arg{
			{Name: "<doc>"},
			{Name: "<mode>", Choices: []string{"x", "-"}},
		},
	}
	if _, kind := findSlot(cmd, nil, "tag"); kind != slotValueFlag {
		t.Errorf("tag: kind = %v, want slotValueFlag", kind)
	}
	if _, kind := findSlot(cmd, nil, "on"); kind != slotBoolFlag {
		t.Errorf("on: kind = %v, want slotBoolFlag", kind)
	}
	if _, kind := findSlot(cmd, nil, "doc"); kind != slotArg {
		t.Errorf("doc: kind = %v, want slotArg", kind)
	}
	if _, kind := findSlot(cmd, nil, "missing"); kind != slotNone {
		t.Errorf("missing: kind = %v, want slotNone", kind)
	}
	flagSlot, _ := findSlot(cmd, nil, "level")
	if cs := slotChoices(flagSlot); len(cs) != 2 || cs[0] != "a" {
		t.Errorf("flag choices = %v, want [a b]", cs)
	}
	argSlot, _ := findSlot(cmd, nil, "mode")
	if cs := slotChoices(argSlot); !containsString(cs, "-") {
		t.Errorf("arg choices = %v, want to include \"-\"", cs)
	}
	if cs := slotChoices("not-a-slot"); cs != nil {
		t.Errorf("non-slot choices = %v, want nil", cs)
	}
}

// ---------------------------------------------------------------------------
// HookTable — the data-shaped consumer configuration.
// ---------------------------------------------------------------------------

func routeIntPtr(i int) *int { return &i }

func TestHookTable_Hooks(t *testing.T) {
	table := HookTable{
		DecodeJSON: []string{"emit"},
		OKExits:    map[string][]int{"compare": {0, 1}},
		Routes:     map[string]map[string]string{"validate": {"locator": RouteStdinDash}},
	}
	decoder, classifier, router := table.Hooks()

	usageSite := func(op string) openbindings.InvokeSite {
		return openbindings.InvokeSite{BindingSpec: BindingSpec, Operation: op}
	}

	// Decoder: a JSON-lane op parses; a non-JSON body is a loud error; an
	// empty body is nil; an op without a row declines.
	if v, err := decoder(usageSite("emit"), openbindings.RawResult{Body: []byte(`{"n":1}`)}); err != nil || v.(map[string]any)["n"] != float64(1) {
		t.Errorf("decoder JSON: got (%v, %v)", v, err)
	}
	if _, err := decoder(usageSite("emit"), openbindings.RawResult{Body: []byte("not json")}); err == nil {
		t.Error("decoder: expected a loud error on a non-JSON machine lane")
	} else {
		var invocationError *openbindings.InvocationError
		if !errors.As(err, &invocationError) {
			t.Fatalf("decoder: error = %T, want InvocationError", err)
		}
		if invocationError.HasData() {
			t.Fatalf("decoder: process evidence leaked into data: %#v", invocationError.Data)
		}
	}
	if v, err := decoder(usageSite("emit"), openbindings.RawResult{Body: nil}); err != nil || v != nil {
		t.Errorf("decoder empty: got (%v, %v), want (nil, nil)", v, err)
	}
	if _, err := decoder(usageSite("other"), openbindings.RawResult{Body: []byte("x")}); !errors.Is(err, openbindings.ErrUseDefault) {
		t.Error("decoder: an op without a JSON row must decline to the default")
	}
	if _, err := decoder(openbindings.InvokeSite{BindingSpec: "openbindings.openapi@1", Operation: "emit"}, openbindings.RawResult{Body: []byte("x")}); !errors.Is(err, openbindings.ErrUseDefault) {
		t.Error("decoder: a non-usage site must decline")
	}

	// Classifier: an OK exit is success; an exit outside the list is not; an
	// op without a row, or a statusless unit, declines.
	if ok, err := classifier(usageSite("compare"), openbindings.RawResult{Status: routeIntPtr(1)}); err != nil || !ok {
		t.Errorf("classifier exit 1: got (%v, %v), want success", ok, err)
	}
	if ok, err := classifier(usageSite("compare"), openbindings.RawResult{Status: routeIntPtr(2)}); err != nil || ok {
		t.Errorf("classifier exit 2: got (%v, %v), want failure", ok, err)
	}
	if _, err := classifier(usageSite("compare"), openbindings.RawResult{Status: nil}); !errors.Is(err, openbindings.ErrUseDefault) {
		t.Error("classifier: a statusless unit must decline")
	}
	if _, err := classifier(usageSite("other"), openbindings.RawResult{Status: routeIntPtr(0)}); !errors.Is(err, openbindings.ErrUseDefault) {
		t.Error("classifier: an op without a row must decline")
	}

	// Router: an op+field with a row elects that channel; anything else "".
	if got := router(usageSite("validate"), "locator", nil); got != RouteStdinDash {
		t.Errorf("router: got %q, want %q", got, RouteStdinDash)
	}
	if got := router(usageSite("validate"), "other", nil); got != "" {
		t.Errorf("router: an unlisted field must decline, got %q", got)
	}
	if got := router(usageSite("nope"), "locator", nil); got != "" {
		t.Errorf("router: an op without a row must decline, got %q", got)
	}
	if got := router(openbindings.InvokeSite{BindingSpec: "openbindings.grpc@1"}, "locator", nil); got != "" {
		t.Errorf("router: a non-usage site must decline, got %q", got)
	}
}

func TestHookTable_Install(t *testing.T) {
	op := openbindings.NewOperationInvoker(NewInvoker())
	HookTable{DecodeJSON: []string{"emit"}}.Install(op)
	if op.OutputDecoder == nil || op.ResultClassifier == nil || op.FieldRouter == nil {
		t.Fatal("Install must attach all three compiled hooks")
	}
}
