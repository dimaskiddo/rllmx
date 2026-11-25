package metrics

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rllmx/app/store/analytics"
)

func fixed() time.Time { return time.Unix(1700000000, 0) }

func record() analytics.Record {
	return analytics.Record{
		Provider: "openai", Model: "gpt-5", ConnectionID: "c1", APIKeyID: "k1", Endpoint: "/v1/chat/completions",
		Status: 200, PromptTokens: 100, CompletionTokens: 50, CostKnown: true, Cost: 0.0125,
		LatencyMs: 1500, TTFBMs: 300,
	}
}

func render(t *testing.T, r *Registry, info Info) string {
	t.Helper()
	var buf bytes.Buffer
	if err := r.Write(&buf, info); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestTheScrapeCarriesEveryFamilyAndEndsWithEOF(t *testing.T) {
	r := New(fixed)
	r.Observe(record())
	out := render(t, r, Info{Version: "1.2.3", Commit: "abc"})
	for _, want := range []string{
		"# TYPE rllmx_requests counter", `rllmx_requests_total{provider="openai",model="gpt-5",connection_id="c1",api_key_id="k1",endpoint="/v1/chat/completions",code="2xx",cached="false"} 1`,
		"# TYPE rllmx_tokens counter", `rllmx_tokens_total{provider="openai",model="gpt-5",connection_id="c1",api_key_id="k1",endpoint="/v1/chat/completions",kind="prompt"} 100`,
		"# TYPE rllmx_cost_usd counter", "rllmx_cost_usd_total{",
		"# TYPE rllmx_request_duration_seconds histogram",
		`rllmx_request_duration_seconds_bucket{provider="openai",model="gpt-5",le="2.5"} 1`,
		`rllmx_request_duration_seconds_bucket{provider="openai",model="gpt-5",le="+Inf"} 1`,
		`rllmx_request_duration_seconds_sum{provider="openai",model="gpt-5"} 1.5`,
		`rllmx_request_duration_seconds_count{provider="openai",model="gpt-5"} 1`,
		"# TYPE rllmx_build info", `rllmx_build_info{version="1.2.3",commit="abc"} 1`,
		"# TYPE process_start_time_seconds gauge",
		"# TYPE go_goroutines gauge",
		"# EOF\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the scrape lacks %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "# EOF\n") {
		t.Errorf("the scrape does not end with # EOF")
	}
	for _, name := range Families {
		if !strings.Contains(out, "# TYPE "+name+" ") && !strings.Contains(out, name+"_total") &&
			!strings.Contains(out, name+"_bucket") && !strings.Contains(out, name+" ") {
			t.Errorf("%s writes no line", name)
		}
	}
}

func TestALabelValueIsEscaped(t *testing.T) {
	r := New(fixed)
	rec := record()
	rec.Model = `we"ird\model` + "\n"
	r.Observe(rec)
	out := render(t, r, Info{})
	if !strings.Contains(out, `model="we\"ird\\model\n"`) {
		t.Fatalf("%s", out)
	}
}

func TestAnUnknownCostIsCountedApartFromASpentZero(t *testing.T) {
	r := New(fixed)
	unpriced := record()
	unpriced.CostKnown = false
	r.Observe(unpriced)
	out := render(t, r, Info{})
	if !strings.Contains(out, "rllmx_cost_unknown_requests_total{") || strings.Contains(out, "rllmx_cost_usd_total{") {
		t.Fatalf("%s", out)
	}
}

func TestASeriesPastTheCapFoldsIntoOtherAndKeepsItsProviderAndModel(t *testing.T) {
	r := New(fixed)
	rec := record()
	for i := 0; i <= maxTuples; i++ {
		rec.ConnectionID = "c" + strconv.Itoa(i)
		r.Observe(rec)
	}
	out := render(t, r, Info{})
	if !strings.Contains(out, `connection_id="other",api_key_id="other",endpoint="other"`) {
		t.Fatalf("no folded series:\n%s", tail(out))
	}
	if !strings.Contains(out, "rllmx_metrics_series_folded_total 1") {
		t.Fatalf("the fold was not counted:\n%s", tail(out))
	}
}

// tail shortens a huge scrape to its last few hundred bytes for a failure.
func tail(s string) string {
	if len(s) < 400 {
		return s
	}
	return "…" + s[len(s)-400:]
}

func TestTheInfoFamiliesNameKeysAndAccountsButNeverAKeyValue(t *testing.T) {
	r := New(fixed)
	out := render(t, r, Info{
		Keys:     []KeyInfo{{ID: "k1", Name: "laptop", User: "dimas"}},
		Accounts: []AccountInfo{{ID: "c1", Provider: "openai", Name: "ops@example.com"}},
		Inflight: []Active{{Provider: "openai", Model: "gpt-5", Connection: "c1", Count: 2}},
	})
	for _, want := range []string{
		`rllmx_api_key_info{api_key_id="k1",api_key_name="laptop",user="dimas"} 1`,
		`rllmx_account_info{connection_id="c1",provider="openai",account_name="ops@example.com"} 1`,
		`rllmx_inflight_requests{provider="openai",model="gpt-5",connection_id="c1"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the scrape lacks %q", want)
		}
	}
	if strings.Contains(out, "sk-") {
		t.Error("a key value reached the scrape")
	}
}

func TestEverySumAgreesWithTheRecordsItCounted(t *testing.T) {
	r := New(fixed)
	recs := []analytics.Record{record(), record(), record()}
	recs[2].Provider, recs[2].Model = "anthropic", "claude"
	r.Observe(recs[0])
	r.Observe(recs[1])
	r.Observe(recs[2])
	out := render(t, r, Info{})
	sum := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "rllmx_requests_total{") {
			sum += int(lastField(line))
		}
	}
	if sum != len(recs) {
		t.Fatalf("the series sum to %d, not %d", sum, len(recs))
	}
}

// lastField is the sample value at the end of a line.
func lastField(line string) int64 {
	fields := strings.Fields(line)
	n, _ := strconv.ParseInt(fields[len(fields)-1], 10, 64)
	return n
}

func TestObserveAndWriteRunTogetherWithoutARace(t *testing.T) {
	r := New(fixed)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.Observe(record())
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf bytes.Buffer
			_ = r.Write(&buf, Info{})
		}()
	}
	wg.Wait()
}

// countingSink stands in for the analytics writer.
type countingSink struct{ got int }

func (c *countingSink) Enqueue(analytics.Record) bool { c.got++; return true }

func TestTheSinkRecordsAndForwardsTheSameRecord(t *testing.T) {
	next := &countingSink{}
	reg := New(fixed)
	s := Sink{Next: next, Reg: reg}
	if !s.Enqueue(record()) || next.got != 1 {
		t.Fatalf("the record was not forwarded: %+v", next)
	}
	if !strings.Contains(render(t, reg, Info{}), "rllmx_requests_total{") {
		t.Fatal("the record was not counted")
	}
}
