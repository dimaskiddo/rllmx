package account

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/ttl"
	"rllmx/app/store/document"
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

// settingsLoadTimeout bounds one read of the settings row, which no caller's
// context owns.
const settingsLoadTimeout = 10 * time.Second

// newSettingsCache reuses the settings read for SettingsTTL. A failed read is
// returned and not cached.
func newSettingsCache(src SettingsReader, now func() time.Time) *settingsCache {
	return &settingsCache{v: ttl.New(SettingsTTL, now, src.Effective)}
}

type settingsCache struct{ v *ttl.Value[document.Doc] }

func (c *settingsCache) get(ctx context.Context) (document.Doc, error) {
	return c.v.Get(ctx, settingsLoadTimeout)
}

func (c *settingsCache) drop() { c.v.Drop() }

// strategyFor is the provider's own strategy, else the global one, else fill-first.
// A name this version does not know behaves as fill-first.
func strategyFor(s document.Doc, provider string) string {
	if raw, ok := s.GetRaw("providerStrategies"); ok {
		entry := gjson.Get(raw, escapeKey(provider))
		// The settings write accepts the bare word as well as the object, so a
		// row saved either way must select the same way.
		if entry.Type == gjson.String && entry.String() != "" {
			return known(entry.String())
		}
		if v := entry.Get("fallbackStrategy").String(); v != "" {
			return known(v)
		}
	}
	if v, _ := s.GetString("fallbackStrategy"); v != "" {
		return known(v)
	}
	return strategyFill
}

// KnownStrategy reports whether name is one the selector acts on. The settings
// write refuses any other, because a stored name that only behaves as fill-first
// looks saved and does nothing.
func KnownStrategy(name string) bool {
	return name == strategyFill || name == strategyRound
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
