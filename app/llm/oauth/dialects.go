package oauth

// Dialects are the provider-specific steps a login may name: the code-exchange
// pair, the device-login pair, the hook that reads who the tokens belong to and
// the hook that runs after a renewal. The engine looks a name up here and
// follows the standard call when it has none, so a provider never changes the
// engine.
type Dialects struct {
	Code    map[string]CodeDialect
	Device  map[string]DeviceDialect
	Hooks   map[string]Hook
	Refresh map[string]RefreshHook
}
