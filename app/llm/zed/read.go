package zed

import (
	"errors"
	"io"

	"rllmx/app/platform/file"
)

// maxCatalog bounds the catalog answer: a listing of every hosted model is far
// smaller than this.
const maxCatalog = 4 << 20

// readCapped reads an answer under the catalog's bound.
func readCapped(r io.Reader) ([]byte, error) {
	raw, err := file.ReadCapped(r, maxCatalog)
	if errors.Is(err, file.ErrTooLarge) {
		return nil, ErrRefused
	}
	if err != nil {
		return nil, ErrUnreachable
	}
	return raw, nil
}
