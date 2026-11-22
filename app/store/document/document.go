// Package document edits a stored JSON object without rewriting the bytes it
// does not touch, so unknown keys, key order and number formatting survive a
// round trip.
package document

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/store/dialect"
)

// ErrCorruptRow means a stored JSON document does not parse (or has a
// duplicate top-level key). Such a row is kept as raw text and never rewritten.
var ErrCorruptRow = errors.New("document: corrupt JSON row")

// Doc edits a stored JSON object in place. It keeps the original text and
// changes only the bytes of the keys it touches, so everything else, including
// key order, number formatting and unknown fields, stays identical.
type Doc struct{ raw string }

// ParseDoc returns ErrCorruptRow for text that does not parse, is not an object,
// or repeats a top-level key.
func ParseDoc(text string) (Doc, error) {
	if !gjson.Valid(text) || !gjson.Parse(text).IsObject() {
		return Doc{raw: text}, ErrCorruptRow
	}
	seen := map[string]struct{}{}
	dup := false
	gjson.Parse(text).ForEach(func(k, _ gjson.Result) bool {
		if _, ok := seen[k.String()]; ok {
			dup = true
			return false
		}
		seen[k.String()] = struct{}{}
		return true
	})
	if dup {
		return Doc{raw: text}, ErrCorruptRow
	}
	return Doc{raw: text}, nil
}

// From wraps text as it is stored, valid or not. A caller that has already read
// a row keeps its bytes rather than failing on a document this version cannot
// parse; ErrCorruptRow on the read path is what reports the problem.
func From(text string) Doc { return Doc{raw: text} }

// String returns the stored JSON, byte for byte.
func (d Doc) String() string { return d.raw }

// pathKey addresses a top-level key literally: every ASCII byte that is not a
// letter, digit, '_' or '-' is backslash-escaped, so a model id such as
// "google/gemma-4-31b-it:free" or "a.b" is one key, not a path.
func pathKey(k string) string {
	var b strings.Builder
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c < 0x80 && !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Has reports whether the top-level key exists (even with a null value).
func (d Doc) Has(key string) bool { return gjson.Get(d.raw, pathKey(key)).Exists() }

// IsNull reports whether the key exists with a null value.
func (d Doc) IsNull(key string) bool {
	r := gjson.Get(d.raw, pathKey(key))
	return r.Exists() && r.Type == gjson.Null
}

// GetString returns a string value; ok is false for a missing or non-string key.
func (d Doc) GetString(key string) (string, bool) {
	r := gjson.Get(d.raw, pathKey(key))
	if !r.Exists() || r.Type != gjson.String {
		return "", false
	}
	return r.String(), true
}

// GetRaw returns the key's value as JSON text, and whether the key exists. The
// bytes are the stored ones, not a re-encoding.
func (d Doc) GetRaw(key string) (string, bool) {
	r := gjson.Get(d.raw, pathKey(key))
	return r.Raw, r.Exists()
}

// Keys returns the top-level keys in stored order.
func (d Doc) Keys() []string {
	var keys []string
	gjson.Parse(d.raw).ForEach(func(k, _ gjson.Result) bool {
		keys = append(keys, k.String())
		return true
	})
	return keys
}

// SetRaw sets a top-level key to valid JSON.
func (d *Doc) SetRaw(key, rawJSON string) error {
	if !gjson.Valid(rawJSON) {
		return fmt.Errorf("document: invalid JSON value for %q", key)
	}
	out, err := sjson.SetRaw(d.raw, pathKey(key), rawJSON)
	if err != nil {
		return err
	}
	d.raw = out
	return nil
}

// SetString stores a string. HTML characters and U+2028 stay as written, which
// is what JavaScript's JSON.stringify writes (encoding/json would escape them).
func (d *Doc) SetString(key, val string) error {
	return d.SetRaw(key, Encode(dialect.StripNUL(val)))
}

// SetInt sets a top-level key to a number.
func (d *Doc) SetInt(key string, v int64) error {
	b, _ := json.Marshal(v)
	return d.SetRaw(key, string(b))
}

// SetNull sets a top-level key to null.
func (d *Doc) SetNull(key string) error { return d.SetRaw(key, "null") }

// Delete removes a key; an absent key is a no-op.
func (d *Doc) Delete(key string) error {
	out, err := sjson.Delete(d.raw, pathKey(key))
	if err != nil {
		return err
	}
	d.raw = out
	return nil
}

// Encode renders s as a JSON string literal the way JSON.stringify does: HTML
// characters and U+2028 stay as they are, which encoding/json would escape.
func Encode(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// GetInt returns an integer value; ok is false for a missing or non-number key.
func (d Doc) GetInt(key string) (int64, bool) {
	r := gjson.Get(d.raw, pathKey(key))
	if !r.Exists() || r.Type != gjson.Number {
		return 0, false
	}
	return r.Int(), true
}

// Str returns a string value, or def when the key is missing or is not a
// string. The typed getters below share that rule, so a hand-edited row of the
// wrong shape reads as unset and never as a surprise.
func (d Doc) Str(key, def string) string {
	if v, ok := d.GetString(key); ok {
		return v
	}
	return def
}

// Bool returns a boolean value, or def for a missing or non-boolean key.
func (d Doc) Bool(key string, def bool) bool {
	r := gjson.Get(d.raw, pathKey(key))
	if r.Type == gjson.True || r.Type == gjson.False {
		return r.Bool()
	}
	return def
}

// Int returns an integer value, or def for a missing or non-number key.
func (d Doc) Int(key string, def int) int {
	if n, ok := d.GetInt(key); ok {
		return int(n)
	}
	return def
}

// Float returns a number value, or def for a missing or non-number key.
func (d Doc) Float(key string, def float64) float64 {
	r := gjson.Get(d.raw, pathKey(key))
	if r.Type == gjson.Number {
		return r.Float()
	}
	return def
}

// FirstString returns the first of vals that is a non-empty JSON string, or "".
func FirstString(vals ...gjson.Result) string {
	for _, v := range vals {
		if v.Type == gjson.String && v.String() != "" {
			return v.String()
		}
	}
	return ""
}
