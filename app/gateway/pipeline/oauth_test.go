package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/oauth"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

const claudeMsg = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[{"type":"text","text":"hey"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`

const subBody = `{"model":"sub/claude-x","messages":[{"role":"user","content":"hi"}]}`

// fakeRefresher swaps tokens in the store, the way the real one does, and
// counts its calls.
type fakeRefresher struct {
	mu sync.Mutex
	st interface {
		Connections() *repository.ConnectionsRepo
	}
	ready    int
	renew    int
	readyErr error
	renewErr error
	// readyTo and renewTo are the access token written when the call succeeds.
	readyTo, renewTo string
}

func (f *fakeRefresher) swap(ctx context.Context, conn repository.Connection, to string) (repository.Connection, error) {
	old, _ := conn.Doc().GetString("refreshToken")
	got, _, err := f.st.Connections().SwapCredentials(ctx, conn.ID, old, map[string]string{"accessToken": `"` + to + `"`, "refreshToken": `"` + to + `-rt"`})
	return got, err
}

func (f *fakeRefresher) Ready(ctx context.Context, conn repository.Connection) (repository.Connection, error) {
	f.mu.Lock()
	f.ready++
	err, to := f.readyErr, f.readyTo
	f.mu.Unlock()
	if err != nil || to == "" {
		return conn, err
	}
	return f.swap(ctx, conn, to)
}

func (f *fakeRefresher) Renew(ctx context.Context, conn repository.Connection) (repository.Connection, error) {
	f.mu.Lock()
	f.renew++
	err, to := f.renewErr, f.renewTo
	f.mu.Unlock()
	if err != nil {
		return conn, err
	}
	if to == "" {
		return conn, nil
	}
	return f.swap(ctx, conn, to)
}

func (r *rig) login(email, access, refresh string) repository.Connection {
	r.t.Helper()
	c, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "sub", AuthType: "oauth", Email: email,
		Fields: map[string]string{"accessToken": `"` + access + `"`, "refreshToken": `"` + refresh + `"`, "testStatus": `"active"`},
	}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

func (r *rig) withRefresher(f *fakeRefresher) *fakeRefresher {
	f.st = r.st
	r.p.d.Refresher = f
	return f
}

func TestALoginTokenGoesAsABearerWithTheProvidersHeadersAndNoKeyHeader(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	r.login("a@x.test", "AT-1", "RT-1")
	out, _ := r.do("/v1/chat/completions", subBody)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	if auth(call) != "Bearer AT-1" || call.Header.Get("x-api-key") != "" {
		t.Fatalf("auth %q, x-api-key %q", auth(call), call.Header.Get("x-api-key"))
	}
	if call.Header.Get("anthropic-beta") != "base-a,oauth-x,redact-y" || call.Header.Get("anthropic-version") == "" {
		t.Fatalf("%v", call.Header)
	}
}

func TestAnAPIKeyOnTheSameProviderKeepsItsOwnScheme(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	if _, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "sub", AuthType: "apikey", Name: "k", Fields: map[string]string{"apiKey": `"sk-1"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	f := r.withRefresher(&fakeRefresher{readyTo: "never", renewTo: "never"})
	out, _ := r.do("/v1/chat/completions", subBody)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	if call.Header.Get("x-api-key") != "sk-1" || auth(call) != "" || call.Header.Get("anthropic-beta") != "" {
		t.Fatalf("%v", call.Header)
	}
	if f.ready != 0 || f.renew != 0 {
		t.Fatalf("a key was renewed: %d %d", f.ready, f.renew)
	}
}

func TestADueTokenIsRenewedBeforeTheRequestIsSent(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	f := r.withRefresher(&fakeRefresher{readyTo: "AT-2"})
	r.login("a@x.test", "AT-1", "RT-1")
	out, _ := r.do("/v1/chat/completions", subBody)
	if out.Status != 200 || f.ready != 1 || f.renew != 0 {
		t.Fatalf("%d, ready %d, renew %d", out.Status, f.ready, f.renew)
	}
	if calls := r.up.Calls(); len(calls) != 1 || auth(calls[0]) != "Bearer AT-2" {
		t.Fatalf("%d calls, auth %q", len(calls), auth(calls[0]))
	}
}

func TestAFailedProactiveRenewalStillSendsWithTheOldToken(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	f := r.withRefresher(&fakeRefresher{readyErr: &oauth.Error{Class: oauth.ClassRejected}})
	r.login("a@x.test", "AT-1", "RT-1")
	out, _ := r.do("/v1/chat/completions", subBody)
	if out.Status != 200 || auth(r.up.Calls()[0]) != "Bearer AT-1" || f.ready != 1 {
		t.Fatalf("%d %q", out.Status, auth(r.up.Calls()[0]))
	}
}

func TestARefusedTokenIsRenewedAndTheRequestSentOnceMore(t *testing.T) {
	for _, status := range []int{401, 403} {
		r := newRig(t, tests.Reply{Status: status, Body: `{"error":{"message":"expired"}}`}, tests.Reply{ContentType: "application/json", Body: claudeMsg})
		f := r.withRefresher(&fakeRefresher{renewTo: "AT-2"})
		r.login("a@x.test", "AT-1", "RT-1")
		out, _ := r.do("/v1/chat/completions", subBody)
		if out.Status != 200 {
			t.Fatalf("%d: %d %s", status, out.Status, out.Body)
		}
		calls := r.up.Calls()
		if len(calls) != 2 || auth(calls[0]) != "Bearer AT-1" || auth(calls[1]) != "Bearer AT-2" || f.renew != 1 {
			t.Fatalf("%d: %d calls, renew %d", status, len(calls), f.renew)
		}
		if r.last().Attempt != 1 {
			t.Fatalf("a renewed retry counted as another account: attempt %d", r.last().Attempt)
		}
	}
}

func TestASecondRefusalIsAnAccountFailureAndLocksIt(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 401, Body: `{"error":{"message":"nope"}}`})
	f := r.withRefresher(&fakeRefresher{renewTo: "AT-2"})
	c := r.login("a@x.test", "AT-1", "RT-1")
	out, _ := r.do("/v1/chat/completions", subBody)
	if out.Status != 502 || r.last().ErrorClass != "auth" {
		t.Fatalf("%d %+v", out.Status, r.last())
	}
	if n := len(r.up.Calls()); n != 2 || f.renew != 1 {
		t.Fatalf("%d calls, renew %d: the retry must happen once", n, f.renew)
	}
	got, _, _ := r.st.Connections().Get(context.Background(), c.ID)
	if st, _ := got.Doc().GetString("testStatus"); st == "active" {
		t.Fatal("a refused renewed token left the account unlocked")
	}
}

