package tests

import "net/http"

// Redirect runs f on every request before sending it, so a test can point a
// provider's fixed address at its own server.
type Redirect func(*http.Request)

// RoundTrip calls f, then sends the request unchanged.
func (f Redirect) RoundTrip(r *http.Request) (*http.Response, error) {
	f(r)
	return http.DefaultTransport.RoundTrip(r)
}
