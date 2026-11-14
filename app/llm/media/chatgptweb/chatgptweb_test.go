package chatgptweb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"rllmx/app/llm/media"
)

// png is a one-pixel PNG, enough for the format check.
var png = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

// fake answers each path the flow calls.
type fake struct {
	routes map[string]func() (int, []byte)
	calls  []string
}

func (f *fake) Do(_ context.Context, method, target string, _ map[string]string, _ []byte) (int, http.Header, []byte, error) {
	f.calls = append(f.calls, method+" "+target)
	// The longest matching prefix wins, so chat-requirements/prepare is not
	// answered by chat-requirements.
	best := ""
	for prefix := range f.routes {
		if strings.Contains(target, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best != "" {
		status, body := f.routes[best]()
		return status, http.Header{}, body, nil
	}
	return http.StatusNotFound, http.Header{}, nil, nil
}

func (f *fake) DoCapped(ctx context.Context, method, target string, h map[string]string, body []byte, _ int64) (int, http.Header, []byte, error) {
	return f.Do(ctx, method, target, h, body)
}

func happy() *fake {
	return &fake{routes: map[string]func() (int, []byte){
		"/api/auth/session": func() (int, []byte) {
			return 200, []byte(`{"accessToken":"tok-1"}`)
		},
		"chat-requirements/prepare": func() (int, []byte) {
			return 200, []byte(`{"prepare_token":"prep"}`)
		},
		"chat-requirements": func() (int, []byte) {
			return 200, []byte(`{"token":"tkn","proofofwork":{"required":true,"seed":"s","difficulty":"f"}}`)
		},
		"/backend-api/f/conversation/prepare": func() (int, []byte) {
			return 200, []byte(`{"conduit_token":"c1"}`)
		},
		"/backend-api/f/conversation": func() (int, []byte) {
			return 200, []byte(`{"conversation_id":"0b9a5c1e-1111-2222-3333-444455556666","x":"file-service://file_abc123"}`)
		},
		"/backend-api/files/file_abc123/download": func() (int, []byte) {
			return 200, []byte(`{"download_url":"https://files.example/a.png"}`)
		},
		"files.example/a.png": func() (int, []byte) { return 200, png },
	}}
}

func request(t *testing.T, body string, credential string) media.Request {
	t.Helper()
	return media.Request{
		Kind:       "image",
		Provider:   "chatgpt-web",
		Body:       []byte(body),
		Credential: credential,
		Account:    map[string]string{"baseUrl": "https://chatgpt.com"},
	}
}

func TestTheImageComesBackAsBase64(t *testing.T) {
	f := happy()
	r := request(t, `{"model":"gpt-image-2","prompt":"a red fox","n":1,"size":"1024x1024"}`, "sess=ok; other=1")
	out, err := Images{}.Direct(context.Background(), r, f)
	if err != nil {
		t.Fatalf("Direct: %v", err)
	}
	if out.Status != 200 {
		t.Fatalf("status = %d", out.Status)
	}
	if got := string(out.Body); !strings.Contains(got, `"b64_json"`) || !strings.Contains(got, `"mime_type":"image/png"`) {
		t.Fatalf("answer = %s", got)
	}
	var answer struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Body, &answer); err != nil || len(answer.Data) != 1 {
		t.Fatalf("answer is not one image: %v", err)
	}
	if raw, err := base64.StdEncoding.DecodeString(answer.Data[0].B64); err != nil || string(raw) != string(png) {
		t.Fatalf("image bytes do not match")
	}
}

func TestATurnstileDemandIsRefused(t *testing.T) {
	f := happy()
	f.routes["/backend-api/f/conversation"] = func() (int, []byte) {
		return 403, []byte(`{"error":{"message":"Turnstile required"}}`)
	}
	r := request(t, `{"prompt":"x"}`, "sess=secret-value")
	_, err := Images{}.Direct(context.Background(), r, f)
	var pe *media.ProviderError
	if !errors.As(err, &pe) || pe.Status != 403 {
		t.Fatalf("err = %v, want a 403 provider error", err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatal("the cookie reached the error text")
	}
}

func TestBadInputIsRefusedBeforeAnyCall(t *testing.T) {
	cases := []struct {
		name string
		body string
		cred string
	}{
		{"no prompt", `{"n":1}`, "a=1"},
		{"two images", `{"prompt":"x","n":2}`, "a=1"},
		{"a broken cookie", `{"prompt":"x"}`, "a=1\r\nInjected: 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fake{}
			_, err := Images{}.Direct(context.Background(), request(t, tc.body, tc.cred), f)
			if err == nil {
				t.Fatal("the request was accepted")
			}
			if len(f.calls) != 0 {
				t.Fatalf("calls = %v, want none", f.calls)
			}
		})
	}
}

func TestTheScraperFindsEveryKindOfID(t *testing.T) {
	upload := "file_00000000" + strings.Repeat("ab", 12)
	body := []byte(`{"conversation_id":"0b9a5c1e-1111-2222-3333-444455556666",` +
		`"a":"file-service://one","b":"file-service://one","c":"` + upload + `",` +
		`"d":"sediment://three"}`)
	conv, files, sediments := scrape(body)
	if conv != "0b9a5c1e-1111-2222-3333-444455556666" {
		t.Fatalf("conversation = %q", conv)
	}
	if len(files) != 2 || files[0] != "one" || files[1] != upload {
		t.Fatalf("files = %v", files)
	}
	if len(sediments) != 1 || sediments[0] != "three" {
		t.Fatalf("sediments = %v", sediments)
	}
}

func TestTheChallengeHashMatchesThePublishedShape(t *testing.T) {
	// The challenge compares a hex prefix with the difficulty word, so a
	// single-digit difficulty is always met on the first counter.
	proof, err := solve("seed", "f")
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if !strings.HasPrefix(proof, "gAAAAAB") || !strings.HasSuffix(proof, "~S") {
		t.Fatalf("proof = %q", proof)
	}
	if _, err := solve("", ""); err == nil {
		t.Fatal("a challenge with no seed was solved")
	}
}

func TestThePrepareTokenCarriesTheFingerprint(t *testing.T) {
	token := prepareToken()
	if !strings.HasPrefix(token, "gAAAAAC") {
		t.Fatalf("token = %q", token)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(token, "gAAAAAC"))
	if err != nil {
		t.Fatalf("token is not base64: %v", err)
	}
	var slots []any
	if err := json.Unmarshal(raw, &slots); err != nil || len(slots) != 25 {
		t.Fatalf("slots = %v (%v)", slots, err)
	}
	if slots[3].(float64) != 1 {
		t.Fatalf("counter slot = %v, want 1", slots[3])
	}
}
