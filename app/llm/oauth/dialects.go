package oauth

import "context"

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

// DefaultDialects is every step the data files may name. A test keeps the names
// equal to the ones upstream publishes.
func DefaultDialects() Dialects {
	return Dialects{
		Code: map[string]CodeDialect{
			"cline":       {exchange: clineExchange, refresh: clineRefresh},
			"kiro-social": {exchange: kiroSocialExchange, refresh: kiroSocialRefresh},
			"zed":         {exchange: zedExchange, refresh: zedNoRefresh},
			"kimchi":      {exchange: kimchiExchange, refresh: kimchiNoRefresh},
		},
		Device: map[string]DeviceDialect{
			"freebuff": {start: freebuffStart, poll: freebuffPoll},
			"qoder":    {start: qoderStart, poll: qoderPoll, refresh: qoderNoRefresh},
			"kilocode": {start: kiloStart, poll: func(ctx context.Context, c *TokenClient, f Flow, s DeviceState, h map[string]string) (Tokens, PollState, error) {
				return kiloPoll(ctx, c, f, s.Code, h)
			}},
			"codebuddy": {start: codebuddyStart, poll: codebuddyPoll, refresh: codebuddyRefresh},
			"minimax":   {start: minimaxStart, poll: minimaxPoll},
			"zcode":     {start: zcodeStart, poll: zcodePoll},
			"kiro":      {start: kiroStart, poll: kiroPoll, refresh: kiroRefresh},
		},
		Hooks:   defaultHooks(),
		Refresh: map[string]RefreshHook{"nous": nousRenewal},
	}
}

// empty reports a table nothing was laid into.
func (d Dialects) empty() bool {
	return d.Code == nil && d.Device == nil && d.Hooks == nil && d.Refresh == nil
}
