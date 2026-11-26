package pipeline

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/access/identity"
	"rllmx/app/access/quota"
	"rllmx/tests"
)

// spend is a budget that answers the same thing every time.
type spend struct {
	ref quota.Refusal
	err error
}

func (s spend) Check(context.Context, string) (quota.Refusal, error) { return s.ref, s.err }

func refused() spend {
	return spend{ref: quota.Refusal{Refused: true, RetryAfter: 90 * time.Second, Remaining: quota.Remaining{Requests: 0, Tokens: -1, Cost: 12.5}}}
}

func TestASpentBudgetIsAnsweredBeforeAnyCall(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	conn := r.conn("forced", "k1")
	r.p.d.Budget = refused()
	out, _ := r.do("/v1/chat/completions", `{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != http.StatusTooManyRequests {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if got := gjson.GetBytes(out.Body, "error.code").String(); got != "insufficient_quota" {
		t.Fatalf("code = %q in %s", got, out.Body)
	}
	if got := out.Header.Get("X-Rllm-Quota-Remaining-Cost"); got != "12.5" {
		t.Fatalf("remaining cost = %q", got)
	}
	if out.Header.Get("X-Rllm-Quota-Remaining-Tokens") != "" {
		t.Fatal("an unlimited type was reported")
	}
	if got := out.Header.Get("Retry-After"); got != "90" {
		t.Fatalf("Retry-After = %q", got)
	}
	if len(r.up.Calls()) != 0 {
		t.Fatal("a spent budget reached the provider")
	}
	// The row still names what the key asked for and the account it would have
	// reached, so the log line is not a row of dashes; the class says the key's
	// budget rather than the provider's pace, and attempt 0 says nothing was sent.
	rows := r.rows.All()
	if len(rows) != 1 {
		t.Fatalf("rows %d", len(rows))
	}
	if row := rows[0]; row.Provider != "forced" || row.Model != "fm" || row.ErrorClass != "key_quota" {
		t.Fatalf("row %+v", row)
	}
	if row := rows[0]; row.ConnectionID != conn.ID || row.Attempt != 0 {
		t.Fatalf("row names account %q at attempt %d", row.ConnectionID, row.Attempt)
	}
	// The peek must not have recorded a use on that account.
	c, ok, err := r.st.Connections().Get(context.Background(), conn.ID)
	if err != nil || !ok {
		t.Fatalf("reading the connection: %v %v", ok, err)
	}
	if _, used := c.Doc().GetString("lastUsedAt"); used {
		t.Fatal("naming the intended account recorded a use")
	}
}

func TestABudgetThatCannotBeReadFailsClosed(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	r.p.d.Budget = spend{err: errors.New("analytics is down")}
	out, _ := r.do("/v1/chat/completions", `{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != http.StatusServiceUnavailable || strings.Contains(string(out.Body), "analytics") {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if len(r.up.Calls()) != 0 {
		t.Fatal("the request went through without a budget check")
	}
}

func TestACachedAnswerIsNotServedToASpentBudget(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	in := r.use(chatBody)
	if first, _ := r.send(in); first.Status != http.StatusOK || first.Header.Get(CacheHeader) != "miss" {
		t.Fatalf("the warm-up: %d %v", first.Status, first.Header)
	}
	calls := len(r.up.Calls())
	r.p.d.Budget = refused()
	out, _ := r.send(in)
	if out.Status != http.StatusTooManyRequests || out.Header.Get(CacheHeader) != "" {
		t.Fatalf("a cached answer was served to a spent budget: %d %v", out.Status, out.Header)
	}
	if len(r.up.Calls()) != calls {
		t.Fatal("the refused request reached the provider")
	}
}

func TestAKeyWithNoBudgetIsNotChecked(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	if out := r.runAs(identity.Caller{ID: "k", Anonymous: true}, "/v1/chat/completions", `{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`); out.Status != http.StatusOK {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}
