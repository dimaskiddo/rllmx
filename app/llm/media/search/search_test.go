package search

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

func req(provider, body string, cfg *upstream.MediaConfig, entry upstream.Entry) media.Request {
	return media.Request{Kind: upstream.KindSearch, Provider: provider, Config: cfg, Body: []byte(body), Credential: "KEY", Entry: entry}
}

func queryBody() string { return `{"query":"hello world","max_results":5,"search_type":"web"}` }

func TestAProviderWithoutItsEngineIdIsRefused(t *testing.T) {
	_, err := (GooglePSE{}).Build(context.Background(), req("google-pse", queryBody(), &upstream.MediaConfig{BaseURL: "https://www.googleapis.com/customsearch/v1"}, upstream.Entry{}), nil)
	var bad *media.BadRequest
	if !errors.As(err, &bad) {
		t.Fatalf("err %v", err)
	}
}

func TestTinyFishKeepsItsOffsetInsideOnePage(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://api.search.tinyfish.ai"}
	_, err := (TinyFish{}).Build(context.Background(), req("tinyfish", `{"query":"q","max_results":5,"offset":8}`, cfg, upstream.Entry{}), nil)
	var bad *media.BadRequest
	if !errors.As(err, &bad) {
		t.Fatalf("8+5 does not fit a ten-result page: err %v", err)
	}
}

func TestTheBuildersSendWhatEachProviderReads(t *testing.T) {
	calls := []struct {
		name     string
		adapter  media.Adapter
		provider string
		body     string
		cfg      *upstream.MediaConfig
		account  map[string]string
		want     []string
	}{
		{"serper news", Serper{}, "serper", `{"query":"q","max_results":3,"search_type":"news","country":"ID","language":"id"}`, &upstream.MediaConfig{BaseURL: "https://google.serper.dev"}, nil,
			[]string{"POST https://google.serper.dev/news", `"q":"q"`, `"gl":"id"`, `"hl":"id"`}},
		{"brave web", Brave{}, "brave-search", queryBody(), &upstream.MediaConfig{BaseURL: "https://api.search.brave.com/res/v1"}, nil,
			[]string{"GET https://api.search.brave.com/res/v1/web/search?count=5", "q=hello+world"}},
		{"exa filters", Exa{}, "exa", `{"query":"q","max_results":2,"domain_filter":["a.com","-b.com"]}`, &upstream.MediaConfig{BaseURL: "https://api.exa.ai/search"}, nil,
			[]string{"POST https://api.exa.ai/search", `"includeDomains":["a.com"]`, `"excludeDomains":["b.com"]`}},
		{"youcom livecrawl", YouCom{}, "youcom", `{"query":"q","max_results":2,"search_type":"news"}`, &upstream.MediaConfig{BaseURL: "https://ydc-index.io/v1/search"}, nil,
			[]string{"GET https://ydc-index.io/v1/search?", "count=2"}},
		{"searxng appends its path", SearXNG{}, "searxng", queryBody(), &upstream.MediaConfig{BaseURL: "http://localhost:8888"}, nil,
			[]string{"GET http://localhost:8888/search?", "format=json", "categories=general"}},
		{"xquik query type", Xquik{}, "xquik", `{"query":"q","max_results":2}`, &upstream.MediaConfig{BaseURL: "https://xquik.com/api/v1/x/tweets/search"}, map[string]string{"queryType": "Top"},
			[]string{"queryType=Top"}},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			r := req(c.provider, c.body, c.cfg, upstream.Entry{})
			r.Account = c.account
			call, err := c.adapter.Build(context.Background(), r, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := call.Method + " " + call.URL + " " + string(call.Body)
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("%s is missing from %s", want, got)
				}
			}
			if c.provider == "searxng" {
				return
			}
			if call.Header["X-Api-Key"] != "KEY" && call.Header["Authorization"] != "Bearer KEY" && !strings.Contains(call.URL, "key=KEY") {
				t.Errorf("the credential was not sent: %v %s", call.Header, call.URL)
			}
		})
	}
}

func TestANormalizerReadsTheProvidersAnswer(t *testing.T) {
	cases := []struct {
		name     string
		adapter  media.Adapter
		provider string
		body     string
		answer   string
		wantURL  string
	}{
		{"serper", Serper{}, "serper", queryBody(), `{"organic":[{"title":"A","link":"https://a.test/x","snippet":"s"}]}`, "https://a.test/x"},
		{"brave", Brave{}, "brave-search", queryBody(), `{"web":{"results":[{"title":"A","url":"https://a.test/x","description":"s"}]}}`, "https://a.test/x"},
		{"exa", Exa{}, "exa", queryBody(), `{"results":[{"title":"A","url":"https://a.test/x","text":"body","score":0.7}]}`, "https://a.test/x"},
		{"tavily", Tavily{}, "tavily", queryBody(), `{"results":[{"title":"A","url":"https://a.test/x","content":"s"}]}`, "https://a.test/x"},
		{"tinyfish", TinyFish{}, "tinyfish", queryBody(), `{"results":[{"title":"A","url":"https://a.test/x","snippet":"s"}]}`, "https://a.test/x"},
		{"searxng", SearXNG{}, "searxng", queryBody(), `{"results":[{"title":"A","url":"https://a.test/x","content":"s"}]}`, "https://a.test/x"},
		{"glm in a text envelope", GLM{}, "glm", queryBody(), `{"result":{"content":[{"type":"text","text":"{\"results\":[{\"title\":\"A\",\"link\":\"https://a.test/x\"}]}"}]}}`, "https://a.test/x"},
		{"xquik", Xquik{}, "xquik", queryBody(), `{"tweets":[{"id":"1","text":"t","author":{"username":"u"}}],"has_next_page":true}`, "https://x.com/u/status/1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := c.adapter.Parse(context.Background(), req(c.provider, c.body, &upstream.MediaConfig{}, upstream.Entry{}), nil, 200, nil, []byte(c.answer))
			if err != nil {
				t.Fatal(err)
			}
			if got := gjson.GetBytes(out.Body, "results.0.url").String(); got != c.wantURL {
				t.Fatalf("url %q in %s", got, out.Body)
			}
			if !gjson.ValidBytes(out.Body) {
				t.Fatalf("not JSON: %s", out.Body)
			}
		})
	}
}

