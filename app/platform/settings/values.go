package settings

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/goravel/framework/contracts/config"
)

// duration reads a time setting. A value that cannot be read or is out of range
// falls back to its default and is reported: none of these settings decides
// where the program connects or whom it trusts.
func duration(cfg config.Config, key, env string, lo, hi, def time.Duration, n *Notices) time.Duration {
	v := cfg.Get(key)
	if v == nil {
		return def
	}
	d, ok := v.(time.Duration)
	if !ok {
		raw := strings.TrimSpace(fmt.Sprint(v))
		if raw == "" {
			return def
		}
		var err error
		if d, err = time.ParseDuration(raw); err != nil {
			n.warn(env, fmt.Sprintf("must be a duration between %s and %s", lo, hi), def.String())
			return def
		}
	}
	if d < lo || d > hi {
		n.warn(env, fmt.Sprintf("must be a duration between %s and %s", lo, hi), def.String())
		return def
	}
	return d
}

// number reads an integer setting within its bounds, with the same fallback.
func number(cfg config.Config, key, env string, lo, hi, def int, n *Notices) int {
	v := cfg.Get(key)
	if v == nil {
		return def
	}
	var got int
	switch x := v.(type) {
	case int:
		got = x
	case int64:
		got = int(x)
	default:
		raw := strings.TrimSpace(fmt.Sprint(x))
		if raw == "" {
			return def
		}
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			n.warn(env, fmt.Sprintf("must be a number between %d and %d", lo, hi), strconv.Itoa(def))
			return def
		}
		got = parsed
	}
	if got < lo || got > hi {
		n.warn(env, fmt.Sprintf("must be between %d and %d", lo, hi), strconv.Itoa(def))
		return def
	}
	return got
}

// switchOf reads an on-or-off setting. The framework's own bool reader turns any
// word it does not know into false, which would switch a feature off on a typo,
// so the text is read here; an unreadable one uses the default and is reported.
func switchOf(cfg config.Config, key, env string, def bool, n *Notices) bool {
	v := cfg.Get(key)
	if b, ok := v.(bool); ok {
		return b
	}
	raw := ""
	if v != nil {
		raw = strings.TrimSpace(fmt.Sprint(v))
	}
	if raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		n.warn(env, "must be true or false", strconv.FormatBool(def))
		return def
	}
	return b
}
