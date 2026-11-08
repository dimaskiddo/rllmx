package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/repository"
	"rllmx/tests"
)

func TestADataFileDrivesTheAddressTheHeadersAndTheModelOnTheWire(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("wire", "k1")
	out, _ := r.do("/v1/chat/completions", `{"model":"wire/short","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	if call.Query != "v=1" {
		t.Errorf("query %q", call.Query)
	}
	if call.Header.Get("X-Title") != "rllmx" || auth(call) != "Bearer k1" {
		t.Errorf("headers %v", call.Header)
	}
	if got := gjson.GetBytes(call.Body, "model").String(); got != "vendor/long-name" {
		t.Errorf("the wire model is %q, want the listed upstream id", got)
	}
	if r.last().Model != "short" {
		t.Errorf("the usage row must keep the client's model, got %q", r.last().Model)
	}
}

func TestAnUnlistedModelGoesOutAsTheClientNamedIt(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("wire", "k1")
	r.do("/v1/chat/completions", `{"model":"wire/other","messages":[{"role":"user","content":"hi"}]}`)
	if got := gjson.GetBytes(r.up.Calls()[0].Body, "model").String(); got != "other" {
		t.Fatalf("model %q", got)
	}
}

func TestTheReasoningPlaceholderIsAddedForAProviderThatAsksForIt(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("wire", "k1")
	r.do("/v1/chat/completions", `{"model":"wire/short","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`)
	if got := gjson.GetBytes(r.up.Calls()[0].Body, "messages.1.reasoning_content").String(); got != " " {
		t.Fatalf("body %s", r.up.Calls()[0].Body)
	}
}

func TestAProviderRetryRuleRetries429InPlaceThroughThePipeline(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 429, Body: `{"error":{"message":"slow down"}}`}, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("wire", "k1")
	out, _ := r.do("/v1/chat/completions", `{"model":"wire/short","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 || len(r.up.Calls()) != 2 {
		t.Fatalf("status %d after %d calls: a 429 should be retried once in place", out.Status, len(r.up.Calls()))
	}
}

func (r *rig) accountConn(provider, key, psd string) repository.Connection {
	r.t.Helper()
	c, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: provider, AuthType: "apikey", Name: key, PSD: psd, Fields: map[string]string{"apiKey": `"` + key + `"`},
	}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

func TestTheAccountIDInTheAddressComesFromTheConnection(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.accountConn("acct", "k1", `{"accountId":"acc42"}`)
	out, _ := r.do("/v1/chat/completions", `{"model":"acct/am","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if got := r.up.Calls()[0].Path; got != "/accounts/acc42/chat/completions" {
		t.Fatalf("path %s", got)
	}
}

func TestAConnectionWithoutAValidAccountIDSendsNothingAndIsNotBlamedOnTheClient(t *testing.T) {
	for name, psd := range map[string]string{"missing": "", "path": `{"accountId":"a/../b"}`} {
		r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
		r.accountConn("acct", "k1", psd)
		out, _ := r.do("/v1/chat/completions", `{"model":"acct/am","messages":[{"role":"user","content":"hi"}]}`)
		if len(r.up.Calls()) != 0 {
			t.Errorf("%s: %d calls were sent", name, len(r.up.Calls()))
		}
		if out.Status == 200 || strings.Contains(string(out.Body), "a/../b") {
			t.Errorf("%s: %d %s", name, out.Status, out.Body)
		}
	}
}
