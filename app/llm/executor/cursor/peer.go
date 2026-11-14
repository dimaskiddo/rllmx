package cursor

import (
	"io"
	"regexp"
	"sync/atomic"
	"time"
)

// peer is the request side of the exchange: the run frame goes out first, then
// the answers the service waits for. The body the HTTP client sends is a pipe,
// so a reply reaches the service while its answer is still being read.
type peer struct {
	pr *io.PipeReader
	pw *io.PipeWriter

	closed atomic.Bool
}

// newPeer is a pipe nothing has written yet.
func newPeer() *peer {
	pr, pw := io.Pipe()
	return &peer{pr: pr, pw: pw}
}

// reader is the body the request carries.
func (p *peer) reader() io.Reader { return p.pr }

// write sends one frame, or drops it once the exchange is over. It holds no lock
// across the pipe write: a stalled stream blocks it, and close is what frees it.
func (p *peer) write(b []byte) error {
	if p.closed.Load() {
		return nil
	}
	_, err := p.pw.Write(b)
	if err != nil && p.closed.Load() {
		return nil
	}
	return err
}

// close ends the request body and releases a write waiting on it.
func (p *peer) close() {
	if p.closed.Swap(true) {
		return
	}
	_ = p.pw.CloseWithError(io.ErrClosedPipe)
}

// startReader reads the turn ahead of the caller, so the wait for the first
// delta can end on a timer without losing what arrives later.
type startReader struct {
	src   io.Reader
	ch    chan readChunk
	done  chan struct{}
	first bool
	buf   []byte
	err   error
}

// readChunk is one read of the turn.
type readChunk struct {
	data []byte
	err  error
}

// newStartReader starts reading the turn.
func newStartReader(r io.Reader) *startReader {
	s := &startReader{src: r, ch: make(chan readChunk, 1), done: make(chan struct{}), first: true}
	go s.pump()
	return s
}

// pump reads the turn until it ends or the reader is closed.
func (s *startReader) pump() {
	for {
		buf := make([]byte, 8<<10)
		n, err := s.src.Read(buf)
		select {
		case s.ch <- readChunk{data: buf[:n], err: err}:
		case <-s.done:
			return
		}
		if err != nil {
			return
		}
	}
}

// Read gives the next bytes without waiting beyond the first.
func (s *startReader) Read(p []byte) (int, error) { return s.Wait(p, 0) }

// Wait gives the next bytes, waiting at most idle for them.
func (s *startReader) Wait(p []byte, idle time.Duration) (int, error) {
	for {
		if len(s.buf) > 0 {
			n := copy(p, s.buf)
			s.buf = s.buf[n:]
			return n, nil
		}
		if s.err != nil {
			return 0, s.err
		}
		var expire <-chan time.Time
		if idle > 0 && s.first {
			t := time.NewTimer(idle)
			defer t.Stop()
			expire = t.C
		}
		select {
		case c := <-s.ch:
			s.first = false
			s.buf, s.err = c.data, c.err
		case <-expire:
			return 0, nil
		}
	}
}

// close ends the read ahead: the pump stops waiting to hand over a chunk, and
// the source is closed so a read it is blocked in returns.
func (s *startReader) close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	if c, ok := s.src.(io.Closer); ok {
		_ = c.Close()
	}
}

// composerModel reports a model whose answer arrives inside its thinking text.
var composerModelRE = regexp.MustCompile(`(?i)^composer(?:-|$)`)

func composerModel(model string) bool {
	name := model
	if i := lastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return composerModelRE.MatchString(name)
}
