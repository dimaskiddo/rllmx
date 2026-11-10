package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/oauth"
	"rllmx/app/repository"
	"rllmx/tests"
)

type fakeProjects struct {
	mu      sync.Mutex
	project string
	err     error
	calls   int
	access  string
}

func (f *fakeProjects) Project(_ context.Context, _ repository.Connection, access string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.access = access
	return f.project, f.err
}

const geminiAnswer = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}}`

func (r *rig) google(project string) repository.Connection {
	r.t.Helper()
	psd := ""
	f := map[string]string{"accessToken": "GTOK", "testStatus": "active"}
	if project != "" {
		f["projectId"] = project
	}
	return r.device("gcl", f, psd)
}

func TestAGeminiCLIRequestCarriesTheAccountsProjectAndTheEnvelope(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: geminiAnswer})
	r.google("stored-proj-1")
	out, _ := r.do("/v1/chat/completions", `{"model":"gcl/gm-1","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	g := gjson.ParseBytes(call.Body)
	if g.Get("project").String() != "stored-proj-1" || g.Get("model").String() != "gm-1" || g.Get("userAgent").String() != "gemini-cli" || !g.Get("request.contents").IsArray() {
		t.Fatalf("%s", call.Body)
	}
	if auth(call) != "Bearer GTOK" || call.Path != "/v1internal:generateContent" || call.Header.Get("User-Agent") != "GeminiCLI/0.34.0/gm-1 (router; router; terminal)" {
		t.Fatalf("%s %v", call.Path, call.Header)
	}
}

func TestAnAccountWithoutAProjectIsResolvedThenServed(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: geminiAnswer})
	r.google("")
	projects := &fakeProjects{project: "found-proj-1"}
	r.p.d.Projects = projects
	out, _ := r.do("/v1/chat/completions", `{"model":"gcl/gm-1","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 || projects.calls != 1 || projects.access != "GTOK" {
		t.Fatalf("%d %s, %d lookups with %q", out.Status, out.Body, projects.calls, projects.access)
	}
	if got := gjson.GetBytes(r.up.Calls()[0].Body, "project").String(); got != "found-proj-1" {
		t.Fatalf("project %q", got)
	}
}

func TestAnAccountWhoseProjectCannotBeFoundSendsNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		projects Projects
		status   int
	}{
		"no resolver":     {nil, 401},
		"not found":       {&fakeProjects{err: &oauth.Error{Class: oauth.ClassRejected}}, 401},
		"google down":     {&fakeProjects{err: &oauth.Error{Class: oauth.ClassTransport}}, 502},
		"unexpected fail": {&fakeProjects{err: errors.New("boom")}, 401},
	} {
		r := newRig(t, tests.Reply{ContentType: "application/json", Body: geminiAnswer})
		r.google("")
		r.p.d.Projects = tc.projects
		out, _ := r.do("/v1/chat/completions", `{"model":"gcl/gm-1","messages":[{"role":"user","content":"hi"}]}`)
		if len(r.up.Calls()) != 0 || out.Status == 200 {
			t.Errorf("%s: %d %s, %d calls", name, out.Status, out.Body, len(r.up.Calls()))
		}
	}
}

func TestAStoredProjectNeedsNoLookup(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: geminiAnswer})
	r.google("stored-proj-1")
	projects := &fakeProjects{project: "other-proj-9"}
	r.p.d.Projects = projects
	r.do("/v1/chat/completions", `{"model":"gcl/gm-1","messages":[{"role":"user","content":"hi"}]}`)
	if projects.calls != 0 {
		t.Fatalf("%d lookups for an account with a project", projects.calls)
	}
}
