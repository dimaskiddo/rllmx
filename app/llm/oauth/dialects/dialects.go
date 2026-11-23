// Package dialects is the one table of login steps the data files may name. It
// is the only place that imports the provider packages, the same way the
// executor's mux builds its table.
package dialects

import (
	"context"

	"rllmx/app/llm/oauth"
	"rllmx/app/llm/oauth/cline"
	"rllmx/app/llm/oauth/codebuddy"
	"rllmx/app/llm/oauth/freebuff"
	"rllmx/app/llm/oauth/github"
	"rllmx/app/llm/oauth/gitlab"
	"rllmx/app/llm/oauth/google"
	"rllmx/app/llm/oauth/kilocode"
	"rllmx/app/llm/oauth/kimchi"
	"rllmx/app/llm/oauth/kiro"
	"rllmx/app/llm/oauth/minimax"
	"rllmx/app/llm/oauth/muse"
	"rllmx/app/llm/oauth/nous"
	"rllmx/app/llm/oauth/qoder"
	"rllmx/app/llm/oauth/xai"
	"rllmx/app/llm/oauth/zcode"
	"rllmx/app/llm/oauth/zed"
)

// Default is every step the data files may name. A test keeps the names equal
// to the ones upstream publishes.
func Default() oauth.Dialects {
	return oauth.Dialects{
		Code: map[string]oauth.CodeDialect{
			"cline": {Exchange: cline.Exchange, Refresh: cline.Refresh},
			"kiro-social": {
				Exchange: kiro.SocialExchange, Refresh: kiro.SocialRefresh,
				CustomURL: kiro.SocialAuthorize, Variant: kiro.Variant, Paste: true,
				Variants:    kiro.Variants(),
				RenewParams: map[string]string{"idp": "authMethod"},
			},
			"zed":    {Exchange: zed.Exchange, Refresh: zed.NoRefresh, Authorize: zed.Authorize},
			"kimchi": {Exchange: kimchi.Exchange, Refresh: kimchi.NoRefresh},
		},
		Device: map[string]oauth.DeviceDialect{
			"freebuff":  {Start: freebuff.Start, Poll: freebuff.Poll},
			"qoder":     {Start: qoder.Start, Poll: qoder.Poll, Refresh: qoder.NoRefresh},
			"kilocode":  {Start: kilocode.Start, Poll: kilocode.Poll},
			"codebuddy": {Start: codebuddy.Start, Poll: codebuddy.Poll, Refresh: codebuddy.Refresh},
			"minimax":   {Start: minimax.Start, Poll: minimax.Poll},
			"zcode":     {Start: zcode.Start, Poll: zcode.Poll},
			"kiro":      {Start: kiro.Start, Poll: kiro.Poll, Refresh: kiro.Refresh},
		},
		Hooks: map[string]oauth.Hook{
			"codex": func(_ context.Context, _ *oauth.TokenClient, _ oauth.Flow, t oauth.Tokens, _ map[string]string) (oauth.Account, error) {
				return oauth.CodexAccount(t), nil
			},
			"github":      github.Account,
			"muse":        muse.Account,
			"nous":        nous.Account,
			"kilocode":    kilocode.Account,
			"grokCli":     xai.Grok,
			"xai":         xai.Xai,
			"gitlab":      gitlab.Account,
			"google":      google.Account,
			"antigravity": google.AntigravityAccount,
			"kiro":        kiro.Account,
			"kiroSocial":  kiro.SocialAccount,
			"zed":         zed.Account,
			"freebuff":    freebuff.Account,
			"qoder":       qoder.Account,
			"kimchi":      kimchi.Account,
		},
		Refresh: map[string]oauth.RefreshHook{"nous": nous.Renewal},
	}
}
