package captcha

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// verifier points at a local server standing in for the provider.
func verifier(t *testing.T, handler http.HandlerFunc) (*Verifier, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Verifier{Provider: Turnstile, Secret: "the-secret", URL: srv.URL, Client: srv.Client()}, &hits
}

func TestAnAcceptedAnswerSendsTheFormAndPasses(t *testing.T) {
	var secret, response, remote string
	v, hits := verifier(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		secret, response, remote = r.PostFormValue("secret"), r.PostFormValue("response"), r.PostFormValue("remoteip")
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	if err := v.Verify(context.Background(), "token-1", "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	if *hits != 1 || secret != "the-secret" || response != "token-1" || remote != "203.0.113.9" {
		t.Fatalf("hits %d secret %q response %q remote %q", *hits, secret, response, remote)
	}
}

func TestARefusedAnswerIsRejected(t *testing.T) {
	v, _ := verifier(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
	})
	if err := v.Verify(context.Background(), "token", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("err %v", err)
	}
}

func TestAnUnreachableOrErroringProviderIsUnavailable(t *testing.T) {
	v1, _ := verifier(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if err := v1.Verify(context.Background(), "token", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("500: err %v", err)
	}
	v2, _ := verifier(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	})
	if err := v2.Verify(context.Background(), "token", ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("junk: err %v", err)
	}
	dead := &Verifier{Provider: Turnstile, URL: "http://127.0.0.1:1/", Client: &http.Client{}}
	if err := dead.Verify(context.Background(), "token", ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("refused connection: err %v", err)
	}
}

func TestAMissingOrOversizedAnswerNeverLeavesTheProcess(t *testing.T) {
	v, hits := verifier(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	})
	if err := v.Verify(context.Background(), "", ""); !errors.Is(err, ErrMissing) {
		t.Fatalf("empty: err %v", err)
	}
	if err := v.Verify(context.Background(), strings.Repeat("x", maxToken+1), ""); !errors.Is(err, ErrMissing) {
		t.Fatalf("oversized: err %v", err)
	}
	if *hits != 0 {
		t.Fatalf("the provider was asked %d times", *hits)
	}
}

func TestTheLoaderAndItsHostsArePerProvider(t *testing.T) {
	if got := Script(Turnstile); !strings.HasPrefix(got, "https://challenges.cloudflare.com/") || !strings.Contains(got, onload) {
		t.Errorf("turnstile script %q", got)
	}
	if got := Script(Recaptcha); !strings.HasPrefix(got, "https://www.google.com/recaptcha/") || !strings.Contains(got, onload) {
		t.Errorf("recaptcha script %q", got)
	}
	if Script("") != "" {
		t.Error("an unknown provider names a script")
	}
	script, frame := Sources(Recaptcha)
	if len(script) == 0 || len(frame) == 0 || strings.Contains(strings.Join(script, " "), "challenges.cloudflare.com") {
		t.Errorf("recaptcha sources %v %v", script, frame)
	}
	if s, f := Sources("hcaptcha"); s != nil || f != nil {
		t.Errorf("unknown sources %v %v", s, f)
	}
}

func TestThePolicyAddsOnlyTheProviderHosts(t *testing.T) {
	base := "default-src 'self'"
	got := Policy(base, Turnstile)
	if !strings.Contains(got, "script-src 'self' https://challenges.cloudflare.com") ||
		!strings.Contains(got, "frame-src https://challenges.cloudflare.com") {
		t.Fatalf("%q", got)
	}
	if strings.Contains(got, "unsafe") {
		t.Fatalf("the policy opens eval or inline: %q", got)
	}
	if Policy(base, "") != base {
		t.Error("a sign-in with no provider gets another policy")
	}
}
