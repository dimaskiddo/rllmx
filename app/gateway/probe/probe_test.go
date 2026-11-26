package probe

import (
	"context"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/catalog"
	"rllmx/app/gateway/pipeline"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

// kek is a hand-cranked clock for the cache window.
type clock struct{ at time.Time }

func (c *clock) Now() time.Time { return c.at }

type kv struct{}

func (kv) List(context.Context, string) (map[string]string, error)  { return nil, nil }
func (kv) DisabledModels(context.Context, string) ([]string, error) { return nil, nil }

// handler is a pipeline stand-in that records what it was asked and when.
type handler struct {
	mu       sync.Mutex
	calls    []string
	inFlight int
	maxIn    int
	finished int
	// before is finished-at-start, one per call, in call order.
	before []int
	delay  time.Duration
	fail   map[string]bool
}

func (h *handler) Handle(ctx context.Context, in pipeline.Input, _ pipeline.Opener) pipeline.Output {
	model := gjson.GetBytes(in.Body, "model").String()
	h.mu.Lock()
	h.calls = append(h.calls, model)
	h.before = append(h.before, h.finished)
	h.inFlight++
	if h.inFlight > h.maxIn {
		h.maxIn = h.inFlight
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.inFlight--
		h.finished++
		h.mu.Unlock()
	}()
	if h.delay > 0 {
		time.Sleep(h.delay)
	}
	if h.fail[model] {
		return pipeline.Output{Status: 429, Body: []byte(`{"error":{"message":"slow down"}}`)}
	}
	return pipeline.Output{Status: 200, Body: []byte(`{"choices":[{"message":{"content":"hi"}}]}`)}
}

func (h *handler) state() (calls []string, before []int, maxIn int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.calls...), append([]int(nil), h.before...), h.maxIn
}

func newProber(t *testing.T, h Handler, now func() time.Time) *Prober {
	t.Helper()
	reg, err := upstream.Load(fstest.MapFS{
		"data/demo.json": {Data: []byte(`{"id":"demo","category":"apikey","display":{"name":"D"},` +
			`"transport":{"baseUrl":"http://127.0.0.1:1/v1/chat/completions","format":"openai"},` +
			`"auth":{"scheme":"bearer","header":"Authorization"},` +
			`"models":[{"id":"m1"},{"id":"m2"},{"id":"m3"},{"id":"m4"},{"id":"m5"},{"id":"m6"},{"id":"img","kind":"image"}]}`)},
	}, "data")
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{
		Pipeline: func() (Handler, error) { return h, nil },
		Nodes:    func() NodeReader { return nodes{} }, Registry: reg, KV: func() catalog.KV { return kv{} }, Now: now,
	})
	return p
}

type nodes map[string]repository.Node

func (n nodes) Get(_ context.Context, id string) (repository.Node, bool, error) {
	v, ok := n[id]
	return v, ok, nil
}

func TestTheProvidersListIsSampledAndTheDefaultModelComesFirst(t *testing.T) {
	h := &handler{}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := newProber(t, h, func() time.Time { return now })
	conn := repository.Connection{ID: "c1", Provider: "demo", Data: `{"defaultModel":"m3"}`}
	got := p.Connection(context.Background(), conn, true)
	if len(got) != 3 {
		t.Fatalf("%d results, want 3", len(got))
	}
	if got[0].ModelID != "m3" || got[0].Model != "demo/m3" {
		t.Fatalf("first result %+v", got[0])
	}
	calls, _, _ := h.state()
	if len(calls) != 3 || calls[0] != "demo/m3" {
		t.Fatalf("calls %v", calls)
	}
}

func TestANonChatModelIsNotProbed(t *testing.T) {
	h := &handler{}
	p := newProber(t, h, time.Now)
	p.Connection(context.Background(), repository.Connection{ID: "c1", Provider: "demo"}, true)
	for _, c := range h.calls {
		if c == "demo/img" {
			t.Fatal("an image model was probed as chat")
		}
	}
}

func TestAConnectionsOwnListIsSampledToo(t *testing.T) {
	h := &handler{}
	p := newProber(t, h, time.Now)
	conn := repository.Connection{ID: "c1", Provider: "demo", Data: `{"providerSpecificData":{"enabledModels":["a","b","c","d","e","f"]}}`}
	got := p.Connection(context.Background(), conn, true)
	if len(got) != 3 {
		t.Fatalf("%d results, want 3", len(got))
	}
	if got[0].Model != "demo/a" {
		t.Fatalf("first %+v", got[0])
	}
	if calls, _, _ := h.state(); len(calls) != 3 {
		t.Fatalf("an enabled list of six spent %d calls", len(calls))
	}
}

func TestAConnectionWithNoModelIsNotPinged(t *testing.T) {
	h := &handler{}
	nodeID := upstream.NodeOpenAIPrefix + "n1"
	p := New(Options{
		Pipeline: func() (Handler, error) { return h, nil },
		Nodes: func() NodeReader {
			return nodes{nodeID: {ID: nodeID, Type: upstream.NodeTypeOpenAI, Data: `{"prefix":"mine"}`}}
		},
		Now: time.Now,
	})
	got := p.Connection(context.Background(), repository.Connection{ID: "c1", Provider: nodeID}, true)
	if len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if calls, _, _ := h.state(); len(calls) != 0 {
		t.Fatalf("a connection with no model spent %v", calls)
	}
}

