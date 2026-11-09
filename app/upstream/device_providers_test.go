package upstream

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestTheDeviceLoginsOf110dDeclareWhatTheyNeed(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	get := func(id string) *OAuth {
		e, ok := reg.ByID(id)
		if !ok || e.OAuth == nil || e.OAuth.Flow != FlowDevice {
			t.Fatalf("%s: no device login", id)
		}
		return e.OAuth
	}
	kimi := get("kimi")
	if !kimi.UsesDeviceID() || kimi.CallHeaders["X-Msh-Device-Id"] != DeviceIDToken || kimi.Endpoints[EndpointVerify] == "" ||
		kimi.Refresh.Encoding != EncodingForm || kimi.Refresh.LeadSeconds != 300 || kimi.Account == nil || kimi.Account.From[0] != "deviceId" {
		t.Errorf("kimi: %+v", kimi)
	}
	muse := get("muse")
	if muse.Hook != "muse" || muse.Endpoints[EndpointKey] == "" || muse.Refresh != (Refresh{}) || muse.CallHeaders["x-api-version"] == "" {
		t.Errorf("muse: %+v", muse)
	}
	nous := get("nous")
	if nous.Hook != "nous" || nous.Refresh.TokenHeader != "x-nous-refresh-token" || nous.Refresh.Hook != "nous" || nous.Endpoints[EndpointAgentKey] == "" {
		t.Errorf("nous: %+v", nous)
	}
	kilo := get("kilocode")
	if kilo.Dialect != "kilocode" || !kilo.NoClient || kilo.Hook != "kilocode" || kilo.Account == nil || kilo.Account.Name != "X-Kilocode-OrganizationID" || kilo.Refresh != (Refresh{}) {
		t.Errorf("kilocode: %+v", kilo)
	}
	grok := get("grok-cli")
	if grok.Hook != "grokCli" || len(grok.Params) != 1 || grok.Params[0].Name != "referrer" || grok.Refresh.LeadSeconds != 300 ||
		grok.TokenURL != "https://auth.x.ai/oauth2/token" || grok.Endpoints[EndpointUser] == "" {
		t.Errorf("grok-cli: %+v", grok)
	}
}

func TestTheNewProvidersListTheirModelsAndFormats(t *testing.T) {
	reg, _ := Default()
	want := map[string]int{"kimi": 10, "muse": 5, "nous": 8, "kilocode": 8}
	for id, n := range want {
		e, ok := reg.ByID(id)
		if !ok || len(e.Models) != n {
			t.Errorf("%s: %d models, want %d", id, len(e.Models), n)
		}
	}
	muse, _ := reg.ByID("muse")
	for _, m := range muse.Models {
		if m.TargetFormat != FormatResponses {
			t.Errorf("muse %s goes to %q", m.ID, m.TargetFormat)
		}
	}
	if muse.Transport.ResponsesURL == "" {
		t.Error("muse has no Responses address")
	}
	for alias, id := range map[string]string{"kc": "kilocode", "kmc": "kimi", "kimi-coding": "kimi", "ns": "nous", "muse-code": "muse"} {
		if got, ok := reg.Resolve(alias); !ok || got != id {
			t.Errorf("%s resolves to %q %v", alias, got, ok)
		}
	}
}

func TestEveryLoginDataNameIsInItsTable(t *testing.T) {
	reg, _ := Default()
	hooks, refresh, dialects := HookNames(), RefreshHookNames(), DialectNames()
	for _, e := range reg.All() {
		o := e.OAuth
		if o == nil {
			continue
		}
		if o.Hook != "" && !slices.Contains(hooks, o.Hook) {
			t.Errorf("%s: hook %q", e.ID, o.Hook)
		}
		if o.Refresh.Hook != "" && !slices.Contains(refresh, o.Refresh.Hook) {
			t.Errorf("%s: refresh hook %q", e.ID, o.Refresh.Hook)
		}
		if o.Dialect != "" && !slices.Contains(dialects, o.Dialect) {
			t.Errorf("%s: dialect %q", e.ID, o.Dialect)
		}
	}
}

