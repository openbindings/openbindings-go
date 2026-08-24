package invoke

import (
	"context"
	"io"
	"reflect"
	"testing"
	"time"

	openbindings "github.com/openbindings/openbindings-go"
)

type supportTestInvoker struct {
	listed    []openbindings.BindingSpecInfo
	warranted []openbindings.BindingSpecInfo
	calls     int
}

func TestOperationInvokerSelectionUsesAuthoritativeSupport(t *testing.T) {
	hidden := &supportTestInvoker{
		warranted: []openbindings.BindingSpecInfo{{BindingSpec: "example.hidden@1"}},
	}
	invoker := NewOperationInvoker(hidden)
	iface := &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]openbindings.Operation{"ping": {}},
		Sources: map[string]openbindings.Source{
			"hidden": {
				BindingSpec: "example.hidden@1",
				Location:    "https://example.test/source",
			},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"ping.hidden": {Operation: "ping", Source: "hidden", Selector: "ping"},
		},
	}

	call := Invoke(context.Background(), invoker, iface, NewOperationSignature[any, any]("ping"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := call.Outputs().Read(ctx); err != io.EOF {
		t.Fatalf("operation terminal = %v, want EOF", err)
	}
	if hidden.calls != 1 {
		t.Fatalf("hidden invoker calls = %d, want 1", hidden.calls)
	}
}

func (s *supportTestInvoker) BindingSpecs() []openbindings.BindingSpecInfo {
	return append([]openbindings.BindingSpecInfo(nil), s.listed...)
}

func (s *supportTestInvoker) CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict {
	return openbindings.CheckBindingSpecs(bindingSpecs, s.warranted)
}

func (s *supportTestInvoker) InvokeBinding(ctx context.Context, _ *BindingInvocationArgs) Invocation[any, any] {
	s.calls++
	invocation := NewInvocationImpl[any, any](ctx)
	go func() {
		_ = invocation.CloseInput()
		invocation.CloseOutput()
	}()
	return invocation
}

func TestCombineInvokersChecksAuthoritativeSupport(t *testing.T) {
	hidden := &supportTestInvoker{
		warranted: []openbindings.BindingSpecInfo{{BindingSpec: "example.hidden@1"}},
	}
	listed := &supportTestInvoker{
		listed:    []openbindings.BindingSpecInfo{{BindingSpec: "example.listed@1"}},
		warranted: []openbindings.BindingSpecInfo{{BindingSpec: "example.listed@1"}},
	}
	combined := CombineInvokers(hidden, listed)

	input := []string{"example.hidden@1", "example.hidden", "example.listed@1", "example.hidden@1"}
	want := []openbindings.BindingSpecVerdict{
		{BindingSpec: "example.hidden@1", Supported: true},
		{BindingSpec: "example.hidden", Supported: false},
		{BindingSpec: "example.listed@1", Supported: true},
	}
	if got := combined.CheckBindingSpecs(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("CheckBindingSpecs() = %#v, want %#v", got, want)
	}
	if got := combined.BindingSpecs(); len(got) != 1 || got[0].BindingSpec != "example.listed@1" {
		t.Fatalf("BindingSpecs() = %#v, want only advisory listed identifier", got)
	}

	call := combined.InvokeBinding(context.Background(), &BindingInvocationArgs{
		Source: InvocationSource{BindingSpec: "example.hidden@1"},
	})
	if _, err := call.Outputs().Read(context.Background()); err == nil {
		t.Fatal("hidden supported invoker did not complete its invocation")
	}
	if hidden.calls != 1 {
		t.Fatalf("hidden invoker calls = %d, want 1", hidden.calls)
	}
}
