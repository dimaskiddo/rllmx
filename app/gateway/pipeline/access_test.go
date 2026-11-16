package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/access/identity"
	"rllmx/app/llm/stream"
	"rllmx/tests"
)

// runAs sends one request as the caller given, which is how a key's list
// reaches the pipeline.
func (r *rig) runAs(caller identity.Caller, path, body string) Output {
	r.t.Helper()
	rec := httptest.NewRecorder()
	open := func() (stream.Client, error) { return stream.NewSSE(rec, stream.Options{}) }
	return r.p.Handle(context.Background(), Input{
		Path: path, Body: []byte(body), Caller: caller, RequestID: "rid-1",
	}, open)
}

const bumpedModel = `{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`

func TestAListedModelIsServedToARestrictedKey(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	key := identity.Caller{ID: "k", Name: "listed", Restricted: true, Allow: []string{"forced/fm"}}
	out := r.runAs(key, "/v1/chat/completions", bumpedModel)
	if out.Status != 200 || strings.Contains(string(out.Body), "not allowed") {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if len(r.up.Calls()) != 1 {
		t.Fatalf("%d upstream calls", len(r.up.Calls()))
	}
}

func TestAnUnlistedModelIsRefusedBeforeAnyCall(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	key := identity.Caller{ID: "k", Name: "other", Restricted: true, Allow: []string{"openai/gpt-4o"}}
	out := r.runAs(key, "/v1/chat/completions", bumpedModel)
	if out.Status != http.StatusForbidden {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if got := gjson.GetBytes(out.Body, "error.message").String(); !strings.Contains(got, "forced/fm") {
		t.Fatalf("the refusal does not name the requested model: %q", got)
	}
	if strings.Contains(string(out.Body), "openai/gpt-4o") {
		t.Fatal("the refusal named the key's own list")
	}
	if len(r.up.Calls()) != 0 {
		t.Fatalf("the request reached the provider %d times", len(r.up.Calls()))
	}
}

func TestARestrictedKeyWithNoListCallsNothing(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	for _, allow := range [][]string{nil, {}} {
		key := identity.Caller{ID: "k", Restricted: true, Allow: allow}
		if out := r.runAs(key, "/v1/chat/completions", bumpedModel); out.Status != http.StatusForbidden {
			t.Fatalf("allow=%v: %d", allow, out.Status)
		}
	}
	if len(r.up.Calls()) != 0 {
		t.Fatal("an empty list reached the provider")
	}
}

func TestAnUnrestrictedKeyIsNotChecked(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	key := identity.Caller{ID: "k", Name: "open"}
	if out := r.runAs(key, "/v1/chat/completions", bumpedModel); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestARestrictedKeyCannotReadACachedAnswerForAModelItMayNotCall(t *testing.T) {
	// The first request, by an unrestricted key, warms the cache for forced/fm.
	// The second may not call it, and must be refused rather than served from
	// the cache: the check runs before the cache lookup.
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	in := r.use(bumpedModel)
	in.Caller = identity.Caller{ID: "key-1", Name: "open"}
	if first, _ := r.send(in); first.Status != 200 || first.Header.Get(CacheHeader) != "miss" {
		t.Fatalf("the warm-up request: %d %v", first.Status, first.Header)
	}
	calls := len(r.up.Calls())

	second := in
	second.Caller = identity.Caller{ID: "key-2", Name: "limited", Restricted: true, Allow: []string{"openai/gpt-4o"}}
	out, _ := r.send(second)
	if out.Status != http.StatusForbidden || out.Header.Get(CacheHeader) != "" {
		t.Fatalf("a cached answer was served to a key that may not call it: %d %v", out.Status, out.Header)
	}
	if len(r.up.Calls()) != calls {
		t.Fatal("the refused request reached the provider")
	}
}
