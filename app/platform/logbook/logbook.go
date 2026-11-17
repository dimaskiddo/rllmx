// Package logbook keeps the last lines the logger wrote, for the dashboard's
// Console Log. It holds text the redacting handler already scrubbed.
package logbook

import (
	"sync"
	"time"

	"rllmx/app/platform/text"
)

// Size is how many lines the ring keeps.
const Size = 200

// maxLine bounds one stored line, so a logged blob cannot fill the ring with a
// single entry.
const maxLine = 4096

// Entry is one captured line.
type Entry struct {
	Seq     int64     `json:"seq"`
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Book is a bounded ring of recent log lines. A writer never blocks: the lock
// covers a slice write and nothing else.
type Book struct {
	mu     sync.Mutex
	buf    []Entry
	start  int
	count  int
	seq    int64
	header int64
}

// New builds a ring of the given size. A size below one means Size.
func New(size int) *Book {
	if size < 1 {
		size = Size
	}
	return &Book{buf: make([]Entry, size)}
}

// Add records one line. Control characters are stripped and an oversized
// message is cut, so a logged blob cannot forge a line or grow the ring.
func (b *Book) Add(level string, at time.Time, message string) {
	if b == nil {
		return
	}
	msg := text.CutRunes(text.StripControl(message), maxLine)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e := Entry{Seq: b.seq, At: at, Level: level, Message: msg}
	if b.count == len(b.buf) {
		b.buf[b.start] = e
		b.start = (b.start + 1) % len(b.buf)
		return
	}
	b.buf[(b.start+b.count)%len(b.buf)] = e
	b.count++
}

// Lines returns the newest limit entries, oldest first. A limit below one means
// every entry.
func (b *Book) Lines(limit int) []Entry {
	if b == nil {
		return []Entry{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lines(limit)
}

func (b *Book) lines(limit int) []Entry {
	if limit <= 0 || limit > b.count {
		limit = b.count
	}
	out := make([]Entry, 0, limit)
	for i := b.count - limit; i < b.count; i++ {
		out = append(out, b.buf[(b.start+i)%len(b.buf)])
	}
	return out
}

// Snapshot is everything the ring holds, with the generation a reader compares
// to notice a clear.
func (b *Book) Snapshot() ([]Entry, int64) {
	if b == nil {
		return []Entry{}, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lines(0), b.header
}

// Since returns the entries written after seq. When gen is not the current
// generation the ring was cleared, and the caller starts again from these
// entries.
func (b *Book) Since(seq, gen int64) ([]Entry, int64) {
	if b == nil {
		return []Entry{}, gen
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if gen != b.header {
		return b.lines(0), b.header
	}
	out := make([]Entry, 0, 8)
	for i := 0; i < b.count; i++ {
		if e := b.buf[(b.start+i)%len(b.buf)]; e.Seq > seq {
			out = append(out, e)
		}
	}
	return out, gen
}

// Clear empties the ring and moves its generation, which a stream reports as a
// clear frame.
func (b *Book) Clear() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.start, b.count = 0, 0
	b.header++
}

// Generation is the current generation; it moves on every Clear.
func (b *Book) Generation() int64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.header
}
