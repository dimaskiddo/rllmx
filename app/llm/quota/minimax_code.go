package quota

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// miniMaxCodeSites are the two account sites: the agent host serves the account
// calls and the platform host the coding-plan windows.
var miniMaxCodeSites = map[string]struct{ agent, platform, lang string }{
	"minimax-code":        {"https://agent.minimaxi.com", "https://www.minimaxi.com", "zh"},
	"minimax-code-global": {"https://agent.minimax.io", "https://platform.minimax.io", "en"},
}

// readMiniMaxCode reads a MiniMax Code plan. The account calls are signed; the
// plan windows are a plain bearer call once the group id is known.
func readMiniMaxCode(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	site, ok := miniMaxCodeSites[a.Conn.Provider]
	if !ok {
		return Reading{}, ErrUnsupported
	}
	if r.MiniMaxSecret == "" {
		return Reading{Message: "MiniMax Code signing is not configured"}, nil
	}
	userID := stored(a.Conn, "realUserID")
	if userID == "" {
		code, info, ierr := r.minimaxSigned(ctx, site, a.Token, "GET", "/v1/api/user/info", "", "", "")
		if ierr != nil {
			return failed("MiniMax Code", ierr)
		}
		if code != http.StatusOK {
			return answered("MiniMax Code", code)
		}
		userID = firstString(info, "data.userInfo.realUserID", "data.userInfo.real_user_id", "data.user_info.real_user_id")
	}
	code, body, err := r.minimaxSigned(ctx, site, a.Token, "POST", "/matrix/api/v1/user/get_user_extra_info", "", `{}`, userID)
	if err != nil {
		return failed("MiniMax Code", err)
	}
	if code != http.StatusOK {
		return answered("MiniMax Code", code)
	}
	workspace := ""
	for _, entry := range body.Get("data.workspaces").Array() {
		if entry.Get("workspace_type").Int() == 0 {
			workspace = entry.Get("workspace_id").String()
			break
		}
	}
	node := body.Get("data")
	payload := `{"workspace_id":` + strconv.Quote(workspace) + `}`
	code, member, err := r.minimaxSigned(ctx, site, a.Token, "POST", "/matrix/api/v1/commerce/get_membership_info", "", payload, userID)
	if err != nil {
		return failed("MiniMax Code", err)
	}
	if code != http.StatusOK {
		return answered("MiniMax Code", code)
	}
	merged := member.Get("data")
	if !merged.IsObject() {
		merged = node
	}
	out := Reading{Quotas: map[string]Quota{}}
	tier := firstString(merged, "token_plan_tier", "data.token_plan_tier")
	switch {
	case tier != "":
		out.Plan = "M Plan " + tier
	case merged.Get("has_token_plan").Bool():
		out.Plan = "M Plan"
	default:
		out.Plan = "Free"
	}
	if balance := firstNumber(merged, "op_credit_summary.total_remaining_amount", "opcredit_balance", "data.op_credit_summary.total_remaining_amount"); balance > 0 {
		out.Quotas["Credits"] = Quota{Used: 0, Total: balance, Credit: true, Currency: "credits", DisplayName: "Credits"}
	}
	group := firstString(merged, "op_group_id", "data.op_group_id")
	if merged.Get("has_token_plan").Bool() && group != "" {
		code, raw, err := r.do(ctx, request{URL: site.platform + "/v1/api/openplatform/coding_plan/remains", Headers: map[string]string{
			"Authorization": bearerHeader(a.Token), "X-Group-Id": group,
		}})
		if err != nil {
			return failed("MiniMax Code", err)
		}
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			return Reading{}, ErrAuth
		}
		parseMiniMaxCodeWindows(raw, r.now(), out.Quotas)
	}
	if len(out.Quotas) == 0 {
		out.Message = "MiniMax Code reported no quota"
	}
	return out, nil
}

