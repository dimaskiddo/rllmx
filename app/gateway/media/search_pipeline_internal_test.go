package media

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

func TestAQueryIsNormalizedTheWayTheReferenceSendsIt(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"  hello   world ", "hello world", true},
		{"ＦＵＬＬ", "FULL", true},
		{"tab\tand\nnewline", "tab and newline", true},
		{"bell\x07", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := cleanQuery(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("%q gave %q %v", c.in, got, ok)
		}
	}
}

func TestASearchIsCappedByTheProvidersOwnLimit(t *testing.T) {
	cfg := &upstream.MediaConfig{DefaultResults: 5, MaxResults: 7}
	if got := maxResults(gjson.Parse(`{}`), cfg); got != 5 {
		t.Fatalf("default %d", got)
	}
	if got := maxResults(gjson.Parse(`{"max_results":50}`), cfg); got != 7 {
		t.Fatalf("cap %d", got)
	}
	if got := maxResults(gjson.Parse(`{"max_results":3}`), cfg); got != 3 {
		t.Fatalf("asked %d", got)
	}
}

func TestASearchRefusesWhatTheProviderCannotTake(t *testing.T) {
	cfg := &upstream.MediaConfig{SearchTypes: []string{"web", "news"}}
	cases := map[string]string{
		"no query":          `{"search_type":"web"}`,
		"an unserved type":  `{"query":"q","search_type":"images"}`,
		"a bad offset":      `{"query":"q","offset":-1}`,
		"a filter not text": `{"query":"q","domain_filter":[1]}`,
	}
	for name, body := range cases {
		if _, msg := searchBody(gjson.Parse(body), cfg); msg == "" {
			t.Errorf("%s passed", name)
		}
	}
	many := fmt.Sprintf(`{"query":"q","domain_filter":[%s]}`, strings.TrimSuffix(strings.Repeat(`"a.com",`, 51), ","))
	if _, msg := searchBody(gjson.Parse(many), cfg); msg == "" {
		t.Error("a filter past the bound passed")
	}
	if out, msg := searchBody(gjson.Parse(`{"query":" q ","domain_filter":["a.com","-b.com"],"offset":2}`), cfg); msg != "" || gjson.GetBytes(out, "query").String() != "q" {
		t.Fatalf("%s %s", msg, out)
	}
}

func TestAFetchBodyIsFitToTheProvider(t *testing.T) {
	p := &Pipeline{}
	cfg := &upstream.MediaConfig{Formats: []string{"markdown", "html"}, MaxCharacters: 4000}
	cases := map[string]string{
		"no address":         `{"url":""}`,
		"a raw scheme":       `{"url":"file:///etc/passwd"}`,
		"an unserved format": `{"url":"https://93.184.216.34/a","format":"pdf"}`,
		"a bad limit":        `{"url":"https://93.184.216.34/a","max_characters":-1}`,
	}
	for name, body := range cases {
		if _, msg := p.fetchBody(context.Background(), gjson.Parse(body), cfg); msg == "" {
			t.Errorf("%s passed", name)
		}
	}
	out, msg := p.fetchBody(context.Background(), gjson.Parse(`{"url":"https://93.184.216.34/a","max_characters":9000}`), cfg)
	if msg != "" {
		t.Fatal(msg)
	}
	if gjson.GetBytes(out, "max_characters").Int() != 4000 || gjson.GetBytes(out, "format").String() != "markdown" {
		t.Fatalf("%s", out)
	}
}

// FuzzCleanQuery holds the sanitizer to its word: text in, bounded text or a
// refusal out.
func FuzzCleanQuery(f *testing.F) {
	f.Add("hello world")
	f.Add("ＦＵＬＬ")
	f.Add("\x00")
	f.Fuzz(func(t *testing.T, query string) {
		got, ok := cleanQuery(query)
		if !ok {
			return
		}
		if got == "" || len(got) > maxQuery {
			t.Fatalf("%q", got)
		}
	})
}
