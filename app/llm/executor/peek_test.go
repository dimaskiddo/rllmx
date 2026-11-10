package executor

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestPeekUntilStopsWhereTheCheckSaysAndReplaysEveryByte(t *testing.T) {
	body := io.NopCloser(strings.NewReader("one\ntwo\nthree\n"))
	status, rest, seen := PeekUntil(context.Background(), body, 0, func(seen []byte) (int, bool, bool) {
		return 0, false, strings.Contains(string(seen), "two")
	})
	if status != 0 || !strings.Contains(string(seen), "two") {
		t.Fatalf("status %d, seen %q", status, seen)
	}
	got, err := io.ReadAll(rest)
	if err != nil || string(got) != "one\ntwo\nthree\n" {
		t.Fatalf("replayed %q (%v), want every byte once", got, err)
	}
}

func TestPeekUntilARefusalClosesTheBodyAndReturnsItsStatus(t *testing.T) {
	closed := false
	body := &closeSpy{Reader: strings.NewReader("bad\n"), onClose: func() { closed = true }}
	status, rest, _ := PeekUntil(context.Background(), body, 0, func(seen []byte) (int, bool, bool) {
		return 429, true, false
	})
	if status != 429 || rest != nil || !closed {
		t.Fatalf("status %d, rest %v, closed %v", status, rest, closed)
	}
}

type closeSpy struct {
	io.Reader
	onClose func()
}

func (c *closeSpy) Close() error { c.onClose(); return nil }

func TestPeekUntilStopsOnTheDecidingChunkWithoutReadingFurther(t *testing.T) {
	body := &chunkBody{chunks: []string{"ready\n", "after\n"}}
	_, rest, _ := PeekUntil(context.Background(), body, 0, func(seen []byte) (int, bool, bool) {
		return 0, false, strings.Contains(string(seen), "ready")
	})
	if body.reads != 1 {
		t.Fatalf("%d reads, want the peek to stop at the chunk that decided", body.reads)
	}
	got, err := io.ReadAll(rest)
	if err != nil || string(got) != "ready\nafter\n" {
		t.Fatalf("replayed %q (%v)", got, err)
	}
}

type chunkBody struct {
	chunks []string
	reads  int
}

func (b *chunkBody) Read(p []byte) (int, error) {
	b.reads++
	if len(b.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.chunks[0])
	b.chunks = b.chunks[1:]
	return n, nil
}

func (b *chunkBody) Close() error { return nil }
