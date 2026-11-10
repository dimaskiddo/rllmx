package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"rllmx/app/upstream"
)

// qoderServer answers the sign-in address's landing page and the poll.
func qoderServer(t *testing.T, poll func(n int, r *http.Request) (int, string)) (*httptest.Server, *[]url.Values) {
	t.Helper()
	var seen []url.Values
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Query())
		if r.URL.Path != "/poll" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n++
		status, body := poll(n, r)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func qoderFlow(srv *httptest.Server) Flow {
	return Flow{
		Provider: "qoder",
		OAuth: upstream.OAuth{
			Flow: upstream.FlowDevice, TokenURL: srv.URL + "/poll",
			Params: []upstream.Param{{Name: "loginUrl", Value: srv.URL + "/device"}},
		},
	}
}

func TestQoderStartsWithAChallengeAndAMachine(t *testing.T) {
	srv, _ := qoderServer(t, func(int, *http.Request) (int, string) { return http.StatusAccepted, "" })
	g, err := qoderStart(context.Background(), &TokenClient{}, qoderFlow(srv), nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	u, err := url.Parse(g.VerificationURI)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"challenge", "machine_id", "nonce"} {
		if u.Query().Get(key) == "" {
			t.Errorf("the sign-in address carries no %s: %s", key, g.VerificationURI)
		}
	}
	if u.Query().Get("challenge_method") != "S256" {
		t.Errorf("method %q", u.Query().Get("challenge_method"))
	}
	if g.Verifier == "" || g.Interval != qoderInterval || g.ExpiresIn != qoderLoginLife {
		t.Errorf("%+v", g)
	}
}

func TestQoderThePollWaitsThenAnswers(t *testing.T) {
	srv, seen := qoderServer(t, func(n int, _ *http.Request) (int, string) {
		switch n {
		case 1:
			return http.StatusAccepted, ""
		case 2:
			return http.StatusNotFound, ""
		}
		return http.StatusOK, `{"token":"DT-1","user_id":"u1","email":"a@b.test"}`
	})
	c := &TokenClient{HTTP: srv.Client()}
	f := qoderFlow(srv)
	g, err := qoderStart(context.Background(), c, f, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	state := deviceState{Code: g.DeviceCode, Verifier: g.Verifier, Extra: g.Extra}
	for i, want := range []pollState{pollPending, pollPending, pollDone} {
		got, state2, err := qoderPoll(context.Background(), c, f, state, nil)
		if err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		if state2 != want {
			t.Fatalf("%d: %v, want %v", i, state2, want)
		}
		if want == pollDone {
			if got.Access != "DT-1" || got.UserID != "u1" || got.Email != "a@b.test" {
				t.Errorf("tokens %+v", got)
			}
			if !strings.Contains(got.PSD, g.Extra) {
				t.Errorf("the machine id was not carried: %s", got.PSD)
			}
		}
	}
	// The poll proves the challenge the start made.
	if (*seen)[0].Get("verifier") != g.Verifier {
		t.Errorf("the poll sent verifier %q, want the login's", (*seen)[0].Get("verifier"))
	}
}

func TestQoderAnAnswerWithoutATokenWaits(t *testing.T) {
	srv, _ := qoderServer(t, func(int, *http.Request) (int, string) { return http.StatusOK, `{}` })
	c := &TokenClient{HTTP: srv.Client()}
	f := qoderFlow(srv)
	g, err := qoderStart(context.Background(), c, f, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if _, state, err := qoderPoll(context.Background(), c, f, deviceState{Code: g.DeviceCode, Verifier: g.Verifier}, nil); err != nil || state != pollPending {
		t.Fatalf("%v %v", state, err)
	}
}

func TestQoderADeviceCodeItDidNotMakeIsRefused(t *testing.T) {
	srv, _ := qoderServer(t, func(int, *http.Request) (int, string) { return http.StatusAccepted, "" })
	c := &TokenClient{HTTP: srv.Client()}
	f := qoderFlow(srv)
	for _, code := range []string{"", "nonsense", "nonce\x00"} {
		if _, _, err := qoderPoll(context.Background(), c, f, deviceState{Code: code}, nil); err == nil {
			t.Errorf("%q was accepted", code)
		}
	}
}

func TestQoderTheAccountCarriesWhoItIs(t *testing.T) {
	srv, _ := qoderServer(t, func(int, *http.Request) (int, string) { return http.StatusOK, "" })
	f := Flow{OAuth: upstream.OAuth{Endpoints: map[string]string{upstream.EndpointUser: srv.URL}}}
	c := &TokenClient{HTTP: srv.Client()}
	acc, err := qoderAccount(context.Background(), c, f, Tokens{Access: "DT", UserID: "u1", PSD: `{"machineId":"m1"}`}, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !acc.NoRefresh {
		t.Error("a gateway login was marked renewable")
	}
	if !strings.Contains(acc.PSD, `"userId":"u1"`) || !strings.Contains(acc.PSD, `"machineId":"m1"`) {
		t.Errorf("provider data %s", acc.PSD)
	}
}

func TestQoderAnUnreachableProfileStillConnects(t *testing.T) {
	c := &TokenClient{HTTP: http.DefaultClient}
	acc, err := qoderAccount(context.Background(), c, Flow{OAuth: upstream.OAuth{Endpoints: map[string]string{upstream.EndpointUser: "https://127.0.0.1:1"}}},
		Tokens{Access: "DT", UserID: "u1", PSD: `{}`}, nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(acc.PSD, `"userId":"u1"`) {
		t.Errorf("provider data %s", acc.PSD)
	}
}
