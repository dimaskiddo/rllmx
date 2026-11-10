package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"rllmx/app/cache"
	"rllmx/app/executor"
	"rllmx/tests"
)

// mimoServer answers the two phases: the sign-on redirect, then a chain of
// redirects that sets cookies and ends with the session.
func mimoServer(t *testing.T, phase1 string, hops []string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var step, calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasPrefix(r.URL.Path, "/pass/serviceLogin") {
			step.Store(0)
			if got := r.Header.Get("Cookie"); !strings.Contains(got, "passToken=PT") {
				t.Errorf("phase one carried no pass token: %q", got)
			}
			if !strings.Contains(r.URL.RawQuery, "sid=") {
				t.Errorf("phase one named no cluster: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte("&&&START&&&" + phase1))
			return
		}
		if r.URL.Path == "/sso" && !strings.Contains(r.URL.RawQuery, "clientSign=") {
			t.Errorf("phase two carried no signature: %s", r.URL.RawQuery)
		}
		n := int(step.Add(1)) - 1
		if n < len(hops) {
			http.SetCookie(w, &http.Cookie{Name: hops[n], Value: "v" + hops[n]})
			http.Redirect(w, r, "/next", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// mimoClient is the token client's own policy: every host goes to the fake
// server, and no redirect is followed, because each hop carries its own cookie.
func mimoClient(srv *httptest.Server) *TokenClient {
	c := reroute(srv)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &TokenClient{HTTP: c}
}

func TestMiMoTheTwoPhasesYieldTheSessionCookie(t *testing.T) {
	srv, _ := mimoServer(t, `{"code":0,"location":"https://account.xiaomi.com/sso","nonce":"9007199254740993","ssecurity":"SEC"}`,
		[]string{"userId", "serviceToken", "extra_ph", "other"})
	src := &MimoCookies{Tokens: mimoClient(srv), Cache: cache.New(tests.NewCache())}
	got, err := src.Cookie(context.Background(), "PT", "U", "C", "sgp", false)
	if err != nil {
		t.Fatalf("%v", err)
	}
	for _, want := range []string{"serviceToken=vserviceToken", "userId=vuserId", "extra_ph=vextra_ph"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s missing from %q", want, got)
		}
	}
	if strings.Contains(got, "other=") || strings.Contains(got, "passToken=") {
		t.Errorf("a cookie the service did not name for the session was kept: %q", got)
	}
}

func TestMiMoAPhaseOneRefusalIsTheAccountError(t *testing.T) {
	srv, _ := mimoServer(t, `{"code":1,"location":"https://account.xiaomi.com/sso","nonce":"1","ssecurity":"S"}`, nil)
	src := &MimoCookies{Tokens: mimoClient(srv), Cache: cache.New(tests.NewCache())}
	if _, err := src.Cookie(context.Background(), "PT", "U", "C", "sgp", false); !errors.Is(err, executor.ErrAccountSession) {
		t.Fatalf("%v", err)
	}
}

func TestMiMoAChainWithoutASessionIsTheAccountError(t *testing.T) {
	srv, _ := mimoServer(t, `{"code":0,"location":"https://account.xiaomi.com/sso","nonce":"1","ssecurity":"S"}`, []string{"userId"})
	src := &MimoCookies{Tokens: mimoClient(srv), Cache: cache.New(tests.NewCache())}
	if _, err := src.Cookie(context.Background(), "PT", "U", "C", "sgp", false); !errors.Is(err, executor.ErrAccountSession) {
		t.Fatalf("%v", err)
	}
}

func TestMiMoAnEmptyPassTokenAsksNothing(t *testing.T) {
	src := &MimoCookies{Tokens: &TokenClient{HTTP: http.DefaultClient}}
	if _, err := src.Cookie(context.Background(), "", "", "", "sgp", false); !errors.Is(err, executor.ErrAccountSession) {
		t.Fatalf("%v", err)
	}
}

func TestMiMoACachedSessionAnswersWithoutACall(t *testing.T) {
	srv, calls := mimoServer(t, `{"code":0,"location":"https://account.xiaomi.com/sso","nonce":"1","ssecurity":"S"}`, []string{"serviceToken"})
	src := &MimoCookies{Tokens: mimoClient(srv), Cache: cache.New(tests.NewCache())}
	for range 3 {
		if _, err := src.Cookie(context.Background(), "PT", "U", "C", "sgp", false); err != nil {
			t.Fatalf("%v", err)
		}
	}
	// One sign-on and two hops on the first read, then nothing at all while
	// the cookie is cached.
	if n := calls.Load(); n != 3 {
		t.Fatalf("%d calls for three reads, want one exchange", n)
	}
	if _, err := src.Cookie(context.Background(), "PT", "U", "C", "sgp", true); err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if n := calls.Load(); n != 6 {
		t.Fatalf("%d calls after a fresh read, want the exchange to run again", n)
	}

}