// minimaxSigned sends one signed account call. The signature covers the body,
// so the address and the JSON cannot be told apart by a replay.
func (r *Reader) minimaxSigned(ctx context.Context, site struct{ agent, platform, lang string }, token, method, path, suffix, body, userID string) (int, gjson.Result, error) {
	if userID == "" {
		userID = "0"
	}
	now := r.now()
	ms := now.UnixMilli()
	_, offset := now.Zone()
	query := "device_platform=mcode&biz_id=3&app_id=3001&version_code=22201&unix=" + strconv.FormatInt(ms, 10) +
		"&timezone_offset=" + strconv.Itoa(offset) + "&sys_language=" + site.lang + "&lang=" + site.lang +
		"&device_id=0&os_name=web&browser_name=mcode&user_id=" + userID + "&client=mcode"
	full := path + "?" + query
	ts := strconv.FormatInt(now.Unix(), 10)
	sum := md5.Sum([]byte(jsEscape(full) + "_" + body + md5Hex(strconv.FormatInt(ms, 10)) + "ooui"))
	sign := md5Hex(ts + r.MiniMaxSecret)
	if body != "" {
		sign = md5Hex(ts + r.MiniMaxSecret + body)
	}
	headers := map[string]string{
		"Authorization": bearerHeader(token),
		"User-Agent":    "MiniMaxCode",
		"x-timestamp":   ts,
		"yy":            hex.EncodeToString(sum[:]),
		"x-signature":   sign,
	}
	req := request{URL: site.agent + full, Headers: headers}
	if method == http.MethodPost {
		req.Method = http.MethodPost
		req.Body = []byte(body)
	}
	code, raw, err := r.do(ctx, req)
	if err != nil {
		return code, gjson.Result{}, err
	}
	out := gjson.ParseBytes(raw)
	if v := out.Get("statusInfo.code"); v.Exists() && v.Int() != 0 {
		if v.Int() == 1000048 {
			return code, out, ErrAuth
		}
		return code, out, nil
	}
	return code, out, nil
}

// parseMiniMaxCodeWindows turns the plan's per-model shares into rows. A window
// the service marks skipped is left out; one marked used up reads zero.
func parseMiniMaxCodeWindows(raw []byte, now time.Time, into map[string]Quota) {
	data := gjson.ParseBytes(raw)
	if v := data.Get("base_resp.status_code"); v.Exists() && v.Int() != 0 {
		return
	}
	for _, m := range data.Get("model_remains").Array() {
		name := m.Get("model_name").String()
		if name == "" {
			name = "unknown"
		}
		name = miniMaxName(name)
		for _, w := range []struct{ label, pct, status, end string }{
			{"(5h)", "current_interval_remaining_percent", "current_interval_status", "current_interval_end_time"},
			{"(7d)", "current_weekly_remaining_percent", "current_weekly_status", "weekly_end_time"},
		} {
			left := m.Get(w.pct)
			if !left.Exists() || m.Get(w.status).Int() == 3 {
				continue
			}
			used := 100 - clampPercent(left.Float())
			if m.Get(w.status).Int() == 2 {
				used = 100
			}
			q := share(used, stampOf(m.Get(w.end)))
			q.DisplayName, q.Recurring = name+" "+w.label, true
			into[name+" "+w.label] = q
		}
	}
}

// jsEscape is encodeURIComponent: everything but the unreserved marks is
// percent-encoded, which is what the signature covers.
func jsEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
	}
	return b.String()
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// firstString answers the first field that holds a non-empty string.
func firstString(v gjson.Result, paths ...string) string {
	for _, p := range paths {
		if s := v.Get(p).String(); s != "" {
			return s
		}
	}
	return ""
}

// firstNumber answers the first field that holds a number above zero.
func firstNumber(v gjson.Result, paths ...string) float64 {
	for _, p := range paths {
		if f := v.Get(p); f.Exists() && f.Float() > 0 {
			return f.Float()
		}
	}
	return 0
}
