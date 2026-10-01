//go:build linux || darwin

package openbindings

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"syscall"
	"time"
)

// sentinels observe retrieval during one check-dependency-kind action: the
// http channel through a counting transport in place of
// http.DefaultTransport, and the file channel through a FIFO whose every
// open for reading is seen. The corpus places {retrieval-sentinel:http} and
// {retrieval-sentinel:file} in the kinds; the adapter replaces them with
// addresses on the observed channels.
type sentinels struct {
	httpAttempts int
	previous     http.RoundTripper
	fifo         string
	dir          string
	opened       atomic.Int64
	stopWatch    chan struct{}
	watching     chan struct{}
}

func startSentinels(channels []string) (*sentinels, error) {
	s := &sentinels{previous: http.DefaultTransport}
	http.DefaultTransport = trapTransport{attempts: &s.httpAttempts}
	if slices.Contains(channels, "file") {
		dir, err := os.MkdirTemp("", "obi-kind-sentinel")
		if err != nil {
			s.stop()
			return nil, err
		}
		s.dir, s.fifo = dir, filepath.Join(dir, "kind")
		if err := syscall.Mkfifo(s.fifo, 0o600); err != nil {
			s.stop()
			return nil, fmt.Errorf("the file channel cannot be observed here: %v", err)
		}
		s.stopWatch, s.watching = make(chan struct{}), make(chan struct{})
		go func() {
			defer close(s.watching)
			for {
				select {
				case <-s.stopWatch:
					return
				default:
				}
				// An open for writing succeeds without blocking only while
				// a reader holds the FIFO open: someone opened the kind.
				if f, err := os.OpenFile(s.fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					s.opened.Add(1)
					f.Close()
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	return s, nil
}

func (s *sentinels) substitute(doc []byte) []byte {
	fifo := s.fifo
	if fifo == "" {
		fifo = "/nonexistent/obi-kind-sentinel"
	}
	doc = bytes.ReplaceAll(doc, []byte("{retrieval-sentinel:file}"), []byte(fifo))
	return bytes.ReplaceAll(doc, []byte("{retrieval-sentinel:http}"), []byte("http://127.0.0.1:9/obi-kind-sentinel"))
}

// stop ends the observation and describes any retrieval it saw.
func (s *sentinels) stop() string {
	http.DefaultTransport = s.previous
	if s.stopWatch != nil {
		time.Sleep(5 * time.Millisecond)
		close(s.stopWatch)
		<-s.watching
	}
	if s.dir != "" {
		os.RemoveAll(s.dir)
	}
	switch {
	case s.httpAttempts > 0:
		return fmt.Sprintf("observed %d http retrieval attempt(s) during the action", s.httpAttempts)
	case s.opened.Load() > 0:
		return fmt.Sprintf("observed %d open(s) of the file sentinel during the action", s.opened.Load())
	}
	return ""
}
