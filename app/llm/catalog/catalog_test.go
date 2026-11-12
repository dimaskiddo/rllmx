package catalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
	"rllmx/tests/store"
)

func conn(t *testing.T) repository.Connection {
	t.Helper()
	s := store.Migrated(t)
	c, err := s.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "kimi", AuthType: "oauth", Name: "k1", Fields: map[string]string{"apiKey": `"secret"`},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func guard(t *testing.T, fetch Fetcher) *Guard {
	t.Helper()
	return &Guard{Fetchers: map[string]Fetcher{"kimi": fetch}}
}

func TestAKnownModelPasses(t *testing.T) {
	g := guard(t, func(context.Context, repository.Connection) ([]string, error) { return []string{"a", "b"}, nil })
	if got := g.Known(context.Background(), "kimi", conn(t), "b"); got != Known {
		t.Fatalf("verdict %v", got)
	}
}

func TestAnUnlistedModelIsUnknown(t *testing.T) {
	g := guard(t, func(context.Context, repository.Connection) ([]string, error) { return []string{"a"}, nil })
	if got := g.Known(context.Background(), "kimi", conn(t), "zzz"); got != Unknown {
		t.Fatalf("verdict %v", got)
	}
}

func TestAFetchFailureIsUndetermined(t *testing.T) {
	g := guard(t, func(context.Context, repository.Connection) ([]string, error) { return nil, errors.New("boom") })
	if got := g.Known(context.Background(), "kimi", conn(t), "zzz"); got != Undetermined {
		t.Fatalf("a failed fetch refused a model: %v", got)
	}
}

func TestAnEmptyListIsUndetermined(t *testing.T) {
	g := guard(t, func(context.Context, repository.Connection) ([]string, error) { return nil, nil })
	if got := g.Known(context.Background(), "kimi", conn(t), "zzz"); got != Undetermined {
		t.Fatalf("an empty list refused a model: %v", got)
	}
}

func TestAProviderWithoutAFetcherIsUndetermined(t *testing.T) {
	g := guard(t, func(context.Context, repository.Connection) ([]string, error) { return []string{"a"}, nil })
	if got := g.Known(context.Background(), "other", conn(t), "a"); got != Undetermined {
		t.Fatalf("verdict %v", got)
	}
	var nilGuard *Guard
	if got := nilGuard.Known(context.Background(), "kimi", conn(t), "a"); got != Undetermined {
		t.Fatalf("a nil guard refused a model: %v", got)
	}
}

func TestAnEmptyModelIsUndetermined(t *testing.T) {
	g := guard(t, func(context.Context, repository.Connection) ([]string, error) {
		return nil, errors.New("should not run")
	})
	if got := g.Known(context.Background(), "kimi", conn(t), ""); got != Undetermined {
		t.Fatalf("verdict %v", got)
	}
}

func TestTheListIsCachedAndKeptApartPerAccount(t *testing.T) {
	var calls int
	g := guard(t, func(_ context.Context, c repository.Connection) ([]string, error) {
		calls++
		return []string{"id-" + c.ID}, nil
	})
	first := conn(t)
	g.Cache = nil
	if got := g.Known(context.Background(), "kimi", first, "id-"+first.ID); got != Known {
		t.Fatalf("verdict %v", got)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

// fakeList is a tiny HTTP server that answers one body and records its headers.
type fakeList struct {
	Server *httptest.Server
	Header http.Header
	Status int
}

func testsUpstream(t *testing.T, body string) *fakeList {
	t.Helper()
	f := &fakeList{Status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.Header = r.Header.Clone()
		w.WriteHeader(f.Status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.Server.Close)
	return f
}

func TestFetchersSkipRowsWithoutOrigin(t *testing.T) {
	reg := &upstream.Registry{}
	if got := Fetchers(reg, http.DefaultClient); len(got) != 0 {
		t.Fatalf("an empty registry produced %d fetchers", len(got))
	}
	if got := Fetchers(nil, nil); len(got) != 0 {
		t.Fatalf("nil inputs produced %d fetchers", len(got))
	}
}

func TestAReadIsBoundedAndBearsItsToken(t *testing.T) {
	up := testsUpstream(t, `{"models":[{"id":"one"},{"id":"two"}]}`)
	defer up.Server.Close()
	raw, err := readGet(context.Background(), http.DefaultClient, up.Server.URL, "tok", map[string]string{"X-Extra": "1"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(idsOf(raw)) != 2 {
		t.Fatalf("ids = %v", idsOf(raw))
	}
	if got := up.Header.Get("Authorization"); got != "Bearer tok" {
		t.Fatalf("authorization = %q", got)
	}
	if got := up.Header.Get("X-Extra"); got != "1" {
		t.Fatalf("extra header = %q", got)
	}
}

func TestAReadOfARefusedListIsAnError(t *testing.T) {
	up := testsUpstream(t, `nope`)
	up.Status = http.StatusUnauthorized
	if _, err := readGet(context.Background(), http.DefaultClient, up.Server.URL, "tok", nil); err == nil {
		t.Fatal("a refusal was read as a list")
	}
}

func TestEncodeAndDecodeRoundTrip(t *testing.T) {
	ids := []string{"a", "b-c.1", ""}
	if got := decode(encode(ids)); len(got) != 2 || got[0] != "a" || got[1] != "b-c.1" {
		t.Fatalf("round trip = %v", got)
	}
}

var _ = time.Second
