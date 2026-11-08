package network

import "testing"

func TestIsLoopbackHost(t *testing.T) {
	yes := []string{"localhost", "LOCALHOST", "localhost.", "127.0.0.1", "127.9.9.9", "::1", "[::1]", "::ffff:127.0.0.1", "[::ffff:127.0.0.1]"}
	no := []string{
		"example.com", "localhost.evil.com", "0.0.0.0", "10.0.0.1", "", "127.0.0.1.evil.com", "::", "1.2.3.4",
		"fe80::1%eth0", "::1%eth0", "[::1%25eth0]", "::ffff:10.0.0.1", "[[::1]", "[::1", "::1]", "localhost%00",
	}
	for _, h := range yes {
		if !IsLoopbackHost(h) {
			t.Errorf("%q should be this machine", h)
		}
	}
	for _, h := range no {
		if IsLoopbackHost(h) {
			t.Errorf("%q should not be this machine", h)
		}
	}
}
