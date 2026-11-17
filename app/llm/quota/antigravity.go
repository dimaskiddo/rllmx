package quota

import (
	"context"
	"regexp"
	"strings"

	"rllmx/app/llm/upstream"
)

// antigravityLabels name the shared buckets the selector also reads.
var antigravityLabels = map[string]string{
	GeminiSession:    "Gemini session (5h)",
	GeminiWeekly:     "Gemini weekly (7d)",
	ArgonSession:     "Argon session (5h)",
	ArgonWeekly:      "Argon weekly (7d)",
	ClaudeGPTSession: "Claude and GPT session (5h)",
	ClaudeGPTWeekly:  "Claude and GPT weekly (7d)",
}

// readAntigravity asks the same calls the account selector's gate reads, so one
// account is never reported differently by the two.
func readAntigravity(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	if e.OAuth == nil {
		return Reading{}, ErrUnsupported
	}
	f := &Fetcher{Client: r.Client, URL: Endpoints{
		Models:    e.OAuth.Endpoints[upstream.EndpointQuotaModels],
		Summary:   e.OAuth.Endpoints[upstream.EndpointQuotaSummary],
		Load:      e.OAuth.Endpoints[upstream.EndpointLoadCodeAssist],
		UserAgent: e.Transport.Headers["User-Agent"],
		Version:   versionOf(e.Transport.Headers["User-Agent"]),
	}}
	return snapshotReading(f.Fetch(ctx, a.Conn.ID, a.Token)), nil
}

// versionOf reads the client version out of a user agent, as the calls send it.
func versionOf(ua string) string {
	m := uaVersion.FindStringSubmatch(ua)
	if m == nil {
		return ""
	}
	return m[1]
}

var uaVersion = regexp.MustCompile(`/([0-9][0-9.]*)`)

// snapshotReading turns the selector's buckets into the rows a dashboard shows.
func snapshotReading(snap Snapshot) Reading {
	out := Reading{Quotas: map[string]Quota{}}
	for name, b := range snap {
		if name == "" {
			continue
		}
		label, ok := antigravityLabels[name]
		if !ok {
			label = strings.ToUpper(name[:1]) + name[1:]
		}
		q := share(100-b.Remaining, b.ResetAt)
		q.DisplayName, q.Recurring = label, true
		out.Quotas[label] = q
	}
	if len(out.Quotas) == 0 {
		out.Message = "Antigravity reported no quota"
	}
	return out
}
