package quota

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

// stub answers the calls a reading makes, by host and path, and keeps what it
// was asked.
type stub struct {
	mu      sync.Mutex
	status  map[string]int
	bodies  map[string]string
	headers map[string]http.Header
	calls   []string
}

func newStub() *stub {
	return &stub{status: map[string]int{}, bodies: map[string]string{}, headers: map[string]http.Header{}}
}

func (s *stub) on(host, path, body string) *stub {
	s.bodies[host+path] = body
	return s
}

func (s *stub) code(host, path string, status int) *stub {
	s.status[host+path] = status
	return s
}

func (s *stub) RoundTrip(r *http.Request) (*http.Response, error) {
	key := r.URL.Host + r.URL.Path
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, key)
	s.headers[key] = r.Header.Clone()
	status := s.status[key]
	if status == 0 {
		status = http.StatusOK
	}
	body, ok := s.bodies[key]
	if !ok {
		body = "{}"
	}
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status),
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   io.NopCloser(strings.NewReader(body)), Request: r,
	}, nil
}

func (s *stub) counted(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		if c == key {
			n++
		}
	}
	return n
}

func (s *stub) header(key, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headers[key].Get(name)
}

var testNow = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

func testReader(s *stub, conn repository.Connection, token string) *Reader {
	reg, err := upstream.Default()
	if err != nil {
		panic(err)
	}
	return &Reader{
		Reg:    reg,
		Client: &http.Client{Transport: s},
		Access: func(context.Context, repository.Connection) (Access, error) {
			return Access{Token: token, Conn: conn}, nil
		},
		Now: func() time.Time { return testNow },
	}
}

// connOf makes the connection a reading is asked for.
func connOf(provider, authType, data string) repository.Connection {
	d := `{"providerSpecificData":{}}`
	if data != "" {
		d = data
	}
	return repository.Connection{ID: "c1", Provider: provider, AuthType: authType, Data: d}
}

