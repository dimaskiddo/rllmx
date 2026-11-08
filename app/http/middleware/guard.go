package middleware

import (
	contractshttp "github.com/goravel/framework/contracts/http"

	apphttp "rllmx/app/http"
)

// Guard is the adapter that installs the access guard as global middleware.
// The policy and the route table live in app/http.
type Guard struct {
	Guard *apphttp.Guard
}

// Signature is how the stack names this middleware.
func (m *Guard) Signature() string {
	return "Guard"
}

// Handle refuses an unregistered route and asks the route's class to decide.
func (m *Guard) Handle(ctx contractshttp.Context) {
	m.Guard.Protect(ctx)
}
