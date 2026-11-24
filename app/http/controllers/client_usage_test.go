package controllers

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/identity"
	"rllmx/app/access/quota"
	apphttp "rllmx/app/http"
	"rllmx/tests"
)

// usageCaller stands in for the guard on a client API route.
type usageCaller struct{ id string }

func (usageCaller) Signature() string { return "test:caller" }
func (m usageCaller) Handle(ctx contractshttp.Context) {
	if m.id != "none" {
		apphttp.WithCaller(ctx, identity.Caller{ID: m.id, Name: "ci"})
	}
	ctx.Request().Next()
}

var usageDay = time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
var usageMonth = time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

func usageRoutes(t *testing.T, caller string, standing func(context.Context, string) (quota.Standings, error)) nethttp.Handler {
	t.Helper()
	c := &ClientUsageController{Standing: standing}
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware([]contractshttp.Middleware{usageCaller{id: caller}})
	r.Get("/api/oauth/usage", c.Claude)
	r.Get("/v1/organizations/{org}/usage", c.Claude)
	r.Get("/v1/usage", c.OpenCode)
	return r
}

func standingAt(day, month float64) func(context.Context, string) (quota.Standings, error) {
	return standingFor("key-1", day, month)
}

// standingFor is standingAt for one named key; every other key has no budget.
func standingFor(key string, day, month float64) func(context.Context, string) (quota.Standings, error) {
	return func(_ context.Context, id string) (quota.Standings, error) {
		if id != key {
			return quota.Standings{Day: quota.Standing{ResetsAt: usageDay}, Month: quota.Standing{ResetsAt: usageMonth}}, nil
		}
		return quota.Standings{
			Day:   quota.Standing{Limited: day > 0, Percent: day, ResetsAt: usageDay},
			Month: quota.Standing{Limited: month > 0, Percent: month, ResetsAt: usageMonth},
		}, nil
	}
}

func getJSON(t *testing.T, h nethttp.Handler, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(nethttp.MethodGet, path, nil))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestTheClaudeUsageAnswersTheKeysBudgetInTheShapeAClaudeClientReads(t *testing.T) {
	h := usageRoutes(t, "key-1", standingAt(75, 20))
	for _, path := range []string{"/api/oauth/usage", "/v1/organizations/org-1/usage"} {
		rec, out := getJSON(t, h, path)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		five, _ := out["five_hour"].(map[string]any)
		seven, _ := out["seven_day"].(map[string]any)
		if five["utilization"] != float64(75) || seven["utilization"] != float64(20) {
			t.Fatalf("%s: %v", path, out)
		}
		if five["resets_at"] != "2026-03-15T00:00:00Z" || seven["resets_at"] != "2026-04-01T00:00:00Z" {
			t.Fatalf("%s: reset times %v %v", path, five["resets_at"], seven["resets_at"])
		}
		if got := rec.Header().Get("Anthropic-Ratelimit-Unified-Status"); got != "allowed" {
			t.Fatalf("%s: status header %q", path, got)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: a usage answer must not be cached", path)
		}
	}
}

func TestASpentBudgetReadsRejectedAndNamesTheTightestWindowsReset(t *testing.T) {
	rec, _ := getJSON(t, usageRoutes(t, "key-1", standingAt(100, 20)), "/api/oauth/usage")
	if rec.Header().Get("Anthropic-Ratelimit-Unified-Status") != "rejected" {
		t.Fatalf("%v", rec.Header())
	}
	if rec.Header().Get("Anthropic-Ratelimit-Unified-Reset") != "1773532800" {
		t.Fatalf("the spent day resets at midnight: %q", rec.Header().Get("Anthropic-Ratelimit-Unified-Reset"))
	}
}

func TestAKeyWithNoBudgetReadsZeroUsed(t *testing.T) {
	rec, out := getJSON(t, usageRoutes(t, "other-key", standingAt(75, 20)), "/api/oauth/usage")
	five, _ := out["five_hour"].(map[string]any)
	if rec.Code != 200 || five["utilization"] != float64(0) {
		t.Fatalf("%d %v", rec.Code, out)
	}
	if rec.Header().Get("Anthropic-Ratelimit-Unified-Status") != "allowed" {
		t.Fatalf("%v", rec.Header())
	}
}

