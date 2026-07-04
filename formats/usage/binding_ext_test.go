package usage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// bindingWithExt builds a BindingEntry carrying an x-usage member the way it
// arrives in the wild: unmarshaled from a document, so the member rides the
// lossless Extensions map.
func bindingWithExt(t *testing.T, ref, ext string) *openbindings.BindingEntry {
	t.Helper()
	var be openbindings.BindingEntry
	raw := `{"operation":"op","source":"src","ref":"` + ref + `","x-usage":` + ext + `}`
	if err := json.Unmarshal([]byte(raw), &be); err != nil {
		t.Fatalf("unmarshal binding: %v", err)
	}
	return &be
}

func TestParseBindingExt(t *testing.T) {
	t.Run("nil binding is the zero ext", func(t *testing.T) {
		ext, err := parseBindingExt(nil)
		if err != nil || len(ext.Delivery) != 0 || ext.Stdout != "" {
			t.Fatalf("got (%+v, %v), want zero ext", ext, err)
		}
	})
	t.Run("absent member is the zero ext", func(t *testing.T) {
		ext, err := parseBindingExt(&openbindings.BindingEntry{Operation: "op", Source: "src"})
		if err != nil || len(ext.Delivery) != 0 || ext.Stdout != "" {
			t.Fatalf("got (%+v, %v), want zero ext", ext, err)
		}
	})
	t.Run("valid member parses", func(t *testing.T) {
		be := bindingWithExt(t, "diff", `{"delivery":{"baseline":"stdin","comparison":"file"},"stdout":"json"}`)
		ext, err := parseBindingExt(be)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ext.Delivery["baseline"] != deliveryStdin || ext.Delivery["comparison"] != deliveryFile || ext.Stdout != stdoutJSON {
			t.Fatalf("parsed ext = %+v", ext)
		}
	})
	t.Run("unknown delivery mode is rejected", func(t *testing.T) {
		be := bindingWithExt(t, "x", `{"delivery":{"doc":"carrier-pigeon"}}`)
		if _, err := parseBindingExt(be); err == nil {
			t.Fatal("expected an error for an unknown delivery mode")
		}
	})
	t.Run("two stdin fields are rejected", func(t *testing.T) {
		be := bindingWithExt(t, "x", `{"delivery":{"a":"stdin","b":"stdin"}}`)
		if _, err := parseBindingExt(be); err == nil || !strings.Contains(err.Error(), "stdin") {
			t.Fatalf("expected the one-stdin error, got %v", err)
		}
	})
	t.Run("unknown stdout mode is rejected", func(t *testing.T) {
		be := bindingWithExt(t, "x", `{"stdout":"yaml"}`)
		if _, err := parseBindingExt(be); err == nil {
			t.Fatal("expected an error for an unknown stdout mode")
		}
	})
}

