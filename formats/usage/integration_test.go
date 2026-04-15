package usage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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

func TestIntegration_JSONOutput(t *testing.T) {
	executor := NewExecutor()
	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: testSpec(),
		},
		Ref:   "json",
		Input: map[string]any{"pairs": []any{"name=alice", "role=admin"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Error != nil {
		t.Fatalf("unexpected error: %s: %s", events[0].Error.Code, events[0].Error.Message)
	}

	// JSON output should be parsed into a map (the executor parses stdout JSON).
	result, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("expected parsed JSON map, got %T: %v", events[0].Data, events[0].Data)
	}
	if result["name"] != "alice" {
		t.Errorf("name = %v, want alice", result["name"])
	}
	if result["role"] != "admin" {
		t.Errorf("role = %v, want admin", result["role"])
	}
}

func TestIntegration_NonZeroExitCode(t *testing.T) {
	executor := NewExecutor()
	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: testSpec(),
		},
		Ref:   "fail",
		Input: map[string]any{"message": []any{"something went wrong"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	ev := events[0]
	// Non-zero exit code is returned as Status on the event.
	// The exit error is handled internally -- the output includes stderr.
	if ev.Status != 1 {
		t.Errorf("status = %d, want 1", ev.Status)
	}
	output, ok := ev.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map output, got %T: %v", ev.Data, ev.Data)
	}
	stderr, _ := output["stderr"].(string)
	if stderr == "" {
		t.Error("expected non-empty stderr from failed command")
	}
}

func TestIntegration_MixedOutput(t *testing.T) {
	executor := NewExecutor()
	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: testSpec(),
		},
		Ref: "mixed",
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 || events[0].Error != nil {
		t.Fatal("expected 1 successful event")
	}

	output := events[0].Data.(map[string]any)
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
	executor := NewExecutor()
	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: testSpec(),
		},
		Ref:   "echo",
		Input: map[string]any{"words": []any{"hello", "world"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 || events[0].Error != nil {
		if len(events) > 0 && events[0].Error != nil {
			t.Fatalf("error: %s: %s", events[0].Error.Code, events[0].Error.Message)
		}
		t.Fatal("expected 1 successful event")
	}

	output := events[0].Data.(map[string]any)
	stdout, _ := output["stdout"].(string)
	if stdout != "hello world\n" {
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
	executor := NewExecutor()
	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: rootSpec,
		},
		Ref:   "",
		Input: map[string]any{"words": []any{"hello", "world"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 || events[0].Error != nil {
		if len(events) > 0 && events[0].Error != nil {
			t.Fatalf("error: %s: %s", events[0].Error.Code, events[0].Error.Message)
		}
		t.Fatal("expected 1 successful event")
	}

	output := events[0].Data.(map[string]any)
	stdout, _ := output["stdout"].(string)
	if stdout != "hello world\n" {
		t.Errorf("stdout = %q, want %q", stdout, "hello world\n")
	}
}

func TestIntegration_InvalidRef(t *testing.T) {
	executor := NewExecutor()
	ch, err := executor.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{
			Format:  FormatToken,
			Content: testSpec(),
		},
		Ref: "nonexistent",
	})
	if err != nil {
		t.Fatal(err)
	}

	events := drainStream(t, ch)
	if len(events) != 1 || events[0].Error == nil {
		t.Fatal("expected error event")
	}
	if events[0].Error.Code != openbindings.ErrCodeRefNotFound {
		t.Errorf("code = %q, want %q", events[0].Error.Code, openbindings.ErrCodeRefNotFound)
	}
}

func drainStream(t *testing.T, ch <-chan openbindings.StreamEvent) []openbindings.StreamEvent {
	t.Helper()
	var events []openbindings.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func mapKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
