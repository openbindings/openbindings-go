package usage

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	openbindings "github.com/openbindings/openbindings-go"
	"github.com/openbindings/openbindings-go/invoke"
)

// prepareBindingSpec names a binary that does not exist. Preflight is
// side-effect-free by contract, so neither it nor the pre-spawn challenge
// may resolve, dereference, or run it.
const prepareBindingSpec = `bin "/nonexistent/prepare-binding-must-not-run"
cmd "ping" {
    help "Never runs"
}
`

// untouchableInvoker is an invoker whose process seam records any spawn
// and refuses it.
func untouchableInvoker() (*Invoker, *atomic.Bool) {
	var spawned atomic.Bool
	invoker := NewInvoker()
	invoker.Execute = func(context.Context, ProcessRequest) (ProcessResult, error) {
		spawned.Store(true)
		return ProcessResult{}, errors.New("preflight must not spawn")
	}
	return invoker, &spawned
}

func prepareBindingArgs(bindCtx map[string]any) *invoke.BindingInvocationArgs {
	return &invoke.BindingInvocationArgs{
		Source:   invoke.InvocationSource{BindingSpec: BindingSpec, Location: "/nonexistent/prepare-binding-must-not-run", Content: openbindings.TextContent(prepareBindingSpec)},
		Selector: "ping",
		Context:  bindCtx,
	}
}

// PrepareBinding reports exactly the challenge the live invocation raises
// for the same arguments: a generic credential that names no process
// environment variable is surfaced under §9.1 / USAGE-P-06 as one
// auth.apiKey requirement against the source location, for the flat
// apiKey and for a scheme-scoped apiKeys entry alike.
func TestPrepareBinding_MatchesLiveChallenge(t *testing.T) {
	for name, bindCtx := range map[string]map[string]any{
		"flat apiKey":          {"apiKey": "k-secret"},
		"scheme-scoped apiKey": {"apiKeys": map[string]any{"svc": "k-secret"}},
	} {
		t.Run(name, func(t *testing.T) {
			invoker, spawned := untouchableInvoker()
			args := prepareBindingArgs(bindCtx)

			preflight, err := invoker.PrepareBinding(context.Background(), args)
			if err != nil {
				t.Fatalf("PrepareBinding: %v", err)
			}
			if !invoke.ValidContextRequiredDetails(preflight) {
				t.Fatalf("PrepareBinding must report a valid challenge, got %#v", preflight)
			}
			if preflight.Target != args.Source.Location {
				t.Errorf("Target = %q, want the source location %q", preflight.Target, args.Source.Location)
			}

			_, ierr := invokeUsage(t, invoker, args, nil)
			if ierr == nil || ierr.Code != invoke.ErrCodeContextRequired {
				t.Fatalf("live terminal = %v, want %s", ierr, invoke.ErrCodeContextRequired)
			}
			live := invoke.ContextRequiredFrom(ierr)
			if !reflect.DeepEqual(preflight, live) {
				t.Errorf("preflight and live challenge differ:\n preflight %#v\n live      %#v", preflight, live)
			}
			if spawned.Load() {
				t.Error("a process was spawned; neither preflight nor the pre-spawn challenge may execute")
			}
		})
	}
}

// Supplying context the binding can place narrows the result to nothing:
// PrepareBinding returns nil exactly where the invocation would proceed to
// load the descriptor.
func TestPrepareBinding_SatisfiedContextIsNil(t *testing.T) {
	for name, bindCtx := range map[string]map[string]any{
		"empty":                    {},
		"credential named an env":  {"configuration": map[string]any{"environment": map[string]any{"API_KEY": "k-9"}}},
		"blank scheme-scoped keys": {"apiKeys": map[string]any{"svc": ""}},
	} {
		t.Run(name, func(t *testing.T) {
			invoker, spawned := untouchableInvoker()
			details, err := invoker.PrepareBinding(context.Background(), prepareBindingArgs(bindCtx))
			if err != nil {
				t.Fatalf("PrepareBinding: %v", err)
			}
			if details != nil {
				t.Errorf("PrepareBinding = %#v, want nil for placeable context", details)
			}
			if spawned.Load() {
				t.Error("a process was spawned during preflight")
			}
		})
	}
}
