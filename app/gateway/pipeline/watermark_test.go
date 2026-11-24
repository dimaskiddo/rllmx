package pipeline

import (
	"context"
	"strings"
	"testing"

	"rllmx/app/llm/translate"
	"rllmx/tests"
)

// hidAnswer is an OpenAI answer whose text carries a zero-width space, raw in
// one place and escaped in another.
var hidAnswer = `{"id":"x","object":"chat.completion","choices":[{"index":0,` +
	`"message":{"role":"assistant","content":"he\u200bllo wor` + "\u200b" + `ld"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`

func stripCall(row string) *call {
	p := &Pipeline{}
	if row != "" {
		p.d.Settings = settingsRow{raw: row}
	}
	return &call{p: p}
}

func TestTheWatermarkStripIsOffUntilTheSwitchIsOn(t *testing.T) {
	for name, c := range map[string]*call{
		"no reader": stripCall(""),
		"off":       stripCall(`{"watermarkStripEnabled":false}`),
		"empty":     stripCall(`{}`),
		"unreadable": func() *call {
			c := stripCall("")
			c.p.d.Settings = settingsRow{raw: "not json"}
			return c
		}(),
	} {
		if c.stripInvisible(context.Background()) {
			t.Errorf("%s: the strip is on", name)
		}
	}
	if !stripCall(`{"watermarkStripEnabled":true}`).stripInvisible(context.Background()) {
		t.Fatal("the switch does not turn the strip on")
	}
}

func TestABufferedAnswerLosesTheWatermarkOnlyWhenSwitchedOn(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: hidAnswer})
	r.conn(nodeID, "k1")
	out, _ := r.do("/v1/chat/completions", chatBody)
	if got := strings.ReplaceAll(string(out.Body), "\u200b", ""); got == string(out.Body) {
		t.Fatal("the strip ran with the switch off")
	}

	r = newRig(t, tests.Reply{ContentType: "application/json", Body: hidAnswer})
	r.conn(nodeID, "k1")
	r.p.d.Settings = settingsRow{raw: `{"watermarkStripEnabled":true}`}
	out, _ = r.do("/v1/chat/completions", chatBody)
	if strings.ContainsRune(string(out.Body), '\u200b') || strings.Contains(string(out.Body), `\u200b`) {
		t.Fatalf("a mark survived: %s", out.Body)
	}
	if !strings.Contains(string(out.Body), "hello world") {
		t.Fatalf("the answer lost its text: %s", out.Body)
	}
}

type recordClient struct {
	frames []translate.Frame
	raws   [][]byte
}

func (r *recordClient) Frame(f translate.Frame) error {
	r.frames = append(r.frames, f)
	return nil
}

func (r *recordClient) Raw(b []byte) error {
	r.raws = append(r.raws, b)
	return nil
}

func TestTheStripClientCleansFramesAndRawWrites(t *testing.T) {
	inner := &recordClient{}
	s := stripClient{Client: inner}
	if err := s.Frame(translate.Frame{Event: "message", Data: []byte(`{"t":"a\u200bb"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Raw([]byte("x" + string(rune(0xfeff)) + "y")); err != nil {
		t.Fatal(err)
	}
	if got := string(inner.frames[0].Data); got != `{"t":"ab"}` || inner.frames[0].Event != "message" {
		t.Fatalf("frame %q %q", inner.frames[0].Event, got)
	}
	if got := string(inner.raws[0]); got != "xy" {
		t.Fatalf("raw %q", got)
	}
}
