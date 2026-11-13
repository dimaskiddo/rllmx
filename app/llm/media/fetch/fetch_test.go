package fetch

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

func req(provider, body string, cfg *upstream.MediaConfig) media.Request {
	return media.Request{Kind: upstream.KindFetch, Provider: provider, Config: cfg, Body: []byte(body), Credential: "KEY"}
}

func body(url, format string, limit int) string {
	return `{"url":"` + url + `","format":"` + format + `","max_characters":` + itoa(limit) + `}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestTheVendorsReadTheirOwnAnswer(t *testing.T) {
	cases := []struct {
		name     string
		adapter  media.Adapter
		provider string
		answer   string
		want     string
	}{
		{"firecrawl", Firecrawl{}, "firecrawl", `{"data":{"markdown":"# Page","metadata":{"title":"T"}}}`, "# Page"},
		{"jina", Jina{}, "jina-reader", "Title: A page\n\n# Heading\n\nbody", "Title: A page\n\n# Heading\n\nbody"},
		{"tavily", Tavily{}, "tavily", `{"results":[{"raw_content":"text"}]}`, "text"},
		{"exa", Exa{}, "exa", `{"results":[{"text":"text","title":"T"}]}`, "text"},
		{"ollama", Ollama{}, "ollama", `{"content":"text","title":"T","links":["https://a.test"]}`, "text"},
		{"tinyfish", TinyFish{}, "tinyfish", `{"results":[{"text":"text","title":"T"}]}`, "text"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := c.adapter.Parse(context.Background(), req(c.provider, body("https://example.com/a", "markdown", 1000), &upstream.MediaConfig{}), nil, 200, nil, []byte(c.answer))
			if err != nil {
				t.Fatal(err)
			}
			if got := gjson.GetBytes(out.Body, "content.text").String(); got != c.want {
				t.Fatalf("text %q", got)
			}
			if gjson.GetBytes(out.Body, "provider").String() != c.provider {
				t.Fatalf("provider %s", out.Body)
			}
		})
	}
}

func TestATinyFishFailureIsNotAPage(t *testing.T) {
	_, err := (TinyFish{}).Parse(context.Background(), req("tinyfish", body("https://example.com/a", "markdown", 100), &upstream.MediaConfig{}), nil, 200, nil,
		[]byte(`{"errors":[{"status":403,"error":"blocked"}]}`))
	var pe *media.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err %v", err)
	}
}

func TestTheBuiltInReaderKeepsWhatAPageMeans(t *testing.T) {
	page := `<!doctype html><html><head><title>T</title><style>x{}</style></head><body>
<blockquote><p>Quoted</p></blockquote>
<ul><li>one</li><li>two</li></ul>
<ol><li>first</li></ol>
<pre><code>code here</code></pre>
<p><img src="/i.png" alt="pic"> <a href="javascript:alert(1)">bad</a></p>
</body></html>`
	title, text, err := readPage([]byte(page), "text/html; charset=utf-8", "https://example.com/dir/a", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if title != "T" {
		t.Fatalf("title %q", title)
	}
	for _, want := range []string{"> Quoted", "- one", "1. first", "code here", "![pic](https://example.com/i.png)"} {
		if !strings.Contains(text, want) {
			t.Errorf("%q is missing from:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"javascript:", "x{}", "](" + "javascript"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("%q survived in:\n%s", unwanted, text)
		}
	}
	if !strings.Contains(text, "bad") {
		t.Errorf("the text of a dropped link was lost:\n%s", text)
	}
}

func TestTheReaderPassesPlainTextAndRefusesWhatItCannotRead(t *testing.T) {
	if _, text, err := readPage([]byte("plain body"), "text/plain; charset=utf-8", "https://example.com/a", "markdown"); err != nil || text != "plain body" {
		t.Fatalf("%q %v", text, err)
	}
	_, _, err := readPage([]byte("%PDF-1.7"), "application/pdf", "https://example.com/a", "markdown")
	var bad *media.BadRequest
	if !errors.As(err, &bad) {
		t.Fatalf("err %v", err)
	}
}

func TestTheHTMLFormatHandsBackThePage(t *testing.T) {
	_, text, err := readPage([]byte(`<html><body><p>raw</p></body></html>`), "text/html", "https://example.com/a", "html")
	if err != nil || !strings.Contains(text, "<p>raw</p>") {
		t.Fatalf("%q %v", text, err)
	}
}

func TestAPageIsCutToTheCharacterLimit(t *testing.T) {
	_, text, err := readPage([]byte(`<html><body><p>`+strings.Repeat("word ", 500)+`</p></body></html>`), "text/html", "https://example.com/a", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(truncate(text, 50))) != 50 {
		t.Fatalf("%d runes", len([]rune(truncate(text, 50))))
	}
}

func TestALinkIsResolvedAgainstThePage(t *testing.T) {
	_, text, err := readPage([]byte(`<html><body><p><a href="../x">x</a></p></body></html>`), "text/html", "https://example.com/dir/a", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "[x](https://example.com/x)") {
		t.Fatalf("text %s", text)
	}
}

func TestARedirectIsAFetchTheClientDecides(t *testing.T) {
	// The adapter makes one GET; the guarded client's redirect policy is what
	// refuses a hop to a private address, and it is exercised in platform/network.
	call, err := (Local{}).Build(context.Background(), req("local-fetch", body("https://example.com/a", "markdown", 100), &upstream.MediaConfig{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if call.Method != http.MethodGet || call.URL != "https://example.com/a" || call.Header["Accept"] != accept {
		t.Fatalf("%+v", call)
	}
}
