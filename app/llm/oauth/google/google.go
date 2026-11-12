package google

import (
	"context"
	"rllmx/app/llm/oauth"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

const (
	// googleMetadata is the client the Cloud Code calls say they are: an
	// Antigravity IDE plugin on macOS arm64. The host's own platform is never sent.
	googleMetadata = `{"ideType":9,"platform":2,"pluginType":2}`

	defaultTier   = "legacy-tier"
	onboardTries  = 4
	onboardPause  = 3 * time.Second
	maxTierLength = 64
)

// ValidProject reports a project id fit to be stored and sent.
func ValidProject(s string) bool { return upstream.ValidProject(s) }

// googleAccount names a Gemini CLI account from the user profile and stores the
// Cloud Code project. Both steps are best effort: a login whose calls fail still
// connects, and the project is looked up on the first request.
func Account(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, h map[string]string) (oauth.Account, error) {
	acc := oauth.Account{Email: googleEmail(ctx, c, f, t.Access, h)}
	acc.ProjectID, _ = loadCodeAssist(ctx, c, f, t.Access, h, true)
	return acc, nil
}

// antigravityAccount does the same for an Antigravity account, then onboards
// it: the project only works once the user is onboarded, and the provider says
// so with `done`. The wait is bounded, and a login whose onboarding does not
// finish still connects.
func AntigravityAccount(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, h map[string]string) (oauth.Account, error) {
	acc := oauth.Account{Email: googleEmail(ctx, c, f, t.Access, h)}
	project, tier := loadCodeAssist(ctx, c, f, t.Access, h, false)
	acc.ProjectID = project
	if project != "" {
		if onboarded := onboardUser(ctx, c, f, t.Access, h, tier); onboarded != "" {
			acc.ProjectID = onboarded
		}
	}
	return acc, nil
}

// googleEmail reads the account's address from the profile endpoint.
func googleEmail(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, access string, h map[string]string) string {
	u := f.Endpoints[upstream.EndpointUser]
	if u == "" {
		return ""
	}
	status, raw, err := c.Get(ctx, u+"?alt=json", oauth.WithEntry(h, "Authorization", "Bearer "+access))
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return ""
	}
	return oauth.CleanEmail(oauth.ProfileText(raw, "email"))
}

// loadCodeAssist asks for the account's project and its default tier. Gemini CLI
// also sends the mode its client sends.
func loadCodeAssist(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, access string, h map[string]string, withMode bool) (project, tier string) {
	u := f.Endpoints[upstream.EndpointLoadCodeAssist]
	if u == "" {
		return "", defaultTier
	}
	body := `{"metadata":` + googleMetadata + `}`
	if withMode {
		body = `{"metadata":` + googleMetadata + `,"mode":1}`
	}
	headers := oauth.WithEntry(h, "Authorization", "Bearer "+access)
	if withMode {
		// Gemini CLI says who it is in a header as well; Antigravity sends none,
		// because the backend refuses to provision a project when it does.
		headers["Client-Metadata"] = googleMetadata
	}
	status, raw, err := c.PostJSON(ctx, u, body, headers)
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return "", defaultTier
	}
	d := gjson.ParseBytes(raw)
	return projectOf(d.Get("cloudaicompanionProject")), tierOf(d)
}

// projectOf reads a project given as a string or as an object with an id.
func projectOf(v gjson.Result) string {
	id := v.String()
	if v.IsObject() {
		id = v.Get("id").String()
	}
	if id = strings.TrimSpace(id); ValidProject(id) {
		return id
	}
	return ""
}

// tierOf is the id of the default tier among the allowed ones.
func tierOf(d gjson.Result) string {
	for _, t := range d.Get("allowedTiers").Array() {
		id := strings.TrimSpace(t.Get("id").String())
		if t.Get("isDefault").Bool() && id != "" && len(id) <= maxTierLength && !strings.ContainsAny(id, " \t\r\n\"\\") {
			return id
		}
	}
	return defaultTier
}

// onboardUser asks the provider to enable the account, a few times and a few
// seconds apart, until it says it is done. It returns the project the answer
// names, or "" when it never finished or named none.
func onboardUser(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, access string, h map[string]string, tier string) string {
	u := f.Endpoints[upstream.EndpointOnboardUser]
	if u == "" {
		return ""
	}
	body := `{"tierId":` + document.Encode(tier) + `,"metadata":` + googleMetadata + `}`
	headers := oauth.WithEntry(h, "Authorization", "Bearer "+access)
	for try := range onboardTries {
		if try > 0 {
			if c.Wait(ctx, onboardPause) != nil {
				return ""
			}
		}
		if project, done := onboardOnce(ctx, c, u, body, headers); done {
			return project
		}
	}
	return ""
}

// onboardOnce makes one onboarding call. done says the provider reported the
// account enabled; project is the one it named, or "".
func onboardOnce(ctx context.Context, c *oauth.TokenClient, target, body string, headers map[string]string) (project string, done bool) {
	status, raw, err := c.PostJSON(ctx, target, body, headers)
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return "", false
	}
	d := gjson.ParseBytes(raw)
	if d.Get("done").Type != gjson.True {
		return "", false
	}
	return projectOf(d.Get("response.cloudaicompanionProject")), true
}
