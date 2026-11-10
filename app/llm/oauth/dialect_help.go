package oauth

import (
	"maps"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/text"
)

// paramOf is the value of a named data parameter, or "".
func paramOf(f Flow, name string) string {
	for _, p := range f.Params {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// withEntries copies h and adds the given headers.
func withEntries(h, add map[string]string) map[string]string {
	out := make(map[string]string, len(h)+len(add))
	maps.Copy(out, h)
	maps.Copy(out, add)
	return out
}

// objectOf parses an answer that must be a JSON object.
func objectOf(raw []byte) (gjson.Result, bool) {
	if !gjson.ValidBytes(raw) {
		return gjson.Result{}, false
	}
	r := gjson.ParseBytes(raw)
	return r, r.IsObject()
}

// bearerText reports a token fit to be stored and sent: not empty, bounded, no
// control character. JWTs are longer than credentialText allows.
func bearerText(s string) bool {
	return s != "" && len(s) <= maxBearer && !text.HasASCIIControl(s) && !strings.ContainsAny(s, " \t")
}
