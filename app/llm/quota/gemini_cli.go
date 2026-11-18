package quota

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

// googleCodeURL is the Cloud Code service's address, by method.
const (
	googleCodeLoad  = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	googleCodeQuota = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota"
)

// readGeminiCLI reads a Gemini CLI account's per-model allowance. The project is
// the one the login stored, or the one the service names.
func readGeminiCLI(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := bearerFor(e, a)
	project, tier := stored(a.Conn, "projectId"), ""
	if project == "" {
		raw, stop, early, earlyErr := r.getOK(ctx, "Gemini CLI", request{Method: http.MethodPost, URL: googleCodeLoad, Headers: h,
			Body: []byte(`{"metadata":{"ideType":"ANTIGRAVITY"}}`)})
		if stop {
			return early, earlyErr
		}
		project = gjson.GetBytes(raw, "cloudaicompanionProject").String()
		if id := gjson.GetBytes(raw, "cloudaicompanionProject.id").String(); id != "" {
			project = id
		}
		tier = gjson.GetBytes(raw, "currentTier.name").String()
	}
	body := []byte(`{}`)
	if project != "" {
		body = []byte(`{"project":` + document.Encode(project) + `}`)
	}
	raw, stop, early, earlyErr := r.getOK(ctx, "Gemini CLI", request{Method: http.MethodPost, URL: googleCodeQuota, Headers: h, Body: body})
	if stop {
		return early, earlyErr
	}
	out := Reading{Quotas: map[string]Quota{}, Plan: tier}
	if out.Plan == "" {
		out.Plan = "Free"
	}
	for _, b := range gjson.GetBytes(raw, "buckets").Array() {
		id := b.Get("modelId").String()
		if id == "" {
			continue
		}
		q := share(100-clampPercent(b.Get("remainingFraction").Float()*100), stampOf(b.Get("resetTime")))
		q.DisplayName, q.Recurring = id, true
		out.Quotas[id] = q
	}
	if len(out.Quotas) == 0 {
		out.Message = "Gemini CLI reported no quota"
	}
	return out, nil
}
