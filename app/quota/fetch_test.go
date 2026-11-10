package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func fetcherRig(t *testing.T, handlers map[string]func(w http.ResponseWriter, r *http.Request)) (*Fetcher, *httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	paths := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path+" "+r.Header.Get("Authorization")+" "+r.Header.Get("X-Client-Name"))
		mu.Unlock()
		if h, ok := handlers[r.URL.Path]; ok {
			h(w, r)
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	return &Fetcher{Client: srv.Client(), URL: Endpoints{Models: srv.URL + "/models", Summary: srv.URL + "/summary", Load: srv.URL + "/load", UserAgent: "ag/1", Version: "1"}}, srv, &paths
}

func reply(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }
}

func TestAPaidAccountReadsBothTheModelsAndTheSummary(t *testing.T) {
	f, _, paths := fetcherRig(t, map[string]func(http.ResponseWriter, *http.Request){
		"/load":    reply(`{"cloudaicompanionProject":{"id":"my-proj-123"},"paidTier":{"id":"g1-pro-tier"}}`),
		"/models":  reply(`{"models":{"claude-opus-5-5-high":{"quotaInfo":{"remainingFraction":0}}}}`),
		"/summary": reply(`{"groups":[{"displayName":"Claude","buckets":[{"window":"5h","remainingFraction":0.5}]}]}`),
	})
	snap := f.Fetch(context.Background(), "a1", "TOK")
	if snap["claude-opus-5-5-high"].Remaining != 0 || snap[ClaudeGPTSession].Remaining != 0 {
		t.Fatalf("%v", snap)
	}
	if len(*paths) != 3 || !strings.Contains((*paths)[1], "Bearer TOK antigravity") {
		t.Fatalf("%v", *paths)
	}
}

func TestAFreeAccountReadsTheSummaryOnly(t *testing.T) {
	for name, load := range map[string]string{"free tier": `{"paidTier":{"id":"free-tier"}}`, "no tier": `{}`, "load fails": ``} {
		h := map[string]func(http.ResponseWriter, *http.Request){
			"/models":  reply(`{"models":{"claude-opus-5-5-high":{"quotaInfo":{"remainingFraction":0}}}}`),
			"/summary": reply(`{"groups":[{"displayName":"Gemini","buckets":[{"window":"weekly","remainingFraction":0.4}]}]}`),
		}
		if load != "" {
			h["/load"] = reply(load)
		}
		f, _, paths := fetcherRig(t, h)
		snap := f.Fetch(context.Background(), "a1", "TOK")
		if snap[GeminiWeekly].Remaining != 40 || len(snap) != 1 {
			t.Errorf("%s: %v", name, snap)
		}
		for _, p := range *paths {
			if strings.HasPrefix(p, "/models") {
				t.Errorf("%s: the per-model call was made for a free account", name)
			}
		}
	}
}

func TestAFetchThatLearnsNothingReturnsNilSoTheOldReadingStays(t *testing.T) {
	f, _, _ := fetcherRig(t, map[string]func(http.ResponseWriter, *http.Request){
		"/summary": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) },
		"/load":    reply(`not json`),
	})
	if snap := f.Fetch(context.Background(), "a1", "TOK"); snap != nil {
		t.Fatalf("%v", snap)
	}
	if snap := (&Fetcher{}).Fetch(context.Background(), "a1", "T"); snap != nil {
		t.Fatalf("%v", snap)
	}
}

func TestAnOversizedAnswerIsDropped(t *testing.T) {
	big := `{"groups":[],"pad":"` + strings.Repeat("a", maxAnswer) + `"}`
	f, _, _ := fetcherRig(t, map[string]func(http.ResponseWriter, *http.Request){"/summary": reply(big)})
	if snap := f.Fetch(context.Background(), "a1", "T"); snap != nil {
		t.Fatalf("%v", snap)
	}
}
