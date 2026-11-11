package pipeline

import (
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/tests"
)

// imageChat asks for a picture, which the Cursor agent path cannot take.
const imageChat = `{"model":"cur","messages":[{"role":"user","content":[{"type":"text","text":"what is this"},
	{"type":"image_url","image_url":{"url":"https://x.example/a.png"}}]}]}`

func TestACursorRequestWithAnImageIsRefusedInTheClientsWords(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/connect+proto", Body: ""})
	// The provider row is the real Cursor data with the address pointed at the
	// fake, so the codec, the executor and the refusal are the ones that ship.
	r.conn("cur", "token-value")
	out, _ := r.run("/v1/chat/completions", imageChat)
	if out.Status != 400 {
		t.Fatalf("status = %d, want 400 (%s)", out.Status, out.Body)
	}
	if msg := gjson.GetBytes(out.Body, "error.message").String(); msg != "This model does not accept images" {
		t.Fatalf("message = %q", msg)
	}
	if n := len(r.up.Calls()); n != 0 {
		t.Fatalf("%d calls reached the upstream, want none", n)
	}
}