func TestApplyDelivery(t *testing.T) {
	t.Run("stdin field substitutes dash and carries bytes", func(t *testing.T) {
		ext := &bindingExt{Delivery: map[string]string{"doc": deliveryStdin}}
		out, stdin, cleanup, err := applyDelivery(map[string]any{"doc": map[string]any{"a": float64(1)}, "tag": "x"}, ext)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		m := out.(map[string]any)
		if m["doc"] != "-" || m["tag"] != "x" {
			t.Fatalf("substituted input = %#v", m)
		}
		if string(stdin) != `{"a":1}` {
			t.Fatalf("stdin bytes = %q", stdin)
		}
	})
	t.Run("string values are written raw", func(t *testing.T) {
		ext := &bindingExt{Delivery: map[string]string{"doc": deliveryStdin}}
		_, stdin, cleanup, err := applyDelivery(map[string]any{"doc": "raw text\n"}, ext)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if string(stdin) != "raw text\n" {
			t.Fatalf("stdin bytes = %q, want the raw string", stdin)
		}
	})
	t.Run("file field materializes and cleans up", func(t *testing.T) {
		ext := &bindingExt{Delivery: map[string]string{"doc": deliveryFile}}
		out, stdin, cleanup, err := applyDelivery(map[string]any{"doc": map[string]any{"a": float64(1)}}, ext)
		if err != nil {
			t.Fatal(err)
		}
		if stdin != nil {
			t.Fatalf("no stdin expected, got %q", stdin)
		}
		path, _ := out.(map[string]any)["doc"].(string)
		if !strings.HasSuffix(path, "doc.json") {
			t.Fatalf("materialized path = %q, want a doc.json temp file", path)
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil || string(data) != `{"a":1}` {
			t.Fatalf("materialized content = %q, %v", data, rerr)
		}
		cleanup()
		if _, serr := os.Stat(path); !os.IsNotExist(serr) {
			t.Fatalf("temp file survived cleanup: %v", serr)
		}
	})
	t.Run("raw string file gets no json suffix", func(t *testing.T) {
		ext := &bindingExt{Delivery: map[string]string{"doc": deliveryFile}}
		out, _, cleanup, err := applyDelivery(map[string]any{"doc": "text"}, ext)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		path, _ := out.(map[string]any)["doc"].(string)
		if !strings.HasSuffix(path, "doc") || strings.HasSuffix(path, ".json") {
			t.Fatalf("materialized path = %q, want a bare doc temp file", path)
		}
	})
	t.Run("absent routed field is a no-op", func(t *testing.T) {
		ext := &bindingExt{Delivery: map[string]string{"doc": deliveryStdin}}
		out, stdin, cleanup, err := applyDelivery(map[string]any{"tag": "x"}, ext)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if stdin != nil {
			t.Fatalf("no stdin expected, got %q", stdin)
		}
		if m := out.(map[string]any); m["tag"] != "x" || len(m) != 1 {
			t.Fatalf("input changed: %#v", m)
		}
	})
}

func TestDecodeStdout(t *testing.T) {
	cases := []struct {
		name    string
		stdout  string
		mode    string
		want    any
		wantErr bool
	}{
		{"json number parses", "42\n", stdoutJSON, float64(42), false},
		{"json object parses", `{"a":1}` + "\n", stdoutJSON, map[string]any{"a": float64(1)}, false},
		{"json empty is null", "", stdoutJSON, nil, false},
		{"json prose errors", "all good\n", stdoutJSON, nil, true},
		{"text strips trailing newlines", "all good\n", stdoutText, "all good", false},
		{"text preserves interior newlines", "a\nb\n\n", stdoutText, "a\nb", false},
		{"heuristic wraps numbers", "42\n", "", map[string]any{"stdout": "42\n"}, false},
		{"heuristic bare-parses null", "null\n", "", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ierr := decodeStdout(tc.stdout, tc.mode)
			if tc.wantErr {
				if ierr == nil || ierr.Code != openbindings.ErrCodeExecutionFailed {
					t.Fatalf("expected ERR_EXECUTION_FAILED, got (%#v, %v)", got, ierr)
				}
				return
			}
			if ierr != nil {
				t.Fatalf("unexpected error: %v", ierr)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tc.want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("decoded %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestArgvToken(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"verbatim", "verbatim"},
		{float64(1000000), "1000000"},
		{float64(1.5), "1.5"},
		{true, "true"},
		{map[string]any{"k": "v"}, `{"k":"v"}`},
		{[]any{float64(1), "two"}, `[1,"two"]`},
	}
	for _, tc := range cases {
		if got := argvToken(tc.in); got != tc.want {
			t.Errorf("argvToken(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Fixture-CLI integration: delivery and stdout modes end to end
// ---------------------------------------------------------------------------

// objSchema marks the operation as input-taking: a non-nil Binding with a nil
// InputSchema means "operation declares no input" to the run loop.
var objSchema = openbindings.JSONSchema{"type": "object"}

func TestIntegration_StdinDelivery(t *testing.T) {
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source:      openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:         "slurp",
		Binding:     bindingWithExt(t, "slurp", `{"delivery":{"doc":"stdin"},"stdout":"json"}`),
		InputSchema: objSchema,
	}, map[string]any{"doc": map[string]any{"a": float64(1)}, "tag": "blue"})
	if ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Message)
	}
	m := out.(map[string]any)
	if m["stdin"] != `{"a":1}` {
		t.Errorf("stdin = %q, want the document JSON", m["stdin"])
	}
	// The routed field's argv slot got "-"; the argv-delivered flag rode along.
	args, _ := m["args"].([]any)
	got := make([]string, 0, len(args))
	for _, a := range args {
		got = append(got, a.(string))
	}
	if strings.Join(got, " ") != "--tag blue -" {
		t.Errorf("argv = %q, want %q", strings.Join(got, " "), "--tag blue -")
	}
}

func TestIntegration_FileDelivery(t *testing.T) {
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:         "readfile",
		Binding:     bindingWithExt(t, "readfile", `{"delivery":{"doc":"file"},"stdout":"json"}`),
		InputSchema: objSchema,
	}, map[string]any{"doc": map[string]any{"a": float64(1)}})
	if ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Message)
	}
	m := out.(map[string]any)
	if m["content"] != `{"a":1}` {
		t.Errorf("content = %q, want the document JSON", m["content"])
	}
	path, _ := m["path"].(string)
	if !strings.HasSuffix(path, "doc.json") {
		t.Errorf("path = %q, want a doc.json temp file", path)
	}
	// The materialized file is cleaned up once the invocation completes.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("temp file survived the invocation: %v", err)
	}
}

