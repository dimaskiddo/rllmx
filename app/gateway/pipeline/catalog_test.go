package pipeline

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/catalog"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

// verdict is a scripted catalogue: one provider, one answer.
type verdict struct {
	answer catalog.Verdict
	calls  int
}

func (v *verdict) Known(context.Context, string, repository.Connection, string) catalog.Verdict {
	v.calls++
	return v.answer
}

func TestAModelTheProvidersCatalogLacksIsRefusedBeforeAnySend(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	conn := r.conn(nodeID, "k1")
	v := &verdict{answer: catalog.Unknown}
	r.p.d.Catalog = v
	out, _ := r.run("/v1/chat/completions", chatBody)
	if out.Status != 400 || gjson.GetBytes(out.Body, "error.message").String() != "Model not found" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if n := len(r.up.Calls()); n != 0 {
		t.Fatalf("%d calls reached the upstream, want none", n)
	}
	// The refusal belongs to the request, so the account is not locked.
	fresh, ok, err := r.st.Connections().Get(context.Background(), conn.ID)
	if err != nil || !ok {
		t.Fatalf("get: %v %v", err, ok)
	}
	if _, locked := fresh.Doc().GetString("modelLock___all"); locked {
		t.Fatal("the refusal locked the account")
	}
}

func TestAKnownOrUndeterminedModelIsSent(t *testing.T) {
	for name, answer := range map[string]catalog.Verdict{"known": catalog.Known, "undetermined": catalog.Undetermined} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
			r.conn(nodeID, "k1")
			r.p.d.Catalog = &verdict{answer: answer}
			out, _ := r.run("/v1/chat/completions", chatBody)
			if out.Status != 200 || len(r.up.Calls()) != 1 {
				t.Fatalf("%d after %d calls", out.Status, len(r.up.Calls()))
			}
		})
	}
}
