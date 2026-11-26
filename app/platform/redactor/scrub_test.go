package redactor

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestScrub(t *testing.T) {
	jwt := "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig_abc-1"
	gemini := "AIza" + strings.Repeat("a", 30)
	tests := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{"openai style key", "hello sk-abcdefghij world", []string{"sk-abcdefghij"}, []string{"hello", "world"}},
		{"word boundary", "disk-abcdefgh", nil, []string{"disk-abcdefgh"}},
		{"bearer in any case", "authorization: bearer abc.def.ghi and BEARER zzz-999", []string{"abc.def.ghi", "zzz-999"}, []string{"and"}},
		{"bearer stops at the token", "auth Bearer abc.def and more", []string{"abc.def"}, []string{"and more"}},
		{"jwt", "v=" + jwt, []string{jwt}, nil},
		{"google api key", gemini, []string{gemini}, nil},
		{"url with user info and key parameter", "https://user:pw-9@host/x?key=AbC123&a=1", []string{"pw-9", "AbC123"}, []string{"a=1", "host/x"}},
		{"oauth query parameters", "https://h/cb?code=AUTHCODE1&state=s1&access_token=ACC1&refresh_token=REF1&client_secret=CS1&id_token=ID1",
			[]string{"AUTHCODE1", "ACC1", "REF1", "CS1", "ID1"}, []string{"state=s1"}},
		{"oauth form body", "grant_type=authorization_code&code=AUTHCODE2&client_secret=CS2&redirect_uri=http://h",
			[]string{"AUTHCODE2", "CS2"}, []string{"grant_type=authorization_code"}},
		{"form body that starts with the secret", "code=AUTHCODE3&state=s3", []string{"AUTHCODE3"}, []string{"state=s3"}},
		{"pkce verifier", "grant_type=authorization_code&code_verifier=VERIF1&redirect_uri=http://h", []string{"VERIF1"}, []string{"grant_type=authorization_code"}},
		{"query names are case-insensitive", "https://h/x?Access_Token=ACC3&KEY=K3", []string{"ACC3", "K3"}, nil},
		{"plain text is untouched", "listening on 127.0.0.1:20128", nil, []string{"listening on 127.0.0.1:20128"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scrub(tt.in)
			for _, a := range tt.absent {
				if strings.Contains(got, a) {
					t.Errorf("%q still contains %q", got, a)
				}
			}
			for _, p := range tt.present {
				if !strings.Contains(got, p) {
					t.Errorf("%q lost %q", got, p)
				}
			}
		})
	}
}

// The framework's query logger writes every bound value inside the statement,
// and an ERROR or a debug line carries a connection document or the settings
// row with it. The shape of the statement survives; the values do not.
func TestAQueryLineKeepsItsShapeButNotItsValues(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			"a failed select",
			`[0.064ms] [rows:-] SELECT "id","provider" FROM "providerConnections" WHERE "id" = '960812d7-b797' AND "name" = 'o''brien'` + "\tcontext deadline exceeded",
			[]string{"960812d7-b797", "o''brien", "o'brien"},
			[]string{`"providerConnections"`, "context deadline exceeded", "[0.064ms] [rows:-] ", "'[redacted]'"},
		},
		{
			"a slow write",
			`[812.5ms] [rows:1] [SLOW] UPDATE "providerConnections" SET "data"='{"apiKey":"enc:v1:abc"}' WHERE "id" = 'x'`,
			[]string{"enc:v1:abc", "'x'"},
			[]string{`UPDATE "providerConnections" SET "data"=`, "[SLOW]"},
		},
		{
			"an ordinary message keeps its apostrophe",
			`upstream refused text="you've reached your limit"`,
			nil,
			[]string{`you've reached your limit`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Scrub(tt.in)
			for _, s := range tt.absent {
				if strings.Contains(got, s) {
					t.Errorf("%q still holds %q", got, s)
				}
			}
			for _, s := range tt.present {
				if !strings.Contains(got, s) {
					t.Errorf("%q lost %q", got, s)
				}
			}
		})
	}
}

func TestSensitiveName(t *testing.T) {
	for _, n := range []string{"password", "my_token", "api-key", "Authorization", "x_cookie", "client_secret", "apikey", "code_verifier", "private-key", "session_id", "Credentials"} {
		if !SensitiveName(n) {
			t.Errorf("%q should be sensitive", n)
		}
	}
	for _, n := range []string{"method", "path", "status", "model", "provider", "duration", ""} {
		if SensitiveName(n) {
			t.Errorf("%q should not be sensitive", n)
		}
	}
}

func TestValue(t *testing.T) {
	cases := []struct {
		name string
		key  string
		in   any
		want any
	}{
		{"sensitive key hides a string", "password", "pw-1", Redacted},
		{"sensitive key hides a map", "Authorization", map[string]any{"a": "b"}, Redacted},
		{"sensitive key hides a nil", "token", nil, Redacted},
		{"numbers pass under a sensitive key", "max_tokens", 5, 5},
		{"booleans pass under a sensitive key", "secret_enabled", true, true},
		{"durations pass under a sensitive key", "token_ttl", 3 * time.Second, 3 * time.Second},
		{"plain string", "path", "/api/health", "/api/health"},
		{"string with a key", "msg", "key sk-abcdefghijkl end", "key " + Redacted + " end"},
		{"nil under a plain key", "x", nil, nil},
		{"error", "err", errors.New("boom sk-zzzzzzzzzz"), "boom " + Redacted},
		{"plain error", "err", errors.New("boom"), "boom"},
		{"byte slice is read as text", "body", []byte("key sk-abcdefghijkl end"), "key " + Redacted + " end"},
		{"raw json", "body", json.RawMessage(`{"v":"sk-abcdefghijkl"}`), `{"v":"` + Redacted + `"}`},
	}
	for _, c := range cases {
		if got := Value(c.key, c.in); got != c.want {
			t.Errorf("%s: Value(%q, %#v) = %#v, want %#v", c.name, c.key, c.in, got, c.want)
		}
	}
}