func TestEachProviderReadsItsOwnAnswer(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		auth     string
		data     string
		host     string
		path     string
		body     string
		wantPlan string
		wantKey  string
		wantUsed float64
		wantTot  float64
		wantLeft float64
	}{
		{
			name: "github paid", provider: "github", auth: "oauth",
			host: "api.github.com", path: "/copilot_internal/user",
			body:     `{"copilot_plan":"individual_pro","quota_reset_date":"2026-12-01","quota_snapshots":{"chat":{"entitlement":300,"remaining":120},"completions":{"entitlement":2000,"remaining":500},"premium_interactions":{"entitlement":50,"remaining":0}}}`,
			wantPlan: "individual_pro", wantKey: "chat", wantUsed: 180, wantTot: 300, wantLeft: 40,
		},
		{
			name: "github free", provider: "github", auth: "oauth",
			host: "api.github.com", path: "/copilot_internal/user",
			body:     `{"access_type_sku":"free_limited_copilot","monthly_quotas":{"chat":50,"completions":2000},"limited_user_quotas":{"chat":10,"completions":1000},"limited_user_reset_date":"2026-12-01"}`,
			wantPlan: "free_limited_copilot", wantKey: "chat", wantUsed: 10, wantTot: 50, wantLeft: 80,
		},
		{
			name: "glm", provider: "glm", auth: "apikey",
			host: "api.z.ai", path: "/api/monitor/usage/quota/limit",
			body:     `{"data":{"level":"PRO","limits":[{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":42.5,"nextResetTime":1795000000000}]}}`,
			wantPlan: "Pro", wantKey: "Session (5h)", wantUsed: 42.5, wantTot: 100, wantLeft: 57.5,
		},
		{
			name: "kimi", provider: "kimi", auth: "apikey",
			host: "api.kimi.com", path: "/coding/v1/usages",
			body:     `{"usage":{"limit":100,"used":25,"resetTime":"2026-11-20T00:00:00Z"},"limits":[{"name":"minute","detail":{"limit":10,"remaining":4}}]}`,
			wantPlan: "Kimi Coding", wantKey: "Weekly", wantUsed: 25, wantTot: 100, wantLeft: 75,
		},
		{
			name: "deepseek", provider: "deepseek", auth: "apikey",
			host: "api.deepseek.com", path: "/user/balance",
			body:     `{"is_available":false,"balance_infos":[{"currency":"USD","total_balance":"3.50"}]}`,
			wantPlan: "DeepSeek (Insufficient Balance)", wantKey: "Balance (USD)", wantUsed: 0, wantTot: 3.5, wantLeft: 100,
		},
		{
			name: "opencode", provider: "opencode-go", auth: "apikey",
			host: "opencode.ai", path: "/inference/go/v1/usage",
			body:     `{"usage":{"rolling":{"percent":10,"resetsAt":"2026-11-18T12:00:00Z"},"weekly":{"percent":60},"monthly":{"percent":5}}}`,
			wantPlan: "opencode-go", wantKey: "Weekly", wantUsed: 60, wantTot: 100, wantLeft: 40,
		},
		{
			name: "vercel", provider: "vercel-ai-gateway", auth: "apikey",
			host: "ai-gateway.vercel.sh", path: "/v1/credits",
			body:     `{"balance":"3.5","total_used":"1.5"}`,
			wantPlan: "Pay-as-you-go", wantKey: "Remaining (USD)", wantUsed: 1.5, wantTot: 5, wantLeft: 70,
		},
		{
			name: "ollama plan", provider: "ollama", auth: "apikey",
			host: "ollama.com", path: "/api/usage",
			body:     `{"limits":{"session":{"usage":0.25},"weekly":{"usage":0.5}}}`,
			wantPlan: "Cloud", wantKey: "Session (5h)", wantUsed: 25, wantTot: 100, wantLeft: 75,
		},
		{
			name: "clinepass", provider: "clinepass", auth: "oauth",
			host: "api.cline.bot", path: "/api/v1/users/me/plan/usage-limits",
			body:     `{"data":{"limits":[{"type":"five_hour","percentUsed":80,"resetsAt":"2026-11-18T14:00:00Z"}]}}`,
			wantPlan: "Cline", wantKey: "Session (5h)", wantUsed: 80, wantTot: 100, wantLeft: 20,
		},
		{
			name: "codebuddy", provider: "codebuddy-cn", auth: "oauth",
			host: "copilot.tencent.com", path: "/v2/billing/meter/get-user-resource",
			body: `{"code":0,"data":{"Response":{"Data":{"PackageName":"Pro","Accounts":[
				{"CycleStartTime":1794000000000,"CycleEndTime":1795000000000,"DeductionEndTime":1797000000000,"CycleCapacityUsedPrecise":30,"CycleCapacitySizePrecise":100},
				{"CycleStartTime":1794000000000,"CycleEndTime":1795000000000,"DeductionEndTime":1795000000000,"CapacityUsedPrecise":5,"CapacitySizePrecise":20}]}}}}`,
			wantPlan: "Pro", wantKey: "Bonus Pack 1", wantUsed: 5, wantTot: 20, wantLeft: 75,
		},
		{
			name: "minimax", provider: "minimax", auth: "apikey",
			host: "www.minimax.io", path: "/v1/token_plan/remains",
			body:     `{"base_resp":{"status_code":0},"model_remains":[{"model_name":"MiniMax-M3","current_interval_total_count":100,"current_interval_usage_count":40,"remains_time":3600000}]}`,
			wantPlan: "", wantKey: "MiniMax-M3 (5h)", wantUsed: 40, wantTot: 100, wantLeft: 60,
		},
		{
			name: "codex", provider: "codex", auth: "oauth",
			host: "chatgpt.com", path: "/backend-api/wham/usage",
			body:     `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":12,"reset_at":1795000000},"secondary_window":{"used_percent":50}},"rate_limit_reset_credits":{"available_count":2}}`,
			wantPlan: "plus", wantKey: "session", wantUsed: 12, wantTot: 100, wantLeft: 88,
		},
		{
			name: "claude", provider: "claude", auth: "oauth",
			host: "api.anthropic.com", path: "/api/oauth/usage",
			body:     `{"five_hour":{"utilization":87,"resets_at":"2026-11-18T14:00:00Z"},"seven_day":{"utilization":20},"limits":[{"kind":"weekly_scoped","percent":35,"scope":{"model":{"display_name":"Fable"}}}]}`,
			wantPlan: "Claude Code", wantKey: "Session (5h)", wantUsed: 87, wantTot: 100, wantLeft: 13,
		},
		{
			name: "gemini-cli", provider: "gemini-cli", auth: "oauth",
			data: `{"providerSpecificData":{"projectId":"proj-1"}}`,
			host: "cloudcode-pa.googleapis.com", path: "/v1internal:retrieveUserQuota",
			body:     `{"buckets":[{"modelId":"gemini-3-flash","remainingFraction":0.25,"resetTime":"2026-11-18T15:00:00Z"}]}`,
			wantPlan: "Free", wantKey: "gemini-3-flash", wantUsed: 75, wantTot: 100, wantLeft: 25,
		},
		{
			name: "freebuff", provider: "freebuff", auth: "oauth",
			host: "www.codebuff.com", path: "/api/v1/freebuff/session",
			body:     `{"status":"active","model":"flash","rateLimit":{"limit":2,"recentCount":0.5}}`,
			wantPlan: "Freebuff", wantKey: "flash", wantUsed: 0.5, wantTot: 2, wantLeft: 75,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStub().on(tt.host, tt.path, tt.body)
			if tt.provider == "ollama" {
				s.on("ollama.com", "/api/me", `{"Plan":"cloud"}`)
			}
			r := testReader(s, connOf(tt.provider, tt.auth, tt.data), "secret")
			out, err := r.Read(context.Background(), connOf(tt.provider, tt.auth, tt.data), true)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if out.Plan != tt.wantPlan {
				t.Fatalf("plan %q, want %q", out.Plan, tt.wantPlan)
			}
			q, ok := out.Quotas[tt.wantKey]
			if !ok {
				t.Fatalf("no %q row: %+v", tt.wantKey, out)
			}
			if q.Used != tt.wantUsed || q.Total != tt.wantTot || q.RemainingPercentage != tt.wantLeft {
				t.Fatalf("row %+v, want used %v total %v left %v", q, tt.wantUsed, tt.wantTot, tt.wantLeft)
			}
		})
	}
}

