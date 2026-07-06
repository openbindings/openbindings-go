package usage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// parseTestWrapper parses a doc map and fails the test on error.
func parseTestWrapper(t *testing.T, doc map[string]any) *Wrapper {
	t.Helper()
	w, err := ParseWrapper(doc)
	if err != nil {
		t.Fatalf("parse wrapper: %v", err)
	}
	return w
}

func TestParseWrapper_FailClosed(t *testing.T) {
	base := func() map[string]any { return testDoc(map[string]any{"echo": unitDef("echo", nil)}) }

	t.Run("unknown document member refuses", func(t *testing.T) {
		doc := base()
		doc["shiny"] = true
		if _, err := ParseWrapper(doc); err == nil {
			t.Fatal("expected refusal of unknown non-x- document member")
		}
	})
	t.Run("x- members are ignored at every level", func(t *testing.T) {
		doc := base()
		doc["x-ob"] = map[string]any{"note": "bookkeeping"}
		units := doc["units"].(map[string]any)
		unit := units["echo"].(map[string]any)
		unit["x-annotation"] = "fine"
		if _, err := ParseWrapper(doc); err != nil {
			t.Fatalf("x- members must be ignored, got %v", err)
		}
	})
	t.Run("description allowed on document and unit", func(t *testing.T) {
		doc := base()
		doc["description"] = "a wrapper"
		doc["units"].(map[string]any)["echo"].(map[string]any)["description"] = "echoes"
		if _, err := ParseWrapper(doc); err != nil {
			t.Fatalf("description must be allowed, got %v", err)
		}
	})
	t.Run("unknown unit member refuses", func(t *testing.T) {
		doc := base()
		doc["units"].(map[string]any)["echo"].(map[string]any)["delivry"] = map[string]any{}
		if _, err := ParseWrapper(doc); err == nil {
			t.Fatal("expected refusal of unknown unit member")
		}
	})
	t.Run("missing unit version refuses", func(t *testing.T) {
		doc := base()
		delete(doc["units"].(map[string]any)["echo"].(map[string]any), unitVersionKey)
		if _, err := ParseWrapper(doc); err == nil {
			t.Fatal("expected refusal of a version-less unit")
		}
	})
	t.Run("higher unit minor refuses", func(t *testing.T) {
		doc := base()
		doc["units"].(map[string]any)["echo"].(map[string]any)[unitVersionKey] = "0.2.0"
		if _, err := ParseWrapper(doc); err == nil {
			t.Fatal("expected refusal of a higher-minor unit version")
		}
	})
	t.Run("two stdin modes refuse", func(t *testing.T) {
		doc := base()
		doc["units"].(map[string]any)["echo"].(map[string]any)["delivery"] = map[string]any{
			"a": "stdin-dash", "b": "stdin",
		}
		if _, err := ParseWrapper(doc); err == nil {
			t.Fatal("expected the one-stdin rule to refuse")
		}
	})
	t.Run("unknown delivery mode refuses", func(t *testing.T) {
		doc := base()
		doc["units"].(map[string]any)["echo"].(map[string]any)["delivery"] = map[string]any{"a": "carrier-pigeon"}
		if _, err := ParseWrapper(doc); err == nil {
			t.Fatal("expected refusal of an unknown delivery mode")
		}
	})
}

