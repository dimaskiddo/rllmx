package google

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

func TestDimensionsAreSetOnEveryRequestOfABatch(t *testing.T) {
	got, err := setDimensions([]byte(`{"requests":[{"model":"m","content":{}},{"model":"m","content":{}}]}`), 256)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if d := gjson.GetBytes(got, "requests."+string(rune('0'+i))+".outputDimensionality").Int(); d != 256 {
			t.Fatalf("request %d: %d in %s", i, d, got)
		}
	}
	if gjson.GetBytes(got, "outputDimensionality").Exists() {
		t.Fatalf("a batch got a top-level size: %s", got)
	}
}

func TestDimensionsAreSetOnASingleRequest(t *testing.T) {
	got, err := setDimensions([]byte(`{"model":"m","content":{"parts":[{"text":"hi"}]}}`), 128)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(got, "outputDimensionality").Int() != 128 || !strings.Contains(string(got), `"text":"hi"`) {
		t.Fatalf("%s", got)
	}
}

func TestAnImageAnswerCarriesTheTimeItWasMade(t *testing.T) {
	before := time.Now().Unix()
	body := []byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"data":"AAAA"}}]}}]}`)
	out, err := (Images{}).Parse(context.Background(), media.Request{Kind: upstream.KindImage}, nil, 200, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	if created := gjson.GetBytes(out.Body, "created").Int(); created < before {
		t.Fatalf("created %d, want a current time", created)
	}
}
