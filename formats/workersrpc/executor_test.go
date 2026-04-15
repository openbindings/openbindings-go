package workersrpc

import (
	"context"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestExecutor_Formats(t *testing.T) {
	exec := NewExecutor()
	formats := exec.Formats()
	if len(formats) != 1 {
		t.Fatalf("expected exactly 1 format, got %d", len(formats))
	}
	if formats[0].Token != FormatToken {
		t.Errorf("token = %q, want %q", formats[0].Token, FormatToken)
	}
	if formats[0].Description == "" {
		t.Error("description should be non-empty")
	}
}

func TestExecutor_ExecuteBinding_AlwaysFails(t *testing.T) {
	// Workers RPC dispatch is not possible from Go. The Go executor stub
	// must yield a clear, actionable error event on the channel (not return
	// a Go error) directing the caller to the TypeScript runtime.
	exec := NewExecutor()
	ch, err := exec.ExecuteBinding(context.Background(), &openbindings.BindingExecutionInput{
		Source: openbindings.BindingExecutionSource{Format: FormatToken, Location: "workers-rpc://test"},
		Ref:    "someMethod",
	})
	if err != nil {
		t.Fatalf("ExecuteBinding must not return a Go error; got: %v", err)
	}
	if ch == nil {
		t.Fatal("ExecuteBinding must return a non-nil channel")
	}
	ev, ok := <-ch
	if !ok {
		t.Fatal("channel closed without yielding an event")
	}
	if ev.Error == nil {
		t.Fatal("expected an error event, got a data event")
	}
	if ev.Error.Code != openbindings.ErrCodeSourceConfigError {
		t.Errorf("error code = %q, want %q", ev.Error.Code, openbindings.ErrCodeSourceConfigError)
	}
	msg := ev.Error.Message
	if !strings.Contains(msg, "Cloudflare Worker") || !strings.Contains(msg, "WorkersRpcExecutor") {
		t.Errorf("error message should mention Cloudflare Worker and WorkersRpcExecutor, got: %s", msg)
	}
	// Channel should be closed after the single error event.
	if _, more := <-ch; more {
		t.Error("channel should be closed after the error event")
	}
}

func TestCreator_Formats(t *testing.T) {
	c := NewCreator()
	formats := c.Formats()
	if len(formats) != 1 {
		t.Fatalf("expected exactly 1 format, got %d", len(formats))
	}
	if formats[0].Token != FormatToken {
		t.Errorf("token = %q, want %q", formats[0].Token, FormatToken)
	}
}

func TestCreator_CreateInterface_AlwaysFails(t *testing.T) {
	// Workers RPC OBIs are hand-authored — there's no source artifact to
	// synthesize from. The creator stub must return a clear error.
	c := NewCreator()
	_, err := c.CreateInterface(context.Background(), &openbindings.CreateInput{
		Sources: []openbindings.CreateSource{
			{Format: FormatToken, Location: "workers-rpc://test"},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "hand-authored") {
		t.Errorf("error message should explain that OBIs are hand-authored, got: %s", msg)
	}
}

func TestFormatToken_Constant(t *testing.T) {
	// Sanity-check the format token to catch accidental version drift.
	want := "workers-rpc@^1.0.0"
	if FormatToken != want {
		t.Errorf("FormatToken = %q, want %q", FormatToken, want)
	}
}

func TestDefaultSourceName_Constant(t *testing.T) {
	want := "workersRpc"
	if DefaultSourceName != want {
		t.Errorf("DefaultSourceName = %q, want %q", DefaultSourceName, want)
	}
}
