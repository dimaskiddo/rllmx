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
		{"unknown redirect", `"redirect":"remote","exchange":"json","refresh":{"encoding":"json","leadSeconds":300}`, "oauth.redirect"},
		{"the public redirect", `"redirect":"public","exchange":"json","refresh":{"encoding":"json","leadSeconds":300}`, ""},
		{"a public redirect with a port", `"redirect":"public","fixedPort":1455,"exchange":"json","refresh":{"encoding":"json","leadSeconds":300}`, "fixed redirect"},
		{"an unknown redirect host", `"redirect":"fixed","fixedPort":1455,"callbackPath":"/cb","redirectHost":"example.com","exchange":"json","refresh":{"encoding":"json","leadSeconds":300}`, "redirectHost"},
		{"a loopback redirect host", `"redirect":"fixed","fixedPort":1455,"callbackPath":"/cb","redirectHost":"127.0.0.1","exchange":"json","refresh":{"encoding":"json","leadSeconds":300}`, ""},
		{"a redirect host on a local redirect", local + `,"redirectHost":"127.0.0.1"`, "fixed redirect"},
		{"an application from the account", local + `,"appFromAccount":true`, ""},
		{"an application on a fixed redirect", `"redirect":"fixed","fixedPort":1455,"callbackPath":"/cb","exchange":"json","appFromAccount":true,"refresh":{"encoding":"json","leadSeconds":300}`, "appFromAccount"},
		{"a refresh address that is not https", `"redirect":"local","exchange":"json","refresh":{"encoding":"json","leadSeconds":1,"url":"http://a.example/r"}`, "https"},
		{"a placeholder the code-pkce flow does not fill", local + `,"params":[{"name":"a","value":"{redirect}"}]`, "placeholder"},
		{"the nonce placeholder", local + `,"params":[{"name":"nonce","value":"{nonce}"}]`, ""},
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

func deviceFile(fields string) string {
	return entry("p", `,"oauth":{"flow":"device","deviceUrl":"https://a.example/device","tokenUrl":"https://a.example/token","scope":"s"`+fields+`}`)
}