func TestARenewalThatFailsForOtherReasonsReportsTheFirstRefusal(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 401, Body: `{"error":{"message":"first-refusal"}}`})
	r.withRefresher(&fakeRefresher{renewErr: &oauth.Error{Class: oauth.ClassTransport}})
	r.login("a@x.test", "AT-1", "RT-1")
	out, _ := r.do("/v1/chat/completions", subBody)
	if out.Status != 502 || len(r.up.Calls()) != 1 {
		t.Fatalf("%d, %d calls", out.Status, len(r.up.Calls()))
	}
	if !strings.Contains(string(out.Body), "first-refusal") {
		t.Fatalf("the first refusal was lost: %s", out.Body)
	}
}

func TestADeadAccountIsSkippedWithoutALockAndTheNextOneServes(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	f := r.withRefresher(&fakeRefresher{})
	dead := r.login("dead@x.test", "AT-d", "RT-d")
	live := r.login("live@x.test", "AT-l", "RT-l")
	f.readyErr = nil
	f.mu.Lock()
	f.readyTo = ""
	f.mu.Unlock()
	r.p.d.Refresher = &onlyFor{inner: f, dead: dead.ID}

	out, _ := r.do("/v1/chat/completions", subBody)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if calls := r.up.Calls(); len(calls) != 1 || auth(calls[0]) != "Bearer AT-l" {
		t.Fatalf("%d calls, auth %q", len(calls), auth(calls[0]))
	}
	got, _, _ := r.st.Connections().Get(context.Background(), dead.ID)
	if _, locked := got.Doc().GetString("modelLock___all"); locked {
		t.Fatal("a dead account was also locked")
	}
	if st, _ := got.Doc().GetString("testStatus"); st != "active" {
		t.Fatalf("the pipeline wrote the dead account's status: %q", st)
	}
	_ = live
}

