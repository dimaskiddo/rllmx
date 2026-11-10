package upstream

import "github.com/tidwall/gjson"

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
