package quota

import (
	"context"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor/xiaomi"
	"rllmx/app/llm/upstream"
)

// readXiaomiMimo reads the desktop account's weekly window from its session
// cookie, or the cloud API's answer for a key. The key path is best effort: the
// service usually answers it with a refusal, which is not a fault here.
func readXiaomiMimo(ctx context.Context, r *Reader, e upstream.Entry, a Access) (Reading, error) {
	region := stored(a.Conn, "region")
	pass := stored(a.Conn, "mimoPassToken")
	if pass != "" {
		if r.Cookies == nil {
			return Reading{Message: "Xiaomi session reading is not available"}, nil
		}
		out, err, done := r.xiaomiSession(ctx, region, pass, stored(a.Conn, "mimoUserId"), stored(a.Conn, "mimoCUserId"))
		if !done {
			return failed("Xiaomi MiMo", err)
		}
		if out != nil {
			return *out, nil
		}
	}
	if a.Token == "" {
		return Reading{Message: "Xiaomi MiMo has no session and no key"}, nil
	}
	code, raw, err := r.do(ctx, request{URL: "https://aistudio.xiaomimimo.com/open-apis/v1/user/usage", Headers: map[string]string{
		"Authorization": bearerHeader(a.Token), "X-Mimo-Source": "mimocode-cli",
	}})
	if err != nil {
		return failed("Xiaomi MiMo", err)
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return Reading{Message: "Xiaomi MiMo needs the desktop session for this account"}, nil
	}
	if code != http.StatusOK {
		return answered("Xiaomi MiMo", code)
	}
	return parseXiaomi(raw), nil
}

// xiaomiSession asks for the cookie, retrying once with a fresh exchange when
// the cached one was refused. done=false carries a transport failure.
func (r *Reader) xiaomiSession(ctx context.Context, region, pass, uid, cuid string) (*Reading, error, bool) {
	for _, fresh := range []bool{false, true} {
		cookie, err := r.Cookies.Cookie(ctx, pass, uid, cuid, region, fresh)
		if err != nil {
			if fresh {
				return nil, err, false
			}
			continue
		}
		code, raw, err := r.do(ctx, request{URL: xiaomi.MimoHost(region) + "/api/user/usage", Headers: map[string]string{
			"Cookie": cookie, "User-Agent": xiaomi.MimoUserAgent,
		}})
		if err != nil {
			return nil, err, false
		}
		if code == http.StatusUnauthorized || code == http.StatusForbidden {
			continue
		}
		if code != http.StatusOK {
			out, _ := answered("Xiaomi MiMo", code)
			return &out, nil, true
		}
		out := parseXiaomi(raw)
		return &out, nil, true
	}
	out := Reading{Message: "Xiaomi MiMo refused the session"}
	return &out, nil, true
}

// parseXiaomi reads {code:0,data:{percent,resetAt|resetDate}}: percent is what
// is left of the weekly window.
func parseXiaomi(raw []byte) Reading {
	out := Reading{Quotas: map[string]Quota{}}
	data := gjson.ParseBytes(raw)
	if code := data.Get("code"); code.Exists() && code.Int() != 0 {
		out.Message = "Xiaomi MiMo refused the reading"
		return out
	}
	body := data.Get("data")
	if !body.Get("percent").Exists() {
		out.Message = "Xiaomi MiMo reported no figure"
		return out
	}
	left := clampPercent(body.Get("percent").Float())
	reset := stampOf(body.Get("resetAt"))
	if reset.IsZero() {
		reset = stampOf(body.Get("resetDate"))
	}
	q := share(100-left, reset)
	q.DisplayName, q.Recurring = "Weekly", true
	out.Quotas["Weekly"] = q
	out.Plan = "Xiaomi MiMo Desktop"
	return out
}
