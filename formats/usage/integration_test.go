package usage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

// testBinary is the path to the compiled test CLI binary.
// Built once in TestMain, used by all integration tests.
var testBinary string

func TestMain(m *testing.M) {
	// Build the test CLI binary.
	tmp, err := os.MkdirTemp("", "usage-go-test-*")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)

	bin := filepath.Join(tmp, "testcli")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/testcli")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		panic("failed to build testcli: " + err.Error())
	}
	testBinary = bin

	os.Exit(m.Run())
}

// testSpecKDL returns the usage-spec KDL text for the test CLI binary — the
// pristine artifact the test wrappers embed.
func testSpecKDL() string {
	return `bin "` + testBinary + `"
cmd "json" {
    help "Output JSON"
    arg "<pairs>..." help="key=value pairs"
}
cmd "fail" {
    help "Exit with error"
    arg "[message]..." help="Error message for stderr"
}
cmd "mixed" {
    help "Write to stdout and stderr"
}
cmd "echo" {
    help "Echo args"
    arg "<words>..." help="Words to echo"
}
cmd "slurp" {
    help "Echo stdin and args as JSON"
    flag "--tag <value>" help="An argv-delivered field"
    arg "<doc>" help="Document locator"
}
cmd "readfile" {
    help "Emit a file's path and content as JSON"
    arg "<doc>" help="File path"
}
cmd "statfile" {
    help "Emit a file's path, content, and mode as JSON"
    arg "<doc>" help="File path"
}
cmd "drink" {
    help "Read stdin, no args"
}
cmd "num" {
    help "Print a bare number"
}
cmd "prose" {
    help "Print human text on stdout, an aside on stderr"
}
`
}

// testSource is the bare-kdl fixture source: the artifact IS the source.
func testSource() openbindings.InvocationSource {
	return openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(testSpecKDL())}
}

// driver abstracts the two entry points tests exercise: the format
// invoker directly (builtins only) and an OperationInvoker wrapping it
// (the embedder pattern — invoker-level hooks reach the binding-layer
// path via fillBindingArgs).
type driver interface {
	InvokeBinding(ctx context.Context, args *openbindings.BindingInvocationArgs) openbindings.Invocation[any, any]
}

// hooked wraps the fixture invoker with invoker-level hooks — the
// consumer configuration that replaced wrapper-unit elections.
func hooked(configure func(op *openbindings.OperationInvoker)) driver {
	op := openbindings.NewOperationInvoker(NewInvoker())
	if configure != nil {
		configure(op)
	}
	return op
}

// jsonHooked is the machine-lane consumer: decode stdout as strict JSON.
func jsonHooked() driver {
	return hooked(func(op *openbindings.OperationInvoker) {
		op.OutputDecoder = func(_ openbindings.InvokeSite, raw openbindings.RawResult) (any, error) {
			var v any
			if len(raw.Body) == 0 {
				return nil, nil
			}
			if err := json.Unmarshal(raw.Body, &v); err != nil {
				return nil, err
			}
			return v, nil
		}
	})
}

// invokeUsage drives a usage invocation to completion: writes input (when
// non-nil), closes the input side, and returns the single output or the
// terminal error. The binding is unary, so exactly one output (or one
// terminal) is expected.
func invokeUsage(t *testing.T, invoker driver, args *openbindings.BindingInvocationArgs, input any) (any, *openbindings.InvocationError) {
	t.Helper()
	out, _, ierr := invokeUsageWithTrailer(t, invoker, args, input)
	return out, ierr
}

// invokeUsageWithTrailer is invokeUsage plus the invocation's trailing
// metadata (x-exit-code, x-stderr), valid once the handle terminates.
func invokeUsageWithTrailer(t *testing.T, invoker driver, args *openbindings.BindingInvocationArgs, input any) (any, openbindings.Metadata, *openbindings.InvocationError) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	call := invoker.InvokeBinding(ctx, args)
	if input != nil {
		if err := call.Write(ctx, input); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	_ = call.Close()

	out := call.Outputs()
	v, err := out.Read(ctx)
	if errors.Is(err, io.EOF) {
		return nil, call.Trailer(), nil // clean close with no output
	}
	if err != nil {
		var ie *openbindings.InvocationError
		if !errors.As(err, &ie) {
			t.Fatalf("expected *InvocationError, got %T: %v", err, err)
		}
		return nil, call.Trailer(), ie
	}
	if _, err2 := out.Read(ctx); !errors.Is(err2, io.EOF) {
		t.Fatalf("expected a single output then io.EOF, got %v", err2)
	}
	return v, call.Trailer(), nil
}

func TestIntegration_JSONOutput(t *testing.T) {
	out, ierr := invokeUsage(t, jsonHooked(), &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "json",
	}, map[string]any{"pairs": []any{"name=alice", "role=admin"}})
	if ierr != nil {
		t.Fatalf("unexpected error: %s: %s", ierr.Code, ierr.Message)
	}

	result, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected parsed JSON map, got %T: %v", out, out)
	}
	if result["name"] != "alice" {
		t.Errorf("name = %v, want alice", result["name"])
	}
	if result["role"] != "admin" {
		t.Errorf("role = %v, want admin", result["role"])
	}
}

