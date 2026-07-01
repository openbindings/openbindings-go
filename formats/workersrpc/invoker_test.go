package workersrpc

import (
	"context"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
)

func TestInvoker_Formats(t *testing.T) {
	invoker := NewInvoker()
	formats := invoker.Formats()
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

func TestInvoker_InvokeBinding_AlwaysFails(t *testing.T) {
	// Workers RPC dispatch is not possible from Go. The Go invoker stub
	// must return an already-errored invocation handle (not panic, not
	// yield outputs) directing the caller to the TypeScript runtime.
	invoker := NewInvoker()
	inv := invoker.InvokeBinding(context.Background(), &openbindings.BindingInvocationArgs{
		Source: openbindings.InvocationSource{Format: FormatToken, Location: "workers-rpc://test"},
		Ref:    "someMethod",
	})
	if inv == nil {
		t.Fatal("InvokeBinding must return a non-nil invocation handle")
	}
	_, err := inv.Outputs().Read(context.Background())
	if err == nil {
		t.Fatal("expected a terminal error, got an output")
	}
	terr, ok := err.(*openbindings.InvocationError)
	if !ok {
		t.Fatalf("expected a terminal *InvocationError, got %T: %v", err, err)
	}
	if terr.Code != openbindings.ErrCodeSourceConfigError {
		t.Errorf("error code = %q, want %q", terr.Code, openbindings.ErrCodeSourceConfigError)
	}
	msg := terr.Message
	if !strings.Contains(msg, "Cloudflare Worker") || !strings.Contains(msg, "WorkersRpcInvoker") {
		t.Errorf("error message should mention Cloudflare Worker and WorkersRpcInvoker, got: %s", msg)
	}
}

func TestSynthesizer_Formats(t *testing.T) {
	c := NewSynthesizer()
	formats := c.Formats()
	if len(formats) != 1 {
		t.Fatalf("expected exactly 1 format, got %d", len(formats))
	}
	if formats[0].Token != FormatToken {
		t.Errorf("token = %q, want %q", formats[0].Token, FormatToken)
	}
}

func TestSynthesizer_SynthesizeInterface_AlwaysFails(t *testing.T) {
	// Workers RPC OBIs are hand-authored — there's no source artifact to
	// synthesize from. The synthesizer stub must return a clear error.
	c := NewSynthesizer()
	_, err := c.SynthesizeInterface(context.Background(), &openbindings.SynthesizeInput{
		Sources: []openbindings.SynthesizeSource{
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