func TestParseWrapper_ExitEdges(t *testing.T) {
	mk := func(exit map[string]any, stdout string) error {
		extra := map[string]any{"exit": exit}
		if stdout != "" {
			extra["stdout"] = stdout
		}
		_, err := ParseWrapper(testDoc(map[string]any{"fail": unitDef("fail", extra)}))
		return err
	}

	if err := mk(map[string]any{"ok": []any{0, 1}}, ""); err != nil {
		t.Fatalf("valid ok list refused: %v", err)
	}
	if err := mk(map[string]any{}, ""); err == nil {
		t.Fatal("exit with missing ok must be rejected fail-closed")
	}
	if err := mk(map[string]any{"ok": []any{}}, ""); err == nil {
		t.Fatal("empty ok list must be rejected fail-closed")
	}
	if err := mk(map[string]any{"ok": []any{256}}, ""); err == nil {
		t.Fatal("ok code out of 0-255 must be rejected")
	}
	if err := mk(map[string]any{"ok": []any{0, 1}, "values": map[string]any{"1": false}}, ""); err == nil {
		t.Fatal("values without stdout none must be rejected")
	}
	if err := mk(map[string]any{"ok": []any{0}, "values": map[string]any{"1": false}}, "none"); err == nil {
		t.Fatal("values key outside ok must be rejected")
	}
	if err := mk(map[string]any{"ok": []any{0, 1}, "values": map[string]any{"0": true, "1": false}}, "none"); err != nil {
		t.Fatalf("valid values map refused: %v", err)
	}
}

func TestValidateUnit_SlotRules(t *testing.T) {
	invoke := func(t *testing.T, units map[string]any, ref string, input any) *openbindings.InvocationError {
		t.Helper()
		invoker := NewInvoker()
		_, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
			Source: openbindings.InvocationSource{Format: WrapperToken, Content: testDoc(units)},
			Ref:    ref,
		}, input)
		return ierr
	}

	t.Run("stdin-dash requires a slot", func(t *testing.T) {
		units := map[string]any{"drink": unitDef("drink", map[string]any{
			"delivery": map[string]any{"doc": "stdin-dash"}, "stdout": "json",
		})}
		ierr := invoke(t, units, "#/units/drink", map[string]any{"doc": "x"})
		if ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed {
			t.Fatalf("expected validation failure for slotless stdin-dash, got %v", ierr)
		}
	})
	t.Run("slotless stdin refuses a slotted field", func(t *testing.T) {
		units := map[string]any{"slurp": unitDef("slurp", map[string]any{
			"delivery": map[string]any{"doc": "stdin"}, "stdout": "json",
		})}
		ierr := invoke(t, units, "#/units/slurp", map[string]any{"doc": "x"})
		if ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed {
			t.Fatalf("expected validation failure for slotted field under slotless stdin, got %v", ierr)
		}
	})
	t.Run("delivery key naming a boolean flag refuses", func(t *testing.T) {
		units := map[string]any{"slurp": unitDef("slurp", map[string]any{
			// slurp's --tag takes a value; use a made-up boolean via mixed?
			// mixed has no flags; point a file key at slurp's tag is valid.
			// Use prose (no flags/args): any key is slotless.
			"delivery": map[string]any{"tag": "file"}, "stdout": "json",
		})}
		// tag names a value-taking flag on slurp — this one is VALID.
		if ierr := invoke(t, units, "#/units/slurp", nil); ierr != nil {
			t.Fatalf("file delivery to a value-taking flag must validate, got %v", ierr)
		}
	})
	t.Run("per-unit granularity: one bad unit does not brick its siblings", func(t *testing.T) {
		units := map[string]any{
			"good": unitDef("echo", nil),
			"bad":  unitDef("no such command", nil),
		}
		if ierr := invoke(t, units, "#/units/good", map[string]any{"words": []any{"hi"}}); ierr != nil {
			t.Fatalf("valid unit must stay invocable beside an invalid one, got %v", ierr)
		}
		ierr := invoke(t, units, "#/units/bad", nil)
		if ierr == nil || ierr.Code != openbindings.ErrCodeValidationFailed {
			t.Fatalf("invalid unit must fail validation, got %v", ierr)
		}
	})
}

