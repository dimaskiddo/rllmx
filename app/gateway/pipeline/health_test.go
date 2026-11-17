package pipeline

import (
	"testing"

	"rllmx/tests"
)

func TestAServedAnswerRecordsASuccess(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	r.do("/v1/chat/completions", chatBody)
	recs := r.hlt.all()
	if len(recs) != 1 {
		t.Fatalf("%d outcomes recorded, want 1: %+v", len(recs), recs)
	}
	if !recs[0].ok || recs[0].provider != nodeID {
		t.Fatalf("%+v", recs[0])
	}
}

func TestOnlyAFailureThatMovesTheRequestOnCountsAgainstTheProvider(t *testing.T) {
	r := newRig(t,
		tests.Reply{Status: 500, ContentType: "application/json", Body: `{"error":{"message":"down"}}`},
		tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	r.conn(nodeID, "k2")
	if out, _ := r.do("/v1/chat/completions", chatBody); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	recs := r.hlt.all()
	if len(recs) != 2 || recs[0].ok || !recs[1].ok {
		t.Fatalf("%+v", recs)
	}
}

func TestARequestScopedAnswerRecordsNothing(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 400, ContentType: "application/json", Body: `{"error":{"message":"bad"}}`})
	r.conn(nodeID, "k1")
	out, _ := r.do("/v1/chat/completions", chatBody)
	if out.Status != 400 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if recs := r.hlt.all(); len(recs) != 0 {
		t.Fatalf("a client mistake counted against the provider: %+v", recs)
	}
}

func TestAnEmptyAnswerRecordsNothing(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: `{"choices":[]}`})
	r.conn(nodeID, "k1")
	r.do("/v1/chat/completions", chatBody)
	if recs := r.hlt.all(); len(recs) != 0 {
		t.Fatalf("an empty answer counted against the provider: %+v", recs)
	}
}
