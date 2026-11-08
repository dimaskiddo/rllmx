package upstream

import (
	"strings"
	"testing"
	"testing/fstest"
)

func file(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }

func entry(id, extra string) string {
	return `{"id":"` + id + `","category":"apikey","transport":{"baseUrl":"https://x.example/v1"},"models":[]` + extra + `}`
}

func TestTheEmbeddedDataLoads(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"anthropic", "openai", "codex", "claude", "gemini", "deepseek", "openrouter", "github", "grok-cli"} {
		if _, ok := reg.ByID(id); !ok {
			t.Errorf("seed provider %q is missing", id)
		}
	}
	if len(reg.All()) < 9 {
		t.Fatalf("%d providers", len(reg.All()))
	}
}

func TestAliasesResolveToTheCanonicalID(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for token, want := range map[string]string{
		"cc": "claude", "cx": "codex", "gh": "github", "ds": "deepseek", "gcli": "grok-cli", "gb": "grok-cli", "grok-build": "grok-cli",
		"claude": "claude", "el": "elevenlabs", "xmtp": "xiaomi-tokenplan",
	} {
		if got, ok := reg.Resolve(token); !ok || got != want {
			t.Errorf("Resolve(%q) = %q %v, want %q", token, got, ok, want)
		}
	}
	if _, ok := reg.Resolve("nothing-like-this"); ok {
		t.Fatal("an unknown name resolved")
	}
}

func TestReservedCoversIDsAliasesAndLocalNames(t *testing.T) {
	reg, _ := Default()
	for _, name := range []string{"openai", "cx", "polly", "xiaomi-tokenplan"} {
		if !reg.Reserved(name) {
			t.Errorf("%q is not reserved", name)
		}
	}
	if reg.Reserved("my-own-node") {
		t.Fatal("a free name is reserved")
	}
}

func TestModelsAreACopy(t *testing.T) {
	reg, _ := Default()
	list := reg.Models("openai")
	if len(list) == 0 {
		t.Fatalf("models %d", len(list))
	}
	list[0].ID = "changed"
	if reg.Models("openai")[0].ID == "changed" {
		t.Fatal("the caller can edit the registry")
	}
	if reg.Models("unknown") != nil {
		t.Fatal("an unknown provider has models")
	}
}

func TestTheLoaderRefusesBrokenData(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"duplicate alias": {map[string]string{
			"data/a.json": entry("a", `,"alias":"same"`),
			"data/b.json": entry("b", `,"alias":"same"`),
		}, "already used"},
		"alias is another id": {map[string]string{
			"data/a.json": entry("a", ""),
			"data/b.json": entry("b", `,"alias":"a"`),
		}, "already used"},
		"local name taken": {map[string]string{
			"data/a.json": entry("a", `,"alias":"polly"`),
		}, "reserved"},
		"file name differs from id": {map[string]string{"data/x.json": entry("y", "")}, "file name"},
		"unknown field":             {map[string]string{"data/a.json": entry("a", `,"colour":"red"`)}, "unknown field"},
		"unknown category":          {map[string]string{"data/a.json": strings.Replace(entry("a", ""), "apikey", "magic", 1)}, "category"},
		"unknown format":            {map[string]string{"data/a.json": strings.Replace(entry("a", ""), `"baseUrl"`, `"format":"smoke","baseUrl"`, 1)}, "format"},
		"unknown executor":          {map[string]string{"data/a.json": entry("a", `,"executor":"mystery"`)}, "executor"},
		"duplicate model":           {map[string]string{"data/a.json": strings.Replace(entry("a", ""), `"models":[]`, `"models":[{"id":"m"},{"id":"m"}]`, 1)}, "duplicate model"},
		"bad id":                    {map[string]string{"data/A_B.json": entry("A_B", "")}, "id must be"},
		"two values":                {map[string]string{"data/a.json": entry("a", "") + entry("a", "")}, "more than one"},
		"missing base url":          {map[string]string{"data/a.json": `{"id":"a","category":"apikey","transport":{},"models":[]}`}, "baseUrl"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for path, body := range c.files {
				fsys[path] = file(body)
			}
			_, err := Load(fsys, "data")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
		})
	}
}

func TestTheLoaderErrorNamesNoFileContent(t *testing.T) {
	secret := "sk-very-secret-value"
	_, err := Load(fstest.MapFS{"data/a.json": file(`{"id":"a","category":"apikey","` + secret + `":1}`)}, "data")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error repeats file content: %v", err)
	}
}

func TestTheLoaderNeedsAtLeastOneFile(t *testing.T) {
	if _, err := Load(fstest.MapFS{}, "data"); err != ErrNoProviders {
		t.Fatalf("err %v", err)
	}
}

func TestAnOversizedFileIsRefused(t *testing.T) {
	big := entry("a", "") + strings.Repeat(" ", maxFileBytes)
	if _, err := Load(fstest.MapFS{"data/a.json": file(big)}, "data"); err == nil || !strings.Contains(err.Error(), "over") {
		t.Fatalf("err %v", err)
	}
}

