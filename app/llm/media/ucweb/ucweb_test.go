package ucweb

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	mediatest "rllmx/tests/media"
)

// happy builds a rig whose Clerk session is named by the legacy claim, so no
// client listing call is needed.
func happy() (*mediatest.Routes, string) {
	client := mediatest.JWT(map[string]any{"sid": "sess_1"})
	token := mediatest.JWT(map[string]any{"uid": "user_1"})
	f := mediatest.NewRoutes(map[string]mediatest.Reply{
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
		"cdn.example/one.png": mediatest.Of(200, mediatest.PNG),
		"cdn.example/one.mp4": mediatest.Of(200, mediatest.MP4),
	})
	return f, "__client=" + client + "; other=1"
}

func req(kind, model, body, cred string) media.Request {
	return media.Request{Kind: kind, Provider: "uc-web", Model: model, WireModel: model, Body: []byte(body), Credential: cred,
		Account: map[string]string{"baseUrl": "https://internal.chatuncensored.ai"}}
}

func TestAnImageIsMintedAndDownloaded(t *testing.T) {
	f, cred := happy()
	out, err := Images{Every: time.Millisecond}.Direct(context.Background(), req("image", "seedream-v4.5", `{"model":"seedream-v4.5","prompt":"a cat","size":"1024x1024"}`, cred), f)
	if err != nil {
		t.Fatalf("Direct: %v", err)
	}
	if out.Status != 200 || !strings.Contains(string(out.Body), `"b64_json"`) {
		t.Fatalf("answer = %s", out.Body)
	}
	if out.NewCredential == "" || !strings.Contains(out.NewCredential, "__client=rotated") {
		t.Fatalf("rotated credential = %q", out.NewCredential)
	}
	joined := f.Sent()
	if !strings.Contains(joined, `"model_version":"seedream-v4.5"`) || !strings.Contains(joined, `"imageWidth":"1024"`) {
		t.Fatalf("image body = %s", joined)
	}
}

func TestAVideoKeepsTheProvidersAddress(t *testing.T) {
	f, cred := happy()
	out, err := Videos{Every: time.Millisecond}.Direct(context.Background(), req("video", "default", `{"model":"default","prompt":"a wave","seconds":6,"resolution":"720p"}`, cred), f)
	if err != nil {
		t.Fatalf("Direct: %v", err)
	}
	if !strings.Contains(string(out.Body), `"status":"completed"`) || !strings.Contains(string(out.Body), "https://cdn.example/one.mp4") {
		t.Fatalf("answer = %s", out.Body)
	}
	joined := f.Sent()
	if !strings.Contains(joined, `"num_frames":97`) || !strings.Contains(joined, `"resolution":"720p"`) {
		t.Fatalf("video body = %s", joined)
	}
	if strings.Contains(string(out.Body), "data:video/mp4;base64") {
		t.Fatal("the video was inlined")
	}
}

func TestACookieWithoutAClientIsRefused(t *testing.T) {
	f, _ := happy()
	_, err := Images{Every: time.Millisecond}.Direct(context.Background(), req("image", "m", `{"prompt":"x"}`, "other=1"), f)
	var pe *media.ProviderError
	if !errors.As(err, &pe) || pe.Status != 401 {
		t.Fatalf("err = %v", err)
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("calls = %v", f.Calls())
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
		if err := media.HTTPSOnly(target); err == nil {
			t.Fatalf("media.HTTPSOnly(%q) was accepted", target)
		}
	}
	if err := media.HTTPSOnly("https://cdn.example/x.png"); err != nil {
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
	if got := claim(mediatest.JWT(map[string]any{"sid": "s"}), "sid"); got != "s" {
		t.Fatalf("claim = %q", got)
	}
	if got := claim("not.a.token", "sid"); got != "" {
		t.Fatalf("claim on garbage = %q", got)
	}
}

func TestTheActiveSessionWinsWhenTheClientNamesNone(t *testing.T) {
	token := mediatest.JWT(map[string]any{"uid": "u"})
	f := mediatest.NewRoutes(map[string]mediatest.Reply{
		"/v1/client": func() (int, http.Header, []byte) {
			return 200, http.Header{}, []byte(`{"sessions":[{"id":"old","status":"ended"},{"id":"live","status":"active"}]}`)
		},
		"/v1/client/sessions/live/tokens": func() (int, http.Header, []byte) {
			return 200, http.Header{}, []byte(`{"jwt":"` + token + `"}`)
		},
	})
	jar, _ := media.ParseCookie("__client=abc")
	s, err := mintSession(context.Background(), f, jar)
	if err != nil {
		t.Fatalf("mintSession: %v", err)
	}
	if s.jwt != token {
		t.Fatalf("jwt = %q", s.jwt)
	}
	for _, call := range f.Calls() {
		if strings.Contains(call, "sessions/old/") {
			t.Fatalf("the ended session was used: %v", f.Calls())
		}
	}
}

func TestARotatedCookieSurvivesAFailedRender(t *testing.T) {
	f, cred := happy()
	// The mint rotates the client cookie, then the create is refused.
	f.Set("/v2/image-gen", mediatest.Of(429, nil))
	out, err := Images{Every: time.Millisecond}.Direct(context.Background(), req("image", "seedream-v4.5", `{"prompt":"a cat"}`, cred), f)
	if err == nil {
		t.Fatal("a refused render was served")
	}
	if !strings.Contains(out.NewCredential, "__client=rotated") {
		t.Fatalf("the rotated cookie was lost with the error: %q", out.NewCredential)
	}
}

func TestAServerErrorIsTheProvidersAndAClientErrorIsTheRequests(t *testing.T) {
	for status, wantBad := range map[int]bool{500: false, 502: false, 503: false, 400: true, 422: true} {
		f, cred := happy()
		f.Set("/v2/image-gen", mediatest.Of(status, nil))
		_, err := Images{Every: time.Millisecond}.Direct(context.Background(), req("image", "seedream-v4.5", `{"prompt":"a cat"}`, cred), f)
		var bad *media.BadRequest
		var pe *media.ProviderError
		switch {
		case wantBad && !errors.As(err, &bad):
			t.Errorf("%d: err = %v, want the request's fault", status, err)
		case !wantBad && (!errors.As(err, &pe) || pe.Status != http.StatusBadGateway):
			t.Errorf("%d: err = %v, want a 502 from the provider", status, err)
		}
	}
}

func TestTheResultIsLookedAtOnTheCadenceTheAdapterWasGiven(t *testing.T) {
	f, cred := happy()
	var heads int
	f.Set("cdn.example/one.png", func() (int, http.Header, []byte) { return 200, http.Header{}, mediatest.PNG })
	started := time.Now()
	if _, err := (Images{Every: time.Millisecond}).Direct(context.Background(), req("image", "seedream-v4.5", `{"prompt":"a cat"}`, cred), f); err != nil {
		t.Fatalf("Direct: %v", err)
	}
	for _, call := range f.Calls() {
		if strings.HasPrefix(call, "HEAD ") {
			heads++
		}
	}
	// The first look comes after one wait. At the provider's own pace that wait
	// is five seconds, so a call this quick used the cadence it was handed.
	if heads == 0 || time.Since(started) > 2*time.Second {
		t.Fatalf("%d looks in %s: the given cadence was not used", heads, time.Since(started))
	}
}