func TestAReadingCarriesTheCredentialTheProviderExpects(t *testing.T) {
	s := newStub().on("api.github.com", "/copilot_internal/user", `{"copilot_plan":"x"}`)
	r := testReader(s, connOf("github", "oauth", ""), "gh-token")
	if _, err := r.Read(context.Background(), connOf("github", "oauth", ""), true); err != nil {
		t.Fatal(err)
	}
	if got := s.header("api.github.com/copilot_internal/user", "Authorization"); got != "token gh-token" {
		t.Fatalf("authorization %q", got)
	}
	if got := s.header("api.github.com/copilot_internal/user", "X-GitHub-Api-Version"); got == "" {
		t.Fatal("the API version header is missing")
	}

	kimi := newStub().on("api.kimi.com", "/coding/v1/usages", `{"usage":{"limit":10,"used":1}}`)
	conn := connOf("kimi", "oauth", `{"providerSpecificData":{"deviceId":"dev-9"}}`)
	kr := testReader(kimi, conn, "k-token")
	if _, err := kr.Read(context.Background(), conn, true); err != nil {
		t.Fatal(err)
	}
	if got := kimi.header("api.kimi.com/coding/v1/usages", "Authorization"); got != "Bearer k-token" {
		t.Fatalf("kimi authorization %q", got)
	}
	if got := kimi.header("api.kimi.com/coding/v1/usages", "X-Msh-Device-Id"); got != "dev-9" {
		t.Fatalf("kimi device %q", got)
	}

	key := newStub().on("api.kimi.com", "/coding/v1/usages", `{"usage":{"limit":10,"used":1}}`)
	kr = testReader(key, connOf("kimi", "apikey", ""), "sk-kimi")
	if _, err := kr.Read(context.Background(), connOf("kimi", "apikey", ""), true); err != nil {
		t.Fatal(err)
	}
	if got := key.header("api.kimi.com/coding/v1/usages", "x-api-key"); got != "sk-kimi" {
		t.Fatalf("kimi key header %q", got)
	}
}

