package codebuddy

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/executor"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
)

// balanceWords are the words in which MiniMax Code says a balance or a quota ran
// out.
var balanceWords = regexp.MustCompile(`(?i)insufficient|balance|credit|quota|exhaust|limit|余额|积分|额度|不足|用完|上限`)

// MinimaxCode sends a request to MiniMax Code. Each call carries the local time
// zone and a session id, and a refusal that says the balance is spent becomes a
// 429, so the account is passed over instead of being renewed and tried again.
type MinimaxCode struct {
	Default *executor.Default
	// Now is the clock for the zone offset; nil uses time.Now.
	Now func() time.Time
}

// Execute sends one call.
func (m *MinimaxCode) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	if req.Auth != nil {
		a := *req.Auth
		add := map[string]string{"X-Mavis-Timezone-Offset": strconv.Itoa(m.offset())}
		if a.Headers["X-Mavis-Session-Id"] == "" {
			add["X-Mavis-Session-Id"] = dialect.NewID()
		}
		a.Headers = executor.WithHeaders(a.Headers, add)
		req.Auth = &a
	}
	resp, err := m.Default.Execute(ctx, req)
	if err != nil || (resp.Status != http.StatusPaymentRequired && resp.Status != http.StatusForbidden) {
		return resp, err
	}
	raw := executor.FailedBody(resp)
	if !balanceWords.Match(raw) {
		return executor.ReplyWith(resp, resp.Status, raw, ""), nil
	}
	body, _ := sjson.SetBytes([]byte(`{"type":"error","error":{"type":"rate_limit_error"}}`), "error.message", "usage limit reached: "+refusalText(raw))
	return executor.ReplyWith(resp, http.StatusTooManyRequests, body, "application/json"), nil
}

// offset is the local zone's distance from UTC in seconds.
func (m *MinimaxCode) offset() int {
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	_, off := now.Zone()
	return off
}

// refusalText is the message of a refusal: the places MiniMax puts it, else the
// text itself.
func refusalText(raw []byte) string {
	if gjson.ValidBytes(raw) {
		g := gjson.ParseBytes(raw)
		if s := document.FirstString(g.Get("error.message"), g.Get("message"), g.Get("msg"), g.Get("base_resp.status_msg")); s != "" {
			return s
		}
	}
	return strings.TrimSpace(string(raw))
}
