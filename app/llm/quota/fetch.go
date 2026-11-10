package quota

import (
	"bytes"
	"context"
	"net/http"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/file"
	"rllmx/app/store/document"
)

// Endpoints and the identity Antigravity's IDE sends on the quota calls.
type Endpoints struct {
	// Models is fetchAvailableModels; Summary is retrieveUserQuotaSummary; Load is
	// loadCodeAssist, which names the project and the tier.
	Models, Summary, Load string
	// UserAgent and Version are the client's own, from the provider's data.
	UserAgent, Version string
}

const (
	fetchTimeout = 10 * time.Second
	maxAnswer    = 1 << 20
	// freeTier is the tier whose only meaningful limit is the weekly one.
	freeTier = "free-tier"
)

// HTTP makes the calls. It is the guarded client the rest of the program uses.
type HTTP interface {
	Do(*http.Request) (*http.Response, error)
}

// Fetcher reads an account's quota from Google. It never returns an error: a call
// that fails leaves that part of the reading out, and a reading with nothing in it
// is nil, so the gate keeps what it already knew.
type Fetcher struct {
	Client HTTP
	URL    Endpoints
}

// Fetch implements Fetch for the gate.
func (f *Fetcher) Fetch(ctx context.Context, _ string, access string) Snapshot {
	project, paid := f.plan(ctx, access)
	snap := Snapshot{}
	var models Snapshot
	if paid {
		if raw, ok := f.post(ctx, f.URL.Models, access, project); ok {
			models = ParseModels(raw)
			for k, v := range models {
				snap[k] = v
			}
		}
	}
	if raw, ok := f.post(ctx, f.URL.Summary, access, project); ok {
		summary := ParseSummary(raw)
		for k, v := range Reconcile(models, summary) {
			snap[k] = v
		}
	}
	if len(snap) == 0 {
		return nil
	}
	return snap
}

// plan asks for the account's project and tier. An account whose tier cannot be
// read is treated as free, which reads fewer limits and so blocks less.
func (f *Fetcher) plan(ctx context.Context, access string) (project string, paid bool) {
	raw, ok := f.post(ctx, f.URL.Load, access, "")
	if !ok {
		return "", false
	}
	project = gjson.GetBytes(raw, "cloudaicompanionProject").String()
	if p := gjson.GetBytes(raw, "cloudaicompanionProject.id"); p.Exists() {
		project = p.String()
	}
	tier := gjson.GetBytes(raw, "paidTier.id").String()
	return project, tier != "" && tier != freeTier
}

func (f *Fetcher) post(ctx context.Context, target, access, project string) ([]byte, bool) {
	if target == "" || f.Client == nil {
		return nil, false
	}
	body := []byte(`{}`)
	if project != "" {
		body = []byte(`{"project":` + document.Encode(project) + `}`)
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", f.URL.UserAgent)
	req.Header.Set("X-Client-Name", "antigravity")
	req.Header.Set("X-Client-Version", f.URL.Version)
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	raw, err := file.ReadCapped(resp.Body, maxAnswer)
	if err != nil {
		return nil, false
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 || !gjson.ValidBytes(raw) {
		return nil, false
	}
	return raw, true
}