func TestAChatAnswerCarriesItsCitations(t *testing.T) {
	entry := upstream.Entry{Media: &upstream.Media{SearchViaChat: &upstream.SearchViaChat{DefaultModel: "gemini-2.5-flash"}}}
	out, err := (Chat{}).Parse(context.Background(), req("gemini", queryBody(), &upstream.MediaConfig{}, entry), nil, 200, nil,
		[]byte(`{"candidates":[{"content":{"parts":[{"text":"an answer"}]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://a.test/x","title":"A"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out.Body, "answer.text").String() != "an answer" || gjson.GetBytes(out.Body, "results.0.url").String() != "https://a.test/x" {
		t.Fatalf("%s", out.Body)
	}
}

func TestTheChatShapesAreTheProvidersOwn(t *testing.T) {
	cases := []struct {
		provider string
		want     []string
		header   string
	}{
		{"gemini", []string{`"google_search":{}`}, "x-goog-api-key"},
		{"openai", []string{`"type":"web_search"`}, "Authorization"},
		{"xai", []string{`"input":[`, `"type":"web_search"`}, "Authorization"},
		{"kimi", []string{`"name":"$web_search"`}, "Authorization"},
		{"minimax", []string{`"tools":[{"type":"web_search"}]`}, "Authorization"},
		{"perplexity", []string{`"messages":[`}, "Authorization"},
		{"perplexity-agent", []string{`"input":"hello world"`}, "Authorization"},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			entry := upstream.Entry{Media: &upstream.Media{SearchViaChat: &upstream.SearchViaChat{
				DefaultModel: "m", Endpoint: "https://api.test/search/{model}",
			}}}
			r := req(c.provider, queryBody(), &upstream.MediaConfig{}, entry)
			call, err := (Chat{}).Build(context.Background(), r, nil)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(call.URL, "{model}") || !strings.HasSuffix(call.URL, "/search/m") {
				t.Fatalf("url %s", call.URL)
			}
			for _, want := range c.want {
				if !strings.Contains(string(call.Body), want) {
					t.Errorf("%s is missing from %s", want, call.Body)
				}
			}
			if call.Header[c.header] == "" {
				t.Errorf("no %s in %v", c.header, call.Header)
			}
		})
	}
}

func TestAnAccountWithoutAProjectCannotSearch(t *testing.T) {
	entry := upstream.Entry{Media: &upstream.Media{SearchViaChat: &upstream.SearchViaChat{DefaultModel: "gemini-2.5-flash", Endpoint: "https://daily.test/x"}}}
	_, err := (Chat{}).Build(context.Background(), req("antigravity", queryBody(), &upstream.MediaConfig{}, entry), nil)
	var bad *media.BadRequest
	if !errors.As(err, &bad) {
		t.Fatalf("err %v", err)
	}
}

func TestOpenAIFallsBackToItsChatAddress(t *testing.T) {
	entry := upstream.Entry{Transport: upstream.Transport{BaseURL: "https://api.openai.com/v1/chat/completions"},
		Media: &upstream.Media{SearchViaChat: &upstream.SearchViaChat{DefaultModel: "gpt-4o-mini"}}}
	call, err := (Chat{}).Build(context.Background(), req("openai", queryBody(), &upstream.MediaConfig{}, entry), nil)
	if err != nil {
		t.Fatal(err)
	}
	if call.URL != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("url %s", call.URL)
	}
}

func TestOpenAINamesNoShapeForVercel(t *testing.T) {
	entry := upstream.Entry{Media: &upstream.Media{SearchViaChat: &upstream.SearchViaChat{DefaultModel: "openai/gpt-4o-mini", Endpoint: "https://ai-gateway.vercel.sh/v1/chat/completions"}}}
	if _, err := (Chat{}).Build(context.Background(), req("vercel-ai-gateway", queryBody(), &upstream.MediaConfig{}, entry), nil); err == nil {
		t.Fatal("a provider with no chat search shape answered")
	}
}

func TestARefusalIsTheProvidersStatus(t *testing.T) {
	_, err := (Serper{}).Parse(context.Background(), req("serper", queryBody(), &upstream.MediaConfig{}, upstream.Entry{}), nil, http.StatusTooManyRequests, nil, nil)
	var pe *media.ProviderError
	if !errors.As(err, &pe) || pe.Status != http.StatusTooManyRequests {
		t.Fatalf("err %v", err)
	}
}

func TestResultsAreBoundedByTheCountAsked(t *testing.T) {
	answer := `{"organic":[{"title":"A","link":"https://a.test/1"},{"title":"B","link":"https://a.test/2"},{"title":"C","link":"https://a.test/3"}]}`
	out, err := (Serper{}).Parse(context.Background(), req("serper", `{"query":"q","max_results":2}`, &upstream.MediaConfig{}, upstream.Entry{}), nil, 200, nil, []byte(answer))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(gjson.GetBytes(out.Body, "results").Array()); n != 2 {
		t.Fatalf("%d results", n)
	}
}
