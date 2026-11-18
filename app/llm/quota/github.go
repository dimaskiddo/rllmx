package quota

import (
	"context"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const githubUsageURL = "https://api.github.com/copilot_internal/user"

// readGitHub reads the Copilot plan of a GitHub login. The call carries the
// login's own token, not the Copilot one a chat request mints.
func readGitHub(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := identity(e)
	delete(h, "anthropic-version")
	if _, ok := h["X-GitHub-Api-Version"]; !ok {
		h["X-GitHub-Api-Version"] = "2022-11-28"
	}
	if h["User-Agent"] == "" {
		h["User-Agent"] = "GitHubCopilotChat/0.38.0"
	}
	h["Authorization"] = "token " + a.Token
	raw, stop, early, earlyErr := r.getOK(ctx, "GitHub", request{URL: githubUsageURL, Headers: h})
	if stop {
		return early, earlyErr
	}
	return parseGitHub(raw), nil
}

func parseGitHub(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: gjson.GetBytes(raw, "copilot_plan").String()}
	if snaps := gjson.GetBytes(raw, "quota_snapshots"); snaps.IsObject() {
		reset := stampOf(gjson.GetBytes(raw, "quota_reset_date"))
		for _, name := range []string{"chat", "completions", "premium_interactions"} {
			s := snaps.Get(name)
			if !s.IsObject() {
				continue
			}
			total := s.Get("entitlement").Float()
			q := window(total-s.Get("remaining").Float(), total, reset)
			q.Unlimited = q.Unlimited || s.Get("unlimited").Bool()
			q.DisplayName = githubName(name)
			out.Quotas[name] = q
		}
	}
	if len(out.Quotas) == 0 {
		monthly, used := gjson.GetBytes(raw, "monthly_quotas"), gjson.GetBytes(raw, "limited_user_quotas")
		if monthly.IsObject() || used.IsObject() {
			if out.Plan == "" {
				out.Plan = gjson.GetBytes(raw, "access_type_sku").String()
			}
			reset := stampOf(gjson.GetBytes(raw, "limited_user_reset_date"))
			for _, name := range []string{"chat", "completions"} {
				q := window(used.Get(name).Float(), monthly.Get(name).Float(), reset)
				q.DisplayName = githubName(name)
				out.Quotas[name] = q
			}
		}
	}
	if len(out.Quotas) == 0 {
		out.Message = "GitHub reported no quota"
	}
	return out
}

func githubName(key string) string {
	switch key {
	case "chat":
		return "Chat"
	case "completions":
		return "Completions"
	case "premium_interactions":
		return "Premium interactions"
	}
	return key
}
