package oauth

import (
	"maps"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/text"
)

// MaxMinted is the longest minted token a login may store.
const MaxMinted = 4096

// MaxBearer is the longest token a login may store and send.
const MaxBearer = 16 << 10

// Param is the value of a named data parameter, or "".
func Param(f Flow, name string) string {
	for _, p := range f.Params {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// WithEntries copies h and adds the given headers.
func WithEntries(h, add map[string]string) map[string]string {
	out := make(map[string]string, len(h)+len(add))
	maps.Copy(out, h)
	maps.Copy(out, add)
	return out
}

// ObjectOf parses an answer that must be a JSON object.
func ObjectOf(raw []byte) (gjson.Result, bool) {
	if !gjson.ValidBytes(raw) {
		return gjson.Result{}, false
	}
	r := gjson.ParseBytes(raw)
	return r, r.IsObject()
}

// BearerText reports a token fit to be stored and sent: not empty, bounded, no
// control character. JWTs are longer than credentialText allows.
func BearerText(s string) bool {
	return s != "" && len(s) <= MaxBearer && !text.HasASCIIControl(s) && !strings.ContainsAny(s, " \t")
}