// onlyFor reports one connection as dead and leaves the others alone.
type onlyFor struct {
	inner *fakeRefresher
	dead  string
}

func (o *onlyFor) Ready(ctx context.Context, c repository.Connection) (repository.Connection, error) {
	if c.ID == o.dead {
		return c, oauth.ErrExpired
	}
	return o.inner.Ready(ctx, c)
}

func (o *onlyFor) Renew(ctx context.Context, c repository.Connection) (repository.Connection, error) {
	if c.ID == o.dead {
		return c, oauth.ErrExpired
	}
	return o.inner.Renew(ctx, c)
}

func TestOnlyDeadAccountsAnswer503NeedsToBeReconnected(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	c := r.login("dead@x.test", "AT-d", "RT-d")
	r.p.d.Refresher = &onlyFor{inner: &fakeRefresher{st: r.st}, dead: c.ID}
	out, _ := r.do("/v1/chat/completions", subBody)
	if out.Status != 503 || !strings.Contains(string(out.Body), "needs to be reconnected") || !strings.Contains(string(out.Body), "sub/claude-x") {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if out.Header.Get("Retry-After") != "" {
		t.Fatal("a dead account has no retry time")
	}
	if row := r.last(); row.ErrorClass != "expired" || row.Status != 503 || len(r.up.Calls()) != 0 {
		t.Fatalf("%+v, %d calls", row, len(r.up.Calls()))
	}
}

func TestAMarkedAccountAnswersTheSameWithNoRefresher(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	c := r.login("dead@x.test", "AT-d", "RT-d")
	if ok, err := r.st.Connections().MarkExpired(context.Background(), c.ID, "RT-d", oauth.ReconnectMessage, r.p.d.Now()); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	out, _ := r.do("/v1/chat/completions", subBody)
	if out.Status != 503 || !strings.Contains(string(out.Body), "needs to be reconnected") || r.last().ErrorClass != "expired" {
		t.Fatalf("%d %s %+v", out.Status, out.Body, r.last())
	}
	if len(r.up.Calls()) != 0 {
		t.Fatal("an expired account was called")
	}
}

func TestADeadAccountWithAnotherProvidersErrorKeepsThatError(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 500, Body: `{"error":{"message":"boom"}}`})
	r.withRefresher(&fakeRefresher{})
	live := r.login("live@x.test", "AT-l", "RT-l")
	dead := r.login("dead@x.test", "AT-d", "RT-d")
	r.p.d.Refresher = &onlyFor{inner: &fakeRefresher{st: r.st}, dead: dead.ID}
	out, _ := r.do("/v1/chat/completions", subBody)
	if out.Status != 500 || r.last().ErrorClass == "expired" {
		t.Fatalf("a real upstream error was hidden behind the reconnect text: %d %s", out.Status, out.Body)
	}
	_ = live
}

func TestAnErrorBodyOfTheFirstCallIsNeverSentToTheClientOnSuccess(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 401, Body: `{"error":{"message":"SECRET-FIRST"}}`}, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	r.withRefresher(&fakeRefresher{renewTo: "AT-2"})
	r.login("a@x.test", "AT-1", "RT-1")
	out, _ := r.do("/v1/chat/completions", subBody)
	if strings.Contains(string(out.Body), "SECRET") || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hey" {
		t.Fatalf("%s", out.Body)
	}
}

func TestOnlyAnAuthRefusalRenewsATokenAnyOtherErrorDoesNot(t *testing.T) {
	for _, status := range []int{400, 404, 429, 500, 503} {
		r := newRig(t, tests.Reply{Status: status, Body: `{"error":{"message":"x"}}`})
		f := r.withRefresher(&fakeRefresher{renewTo: "AT-2"})
		r.login("a@x.test", "AT-1", "RT-1")
		r.do("/v1/chat/completions", subBody)
		if f.renew != 0 {
			t.Errorf("status %d renewed the token", status)
		}
	}
}
