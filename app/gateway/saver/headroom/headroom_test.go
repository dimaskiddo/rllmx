package headroom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rllmx/app/llm/translate"
)

func toolMessage(text string, failed bool) translate.Message {
	return translate.Message{Role: translate.RoleTool, ToolCallID: "call_1", Content: translate.TextPart(text), Error: failed}
}

func requestWith(msgs ...translate.Message) *translate.Request {
	return &translate.Request{Model: "m", Messages: msgs}
}

// fixedClient hands the same test client to every host.
func fixedClient(c *http.Client) func(string) *http.Client {
	return func(string) *http.Client { return c }
}

// server answers every request with the given compressed rows.
func server(t *testing.T, rows []headroomMessage, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != headroomPath {
			t.Errorf("path %q", r.URL.Path)
		}
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": rows})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTheRowsItCompressedAreReplaced(t *testing.T) {
	rows := []headroomMessage{{Role: "tool", ToolCallID: "call_1", Content: "small"}}
	srv := server(t, rows, 0)
	c := &Compressor{Client: fixedClient(srv.Client())}
	r := requestWith(toolMessage("a much longer tool result", false))
	res := c.Apply(context.Background(), r, Options{Enabled: true, URL: srv.URL, Timeout: time.Second})
	if !res.Compressed || r.Messages[0].Content[0].Text != "small" {
		t.Fatalf("result %+v, text %q", res, r.Messages[0].Content[0].Text)
	}
}

func TestAnyDoubtLeavesTheRequestAlone(t *testing.T) {
	long := "a much longer tool result"
	cases := map[string][]headroomMessage{
		"no rows":        nil,
		"reordered rows": {{Role: "user", Content: "x"}},
		"grew":           {{Role: "tool", ToolCallID: "call_1", Content: long + " and more"}},
		"wrong id":       {{Role: "tool", ToolCallID: "other", Content: "small"}},
	}
	for name, rows := range cases {
		srv := server(t, rows, 0)
		r := requestWith(toolMessage(long, false))
		res := (&Compressor{Client: fixedClient(srv.Client())}).Apply(context.Background(), r, Options{Enabled: true, URL: srv.URL, Timeout: time.Second})
		if res.Compressed {
			t.Errorf("%s: accepted", name)
		}
		if r.Messages[0].Content[0].Text != long {
			t.Errorf("%s: text changed to %q", name, r.Messages[0].Content[0].Text)
		}
	}
}

func TestAnAbsentOrSlowSidecarFailsOpen(t *testing.T) {
	long := "a much longer tool result"
	r := requestWith(toolMessage(long, false))
	res := (&Compressor{}).Apply(context.Background(), r, Options{Enabled: true, URL: "http://127.0.0.1:1"})
	if res.Compressed || r.Messages[0].Content[0].Text != long {
		t.Fatalf("result %+v", res)
	}

	srv := server(t, []headroomMessage{{Role: "tool", ToolCallID: "call_1", Content: "small"}}, 200*time.Millisecond)
	r = requestWith(toolMessage(long, false))
	res = (&Compressor{Client: fixedClient(srv.Client())}).Apply(context.Background(), r, Options{Enabled: true, URL: srv.URL, Timeout: time.Millisecond})
	if res.Compressed {
		t.Fatal("a timed-out answer was accepted")
	}
}

func TestUserTextIsSentOnlyWhenAllowed(t *testing.T) {
	rows := []headroomMessage{
		{Role: "tool", ToolCallID: "call_1", Content: "small"},
		{Role: "user", Content: "short question"},
	}
	r := requestWith(toolMessage("a much longer tool result", false),
		translate.Message{Role: translate.RoleUser, Content: translate.TextPart("a much longer question")})

	srv := server(t, rows, 0)
	(&Compressor{Client: fixedClient(srv.Client())}).Apply(context.Background(), r, Options{Enabled: true, URL: srv.URL, Timeout: time.Second})
	if r.Messages[1].Content[0].Text != "a much longer question" {
		t.Fatal("a user message was compressed without the setting")
	}

	srv = server(t, rows, 0)
	(&Compressor{Client: fixedClient(srv.Client())}).Apply(context.Background(), r, Options{Enabled: true, URL: srv.URL, CompressUserMessages: true, Timeout: time.Second})
	if r.Messages[1].Content[0].Text != "short question" {
		t.Fatalf("user text %q", r.Messages[1].Content[0].Text)
	}
}

func TestTheEndpointIsTheSidecarsCompressPath(t *testing.T) {
	for raw, want := range map[string]string{
		"http://localhost:8787":     "http://localhost:8787/v1/compress",
		"http://localhost:8787/":    "http://localhost:8787/v1/compress",
		"http://host:9/base":        "http://host:9/base/v1/compress",
		"https://host:9/base?x=1#f": "",
		"ftp://host:8787":           "",
		"http://u:p@host:8787":      "",
	} {
		got, err := compressEndpoint(raw)
		if want == "" {
			if err == nil {
				t.Errorf("%s: accepted as %s", raw, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("%s: %q, %v", raw, got, err)
		}
	}
}

func TestAPromptNeverLeaves(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []headroomMessage `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, m := range body.Messages {
			if m.Role == "system" || strings.Contains(m.Content, "system instruction") {
				seen = m.Content
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": body.Messages})
	}))
	t.Cleanup(srv.Close)
	r := requestWith(
		translate.Message{Role: translate.RoleSystem, Content: translate.TextPart("a system instruction")},
		toolMessage("a much longer tool result", false),
	)
	(&Compressor{Client: fixedClient(srv.Client())}).Apply(context.Background(), r, Options{Enabled: true, URL: srv.URL, Timeout: time.Second})
	if seen != "" {
		t.Fatalf("the system text was sent: %q", seen)
	}
}
