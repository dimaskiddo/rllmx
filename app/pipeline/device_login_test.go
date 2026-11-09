package pipeline

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/oauth"
	"rllmx/app/repository"
	"rllmx/tests"
)

func chatFor(provider string) string {
	return `{"model":"` + provider + `/m1","messages":[{"role":"user","content":"hi"}]}`
}

func (r *rig) device(provider string, fields map[string]string, psd string) repository.Connection {
	r.t.Helper()
	f := map[string]string{"testStatus": `"active"`}
	for k, v := range fields {
		f[k] = `"` + v + `"`
	}
	c, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: provider, AuthType: "oauth", Email: "a@x.test", Fields: f, PSD: psd,
	}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

func TestAKimiRequestCarriesItsStaticHeadersAndTheAccountsDeviceID(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.device("kim", map[string]string{"accessToken": "KIMI-AT"}, `{"deviceId":"dev-7"}`)
	if out, _ := r.do("/v1/chat/completions", chatFor("kim")); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	if auth(call) != "Bearer KIMI-AT" || call.Header.Get("X-Msh-Platform") != "rllmx" || call.Header.Get("X-Msh-Device-Id") != "dev-7" {
		t.Fatalf("%v", call.Header)
	}
}

func TestTwoKimiAccountsSendTheirOwnDeviceIDs(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 429, Body: `{"error":{"message":"busy"}}`}, tests.Reply{ContentType: "application/json", Body: okJSON})
	if _, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "kim", AuthType: "oauth", Email: "a@x.test", PSD: `{"deviceId":"dev-A"}`,
		Fields: map[string]string{"accessToken": `"A"`, "testStatus": `"active"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "kim", AuthType: "oauth", Email: "b@x.test", PSD: `{"deviceId":"dev-B"}`,
		Fields: map[string]string{"accessToken": `"B"`, "testStatus": `"active"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	r.do("/v1/chat/completions", chatFor("kim"))
	calls := r.up.Calls()
	if len(calls) != 2 || calls[0].Header.Get("X-Msh-Device-Id") == "" || calls[0].Header.Get("X-Msh-Device-Id") == calls[1].Header.Get("X-Msh-Device-Id") {
		t.Fatalf("%v / %v", calls[0].Header, calls[len(calls)-1].Header)
	}
}

func TestAnAccountWithNoDeviceIDSendsNoDeviceIDHeader(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.device("kim", map[string]string{"accessToken": "KIMI-AT"}, "")
	r.do("/v1/chat/completions", chatFor("kim"))
	if _, has := r.up.Calls()[0].Header["X-Msh-Device-Id"]; has {
		t.Fatalf("%v", r.up.Calls()[0].Header)
	}
}

func TestAMuseLoginSendsItsVersionHeaderAndAKeyOnTheSameProviderDoesNot(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON}, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.device("mus", map[string]string{"accessToken": "LLM|KEY"}, "")
	r.do("/v1/chat/completions", chatFor("mus"))
	if got := r.up.Calls()[0].Header.Get("x-api-version"); got != "1.0.0" || auth(r.up.Calls()[0]) != "Bearer LLM|KEY" {
		t.Fatalf("login: %q %q", got, auth(r.up.Calls()[0]))
	}
	r2 := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r2.accountConn("mus", "sk-1", "")
	r2.do("/v1/chat/completions", chatFor("mus"))
	if got := r2.up.Calls()[0].Header.Get("x-api-version"); got != "" {
		t.Fatalf("a key row sent %q", got)
	}
}

func TestANousRequestCarriesTheAgentKeyNotTheAccessToken(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.device("kim", map[string]string{"accessToken": "ACCESS", "apiKey": "AGENT-KEY"}, "")
	r.do("/v1/chat/completions", chatFor("kim"))
	if got := auth(r.up.Calls()[0]); got != "Bearer AGENT-KEY" {
		t.Fatalf("auth %q", got)
	}
}

func TestAKiloRequestCarriesTheOrganisationOfTheAccount(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.device("kc", map[string]string{"accessToken": "KILO"}, `{"orgId":"org_9"}`)
	r.do("/v1/chat/completions", chatFor("kc"))
	if got := r.up.Calls()[0].Header.Get("X-Kilocode-OrganizationID"); got != "org_9" {
		t.Fatalf("%q", got)
	}
	r2 := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r2.device("kc", map[string]string{"accessToken": "KILO"}, "")
	r2.do("/v1/chat/completions", chatFor("kc"))
	if _, has := r2.up.Calls()[0].Header["X-Kilocode-Organizationid"]; has {
		t.Fatal("an organisation header with no organisation")
	}
}

