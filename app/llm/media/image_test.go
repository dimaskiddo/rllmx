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
		_, err := FetchImage(context.Background(), a, "https://93.184.216.34/x.png")
		var bad *BadRequest
		if !errors.As(err, &bad) {
			t.Errorf("%s: err = %v, want a BadRequest", name, err)
		}
		var pe *ProviderError
		if errors.As(err, &pe) {
			t.Errorf("%s: a foreign status reached the account's lock rules (%d)", name, pe.Status)
		}
	}
	got, err := FetchImage(context.Background(), answer{200, png}, "https://93.184.216.34/x.png")
	if err != nil || !strings.HasPrefix(got, "iVBOR") {
		t.Fatalf("a real image: %q, %v", got, err)
	}
}

// counting is a client that notes every call, so a refused address is shown to
// have reached nothing.
type counting struct{ calls int }

func (c *counting) Do(context.Context, string, string, map[string]string, []byte) (int, http.Header, []byte, error) {
	c.calls++
	return 200, http.Header{}, append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...), nil
}

// A source image is named by the caller, so the client the operator gave a
// private allowance must never be pointed at a private address with it.
func TestASourceImageNamingAPrivateAddressReachesNothing(t *testing.T) {
	for name, target := range map[string]string{
		"a private literal":    "http://10.0.0.5/admin.png",
		"loopback":             "http://127.0.0.1:8080/x.png",
		"link-local metadata":  "http://169.254.169.254/latest/meta-data",
		"a name for loopback":  "http://localhost/x.png",
		"credentials in a URL": "https://user:pw@93.184.216.34/x.png",
		"not http":             "file:///etc/passwd",
	} {
		c := &counting{}
		_, err := InlineImage(context.Background(), c, target)
		var bad *BadRequest
		if !errors.As(err, &bad) {
			t.Errorf("%s: err = %v, want a BadRequest", name, err)
		}
		if c.calls != 0 {
			t.Errorf("%s: the client was called %d times", name, c.calls)
		}
	}
}
