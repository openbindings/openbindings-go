package sdk

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
	"github.com/openbindings/openbindings-go/synthesize"
)

const testBindingSpec = "example.binding@1"

type testProvider struct{}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (testProvider) BindingSpecs() []openbindings.BindingSpecInfo {
	return []openbindings.BindingSpecInfo{{BindingSpec: testBindingSpec, Description: "test"}}
}

func (testProvider) CheckBindingSpecs(bindingSpecs []string) []openbindings.BindingSpecVerdict {
	return openbindings.CheckBindingSpecs(bindingSpecs, testProvider{}.BindingSpecs())
}

func (testProvider) InvokeBinding(ctx context.Context, _ *invoke.BindingInvocationArgs) invoke.Invocation[any, any] {
	call := invoke.NewInvocationImpl[any, any](ctx)
	go func() {
		_ = call.CloseInput()
		_ = call.EmitOutput(map[string]any{"ok": true})
		call.CloseOutput()
	}()
	return call
}

func (testProvider) SynthesizeInterface(_ context.Context, _ *synthesize.SynthesizeInput) (*openbindings.Interface, error) {
	return testInterface(), nil
}

func (p testProvider) SynthesizeInterfaceWithCoverage(ctx context.Context, input *synthesize.SynthesizeInput) (*synthesize.SynthesizeResult, error) {
	iface, err := p.SynthesizeInterface(ctx, input)
	if err != nil {
		return nil, err
	}
	return synthesize.NewSynthesisResult(iface, []synthesize.SynthesisCoverageEntry{{
		SourceIndex: 0, SourceKey: "source", SourceRef: "target", Scope: synthesize.SynthesisCoverageTarget,
		Status: synthesize.SynthesisRepresented, OperationKey: "ping", BindingKey: "ping.binding",
		BindingSelector: "target",
	}}, true)
}

func (testProvider) InspectSource(_ context.Context, _ *openbindings.Source) (*synthesize.SourceInspection, error) {
	return &synthesize.SourceInspection{Targets: []synthesize.BindableTarget{{Selector: "target", OperationKey: "ping"}}, Exhaustive: true}, nil
}

func testInterface() *openbindings.Interface {
	return &openbindings.Interface{
		OpenBindings: "0.2.0",
		Operations:   map[string]openbindings.Operation{"ping": {}},
		Sources: map[string]openbindings.Source{
			"source": {BindingSpec: testBindingSpec, Location: "memory://source"},
		},
		Bindings: map[string]openbindings.BindingEntry{
			"ping.binding": {Operation: "ping", Source: "source", Selector: "target"},
		},
	}
}

func TestRuntimeComposesProviderCapabilities(t *testing.T) {
	runtime, err := New(RuntimeOptions{Providers: []BindingProvider{testProvider{}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.BindingSpecs(); len(got) != 1 || got[0].BindingSpec != testBindingSpec {
		t.Fatalf("binding specs = %#v", got)
	}
	inspection, err := runtime.InspectSource(context.Background(), &openbindings.Source{BindingSpec: testBindingSpec})
	if err != nil || len(inspection.Targets) != 1 || inspection.Targets[0].OperationKey != "ping" {
		t.Fatalf("inspection = (%#v, %v)", inspection, err)
	}
	result, err := runtime.SynthesizeInterfaceWithCoverage(context.Background(), &synthesize.SynthesizeInput{
		Sources: []synthesize.SynthesizeSource{{BindingSpec: testBindingSpec}},
	})
	if err != nil || !result.Coverage.Exhaustive || len(result.Coverage.Entries) != 1 {
		t.Fatalf("synthesis = (%#v, %v)", result, err)
	}
	details, err := runtime.PrepareOperation(context.Background(), testInterface(), "ping")
	if err != nil || details != nil {
		t.Fatalf("preflight = (%#v, %v)", details, err)
	}
	call := runtime.Invoke(context.Background(), testInterface(), "ping")
	if err := call.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := invoke.Single(context.Background(), call.Outputs())
	if err != nil {
		t.Fatal(err)
	}
	if got := output.(map[string]any)["ok"]; got != true {
		t.Fatalf("output = %#v", output)
	}
}

func TestRuntimeRejectsDuplicateBindingRegistrations(t *testing.T) {
	_, err := New(RuntimeOptions{Providers: []BindingProvider{testProvider{}, testProvider{}}})
	if err == nil {
		t.Fatal("duplicate registration was accepted")
	}
}

func TestRuntimeResolvesThroughRegisteredProvider(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    request,
		}, nil
	})}
	runtime, err := New(RuntimeOptions{Providers: []BindingProvider{testProvider{}}, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := runtime.Resolve(context.Background(), "https://api.example.test/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Synthesized || resolved.Interface == nil || resolved.Coverage == nil || !resolved.Coverage.Exhaustive {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestRuntimeWithoutProvidersReturnsInvocationFailure(t *testing.T) {
	runtime, err := New(RuntimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	call := runtime.Invoke(context.Background(), testInterface(), "ping")
	_ = call.Close()
	_, err = call.Outputs().Read(context.Background())
	if err == nil || err == io.EOF {
		t.Fatalf("terminal = %v, want no-invoker failure", err)
	}
}
