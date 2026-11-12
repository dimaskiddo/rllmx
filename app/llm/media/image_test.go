package media

import "testing"

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
