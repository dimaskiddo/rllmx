package quota

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// glmUsageURL is the plan monitor, by region.
var glmUsageURL = map[string]string{
	"glm":    "https://api.z.ai/api/monitor/usage/quota/limit",
	"glm-cn": "https://open.bigmodel.cn/api/monitor/usage/quota/limit",
}

// readGLM reads a GLM coding plan's limits. A pasted key and a login's token are
// the same credential there, so both auth types are served.
func readGLM(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	target, ok := glmUsageURL[providerOf(a, e)]
	if !ok {
		return Reading{}, ErrUnsupported
	}
	code, raw, err := r.do(ctx, request{URL: target, Headers: keyed(e, identity(e), a.Token)})
	if err != nil {
		return failed("GLM", err)
	}
	if code != http.StatusOK {
		return answered("GLM", code)
	}
	return parseGLM(raw), nil
}

func parseGLM(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}}
	data := gjson.GetBytes(raw, "data")
	for _, l := range data.Get("limits").Array() {
		kind := l.Get("type").String()
		if kind != "TOKENS_LIMIT" && kind != "CREDIT_LIMIT" {
			continue
		}
		number := l.Get("number").Float()
		key := "Session"
		switch {
		case l.Get("unit").Int() == 3:
			key = "Session (" + numberText(number) + "h)"
		case l.Get("unit").Int() == 6:
			key = "Weekly (7d)"
		case kind == "TOKENS_LIMIT":
			key = "Tokens"
		default:
			key = "Limit (" + numberText(number) + ")"
		}
		out.Quotas[key] = share(l.Get("percentage").Float(), ParseReset(l.Get("nextResetTime")))
	}
	if level := data.Get("level").String(); level != "" {
		out.Plan = strings.ToUpper(level[:1]) + strings.ToLower(level[1:])
	}
	if len(out.Quotas) == 0 {
		out.Message = "GLM reported no quota"
	}
	return out
}

// providerOf names the provider a reading was asked for, from the connection.
func providerOf(a Access, e upstream.Entry) string {
	if a.Conn.Provider != "" {
		return a.Conn.Provider
	}
	return e.ID
}

// numberText prints a count the way a label reads it.
func numberText(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
