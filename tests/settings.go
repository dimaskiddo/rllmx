package tests

import (
	"context"

	"rllmx/app/store/document"
)

// Settings is a settings reader over a fixed document, for a test that needs
// the selector or the pipeline to read settings without a store. The zero
// value is the empty document.
type Settings struct{ JSON string }

// Effective returns the document.
func (s Settings) Effective(context.Context) (document.Doc, error) {
	if s.JSON == "" {
		return document.From("{}"), nil
	}
	return document.From(s.JSON), nil
}
