package openbindings

import (
	"bytes"
	"fmt"
	"net"
	"slices"
	"sync/atomic"
	"time"
)

// sentinels observe retrieval during one check-dependency-kind action. The
// corpus places {retrieval-sentinel:http} and {retrieval-sentinel:file} in
// the kinds; the adapter replaces them with addresses on channels it observes
// for the whole action, loading included:
//   - http: a TCP listener bound to an ephemeral local port for the action,
//     whose address the kind names. Every accepted connection counts,
//     whatever client, transport, or protocol made it.
//   - file: a FIFO whose every open for reading is seen, where the platform
//     has FIFOs.
type sentinels struct {
	listener  *net.TCPListener
	accepted  atomic.Int64
	accepting chan struct{}
	file      *fileSentinel
}

func startSentinels(channels []string) (*sentinels, error) {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, fmt.Errorf("the http channel cannot be observed here: %v", err)
	}
	s := &sentinels{listener: ln, accepting: make(chan struct{})}
	go func() {
		defer close(s.accepting)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepted.Add(1)
			conn.Close()
		}
	}()
	if slices.Contains(channels, "file") {
		if s.file, err = startFileSentinel(); err != nil {
			s.stop()
			return nil, err
		}
	}
	return s, nil
}

func (s *sentinels) substitute(doc []byte) []byte {
	fifo := "/nonexistent/obi-kind-sentinel"
	if s.file != nil {
		fifo = s.file.path
	}
	doc = bytes.ReplaceAll(doc, []byte("{retrieval-sentinel:file}"), []byte(fifo))
	return bytes.ReplaceAll(doc, []byte("{retrieval-sentinel:http}"), []byte("http://"+s.listener.Addr().String()+"/obi-kind-sentinel"))
}

// stop ends the observation and describes any retrieval it saw. A
// connection the action completed waits in the listener's queue, so the
// listener drains the queue before it closes.
func (s *sentinels) stop() string {
	s.listener.SetDeadline(time.Now().Add(20 * time.Millisecond))
	<-s.accepting
	s.listener.Close()
	opened := int64(0)
	if s.file != nil {
		opened = s.file.stop()
	}
	switch {
	case s.accepted.Load() > 0:
		return fmt.Sprintf("observed %d connection(s) to the http sentinel during the action", s.accepted.Load())
	case opened > 0:
		return fmt.Sprintf("observed %d open(s) of the file sentinel during the action", opened)
	}
	return ""
}
