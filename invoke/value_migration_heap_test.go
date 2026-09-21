package invoke

import (
	"context"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// This opt-in evidence probe holds equal public results in every candidate.
// Timing benchmarks run separately; the sampler deliberately adds overhead.
func TestValueMigrationRetainedHeap(t *testing.T) {
	if os.Getenv("OB_VALUE_HEAP_PROBE") != "1" {
		t.Skip("qualification heap probe")
	}
	ctx := context.Background()
	payload := make([]byte, 1<<20)
	type result struct {
		Data []byte `json:"data"`
	}
	runtime.GC()
	var start runtime.MemStats
	runtime.ReadMemStats(&start)
	var peak atomic.Uint64
	peak.Store(start.HeapAlloc)
	stop, joined := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(joined)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				for {
					old := peak.Load()
					if m.HeapAlloc <= old || peak.CompareAndSwap(old, m.HeapAlloc) {
						break
					}
				}
			}
		}
	}()
	call := NewInvocationImpl[any, any](ctx)
	for n := 0; n < 4; n++ {
		if err := call.EmitOutput(result{payload}); err != nil {
			t.Fatal(err)
		}
	}
	call.CloseOutput()
	runtime.GC()
	var queued runtime.MemStats
	runtime.ReadMemStats(&queued)
	out := NewTypedInvocation[any, result](call).Outputs()
	held := make([]result, 0, 4)
	for n := 0; n < 4; n++ {
		v, err := out.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, v)
	}
	out.Stop()
	runtime.GC()
	var retained runtime.MemStats
	runtime.ReadMemStats(&retained)
	runtime.KeepAlive(held)
	runtime.KeepAlive(payload)
	held = nil
	payload = nil
	close(stop)
	<-joined
	runtime.GC()
	var retired runtime.MemStats
	runtime.ReadMemStats(&retired)
	t.Logf("heap deltas bytes: queued=%d public-retained=%d after-retirement=%d sampled-peak=%d", int64(queued.HeapAlloc)-int64(start.HeapAlloc), int64(retained.HeapAlloc)-int64(start.HeapAlloc), int64(retired.HeapAlloc)-int64(start.HeapAlloc), int64(peak.Load())-int64(start.HeapAlloc))
}