func TestIntegration_StdoutJSONStrict(t *testing.T) {
	invoker := NewInvoker()

	// A bare number parses on the declared JSON lane...
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:     "num",
		Binding: bindingWithExt(t, "num", `{"stdout":"json"}`),
	}, nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %s", ierr.Message)
	}
	if out != float64(42) {
		t.Errorf("output = %#v, want 42", out)
	}

	// ...and still wraps on an undeclared lane.
	out, ierr = invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:    "num",
	}, nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %s", ierr.Message)
	}
	if m, _ := out.(map[string]any); m == nil || m["stdout"] != "42\n" {
		t.Errorf("output = %#v, want the {stdout} wrap", out)
	}

	// Prose on a declared JSON lane is a terminal error, not a silent wrap.
	_, ierr = invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:     "prose",
		Binding: bindingWithExt(t, "prose", `{"stdout":"json"}`),
	}, nil)
	if ierr == nil || ierr.Code != openbindings.ErrCodeExecutionFailed {
		t.Fatalf("expected ERR_EXECUTION_FAILED for prose on a JSON lane, got %v", ierr)
	}
}

func TestIntegration_StdoutText(t *testing.T) {
	invoker := NewInvoker()
	out, trailer, ierr := invokeUsageWithTrailer(t, invoker, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:     "prose",
		Binding: bindingWithExt(t, "prose", `{"stdout":"text"}`),
	}, nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %s", ierr.Message)
	}
	if out != "all good" {
		t.Errorf("output = %#v, want stdout with the trailing newline stripped", out)
	}
	if got := trailer["x-stderr"]; len(got) != 1 || got[0] != "checked 3 things\n" {
		t.Errorf("x-stderr trailer = %q", got)
	}
}

func TestIntegration_InvalidExtIsValidationError(t *testing.T) {
	invoker := NewInvoker()
	_, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:         "slurp",
		Binding:     bindingWithExt(t, "slurp", `{"delivery":{"doc":"stdin","tag":"stdin"}}`),
		InputSchema: objSchema,
	}, map[string]any{"doc": "x", "tag": "y"})
	if ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed {
		t.Fatalf("expected ERR_VALIDATION_FAILED, got %v", ierr)
	}
}

func TestParseBindingExt_UnknownMemberFailsClosed(t *testing.T) {
	be := bindingWithExt(t, "x", `{"delivery":{"doc":"stdin"},"shiny":"new"}`)
	if _, err := parseBindingExt(be); err == nil {
		t.Fatal("expected an error for an unknown x-usage member (fail closed)")
	}
}

func TestApplyDelivery_NullRoutedFieldIsAbsent(t *testing.T) {
	ext := &bindingExt{Delivery: map[string]string{"doc": deliveryStdin}}
	out, stdin, cleanup, err := applyDelivery(map[string]any{"doc": nil, "tag": "x"}, ext)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if stdin != nil {
		t.Fatalf("null must route nowhere, got stdin %q", stdin)
	}
	m := out.(map[string]any)
	if _, present := m["doc"]; present {
		t.Fatalf("null routed field must vanish from the input, got %#v", m)
	}
}

func TestApplyDelivery_CapEnforced(t *testing.T) {
	ext := &bindingExt{Delivery: map[string]string{"doc": deliveryStdin}}
	big := strings.Repeat("x", maxDeliveryBytes+1)
	_, _, cleanup, err := applyDelivery(map[string]any{"doc": big}, ext)
	defer cleanup()
	if err == nil || !strings.Contains(err.Error(), "delivery cap") {
		t.Fatalf("expected the delivery-cap error, got %v", err)
	}
}

