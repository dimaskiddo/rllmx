package quota

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// openCodeUsageURL is the plan address of each OpenCode service.
var openCodeUsageURL = map[string]string{
	"opencode-go":  "https://opencode.ai/inference/go/v1/usage",
	"opencode-zen": "https://opencode.ai/zen/v1/usage",
}

var openCodeWindows = []struct{ key, label string }{
	{"rolling", "Rolling"},
	{"weekly", "Weekly"},
	{"monthly", "Monthly"},
}

// readOpenCode reads an OpenCode plan's three windows.
func readOpenCode(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	target, ok := openCodeUsageURL[a.Conn.Provider]
	if !ok {
		return Reading{}, ErrUnsupported
	}
	code, raw, err := r.do(ctx, request{URL: target, Headers: keyed(e, identity(e), a.Token)})
	if err != nil {
		return failed("OpenCode", err)
	}
	if code != http.StatusOK {
		return answered("OpenCode", code)
	}
	out := Reading{Quotas: map[string]Quota{}, Plan: a.Conn.Provider}
	for _, w := range openCodeWindows {
		q := gjson.GetBytes(raw, "usage."+w.key)
		if !q.Exists() {
			continue
		}
		row := share(q.Get("percent").Float(), stampOf(q.Get("resetsAt")))
		row.DisplayName, row.Recurring = w.label, true
		out.Quotas[w.label] = row
	}
	if len(out.Quotas) == 0 {
		out.Message = "OpenCode reported no quota"
	}
	return out, nil
}
