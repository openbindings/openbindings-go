package invoke

import (
	"context"
	"testing"
	"testing/synctest"
)

type cancellationPreparer struct {
	mockBindingInvoker
	prepare func(context.Context) (*ContextRequiredDetails, error)
}

func (p *cancellationPreparer) PrepareBinding(ctx context.Context, _ *BindingInvocationArgs) (*ContextRequiredDetails, error) {
	return p.prepare(ctx)
}

func TestCancelAfterRejectedInputStopsPreflight(t *testing.T) {
	for _, stage := range []string{"prepare", "resolve"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, stop := context.WithCancel(context.Background())
				defer stop()
				entered := make(chan context.Context, 1)
				block := func(workCtx context.Context) {
					entered <- workCtx
					<-workCtx.Done()
				}
				binding := &cancellationPreparer{prepare: func(workCtx context.Context) (*ContextRequiredDetails, error) {
					if stage == "prepare" {
						block(workCtx)
						return nil, workCtx.Err()
					}
					return bearerDetails, nil
				}}
				resolve := func(workCtx context.Context, _ *ContextRequiredDetails) (map[string]any, error) {
					block(workCtx)
					return nil, workCtx.Err()
				}
				call := Invoke(ctx, newOpInvoker(binding, resolve), opTestInterface(), NewOperationSignature[any, any]("ping"))
				defer call.Cancel()
				workCtx := <-entered
				if err := call.Write(ctx, make(chan int)); codeOf(t, err) != ErrCodeTypeMismatch {
					t.Fatalf("expected rejected input: %v", err)
				}
				call.Cancel() // The application's per-attempt cleanup.
				synctest.Wait()
				select {
				case <-workCtx.Done():
				default:
					t.Errorf("invocation cancellation did not cancel %s", stage)
				}
				stop() // Also release a faulty implementation before ending the test.
				synctest.Wait()
				if attempts, _, _, _ := binding.snapshot(); attempts != 0 {
					t.Fatal("binding was invoked after cancelled preflight")
				}
			})
		})
	}
}
