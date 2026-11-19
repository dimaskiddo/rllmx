package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rllmx/app/gateway/saver"
	"rllmx/app/llm/translate"
	"rllmx/app/store/document"
	"rllmx/tests"
)

// settingsRow serves one stored settings JSON for a test.
type settingsRow struct{ raw string }

func (s settingsRow) Effective(context.Context) (document.Doc, error) {
	return document.ParseDoc(s.raw)
}

func saverCall(row string, header http.Header) *call {
	p := &Pipeline{}
	if row != "" {
		p.d.Settings = settingsRow{raw: row}
	}
	return &call{
		p:  p,
		in: Input{Header: header},
		req: &translate.Request{Model: "m", Messages: []translate.Message{
			{Role: translate.RoleTool, ToolCallID: "c1", Content: translate.TextPart(toolDiff())},
		}},
	}
}

func toolDiff() string {
	var b strings.Builder
	b.WriteString("diff --git a/foo.go b/foo.go\n@@ -1 +1,2 @@\n")
	for i := 0; i < 120; i++ {
		b.WriteString("-old line\n+new line\n")
	}
	return b.String()
}

func TestSaverOptionsReadTheStoredRow(t *testing.T) {
	c := saverCall(`{"rtkEnabled":false,"cavemanEnabled":true,"cavemanLevel":"ultra",`+
		`"cavemanRoutingMode":"sometimes","ponytailEnabled":true,"ponytailLevel":"lite","ponytailRoutingMode":"always",`+
		`"customPromptEnabled":true,"customPrompt":"Answer in French.","headroomEnabled":true,"headroomUrl":`+
		`"http://sidecar:8787","headroomTimeoutMs":100}`, nil)
	o := c.saverOptions(context.Background())
	if o.RTK || !o.Caveman.Enabled || o.Caveman.Level != "ultra" {
		t.Fatalf("%+v", o)
	}
	if o.Caveman.Mode != "always" || o.Ponytail.Mode != "always" {
		t.Fatalf("modes %+v", o)
	}
	if !o.Headroom.Enabled || o.Headroom.URL != "http://sidecar:8787" || o.Headroom.Timeout.Milliseconds() != 100 {
		t.Fatalf("headroom %+v", o.Headroom)
	}
	if !o.Custom.Enabled || o.Custom.Text != "Answer in French." {
		t.Fatalf("custom %+v", o.Custom)
	}
}

func TestSaverOptionsTakeTheConfiguredAddressWhenTheRowHasNone(t *testing.T) {
	c := saverCall(`{"headroomEnabled":true,"headroomUrl":""}`, nil)
	c.p.d.SaverURL = "http://configured:9999"
	if got := c.saverOptions(context.Background()).Headroom.URL; got != "http://configured:9999" {
		t.Fatalf("url %q", got)
	}
	c = saverCall(`{"headroomEnabled":true,"headroomUrl":""}`, nil)
	if got := c.saverOptions(context.Background()).Headroom.URL; got != saverDefaultURL {
		t.Fatalf("url %q", got)
	}
}

func TestTheBypassHeaderTurnsEverySaverOff(t *testing.T) {
	row := `{"rtkEnabled":true,"cavemanEnabled":true,"cavemanLevel":"full","cavemanRoutingMode":"always"}`
	for _, v := range []string{"off", "OFF", " off "} {
		h := http.Header{}
		h.Set(saverBypassHeader, v)
		c := saverCall(row, h)
		if o := c.saverOptions(context.Background()); o.RTK || o.Caveman.Enabled {
			t.Fatalf("%q: %+v", v, o)
		}
		if c.saverKey(context.Background()) != "" {
			t.Fatalf("%q: key not empty", v)
		}
	}
	if !saverCall(row, nil).saverOptions(context.Background()).RTK {
		t.Fatal("RTK is off without the header")
	}
}

func TestNoSettingsMeansNoSaver(t *testing.T) {
	c := saverCall("", nil)
	if o := c.saverOptions(context.Background()); o != (saver.Options{}) {
		t.Fatalf("%+v", o)
	}
	c.p.d.Settings = settingsRow{raw: "not json"}
	if o := c.saverOptions(context.Background()); o != (saver.Options{}) {
		t.Fatalf("%+v", o)
	}
}

func TestTheCacheKeyNamesTheSaverConfiguration(t *testing.T) {
	base := saverCall(`{"rtkEnabled":true}`, nil).saverKey(context.Background())
	if base == "" {
		t.Fatal("no key with RTK on")
	}
	changed := saverCall(`{"rtkEnabled":true,"cavemanEnabled":true,"cavemanLevel":"lite","cavemanRoutingMode":"always"}`, nil).saverKey(context.Background())
	if changed == base {
		t.Fatal("an injected prompt does not change the key")
	}
	same := saverCall(`{"rtkEnabled":true}`, nil).saverKey(context.Background())
	if same != base {
		t.Fatal("the same configuration changed the key")
	}
	promptA := saverCall(`{"customPromptEnabled":true,"customPrompt":"a"}`, nil).saverKey(context.Background())
	promptB := saverCall(`{"customPromptEnabled":true,"customPrompt":"b"}`, nil).saverKey(context.Background())
	if promptA == promptB {
		t.Fatal("the custom prompt is not in the key")
	}
}

func TestApplySaversCompactsThroughTheSettings(t *testing.T) {
	c := saverCall(`{"rtkEnabled":true}`, nil)
	c.p.d.Log = &tests.Log{}
	before := len(c.req.Messages[0].Content[0].Text)
	c.applySavers(context.Background())
	if len(c.req.Messages[0].Content[0].Text) >= before {
		t.Fatal("the tool result was not compacted")
	}
}

func TestApplySaversCallsTheSidecar(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"role":"tool","tool_call_id":"c1","content":"short"}]}`))
	}))
	defer srv.Close()
	c := saverCall(`{"rtkEnabled":false,"headroomEnabled":true,"headroomUrl":"`+srv.URL+`"}`, nil)
	c.p.d.Saver = &saver.Compressor{Client: func(string) *http.Client { return srv.Client() }}
	c.applySavers(context.Background())
	if hits != 1 || c.req.Messages[0].Content[0].Text != "short" {
		t.Fatalf("hits %d, text %.20q", hits, c.req.Messages[0].Content[0].Text)
	}
}
