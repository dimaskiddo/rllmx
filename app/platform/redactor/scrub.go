// Package redactor removes secrets from what is logged. The framework's log
// channels write whatever they are given, so a custom channel wraps the file
// handler with this one and no secret reaches a file.
package redactor

import (
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// Redacted replaces a value that must not be logged.
const Redacted = "[redacted]"

// maxDepth bounds the walk through nested values. Below it everything is
// replaced, which also ends a map that contains itself.
const maxDepth = 16

// sensitiveKeys are matched as substrings of a key after lower-casing and
// turning '-' into '_'.
var sensitiveKeys = []string{
	"authorization", "cookie", "token", "password", "secret",
	"api_key", "apikey", "verifier", "credential", "private_key", "session",
}

var valuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`eyJ[\w-]+\.[\w-]+\.[\w-]+`),
	regexp.MustCompile(`AIza[\w-]{20,}`),
}

var (
	queryKey = regexp.MustCompile(`(?i)((?:^|[?&])(?:key|code|code_verifier|access_token|refresh_token|id_token|client_secret)=)[^&\s"]+`)
	userInfo = regexp.MustCompile(`(://)[^/\s:@]+:[^/\s@]+@`)
)

var (
	// queryTrace is the framework query logger's prefix. Its statement has every
	// bound value inlined, so the literals are masked and the shape stays.
	queryTrace = regexp.MustCompile(`^\[[\d.]+ms\] \[rows:[^\]]*\] `)
	sqlLiteral = regexp.MustCompile(`'(?:[^']|'')*'`)
)

// SensitiveName reports whether a key names a secret.
func SensitiveName(name string) bool {
	name = strings.ReplaceAll(strings.ToLower(name), "-", "_")
	for _, k := range sensitiveKeys {
		if strings.Contains(name, k) {
			return true
		}
	}
	return false
}

// Scrub removes the secrets that have a recognizable shape from text: keys,
// bearer tokens, JWTs, Google API keys, secret query parameters and the
// credentials in a URL.
func Scrub(s string) string {
	if queryTrace.MatchString(s) {
		s = sqlLiteral.ReplaceAllString(s, "'"+Redacted+"'")
	}
	for _, re := range valuePatterns {
		s = re.ReplaceAllString(s, Redacted)
	}
	s = queryKey.ReplaceAllString(s, "${1}"+Redacted)
	return userInfo.ReplaceAllString(s, "${1}"+Redacted+"@")
}

// Value returns v without secrets. A sensitive key hides any value except a
// number, a boolean, a duration or a time, so counters such as max_tokens stay
// readable. Maps and slices are copied, never changed.
func Value(key string, v any) any {
	return walk(key, v, 0)
}

// Map returns m with every value passed through Value under its own key.
func Map(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	return walkMap(m, 0)
}

func walk(key string, v any, depth int) any {
	if exempt(v) {
		return v
	}
	if depth >= maxDepth || SensitiveName(key) {
		return Redacted
	}
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return Scrub(x)
	case error:
		return Scrub(x.Error())
	case []byte:
		return Scrub(string(x))
	case map[string]any:
		return walkMap(x, depth+1)
	case map[string]string:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = walk(k, e, depth+1)
		}
		return out
	case http.Header:
		return walkHeader(x)
	case map[string][]string:
		return walkHeader(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = walk(key, e, depth+1)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, e := range x {
			out[i] = Scrub(e)
		}
		return out
	}
	return viaText(v)
}

func walkMap(m map[string]any, depth int) map[string]any {
	out := make(map[string]any, len(m))
	for k, e := range m {
		out[k] = walk(k, e, depth)
	}
	return out
}

func walkHeader(h map[string][]string) map[string]any {
	out := make(map[string]any, len(h))
	for k, vs := range h {
		if SensitiveName(k) {
			out[k] = Redacted
			continue
		}
		scrubbed := make([]string, len(vs))
		for i, e := range vs {
			scrubbed[i] = Scrub(e)
		}
		out[k] = scrubbed
	}
	return out
}

func exempt(v any) bool {
	switch v.(type) {
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, time.Duration, time.Time:
		return true
	}
	return false
}

// viaText handles any other type through the text a handler would print for
// it. A byte slice of a named type, such as json.RawMessage, is read as text:
// printed as numbers it would hide a secret from the patterns.
func viaText(v any) any {
	var text string
	if rv := reflect.ValueOf(v); rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
		text = string(rv.Bytes())
		return Scrub(text)
	}
	text = fmt.Sprint(v)
	if scrubbed := Scrub(text); scrubbed != text {
		return scrubbed
	}
	return v
}