func TestARefusedCredentialIsRenewedOnce(t *testing.T) {
	s := newStub().
		code("www.codebuff.com", "/api/v1/freebuff/session", http.StatusUnauthorized).
		on("www.codebuff.com", "/api/v1/freebuff/session", `{"status":"active","rateLimit":{"limit":2,"recentCount":0}}`)
	conn := connOf("freebuff", "oauth", "")
	r := testReader(s, conn, "first")
	renewed := 0
	r.Renew = func(context.Context, repository.Connection) (repository.Connection, error) {
		renewed++
		// The second call is served: the stub answers its status first, so a
		// renewed reading still sees the refusal unless the caller changes it.
		return conn, nil
	}
	// A 401 stays a 401 here, so the retry must surface the refusal rather than
	// loop or answer a reading.
	out, err := r.Read(context.Background(), conn, true)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("%v %+v", err, out)
	}
	if renewed != 1 || s.counted("www.codebuff.com/api/v1/freebuff/session") != 2 {
		t.Fatalf("renewed %d calls %d", renewed, s.counted("www.codebuff.com/api/v1/freebuff/session"))
	}
}

func TestRenewalReplacesTheRefusedToken(t *testing.T) {
	s := newStub().
		code("www.codebuff.com", "/api/v1/freebuff/session", http.StatusUnauthorized).
		on("www.codebuff.com", "/api/v1/freebuff/session", `{"status":"active","rateLimit":{"limit":2,"recentCount":0}}`)
	fresh := connOf("freebuff", "oauth", "")
	renewed := false
	// The retry presents whatever the renewal handed back.
	r := &Reader{
		Client: &http.Client{Transport: s},
		Now:    func() time.Time { return testNow },
	}
	r.Access = func(_ context.Context, conn repository.Connection) (Access, error) {
		return Access{Token: "first", Conn: conn}, nil
	}
	r.Renew = func(context.Context, repository.Connection) (repository.Connection, error) {
		renewed = true
		s.code("www.codebuff.com", "/api/v1/freebuff/session", 0)
		return fresh, nil
	}
	out, err := r.Read(context.Background(), fresh, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Quotas) != 1 || !renewed {
		t.Fatalf("reading %+v renewed=%v", out, renewed)
	}
}

func TestAnUnknownOrIneligibleConnectionIsNotRead(t *testing.T) {
	deferred := connOf("minimax-code", "oauth", "")
	if _, err := testReader(newStub(), deferred, "t").Read(context.Background(), deferred, true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("%v", err)
	}
	if Supports("qoder") || Supports("xiaomi-mimo") || Supports("minimax-code") {
		t.Fatal("a deferred provider answers")
	}
	for _, tt := range []struct {
		provider, auth string
		ok             bool
	}{
		{"claude", "oauth", true},
		{"claude", "apikey", false},
		{"glm", "apikey", true},
		{"deepseek", "oauth", false},
		{"deepseek", "cookie", false},
		{"codex", "access_token", true},
	} {
		if got := Serves(tt.provider, tt.auth); got != tt.ok {
			t.Errorf("Serves(%s, %s) = %v", tt.provider, tt.auth, got)
		}
	}
}