func TestTheOpenCodeUsageAnswersRollingAndMonthlyAndNothingItCannotBack(t *testing.T) {
	rec, out := getJSON(t, usageRoutes(t, "key-1", standingAt(75, 20)), "/v1/usage")
	usage, _ := out["usage"].(map[string]any)
	rolling, _ := usage["rolling"].(map[string]any)
	monthly, _ := usage["monthly"].(map[string]any)
	if rec.Code != 200 || rolling["percent"] != float64(75) || monthly["percent"] != float64(20) {
		t.Fatalf("%d %v", rec.Code, out)
	}
	if rolling["resetsAt"] != "2026-03-15T00:00:00Z" {
		t.Fatalf("resetsAt %v", rolling["resetsAt"])
	}
	if _, invented := usage["weekly"]; invented {
		t.Fatalf("a weekly window the key has no budget for was made up: %v", usage)
	}
}

func TestAUsageReadThatFailsIsNotReadAsAnEmptyBudget(t *testing.T) {
	failing := func(context.Context, string) (quota.Standings, error) { return quota.Standings{}, errors.New("down") }
	for _, path := range []string{"/api/oauth/usage", "/v1/usage"} {
		if rec, _ := getJSON(t, usageRoutes(t, "key-1", failing), path); rec.Code != nethttp.StatusServiceUnavailable {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
}

func TestAUsageReadWithoutACallerIsRefused(t *testing.T) {
	if rec, _ := getJSON(t, usageRoutes(t, "none", standingAt(1, 1)), "/api/oauth/usage"); rec.Code != nethttp.StatusUnauthorized {
		t.Fatalf("%d", rec.Code)
	}
}

func TestABudgetedKeysAnswerCarriesItsRateLimitStanding(t *testing.T) {
	r := newChatRig(t, 1<<20, tests.Reply{ContentType: "application/json", Body: chatReply})
	r.chat.Standing = standingFor("key-7", 40, 10)
	rec := r.post(context.Background(), `{"model":"`+chatModel+`","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Anthropic-Ratelimit-Unified-Status") != "allowed" || rec.Header().Get("Anthropic-Ratelimit-Unified-Reset") != "1773532800" {
		t.Fatalf("%v", rec.Header())
	}
}

func TestASpentBudgetsRefusalReadsRejected(t *testing.T) {
	r := newChatRig(t, 1<<20, tests.Reply{ContentType: "application/json", Body: chatReply})
	r.chat.Standing = standingFor("key-7", 100, 10)
	rec := r.post(context.Background(), `{"model":"`+chatModel+`","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Header().Get("Anthropic-Ratelimit-Unified-Status") != "rejected" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}

func TestAKeyWithoutABudgetGetsNoInventedRateLimitHeaders(t *testing.T) {
	r := newChatRig(t, 1<<20, tests.Reply{ContentType: "application/json", Body: chatReply})
	r.chat.Standing = standingFor("key-7", 0, 0)
	rec := r.post(context.Background(), `{"model":"`+chatModel+`","messages":[{"role":"user","content":"hi"}]}`, nil)
	for name := range rec.Header() {
		if strings.HasPrefix(strings.ToLower(name), "anthropic-ratelimit") {
			t.Fatalf("a key with no budget was given %s", name)
		}
	}
}

func TestAStandingThatCannotBeReadDoesNotStopTheAnswer(t *testing.T) {
	r := newChatRig(t, 1<<20, tests.Reply{ContentType: "application/json", Body: chatReply})
	r.chat.Standing = func(context.Context, string) (quota.Standings, error) { return quota.Standings{}, errors.New("down") }
	if rec := r.post(context.Background(), `{"model":"`+chatModel+`","messages":[{"role":"user","content":"hi"}]}`, nil); rec.Code != 200 {
		t.Fatalf("a header's source failing stopped the request: %d", rec.Code)
	}
}