func TestWrapper_SpecChecks(t *testing.T) {
	t.Run("spec.format outside the accepted range refuses", func(t *testing.T) {
		doc := testDoc(map[string]any{"echo": unitDef("echo", nil)})
		doc["spec"].(map[string]any)["format"] = "usage@3.0.0"
		invoker := NewInvoker()
		_, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
			Source: openbindings.InvocationSource{Format: WrapperToken, Content: doc},
			Ref:    "#/units/echo",
		}, nil)
		if ierr == nil {
			t.Fatal("expected refusal of an out-of-range artifact version")
		}
	})
	t.Run("location-only requires and verifies hash", func(t *testing.T) {
		dir := t.TempDir()
		kdlPath := filepath.Join(dir, "cli.usage.kdl")
		kdl := testSpecKDL()
		if err := os.WriteFile(kdlPath, []byte(kdl), 0o600); err != nil {
			t.Fatal(err)
		}
		mkDoc := func(hash string) map[string]any {
			spec := map[string]any{"format": "usage@" + MaxTestedVersion, "location": kdlPath}
			if hash != "" {
				spec["hash"] = hash
			}
			return map[string]any{"spec": spec, "units": map[string]any{"echo": unitDef("echo", nil)}}
		}
		invoker := NewInvoker()
		run := func(doc map[string]any) *openbindings.InvocationError {
			_, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
				Source: openbindings.InvocationSource{Format: WrapperToken, Content: doc},
				Ref:    "#/units/echo",
			}, map[string]any{"words": []any{"hi"}})
			return ierr
		}
		if ierr := run(mkDoc("")); ierr == nil {
			t.Fatal("location-only without hash must refuse")
		}
		if ierr := run(mkDoc("sha256:deadbeef")); ierr == nil {
			t.Fatal("hash mismatch must refuse (fail-closed against drift)")
		}
		if ierr := run(mkDoc(ArtifactHash(kdl))); ierr != nil {
			t.Fatalf("correct hash must load: %v", ierr)
		}
	})
	t.Run("content-mode hash is provenance, never a refusal", func(t *testing.T) {
		doc := testDoc(map[string]any{"echo": unitDef("echo", nil)})
		doc["spec"].(map[string]any)["hash"] = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		invoker := NewInvoker()
		_, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
			Source: openbindings.InvocationSource{Format: WrapperToken, Content: doc},
			Ref:    "#/units/echo",
		}, map[string]any{"words": []any{"hi"}})
		if ierr != nil {
			t.Fatalf("content is authoritative; a stale provenance hash must not refuse: %v", ierr)
		}
	})
}

