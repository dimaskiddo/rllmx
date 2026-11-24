package oauth

import (
	"maps"
	"strings"

	"rllmx/app/llm/upstream"
)

// deviceIDKey is the provider data key that holds an account's device id.
const deviceIDKey = "deviceId"

// WithEntry returns a copy of h with one more entry. The original is not
// changed, because a flow's headers are shared by every account.
func WithEntry(h map[string]string, k, v string) map[string]string {
	out := make(map[string]string, len(h)+1)
	maps.Copy(out, h)
	out[k] = v
	return out
}

// WithIdentity returns h with an identity laid over it: a header the provider
// asks for by name replaces one the flow's data file set, and h is left as it was.
func WithIdentity(h, identity map[string]string) map[string]string {
	out := make(map[string]string, len(h)+len(identity))
	maps.Copy(out, h)
	maps.Copy(out, identity)
	return out
}

// CallHeaders are the headers the router sends to a provider while logging in,
// renewing a token and running a hook. The device id placeholder is filled from
// the account; with none it is left out rather than sent as text.
func CallHeaders(f Flow, deviceID string) map[string]string {
	if len(f.CallHeaders) == 0 {
		return nil
	}
	out := make(map[string]string, len(f.CallHeaders))
	for k, v := range f.CallHeaders {
		if strings.Contains(v, upstream.DeviceIDToken) {
			if deviceID == "" {
				continue
			}
			v = strings.ReplaceAll(v, upstream.DeviceIDToken, deviceID)
		}
		out[k] = v
	}
	return out
}
