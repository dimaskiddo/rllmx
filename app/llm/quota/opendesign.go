package quota

import (
	"context"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

const (
	openDesignAPI = "https://amr-api.open-design.ai/api/v1/workspaces"
	// openDesignNoKey names the credential the reading needs. The runtime key
	// the chat path sends is scoped to inference and reads nothing here.
	openDesignNoKey = "OpenDesign Go: paste the Control Key (vela_ctrl_...) into this account's provider data as opendesignControlKey; the runtime key cannot read quota"
)

// openDesignWindows are the two credit windows shown, by their length in seconds.
var openDesignWindows = []struct {
	seconds int64
	name    string
}{{18000, "Session (5h)"}, {604800, "Weekly (7d)"}}

// openDesignID is what a workspace id may look like. It joins the address, so
// anything else is never sent.
var openDesignID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// readOpenDesign reads the plan's credit windows and the wallet with the
// account's control key. The control plane is the vendor's internal API, so
// every field is checked and any trouble is a message, never a failure the chat
// path could feel.
func readOpenDesign(ctx context.Context, r *Reader, _ upstream.Entry, a Access) (Reading, error) {
	out := Reading{Quotas: map[string]Quota{}, Plan: "OpenDesign Go"}
	control := strings.TrimSpace(stored(a.Conn, "opendesignControlKey"))
	if control == "" {
		out.Message = openDesignNoKey
		return out, nil
	}
	headers := func(workspace string) map[string]string {
		h := map[string]string{"Authorization": "Bearer " + control}
		if workspace != "" {
			h["x-vela-workspace-id"] = workspace
		}
		return h
	}
	workspace, msg := openDesignWorkspace(ctx, r, a, headers(""))
	if msg != "" {
		out.Message = msg
		return out, nil
	}
	base := openDesignAPI + "/" + workspace
	var errs []string
	if body, ok := r.openDesignGet(ctx, base+"/billing/preflight", headers(workspace), &errs, "preflight"); ok {
		if gjson.GetBytes(body, "workspaceId").String() != workspace {
			errs = append(errs, "preflight: workspace mismatch")
		} else {
			openDesignPlan(gjson.ParseBytes(body), out.Quotas)
		}
	}
	if body, ok := r.openDesignGet(ctx, base+"/wallet/balance", headers(workspace), &errs, "wallet"); ok {
		if gjson.GetBytes(body, "workspaceId").String() != workspace {
			errs = append(errs, "wallet: workspace mismatch")
		} else if usd := gjson.GetBytes(body, "balanceUsd"); usd.Type == gjson.String && usd.Float() >= 0 && strings.TrimSpace(usd.String()) != "" {
			out.Quotas["Balance ($)"] = balance(float64(int64(usd.Float()*100+0.5))/100, "USD", "USD")
		}
	}
	if len(out.Quotas) == 0 {
		out.Message = "OpenDesign Go reported no quota for this account"
		if len(errs) > 0 {
			out.Message += " (" + strings.Join(errs, "; ") + ")"
		}
	}
	return out, nil
}

// openDesignWorkspace is the workspace the control key reads. A key that sees
// several is not guessed at: the first would show another account's spend.
func openDesignWorkspace(ctx context.Context, r *Reader, a Access, h map[string]string) (string, string) {
	if pinned := strings.TrimSpace(stored(a.Conn, "opendesignWorkspaceId")); pinned != "" {
		if !openDesignID.MatchString(pinned) {
			return "", "OpenDesign Go: opendesignWorkspaceId is not a plain workspace id"
		}
		return pinned, ""
	}
	code, raw, err := r.do(ctx, request{URL: openDesignAPI, Headers: h})
	if err != nil {
		reading, _ := failed("OpenDesign Go", err)
		return "", reading.Message
	}
	if code != http.StatusOK {
		return "", "OpenDesign Go refused the Control Key or could not list workspaces (" + http.StatusText(code) + ")"
	}
	var ids []string
	for _, item := range gjson.GetBytes(raw, "items").Array() {
		if id := strings.TrimSpace(item.Get("workspaceId").String()); id != "" {
			ids = append(ids, id)
		}
	}
	switch {
	case len(ids) == 0:
		return "", "OpenDesign Go: no workspace found for this Control Key"
	case len(ids) > 1:
		return "", "OpenDesign Go: this Control Key sees " + strconv.Itoa(len(ids)) + " workspaces; set opendesignWorkspaceId in this account's provider data so another workspace's quota is not shown"
	case !openDesignID.MatchString(ids[0]):
		return "", "OpenDesign Go: the workspace id it listed is not a plain id"
	}
	return ids[0], ""
}

// openDesignGet makes one control-plane call. A failure is recorded and the
// other call still runs: the wallet and the plan ledger are separate services.
func (r *Reader) openDesignGet(ctx context.Context, url string, h map[string]string, errs *[]string, label string) ([]byte, bool) {
	code, raw, err := r.do(ctx, request{URL: url, Headers: h})
	if err != nil || code != http.StatusOK || !gjson.ValidBytes(raw) {
		*errs = append(*errs, label+" unavailable")
		return nil, false
	}
	return raw, true
}

func openDesignPlan(preflight gjson.Result, into map[string]Quota) {
	plan := preflight.Get("codingPlan")
	if plan.Get("eligible").Type == gjson.False {
		return
	}
	for _, want := range openDesignWindows {
		for _, w := range plan.Get("windows").Array() {
			if w.Get("durationSeconds").Int() != want.seconds {
				continue
			}
			if q, ok := openDesignWindow(w); ok {
				into[want.name] = q
			}
			break
		}
	}
}

// openDesignWindow reads a credit window whose counts are decimal strings. They
// are kept as integers until the share is known: a pool past 2^53 would round in
// a float and show a spent pool as untouched.
func openDesignWindow(w gjson.Result) (Quota, bool) {
	used, ok1 := new(big.Int).SetString(strings.TrimSpace(w.Get("usedCredits").String()), 10)
	limit, ok2 := new(big.Int).SetString(strings.TrimSpace(w.Get("limitCredits").String()), 10)
	if !ok1 || !ok2 || used.Sign() < 0 || limit.Sign() <= 0 || !digitsOnly(w.Get("usedCredits").String()) || !digitsOnly(w.Get("limitCredits").String()) {
		return Quota{}, false
	}
	if used.Cmp(limit) > 0 {
		used = limit
	}
	// Hundredths of a percent, rounded half up.
	left := new(big.Int).Sub(limit, used)
	hundredths := new(big.Int).Mul(left, big.NewInt(20000))
	hundredths.Add(hundredths, limit)
	hundredths.Div(hundredths, new(big.Int).Mul(limit, big.NewInt(2)))
	usedF, _ := new(big.Float).SetInt(used).Float64()
	limitF, _ := new(big.Float).SetInt(limit).Float64()
	return Quota{
		Used: usedF, Total: limitF, RemainingPercentage: float64(hundredths.Int64()) / 100,
		ResetAt: stamp(stampOf(w.Get("resetsAt"))),
	}, true
}

func digitsOnly(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && strings.Trim(s, "0123456789") == ""
}