func TestValueWalksMapsAndSlices(t *testing.T) {
	in := map[string]any{
		"method": "GET",
		"headers": map[string]any{
			"Authorization": "Bearer abc.def",
			"Accept":        "application/json",
		},
		"list":    []any{"ok", "sk-abcdefghijkl", map[string]any{"cookie": "c-1"}},
		"strings": []string{"a", "sk-abcdefghijkl"},
		"count":   3,
	}
	got, ok := Value("request", in).(map[string]any)
	if !ok {
		t.Fatalf("a map must stay a map: %#v", Value("request", in))
	}
	text := strings.Join(flatten(got), "|")
	for _, leak := range []string{"abc.def", "sk-abcdefghijkl", "c-1"} {
		if strings.Contains(text, leak) {
			t.Errorf("%q leaked: %s", leak, text)
		}
	}
	for _, keep := range []string{"GET", "application/json", "ok", "3"} {
		if !strings.Contains(text, keep) {
			t.Errorf("%q was lost: %s", keep, text)
		}
	}
	if in["headers"].(map[string]any)["Authorization"] != "Bearer abc.def" {
		t.Error("the caller's map was changed")
	}
}

func TestValueUnderASensitiveGroupHidesEverythingInside(t *testing.T) {
	got := Value("session", map[string]any{"id": "sess-1", "n": 3}).(string)
	if got != Redacted {
		t.Fatalf("got %q", got)
	}
	nested := Value("outer", map[string]any{"auth": map[string]any{"cookie": "ck-2", "note": "x"}}).(map[string]any)
	if text := strings.Join(flatten(nested), "|"); strings.Contains(text, "ck-2") {
		t.Fatalf("leaked: %s", text)
	}
}

func TestValueRedactsHTTPHeaders(t *testing.T) {
	got := Value("hdr", http.Header{"Authorization": {"Bearer abc.def"}, "Accept": {"text/plain"}})
	text := strings.Join(flatten(got), "|")
	if strings.Contains(text, "abc.def") || !strings.Contains(text, "text/plain") {
		t.Fatalf("got %s", text)
	}
}

// A cookie value or an API key header has no shape a pattern could find, so
// only its header name says it is secret.
func TestValueRedactsAHeaderByNameWhenTheValueHasNoShape(t *testing.T) {
	got := Value("hdr", http.Header{
		"Cookie":    {"auth_token=plainvalue123"},
		"X-Api-Key": {"plainvalue456"},
		"Accept":    {"text/plain"},
	})
	text := strings.Join(flatten(got), "|")
	for _, leak := range []string{"plainvalue123", "plainvalue456"} {
		if strings.Contains(text, leak) {
			t.Errorf("%q leaked: %s", leak, text)
		}
	}
	if !strings.Contains(text, "text/plain") {
		t.Errorf("a harmless header was lost: %s", text)
	}
}

func TestValueRedactsAStructThroughItsText(t *testing.T) {
	type creds struct{ Note string }
	got := Value("v", creds{Note: "sk-abcdefghijkl"})
	if s, _ := got.(string); strings.Contains(s, "sk-abcdefghijkl") || s == "" {
		t.Fatalf("got %#v", got)
	}
	plain := creds{Note: "fine"}
	if Value("v", plain) != plain {
		t.Fatal("a value with no secret must come back as it was")
	}
}

func nested(levels int, leaf string) map[string]any {
	root := map[string]any{}
	cur := root
	for range levels {
		next := map[string]any{}
		cur["n"] = next
		cur = next
	}
	cur["leaf"] = leaf
	return root
}

func TestValueStopsWalkingAtTheDepthLimit(t *testing.T) {
	shallow := strings.Join(flatten(Value("v", nested(5, "plain-text"))), "|")
	if !strings.Contains(shallow, "plain-text") {
		t.Fatalf("a value a few levels down was lost: %s", shallow)
	}
	deep := strings.Join(flatten(Value("v", nested(maxDepth+4, "plain-text"))), "|")
	if strings.Contains(deep, "plain-text") || !strings.Contains(deep, Redacted) {
		t.Fatalf("a value below the depth limit was kept: %s", deep)
	}
}

func TestValueEndsOnAMapThatContainsItself(t *testing.T) {
	loop := map[string]any{"name": "ok"}
	loop["self"] = loop
	done := make(chan any, 1)
	go func() { done <- Value("v", loop) }()
	select {
	case got := <-done:
		if text := strings.Join(flatten(got), "|"); !strings.Contains(text, "ok") {
			t.Fatalf("got %s", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the walk never ended")
	}
}

func TestMapKeepsKeysAndRedactsValues(t *testing.T) {
	if Map(nil) != nil {
		t.Fatal("a nil map must stay nil")
	}
	got := Map(map[string]any{"user": "ann", "password": "pw", "n": 1})
	if got["user"] != "ann" || got["password"] != Redacted || got["n"] != 1 {
		t.Fatalf("got %#v", got)
	}
}

func flatten(v any) []string {
	switch x := v.(type) {
	case map[string]any:
		var out []string
		for k, e := range x {
			out = append(out, k)
			out = append(out, flatten(e)...)
		}
		return out
	case map[string][]string:
		var out []string
		for k, e := range x {
			out = append(out, k)
			out = append(out, e...)
		}
		return out
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, flatten(e)...)
		}
		return out
	case []string:
		return x
	case string:
		return []string{x}
	}
	return []string{fmt.Sprint(v)}
}