func TestAReadingIsCachedUntilForced(t *testing.T) {
	s := newStub().on("api.deepseek.com", "/user/balance", `{"balance_infos":[{"currency":"USD","total_balance":"2"}]}`)
	conn := connOf("deepseek", "apikey", "")
	r := testReader(s, conn, "sk")
	r.Cache = cache.New(tests.NewCache(), cache.WithoutShared())
	for range 3 {
		if _, err := r.Read(context.Background(), conn, false); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.counted("api.deepseek.com/user/balance"); n != 1 {
		t.Fatalf("calls %d", n)
	}
	if _, err := r.Read(context.Background(), conn, true); err != nil {
		t.Fatal(err)
	}
	if n := s.counted("api.deepseek.com/user/balance"); n != 2 {
		t.Fatalf("a forced read called %d times", n)
	}
}

func TestASoftFailureIsNotCached(t *testing.T) {
	s := newStub().code("api.deepseek.com", "/user/balance", http.StatusInternalServerError)
	conn := connOf("deepseek", "apikey", "")
	r := testReader(s, conn, "sk")
	r.Cache = cache.New(tests.NewCache(), cache.WithoutShared())
	out, err := r.Read(context.Background(), conn, false)
	if err != nil || out.Message == "" {
		t.Fatalf("%v %+v", err, out)
	}
	if _, err := r.Read(context.Background(), conn, false); err != nil {
		t.Fatal(err)
	}
	if n := s.counted("api.deepseek.com/user/balance"); n != 2 {
		t.Fatalf("a failure served %d call(s); it must be asked again", n)
	}
}

func TestAResetGrantIsValidatedBeforeAnyCall(t *testing.T) {
	s := newStub()
	conn := connOf("claude", "oauth", "")
	r := testReader(s, conn, "t")
	for _, grant := range []string{"", "BAD ID", strings.Repeat("a", 41)} {
		if _, err := r.ClaudeReset(context.Background(), conn, grant); !errors.Is(err, ErrInvalidGrant) {
			t.Fatalf("%q: %v", grant, err)
		}
	}
	if len(s.calls) != 0 {
		t.Fatalf("an invalid grant was sent: %v", s.calls)
	}
}

func TestAClaudeGrantIsSpentOnTheAccountOrganization(t *testing.T) {
	s := newStub().
		on("api.anthropic.com", "/api/oauth/profile", `{"organization":{"uuid":"org-9"}}`).
		on("api.anthropic.com", "/api/organizations/org-9/reset_rate_limits", `{"result":"reset","resets_left":2}`)
	r := testReader(s, connOf("claude", "oauth", ""), "t")
	out, err := r.ClaudeReset(context.Background(), connOf("claude", "oauth", ""), "grant-1")
	if err != nil || !out.OK || out.ResetsLeft != 2 {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestACodexCreditSpendReportsTheServiceAnswer(t *testing.T) {
	s := newStub().
		code("chatgpt.com", "/backend-api/wham/rate-limit-reset-credits/consume", http.StatusConflict).
		on("chatgpt.com", "/backend-api/wham/rate-limit-reset-credits/consume", `{"code":"no_credit","windows_reset":0,"message":"No Codex reset credits available."}`)
	r := testReader(s, connOf("codex", "oauth", ""), "t")
	out, err := r.CodexConsume(context.Background(), connOf("codex", "oauth", ""))
	if err != nil {
		t.Fatal(err)
	}
	if out.Reset || out.Code != "no_credit" || out.RedeemRequestID == "" || out.Status != http.StatusConflict {
		t.Fatalf("%+v", out)
	}
}

func TestTheReadingWireShape(t *testing.T) {
	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	out := Reading{
		Plan: "Pro",
		Quotas: map[string]Quota{
			"Session (5h)": {Used: 42, Total: 100, RemainingPercentage: 58, ResetAt: stamp(reset), DisplayName: "Session (5h)", Recurring: true},
		},
		Message: "note",
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"plan":"Pro","quotas":{"Session (5h)":{"used":42,"total":100,"remainingPercentage":58,"resetAt":"2026-12-01T00:00:00Z","recurring":true,"displayName":"Session (5h)"}},"message":"note"}`
	if string(raw) != want {
		t.Fatalf("wire\n got %s\nwant %s", raw, want)
	}
}

func TestTheAntigravitySnapshotBecomesRows(t *testing.T) {
	out := snapshotReading(Snapshot{
		GeminiSession:         {Remaining: 0, ResetAt: testNow.Add(time.Hour)},
		"gemini-4-argon-high": {Remaining: 50},
	})
	if len(out.Quotas) != 2 {
		t.Fatalf("%+v", out)
	}
	if got := out.Quotas["Gemini session (5h)"]; got.Used != 100 || got.RemainingPercentage != 0 {
		t.Fatalf("spent row %+v", got)
	}
	if got := out.Quotas["Gemini-4-argon-high"]; got.Used != 50 {
		t.Fatalf("model row %+v", got)
	}
	if versionOf("antigravity/hub/2.24.0 darwin/arm64") != "2.24.0" {
		t.Fatalf("version %q", versionOf("antigravity/hub/2.24.0 darwin/arm64"))
	}
}

func TestTheTableCoversEveryReaderItClaims(t *testing.T) {
	if len(fetchers) < 20 {
		t.Fatalf("only %d readers", len(fetchers))
	}
	if !Supports("claude") || Supports("iflow") || Supports("ollama-web") {
		t.Fatal("the dispatch table names the wrong providers")
	}
}

func TestAShareOutsideTheHundredIsClamped(t *testing.T) {
	if q := share(-5, time.Time{}); q.Used != 0 || q.RemainingPercentage != 100 {
		t.Fatalf("below zero %+v", q)
	}
	if q := share(140, time.Time{}); q.Used != 100 || q.RemainingPercentage != 0 {
		t.Fatalf("above a hundred %+v", q)
	}
	if q := window(20, 0, time.Time{}); !q.Unlimited || q.RemainingPercentage != 100 {
		t.Fatalf("an uncapped row %+v", q)
	}
}
