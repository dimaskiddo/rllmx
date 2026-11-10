package pipeline

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/store/repository"
	"rllmx/tests"
)

const (
	workosJWT = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig"
	userMsg   = `{"model":"cln/cl-1","messages":[{"role":"user","content":"hi"}]}`
)

func TestAWorkOSTokenGoesOutMarkedAndAPlanKeyGoesOutAsItIs(t *testing.T) {
	for name, tc := range map[string]struct{ key, want string }{
		"a WorkOS token": {workosJWT, "Bearer workos:" + workosJWT},
		"a plan key":     {"clp_abc123", "Bearer clp_abc123"},
	} {
		r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
		r.conn("cln", tc.key)
		if out, _ := r.do("/v1/chat/completions", userMsg); out.Status != 200 {
			t.Fatalf("%s: %d %s", name, out.Status, out.Body)
		}
		if got := auth(r.up.Calls()[0]); got != tc.want {
			t.Errorf("%s: Authorization %q, want %q", name, got, tc.want)
		}
	}
}

func TestAClineEnvelopeIsUnwrappedForANonStreamAnswer(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: `{"success":true,"data":` + okJSON + `}`})
	r.conn("cln", "clp_1")
	out, _ := r.do("/v1/chat/completions", userMsg)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() == "" || gjson.GetBytes(out.Body, "success").Exists() {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestAnErrorEnvelopeOrOtherBodyIsNotUnwrapped(t *testing.T) {
	for name, body := range map[string]string{
		"a failed envelope": `{"success":false,"data":` + okJSON + `}`,
		"data an array":     `{"success":true,"data":[1]}`,
		"no success":        `{"data":` + okJSON + `}`,
		"success as text":   `{"success":"true","data":` + okJSON + `}`,
	} {
		if got := string(unwrapEnvelope([]byte(body))); got != body {
			t.Errorf("%s was rewritten: %s", name, got)
		}
	}
	if got := string(unwrapEnvelope([]byte("not json"))); got != "not json" {
		t.Fatalf("%s", got)
	}
}

func TestAnEnvelopeIsLeftAloneForAProviderThatDidNotAskForTheQuirk(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: `{"success":true,"data":` + okJSON + `}`})
	r.conn("wire", "k1")
	out, _ := r.do("/v1/chat/completions", `{"model":"wire/short","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != http.StatusBadGateway {
		t.Fatalf("an envelope was unwrapped for a provider that did not ask: %d %s", out.Status, out.Body)
	}
}

func FuzzUnwrapEnvelope(f *testing.F) {
	for _, s := range []string{`{}`, `{"success":true,"data":{}}`, `{"success":true,"data":null}`, `[`, `{"success":true,"data":{"success":true,"data":{}}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		out := unwrapEnvelope(raw)
		if len(out) > len(raw) {
			t.Fatalf("grew %q into %q", raw, out)
		}
		if string(out) != string(raw) && !strings.HasPrefix(strings.TrimSpace(string(out)), "{") {
			t.Fatalf("unwrapped %q into a non-object %q", raw, out)
		}
	})
}

func TestAGitLabAccountOnAPlainHTTPServerSendsNothing(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	if _, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "glb", AuthType: "apikey", Name: "g", Fields: map[string]string{"apiKey": `"glpat-1"`}, PSD: `{"baseUrl":"` + r.up.URL + `"}`,
	}, nil); err != nil {
		t.Fatal(err)
	}
	out, _ := r.run("/v1/chat/completions", `{"model":"glb/gm1","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status == 200 || len(r.up.Calls()) != 0 {
		t.Fatalf("a plain http server address was used: %d, %d calls", out.Status, len(r.up.Calls()))
	}
}
