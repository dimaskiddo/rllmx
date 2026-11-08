package providers

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/goravel/framework/contracts/binding"
	"github.com/goravel/framework/contracts/config"
	"github.com/goravel/framework/contracts/foundation"
	"github.com/goravel/framework/contracts/log"
	"github.com/goravel/framework/contracts/schedule"

	"rllmx/app/data"
	"rllmx/tests"
)

type dataApp struct {
	fakeApp
	ctx context.Context
}

func (a *dataApp) Context() context.Context { return a.ctx }

func dataValues(over map[string]any) tests.Config {
	m := map[string]any{
		"database.default":                     "sqlite",
		"database.connections.sqlite.database": "/nonexistent/db/data.sqlite",
		"database.encryption_secret":           tests.DBSecret,
		"database.analytics.retention_days":    90,
		"app.data_dir":                         "/nonexistent",
	}
	for k, v := range over {
		m[k] = v
	}
	return tests.Config{Values: m}
}

func resolveData(t *testing.T, cfg config.Config) (any, error) {
	t.Helper()
	app := &dataApp{fakeApp: fakeApp{cfg: cfg, log: &tests.Log{}}, ctx: context.Background()}
	(&DataServiceProvider{Version: "t"}).Register(app)
	cb := app.bindings[BindingData]
	if cb == nil {
		t.Fatal("Register bound nothing under " + BindingData)
	}
	return cb(app)
}

func TestTheDataProviderDeclaresWhatItReads(t *testing.T) {
	rel := (&DataServiceProvider{}).Relationship()
	for _, want := range []string{binding.Config, binding.Log, binding.DB, binding.Schema, binding.Schedule} {
		if !slices.Contains(rel.Dependencies, want) {
			t.Errorf("%s is read by the provider but is not a dependency: %v", want, rel.Dependencies)
		}
	}
	if len(rel.Bindings) != 1 || rel.Bindings[0] != BindingData {
		t.Fatalf("bindings = %v", rel.Bindings)
	}
}

func TestTheServerProviderWaitsForTheDataProvider(t *testing.T) {
	rel := (&ServerServiceProvider{}).Relationship()
	if !slices.Contains(rel.Dependencies, BindingData) {
		t.Fatalf("the HTTP server waits for the data gate, so its provider must come after the data provider: %v", rel.Dependencies)
	}
}

func TestBootDoesNothingThatACommandCouldNotDoForTheDataProvider(t *testing.T) {
	(&DataServiceProvider{}).Boot(&fakeApp{})
}

func TestRegisterBindsAnUnopenedData(t *testing.T) {
	v, err := resolveData(t, dataValues(nil))
	if err != nil {
		t.Fatal(err)
	}
	d, ok := v.(*Data)
	if !ok || d.Gate == nil || d.Runner == nil {
		t.Fatalf("binding = %#v", v)
	}
	if d.Runner.Store() != nil {
		t.Fatal("a store was opened while only binding")
	}
}

func TestRegisterNeedsNoSecretBecauseTheFrameworkResolvesTheBindingForEveryCommand(t *testing.T) {
	v, err := resolveData(t, dataValues(map[string]any{"database.encryption_secret": ""}))
	if err != nil || v == nil {
		t.Fatalf("version and health run without a secret, and Build resolves this binding for them: %v", err)
	}
}

func TestRegisterRefusesAMalformedSecretWithoutQuotingIt(t *testing.T) {
	const marker = "zz-not-hex-marker"
	v, err := resolveData(t, dataValues(map[string]any{"database.encryption_secret": marker}))
	if err == nil || v != nil {
		t.Fatalf("binding %#v error %v", v, err)
	}
	if contains(err.Error(), marker) {
		t.Fatalf("the error repeats the secret: %v", err)
	}
}

func TestTheRunnersAreTheDataRunnerAndTheScheduleIsRegisteredOnce(t *testing.T) {
	bound, err := resolveData(t, dataValues(nil))
	if err != nil {
		t.Fatal(err)
	}
	app := &dataApp{fakeApp: fakeApp{cfg: dataValues(nil), log: &tests.Log{}, values: map[any]any{BindingData: bound}}, ctx: context.Background()}
	runners := (&DataServiceProvider{}).Runners(app)
	if len(runners) != 1 || runners[0].Signature() != "rllmx:data" {
		t.Fatalf("runners = %v", runners)
	}
	if p, ok := runners[0].(foundation.RunnerWithShutdownPriority); !ok || p.ShutdownPriority() <= 0 {
		t.Fatal("the data runner must stop after the server and the schedule")
	}
}

func TestDataOfReturnsTheBinding(t *testing.T) {
	want := &Data{}
	if got := DataOf(&fakeApp{values: map[any]any{BindingData: want}}); got != want {
		t.Fatal("not the bound value")
	}
}

func TestDataOfPanicsWhenTheProviderIsNotRegistered(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a missing binding must stop the start loudly")
		}
	}()
	DataOf(&fakeApp{})
}

func TestTheScheduleEventsComeFromTheJobSet(t *testing.T) {
	d := &Data{Jobs: data.NewJobSet([]data.Job{{Name: "a", Cron: "* * * * *", Run: func(context.Context) error { return nil }}}, context.Background(), &tests.Log{}, time.Now)}
	events := ScheduleOf(&fakeApp{values: map[any]any{BindingData: d}})
	if len(events) != 1 || events[0].GetName() != "a" {
		t.Fatalf("events = %v", events)
	}
	var _ []schedule.Event = events
	var _ log.Log = &tests.Log{}
}

func contains(s, sub string) bool { return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
