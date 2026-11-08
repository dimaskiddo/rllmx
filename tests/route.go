package tests

import (
	"testing"

	"github.com/goravel/framework/contracts/config"
	goravelgin "github.com/goravel/gin"
)

// routeConfig answers the reads the driver makes while it builds a Route, with
// every value unset.
type routeConfig struct{ config.Config }

func (routeConfig) Get(string, ...any) any        { return nil }
func (routeConfig) GetBool(string, ...bool) bool  { return false }
func (routeConfig) GetInt(_ string, d ...int) int { return 0 }

// NewRoute returns a real driver Route that is not tied to the application.
// Its stock middleware reads facades that do not exist here, so a test calls
// SetGlobalMiddleware (and Recover, if it needs one) before declaring routes:
// the driver rebuilds its engine on both and forgets earlier routes.
func NewRoute(t testing.TB) *goravelgin.Route {
	t.Helper()
	// The driver logs a malformed JSON body through this package variable, which
	// the application sets and a test never does.
	if goravelgin.LogFacade == nil {
		goravelgin.LogFacade = &Log{}
	}
	r, err := goravelgin.NewRoute(routeConfig{}, map[string]any{"driver": "gin"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