func TestApplyDelivery(t *testing.T) {
	t.Run("stdin-dash substitutes dash and carries bytes", func(t *testing.T) {
		u := &Unit{Delivery: map[string]string{"doc": deliveryStdinDash}}
		out, stdin, cleanup, err := applyDelivery(map[string]any{"doc": map[string]any{"a": float64(1)}, "tag": "x"}, u)
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
	t.Run("slotless stdin removes the field", func(t *testing.T) {
		u := &Unit{Delivery: map[string]string{"doc": deliveryStdin}}
		out, stdin, cleanup, err := applyDelivery(map[string]any{"doc": "raw text\n"}, u)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if string(stdin) != "raw text\n" {
			t.Fatalf("stdin bytes = %q, want the raw string", stdin)
		}
		if m := out.(map[string]any); len(m) != 0 {
			t.Fatalf("slotless field must vanish from the input, got %#v", m)
		}
	})
	t.Run("file field materializes and cleans up", func(t *testing.T) {
		u := &Unit{Delivery: map[string]string{"doc": deliveryFile}}
		out, stdin, cleanup, err := applyDelivery(map[string]any{"doc": map[string]any{"a": float64(1)}}, u)
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
	t.Run("raw string file gets no json suffix, even when named like one", func(t *testing.T) {
		u := &Unit{Delivery: map[string]string{"payload.json": deliveryFile}}
		out, _, cleanup, err := applyDelivery(map[string]any{"payload.json": "text"}, u)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		path, _ := out.(map[string]any)["payload.json"].(string)
		if strings.HasSuffix(path, ".json") {
			t.Fatalf("raw-string file must not end in .json, got %q", path)
		}
	})
	t.Run("null routed field is absent", func(t *testing.T) {
		u := &Unit{Delivery: map[string]string{"doc": deliveryStdinDash}}
		out, stdin, cleanup, err := applyDelivery(map[string]any{"doc": nil, "tag": "x"}, u)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		if stdin != nil {
			t.Fatalf("null must route nowhere, got %q", stdin)
		}
		if m := out.(map[string]any); len(m) != 1 || m["tag"] != "x" {
			t.Fatalf("null routed field must vanish, got %#v", m)
		}
	})
	t.Run("cap enforced", func(t *testing.T) {
		u := &Unit{Delivery: map[string]string{"doc": deliveryStdinDash}}
		big := strings.Repeat("x", maxDeliveryBytes+1)
		_, _, cleanup, err := applyDelivery(map[string]any{"doc": big}, u)
		defer cleanup()
		if err == nil || !strings.Contains(err.Error(), "delivery cap") {
			t.Fatalf("expected the delivery-cap error, got %v", err)
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
		{"none is null", "ignored\n", stdoutNone, nil, false},
		{"text strips trailing newlines", "all good\n", stdoutText, "all good", false},
		{"text preserves interior newlines", "a\nb\n\n", stdoutText, "a\nb", false},
		{"absent mode is text", "42\n", "", "42", false},
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

func TestTailBuffer(t *testing.T) {
	tb := &tailBuffer{limit: 8}
	_, _ = tb.Write([]byte("0123456789"))
	if tb.String() != "23456789" || !tb.truncated {
		t.Fatalf("tail = %q truncated=%v, want last 8 bytes", tb.String(), tb.truncated)
	}
	_, _ = tb.Write([]byte("AB"))
	if tb.String() != "456789AB" {
		t.Fatalf("tail = %q, want rolling last 8", tb.String())
	}
}

// ---------------------------------------------------------------------------
// Fixture-CLI integration: delivery, stdout, and exit end to end
// ---------------------------------------------------------------------------

func TestIntegration_StdinDashDelivery(t *testing.T) {
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "#/units/slurp",
	}, map[string]any{"doc": map[string]any{"a": float64(1)}, "tag": "blue"})
	if ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Message)
	}
	m := out.(map[string]any)
	if m["stdin"] != `{"a":1}` {
		t.Errorf("stdin = %q, want the document JSON", m["stdin"])
	}
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
		Source: testSource(),
		Ref:    "#/units/readfile",
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
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("temp file survived the invocation: %v", err)
	}
}

func TestIntegration_SlotlessStdin(t *testing.T) {
	// drink has no flags or args: the routed field is consumed by delivery
	// (bytes to stdin, nothing on argv — the pbcopy class).
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "#/units/drink",
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

func TestIntegration_StdoutModes(t *testing.T) {
	invoker := NewInvoker()

	// A bare number parses on a declared JSON lane...
	units := testUnits()
	units["num"] = unitDef("num", map[string]any{"stdout": "json"})
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: WrapperToken, Content: testDoc(units)},
		Ref:    "#/units/num",
	}, nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %s", ierr.Message)
	}
	if out != float64(42) {
		t.Errorf("output = %#v, want 42", out)
	}

	// ...and is the string "42" on the default text lane (no heuristic).
	out, ierr = invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "#/units/num",
	}, nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %s", ierr.Message)
	}
	if out != "42" {
		t.Errorf("output = %#v, want the string \"42\"", out)
	}

	// Prose on a declared JSON lane is a terminal error, never a wrap.
	units = testUnits()
	units["prose"] = unitDef("prose", map[string]any{"stdout": "json"})
	_, ierr = invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: WrapperToken, Content: testDoc(units)},
		Ref:    "#/units/prose",
	}, nil)
	if ierr == nil || ierr.Code != openbindings.ErrCodeExecutionFailed {
		t.Fatalf("expected ERR_EXECUTION_FAILED for prose on a JSON lane, got %v", ierr)
	}

	// Text mode strips the trailing newline and carries stderr as a trailer.
	out, trailer, terr := invokeUsageWithTrailer(t, invoker, &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "#/units/prose",
	}, nil)
	if terr != nil {
		t.Fatalf("unexpected error: %s", terr.Message)
	}
	if out != "all good" {
		t.Errorf("output = %#v, want stdout with the trailing newline stripped", out)
	}
	if got := trailer["x-stderr"]; len(got) != 1 || got[0] != "checked 3 things\n" {
		t.Errorf("x-stderr trailer = %q", got)
	}
}

