package file

import (
	"errors"
	"io"
)

// ErrTooLarge means a reader held more than the limit it was given.
var ErrTooLarge = errors.New("file: more data than the limit")

// ReadCapped reads r to its end and fails with ErrTooLarge when it holds more
// than limit bytes. The first limit+1 bytes are read at most, so an endless
// reader costs a bounded buffer. On ErrTooLarge the bytes read so far are
// returned too, for a caller that only wants to know the size was passed.
func ReadCapped(r io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		limit = 0
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return data, err
	}
	if int64(len(data)) > limit {
		return data, ErrTooLarge
	}
	return data, nil
}

// ReadUpTo reads at most limit bytes of r and returns them, stopping there
// without an error when r holds more. It is for an answer whose first part is
// all that is wanted, such as an error body; ReadCapped is for one that must be
// whole or refused.
func ReadUpTo(r io.Reader, limit int64) []byte {
	if limit < 0 {
		limit = 0
	}
	data, _ := io.ReadAll(io.LimitReader(r, limit))
	return data
}
