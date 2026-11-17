package providers

import (
	"context"
	"fmt"
	"testing"

	"rllmx/app/llm/upstream"
)

// priced builds a one-model table at input dollars per million tokens.
func priced(t *testing.T, input float64) *upstream.Table {
	t.Helper()
	body := fmt.Sprintf(`{"models":{"m":{"input":%v,"output":1}}}`, input)
	tb, err := upstream.NewTable([]byte(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestAResetMakesTheNextReaderRebuildTheTable(t *testing.T) {
	builds := 0
	s := &PriceSource{}
	s.build = func(context.Context) (*upstream.Table, error) {
		builds++
		return priced(t, 1), nil
	}
	if _, err := s.Get(); err != nil {
		t.Fatal(err)
	}
	s.Get()
	if builds != 1 {
		t.Fatalf("a built table was rebuilt: %d", builds)
	}
	s.Reset()
	if _, err := s.Get(); err != nil {
		t.Fatal(err)
	}
	if builds != 2 {
		t.Fatalf("the reset did not drop the table: %d", builds)
	}
}

func TestThePriceReaderPricesWithTheTableThatStandsNow(t *testing.T) {
	rate := 1.0
	s := &PriceSource{}
	s.build = func(context.Context) (*upstream.Table, error) { return priced(t, rate), nil }
	read := priceReader{src: s}
	if cost, ok := read.Cost("", "m", upstream.Usage{Prompt: 1_000_000}); !ok || cost != 1 {
		t.Fatalf("%v %v", cost, ok)
	}
	rate = 3
	s.Reset()
	if cost, _ := read.Cost("", "m", upstream.Usage{Prompt: 1_000_000}); cost != 3 {
		t.Fatalf("the reader kept the old table: %v", cost)
	}
	if _, ok := read.Cost("", "unknown", upstream.Usage{}); ok {
		t.Fatal("an unknown model reported a price")
	}
}
