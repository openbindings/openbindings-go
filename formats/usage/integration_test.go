package usage

import (
	"context"
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

// testSpec returns a usage-spec KDL string for the test CLI binary.
func testSpec() string {
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
`
}

// invokeUsage drives a usage invocation to completion: writes input (when
// non-nil), closes the input side, and returns the single output or the
// terminal error. The binding is unary, so exactly one output (or one
// terminal) is expected.
func invokeUsage(t *testing.T, invoker *Invoker, args *openbindings.BindingInvocationArgs, input any) (any, *openbindings.InvocationError) {
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
		return nil, nil // clean close with no output
	}
	if err != nil {
		var ie *openbindings.InvocationError
		if !errors.As(err, &ie) {
			t.Fatalf("expected *InvocationError, got %T: %v", err, err)
		}
		return nil, ie
	}
	if _, err2 := out.Read(ctx); !errors.Is(err2, io.EOF) {
		t.Fatalf("expected a single output then io.EOF, got %v", err2)
	}
	return v, nil
}

func TestIntegration_JSONOutput(t *testing.T) {
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
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
		Source: openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:    "fail",
	}, map[string]any{"message": []any{"something went wrong"}})

	// A non-zero exit is a terminal error carrying the exit code and the
	// captured output (including stderr) in Details.
	if out != nil {
		t.Fatalf("expected no output on non-zero exit, got %v", out)
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
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:    "mixed",
	}, nil)
	if ierr != nil {
		t.Fatalf("unexpected error: %s", ierr.Message)
	}

	output := out.(map[string]any)
	stdout, _ := output["stdout"].(string)
	stderr, _ := output["stderr"].(string)
	if stdout == "" {
		t.Error("expected non-empty stdout")
	}
	if stderr == "" {
		t.Error("expected non-empty stderr")
	}
}

func TestIntegration_EchoCommand(t *testing.T) {
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:    "echo",
	}, map[string]any{"words": []any{"hello", "world"}})
	if ierr != nil {
		t.Fatalf("error: %s: %s", ierr.Code, ierr.Message)
	}

	output := out.(map[string]any)
	if stdout, _ := output["stdout"].(string); stdout != "hello world\n" {
		t.Errorf("stdout = %q, want %q", stdout, "hello world\n")
	}
}

func TestIntegration_CreateInterface(t *testing.T) {
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
	creator := NewCreator()
	iface, err := creator.CreateInterface(context.Background(), &openbindings.CreateInput{
		Sources: []openbindings.CreateSource{{
			Format:  FormatToken,
			Content: spec,
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

	if len(iface.Operations) != 3 {
		t.Fatalf("expected 3 operations, got %d: %v", len(iface.Operations), mapKeys(iface.Operations))
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
	props := greetOp.Input["properties"].(map[string]any)
	if _, ok := props["name"]; !ok {
		t.Error("expected 'name' flag in greet input")
	}
	if _, ok := props["message"]; !ok {
		t.Error("expected 'message' arg in greet input")
	}

	binding := iface.Bindings["config.get."+DefaultSourceName]
	if binding.Ref != "config get" {
		t.Errorf("config.get ref = %q, want 'config get'", binding.Ref)
	}
}

func TestIntegration_AliasOnParentCommand(t *testing.T) {
	// Verify that aliases on parent commands are matched when resolving refs.
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
	// The canonical path should use the primary name, not the alias.
	if result.path[0] != "configuration" {
		t.Errorf("path[0] = %q, want configuration", result.path[0])
	}

	// "cfg set" should also work.
	result2, err := findCommand(mustParse(t, spec), "cfg set")
	if err != nil {
		t.Fatalf("findCommand(cfg set): %v", err)
	}
	if result2.cmd.Name != "set" {
		t.Errorf("cmd name = %q, want set", result2.cmd.Name)
	}
}

func TestIntegration_RootCommand(t *testing.T) {
	// Test executing with an empty ref (root invocation).
	rootSpec := `bin "` + testBinary + `"
flag "-v --verbose" help="Verbose output"
arg "<words>..." help="Words to echo"
`
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Content: rootSpec},
		Ref:    "",
	}, map[string]any{"words": []any{"hello", "world"}})
	if ierr != nil {
		t.Fatalf("error: %s: %s", ierr.Code, ierr.Message)
	}

	output := out.(map[string]any)
	if stdout, _ := output["stdout"].(string); stdout != "hello world\n" {
		t.Errorf("stdout = %q, want %q", stdout, "hello world\n")
	}
}

func TestIntegration_InvalidRef(t *testing.T) {
	invoker := NewInvoker()
	out, ierr := invokeUsage(t, invoker, &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:    "nonexistent",
	}, nil)
	if out != nil {
		t.Fatalf("expected no output, got %v", out)
	}
	if ierr == nil || ierr.Code != openbindings.ErrCodeRefNotFound {
		t.Fatalf("expected ERR_REF_NOT_FOUND, got %v", ierr)
	}
}

// TestIntegration_NoInputOperationConvention verifies the operation-layer
// no-input convention: a binding carrying Binding != nil and InputSchema ==
// nil runs the bare command without waiting for (or rejecting) a write.
func TestIntegration_NoInputOperationConvention(t *testing.T) {
	invoker := NewInvoker()
	ctx := context.Background()
	call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: FormatToken, Content: testSpec()},
		Ref:     "mixed",
		Binding: &openbindings.BindingEntry{Operation: "mixed", Source: "s", Ref: "mixed"},
		// InputSchema nil → no-input operation; the binding closes input itself.
	})
	// The caller writes nothing and does not close; the binding must still run.
	out, err := openbindings.Single(ctx, call.Outputs())
	if err != nil {
		t.Fatalf("no-input convention failed: %v", err)
	}
	if _, ok := out.(map[string]any); !ok {
		t.Fatalf("expected map output, got %T", out)
	}
}

func TestIntegration_Cancellation(t *testing.T) {
	// A cancelled context tears down the running process; the handle
	// terminates with ERR_CANCELLED. Uses the direct-binary metadata path to
	// run `sleep 10`.
	invoker := NewInvoker()
	ctx, cancel := context.WithCancel(context.Background())
	call := invoker.InvokeBinding(ctx, &openbindings.BindingInvocationArgs{
		Source:  openbindings.InvocationSource{Format: FormatToken, Content: `bin "sleep"`},
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
