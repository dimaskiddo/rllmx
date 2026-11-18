package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/dialect"
	"rllmx/app/store/repository"
)

const (
	codexUsageURL   = "https://chatgpt.com/backend-api/wham/usage"
	codexCreditsURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
	codexConsumeURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume"
)

// readCodex reads the account's windows: the session and weekly pair, then the
// review and spark pairs when the plan reports them.
func readCodex(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	raw, stop, early, earlyErr := r.getOK(ctx, "Codex", request{URL: codexUsageURL, Headers: codexHeaders(e, a, nil)})
	if stop {
		return early, earlyErr
	}
	return parseCodex(raw), nil
}

// codexHeaders are the CLI's own: its identity, the account header and the
// bearer token.
func codexHeaders(e upstream.Entry, a Access, extra map[string]string) map[string]string {
	h := bearerFor(e, a)
	if e.OAuth != nil && e.OAuth.Account != nil {
		if v := e.OAuth.Account.Value(a.Conn.Data); v != "" {
			h[e.OAuth.Account.Name] = v
		}
	}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func parseCodex(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "Codex"}
	data := gjson.ParseBytes(raw)
	if plan := data.Get("plan_type").String(); plan != "" {
		out.Plan = plan
	}
	pairs := []struct{ prefix, path string }{
		{"", "rate_limit"},
		{"review", "code_review_rate_limit"},
		{"spark", "spark_rate_limit"},
	}
	for _, p := range pairs {
		snap := data.Get(p.path)
		if !snap.Exists() {
			continue
		}
		addCodexWindows(out.Quotas, p.prefix, snap)
	}
	// A plan may name its other limits instead, one entry per meter.
	for _, entry := range data.Get("additional_rate_limits").Array() {
		name := entry.Get("limit_name").String()
		if name == "" {
			name = entry.Get("metered_feature").String()
		}
		if name == "" {
			continue
		}
		addCodexWindows(out.Quotas, codexPrefix(name), entry)
	}
	if count := data.Get("rate_limit_reset_credits.available_count"); count.Exists() {
		out.ResetCredits = &ResetCredits{AvailableCount: int(count.Int())}
	}
	if len(out.Quotas) == 0 {
		out.Message = "Codex reported no quota"
	}
	return out
}

// addCodexWindows adds a pair of windows under their prefix.
func addCodexWindows(quotas map[string]Quota, prefix string, snap gjson.Result) {
	if body := snap.Get("rate_limit"); body.IsObject() {
		snap = body
	}
	for _, w := range []struct{ field, label string }{
		{"primary_window", "session"},
		{"secondary_window", "weekly"},
	} {
		v := snap.Get(w.field)
		if !v.Exists() {
			v = snap.Get(w.field[:len(w.field)-len("_window")])
		}
		if !v.IsObject() {
			continue
		}
		label := w.label
		if prefix != "" {
			label = prefix + " " + label
		}
		q := share(v.Get("used_percent").Float(), stampOf(v.Get("reset_at")))
		q.DisplayName, q.Recurring = label, true
		quotas[label] = q
	}
}

// codexPrefix turns a meter's name into a row prefix.
func codexPrefix(name string) string {
	id := strings.ToLower(name)
	switch {
	case strings.Contains(id, "review"):
		return "review"
	case strings.Contains(id, "spark"):
		return "spark"
	}
	return strings.ReplaceAll(id, "_", "-")
}

// CodexCredits lists the refills an account holds.
func (r *Reader) CodexCredits(ctx context.Context, conn repository.Connection) (ResetCredits, error) {
	entry, _ := r.entry(conn.Provider)
	access, err := r.access(ctx, conn)
	if err != nil {
		return ResetCredits{}, err
	}
	code, raw, err := r.do(ctx, request{URL: codexCreditsURL, Headers: codexHeaders(entry, access, map[string]string{
		"OpenAI-Beta": "codex-1",
	})})
	if err != nil {
		return ResetCredits{}, err
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return ResetCredits{}, ErrAuth
	}
	if code != http.StatusOK {
		return ResetCredits{}, ErrRefused
	}
	out := ResetCredits{}
	data := gjson.ParseBytes(raw)
	out.AvailableCount = int(data.Get("availableCount").Int())
	for _, c := range data.Get("credits").Array() {
		out.Credits = append(out.Credits, Credit{
			Status:    c.Get("status").String(),
			GrantedAt: stamp(stampOf(c.Get("grantedAt"))),
			ExpiresAt: stamp(stampOf(c.Get("expiresAt"))),
		})
	}
	return out, nil
}

// CodexConsume spends one reset credit and returns what the service says it
// reset. The request id is made here, so a retried call is a new spend only when
// the caller asks again.
func (r *Reader) CodexConsume(ctx context.Context, conn repository.Connection) (ConsumeResult, error) {
	entry, _ := r.entry(conn.Provider)
	access, err := r.access(ctx, conn)
	if err != nil {
		return ConsumeResult{}, err
	}
	id := dialect.NewID()
	body, err := json.Marshal(map[string]string{"redeem_request_id": id})
	if err != nil {
		return ConsumeResult{}, err
	}
	code, raw, err := r.do(ctx, request{Method: http.MethodPost, URL: codexConsumeURL, Headers: codexHeaders(entry, access, nil), Body: body})
	if err != nil {
		return ConsumeResult{}, err
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return ConsumeResult{}, ErrAuth
	}
	out := ConsumeResult{RedeemRequestID: id, Status: code}
	data := gjson.ParseBytes(raw)
	out.Code = data.Get("code").String()
	out.WindowsReset = int(data.Get("windows_reset").Int())
	out.Message = data.Get("message").String()
	out.Reset = code/100 == 2 && (out.Code == "reset" || out.WindowsReset > 0)
	return out, nil
}
