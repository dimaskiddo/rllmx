package middleware

import (
	"testing"

	"github.com/goravel/framework/foundation/configuration"

	apphttp "rllmx/app/http"
	"rllmx/tests"
)

func TestStackReplacesTheStockMiddlewareInOrder(t *testing.T) {
	stock := tests.NewRoute(t).GetGlobalMiddleware()
	if len(stock) == 0 {
		t.Fatal("the driver has no stock middleware to replace")
	}
	m := configuration.NewMiddleware(stock)
	Stack(m, Options{Guard: apphttp.NewGuard(nil, true), Log: &tests.Log{}, MaxBody: 128 << 20})

	var got []string
	for _, mw := range m.GetGlobalMiddleware() {
		got = append(got, mw.Signature())
	}
	want := []string{
		"RequestId", "AccessLog", "SecurityHeaders", "CORS",
		"Deadlines", "BodyLimit", "SameOrigin", "Guard",
	}
	if len(got) != len(want) {
		t.Fatalf("stack = %v, want %v: the timeout, CORS, TLS and maintenance middleware must be gone", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stack = %v, want %v", got, want)
		}
	}
	if m.GetRecover() == nil {
		t.Fatal("no recover handler")
	}
}
