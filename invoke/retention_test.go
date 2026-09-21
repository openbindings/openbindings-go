package invoke

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

type handoffCodec func() ([]byte, error)

func (f handoffCodec) MarshalJSON() ([]byte, error) { return f() }

func TestTerminalOutputSkipsCapture(t *testing.T) {
	for _, outcome := range []string{"cancelled", "completed", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			call := NewInvocationImpl[any, any](t.Context())
			defer call.Cancel()
			if err := call.EmitOutput("accepted"); err != nil {
				t.Fatal(err)
			}
			want := ErrCodeCancelled
			switch outcome {
			case "cancelled":
				call.Cancel()
			case "completed":
				call.CloseOutput()
				want = ErrCodeInvocationClosed
			case "failed":
				call.FireError(NewInvocationErrorWithData(ErrCodeExecutionFailed, map[string]any{"reason": "original"}))
				want = ErrCodeExecutionFailed
			}
			codecCalls := 0
			err := call.EmitOutput(handoffCodec(func() ([]byte, error) {
				codecCalls++
				return []byte(`"late"`), nil
			}))
			if codeOf(t, err) != want {
				t.Fatalf("late output: %v", err)
			}
			if codecCalls != 0 {
				t.Errorf("terminal handoff invoked the producer codec %d times", codecCalls)
			}
			if outcome == "failed" {
				// The early rejection must preserve detached terminal details.
				AsInvocationError(err).Data.(map[string]any)["reason"] = "changed"
			}
			out := call.Outputs()
			if got, err := out.Read(t.Context()); err != nil || got != "accepted" {
				t.Fatalf("accepted output lost: %v, %v", got, err)
			}
			_, err = out.Read(t.Context())
			if outcome == "completed" {
				if err != io.EOF {
					t.Fatalf("completion: %v", err)
				}
			} else if codeOf(t, err) != want {
				t.Fatalf("terminal changed: %v", err)
			} else if outcome == "failed" && AsInvocationError(err).Data.(map[string]any)["reason"] != "original" {
				t.Fatalf("terminal data was shared: %v", err)
			}
		})
	}
}

func TestOutputCaptureAfterInputClose(t *testing.T) {
	call := NewInvocationImpl[any, any](t.Context())
	defer call.Cancel()
	_ = call.CloseInput()
	if err := call.EmitOutput("response"); err != nil {
		t.Fatal(err)
	}
	call.CloseOutput()
	if got, err := Single(t.Context(), call.Outputs()); err != nil || got != "response" {
		t.Fatalf("input closure prevented output: %v, %v", got, err)
	}
}

func TestCancellationDuringOutputCodec(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		call := NewInvocationImpl[any, any](context.Background())
		defer call.Cancel()
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		emitted := make(chan error, 1)
		go func() {
			emitted <- call.EmitOutput(handoffCodec(func() ([]byte, error) {
				close(entered)
				<-release
				return []byte(`"late"`), nil
			}))
		}()
		<-entered
		cancelled := make(chan struct{})
		go func() { call.Cancel(); close(cancelled) }()
		synctest.Wait()
		select {
		case <-cancelled:
		default:
			t.Fatal("cancellation waited for a running producer codec")
		}
		// Cancellation cannot interrupt the codec, but its late result must
		// be refused once the codec cooperates and returns.
		unblock()
		synctest.Wait()
		if err := <-emitted; codeOf(t, err) != ErrCodeCancelled {
			t.Fatalf("late capture escaped cancellation: %v", err)
		}
		if _, err := call.Outputs().Read(context.Background()); codeOf(t, err) != ErrCodeCancelled {
			t.Fatalf("late output was enqueued: %v", err)
		}
	})
}

func TestConcurrentParkedHandoffsCancel(t *testing.T) {
	for _, output := range []bool{false, true} {
		name := "input"
		if output {
			name = "output"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				call := NewInvocationImpl[any, any](context.Background())
				defer call.Cancel()
				capacity := inputBufferCapacity
				send := func() error { return call.Write(context.Background(), "value") }
				if output {
					capacity, send = outputBufferCapacity, func() error { return call.EmitOutput("value") }
				}
				for n := 0; n < capacity; n++ {
					if err := send(); err != nil {
						t.Fatal(err)
					}
				}
				const producers = 12
				done := make(chan error, producers)
				for n := 0; n < producers; n++ {
					go func() { done <- send() }()
				}
				synctest.Wait()
				if len(done) != 0 {
					t.Fatal("a full queue did not park every producer")
				}
				call.Cancel()
				synctest.Wait()
				if len(done) != producers {
					t.Fatalf("only %d producers woke after cancellation", len(done))
				}
				for n := 0; n < producers; n++ {
					if err := <-done; codeOf(t, err) != ErrCodeCancelled {
						t.Fatalf("parked handoff: %v", err)
					}
				}
			})
		})
	}
}

type retentionStreamBinding struct {
	mockBindingInvoker
	accepted atomic.Int32
	stopped  chan error
}

func (b *retentionStreamBinding) InvokeBinding(ctx context.Context, _ *BindingInvocationArgs) Invocation[any, any] {
	call := NewInvocationImpl[any, any](ctx)
	_ = call.CloseInput()
	go func() {
		defer call.CloseOutput()
		for n := 0; n < 64; n++ {
			if err := call.EmitOutput(n); err != nil {
				b.stopped <- err
				return
			}
			b.accepted.Add(1)
		}
		b.stopped <- nil
	}()
	return call
}

func TestCancelledOperationUnparksBindingProducer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		binding := &retentionStreamBinding{stopped: make(chan error, 1)}
		call := Invoke(context.Background(), newOpInvoker(binding, nil), opTestInterface(), NewOperationSignature[any, any]("ping"))
		defer call.Cancel()
		// No application reads: both operation and binding output queues
		// fill, parking the forwarding stage and then the binding producer.
		synctest.Wait()
		if binding.accepted.Load() == 0 || len(binding.stopped) != 0 {
			t.Fatal("the pipeline did not apply backpressure to its binding")
		}
		call.Cancel()
		synctest.Wait()
		select {
		case err := <-binding.stopped:
			if codeOf(t, err) != ErrCodeCancelled {
				t.Fatalf("binding terminal: %v", err)
			}
		default:
			t.Fatal("the binding remained blocked after operation cancellation")
		}
		out := call.Outputs()
		read := 0
		for {
			_, err := out.Read(context.Background())
			if err != nil {
				if read == 0 || codeOf(t, err) != ErrCodeCancelled {
					t.Fatalf("accepted prefix or terminal lost: outputs=%d, err=%v", read, err)
				}
				break
			}
			read++
		}
	})
}
