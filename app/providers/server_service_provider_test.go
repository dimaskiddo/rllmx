package providers

import (
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/goravel/framework/contracts/binding"
	"github.com/goravel/framework/contracts/config"
	consolecontract "github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/foundation"
	frameworkhash "github.com/goravel/framework/contracts/hash"
	"github.com/goravel/framework/contracts/log"
	"github.com/goravel/framework/contracts/route"
	"github.com/goravel/framework/hash"

	"rllmx/app/data"
	"rllmx/tests"
)

// fakeApp implements the few container calls the provider makes. Any other
// method panics through the nil embedded interface. It models the framework's
// resolved-instance cache, because that cache is what makes Instance alone
// insufficient: a cached value outlives a new binding until Fresh clears it.
type fakeApp struct {
	foundation.Application
	bindings map[any]func(foundation.Application) (any, error)
	values   map[any]any
	cfg      config.Config
	route    route.Route
	log      log.Log
}

func (f *fakeApp) Singleton(key any, cb func(foundation.Application) (any, error)) {
	if f.bindings == nil {
		f.bindings = map[any]func(foundation.Application) (any, error){}
	}
	f.bindings[key] = cb
}

func (f *fakeApp) Make(key any) (any, error) {
	if v, ok := f.values[key]; ok {
		return v, nil
	}
	if cb, ok := f.bindings[key]; ok {
		v, err := cb(f)
		if err != nil {
			return nil, err
		}
		if f.values == nil {
			f.values = map[any]any{}
		}
		f.values[key] = v
		return v, nil
	}
	return nil, errors.New("not bound")
}

func (f *fakeApp) Instance(key, instance any) {
	f.Singleton(key, func(foundation.Application) (any, error) { return instance, nil })
}

func (f *fakeApp) Fresh(keys ...any) {
	for _, key := range keys {
		delete(f.values, key)
	}
}

func (f *fakeApp) MakeConfig() config.Config { return f.cfg }
func (f *fakeApp) MakeRoute() route.Route    { return f.route }
func (f *fakeApp) MakeLog() log.Log          { return f.log }

// MakeHash returns the framework's bcrypt hasher at a low cost, so a provider
// test does not spend a real bcrypt round.
func (f *fakeApp) MakeHash() frameworkhash.Hash {
	return hash.NewBcrypt(tests.Config{Values: map[string]any{"hashing.bcrypt.rounds": 4}})
}

func (f *fakeApp) MakeArtisan() consolecontract.Artisan {
	v, err := f.Make(binding.Artisan)
	if err != nil {
		return nil
	}
	a, _ := v.(consolecontract.Artisan)
	return a
}

// consoleApp is a container whose console provider has already bound the
// framework's Artisan, which is the state the commands provider sees.
func consoleApp(inner consolecontract.Artisan) *fakeApp {
	app := &fakeApp{}
	app.Singleton(binding.Artisan, func(foundation.Application) (any, error) { return inner, nil })
	return app
}

func resolve(t *testing.T, cfg config.Config) (any, error) {
	t.Helper()
	app := &fakeApp{cfg: cfg}
	(&ServerServiceProvider{}).Register(app)
	cb := app.bindings[BindingHTTP]
	if cb == nil {
		t.Fatal("Register bound nothing under " + BindingHTTP)
	}
	return cb(app)
}

func TestTheProviderDependsOnWhatItReads(t *testing.T) {
	rel := (&ServerServiceProvider{}).Relationship()
	for _, want := range []string{binding.Config, binding.Log, binding.Route} {
		if !slices.Contains(rel.Dependencies, want) {
			t.Errorf("%s is read by the provider but is not a dependency: %v", want, rel.Dependencies)
		}
	}
	if len(rel.Bindings) != 1 || rel.Bindings[0] != BindingHTTP {
		t.Fatalf("bindings = %v", rel.Bindings)
	}
}

