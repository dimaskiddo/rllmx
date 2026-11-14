package chatgptweb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"rllmx/app/llm/media"
	mediatest "rllmx/tests/media"
)

func happy() *mediatest.Routes {
	return mediatest.NewRoutes(map[string]mediatest.Reply{
		"/api/auth/session":                       mediatest.Of(200, []byte(`{"accessToken":"tok-1"}`)),
		"chat-requirements/prepare":               mediatest.Of(200, []byte(`{"prepare_token":"prep"}`)),
		"chat-requirements":                       mediatest.Of(200, []byte(`{"token":"tkn","proofofwork":{"required":true,"seed":"s","difficulty":"f"}}`)),
		"/backend-api/f/conversation/prepare":     mediatest.Of(200, []byte(`{"conduit_token":"c1"}`)),
		"/backend-api/f/conversation":             mediatest.Of(200, []byte(`{"conversation_id":"0b9a5c1e-1111-2222-3333-444455556666","x":"file-service://file_abc123"}`)),
		"/backend-api/files/file_abc123/download": mediatest.Of(200, []byte(`{"download_url":"https://files.example/a.png"}`)),
		"files.example/a.png":                     mediatest.Of(200, mediatest.PNG),
	})
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
	out, err := Images{Every: time.Millisecond}.Direct(context.Background(), r, f)
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
	if raw, err := base64.StdEncoding.DecodeString(answer.Data[0].B64); err != nil || string(raw) != string(mediatest.PNG) {
		t.Fatalf("image bytes do not match")
	}
}

func TestATurnstileDemandIsRefused(t *testing.T) {
	f := happy()
	f.Set("/backend-api/f/conversation", mediatest.Of(403, []byte(`{"error":{"message":"Turnstile required"}}`)))
	r := request(t, `{"prompt":"x"}`, "sess=secret-value")
	_, err := Images{Every: time.Millisecond}.Direct(context.Background(), r, f)
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
			f := mediatest.NewRoutes(nil)
			_, err := Images{Every: time.Millisecond}.Direct(context.Background(), request(t, tc.body, tc.cred), f)
			if err == nil {
				t.Fatal("the request was accepted")
			}
			if len(f.Calls()) != 0 {
				t.Fatalf("calls = %v, want none", f.Calls())
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
	proof, err := solve(context.Background(), "seed", "f")
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if !strings.HasPrefix(proof, "gAAAAAB") || !strings.HasSuffix(proof, "~S") {
		t.Fatalf("proof = %q", proof)
	}
	if _, err := solve(context.Background(), "", ""); err == nil {
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

func TestAProofOfWorkStopsWhenTheRequestDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// "0" is below every hash, so no counter ever meets it: only the context ends this.
	if _, err := solve(ctx, "seed", ""+"0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context's", err)
	}
}

func TestASessionThatExpiresMidPollIsAnAuthFailureAtOnce(t *testing.T) {
	f := happy()
	f.Set("/backend-api/f/conversation", mediatest.Of(200, []byte(`{"conversation_id":"0b9a5c1e-1111-2222-3333-444455556666"}`)))
	f.Set("/backend-api/conversation/0b9a5c1e", mediatest.Of(401, nil))
	started := time.Now()
	_, err := Images{Every: time.Millisecond}.Direct(context.Background(), request(t, `{"prompt":"x"}`, "sess=ok"), f)
	var pe *media.ProviderError
	if !errors.As(err, &pe) || pe.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want a 401 provider error", err)
	}
	if took := time.Since(started); took > pollInterval+3*time.Second {
		t.Fatalf("an expired session was polled for %s", took)
	}
}
