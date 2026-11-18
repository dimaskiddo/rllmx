package quota

import (
	"context"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const clineUsageURL = "https://api.cline.bot/api/v1/users/me/plan/usage-limits"

var clineWindows = map[string]string{
	"five_hour": "Session (5h)",
	"weekly":    "Weekly (7d)",
	"monthly":   "Monthly",
}

// readCline reads a Cline plan's windows. A login's token is a WorkOS token and
// goes with its marker; a pasted key goes as it is. Both rules are the data
// file's.
func readCline(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := identity(e)
	if a.Conn.AuthType == "oauth" || a.Conn.AuthType == "access_token" {
		// The usage call is the product's own, so it names itself as the client.
		h["Authorization"] = "Bearer " + upstream.WorkOSToken(a.Token)
		h["HTTP-Referer"] = "https://cline.bot"
		h["X-Title"] = "Cline"
	} else {
		h = keyed(e, h, a.Token)
	}
	raw, stop, early, earlyErr := r.getOK(ctx, "Cline", request{URL: clineUsageURL, Headers: h})
	if stop {
		return early, earlyErr
	}
	out := Reading{Quotas: map[string]Quota{}, Plan: "Cline"}
	for _, l := range gjson.GetBytes(raw, "data.limits").Array() {
		label, ok := clineWindows[l.Get("type").String()]
		if !ok {
			continue
		}
		q := share(l.Get("percentUsed").Float(), stampOf(l.Get("resetsAt")))
		q.DisplayName, q.Recurring = label, true
		out.Quotas[label] = q
	}
	if len(out.Quotas) == 0 {
		out.Message = "Cline reported no quota"
	}
	return out, nil
}