func TestIntegration_NonZeroExitCode(t *testing.T) {
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "fail",
	}, map[string]any{"message": []any{"something went wrong"}})

	// A non-ok exit is a terminal error carrying the exit code and the
	// captured output (including stderr) in Details.
	if out != nil {
		t.Fatalf("expected no output on non-ok exit, got %v", out)
	}
	if ierr == nil || ierr.Code != openbindings.ErrCodeExecutionFailed {
		t.Fatalf("expected ERR_EXECUTION_FAILED, got %v", ierr)
	}
	details, ok := ierr.Details.(map[string]any)
	if !ok {
		t.Fatalf("expected map details, got %T", ierr.Details)
	}
	if details["exitCode"] != 1 {
		t.Errorf("exitCode = %v, want 1", details["exitCode"])
	}
	output, ok := details["output"].(map[string]any)
	if !ok {
		t.Fatalf("expected map output in details, got %T", details["output"])
	}
	if stderr, _ := output["stderr"].(string); stderr == "" {
		t.Error("expected non-empty stderr from failed command")
	}
}

func TestIntegration_MixedOutput(t *testing.T) {
	// Default lane is text (a declaration, not a guess): the output value is
	// stdout with the trailing newline stripped; stderr is diagnostics and
	// rides the x-stderr trailer, never the output value.
	invoker := NewInvoker()
	out, trailer, ierr := invokeUsageWithTrailer(t, invoker, &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "mixed",
	}, nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %s", ierr.Message)
	}
	if out != "stdout line" {
		t.Errorf("output = %#v, want the text-mode string", out)
	}
	if got := trailer["x-stderr"]; len(got) != 1 || got[0] != "stderr line\n" {
		t.Errorf("x-stderr trailer = %q, want [%q]", got, "stderr line\n")
	}
}

func TestIntegration_EchoCommand(t *testing.T) {
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: testSource(),
		Ref:    "echo",
	}, map[string]any{"words": []any{"hello", "world"}})
	if ierr != nil {
		t.Fatalf("error: %s: %s", ierr.Code, ierr.Message)
	}
	if out != "hello world" {
		t.Errorf("output = %#v, want %q", out, "hello world")
	}
}

