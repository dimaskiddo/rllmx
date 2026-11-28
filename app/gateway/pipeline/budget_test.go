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
	"rllmx/app/llm/stream"
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

func TestAComboRefusedByItsBudgetKeepsTheComboName(t *testing.T) {
	r := comboRig(t, []string{"forced/fm", "wire/short"})
	r.conn("forced", "k1")
	r.conn("wire", "k3")
	r.p.d.Budget = refused()
	out, _ := r.do("/v1/chat/completions", comboAsk)
	if out.Status != http.StatusTooManyRequests {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	// No member was chosen, so the row names the combo rather than inventing one.
	if row := r.last(); row.Model != "chain" || row.Provider != "" {
		t.Fatalf("a pre-walk refusal named a member it never chose: %+v", row)
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
	if row := r.last(); row.ErrorClass != "budget_unavailable" {
		t.Fatalf("a failed read is filed as %q", row.ErrorClass)
	}
}

// stalledBudget blocks until the request ends, the way a store read holds the
// caller while the connection behind it closes.
type stalledBudget struct{}

func (stalledBudget) Check(ctx context.Context, _ string) (quota.Refusal, error) {
	<-ctx.Done()
	return quota.Refusal{}, ctx.Err()
}

func TestABudgetCheckTheRequestAbandonedIsNotAServerFault(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		class  string
		ctx    func() (context.Context, context.CancelFunc)
	}{
		{"the client leaves", 499, "client_closed", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(20*time.Millisecond, cancel)
			return ctx, cancel
		}},
		{"the call's deadline passes", http.StatusGatewayTimeout, "timeout", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 20*time.Millisecond)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
			r.conn("forced", "k1")
			r.p.d.Budget = stalledBudget{}
			ctx, cancel := tt.ctx()
			defer cancel()
			in := Input{Path: "/v1/chat/completions", Body: []byte(`{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`),
				Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-13"}
			out := r.p.Handle(ctx, in, func() (stream.Client, error) { return nil, errors.New("never") })
			if out.Status != tt.status {
				t.Fatalf("status %d, want %d", out.Status, tt.status)
			}
			if row := r.last(); row.ErrorClass != tt.class {
				t.Fatalf("class %q, want %q", row.ErrorClass, tt.class)
			}
			if len(r.up.Calls()) != 0 {
				t.Fatal("a request whose context ended reached the provider")
			}
		})
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
