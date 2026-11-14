package ucweb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

var png = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
var mp4 = append([]byte{0, 0, 0, 24}, append([]byte("ftypisom"), make([]byte, 24)...)...)

// jwt builds a token whose payload carries the given claims.
func jwt(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return "x." + base64.RawURLEncoding.EncodeToString(payload) + ".y"
}

// fake answers each path the flow calls.
type fake struct {
	routes map[string]func() (int, http.Header, []byte)
	calls  []string
	bodies []string
}

func (f *fake) Do(_ context.Context, method, target string, _ map[string]string, body []byte) (int, http.Header, []byte, error) {
	f.calls = append(f.calls, method+" "+target)
	f.bodies = append(f.bodies, string(body))
	best := ""
	for prefix := range f.routes {
		if strings.Contains(target, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return http.StatusNotFound, http.Header{}, nil, nil
	}
	status, header, body := f.routes[best]()
	return status, header, body, nil
}

func (f *fake) DoCapped(ctx context.Context, method, target string, h map[string]string, body []byte, _ int64) (int, http.Header, []byte, error) {
	return f.Do(ctx, method, target, h, body)
}

// happy builds a rig whose Clerk session is named by the legacy claim, so no
// client listing call is needed.
func happy() (*fake, string) {
	client := jwt(map[string]any{"sid": "sess_1"})
	token := jwt(map[string]any{"uid": "user_1"})
	f := &fake{routes: map[string]func() (int, http.Header, []byte){
		"/v1/client/sessions/sess_1/tokens": func() (int, http.Header, []byte) {
			h := http.Header{}
			h.Add("Set-Cookie", "__client=rotated; Path=/; HttpOnly")
			return 200, h, []byte(`{"jwt":"` + token + `"}`)
		},
		"/v2/image-gen": func() (int, http.Header, []byte) {
			return 200, http.Header{}, []byte(`{"url":"https://cdn.example/one.png"}`)
		},
		"/image_to_video": func() (int, http.Header, []byte) {
			return 200, http.Header{}, []byte(`{"url":"https://cdn.example/one.mp4"}`)
		},
		"cdn.example/one.png": func() (int, http.Header, []byte) { return 200, http.Header{}, png },
		"cdn.example/one.mp4": func() (int, http.Header, []byte) { return 200, http.Header{}, mp4 },
	}}
	return f, "__client=" + client + "; other=1"
}

func req(kind, model, body, cred string) media.Request {
	return media.Request{Kind: kind, Provider: "uc-web", Model: model, WireModel: model, Body: []byte(body), Credential: cred,
		Account: map[string]string{"baseUrl": "https://internal.chatuncensored.ai"}}
}

func TestAnImageIsMintedAndDownloaded(t *testing.T) {
	f, cred := happy()
	out, err := Images{}.Direct(context.Background(), req("image", "seedream-v4.5", `{"model":"seedream-v4.5","prompt":"a cat","size":"1024x1024"}`, cred), f)
	if err != nil {
		t.Fatalf("Direct: %v", err)
	}
	if out.Status != 200 || !strings.Contains(string(out.Body), `"b64_json"`) {
		t.Fatalf("answer = %s", out.Body)
	}
	if out.NewCredential == "" || !strings.Contains(out.NewCredential, "__client=rotated") {
		t.Fatalf("rotated credential = %q", out.NewCredential)
	}
	joined := strings.Join(f.bodies, "\n")
	if !strings.Contains(joined, `"model_version":"seedream-v4.5"`) || !strings.Contains(joined, `"imageWidth":"1024"`) {
		t.Fatalf("image body = %s", joined)
	}
}

func TestAVideoKeepsTheProvidersAddress(t *testing.T) {
	f, cred := happy()
	out, err := Videos{}.Direct(context.Background(), req("video", "default", `{"model":"default","prompt":"a wave","seconds":6,"resolution":"720p"}`, cred), f)
	if err != nil {
		t.Fatalf("Direct: %v", err)
	}
	if !strings.Contains(string(out.Body), `"status":"completed"`) || !strings.Contains(string(out.Body), "https://cdn.example/one.mp4") {
		t.Fatalf("answer = %s", out.Body)
	}
	joined := strings.Join(f.bodies, "\n")
	if !strings.Contains(joined, `"num_frames":97`) || !strings.Contains(joined, `"resolution":"720p"`) {
		t.Fatalf("video body = %s", joined)
	}
	if strings.Contains(string(out.Body), "data:video/mp4;base64") {
		t.Fatal("the video was inlined")
	}
}

func TestACookieWithoutAClientIsRefused(t *testing.T) {
	f, _ := happy()
	_, err := Images{}.Direct(context.Background(), req("image", "m", `{"prompt":"x"}`, "other=1"), f)
	var pe *media.ProviderError
	if !errors.As(err, &pe) || pe.Status != 401 {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestABadResultAddressIsRefused(t *testing.T) {
	cases := []string{
		"http://cdn.example/x.png",
		"https://user:pw@cdn.example/x.png",
		"https:///nohost",
		"not a url",
	}
	for _, target := range cases {
		if err := public(target); err == nil {
			t.Fatalf("public(%q) was accepted", target)
		}
	}
	if err := public("https://cdn.example/x.png"); err != nil {
		t.Fatalf("public refused a good address: %v", err)
	}
}

func TestTheSecondsAndResolutionAreClamped(t *testing.T) {
	cases := []struct {
		body string
		want int
	}{
		{`{"seconds":6}`, 6},
		{`{"seconds":0}`, 1},
		{`{"seconds":99}`, 12},
		{`{"duration":8}`, 8},
		{`{}`, 5},
	}
	for _, tc := range cases {
		if got := secondsOf(mustParse(tc.body), 5); got != tc.want {
			t.Fatalf("secondsOf(%s) = %d, want %d", tc.body, got, tc.want)
		}
	}
	if got := pick("720P", []string{"480p", "720p"}); got != "720p" {
		t.Fatalf("pick = %q", got)
	}
	if got := pick("1080p", []string{"480p", "720p"}); got != "" {
		t.Fatalf("pick accepted a value outside the set: %q", got)
	}
}

func mustParse(body string) gjson.Result { return gjson.Parse(body) }

func TestTheAspectIsTheClosestRatio(t *testing.T) {
	aspect, w, h := aspectFor("1792x1024")
	if aspect != "16:9" || w != "1024" || h != "576" {
		t.Fatalf("aspectFor = %s %s %s", aspect, w, h)
	}
	if aspect, _, _ := aspectFor("garbage"); aspect != "1:1" {
		t.Fatalf("fallback = %s", aspect)
	}
}

func TestTheSessionIsReadFromTheClientClaim(t *testing.T) {
	if got := claim(jwt(map[string]any{"sid": "s"}), "sid"); got != "s" {
		t.Fatalf("claim = %q", got)
	}
	if got := claim("not.a.token", "sid"); got != "" {
		t.Fatalf("claim on garbage = %q", got)
	}
}

func TestTheActiveSessionWinsWhenTheClientNamesNone(t *testing.T) {
	token := jwt(map[string]any{"uid": "u"})
	f := &fake{routes: map[string]func() (int, http.Header, []byte){
		"/v1/client": func() (int, http.Header, []byte) {
			return 200, http.Header{}, []byte(`{"sessions":[{"id":"old","status":"ended"},{"id":"live","status":"active"}]}`)
		},
		"/v1/client/sessions/live/tokens": func() (int, http.Header, []byte) {
			return 200, http.Header{}, []byte(`{"jwt":"` + token + `"}`)
		},
	}}
	jar, _ := media.ParseCookie("__client=abc")
	s, err := mintSession(context.Background(), f, jar)
	if err != nil {
		t.Fatalf("mintSession: %v", err)
	}
	if s.jwt != token {
		t.Fatalf("jwt = %q", s.jwt)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "sessions/old/") {
			t.Fatalf("the ended session was used: %v", f.calls)
		}
	}
}
