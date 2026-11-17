package quota

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// miniMaxUsageURLs are the plan addresses, tried in order: the first that
// serves the key answers.
var miniMaxUsageURLs = map[string][]string{
	"minimax": {
		"https://www.minimax.io/v1/token_plan/remains",
		"https://api.minimax.io/v1/api/openplatform/coding_plan/remains",
	},
	"minimax-cn": {
		"https://www.minimaxi.com/v1/api/openplatform/coding_plan/remains",
		"https://api.minimaxi.com/v1/api/openplatform/coding_plan/remains",
	},
}

// readMiniMax reads a Token or Coding plan. The plan's count fields mean what
// is left on one address and what was spent on another, so the address decides.
func readMiniMax(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	urls := miniMaxUsageURLs[a.Conn.Provider]
	if len(urls) == 0 {
		return Reading{}, ErrUnsupported
	}
	var last Reading
	for i, target := range urls {
		code, raw, err := r.do(ctx, request{URL: target, Headers: keyed(e, identity(e), a.Token)})
		if err != nil {
			return failed("MiniMax", err)
		}
		switch {
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return Reading{}, ErrAuth
		case code == http.StatusNotFound || code == http.StatusMethodNotAllowed || code >= 500:
			last = Reading{Message: "MiniMax answered " + numberText(float64(code))}
			if i < len(urls)-1 {
				continue
			}
			return last, nil
		case code != http.StatusOK:
			return answered("MiniMax", code)
		}
		out := parseMiniMax(raw, strings.Contains(target, "/coding_plan/remains"), r.now())
		if out.Message != "" && i < len(urls)-1 {
			last = out
			continue
		}
		return out, nil
	}
	return last, nil
}

func parseMiniMax(raw []byte, countIsRemaining bool, now time.Time) Reading {
	out := Reading{Quotas: map[string]Quota{}}
	data := gjson.ParseBytes(raw)
	if status := data.Get("base_resp.status_code"); status.Exists() && status.Int() != 0 {
		// The key reached a plan it cannot use. The answer's message is left out:
		// it may echo the key.
		out.Message = "MiniMax refused the key for this plan"
		return out
	}
	for _, m := range data.Get("model_remains").Array() {
		name := m.Get("model_name").String()
		if name == "" {
			name = m.Get("modelName").String()
		}
		if name == "" {
			name = "unknown"
		}
		name = miniMaxName(name)
		for _, w := range []struct {
			window, total, used, left, remains, end string
		}{
			{"(5h)", "current_interval_total_count", "current_interval_usage_count", "current_interval_remaining_percent", "remains_time", "end_time"},
			{"(7d)", "current_weekly_total_count", "current_weekly_usage_count", "current_weekly_remaining_percent", "weekly_remains_time", "weekly_end_time"},
		} {
			total := field(m, w.total)
			left := m.Get(w.left)
			count := field(m, w.used)
			if total <= 0 && !left.Exists() {
				continue
			}
			reset := now.Add(time.Duration(field(m, w.remains)) * time.Millisecond)
			if field(m, w.remains) <= 0 {
				reset = stampOf(m.Get(w.end))
			}
			var q Quota
			switch {
			case total > 0:
				used := count
				if countIsRemaining {
					used = total - count
				}
				q = window(used, total, reset)
			default:
				q = share(100-clampPercent(left.Float()), reset)
			}
			q.DisplayName, q.Recurring = name+" "+w.window, true
			out.Quotas[name+" "+w.window] = q
		}
	}
	if len(out.Quotas) == 0 && out.Message == "" {
		out.Message = "MiniMax reported no quota"
	}
	return out
}

// field reads a count given under either spelling of its name.
func field(m gjson.Result, snake string) float64 {
	if v := m.Get(snake); v.Exists() {
		return v.Float()
	}
	return m.Get(camel(snake)).Float()
}

// camel turns a snake_case field name into the camelCase spelling a service
// sometimes sends instead.
func camel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// miniMaxName is the plan series a model belongs to, as the client labels it.
func miniMaxName(raw string) string {
	switch raw {
	case "MiniMax-M*", "general":
		return "M-series"
	}
	return raw
}
