package pipeline

import (
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/tests"
)

func TestAnAnswerOverTheCapFailsOverAndOneAtTheCapIsServed(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	r.p.d.MaxAnswer = int64(len(okJSON))
	out, _ := r.do("/v1/chat/completions", chatBody)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("an answer exactly at the cap: %d %s", out.Status, out.Body)
	}

	over := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	over.conn(nodeID, "k1")
	over.p.d.MaxAnswer = int64(len(okJSON)) - 1
	got, _ := over.do("/v1/chat/completions", chatBody)
	if got.Status != 502 {
		t.Fatalf("an answer one byte over the cap gave %d %s, want 502", got.Status, got.Body)
	}
	if gjson.GetBytes(got.Body, "choices").Exists() {
		t.Fatalf("a cut answer was served: %s", got.Body)
	}
}

func TestADefaultCapIsSetWhenNoneIsGiven(t *testing.T) {
	r := newRig(t)
	if r.p.d.MaxAnswer != maxAnswer {
		t.Fatalf("MaxAnswer %d, want %d", r.p.d.MaxAnswer, maxAnswer)
	}
}