func TestCallHeadersAreValidatedAtLoad(t *testing.T) {
	cases := []struct {
		name, fields, reason string
	}{
		{"a device id header", `,"callHeaders":{"X-Id":"{deviceId}"}`, ""},
		{"text around the placeholder", `,"callHeaders":{"X-Id":"a-{deviceId}-b"}`, ""},
		{"an unknown placeholder", `,"callHeaders":{"X-Id":"{secret}"}`, "call header"},
		{"a half placeholder", `,"callHeaders":{"X-Id":"{deviceId"}`, "call header"},
		{"a credential header", `,"callHeaders":{"Authorization":"x"}`, "call header"},
		{"a key header", `,"callHeaders":{"x-api-key":"x"}`, "call header"},
		{"a framing header", `,"callHeaders":{"Content-Length":"1"}`, "call header"},
		{"a control byte", `,"callHeaders":{"X-Id":"a\u0000"}`, "call header"},
		{"too many", `,"callHeaders":{"a":"1","b":"1","c":"1","d":"1","e":"1","f":"1","g":"1","h":"1","i":"1"}`, "too many"},
		{"an unknown dialect", `,"dialect":"shell"`, "oauth.dialect"},
		{"the kilocode dialect", `,"dialect":"kilocode","noClient":true`, ""},
		{"an unknown refresh hook", `,"refresh":{"encoding":"form","leadSeconds":1,"hook":"shell"}`, "refresh.hook"},
		{"a refresh token header", `,"refresh":{"encoding":"form","leadSeconds":1,"tokenHeader":"x-rt"}`, ""},
		{"a refresh token header that is a credential", `,"refresh":{"encoding":"form","leadSeconds":1,"tokenHeader":"Authorization"}`, "tokenHeader"},
		{"an unknown hook", `,"hook":"shell"`, "oauth.hook"},
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
}

func TestCallHeadersBelongToTheDeviceFlowOnly(t *testing.T) {
	const local = `"redirect":"local","exchange":"json","refresh":{"encoding":"json","leadSeconds":300}`
	for _, extra := range []string{`,"callHeaders":{"X-Id":"1"}`, `,"dialect":"kilocode"`, `,"noClient":true`} {
		_, err := Load(fstest.MapFS{"data/p.json": file(loginFile(local + extra))}, "data")
		if err == nil || !strings.Contains(err.Error(), "device flow") {
			t.Errorf("%s: %v", extra, err)
		}
	}
}

func TestADialectMayLeaveTheScopeOutAndOtherLoginsMayNot(t *testing.T) {
	bare := func(extra string) string {
		return entry("p", `,"oauth":{"flow":"device","deviceUrl":"https://a.example/d","tokenUrl":"https://a.example/t"`+extra+`}`)
	}
	if _, err := Load(fstest.MapFS{"data/p.json": file(bare(`,"dialect":"kilocode"`))}, "data"); err != nil {
		t.Errorf("a dialect with no scope: %v", err)
	}
	if _, err := Load(fstest.MapFS{"data/p.json": file(bare(``))}, "data"); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Errorf("a plain device login with no scope: %v", err)
	}
}

func TestKimiIsReachedInTheClientsOwnFormat(t *testing.T) {
	reg, _ := Default()
	e, _ := reg.ByID("kimi")
	tr := e.Transport
	if tr.Format != FormatClaude || tr.BaseURL != "https://api.kimi.com/coding/v1/messages" || tr.URLSuffix != "?beta=true" {
		t.Fatalf("default lane: %+v", tr)
	}
	if e.Auth == nil || e.Auth.Scheme != SchemeRaw || e.Auth.Header != "x-api-key" {
		t.Fatalf("the Claude lane sends the token raw: %+v", e.Auth)
	}
	for format, addr := range map[string]string{
		FormatOpenAI:    "https://api.kimi.com/coding/v1/chat/completions",
		FormatResponses: "https://api.kimi.com/coding/v1/responses",
	} {
		l, ok := tr.Lanes[format]
		if !ok || l.BaseURL != addr || l.Auth == nil || l.Auth.Scheme != SchemeBearer || l.Auth.Header != "Authorization" || l.URLSuffix != "" {
			t.Errorf("%s: %+v %v", format, l, ok)
		}
	}
}

func TestLanesAreValidatedAtLoad(t *testing.T) {
	const base = `"baseUrl":"https://h.example/v1/messages","format":"claude"`
	cases := []struct {
		name, lanes, reason string
	}{
		{"a lane on the same host", `{"openai":{"baseUrl":"https://h.example/v1/chat"}}`, ""},
		{"a lane with its own auth and headers", `{"openai":{"baseUrl":"https://h.example/c","auth":{"scheme":"bearer","header":"Authorization"},"headers":{"X-A":"1"},"urlSuffix":"?a=1"}}`, ""},
		{"another host", `{"openai":{"baseUrl":"https://evil.example/c"}}`, "lane must be"},
		{"another scheme", `{"openai":{"baseUrl":"http://h.example/c"}}`, "lane must be"},
		{"a query in the address", `{"openai":{"baseUrl":"https://h.example/c?x=1"}}`, "lane must be"},
		{"credentials in the address", `{"openai":{"baseUrl":"https://u:p@h.example/c"}}`, "lane must be"},
		{"an unknown format", `{"cobol":{"baseUrl":"https://h.example/c"}}`, "unknown format"},
		{"a lane suffix that is not a query", `{"openai":{"baseUrl":"https://h.example/c","urlSuffix":"/x"}}`, "urlSuffix"},
		{"an unknown scheme", `{"openai":{"baseUrl":"https://h.example/c","auth":{"scheme":"magic","header":"X"}}}`, "auth.scheme"},
		{"a header that shadows the lane's credential", `{"openai":{"baseUrl":"https://h.example/c","auth":{"scheme":"bearer","header":"X-K"},"headers":{"X-K":"1"}}}`, "invalid header"},
		{"a framing header", `{"openai":{"baseUrl":"https://h.example/c","headers":{"Content-Length":"1"}}}`, "invalid header"},
	}
	for _, tc := range cases {
		body := `{"id":"p","category":"oauth","transport":{` + base + `,"lanes":` + tc.lanes + `},"models":[]}`
		_, err := Load(fstest.MapFS{"data/p.json": file(body)}, "data")
		switch {
		case tc.reason == "" && err != nil:
			t.Errorf("%s: %v", tc.name, err)
		case tc.reason != "" && (err == nil || !strings.Contains(err.Error(), tc.reason)):
			t.Errorf("%s: error %v, want %q", tc.name, err, tc.reason)
		}
	}
}
