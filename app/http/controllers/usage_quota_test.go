package controllers

import (
	"context"
	nethttp "net/http"
	"strings"
	"testing"

	apphttp "rllmx/app/http"
	"rllmx/app/llm/quota"
	"rllmx/app/store/repository"
)

// quotaFake is the quota reader as the routes use it.
type quotaFake struct {
	reading    quota.Reading
	readErr    error
	forced     bool
	reset      quota.ResetResult
	resetErr   error
	grant      string
	credits    quota.ResetCredits
	creditsErr error
	consume    quota.ConsumeResult
	consumeErr error
}

func (f *quotaFake) Read(_ context.Context, _ repository.Connection, force bool) (quota.Reading, error) {
	f.forced = force
	return f.reading, f.readErr
}

func (f *quotaFake) ClaudeReset(_ context.Context, _ repository.Connection, grant string) (quota.ResetResult, error) {
	f.grant = grant
	return f.reset, f.resetErr
}

func (f *quotaFake) CodexCredits(context.Context, repository.Connection) (quota.ResetCredits, error) {
	return f.credits, f.creditsErr
}

func (f *quotaFake) CodexConsume(context.Context, repository.Connection) (quota.ConsumeResult, error) {
	return f.consume, f.consumeErr
}

// quotaRig is the usage rig with the quota routes and a fake reader behind them.
func quotaRig(t *testing.T) (*usageRig, *quotaFake) {
	t.Helper()
	r := newUsageRig(t)
	f := &quotaFake{}
	r.c.Quota = func() QuotaReader { return f }
	r.m.guard.Handle(r.m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/{id}", r.c.Connection)
	r.m.guard.Handle(r.m.router, apphttp.ClassProtected, nethttp.MethodPost, "/api/usage/{id}/claude-reset", r.c.ClaudeReset)
	r.m.guard.Handle(r.m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/{id}/codex-reset-credits", r.c.CodexCredits)
	r.m.guard.Handle(r.m.router, apphttp.ClassProtected, nethttp.MethodPost, "/api/usage/{id}/codex-reset-credits", r.c.CodexResetCredits)
	return r, f
}

func TestTheConnectionReadingAnswersWhatTheReaderSays(t *testing.T) {
	r, f := quotaRig(t)
	f.reading = quota.Reading{Plan: "Pro", Quotas: map[string]quota.Quota{
		"Session (5h)": {Used: 10, Total: 100, RemainingPercentage: 90},
	}}
	code, out, raw := r.m.call("GET", "/api/usage/"+r.conn.ID+"?force=1", "", true)
	if code != 200 || out["plan"] != "Pro" || !f.forced {
		t.Fatalf("%d %s forced=%v", code, raw, f.forced)
	}
	if quota := out["quotas"].(map[string]any)["Session (5h)"].(map[string]any); quota["remainingPercentage"] != float64(90) {
		t.Fatalf("row %v", quota)
	}

	code, out, raw = r.m.call("GET", "/api/usage/missing", "", true)
	if code != 404 || out["error"] != "Connection not found" {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestAnUnreadableConnectionStillAnswers(t *testing.T) {
	r, f := quotaRig(t)
	f.readErr = quota.ErrUnsupported
	if code, out, raw := r.m.call("GET", "/api/usage/"+r.conn.ID, "", true); code != 200 ||
		out["message"] != "Usage API not implemented for openai" {
		t.Fatalf("%d %s", code, raw)
	}
	f.readErr = quota.ErrNotEligible
	if code, out, raw := r.m.call("GET", "/api/usage/"+r.conn.ID, "", true); code != 200 ||
		out["message"] != "Usage not available for this connection" {
		t.Fatalf("%d %s", code, raw)
	}
	f.readErr = quota.ErrExpired
	code, out, raw := r.m.call("GET", "/api/usage/"+r.conn.ID, "", true)
	if code != 401 || !strings.HasPrefix(out["error"].(string), "Credential refresh failed") {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestAResetRouteChecksTheProviderAndTheGrant(t *testing.T) {
	r, f := quotaRig(t)
	// The seeded connection is an OpenAI one.
	code, out, raw := r.m.call("POST", "/api/usage/"+r.conn.ID+"/claude-reset", `{"grantId":"g1"}`, true)
	if code != 400 || out["error"] != "Limit reset is only available for Claude OAuth connections." {
		t.Fatalf("%d %s", code, raw)
	}
	if f.grant != "" {
		t.Fatal("a reset was attempted for the wrong provider")
	}

	claude := seedConnection(t, r, "claude", "oauth")
	f.resetErr = quota.ErrInvalidGrant
	code, out, raw = r.m.call("POST", "/api/usage/"+claude.ID+"/claude-reset", `{"grantId":"BAD"}`, true)
	if code != 400 || out["error"] != "Invalid reset grant id." {
		t.Fatalf("%d %s", code, raw)
	}

	f.resetErr = nil
	f.reset = quota.ResetResult{OK: true, Status: 200, Result: "reset", ResetsLeft: 1}
	code, out, raw = r.m.call("POST", "/api/usage/"+claude.ID+"/claude-reset", `{"grantId":"grant-1"}`, true)
	if code != 200 || out["ok"] != true || f.grant != "grant-1" {
		t.Fatalf("%d %s grant=%q", code, raw, f.grant)
	}

	f.reset = quota.ResetResult{Status: 200, Result: "already_used"}
	code, out, raw = r.m.call("POST", "/api/usage/"+claude.ID+"/claude-reset", `{"grantId":"grant-1"}`, true)
	if code != 409 || out["message"] != "Reset not applied: already_used" {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestACodexCreditIsListedAndSpent(t *testing.T) {
	r, f := quotaRig(t)
	codex := seedConnection(t, r, "codex", "oauth")

	code, out, raw := r.m.call("POST", "/api/usage/"+r.conn.ID+"/codex-reset-credits", "", true)
	if code != 400 || out["error"] != "Codex reset credits are only available for Codex connections." {
		t.Fatalf("%d %s", code, raw)
	}

	f.credits = quota.ResetCredits{AvailableCount: 2, Credits: []quota.Credit{{Status: "available"}}}
	code, out, raw = r.m.call("GET", "/api/usage/"+codex.ID+"/codex-reset-credits", "", true)
	if code != 200 || out["availableCount"] != float64(2) {
		t.Fatalf("%d %s", code, raw)
	}

	f.consume = quota.ConsumeResult{Code: "no_credit", Status: 200, Message: "No Codex reset credits available."}
	code, out, raw = r.m.call("POST", "/api/usage/"+codex.ID+"/codex-reset-credits", "", true)
	if code != 409 || out["code"] != "no_credit" {
		t.Fatalf("%d %s", code, raw)
	}

	f.consume = quota.ConsumeResult{Reset: true, Status: 200, WindowsReset: 1, RedeemRequestID: "r1"}
	code, out, raw = r.m.call("POST", "/api/usage/"+codex.ID+"/codex-reset-credits", "", true)
	if code != 200 || out["reset"] != true || out["windows_reset"] != float64(1) {
		t.Fatalf("%d %s", code, raw)
	}

	f.consume = quota.ConsumeResult{Status: 400, Message: "bad"}
	code, _, raw = r.m.call("POST", "/api/usage/"+codex.ID+"/codex-reset-credits", "", true)
	if code != 400 {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestTheClientListShowsOnlyReadableConnections(t *testing.T) {
	r, _ := quotaRig(t)
	client := &ProvidersController{DB: func() *repository.Store { return r.m.st.Store }, Registry: r.m.pc.Registry}
	r.m.guard.Handle(r.m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers/client", client.Client)

	for i := range 3 {
		seedConnectionNamed(t, r, "claude", "oauth", "claude-"+string(rune('a'+i)), int64(i+1))
	}
	seedConnectionNamed(t, r, "openai", "apikey", "plain", 9)

	code, out, raw := r.m.call("GET", "/api/providers/client?pageSize=2", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	pagination := out["pagination"].(map[string]any)
	if pagination["total"] != float64(3) || pagination["pageSize"] != float64(2) || pagination["totalPages"] != float64(2) {
		t.Fatalf("pagination %v", pagination)
	}
	totals := out["totals"].(map[string]any)
	if totals["eligibleConnections"] != float64(3) {
		t.Fatalf("totals %v", totals)
	}
	if strings.Contains(raw, "sk-test") || strings.Contains(raw, "accessToken") || strings.Contains(raw, "apiKey") {
		t.Fatalf("the list carries a credential: %s", raw)
	}

	code, out, raw = r.m.call("GET", "/api/providers/client?provider=openai", "", true)
	if code != 200 || out["pagination"].(map[string]any)["total"] != float64(0) {
		t.Fatalf("an unreadable provider is listed: %d %s", code, raw)
	}
	if got := out["providerOptions"].([]any); len(got) != 1 || got[0] != "claude" {
		t.Fatalf("options %v", got)
	}

	if page, size := clientPaging("0", "900"); page != 1 || size != clientPageSizeMax {
		t.Fatalf("paging %d %d", page, size)
	}
	// A page past anything real cannot wrap the offset into a negative slice index.
	code, _, raw = r.m.call("GET", "/api/providers/client?page=461168601842738792&pageSize=500", "", true)
	if code != 200 {
		t.Fatalf("a huge page answered %d %s", code, raw)
	}
}

func TestAStaticUsageRouteWinsOverTheConnectionOne(t *testing.T) {
	r, _ := quotaRig(t)
	code, out, raw := r.m.call("GET", "/api/usage/stats", "", true)
	if code != 200 || out["totalRequests"] == nil {
		t.Fatalf("stats answered %d %s", code, raw)
	}
	code, out, raw = r.m.call("GET", "/api/usage/"+r.conn.ID, "", true)
	if code != 200 || out["error"] != nil {
		t.Fatalf("the connection route answered %d %s", code, raw)
	}
}

// seedConnection stores one more connection for a provider.
func seedConnection(t *testing.T, r *usageRig, provider, authType string) repository.Connection {
	t.Helper()
	return seedConnectionNamed(t, r, provider, authType, "work", 1)
}

func seedConnectionNamed(t *testing.T, r *usageRig, provider, authType, name string, priority int64) repository.Connection {
	t.Helper()
	fields := map[string]string{"apiKey": `"sk-test"`}
	if authType == "oauth" {
		fields = map[string]string{"accessToken": `"sk-test"`}
	}
	conn, err := r.m.st.Store.Connections().Create(context.Background(), repository.ConnInput{
		Provider: provider, AuthType: authType, Name: name, Fields: fields,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	value := priority
	if _, err := r.m.st.Store.Connections().Update(context.Background(), conn.ID, repository.ConnPatch{Priority: &value}); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestAPageIsASliceOfTheRowsAndAPastTheEndPageIsEmpty(t *testing.T) {
	for _, tt := range []struct{ total, page, size, from, to, pages int }{
		{0, 1, 20, 0, 0, 0},
		{3, 1, 2, 0, 2, 2},
		{3, 2, 2, 2, 3, 2},
		{3, 3, 2, 3, 3, 2},
		{3, 1_000_000, 500, 3, 3, 1},
		{40, 2, 20, 20, 40, 2},
	} {
		from, to, pages := pageSpan(tt.total, tt.page, tt.size)
		if from != tt.from || to != tt.to || pages != tt.pages {
			t.Errorf("%+v: got %d %d %d", tt, from, to, pages)
		}
	}
}

func TestOnlyReadableConnectionsAreListedAndTheirProvidersAreNamedOnceInOrder(t *testing.T) {
	rows, names := eligibleClients([]repository.Connection{
		{Provider: "kimi", AuthType: "apikey"}, {Provider: "claude", AuthType: "oauth"},
		{Provider: "claude", AuthType: "apikey"}, {Provider: "claude", AuthType: "oauth"}, {Provider: "deepseek", AuthType: "oauth"},
	})
	if len(rows) != 3 || strings.Join(names, ",") != "claude,kimi" {
		t.Fatalf("%d rows, providers %v", len(rows), names)
	}
	if _, none := eligibleClients(nil); none == nil {
		t.Fatal("no providers must answer an empty list, not null")
	}
}