func TestADeviceLoginIsValidatedAtLoad(t *testing.T) {
	const mint = `,"mint":"copilot","endpoints":{"copilotToken":"https://a.example/copilot"}`
	cases := []struct {
		name, fields, reason string
	}{
		{"ok, no refresh block", ``, ""},
		{"ok, with a hook and a mint", `,"hook":"github"` + mint, ""},
		{"ok, with a refresh block", `,"refresh":{"encoding":"form","leadSeconds":60}`, ""},
		{"a redirect belongs to the other flow", `,"redirect":"local"`, "device login"},
		{"an exchange belongs to the other flow", `,"exchange":"json"`, "device login"},
		{"an authorize address belongs to the other flow", `,"authorizeUrl":"https://a.example/a"`, "device login"},
		{"a device login may send extra start parameters", `,"params":[{"name":"referrer","value":"x"}]`, ""},
		{"a device parameter the login sets itself", `,"params":[{"name":"client_id","value":"x"}]`, "parameter name"},
		{"a refresh block with a bad encoding", `,"refresh":{"encoding":"xml","leadSeconds":1}`, "oauth.refresh.encoding"},
		{"unknown mint", `,"mint":"cookie"`, "oauth.mint"},
		{"a mint with no endpoint", `,"mint":"copilot"`, "copilotToken endpoint"},
		{"an endpoint over plain http", `,"mint":"copilot","endpoints":{"copilotToken":"http://a.example/c"}`, "https"},
		{"an endpoint with a bad name", `,"endpoints":{"a b":"https://a.example/c"}`, "endpoints"},
		{"too many endpoints", `,"endpoints":{` + strings.TrimSuffix(strings.Repeat(`"e1":"https://a.example/c",`, 1), ",") + `,"e2":"https://a.example/c","e3":"https://a.example/c","e4":"https://a.example/c","e5":"https://a.example/c","e6":"https://a.example/c","e7":"https://a.example/c","e8":"https://a.example/c","e9":"https://a.example/c"}`, "too many"},
		{"a credential header", `,"headers":{"Authorization":"x"}`, "oauth header"},
	}
	for _, tc := range cases {
		_, err := Load(fstest.MapFS{"data/p.json": file(deviceFile(tc.fields))}, "data")
		switch {
		case tc.reason == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.reason != "" && (err == nil || !strings.Contains(err.Error(), tc.reason)):
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.reason)
		}
	}
	// A redirect login keeps its own rules.
	for name, fields := range map[string]string{
		"a device address on a redirect login": `"redirect":"local","exchange":"json","refresh":{"encoding":"json","leadSeconds":1},"deviceUrl":"https://a.example/d"`,
		"a device flow with no device address": ``,
	} {
		body := loginFile(fields)
		if name == "a device flow with no device address" {
			body = strings.Replace(deviceFile(``), `"deviceUrl":"https://a.example/device",`, "", 1)
		}
		if _, err := Load(fstest.MapFS{"data/p.json": file(body)}, "data"); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestTheGitHubLoginDeclaresItsDeviceFlow(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	gh, _ := reg.ByID("github")
	o := gh.OAuth
	if o == nil || o.Flow != FlowDevice || o.DeviceURL != "https://github.com/login/device/code" || o.TokenURL != "https://github.com/login/oauth/access_token" ||
		o.Scope != "read:user" || o.Hook != "github" || o.Mint != MintCopilot || o.Endpoints[EndpointCopilotToken] == "" || o.Endpoints[EndpointUser] == "" ||
		o.Refresh != (Refresh{}) || gh.Executor != ExecutorGitHub {
		t.Fatalf("%+v executor %s", o, gh.Executor)
	}
	if gh.Transport.ResponsesURL == "" || gh.Transport.MessagesURL == "" {
		t.Fatalf("%+v", gh.Transport)
	}
	for _, h := range []string{"copilot-integration-id", "editor-version", "editor-plugin-version", "user-agent", "openai-intent", "x-github-api-version", "x-vscode-user-agent-library-version", "X-Initiator"} {
		if o.Headers[h] == "" {
			t.Errorf("header %s is missing", h)
		}
	}
}

// The pipeline sends every Claude-named model to the Claude address by its
// name, as the reference does; the data names a format only for the one model
// the reference learns to need /responses.
func TestTheGitHubModelsChooseTheirWireFormat(t *testing.T) {
	reg, _ := Default()
	want := map[string]string{
		"gpt-5.3-codex": FormatResponses,
		"gpt-5.4":       "", "gemini-3.7-flash": "", "kimi-k3": "", "mai-code-1.1-flash": "",
		"claude-sonnet-5": "", "claude-opus-4.8-fast": "", "claude-haiku-4.5": "",
	}
	for id, format := range want {
		m, ok := reg.Model("github", id)
		if !ok || m.TargetFormat != format {
			t.Errorf("%s: %+v %v, want %q", id, m, ok, format)
		}
	}
	e, _ := reg.ByID("github")
	if e.Transport.MessagesURL == "" || e.Transport.ResponsesURL == "" {
		t.Errorf("the Claude and Responses addresses are data: %+v", e.Transport)
	}
	for _, m := range e.Models {
		if strings.HasPrefix(m.ID, "claude-") && m.TargetFormat != "" {
			t.Errorf("%s names a format the pipeline already decides by its name", m.ID)
		}
	}
}

func TestAFormatAddressStaysOnTheBaseHost(t *testing.T) {
	const base = `"transport":{"baseUrl":"https://h.example/chat/completions"`
	for name, tc := range map[string]struct {
		extra string
		ok    bool
	}{
		"same host":       {`,"responsesUrl":"https://h.example/responses","messagesUrl":"https://h.example/v1/messages"`, true},
		"other host":      {`,"responsesUrl":"https://evil.example/responses"`, false},
		"other scheme":    {`,"messagesUrl":"http://h.example/m"`, false},
		"a query":         {`,"messagesUrl":"https://h.example/m?x=1"`, false},
		"a user":          {`,"messagesUrl":"https://u:p@h.example/m"`, false},
		"a fragment":      {`,"messagesUrl":"https://h.example/m#f"`, false},
		"host in capital": {`,"responsesUrl":"https://H.EXAMPLE/responses"`, true},
	} {
		body := `{"id":"p","category":"apikey",` + base + tc.extra + `},"models":[]}`
		_, err := Load(fstest.MapFS{"data/p.json": file(body)}, "data")
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", name, err)
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
