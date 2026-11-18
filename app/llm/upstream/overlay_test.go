package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const testCapsJSON = `{"providers":{},"models":{"claude-opus-4.8":{"contextWindow":200000,"maxOutput":32000},"doc-model":{"pdf":true}},"patterns":[]}`

// testCaps builds a resolver over one model row and a registry from the given
// entry files.
func testCaps(t *testing.T, files map[string]string) *Capabilities {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(body)}
	}
	reg, err := Load(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCapabilities([]byte(testCapsJSON), []byte(thinkingJSON), []byte(visionJSON), []byte(`{"rules":[]}`), reg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func decodeCatalog(t *testing.T, body string) map[string]devProvider {
	t.Helper()
	cat, err := DecodeCatalog(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

func TestModalitiesAreFiledByGatewayAndVendorStripped(t *testing.T) {
	c := testCaps(t, map[string]string{
		"glm.json":    `{"id":"glm","category":"apikey","display":{"name":"GLM"},"transport":{"baseUrl":"https://x"},"models":[{"id":"glm-4.6v"}]}`,
		"glm-cn.json": `{"id":"glm-cn","category":"apikey","display":{"name":"GLM CN"},"transport":{"baseUrl":"https://y"},"models":[{"id":"glm-5-canary"}]}`,
		"kimi.json":   `{"id":"kimi","category":"apikey","display":{"name":"Kimi"},"transport":{"baseUrl":"https://z"},"models":[{"id":"kimi-k3"}]}`,
	})
	cat := decodeCatalog(t, `{
		"zai":{"models":{"glm-4.6v":{"modalities":{"input":["text","image"]}}}},
		"zhipuai":{"models":{"glm-5-canary":{"modalities":{"input":["text","image","pdf"]}}}},
		"moonshotai":{"models":{"kimi-k3":{"modalities":{"input":["text"]}}}},
		"kilo":{"models":{"kilo-auto/efficient":{"modalities":{"input":["text","image"]}}}}
	}`)
	o := c.BuildOverlay(cat)
	for key, want := range map[string]Modalities{
		"glm:glm-4.6v":         {Vision: true},
		"zai:glm-4.6v":         {Vision: true},
		"glm-cn:glm-5-canary":  {Vision: true, PDF: true},
		"zhipuai:glm-5-canary": {Vision: true, PDF: true},
		"kilo:efficient":       {Vision: true},
	} {
		if got, ok := o.Models[key]; !ok || got != want {
			t.Errorf("%s: %+v present=%v, want %+v", key, got, ok, want)
		}
	}
	// A text-only model files nothing, and no key is the bare normalized id.
	for _, key := range []string{"kimi:kimi-k3", "glm-4.6v", "efficient"} {
		if _, ok := o.Models[key]; ok {
			t.Errorf("%s should not be filed", key)
		}
	}
	// A router mode named "efficient" on another gateway is not resolved.
	if _, ok := o.Modalities("qoder", "efficient"); ok {
		t.Error("a different gateway inherited another's modalities")
	}
}

func TestLimitsAreDeltasAgainstTheTables(t *testing.T) {
	c := testCaps(t, map[string]string{
		"github.json": `{"id":"github","category":"oauth","display":{"name":"GH"},"transport":{"baseUrl":"https://g"},"models":[{"id":"claude-opus-4.8"}]}`,
		"claude.json": `{"id":"claude","category":"oauth","display":{"name":"Claude"},"transport":{"baseUrl":"https://c"},"models":[{"id":"claude-opus-4.8"}]}`,
		"gemini.json": `{"id":"gemini","category":"oauth","display":{"name":"Gemini"},"transport":{"baseUrl":"https://m"},"models":[{"id":"gemini-4-argon"}]}`,
		"openai.json": `{"id":"openai","category":"apikey","display":{"name":"OpenAI"},"transport":{"baseUrl":"https://o"},"models":[{"id":"gpt-4o"}]}`,
	})
	cat := decodeCatalog(t, `{
		"github-copilot":{"models":{"claude-opus-4.8":{"limit":{"context":300000,"output":32000}}}},
		"anthropic":{"models":{"claude-opus-4.8":{"limit":{"context":210000,"output":64000}}}},
		"google":{"models":{"gemini-4-argon":{"limit":{"context":1048576,"output":65536}}}},
		"openai":{"models":{"gpt-4o":{"limit":{"context":220000,"output":70400}}}}
	}`)
	o := c.BuildOverlay(cat)
	if got := o.Providers["github"]["claude-opus-4.8"]; got.ContextWindow != 300000 || got.MaxOutput != 0 {
		t.Errorf("github: %+v", got)
	}
	// Within the tolerance the table keeps its number.
	if got := o.Providers["claude"]["claude-opus-4.8"]; got.ContextWindow != 0 || got.MaxOutput != 64000 {
		t.Errorf("claude: %+v", got)
	}
	// A model no row covers takes the catalog's window; its answer cap is close.
	if got := o.Providers["gemini"]["gemini-4-argon"]; got.ContextWindow != 1048576 || got.MaxOutput != 0 {
		t.Errorf("gemini: %+v", got)
	}
	// Exactly at the tolerance is not a difference.
	if _, ok := o.Providers["openai"]; ok {
		t.Errorf("a boundary difference was written: %+v", o.Providers["openai"])
	}
}

func TestAStatedContextLengthSkipsTheWindowButNotTheOutput(t *testing.T) {
	c := testCaps(t, map[string]string{
		"grok-cli.json": `{"id":"grok-cli","category":"oauth","display":{"name":"Grok"},"transport":{"baseUrl":"https://x"},"models":[{"id":"grok-build","contextLength":500000}]}`,
	})
	c.SetOverlay(nil)
	cat := decodeCatalog(t, `{"grok-cli":{"models":{"grok-build":{"limit":{"context":1000000,"output":100000}}}}}`)
	o := c.BuildOverlay(cat)
	got := o.Providers["grok-cli"]["grok-build"]
	if got.ContextWindow != 0 {
		t.Errorf("the gateway's own window was overwritten: %+v", got)
	}
	if got.MaxOutput != 100000 {
		t.Errorf("the answer cap was not taken: %+v", got)
	}
}

func TestTheOverlayOnlyTurnsCapabilitiesOn(t *testing.T) {
	c := testCaps(t, map[string]string{
		"p.json": `{"id":"p","category":"apikey","display":{"name":"P"},"transport":{"baseUrl":"https://x"},"models":[{"id":"plain-chat"}]}`,
	})
	before := c.For("p", "plain-chat")
	if before.Vision || before.PDF {
		t.Fatalf("the tables already claim a modality: %+v", before)
	}
	c.SetOverlay(&Overlay{Version: OverlayVersion, Models: map[string]Modalities{
		"p:plain-chat": {Vision: true, AudioInput: true},
	}, Providers: map[string]map[string]Limits{
		"p": {"plain-chat": {ContextWindow: 1000000, MaxOutput: 128000}},
	}})
	got := c.For("p", "plain-chat")
	if !got.Vision || !got.AudioInput || got.PDF || got.VideoInput {
		t.Errorf("modalities: %+v", got)
	}
	if got.ContextWindow != 1000000 || got.MaxOutput != 128000 {
		t.Errorf("limits: %+v", got)
	}
	// A catalog entry may not switch a table value off.
	c.SetOverlay(&Overlay{Version: OverlayVersion, Models: map[string]Modalities{
		"p:doc-model": {Vision: true},
	}})
	if got := c.For("p", "doc-model"); !got.PDF || !got.Vision {
		t.Errorf("a table capability was overwritten: %+v", got)
	}
	c.SetOverlay(nil)
	if back := c.For("p", "plain-chat"); back.ContextWindow != before.ContextWindow || back.MaxOutput != before.MaxOutput || back.Vision != before.Vision {
		t.Errorf("detaching did not restore the tables: %+v", back)
	}
}

func TestAProviderRowIsNeverChangedByTheCatalog(t *testing.T) {
	fsys := fstest.MapFS{"p.json": &fstest.MapFile{Data: []byte(`{"id":"p","category":"apikey","display":{"name":"P"},"transport":{"baseUrl":"https://x"},"models":[{"id":"row-model"}]}`)}}
	reg, err := Load(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCapabilities(
		[]byte(`{"providers":{"p":{"row-model":{"contextWindow":50000,"maxOutput":1000}}},"models":{},"patterns":[]}`),
		[]byte(thinkingJSON), []byte(visionJSON), []byte(`{"rules":[]}`), reg)
	if err != nil {
		t.Fatal(err)
	}
	c.SetOverlay(&Overlay{Version: OverlayVersion,
		Models:    map[string]Modalities{"p:row-model": {Vision: true}},
		Providers: map[string]map[string]Limits{"p": {"row-model": {ContextWindow: 1000000}}},
	})
	got := c.For("p", "row-model")
	if got.ContextWindow != 50000 || got.Vision {
		t.Errorf("a provider row was refined: %+v", got)
	}
}

func TestTheBuilderNeverSeesTheInstalledCatalog(t *testing.T) {
	c := testCaps(t, map[string]string{
		"github.json": `{"id":"github","category":"oauth","display":{"name":"GH"},"transport":{"baseUrl":"https://g"},"models":[{"id":"claude-opus-4.8"}]}`,
	})
	cat := decodeCatalog(t, `{"github-copilot":{"models":{"claude-opus-4.8":{"limit":{"context":300000,"output":32000}}}}}`)
	first := c.BuildOverlay(cat)
	c.SetOverlay(first)
	second := c.BuildOverlay(cat)
	if len(second.Providers["github"]) != 1 {
		t.Fatalf("the second build lost the delta: %+v", second.Providers)
	}
	if len(second.Providers["github"]) != len(first.Providers["github"]) {
		t.Fatal("a rebuild changed what the catalog holds")
	}
}

func TestOverlayLookupsFallBackToTheBaseID(t *testing.T) {
	o := &Overlay{Version: OverlayVersion,
		Models:    map[string]Modalities{"p:m-1": {Vision: true}},
		Providers: map[string]map[string]Limits{"p": {"m-1": {ContextWindow: 1000}}},
	}
	if _, ok := o.Modalities("p", "vendor/model-1:free"); ok {
		t.Error("a different model answered")
	}
	if m, ok := o.Modalities("p", "vendor/M-1:free"); !ok || !m.Vision {
		t.Error("the base id did not resolve")
	}
	if l, ok := o.Limits("p", "vendor/m-1:free"); !ok || l.ContextWindow != 1000 {
		t.Error("the limit fallback did not resolve")
	}
}

func TestParseOverlayRefusesAnotherLayout(t *testing.T) {
	if _, err := ParseOverlay([]byte(`{"v":2,"models":{},"providers":{}}`)); !errors.Is(err, ErrOverlayVersion) {
		t.Fatalf("version 2: %v", err)
	}
	for name, body := range map[string]string{
		"unknown field":        `{"v":3,"models":{},"providers":{},"extra":1}`,
		"two values":           `{"v":3,"models":{},"providers":{}} {}`,
		"empty limit":          `{"v":3,"models":{},"providers":{"p":{"m":{}}}}`,
		"negative":             `{"v":3,"models":{},"providers":{"p":{"m":{"contextWindow":-1}}}}`,
		"over the cap":         `{"v":3,"models":{},"providers":{"p":{"m":{"contextWindow":200000000}}}}`,
		"key with no provider": `{"v":3,"models":{"m":{"vision":true}},"providers":{}}`,
		"not an object":        `[]`,
	} {
		if _, err := ParseOverlay([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTheCatalogFileRoundTrips(t *testing.T) {
	dir := t.TempDir()
	o := &Overlay{Version: OverlayVersion, ETag: `"abc"`, SyncedAt: 1700000000000,
		Models:    map[string]Modalities{"github:claude-opus-4.8": {Vision: true}},
		Providers: map[string]map[string]Limits{"github": {"claude-opus-4.8": {ContextWindow: 300000}}},
	}
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, OverlayFile)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	back, got, err := ReadOverlayFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(raw) || back.ETag != `"abc"` || back.SyncedAt != 1700000000000 {
		t.Fatalf("round trip: %+v", back)
	}
	sum := back.Summary()
	if sum.Models != 1 || sum.Providers != 1 {
		t.Errorf("summary: %+v", sum)
	}
}

func TestDecodeCatalogRefusesATruncatedObject(t *testing.T) {
	for name, body := range map[string]string{
		"array":      `[]`,
		"empty":      `{}`,
		"no id":      `{"":{"models":{}}}`,
		"truncated":  `{"p":{"models":{`,
		"not an obj": `"x"`,
	} {
		if _, err := DecodeCatalog(strings.NewReader(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// stubDo answers one request with a canned answer.
type stubDo struct {
	status int
	body   string
	header http.Header
	calls  []*http.Request
}

func (s *stubDo) RoundTrip(r *http.Request) (*http.Response, error) {
	s.calls = append(s.calls, r)
	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	h := s.header
	if h == nil {
		h = http.Header{}
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: h, Body: io.NopCloser(strings.NewReader(s.body))}, nil
}

func TestFetchOverlaySendsTheStoredETag(t *testing.T) {
	c := caps(t)
	stub := &stubDo{status: http.StatusNotModified}
	o, raw, res, err := c.FetchOverlay(context.Background(), &http.Client{Transport: stub}, `"v1"`)
	if err != nil {
		t.Fatal(err)
	}
	if o != nil || raw != nil || res.Status != "unchanged" || res.ETag != `"v1"` {
		t.Fatalf("304: %+v %v", res, o)
	}
	if got := stub.calls[0].Header.Get("If-None-Match"); got != `"v1"` {
		t.Errorf("If-None-Match: %q", got)
	}
}

func TestFetchOverlayBuildsAndEncodes(t *testing.T) {
	c := testCaps(t, map[string]string{
		"github.json": `{"id":"github","category":"oauth","display":{"name":"GH"},"transport":{"baseUrl":"https://g"},"models":[{"id":"claude-opus-4.8"}]}`,
	})
	stub := &stubDo{header: http.Header{"Etag": {`"v2"`}},
		body: `{"github-copilot":{"models":{"claude-opus-4.8":{"modalities":{"input":["image"]},"limit":{"context":300000,"output":32000}}}}}`}
	o, raw, res, err := c.FetchOverlay(context.Background(), &http.Client{Transport: stub}, "")
	if err != nil {
		t.Fatal(err)
	}
	// The gateway is filed under its local id and its upstream id.
	if o.ETag != `"v2"` || o.SyncedAt == 0 || len(o.Models) != 2 || len(o.Providers) != 1 {
		t.Fatalf("overlay: %+v", o)
	}
	if res.Status != "updated" || res.Bytes != len(raw) || res.Models != 2 || res.Providers != 1 {
		t.Fatalf("result: %+v against %d bytes", res, len(raw))
	}
	back, err := ParseOverlay(raw)
	if err != nil {
		t.Fatalf("the encoded file does not parse: %v", err)
	}
	if back.Providers["github"]["claude-opus-4.8"].ContextWindow != 300000 {
		t.Errorf("encoded: %+v", back.Providers)
	}
}

func TestFetchOverlayReportsAClassOnly(t *testing.T) {
	c := caps(t)
	stub := &stubDo{status: http.StatusBadGateway, body: "secret account text"}
	_, _, _, err := c.FetchOverlay(context.Background(), &http.Client{Transport: stub}, "")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error: %v", err)
	}
}

func FuzzParseOverlay(f *testing.F) {
	f.Add([]byte(`{"v":3,"models":{},"providers":{}}`))
	f.Add([]byte(`{"v":3,"models":{"p:m":{"vision":true}},"providers":{"p":{"m":{"maxOutput":1}}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		o, err := ParseOverlay(data)
		if err == nil && o.Version != OverlayVersion {
			t.Fatalf("accepted version %d", o.Version)
		}
	})
}

func FuzzDecodeCatalog(f *testing.F) {
	f.Add([]byte(`{"p":{"models":{"m":{"modalities":{"input":["image"]}}}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeCatalog(strings.NewReader(string(data)))
	})
}

func TestEveryCatalogAliasNamesABuiltProvider(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for local, dev := range overlayAliases {
		if _, ok := reg.ByID(local); !ok {
			t.Errorf("alias %q names no built provider", local)
		}
		if dev == "" || dev == local {
			t.Errorf("alias %q maps to %q", local, dev)
		}
	}
}

func TestAStatedContextLengthIsReadAndBounded(t *testing.T) {
	c := testCaps(t, map[string]string{
		"p.json": `{"id":"p","category":"apikey","display":{"name":"P"},"transport":{"baseUrl":"https://x"},"models":[{"id":"m","contextLength":500000}]}`,
	})
	reg := c.registry
	m, ok := reg.Model("p", "m")
	if !ok || m.ContextLength != 500000 {
		t.Fatalf("context length: %+v", m)
	}
	fsys := fstest.MapFS{"bad.json": &fstest.MapFile{Data: []byte(`{"id":"bad","category":"apikey","display":{"name":"B"},"transport":{"baseUrl":"https://x"},"models":[{"id":"m","contextLength":-1}]}`)}}
	if _, err := Load(fsys, "."); err == nil {
		t.Fatal("a negative context length was accepted")
	}
}
