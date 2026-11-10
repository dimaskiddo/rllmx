package executor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"rllmx/tests"
)

// mimoClient sends every address to the fake server, so the account route's
// real host is never dialled.
func mimoClient(up *tests.Upstream) *http.Client {
	return &http.Client{Transport: roundTripper(func(r *http.Request) {
		r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(up.URL, "http://")
	})}
}

// mimoCookies is a scripted CookieSource: each call returns the next cookie,
// or the next error.
type mimoCookies struct {
	cookies []string
	errs    []error
	fresh   []bool
}

func (m *mimoCookies) Cookie(_ context.Context, _, _, _, _ string, fresh bool) (string, error) {
	m.fresh = append(m.fresh, fresh)
	cookie := ""
	if len(m.cookies) > 0 {
		cookie, m.cookies = m.cookies[0], m.cookies[1:]
	}
	if len(m.errs) > 0 && m.errs[0] != nil {
		err := m.errs[0]
		m.errs = m.errs[1:]
		return "", err
	}
	return cookie, nil
}

func TestMimoTakesTheAccountRouteOnlyForItsOwnIdsWithAPassToken(t *testing.T) {
	for name, tc := range map[string]struct {
		model, pass string
		want        bool
	}{
		"its own id with a pass token": {"mimo-v2.6-pro", "pt", true},
		"a provider qualified id":      {"xiaomi/mimo-v2.6-pro", "pt", true},
		"its own id without one":       {"mimo-v2.6-pro", "", false},
		"another id with one":          {"mimo-v2.5-pro", "pt", false},
		"a cloud id":                   {"mimo-v2-omni", "pt", false},
	} {
		got := mimoAccountRoute(Request{Model: tc.model, Account: acct(map[string]string{"mimoPassToken": tc.pass})})
		if got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

func TestMimoBodyMovesTheEffortAndFillsTheSamplingDefaults(t *testing.T) {
	got := string(mimoAccountBody([]byte(`{"model":"m","reasoning_effort":"xhigh","temperature":0.2}`)))
	for _, want := range []string{`"output_config":{"effort":"high"}`, `"temperature":0.2`, `"top_p":0.95`} {
		if !strings.Contains(got, want) {
			t.Errorf("%s missing from %s", want, got)
		}
	}
	if strings.Contains(got, `"reasoning_effort"`) {
		t.Errorf("the effort field survived: %s", got)
	}
}

func TestMimoAFailedSessionSendsNothing(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	x := &XiaomiMimo{Default: &Default{Client: mimoClient(up)}, Cookies: &mimoCookies{errs: []error{ErrAccountSession}}}
	_, err := x.Execute(context.Background(), Request{
		URL: up.URL, Model: "mimo-v2.6-pro", Body: []byte(`{}`),
		Account: acct(map[string]string{"mimoPassToken": "pt"}),
	})
	if !errors.Is(err, ErrAccountSession) || len(up.Calls()) != 0 {
		t.Fatalf("err %v after %d calls", err, len(up.Calls()))
	}
}

func TestMimoARejectedCookieIsRefreshedOnce(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Status: 401}, tests.Reply{Body: `{}`})
	src := &mimoCookies{cookies: []string{"a", "b"}}
	x := &XiaomiMimo{Default: &Default{Client: mimoClient(up)}, Cookies: src}
	resp, err := x.Execute(context.Background(), Request{
		URL: up.URL, Model: "mimo-v2.6-pro", Body: []byte(`{}`),
		Account: acct(map[string]string{"mimoPassToken": "pt"}),
	})
	if err != nil {
		t.Fatalf("%v", err)
	}
	resp.Body.Close()
	if len(src.fresh) != 2 || src.fresh[1] != true {
		t.Fatalf("fresh flags %v, want the second call to force one", src.fresh)
	}
	if got := up.Calls()[1].Header.Get("Cookie"); got != "b" {
		t.Errorf("the second call carried %q", got)
	}
	if got := up.Calls()[1].Header.Get("Authorization"); got != "" {
		t.Errorf("the account route sent a bearer: %q", got)
	}
}

func TestMimoWithoutACookieSourceStaysOnTheCloudAPI(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	x := &XiaomiMimo{Default: &Default{Client: up.Server.Client()}}
	req := Request{URL: up.URL, Model: "mimo-v2.6-pro", Key: "sk", Body: []byte(`{}`),
		Auth: bearerAuth(), Account: acct(map[string]string{"mimoPassToken": "pt"})}
	resp, err := x.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("%v", err)
	}
	resp.Body.Close()
	if got := up.Calls()[0].Header.Get("Authorization"); got != "Bearer sk" {
		t.Errorf("authorization %q", got)
	}
}