func TestIntegration_SynthesizeInterface(t *testing.T) {
	spec := `
name "mycli"
version "1.0.0"
about "A test CLI"
bin "mycli"
cmd "greet" {
    help "Say hello"
    flag "--name <value>" help="Who to greet"
    arg "<message>" help="What to say"
}
cmd "config" subcommand_required=#true {
    cmd "get" {
        help "Get a config value"
        arg "<key>" help="Config key"
    }
    cmd "set" {
        help "Set a config value"
        arg "<key>" help="Config key"
        arg "<value>" help="Config value"
    }
}
`
	synthesizer := NewSynthesizer()
	iface, err := synthesizer.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{{
			BindingSpec: BindingSpec,
			Content:     openbindings.TextContent(spec),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if iface.Name != "mycli" {
		t.Errorf("name = %q, want mycli", iface.Name)
	}
	if iface.Version != "1.0.0" {
		t.Errorf("version = %q, want 1.0.0", iface.Version)
	}

	if len(iface.Operations) != 4 {
		t.Fatalf("expected root plus 3 operations, got %d: %v", len(iface.Operations), mapKeys(iface.Operations))
	}
	if _, ok := iface.Operations["mycli"]; !ok {
		t.Error("expected root operation 'mycli'")
	}
	if _, ok := iface.Operations["greet"]; !ok {
		t.Error("expected operation 'greet'")
	}
	if _, ok := iface.Operations["config.get"]; !ok {
		t.Error("expected operation 'config.get'")
	}
	if _, ok := iface.Operations["config.set"]; !ok {
		t.Error("expected operation 'config.set'")
	}

	greetOp := iface.Operations["greet"]
	if greetOp.Input == nil {
		t.Fatal("expected greet input schema")
	}
	props := greetOp.Input.(map[string]any)["properties"].(map[string]any)
	if _, ok := props["name"]; !ok {
		t.Error("expected 'name' flag in greet input")
	}
	if _, ok := props["message"]; !ok {
		t.Error("expected 'message' arg in greet input")
	}

	// The emitted source is the PRISTINE bare artifact; bindings carry
	// command-path refs (the format's own grammar).
	src := iface.Sources[DefaultSourceName]
	if src.BindingSpec != BindingSpec {
		t.Errorf("source format = %q, want the bare usage token", src.BindingSpec)
	}
	if string(src.Content) != string(openbindings.TextContent(spec)) {
		t.Fatal("expected the pristine kdl text as embedded content")
	}
	binding := iface.Bindings["config.get."+DefaultSourceName]
	if binding.Ref != "config get" {
		t.Errorf("config.get ref = %q, want 'config get'", binding.Ref)
	}

	// FLOOR-TRUE derived outputs: {"type":"string"} with the in-schema
	// x-ob floor-stamp the diagnostics key on.
	out, _ := iface.Operations["config.get"].Output.(map[string]any)
	if out == nil || out["type"] != "string" {
		t.Fatalf("expected floor-true string output schema, got %v", out)
	}
	if xob, _ := out["x-ob"].(map[string]any); xob == nil || xob["floor"] != "text" {
		t.Fatalf("expected the x-ob floor-stamp, got %v", out["x-ob"])
	}

	// Synthesize-then-invoke coherence: the emitted ref resolves in the
	// emitted artifact.
	if _, err := findCommand(mustParse(t, spec), binding.Ref); err != nil {
		t.Fatalf("emitted binding ref does not resolve: %v", err)
	}
}

func TestIntegration_AliasOnParentCommand(t *testing.T) {
	// Verify that aliases on parent commands are matched when resolving
	// unit command paths.
	spec := `
bin "testcli"
cmd "configuration" {
    alias "config" "cfg"
    cmd "set" {
        help "Set a config value"
        arg "<key>" help="Config key"
        arg "<value>" help="Config value"
    }
}
`
	// "config set" should match "configuration set" via the alias on the parent.
	result, err := findCommand(mustParse(t, spec), "config set")
	if err != nil {
		t.Fatalf("findCommand(config set): %v", err)
	}
	if result.cmd.Name != "set" {
		t.Errorf("cmd name = %q, want set", result.cmd.Name)
	}
	// The ref spelling is also the argv spelling; an artifact-declared alias
	// is preserved rather than normalized to the primary command name.
	if result.path[0] != "config" {
		t.Errorf("path[0] = %q, want supplied alias config", result.path[0])
	}

	// "cfg set" should also work.
	result2, err := findCommand(mustParse(t, spec), "cfg set")
	if err != nil {
		t.Fatalf("findCommand(cfg set): %v", err)
	}
	if result2.cmd.Name != "set" {
		t.Errorf("cmd name = %q, want set", result2.cmd.Name)
	}
	if result2.path[0] != "cfg" {
		t.Errorf("path[0] = %q, want supplied alias cfg", result2.path[0])
	}
}

func TestIntegration_RootCommand(t *testing.T) {
	// A unit with an empty command targets the root invocation.
	rootKDL := `bin "` + testBinary + `"
flag "-v --verbose" help="Verbose output"
arg "<words>..." help="Words to echo"
`
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{BindingSpec: BindingSpec, Content: openbindings.TextContent(rootKDL)},
		Ref:    "",
	}, map[string]any{"words": []any{"hello", "world"}})
	if ierr != nil {
		t.Fatalf("error: %s: %s", ierr.Code, ierr.Message)
	}
	if out != "hello world" {
		t.Errorf("output = %#v, want %q", out, "hello world")
	}
}

func TestIntegration_InvalidRef(t *testing.T) {
	invoker := NewInvoker()
	for _, ref := range []string{"nonexistent", "no such command", "json bogus"} {
		out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
			Source: testSource(),
			Ref:    ref,
		}, nil)
		if out != nil {
			t.Fatalf("ref %q: expected no output, got %v", ref, out)
		}
		if ierr == nil || ierr.Code != openbindings.ErrCodeRefNotFound {
			t.Fatalf("ref %q: expected ERR_REF_NOT_FOUND, got %v", ref, ierr)
		}
	}
}

// TestIntegration_NoInputOperationConvention verifies the operation-layer
// no-input convention: a binding carrying Binding != nil and InputSchema ==
// nil runs the bare command without waiting for (or rejecting) a write.
func TestIntegration_NoInputOperationConvention(t *testing.T) {
	invoker := NewInvoker()
	ctx := context.Background()
	call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:  testSource(),
		Ref:     "mixed",
		Binding: &openbindings.BindingEntry{Operation: "mixed", Source: "s", Ref: "mixed"},
		// InputSchema nil → no-input operation; the binding closes input itself.
	})
	// The caller writes nothing and does not close; the binding must still run.
	out, err := openbindings.Single(ctx, call.Outputs())
	if err != nil {
		t.Fatalf("no-input convention failed: %v", err)
	}
	if out != "stdout line" {
		t.Fatalf("expected text output, got %#v", out)
	}
}

func TestIntegration_Cancellation(t *testing.T) {
	// A cancelled context tears down the running process; the handle
	// terminates with ERR_CANCELLED. Uses the direct-binary metadata path to
	// run `sleep 10` (no wrapper is loaded on that path).
	invoker := NewInvoker()
	ctx, cancel := context.WithCancel(context.Background())
	call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{BindingSpec: BindingSpec},
		Ref:     "10",
		Context: map[string]any{"metadata": map[string]any{"binary": "sleep"}},
	})
	_ = call.Close()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := call.Outputs().Read(context.Background())
	var ie *openbindings.InvocationError
	if !errors.As(err, &ie) || ie.Code != openbindings.ErrCodeCancelled {
		t.Fatalf("expected ERR_CANCELLED, got %v", err)
	}
}

func mapKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
