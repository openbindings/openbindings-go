//go:build linux || darwin

package openbindings

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

// fileSentinel is a FIFO whose every open for reading is seen.
type fileSentinel struct {
	path, dir string
	opened    atomic.Int64
	stopWatch chan struct{}
	watching  chan struct{}
}

func startFileSentinel() (*fileSentinel, error) {
	dir, err := os.MkdirTemp("", "obi-kind-sentinel")
	if err != nil {
		return nil, err
	}
	f := &fileSentinel{dir: dir, path: filepath.Join(dir, "kind"), stopWatch: make(chan struct{}), watching: make(chan struct{})}
	if err := syscall.Mkfifo(f.path, 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("the file channel cannot be observed here: %v", err)
	}
	go func() {
		defer close(f.watching)
		for {
			select {
			case <-f.stopWatch:
				return
			default:
			}
			// An open for writing succeeds without blocking only while a
			// reader holds the FIFO open: someone opened the kind.
			if w, err := os.OpenFile(f.path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				f.opened.Add(1)
				w.Close()
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return f, nil
}

// stop ends the observation and returns the opens it saw.
func (f *fileSentinel) stop() int64 {
	time.Sleep(5 * time.Millisecond)
	close(f.stopWatch)
	<-f.watching
	os.RemoveAll(f.dir)
	return f.opened.Load()
}