func TestADeviceLoginWithNoRefreshTokenOnA401IsAnAccountFailureNotACrash(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 401, Body: `{"error":{"message":"expired"}}`})
	f := r.withRefresher(&fakeRefresher{renewErr: oauth.ErrNoRefresh})
	r.device("kc", map[string]string{"accessToken": "KILO"}, "")
	out, _ := r.do("/v1/chat/completions", chatFor("kc"))
	if out.Status != 502 || len(r.up.Calls()) != 1 || f.renew != 1 {
		t.Fatalf("%d, %d calls, %d renewals", out.Status, len(r.up.Calls()), f.renew)
	}
}

func TestAClientIsServedOnTheLaneOfItsOwnFormat(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, wantPath, wantAuth, wantHeader string
	}{
		{"openai", "/v1/chat/completions", `{"model":"lane/m1","messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions", "Bearer TOK", ""},
		{"responses", "/v1/responses", `{"model":"lane/m1","input":"hi"}`, "/v1/responses", "", "resp"},
		{"claude", "/v1/messages", `{"model":"lane/m1","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
			r.device("lane", map[string]string{"accessToken": "TOK"}, "")
			r.run(tc.path, tc.body)
			calls := r.up.Calls()
			if len(calls) != 1 || calls[0].Path != tc.wantPath {
				t.Fatalf("%v", calls)
			}
			c := calls[0]
			if auth(c) != tc.wantAuth {
				t.Errorf("Authorization %q, want %q", auth(c), tc.wantAuth)
			}
			if tc.name == "claude" && c.Header.Get("x-api-key") != "TOK" {
				t.Errorf("the Claude lane sends the token raw: %v", c.Header)
			}
			if tc.name == "responses" && c.Header.Get("x-lane-key") != "TOK" {
				t.Errorf("the lane's own scheme was not used: %v", c.Header)
			}
			if tc.name != "claude" && c.Header.Get("x-api-key") != "" {
				t.Errorf("a lane sent the provider's x-api-key: %v", c.Header)
			}
			if c.Header.Get("X-Lane") != tc.wantHeader || c.Header.Get("X-Login") != "1" {
				t.Errorf("headers %v", c.Header)
			}
		})
	}
}

func TestOnlyTheClaudeLaneGetsTheQueryString(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON}, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.device("lane", map[string]string{"accessToken": "TOK"}, "")
	r.run("/v1/messages", `{"model":"lane/m1","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	r.run("/v1/chat/completions", `{"model":"lane/m1","messages":[{"role":"user","content":"hi"}]}`)
	calls := r.up.Calls()
	if len(calls) != 2 || calls[0].Query != "beta=true" || calls[1].Query != "" {
		t.Fatalf("%+v", calls)
	}
}

// A client format with no lane is translated to the provider's own format and
// sent to its default address with its default scheme.
func TestAClientWithNoLaneIsTranslatedToTheDefaultAddress(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	r.device("lane", map[string]string{"accessToken": "TOK"}, "")
	out, _ := r.run("/v1beta/models/lane/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	calls := r.up.Calls()
	if len(calls) != 1 || calls[0].Path != "/v1/messages" || calls[0].Query != "beta=true" {
		t.Fatalf("%d %s %+v", out.Status, out.Body, calls)
	}
	if calls[0].Header.Get("x-api-key") != "TOK" || auth(calls[0]) != "" {
		t.Fatalf("the default lane sends the token raw: %v", calls[0].Header)
	}
	if calls[0].Header.Get("Anthropic-Version") != "2023-06-01" || !gjson.GetBytes(calls[0].Body, "max_tokens").Exists() {
		t.Fatalf("not a Claude request: %v %s", calls[0].Header, calls[0].Body)
	}
}

func TestAKeyOnALaneProviderIsPresentedAsTheLaneSays(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.accountConn("lane", "sk-1", "")
	r.run("/v1/chat/completions", `{"model":"lane/m1","messages":[{"role":"user","content":"hi"}]}`)
	c := r.up.Calls()[0]
	if c.Path != "/v1/chat/completions" || auth(c) != "Bearer sk-1" || c.Header.Get("x-api-key") != "" {
		t.Fatalf("%s %v", c.Path, c.Header)
	}
}
