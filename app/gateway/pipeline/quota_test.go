package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/account"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

type fakeQuota struct {
	mu      sync.Mutex
	held    map[string]time.Time
	refused map[string]time.Time
	log     []string
}

func (q *fakeQuota) Skip(account, model string) (time.Time, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	t, ok := q.held[account]
	return t, ok
}

func (q *fakeQuota) Refused(_ context.Context, account, access, model string) (time.Time, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.log = append(q.log, "refused:"+account+":"+access+":"+model)
	t, ok := q.refused[account]
	if ok {
		if q.held == nil {
			q.held = map[string]time.Time{}
		}
		q.held[account] = t
	}
	return t, ok
}

func (q *fakeQuota) Succeeded(account, model string) {
	q.mu.Lock()
	q.log = append(q.log, "ok:"+account+":"+model)
	q.mu.Unlock()
}

// lockedFor reports a lock on a model of a stored connection, read the way the
// selector reads it.
func (r *rig) lockedFor(id, model string) bool {
	r.t.Helper()
	c, ok, err := r.st.Connections().Get(context.Background(), id)
	if err != nil || !ok {
		r.t.Fatalf("get %s: %v %v", id, ok, err)
	}
	return account.LockActive(c.Doc(), model, time.Now())
}

// agy adds an Antigravity account with an email of its own: two accounts with one
// email are one account.
func (r *rig) agy(token string) repository.Connection {
	r.t.Helper()
	f := map[string]string{"accessToken": `"` + token + `"`, "projectId": `"my-proj-123"`, "testStatus": `"active"`}
	c, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "antigravity", AuthType: "oauth", Email: token + "@x.test", Fields: f,
	}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

const agyBody = `{"model":"antigravity/claude-opus-5-5-high","messages":[{"role":"user","content":"hi"}]}`

func TestASpentAntigravityAccountIsPassedOverWithoutALockAndTheNextServes(t *testing.T) {
	// The executor retries a 429 in place three more times before it gives the
	// answer to the pipeline, so the first account takes four of the replies.
	quota := tests.Reply{Status: 429, ContentType: "application/json", Body: `{"error":{"message":"quota"}}`}
	r := newRig(t, quota, quota, quota, quota, tests.Reply{ContentType: "application/json", Body: geminiAnswer})
	a, b := r.agy("TOK-A"), r.agy("TOK-B")
	q := &fakeQuota{refused: map[string]time.Time{a.ID: time.Now().Add(2 * time.Hour), b.ID: time.Now().Add(2 * time.Hour)}}
	r.p.d.Quota = q
	out, _ := r.do("/v1/chat/completions", agyBody)
	if out.Status != 200 || len(r.up.Calls()) != 5 {
		t.Fatalf("%d %s, %d calls", out.Status, out.Body, len(r.up.Calls()))
	}
	first := gjson.GetBytes(r.up.Calls()[0].Body, "project").String()
	if first != "my-proj-123" {
		t.Fatalf("%s", r.up.Calls()[0].Body)
	}
	for _, id := range []string{a.ID, b.ID} {
		if r.lockedFor(id, "claude-opus-5-5-high") {
			t.Fatal("the gate left a stored lock")
		}
	}
	if len(q.log) < 2 || q.log[0][:8] != "refused:" || q.log[len(q.log)-1][:3] != "ok:" {
		t.Fatalf("%v", q.log)
	}
}

func TestEveryAntigravityAccountSpentAnswersWithTheEarliestReset(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: geminiAnswer})
	a, b := r.agy("TOK-A"), r.agy("TOK-B")
	soon, late := time.Now().Add(20*time.Minute), time.Now().Add(3*time.Hour)
	r.p.d.Quota = &fakeQuota{held: map[string]time.Time{a.ID: late, b.ID: soon}}
	out, _ := r.do("/v1/chat/completions", agyBody)
	if out.Status != 503 || len(r.up.Calls()) != 0 {
		t.Fatalf("%d %s, %d calls", out.Status, out.Body, len(r.up.Calls()))
	}
	if out.Header.Get("Retry-After") == "" {
		t.Fatalf("no retry hint: %v", out.Header)
	}
}

func TestAnAntigravityRefusalTheGateCannotPlaceFallsBackToTheCommonRules(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 429, ContentType: "application/json", Body: `{"error":{"message":"slow down","resets_at":4102444800}}`, Header: map[string]string{"Retry-After": "3600"}})
	a := r.agy("TOK-A")
	q := &fakeQuota{}
	r.p.d.Quota = q
	out, _ := r.do("/v1/chat/completions", agyBody)
	if out.Status == 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if !r.lockedFor(a.ID, "claude-opus-5-5-high") {
		t.Fatal("the common lock was not applied when the gate had no time")
	}
}

func TestOnlyAQuotaStatusReachesTheGateAndOnlyForAntigravity(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 500, ContentType: "application/json", Body: `{"error":{"message":"boom"}}`})
	r.agy("TOK-A")
	q := &fakeQuota{}
	r.p.d.Quota = q
	r.do("/v1/chat/completions", agyBody)
	for _, l := range q.log {
		if l[:8] == "refused:" {
			t.Fatalf("a 500 reached the gate: %v", q.log)
		}
	}
	r2 := newRig(t, tests.Reply{Status: 429, ContentType: "application/json", Body: `{"error":{"message":"quota"}}`})
	r2.google("stored-proj-1")
	q2 := &fakeQuota{}
	r2.p.d.Quota = q2
	r2.do("/v1/chat/completions", `{"model":"gcl/gm-1","messages":[{"role":"user","content":"hi"}]}`)
	if len(q2.log) != 0 {
		t.Fatalf("another provider reached the gate: %v", q2.log)
	}
}
