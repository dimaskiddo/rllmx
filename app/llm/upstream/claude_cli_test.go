package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func claudeServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTheClaudeCLIVersionIsTheFloorUntilAServerRaisesIt(t *testing.T) {
	c := NewClaudeCLI("")
	if c.Version() != ClaudeCLIFloor || c.Pinned() {
		t.Fatalf("%s %v", c.Version(), c.Pinned())
	}
	srv := claudeServer(t, http.StatusOK, `{"version":"2.1.400"}`)
	c.url = srv.URL
	if !c.Refresh(context.Background(), srv.Client()) || c.Version() != "2.1.400" {
		t.Fatalf("%s", c.Version())
	}
	// A published version older than the floor never lowers it.
	old := claudeServer(t, http.StatusOK, `{"version":"2.0.1"}`)
	c.url = old.URL
	if !c.Refresh(context.Background(), old.Client()) {
		t.Fatal("a usable answer was refused")
	}
	if c.Version() != ClaudeCLIFloor {
		t.Fatalf("the floor was lowered: %s", c.Version())
	}
}

func TestAnUnusableClaudeCLIAnswerKeepsTheLastValue(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"bad format":  {http.StatusOK, `{"version":"2.1"}`},
		"not json":    {http.StatusOK, `nope`},
		"no version":  {http.StatusOK, `{}`},
		"server fail": {http.StatusBadGateway, `{"version":"9.9.9"}`},
		"oversized":   {http.StatusOK, `{"version":"9.9.9","pad":"` + strings.Repeat("x", claudeCLIMaxBytes) + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			c := NewClaudeCLI("")
			srv := claudeServer(t, tc.status, tc.body)
			c.url = srv.URL
			if c.Refresh(context.Background(), srv.Client()) {
				t.Fatalf("%s: an unusable answer was accepted", name)
			}
			if c.Version() != ClaudeCLIFloor {
				t.Fatalf("%s: %s", name, c.Version())
			}
		})
	}
}

func TestAPinnedClaudeCLIVersionNeverCalls(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer srv.Close()
	c := NewClaudeCLI("2.1.999")
	c.url = srv.URL
	if !c.Pinned() || c.Version() != "2.1.999" {
		t.Fatalf("%v %s", c.Pinned(), c.Version())
	}
	if c.Refresh(context.Background(), srv.Client()) {
		t.Fatal("a pinned version refreshed")
	}
	if calls != 0 {
		t.Fatalf("%d calls", calls)
	}
}

func TestTheClaudeCLIHeaderRewrite(t *testing.T) {
	c := NewClaudeCLI("")
	headers := map[string]string{
		"User-Agent": "claude-cli/2.1.280 (external, sdk-cli)",
		"X-Other":    "claude-code/1.0.0 stays",
		"X-Text":     "claude-cli/2.1.322",
	}
	c.Rewrite(headers)
	if headers["User-Agent"] != "claude-cli/"+ClaudeCLIFloor+" (external, sdk-cli)" || headers["X-Text"] != "claude-cli/"+ClaudeCLIFloor {
		t.Fatalf("%v", headers)
	}
	// A value that names no claude-cli version is left alone.
	if headers["X-Other"] != "claude-code/1.0.0 stays" {
		t.Fatalf("%v", headers)
	}
}

func TestTheClaudeFileAdvertisesTheFloor(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	e, ok := reg.ByID("claude")
	if !ok || e.OAuth == nil {
		t.Fatal("the claude entry is missing")
	}
	want := "claude-cli/" + ClaudeCLIFloor + " (external, sdk-cli)"
	if got := e.OAuth.Identity["User-Agent"]; got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}
