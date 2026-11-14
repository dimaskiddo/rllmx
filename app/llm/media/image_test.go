package media

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestAPollAddressMustStayOnTheProvidersSite(t *testing.T) {
	for name, tc := range map[string]struct {
		base, target string
		ok           bool
	}{
		"same host":     {"https://api.bfl.ai/v1", "https://api.bfl.ai/poll/1", true},
		"a subdomain":   {"https://api.bfl.ai/v1", "https://queue.api.bfl.ai/poll/1", true},
		"another host":  {"https://api.bfl.ai/v1", "https://evil.example/poll/1", false},
		"a suffix trap": {"https://api.bfl.ai/v1", "https://notapi.bfl.ai.evil.example", false},
		"plain http":    {"https://api.bfl.ai/v1", "http://api.bfl.ai/poll/1", false},
	} {
		if got := SameSite(tc.base, tc.target); got != tc.ok {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// answer is a client that replies with one canned answer.
type answer struct {
	status int
	body   []byte
}

func (a answer) Do(context.Context, string, string, map[string]string, []byte) (int, http.Header, []byte, error) {
	return a.status, http.Header{}, a.body, nil
}

func TestASourceImageThatFailsIsTheClientsInputNotTheAccounts(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	for name, a := range map[string]answer{
		"a foreign 403":  {403, []byte("no")},
		"a foreign 429":  {429, []byte("slow")},
		"a foreign 404":  {404, nil},
		"a server error": {500, nil},
		"not an image":   {200, []byte("<html>")},
		"too large":      {200, append(png, make([]byte, imageCap)...)},
	} {
		_, err := FetchImage(context.Background(), a, "https://example.test/x.png")
		var bad *BadRequest
		if !errors.As(err, &bad) {
			t.Errorf("%s: err = %v, want a BadRequest", name, err)
		}
		var pe *ProviderError
		if errors.As(err, &pe) {
			t.Errorf("%s: a foreign status reached the account's lock rules (%d)", name, pe.Status)
		}
	}
	got, err := FetchImage(context.Background(), answer{200, png}, "https://example.test/x.png")
	if err != nil || !strings.HasPrefix(got, "iVBOR") {
		t.Fatalf("a real image: %q, %v", got, err)
	}
}