func TestANodeIsAddressedThroughItsPrefix(t *testing.T) {
	h := &handler{}
	reg, err := upstream.Load(fstest.MapFS{
		"data/demo.json": {Data: []byte(`{"id":"demo","category":"apikey","display":{"name":"D"},` +
			`"transport":{"baseUrl":"http://127.0.0.1:1/v1/chat/completions","format":"openai"},` +
			`"models":[{"id":"m1"}]}`)},
	}, "data")
	if err != nil {
		t.Fatal(err)
	}
	nodeID := upstream.NodeOpenAIPrefix + "n1"
	p := New(Options{
		Pipeline: func() (Handler, error) { return h, nil }, KV: func() catalog.KV { return kv{} }, Registry: reg, Now: time.Now,
		Nodes: func() NodeReader {
			return nodes{nodeID: {ID: nodeID, Type: upstream.NodeTypeOpenAI, Data: `{"prefix":"mine"}`}}
		},
	})
	conn := repository.Connection{ID: "c1", Provider: nodeID, Data: `{"providerSpecificData":{"enabledModels":["a"]}}`}
	got := p.Connection(context.Background(), conn, true)
	if len(got) != 1 || got[0].Model != "mine/a" {
		t.Fatalf("%+v", got)
	}
}

func TestTheFirstPingRunsAloneThenThreeAtOnce(t *testing.T) {
	h := &handler{delay: 5 * time.Millisecond}
	p := newProber(t, h, time.Now)
	conn := repository.Connection{ID: "c1", Provider: "demo", Data: `{"models":["a","b","c","d","e","f","g"]}`}
	got, err := p.All(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("%d results", len(got))
	}
	_, before, maxIn := h.state()
	if maxIn > parallel {
		t.Fatalf("%d pings in flight, want at most %d", maxIn, parallel)
	}
	if before[0] != 0 || before[1] < 1 {
		t.Fatalf("the second ping started before the first finished: %v", before)
	}
}

func TestACachedPingIsNotSpentAgainUnlessForced(t *testing.T) {
	h := &handler{}
	c := &clock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	p := newProber(t, h, c.Now)
	conn := repository.Connection{ID: "c1", Provider: "demo", Data: `{"providerSpecificData":{"enabledModels":["a","b"]}}`}
	p.Connection(context.Background(), conn, false)
	first, _, _ := h.state()
	if len(first) != 2 {
		t.Fatalf("%d calls", len(first))
	}
	c.at = c.at.Add(time.Minute)
	p.Connection(context.Background(), conn, false)
	if again, _, _ := h.state(); len(again) != 2 {
		t.Fatalf("the second read spent %d calls more", len(again)-2)
	}
	c.at = c.at.Add(cacheLife + time.Second)
	p.Connection(context.Background(), conn, false)
	if after, _, _ := h.state(); len(after) != 4 {
		t.Fatalf("%d calls after the cache aged out", len(after))
	}
	p.Connection(context.Background(), conn, true)
	if forced, _, _ := h.state(); len(forced) != 6 {
		t.Fatalf("%d calls after force", len(forced))
	}
}

func TestAFailureIsTheClientsOwnMessageAndKeepsNoBody(t *testing.T) {
	h := &handler{fail: map[string]bool{"demo/m1": true}}
	p := newProber(t, h, time.Now)
	got := p.Model(context.Background(), "demo/m1")
	if got.OK || got.Status != 429 || got.Error != "slow down" {
		t.Fatalf("%+v", got)
	}
	if got.LatencyMs < 0 {
		t.Fatalf("latency %d", got.LatencyMs)
	}
}

func TestAProbeIsPinnedToItsConnectionAndBypassesTheCache(t *testing.T) {
	var seen []pipeline.Input
	h := handlerFunc(func(_ context.Context, in pipeline.Input, _ pipeline.Opener) pipeline.Output {
		seen = append(seen, in)
		return pipeline.Output{Status: 200}
	})
	p := newProber(t, h, time.Now)
	p.Connection(context.Background(), repository.Connection{ID: "conn-9", Provider: "demo", Data: `{"providerSpecificData":{"enabledModels":["a"]}}`}, true)
	if len(seen) != 1 || seen[0].Connection != "conn-9" || seen[0].Cache != "bypass" {
		t.Fatalf("%+v", seen)
	}
	if seen[0].Caller.ID != "system-probe" {
		t.Fatalf("caller %+v", seen[0].Caller)
	}
}

// handlerFunc adapts a function to Handler.
type handlerFunc func(context.Context, pipeline.Input, pipeline.Opener) pipeline.Output

func (f handlerFunc) Handle(ctx context.Context, in pipeline.Input, open pipeline.Opener) pipeline.Output {
	return f(ctx, in, open)
}

func TestAProbeNeverStreams(t *testing.T) {
	var open pipeline.Opener
	h := handlerFunc(func(_ context.Context, _ pipeline.Input, o pipeline.Opener) pipeline.Output {
		open = o
		return pipeline.Output{Status: 200}
	})
	p := newProber(t, h, time.Now)
	p.Model(context.Background(), "demo/m1")
	if open == nil {
		t.Fatal("the probe offered no opener")
	}
	if _, err := open(); err == nil {
		t.Fatal("a probe opened a stream")
	}
}
