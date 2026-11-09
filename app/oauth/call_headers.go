package oauth

import (
	"maps"
	"strings"

	"rllmx/app/upstream"
)

// deviceIDKey is the provider data key that holds an account's device id.
const deviceIDKey = "deviceId"

// withEntry returns a copy of h with one more entry. The original is not
// changed, because a flow's headers are shared by every account.
func withEntry(h map[string]string, k, v string) map[string]string {
	out := make(map[string]string, len(h)+1)
	maps.Copy(out, h)
	out[k] = v
	return out
}

// callHeaders are the headers the router sends to a provider while logging in,
// renewing a token and running a hook. The device id placeholder is filled from
// the account; with none it is left out rather than sent as text.
func callHeaders(f Flow, deviceID string) map[string]string {
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
