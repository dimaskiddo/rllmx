package executor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"rllmx/app/upstream"
	"rllmx/tests"
)

type fakeTokens struct {
	token string
	err   error
	seen  []upstream.GoogleKey
}

func (f *fakeTokens) Token(_ context.Context, k upstream.GoogleKey) (string, error) {
	f.seen = append(f.seen, k)
	return f.token, f.err
}

const (
	saText  = `{"type":"service_account","project_id":"sa-proj-111","client_email":"a@b.c","private_key":"K"}`
	adcText = `{"type":"authorized_user","client_id":"i","client_secret":"s","refresh_token":"r","quota_project_id":"adc-proj-22"}`
)

func TestTheVertexAddressFollowsTheKeyKindTheProjectAndTheLocation(t *testing.T) {
	for name, tc := range map[string]struct {
		key, project, location, model, spec string
		stream                              bool
		partner                             bool
		want                                string
	}{
		"service account, project from the file": {saText, "", "", "gemini-3-pro-preview", "", false, false,
			"https://aiplatform.googleapis.com/v1/projects/sa-proj-111/locations/us-central1/publishers/google/models/gemini-3-pro-preview:generateContent"},
		"account project wins": {saText, "acct-proj-9", "", "m", "", true, false,
			"https://aiplatform.googleapis.com/v1/projects/acct-proj-9/locations/us-central1/publishers/google/models/m:streamGenerateContent?alt=sse"},
		"adc quota project": {adcText, "", "europe-west4", "m", "", false, false,
			"https://aiplatform.googleapis.com/v1/projects/adc-proj-22/locations/europe-west4/publishers/google/models/m:generateContent"},
		"model default location": {saText, "", "", "gemini-4-argon", "global", false, false,
			"https://aiplatform.googleapis.com/v1/projects/sa-proj-111/locations/global/publishers/google/models/gemini-4-argon:generateContent"},
		"raw key without a project": {"AIzaKey", "", "", "m", "", false, false,
			"https://aiplatform.googleapis.com/v1/publishers/google/models/m:generateContent"},
		"raw key with a project goes global": {"AIzaKey", "my-proj-123", "", "m", "", true, false,
			"https://aiplatform.googleapis.com/v1/projects/my-proj-123/locations/global/publishers/google/models/m:streamGenerateContent?alt=sse"},
		"a bad location falls back": {saText, "", "../x", "m", "", false, false,
			"https://aiplatform.googleapis.com/v1/projects/sa-proj-111/locations/us-central1/publishers/google/models/m:generateContent"},
		"partner": {saText, "", "", "deepseek-ai/deepseek-v3.2-maas", "", false, true,
			"https://aiplatform.googleapis.com/v1/projects/sa-proj-111/locations/global/endpoints/openapi/chat/completions"},
	} {
		v := &Vertex{Partner: tc.partner}
		key, ok := upstream.ParseGoogleKey(tc.key)
		if !ok {
			t.Fatalf("%s: key", name)
		}
		req := Request{Model: tc.model, Stream: tc.stream, Spec: upstream.Model{DefaultLocation: tc.spec},
			Account: acct(map[string]string{"projectId": tc.project, "location": tc.location})}
		project, loc := v.place(req, key)
		got, err := v.address(req, key, project, loc)
		if err != nil || got != tc.want {
			t.Errorf("%s:\n got %s %v\nwant %s", name, got, err, tc.want)
		}
	}
}

func TestAKeyThatNeedsAProjectAndHasNoneSendsNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		key     string
		partner bool
	}{"adc, no project": {`{"type":"authorized_user","client_id":"i","client_secret":"s","refresh_token":"r"}`, false}, "partner, raw key, no project": {"AIzaKey", true}} {
		up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
		v := &Vertex{Default: &Default{Client: http.DefaultClient}, Tokens: &fakeTokens{token: "T"}, Partner: tc.partner}
		_, err := v.Execute(context.Background(), Request{URL: up.URL, Key: tc.key, Model: "m", Account: acct(nil)})
		if !errors.Is(err, ErrBadAccount) || len(up.Calls()) != 0 {
			t.Errorf("%s: %v, %d calls", name, err, len(up.Calls()))
		}
	}
}

func TestARawVertexKeyGoesInAHeaderNeverInTheAddress(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	rt := roundTripper(func(r *http.Request) { r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(up.URL, "http://") })
	v := &Vertex{Default: &Default{Client: &http.Client{Transport: rt}}}
	resp, err := v.Execute(context.Background(), Request{Key: " AIzaSECRET ", Model: "m", Body: []byte(`{}`), Account: acct(nil)})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	c := up.Calls()[0]
	if c.Header.Get("X-Goog-Api-Key") != "AIzaSECRET" || strings.Contains(c.Path+"?"+c.Query, "AIza") || c.Header.Get("Authorization") != "" {
		t.Fatalf("%+v", c)
	}
}

func TestATokenKeySendsTheMintedBearerAndNotTheKeyText(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	rt := roundTripper(func(r *http.Request) { r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(up.URL, "http://") })
	tok := &fakeTokens{token: "MINTED"}
	v := &Vertex{Default: &Default{Client: &http.Client{Transport: rt}}, Tokens: tok}
	resp, err := v.Execute(context.Background(), Request{Key: saText, Model: "m", Body: []byte(`{}`), Account: acct(nil)})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	c := up.Calls()[0]
	if c.Header.Get("Authorization") != "Bearer MINTED" || strings.Contains(string(c.Body), "private_key") || len(tok.seen) != 1 || tok.seen[0].Kind != upstream.KeyServiceAccount {
		t.Fatalf("%+v %v", c.Header, tok.seen)
	}
}

func TestAFailedTokenIsABadAccountOrUnreachableAndSendsNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want error
	}{"refused": {errors.New("refused"), ErrBadAccount}, "unreachable": {ErrTokenUnreachable, ErrUnreachable}} {
		up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
		v := &Vertex{Default: &Default{Client: http.DefaultClient}, Tokens: &fakeTokens{err: tc.err}}
		_, err := v.Execute(context.Background(), Request{URL: up.URL, Key: saText, Model: "m", Account: acct(nil)})
		if !errors.Is(err, tc.want) || len(up.Calls()) != 0 {
			t.Errorf("%s: %v, %d calls", name, err, len(up.Calls()))
		}
	}
}

func TestAMalformedOrMissingKeySendsNothing(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	v := &Vertex{Default: &Default{Client: http.DefaultClient}}
	for name, key := range map[string]string{"half a service account": `{"type":"service_account","project_id":"sa-proj-111"}`, "broken json": `{"type":`} {
		if _, err := v.Execute(context.Background(), Request{Key: key, Model: "m", Account: acct(nil)}); !errors.Is(err, ErrBadAccount) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := v.Execute(context.Background(), Request{Model: "m"}); !errors.Is(err, ErrNoCredential) || len(up.Calls()) != 0 {
		t.Fatalf("%v", err)
	}
}

type roundTripper func(*http.Request)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	f(r)
	return http.DefaultTransport.RoundTrip(r)
}