func TestIntegration_StdinDeliveryNoSlot(t *testing.T) {
	// A stdin-routed field that maps to no flag or arg is consumed by
	// delivery itself: bytes to stdin, nothing on argv (the pbcopy class).
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source:      openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:         "drink",
		Binding:     bindingWithExt(t, "drink", `{"delivery":{"doc":"stdin"},"stdout":"json"}`),
		InputSchema: objSchema,
	}, map[string]any{"doc": map[string]any{"a": float64(1)}})
	if ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Message)
	}
	m := out.(map[string]any)
	if m["stdin"] != `{"a":1}` {
		t.Errorf("stdin = %q, want the document JSON", m["stdin"])
	}
	if args, _ := m["args"].([]any); len(args) != 0 {
		t.Errorf("argv = %v, want no tokens for a slotless stdin field", args)
	}
}

func TestIntegration_ExitOKClassification(t *testing.T) {
	invoker := NewInvoker()

	// diff(1)-style: exit 1 declared as success completes with output and
	// the real code in the trailer.
	out, trailer, ierr := invokeUsageWithTrailer(t, invoker, &openbindings.BindingInvocationArgs{
		Source:      openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:         "fail",
		Binding:     bindingWithExt(t, "fail", `{"exit":{"ok":[0,1]}}`),
		InputSchema: objSchema,
	}, map[string]any{"message": []any{"differences found"}})
	if ierr != nil {
		t.Fatalf("declared-ok exit 1 must succeed, got %s: %s", ierr.Code, ierr.Message)
	}
	if m, _ := out.(map[string]any); m == nil || m["stdout"] != "" {
		t.Errorf("output = %#v, want the empty-stdout wrap", out)
	}
	if got := trailer["x-exit-code"]; len(got) != 1 || got[0] != "1" {
		t.Errorf(`x-exit-code = %q, want ["1"]`, got)
	}

	// Undeclared exit 1 remains a terminal error.
	_, ierr = invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source:      openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:         "fail",
		Binding:     bindingWithExt(t, "fail", `{}`),
		InputSchema: objSchema,
	}, map[string]any{"message": []any{"boom"}})
	if ierr == nil || ierr.Code != openbindings.ErrCodeExecutionFailed {
		t.Fatalf("undeclared exit 1 must fail, got %v", ierr)
	}
}

// TestIntegration_OperationInvoker_XUsage proves the member flows from a real
// interface document through the SDK's operation layer: the OperationInvoker
// selects the binding (extensions intact, per lossless unmarshaling) and the
// usage invoker routes delivery off it.
func TestIntegration_OperationInvoker_XUsage(t *testing.T) {
	doc := `{
		"openbindings": "0.2.0",
		"name": "testcli",
		"operations": {
			"slurp": {
				"input": {"type": "object", "properties": {"doc": {"type": "object"}, "tag": {"type": "string"}}},
				"output": {"type": "object", "properties": {"stdin": {"type": "string"}, "args": {"type": "array", "items": {"type": "string"}}}}
			}
		},
		"sources": {"cli": {"format": "` + FormatToken + `", "content": ""}},
		"bindings": {
			"slurp.cli": {
				"operation": "slurp",
				"source": "cli",
				"ref": "slurp",
				"x-usage": {"delivery": {"doc": "stdin"}, "stdout": "json"}
			}
		}
	}`
	var iface openbindings.Interface
	if err := json.Unmarshal([]byte(doc), &iface); err != nil {
		t.Fatalf("unmarshal interface: %v", err)
	}
	// The source content can't be baked into the JSON literal (the binary
	// path is only known at run time), so it's set here.
	src := iface.Sources["cli"]
	src.Content = testSpec()
	iface.Sources["cli"] = src

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	invoker := openbindings.NewOperationInvoker(NewInvoker())
	sig := openbindings.NewOperationSignature[any, any]("slurp")
	call := openbindings.Invoke(ctx, invoker, &iface, sig)
	if err := call.Write(ctx, map[string]any{"doc": map[string]any{"a": float64(1)}, "tag": "blue"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = call.Close()

	outs := call.Outputs()
	v, err := outs.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m, _ := v.(map[string]any)
	if m == nil || m["stdin"] != `{"a":1}` {
		t.Fatalf("output = %#v, want the document on stdin", v)
	}
	if _, err := outs.Read(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}
