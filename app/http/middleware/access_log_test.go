package middleware

import (
	"net/http"
	"testing"
)

// A read is routine, so its line belongs at DEBUG; a call that changes state is
// the event the log is kept for.
func TestOnlyAMutatingRequestIsLoggedAtInfo(t *testing.T) {
	for method, want := range map[string]bool{
		http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true,
		http.MethodPost: false, http.MethodPut: false, http.MethodPatch: false, http.MethodDelete: false,
	} {
		if got := quiet(method); got != want {
			t.Errorf("quiet(%s) = %v, want %v", method, got, want)
		}
	}
}