func TestRegisterBindsTheSettingsAndTheGuardTogether(t *testing.T) {
	values := tests.ServerValues()
	values["http.guard.trusted_proxies"] = "10.0.0.0/8"
	v, err := resolve(t, tests.Config{Values: values})
	if err != nil {
		t.Fatal(err)
	}
	h, ok := v.(*HTTP)
	if !ok || h.Settings == nil || h.Guard == nil {
		t.Fatalf("binding = %#v", v)
	}
	if h.Settings.Port != 20128 || !h.Settings.TrustLoopback {
		t.Fatalf("settings = %+v", h.Settings)
	}
	if h.Stream == nil || h.Stream.UsageWait.Seconds() != 3 || h.Stream.Stall.Seconds() != 360 || h.Stream.FirstChunk.Seconds() != 200 {
		t.Fatalf("stream = %+v", h.Stream)
	}
	if h.Registry == nil {
		t.Fatal("the provider registry is not bound: the routes would reach a nil registry")
	}
	if _, found := h.Registry.ByID("anthropic"); !found {
		t.Fatal("the bound registry is empty")
	}
	if h.Capabilities == nil || !h.Capabilities.For("anthropic", "claude-opus-5").Reasoning {
		t.Fatal("the capability tables are not bound, or do not resolve a seed model")
	}
}

func TestRegisterBelievesTheConfiguredProxies(t *testing.T) {
	values := tests.ServerValues()
	values["http.guard.trusted_proxies"] = "10.0.0.0/8"
	v, err := resolve(t, tests.Config{Values: values})
	if err != nil {
		t.Fatal(err)
	}
	h := v.(*HTTP)
	req := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.2.3:5000"
	req.Header.Set("X-Forwarded-Proto", "https")
	if !h.Trust.SecureRequest(req) {
		t.Fatal("a configured proxy is not believed: SERVER_TRUSTED_PROXIES is still inert")
	}
}

func TestRegisterFailsWithoutAConfigFacade(t *testing.T) {
	if v, err := resolve(t, nil); err == nil || v != nil {
		t.Fatalf("binding %#v error %v", v, err)
	}
}

func TestRegisterRefusesInvalidSettings(t *testing.T) {
	values := tests.ServerValues()
	values["http.port"] = "0"
	if v, err := resolve(t, tests.Config{Values: values}); err == nil || v != nil {
		t.Fatalf("binding %#v error %v", v, err)
	}
}

func TestBootDoesNothingThatACommandCouldNotDo(t *testing.T) {
	// A nil container panics on any call, so this fails if Boot touches it.
	(&ServerServiceProvider{}).Boot(&fakeApp{})
}

func TestRunnersReturnsTheServerRunner(t *testing.T) {
	bound, err := resolve(t, tests.Config{Values: tests.ServerValues()})
	if err != nil {
		t.Fatal(err)
	}
	app := &fakeApp{values: map[any]any{BindingHTTP: bound, BindingData: &Data{Gate: data.NewGate()}}, route: tests.NewRoute(t), log: &tests.Log{}}
	runners := (&ServerServiceProvider{}).Runners(app)
	if len(runners) != 1 || runners[0].Signature() != "rllmx:http" || !runners[0].ShouldRun() {
		t.Fatalf("runners = %v", runners)
	}
	withPriority, ok := runners[0].(foundation.RunnerWithShutdownPriority)
	if !ok || withPriority.ShutdownPriority() >= 0 {
		t.Fatal("the server must declare a shutdown priority ahead of the default")
	}
}

func TestRunnersPanicsOnAMissingBinding(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a missing binding must stop the start loudly, not leave the app with no server")
		}
	}()
	(&ServerServiceProvider{}).Runners(&fakeApp{})
}

func TestHTTPOfReturnsTheBinding(t *testing.T) {
	want := &HTTP{}
	if got := HTTPOf(&fakeApp{values: map[any]any{BindingHTTP: want}}); got != want {
		t.Fatal("not the bound value")
	}
}
