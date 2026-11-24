package upstream

import (
	"strings"

	"github.com/tidwall/gjson"
)

// ClaudeVersion is the Anthropic API version a Claude-format provider sends
// when its data names no auth block.
const ClaudeVersion = "2023-06-01"

// BuiltinHeaders is the provider's own header set, as the dashboard offers it
// for editing: those a key request carries, and those a login adds on top. The
// beta list is left out because its value is computed per model and client, and
// internal data keys are left out because no request carries them.
func (e Entry) BuiltinHeaders() map[string]string {
	out := map[string]string{}
	add := func(m map[string]string) {
		for name, value := range m {
			if strings.HasPrefix(name, cliAgentHeaderPrefix) || ReservedHeader(name) {
				continue
			}
			out[name] = value
		}
	}
	add(e.Transport.Headers)
	switch {
	case e.Auth != nil:
		add(e.Auth.Headers)
	case e.Transport.Format == FormatClaude:
		out["anthropic-version"] = ClaudeVersion
	}
	if e.OAuth != nil {
		add(e.OAuth.Headers)
		add(e.OAuth.Identity)
	}
	return out
}

// cliAgentHeaderPrefix marks the data keys that hold a header template rather
// than a header a request sends.
const cliAgentHeaderPrefix = "X-Rllmx-"

// HeaderOverrides reads the operator's extra headers for one provider out of the
// stored providerOverrides document. Non-string values are skipped. The provider
// key is matched by walking the object, so no character in it needs escaping.
func HeaderOverrides(raw, provider string) map[string]string {
	out := map[string]string{}
	gjson.Parse(raw).ForEach(func(k, v gjson.Result) bool {
		if k.String() != provider {
			return true
		}
		v.Get("headers").ForEach(func(name, val gjson.Result) bool {
			if val.Type == gjson.String {
				out[name.String()] = val.String()
			}
			return true
		})
		return false
	})
	return out
}
