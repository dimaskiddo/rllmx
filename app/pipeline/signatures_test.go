package pipeline

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/cache"
	"rllmx/app/identity"
	"rllmx/app/stream"
	"rllmx/tests"
)

func sigCache() Loader { return cache.New(tests.NewCache(), cache.WithoutShared()) }

func TestASignatureTheClientDroppedComesBack(t *testing.T) {
	c := sigCache()
	first := newSignatureStore(c, "key-1", "gemini-2.5-pro")
	first.Put("call_1", "SIG-A")
	next := newSignatureStore(c, "key-1", "gemini-2.5-flash")
	if got, ok := next.Get("call_1"); !ok || got != "SIG-A" {
		t.Fatalf("got %q %v: the same family on the next turn must get it back", got, ok)
	}
}

func TestAnotherKeyNeverSeesTheSignature(t *testing.T) {
	c := sigCache()
	newSignatureStore(c, "key-1", "gemini-2.5-pro").Put("call_1", "SIG-A")
	if got, ok := newSignatureStore(c, "key-2", "gemini-2.5-pro").Get("call_1"); ok {
		t.Fatalf("another key read %q", got)
	}
}

func TestAnotherModelFamilyNeverGetsTheSignature(t *testing.T) {
	c := sigCache()
	newSignatureStore(c, "key-1", "gemini-2.5-pro").Put("call_1", "SIG-A")
	if got, ok := newSignatureStore(c, "key-1", "gemini-3-pro").Get("call_1"); ok {
		t.Fatalf("a model of another family was offered %q", got)
	}
	if got, ok := newSignatureStore(c, "key-1", "claude-sonnet-4").Get("call_1"); ok {
		t.Fatalf("a model of another vendor was offered %q", got)
	}
}

func TestTheBareCallIDIsNeverTheCacheKey(t *testing.T) {
	c := sigCache()
	newSignatureStore(c, "key-1", "gemini-2.5-pro").Put("call_1", "SIG-A")
	if _, ok := c.Get(cache.ToolCalls, "call_1"); ok {
		t.Fatal("the entry is stored under the bare call id")
	}
}

func TestAnOversizedOrEmptySignatureIsNotStored(t *testing.T) {
	c := sigCache()
	s := newSignatureStore(c, "key-1", "gemini-2.5-pro")
	s.Put("big", strings.Repeat("x", maxSignatureBytes+1))
	s.Put("empty", "")
	s.Put("", "SIG")
	for _, id := range []string{"big", "empty", ""} {
		if got, ok := s.Get(id); ok {
			t.Errorf("%q stored %q", id, got)
		}
	}
	s.Put("edge", strings.Repeat("x", maxSignatureBytes))
	if _, ok := s.Get("edge"); !ok {
		t.Error("a signature exactly at the cap was refused")
	}
}

func TestNoCacheOrNoKeyMeansNoStore(t *testing.T) {
	if newSignatureStore(nil, "key-1", "gemini-2.5-pro") != nil {
		t.Error("a store without a cache")
	}
	if newSignatureStore(sigCache(), "", "gemini-2.5-pro") != nil {
		t.Error("a store without an API key")
	}
	var none *signatureStore
	none.Put("c", "s")
	if _, ok := none.Get("c"); ok {
		t.Error("a nil store returned a signature")
	}
}

func TestModelFamily(t *testing.T) {
	for in, want := range map[string]string{
		"gemini-2.5-pro": "gemini-2.5", "gemini-2.5-flash": "gemini-2.5", "models/gemini-3-pro": "gemini-3",
		"Gemini-2.0-Flash": "gemini-2.0", "claude-sonnet-4": "claude-sonnet-4", "gpt": "gpt", "": "",
	} {
		if got := modelFamily(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

const geminiToolAnswer = `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_a","name":"lookup","args":{"q":"x"}},"thoughtSignature":"SIG-FROM-GEMINI"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`

const secondTurn = `{"model":"gem/g-vision","messages":[` +
	`{"role":"user","content":"find x"},` +
	`{"role":"assistant","content":null,"tool_calls":[{"id":"call_a","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},` +
	`{"role":"tool","tool_call_id":"call_a","content":"found"}]}`

func TestADroppedGeminiSignatureIsRestoredOnTheNextTurn(t *testing.T) {
	r := newRig(t,
		tests.Reply{ContentType: "application/json", Body: geminiToolAnswer},
		tests.Reply{ContentType: "application/json", Body: geminiToolAnswer},
	)
	r.p.d.Signatures = sigCache()
	r.conn("gem", "k1")

	first, _ := r.do("/v1/chat/completions", `{"model":"gem/g-vision","messages":[{"role":"user","content":"find x"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`)
	if first.Status != 200 {
		t.Fatalf("turn one: %d %s", first.Status, first.Body)
	}
	if got := gjson.GetBytes(first.Body, "choices.0.message.tool_calls.0.id").String(); got != "call_a" {
		t.Fatalf("the call id changed: %q in %s", got, first.Body)
	}

	second, _ := r.do("/v1/chat/completions", secondTurn)
	if second.Status != 200 {
		t.Fatalf("turn two: %d %s", second.Status, second.Body)
	}
	calls := r.up.Calls()
	sent := calls[len(calls)-1].Body
	if got := gjson.GetBytes(sent, "contents.1.parts.0.thoughtSignature").String(); got != "SIG-FROM-GEMINI" {
		t.Fatalf("the signature the client dropped was not sent back: %q in %s", got, sent)
	}
}

func TestNoSignatureCacheMeansNoRestoredSignature(t *testing.T) {
	r := newRig(t,
		tests.Reply{ContentType: "application/json", Body: geminiToolAnswer},
		tests.Reply{ContentType: "application/json", Body: geminiToolAnswer},
	)
	r.conn("gem", "k1")
	r.do("/v1/chat/completions", `{"model":"gem/g-vision","messages":[{"role":"user","content":"find x"}]}`)
	r.do("/v1/chat/completions", secondTurn)
	calls := r.up.Calls()
	if gjson.GetBytes(calls[len(calls)-1].Body, "contents.1.parts.0.thoughtSignature").String() == "SIG-FROM-GEMINI" {
		t.Fatal("a signature came back with no store behind it")
	}
}

func TestAnotherKeyDoesNotGetTheSignatureBack(t *testing.T) {
	r := newRig(t,
		tests.Reply{ContentType: "application/json", Body: geminiToolAnswer},
		tests.Reply{ContentType: "application/json", Body: geminiToolAnswer},
	)
	r.p.d.Signatures = sigCache()
	r.conn("gem", "k1")
	r.do("/v1/chat/completions", `{"model":"gem/g-vision","messages":[{"role":"user","content":"find x"}]}`)

	rec := httptest.NewRecorder()
	open := func() (stream.Client, error) { return stream.NewSSE(rec, stream.Options{}) }
	in := Input{Path: "/v1/chat/completions", Body: []byte(secondTurn), Caller: identity.Caller{ID: "key-2", Name: "other"}, RequestID: "rid-2"}
	if out := r.p.Handle(context.Background(), in, open); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	calls := r.up.Calls()
	if gjson.GetBytes(calls[len(calls)-1].Body, "contents.1.parts.0.thoughtSignature").String() == "SIG-FROM-GEMINI" {
		t.Fatal("another API key was handed a signature stored for the first")
	}
}