func TestIntegration_ExitClassification(t *testing.T) {
	invoker := NewInvoker()

	// diff(1)-style: exit 1 declared ok completes with output and the real
	// code in the trailer.
	units := testUnits()
	units["fail"] = unitDef("fail", map[string]any{"exit": map[string]any{"ok": []any{0, 1}}})
	out, trailer, ierr := invokeUsageWithTrailer(t, invoker, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: WrapperToken, Content: testDoc(units)},
		Ref:    "#/units/fail",
	}, map[string]any{"message": []any{"differences found"}})
	if ierr != nil {
		t.Fatalf("declared-ok exit 1 must succeed, got %s: %s", ierr.Code, ierr.Message)
	}
	if out != "" {
		t.Errorf("output = %#v, want the empty text value", out)
	}
	if got := trailer["x-exit-code"]; len(got) != 1 || got[0] != "1" {
		t.Errorf(`x-exit-code = %q, want ["1"]`, got)
	}

	// Undeclared exit 1 remains a terminal error.
	_, ierr = invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "#/units/fail",
	}, map[string]any{"message": []any{"boom"}})
	if ierr == nil || ierr.Code != openbindings.ErrCodeExecutionFailed {
		t.Fatalf("undeclared exit 1 must fail, got %v", ierr)
	}
}

func TestIntegration_ExitValues(t *testing.T) {
	// The grep -q class: the exit code IS the result; values maps it to a
	// literal output under stdout "none".
	invoker := NewInvoker()
	units := testUnits()
	units["fail"] = unitDef("fail", map[string]any{
		"stdout": "none",
		"exit":   map[string]any{"ok": []any{0, 1}, "values": map[string]any{"0": true, "1": false}},
	})
	src := openbindings.InvocationSource{Format: WrapperToken, Content: testDoc(units)}

	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{Source: src, Ref: "#/units/fail"},
		map[string]any{"message": []any{"nope"}})
	if ierr != nil {
		t.Fatalf("values-mapped exit must succeed, got %v", ierr)
	}
	if out != false {
		t.Errorf("output = %#v, want false (the exit-1 literal)", out)
	}
}

// TestIntegration_OperationInvoker_Wrapper proves the recipe flows from a
// real interface document through the SDK's operation layer: {source, ref}
// alone carries everything (the carriage argument made executable).
func TestIntegration_OperationInvoker_Wrapper(t *testing.T) {
	iface := &openbindings.Interface{
		OpenBindings: openbindings.MaxTestedVersion,
		Name:         "testcli",
		Operations: map[string]openbindings.Operation{
			"slurp": {
				Input:  openbindings.JSONSchema{"type": "object", "properties": map[string]any{"doc": map[string]any{"type": "object"}, "tag": map[string]any{"type": "string"}}},
				Output: openbindings.JSONSchema{"type": "object", "properties": map[string]any{"stdin": map[string]any{"type": "string"}, "args": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}},
			},
		},
		Sources: map[string]openbindings.Source{
			"cli": {Format: WrapperToken, Content: testDoc(testUnits())},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"slurp.cli": {Operation: "slurp", Source: "cli", Ref: "#/units/slurp"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	invoker := openbindings.NewOperationInvoker(NewInvoker())
	sig := openbindings.NewOperationSignature[any, any]("slurp")
	call := openbindings.Invoke(ctx, invoker, iface, sig)
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
