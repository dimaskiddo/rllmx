package account

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/document"
)

const (
	// SettingsTTL is how long a read of the settings row is reused.
	SettingsTTL   = 5 * time.Second
	defaultSticky = 3
	strategyFill  = "fill-first"
	strategyRound = "round-robin"
)

// SettingsReader returns the effective settings document.
type SettingsReader interface {
	Effective(ctx context.Context) (document.Doc, error)
}

// settingsCache reuses the settings read for SettingsTTL. A failed read is
// returned and not cached.
type settingsCache struct {
	src SettingsReader
	now func() time.Time

	mu  sync.Mutex
	at  time.Time
	doc document.Doc
	ok  bool
}

func (c *settingsCache) get(ctx context.Context) (document.Doc, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && c.now().Sub(c.at) < SettingsTTL {
		return c.doc, nil
	}
	d, err := c.src.Effective(ctx)
	if err != nil {
		return document.Doc{}, err
	}
	c.doc, c.at, c.ok = d, c.now(), true
	return d, nil
}

func (c *settingsCache) drop() {
	c.mu.Lock()
	c.ok = false
	c.mu.Unlock()
}

// strategyFor is the provider's own strategy, else the global one, else fill-first.
// A name this version does not know behaves as fill-first.
func strategyFor(s document.Doc, provider string) string {
	if raw, ok := s.GetRaw("providerStrategies"); ok {
		if v := gjson.Get(raw, escapeKey(provider)+".fallbackStrategy").String(); v != "" {
			return known(v)
		}
	}
	if v, _ := s.GetString("fallbackStrategy"); v != "" {
		return known(v)
	}
	return strategyFill
}

func known(name string) string {
	if name == strategyRound {
		return strategyRound
	}
	return strategyFill
}

// stickyLimit is the provider's limit, else the global one, else 3. Zero,
// negative and unreadable values fall through to the next.
func stickyLimit(s document.Doc, provider string) int {
	if raw, ok := s.GetRaw("providerStrategies"); ok {
		if n := positive(gjson.Get(raw, escapeKey(provider)+".stickyRoundRobinLimit")); n > 0 {
			return n
		}
	}
	if raw, ok := s.GetRaw("stickyRoundRobinLimit"); ok {
		if n := positive(gjson.Parse(raw)); n > 0 {
			return n
		}
	}
	return defaultSticky
}

func positive(r gjson.Result) int {
	switch r.Type {
	case gjson.Number:
		if n := int(r.Int()); n > 0 {
			return n
		}
	case gjson.String:
		if n, err := strconv.Atoi(strings.TrimSpace(r.String())); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// escapeKey makes a provider id safe as one gjson path element.
func escapeKey(k string) string {
	var b strings.Builder
	for _, r := range k {
		if strings.ContainsRune(`.*?|#@\!=<>%`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
