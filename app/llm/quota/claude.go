package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/store/dialect"
	"rllmx/app/store/repository"
)

const (
	claudeUsageURL   = "https://api.anthropic.com/api/oauth/usage?cedar_ember=1"
	claudeProfileURL = "https://api.anthropic.com/api/oauth/profile"
	claudeResetURL   = "https://api.anthropic.com/api/organizations/"
)

// claudeHeaders adds the two headers every call to the Claude OAuth API carries.
func claudeHeaders(h map[string]string) map[string]string {
	h["anthropic-beta"] = "oauth-2025-04-20"
	h["anthropic-version"] = "2023-06-01"
	return h
}

// readClaude reads a Claude Code login's windows. The service answers a spent
// window as a number used out of 100, and names a model's own weekly window in
// either the top-level keys or the limits list.
func readClaude(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	h := claudeHeaders(bearerFor(e, a))
	raw, stop, early, earlyErr := r.getOK(ctx, "Claude", request{URL: claudeUsageURL, Headers: h})
	if stop {
		return early, earlyErr
	}
	return parseClaude(raw), nil
}

func parseClaude(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}, Plan: "Claude Code"}
	data := gjson.ParseBytes(raw)
	if w := data.Get("five_hour"); utilWindow(w) {
		out.Quotas["Session (5h)"] = named(share(w.Get("utilization").Float(), stampOf(w.Get("resets_at"))), "Session (5h)")
	}
	if w := data.Get("seven_day"); utilWindow(w) {
		out.Quotas["Weekly (7d)"] = named(share(w.Get("utilization").Float(), stampOf(w.Get("resets_at"))), "Weekly (7d)")
	}
	data.ForEach(func(key, w gjson.Result) bool {
		name, ok := strings.CutPrefix(key.String(), "seven_day_")
		if !ok || !utilWindow(w) {
			return true
		}
		label := "Weekly " + name + " (7d)"
		out.Quotas[label] = named(share(w.Get("utilization").Float(), stampOf(w.Get("resets_at"))), label)
		return true
	})
	for _, limit := range data.Get("limits").Array() {
		if limit.Get("kind").String() != "weekly_scoped" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(limit.Get("scope.model.display_name").String()))
		if name == "" || !limit.Get("percent").Exists() {
			continue
		}
		label := "Weekly " + name + " (7d)"
		out.Quotas[label] = named(share(limit.Get("percent").Float(), stampOf(limit.Get("resets_at"))), label)
	}
	out.ResetCredits = claudeGrants(data.Get("cedar_ember"))
	if len(out.Quotas) == 0 {
		out.Message = "Claude reported no quota"
	}
	return out
}

// grantID is the shape a reset grant id has.
var grantID = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)

// ClaudeReset spends one free refill. It refills the limits the grant names and
// cannot be undone.
func (r *Reader) ClaudeReset(ctx context.Context, conn repository.Connection, grant string) (ResetResult, error) {
	if !grantID.MatchString(grant) {
		return ResetResult{}, ErrInvalidGrant
	}
	entry, _ := r.entry(conn.Provider)
	access, err := r.access(ctx, conn)
	if err != nil {
		return ResetResult{}, err
	}
	h := claudeHeaders(bearer(identity(entry), access.Token))
	h["Content-Type"] = "application/json"
	code, raw, err := r.do(ctx, request{URL: claudeProfileURL, Headers: h})
	if err != nil {
		return ResetResult{}, err
	}
	org := gjson.GetBytes(raw, "organization.uuid").String()
	if code != http.StatusOK || org == "" {
		return ResetResult{Status: code, Message: "Cannot resolve the organization"}, nil
	}
	body, err := json.Marshal(map[string]string{
		"program": "cedar_ember", "grant_id": grant, "request_id": dialect.NewID(),
	})
	if err != nil {
		return ResetResult{}, err
	}
	target := claudeResetURL + url.PathEscape(org) + "/reset_rate_limits"
	code, raw, err = r.do(ctx, request{Method: http.MethodPost, URL: target, Headers: h, Body: body})
	if err != nil {
		return ResetResult{}, err
	}
	out := ResetResult{Status: code}
	data := gjson.ParseBytes(raw)
	out.Result = upstreamText(data.Get("result").String())
	out.Reason = upstreamText(data.Get("reason").String())
	out.ResetsLeft = int(data.Get("resets_left").Int())
	out.Message = upstreamText(data.Get("error.message").String())
	out.OK = code/100 == 2 && out.Result == "reset"
	// The limits the grant refilled are not what the cache holds any more.
	if r.Cache != nil {
		r.Cache.Delete(cache.Quota, conn.ID)
	}
	return out, nil
}

// utilWindow reports a window the service measured against 100.
func utilWindow(w gjson.Result) bool {
	return w.IsObject() && w.Get("utilization").Type == gjson.Number
}

// claudeGrants reads the free refills an account holds. Every grant is listed,
// paused and spent ones included, so the page can say why one cannot be used;
// only the usable ones count.
func claudeGrants(block gjson.Result) *ResetCredits {
	if !block.Get("eligible").Bool() || !block.Get("grants").IsArray() {
		return nil
	}
	out := &ResetCredits{
		NextGrantID:    block.Get("next_grant_id").String(),
		CooldownUntil:  stamp(stampOf(block.Get("cooldown_until"))),
		WeeklyResetsAt: stamp(stampOf(block.Get("weekly_resets_at"))),
	}
	for _, g := range block.Get("grants").Array() {
		id := g.Get("id").String()
		if id == "" {
			continue
		}
		left := int(g.Get("resets_left").Int())
		paused := g.Get("paused").Bool()
		c := Credit{
			ID: id, Label: g.Get("label").String(),
			ResetsLeft: left, ResetsTotal: int(g.Get("resets_total").Int()),
			Paused: paused, UsableNow: g.Get("usable_now").Bool(),
			UseRequiresLimit: g.Get("use_requires_limit").Bool(),
			StartsAt:         stamp(stampOf(g.Get("starts_at"))),
			EndsAt:           stamp(stampOf(g.Get("ends_at"))),
		}
		for _, clear := range g.Get("clears").Array() {
			if v := clear.String(); v != "" {
				c.Clears = append(c.Clears, v)
			}
		}
		if !paused && left > 0 {
			out.AvailableCount += left
		}
		out.Credits = append(out.Credits, c)
	}
	if out.AvailableCount == 0 {
		return nil
	}
	if out.NextGrantID != "" {
		for i := range out.Credits {
			if out.Credits[i].ID == out.NextGrantID {
				out.ExpiresAt = out.Credits[i].EndsAt
				break
			}
		}
	}
	return out
}
