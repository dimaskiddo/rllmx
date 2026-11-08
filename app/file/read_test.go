package file

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadCappedReturnsWhatFitsAndRefusesWhatDoesNot(t *testing.T) {
	for _, c := range []struct {
		in    string
		limit int64
		want  string
		err   error
	}{
		{"", 5, "", nil},
		{"abc", 5, "abc", nil},
		{"abcde", 5, "abcde", nil},
		{"abcdef", 5, "abcdef", ErrTooLarge},
		{"abcdefghij", 5, "abcdef", ErrTooLarge},
		{"x", 0, "x", ErrTooLarge},
		{"", 0, "", nil},
		{"x", -3, "x", ErrTooLarge},
	} {
		got, err := ReadCapped(strings.NewReader(c.in), c.limit)
		if string(got) != c.want || !errors.Is(err, c.err) {
			t.Errorf("%q limit %d: got %q %v, want %q %v", c.in, c.limit, got, err, c.want, c.err)
		}
	}
}

func TestReadCappedReadsAnEndlessReaderInBoundedMemory(t *testing.T) {
	endless := io.LimitReader(zeros{}, 1<<30)
	got, err := ReadCapped(endless, 1<<16)
	if !errors.Is(err, ErrTooLarge) || len(got) != 1<<16+1 {
		t.Fatalf("%d bytes, %v", len(got), err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestReadCappedPassesAReadErrorThrough(t *testing.T) {
	boom := errors.New("boom")
	if _, err := ReadCapped(iotest.ErrReader(boom), 10); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}