func TestDefaultsAreFilledIn(t *testing.T) {
	reg, err := Load(fstest.MapFS{"data/a.json": file(entry("a", ""))}, "data")
	if err != nil {
		t.Fatal(err)
	}
	e, _ := reg.ByID("a")
	if e.Executor != ExecutorDefault || e.Transport.Format != FormatOpenAI || e.Display.Name != "a" {
		t.Fatalf("%+v", e)
	}
}

func withAuth(auth string) map[string]string {
	return map[string]string{"data/a.json": `{"id":"a","category":"apikey","transport":{"baseUrl":"https://x.example/v1/chat/completions"},"models":[],"auth":` + auth + `}`}
}

func TestTheLoaderRefusesABadAuthBlock(t *testing.T) {
	cases := map[string]struct{ auth, want string }{
		"unknown scheme":          {`{"scheme":"magic","header":"X-Key"}`, "scheme"},
		"no header":               {`{"scheme":"bearer"}`, "header"},
		"bad header name":         {`{"scheme":"raw","header":"X Key"}`, "header"},
		"reserved header":         {`{"scheme":"raw","header":"Cookie"}`, "reserved"},
		"credential in headers":   {`{"scheme":"raw","header":"X-Key","headers":{"Authorization":"x"}}`, "credential or framing"},
		"cookie in headers":       {`{"scheme":"raw","header":"X-Key","headers":{"cookie":"x"}}`, "credential or framing"},
		"key header repeated":     {`{"scheme":"raw","header":"X-Key","headers":{"x-key":"x"}}`, "credential or framing"},
		"control in value":        {`{"scheme":"raw","header":"X-Key","headers":{"X-A":"a\u0085b"}}`, "value"},
		"bad static name":         {`{"scheme":"raw","header":"X-Key","headers":{"X_A":"a"}}`, "name"},
		"bad status":              {`{"scheme":"raw","header":"X-Key","badKey":[200]}`, "400-599"},
		"url on another host":     {`{"scheme":"bearer","header":"Authorization","validateUrl":"https://evil.example/models"}`, "another host"},
		"url with credentials":    {`{"scheme":"bearer","header":"Authorization","validateUrl":"https://u:p@x.example/models"}`, "plain http"},
		"url with a query":        {`{"scheme":"bearer","header":"Authorization","validateUrl":"https://x.example/models?key=1"}`, "plain http"},
		"url with another scheme": {`{"scheme":"bearer","header":"Authorization","validateUrl":"ftp://x.example/models"}`, "plain http"},
		"unknown field":           {`{"scheme":"bearer","header":"Authorization","secret":"x"}`, "unknown field"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for p, body := range withAuth(c.auth) {
				fsys[p] = file(body)
			}
			_, err := Load(fsys, "data")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
		})
	}
}

func TestAnAuthorizationBearerHeaderIsAllowed(t *testing.T) {
	fsys := fstest.MapFS{}
	for p, body := range withAuth(`{"scheme":"bearer","header":"Authorization"}`) {
		fsys[p] = file(body)
	}
	if _, err := Load(fsys, "data"); err != nil {
		t.Fatal(err)
	}
}

func TestProbeURLAndBadKeyStatuses(t *testing.T) {
	for base, want := range map[string]string{
		"https://x.example/v1/chat/completions": "https://x.example/v1/models",
		"https://x.example/v1/messages":         "https://x.example/v1/models",
		"https://x.example/v1/responses":        "https://x.example/v1/models",
		"https://x.example/v1":                  "https://x.example/v1/models",
		"https://x.example/v1/":                 "https://x.example/v1/models",
		"https://x.example":                     "https://x.example/models",
	} {
		if got := (Entry{Transport: Transport{BaseURL: base}}).ProbeURL(); got != want {
			t.Errorf("%s: %q, want %q", base, got, want)
		}
	}
	custom := Entry{Transport: Transport{BaseURL: "https://x.example/v1"}, Auth: &Auth{ValidateURL: "https://x.example/ping"}}
	if custom.ProbeURL() != "https://x.example/ping" {
		t.Fatal(custom.ProbeURL())
	}
	def := &Auth{}
	if !def.BadKeyStatus(401) || !def.BadKeyStatus(403) || def.BadKeyStatus(400) || def.BadKeyStatus(500) {
		t.Fatal("the default bad-key set is 401 and 403")
	}
	xai := &Auth{BadKey: []int{400}}
	if !xai.BadKeyStatus(400) || xai.BadKeyStatus(401) {
		t.Fatal("a listed set replaces the default")
	}
}

func TestTheSeedProvidersHaveTheAuthTheirKindNeeds(t *testing.T) {
	reg, _ := Default()
	for _, id := range []string{"anthropic", "openai", "deepseek", "openrouter", "gemini"} {
		e, _ := reg.ByID(id)
		if e.Auth == nil || e.ProbeURL() == "" {
			t.Errorf("%s cannot be checked", id)
		}
	}
	for _, id := range []string{"claude", "codex", "github", "grok-cli"} {
		if e, _ := reg.ByID(id); e.Auth != nil {
			t.Errorf("%s is an OAuth provider and must not carry a key check", id)
		}
	}
}
