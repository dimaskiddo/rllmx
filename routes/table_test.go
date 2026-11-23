package routes

import (
	"context"
	"flag"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"

	apphttp "rllmx/app/http"
	"rllmx/app/http/controllers"
	"rllmx/app/http/middleware"
	"rllmx/tests"
)

var update = flag.Bool("update", false, "rewrite the golden route table")

// fullBase fills every controller, so Web declares every route it can.
func fullBase() Base {
	return Base{
		Version: "1", Commit: "c", Connections: func(context.Context) (int64, error) { return 0, nil },
		Auth: &controllers.AuthController{}, OIDC: &controllers.OIDCController{}, SAML: &controllers.SAMLController{},
		Providers: &controllers.ProvidersController{}, Nodes: &controllers.NodesController{},
		Chat: &controllers.ChatController{}, Models: &controllers.ModelsController{}, Media: &controllers.MediaController{},
		OAuth: &controllers.OAuthController{}, Pages: &controllers.PagesController{},
		Keys: &controllers.KeysController{}, Users: &controllers.UsersController{}, Health: controllers.NewHealthController(),
		Combos: &controllers.CombosController{}, Settings: &controllers.SettingsController{},
		Pricing: &controllers.PricingController{}, Logs: &controllers.LogsController{}, Usage: &controllers.UsageController{},
		Headroom: &controllers.HeadroomController{}, Skills: &controllers.SkillsController{},
	}
}

// tableOf lists every route a full build declares as "METHOD path class", sorted.
// The guard's own table is read, not the driver's: the driver keeps its routes
// for the whole process, so a repeated or shuffled run would find the routes
// other tests declared, while the guard here is fresh and holds only this build.
func tableOf(t *testing.T) []string {
	t.Helper()
	r := tests.NewRoute(t)
	g := apphttp.NewGuard(nil, true)
	r.SetGlobalMiddleware([]contractshttp.Middleware{&middleware.RequestID{}, &middleware.Guard{Guard: g}})
	Web(r, g, fullBase())
	var lines []string
	for _, info := range r.GetRoutes() {
		for _, m := range strings.Split(info.Method, "|") {
			if class, ok := g.RouteClass(m, info.Path); ok {
				lines = append(lines, m+" "+info.Path+" class="+strconv.Itoa(int(class)))
			}
		}
	}
	sort.Strings(lines)
	return slices.Compact(lines)
}

// TestTheRouteTableIsPinned holds every route, with its method and guard class,
// against a golden file, so a change to how the table is built cannot add, drop
// or reclassify a route without the diff showing it.
func TestTheRouteTableIsPinned(t *testing.T) {
	got := strings.Join(tableOf(t), "\n") + "\n"
	const golden = "testdata/routes.golden"
	if *update {
		if err := os.MkdirAll("testdata", 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("the route table changed; run with -update to accept it\n%s", diffLines(string(want), got))
	}
}

func diffLines(want, got string) string {
	has := func(set, line string) bool { return strings.Contains("\n"+set, "\n"+line+"\n") }
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(want), "\n") {
		if !has(got, l) {
			out = append(out, "- "+l)
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(got), "\n") {
		if !has(want, l) {
			out = append(out, "+ "+l)
		}
	}
	return strings.Join(out, "\n")
}
