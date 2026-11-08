package providers

import (
	"context"
	"time"

	"rllmx/app/document"
	"rllmx/app/repository"
	"rllmx/app/ttl"
)

const (
	// settingsLife is how long a settings read is reused. A write through this
	// store drops it at once, so only another instance's write waits this long.
	settingsLife        = 5 * time.Second
	settingsLoadTimeout = 10 * time.Second
)

type settingsReader interface {
	Effective(ctx context.Context) (document.Doc, error)
}

// cachedSettings serves the effective settings of one store to the request path.
// The pipeline reads them several times per request (header overrides, topic
// naming), and each read is a query, a decryption and a merge of the defaults.
type cachedSettings struct{ v *ttl.Value[document.Doc] }

func newCachedSettings(src settingsReader) *cachedSettings {
	return &cachedSettings{v: ttl.New(settingsLife, nil, src.Effective)}
}

// Effective returns the settings from the cache while they are fresh.
func (c *cachedSettings) Effective(ctx context.Context) (document.Doc, error) {
	return c.v.Get(ctx, settingsLoadTimeout)
}

// settingsFor caches the settings of a store and drops the cache on every write
// made through it, so a dashboard change applies to the next request.
func settingsFor(store *repository.Store) *cachedSettings {
	c := newCachedSettings(store.Settings())
	store.OnSettingsChange(c.Drop)
	return c
}

// Drop forgets the cached read.
func (c *cachedSettings) Drop() { c.v.Drop() }
