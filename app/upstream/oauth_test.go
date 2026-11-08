package upstream

import (
	"strings"
	"testing"
	"testing/fstest"
)

func loginFile(fields string) string {
	return entry("p", `,"oauth":{"flow":"code-pkce","authorizeUrl":"https://a.example/authorize","tokenUrl":"https://a.example/token","scope":"s",`+fields+`}`)
}

func TestTheSeedLoginsDeclareTheirFlow(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	claude, _ := reg.ByID("claude")
	if o := claude.OAuth; o == nil || o.Redirect != RedirectLocal || o.Exchange != EncodingJSON || !o.EchoState || o.FixedPort != 0 ||
		o.Refresh != (Refresh{Encoding: EncodingJSON, LeadSeconds: 14400}) || len(o.Headers) != 0 ||
		o.URLSuffix != "?beta=true" || !o.Disguise || o.Beta == nil || len(o.Beta.Base) != 9 || len(o.Beta.Heavy) != 2 ||
		o.Session == nil || o.Session.Name != "x-claude-code-session-id" || o.Session.Keyed || o.Identity["User-Agent"] == "" {
		t.Fatalf("claude: %+v", claude.OAuth)
	}
	codex, _ := reg.ByID("codex")
	if o := codex.OAuth; o == nil || o.Redirect != RedirectFixed || o.FixedPort != 1455 || o.CallbackPath != "/auth/callback" || o.Exchange != EncodingForm || o.EchoState || o.Hook != "codex" ||
		o.Refresh != (Refresh{Encoding: EncodingJSON, LeadSeconds: 600, MaxAgeSeconds: 691200}) ||
		o.Disguise || o.Session == nil || !o.Session.Keyed || o.Account == nil || o.Account.Name != "ChatGPT-Account-ID" || len(o.Account.From) != 3 {
		t.Fatalf("codex: %+v", codex.OAuth)
	}
}

func TestALoginBlockIsValidatedAtLoad(t *testing.T) {
	const local = `"redirect":"local","exchange":"json","refresh":{"encoding":"json","leadSeconds":300}`
	cases := []struct {
		name, fields, reason string
	}{
		{"ok", local, ""},
		{"fixed needs a port", `"redirect":"fixed","exchange":"json","refresh":{"encoding":"json","leadSeconds":300}`, "fixedPort"},
		{"unknown redirect", `"redirect":"public","exchange":"json","refresh":{"encoding":"json","leadSeconds":300}`, "oauth.redirect"},
		{"unknown encoding", `"redirect":"local","exchange":"xml","refresh":{"encoding":"json","leadSeconds":300}`, "oauth.exchange"},
		{"unknown hook", local + `,"hook":"shell"`, "oauth.hook"},
		{"own parameter", local + `,"params":[{"name":"state","value":"x"}]`, "parameter name"},
		{"bad parameter name", local + `,"params":[{"name":"A b","value":"x"}]`, "parameter name"},
		{"repeated parameter", local + `,"params":[{"name":"a","value":"1"},{"name":"a","value":"2"}]`, "parameter name"},
		{"control in value", local + `,"params":[{"name":"a","value":"x\u0000"}]`, "control"},
		{"unknown field", local + `,"clientId":"x"`, "unknown field"},
		{"port on a local redirect", local + `,"fixedPort":1455`, "fixed redirect"},
		{"no refresh block", `"redirect":"local","exchange":"json"`, "oauth.refresh.encoding"},
		{"unknown refresh encoding", `"redirect":"local","exchange":"json","refresh":{"encoding":"xml","leadSeconds":1}`, "oauth.refresh.encoding"},
		{"negative lead", `"redirect":"local","exchange":"json","refresh":{"encoding":"json","leadSeconds":-1}`, "timing"},
		{"huge lead", `"redirect":"local","exchange":"json","refresh":{"encoding":"json","leadSeconds":99999999}`, "timing"},
		{"huge age", `"redirect":"local","exchange":"json","refresh":{"encoding":"json","leadSeconds":1,"maxAgeSeconds":999999999}`, "timing"},
		{"credential header", local + `,"headers":{"x-api-key":"k"}`, "oauth header"},
		{"authorization header", local + `,"headers":{"Authorization":"Bearer x"}`, "oauth header"},
		{"bad header name", local + `,"headers":{"a b":"v"}`, "oauth header"},
		{"control in header", local + `,"headers":{"x-a":"v\u0000"}`, "oauth header"},
		{"a static header is fine", local + `,"headers":{"anthropic-beta":"oauth-2025-04-20"}`, ""},
		{"identity with a credential header", local + `,"identity":{"Authorization":"x"}`, "oauth header"},
		{"identity with a control byte", local + `,"identity":{"User-Agent":"a\u0000"}`, "oauth header"},
		{"a header name with an underscore", local + `,"session":{"name":"session_id"}`, ""},
		{"a session header that frames", local + `,"session":{"name":"Content-Length"}`, "session.name"},
		{"url suffix that is not a query", local + `,"urlSuffix":"/x"`, "urlSuffix"},
		{"url suffix with a fragment", local + `,"urlSuffix":"?a#b"`, "urlSuffix"},
		{"beta flag with a comma", local + `,"beta":{"base":["a,b"]}`, "beta flag"},
		{"beta flag with a newline", local + `,"beta":{"base":["a\nb"]}`, "beta flag"},
		{"too many beta flags", local + `,"beta":{"base":[` + strings.TrimSuffix(strings.Repeat(`"f1",`, 33), ",") + `]}`, "beta"},
		{"beta prefix with a space", local + `,"beta":{"base":["a"],"heavyPrefixes":["a b"]}`, "beta prefix"},
		{"account without a source", local + `,"account":{"name":"X-Acct","from":[]}`, "oauth.account"},
		{"account source not a field", local + `,"account":{"name":"X-Acct","from":["a.b"]}`, "field name"},
		{"a good account", local + `,"account":{"name":"X-Acct","from":["accountId"]}`, ""},
	}
	for _, tc := range cases {
		_, err := Load(fstest.MapFS{"data/p.json": file(loginFile(tc.fields))}, "data")
		switch {
		case tc.reason == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.reason != "" && (err == nil || !strings.Contains(err.Error(), tc.reason)):
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.reason)
		}
	}
}

func TestAnOAuthAddressMustBePlainHTTPS(t *testing.T) {
	for _, bad := range []string{"http://a.example/t", "https://u:p@a.example/t", "https://a.example/t?x=1", "https://a.example/t#f", "ftp://a.example", "/relative"} {
		body := strings.Replace(loginFile(`"redirect":"local","exchange":"json","refresh":{"encoding":"json","leadSeconds":300}`), "https://a.example/token", bad, 1)
		if _, err := Load(fstest.MapFS{"data/p.json": file(body)}, "data"); err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
