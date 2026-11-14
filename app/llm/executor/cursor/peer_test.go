package cursor

import (
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAClosedPeerReleasesAWriteTheServiceNeverReads(t *testing.T) {
	p := newPeer()
	wrote := make(chan error, 1)
	// Nothing reads the pipe, so this write blocks the way a stalled stream does.
	go func() { wrote <- p.write([]byte("frame")) }()
	time.Sleep(20 * time.Millisecond)

	done := make(chan struct{})
	go func() { p.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("close waited on a write that nothing would ever read")
	}
	select {
	case err := <-wrote:
		// The exchange ended under it, so the frame is dropped, not failed.
		if err != nil {
			t.Fatalf("a write released by the end of the exchange failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the blocked write was never released")
	}
	if err := p.write([]byte("late")); err != nil {
		t.Fatalf("a write after the end is dropped, not an error: %v", err)
	}
}

// slowSource hands out chunks until it is closed.
type slowSource struct {
	chunks int
	closed chan struct{}
}

func (s *slowSource) Read(p []byte) (int, error) {
	select {
	case <-s.closed:
		return 0, io.ErrClosedPipe
	default:
	}
	if s.chunks == 0 {
		return 0, io.EOF
	}
	s.chunks--
	return copy(p, strings.Repeat("x", 4)), nil
}

func (s *slowSource) Close() error { close(s.closed); return nil }

func TestAnAbandonedTurnLeavesNoReaderBehind(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		src := &slowSource{chunks: 1000, closed: make(chan struct{})}
		r := newStartReader(src)
		buf := make([]byte, 16)
		if _, err := r.Wait(buf, time.Second); err != nil {
			t.Fatal(err)
		}
		// The client leaves with more already read ahead than anyone will take.
		r.close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runtime.NumGoroutine() > before+2 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before+2 {
		t.Fatalf("goroutines: %d before, %d after twenty abandoned turns", before, got)
	}
}
