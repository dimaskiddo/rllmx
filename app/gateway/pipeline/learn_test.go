package pipeline

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"rllmx/app/access/identity"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/upstream"
	"rllmx/tests"
)

type recordingDeclarer struct {
	got []string
	err error
}

func (d *recordingDeclarer) Remember(_ context.Context, provider, model string, _ upstream.Declared) (bool, error) {
	d.got = append(d.got, provider+"/"+model)
	return d.err == nil, d.err
}

func imageInput(model string) Input {
	return Input{
		Path: "/v1/chat/completions", RequestID: "rid-learn",
		Body:   []byte(`{"model":"` + model + `","messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`),
		Caller: identity.Caller{ID: "key-1", Name: "ci"},
	}
}

func handleLearn(t *testing.T, reply tests.Reply, in Input, d *recordingDeclarer) Output {
	t.Helper()
	r := newRig(t, reply)
	r.conn("forced", "k1")
	r.p.d.Declarer = d
	return r.p.Handle(context.Background(), in, func() (stream.Client, error) { return nil, errors.New("never") })
}

func TestASuccessWithAnImageTeachesAnUnknownModelToReadOne(t *testing.T) {
	d := &recordingDeclarer{}
	out := handleLearn(t, tests.Reply{ContentType: "application/json", Body: okJSON}, imageInput("forced/fm"), d)
	if out.Status != http.StatusOK {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if len(d.got) != 1 || d.got[0] != "forced/fm" {
		t.Fatalf("learned %v", d.got)
	}
}

func TestOnlyACustomModelCanBeTaughtAndOnlyByASuccess(t *testing.T) {
	for name, tt := range map[string]struct {
		reply tests.Reply
		in    Input
	}{
		"no image":      {tests.Reply{ContentType: "application/json", Body: okJSON}, Input{Path: "/v1/chat/completions", Body: []byte(`{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`), Caller: identity.Caller{ID: "key-1", Name: "ci"}}},
		"a failed call": {tests.Reply{Status: 500, ContentType: "application/json", Body: `{"error":{"message":"boom"}}`}, imageInput("forced/fm")},
		"a known table": {tests.Reply{ContentType: "application/json", Body: okJSON}, imageInput("forced/gpt-4o")},
	} {
		t.Run(name, func(t *testing.T) {
			d := &recordingDeclarer{}
			handleLearn(t, tt.reply, tt.in, d)
			if len(d.got) != 0 {
				t.Fatalf("learned %v", d.got)
			}
		})
	}
}

func TestAStoreThatRefusesTheWriteLeavesTheAnswerAlone(t *testing.T) {
	d := &recordingDeclarer{err: errors.New("down")}
	out := handleLearn(t, tests.Reply{ContentType: "application/json", Body: okJSON}, imageInput("forced/fm"), d)
	if out.Status != http.StatusOK {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if len(d.got) != 1 || d.got[0] != "forced/fm" {
		t.Fatalf("learned %v", d.got)
	}
}
