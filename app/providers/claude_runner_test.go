package providers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"rllmx/app/llm/upstream"
)

type cannedTransport struct{ body string }

func (c cannedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(c.body)),
		Header:     make(http.Header),
	}, nil
}

func TestTheClaudeRunnerRefreshesThenStopsOnCancel(t *testing.T) {
	cli := upstream.NewClaudeCLI("")
	ctx, cancel := context.WithCancel(context.Background())
	r := &claudeRunner{
		cli: cli, ctx: ctx, cancel: cancel,
		newClient: func() *http.Client { return &http.Client{Transport: cannedTransport{`{"version":"2.1.401"}`}} },
	}
	done := make(chan error, 1)
	go func() { done <- r.Run() }()
	deadline := time.Now().Add(2 * time.Second)
	for cli.Version() == upstream.ClaudeCLIFloor {
		select {
		case err := <-done:
			t.Fatalf("the runner returned before refreshing: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the first refresh never ran")
		}
		time.Sleep(time.Millisecond)
	}
	if cli.Version() != "2.1.401" {
		t.Fatalf("version %s", cli.Version())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the runner did not stop on cancel")
	}
}

func TestAPinnedClaudeRunnerDoesNothing(t *testing.T) {
	calls := 0
	cli := upstream.NewClaudeCLI("2.1.500")
	r := &claudeRunner{
		cli: cli, ctx: context.Background(),
		newClient: func() *http.Client {
			calls++
			return &http.Client{Transport: cannedTransport{`{"version":"9.9.9"}`}}
		},
	}
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || cli.Version() != "2.1.500" {
		t.Fatalf("%d calls, %s", calls, cli.Version())
	}
}
