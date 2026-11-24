package http

import (
	"fmt"
	nethttp "net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/route"

	"rllmx/app/access/auth"
	"rllmx/app/access/identity"
	"rllmx/app/platform/failure"
)

type callerKey struct{}

// CallerFrom returns who a client API request acts as; the guard sets it on
// every client API route it lets through.
func CallerFrom(ctx contractshttp.Context) (identity.Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(identity.Caller)
	return c, ok
}

// WithCaller records who a request acts as. The guard calls it for every client
// API route; a test calls it to stand in for the guard.
func WithCaller(ctx contractshttp.Context, c identity.Caller) {
	ctx.WithValue(callerKey{}, c)
}

// Guard decides who may reach which route. Every route is declared through
// Handle, which records its class; Protect, installed as global middleware,
// reads the class and the socket peer, never the framework's client IP.
// Routes are declared before serving starts, so the table needs no lock.
type Guard struct {
	auth          Authenticator
	trustLoopback bool
	routes        map[string]Class
}

// NewGuard builds a guard. A nil authenticator denies everything.
func NewGuard(auth Authenticator, trustLoopback bool) *Guard {
	if auth == nil {
		auth = denyAll{}
	}
	return &Guard{auth: auth, trustLoopback: trustLoopback, routes: map[string]Class{}}
}

// Handle registers a route on router and records its class. It panics on a
// mistake that would leave a route unguarded or wrongly guarded, because that
// is a bug in the program, not a runtime condition.
func (g *Guard) Handle(router route.Router, class Class, method, path string, handler contractshttp.HandlerFunc) {
	if !class.valid() {
		panic(fmt.Sprintf("http: route %s %s has an unknown guard class", method, path))
	}
	switch {
	case IsClientAPI(path) && class != ClassLLM:
		panic(fmt.Sprintf("http: client API route %s %s must be ClassLLM", method, path))
	case !IsClientAPI(path) && class == ClassLLM:
		panic(fmt.Sprintf("http: ClassLLM is for /v1 and /v1beta only, not %s %s", method, path))
	case IsDashboardAPI(path) && class == ClassPage:
		panic(fmt.Sprintf("http: %s %s is an API route, not a page", method, path))
	}
	methods := []string{method}
	if method == nethttp.MethodGet {
		methods = append(methods, nethttp.MethodHead)
	}
	for _, m := range methods {
		if _, dup := g.routes[m+" "+path]; dup {
			panic("http: route registered twice: " + m + " " + path)
		}
	}
	switch method {
	case nethttp.MethodGet:
		router.Get(path, handler)
	case nethttp.MethodPost:
		router.Post(path, handler)
	case nethttp.MethodPut:
		router.Put(path, handler)
	case nethttp.MethodPatch:
		router.Patch(path, handler)
	case nethttp.MethodDelete:
		router.Delete(path, handler)
	case nethttp.MethodOptions:
		router.Options(path, handler)
	default:
		panic(fmt.Sprintf("http: route %s %s has an unsupported method", method, path))
	}
	for _, m := range methods {
		g.routes[m+" "+path] = class
	}
}

// RouteClass returns the class a route was registered with.
func (g *Guard) RouteClass(method, path string) (Class, bool) {
	c, ok := g.routes[method+" "+path]
	return c, ok
}

// Unclassified lists the routes in infos that were not declared through
// Handle, so the start can refuse a route added on the bare Route facade.
func (g *Guard) Unclassified(infos []contractshttp.Info) []string {
	var missing []string
	for _, info := range infos {
		for m := range strings.SplitSeq(info.Method, "|") {
			if _, ok := g.routes[m+" "+info.Path]; !ok {
				missing = append(missing, m+" "+info.Path)
			}
		}
	}
	sort.Strings(missing)
	return slices.Compact(missing)
}

func (g *Guard) classOf(ctx contractshttp.Context) (Class, bool) {
	fp := ctx.Request().OriginPath()
	if fp == "" {
		return 0, false
	}
	c, ok := g.routes[ctx.Request().Method()+" "+fp]
	return c, ok
}

// deny answers in the client's format and stops the chain.
func deny(ctx contractshttp.Context, status int, msg string) {
	failure.WriteFor(ctx.Response().Writer(), ctx.Request().Origin(), status, msg)
	ctx.Request().Abort(status)
}

// Protect is the guard middleware body. It runs for every request, matched or
// not, so a mistyped or hidden path is answered the same way as a real one.
func (g *Guard) Protect(ctx contractshttp.Context) {
	r := ctx.Request().Origin()
	class, routed := g.classOf(ctx)
	if !routed {
		class = unroutedClass(r.URL.Path)
	}
	local := isLocalRequest(r, g.trustLoopback)
	who, signedIn := g.auth.SessionInfo(r)
	anonymousOK := local && !g.auth.RequireLogin(r.Context())
	keyOK := false
	var caller identity.Caller
	if class == ClassLLM {
		limiter, throttled := g.auth.(keyLimiter)
		throttled = throttled && !local
		if throttled && identity.ExtractKey(r) != "" {
			if wait := limiter.KeyLocked(r); wait > 0 {
				secs := max(int64((wait+time.Second-1)/time.Second), 1)
				ctx.Response().Header("Retry-After", strconv.FormatInt(secs, 10))
				deny(ctx, nethttp.StatusTooManyRequests, "")
				return
			}
		}
		res := g.auth.APIKey(r)
		if throttled && res.Status == identity.KeyInvalid {
			limiter.KeyFailed(r)
		}
		switch {
		case res.Status == identity.KeyValid:
			keyOK, caller = true, res.Caller
		case local && !g.auth.RequireAPIKey(r.Context()):
			keyOK, caller = true, identity.AnonymousLocal()
		}
	}

	switch decide(class, local, signedIn, anonymousOK, keyOK) {
	case verdictAllow:
		if class == ClassLLM {
			WithCaller(ctx, caller)
		}
		// A local request let through with login off acts as the admin: that
		// switch already says this machine is trusted.
		role := auth.RoleAdmin
		if signedIn {
			role = who.Role
			WithSession(ctx, who)
		}
		// A public route needs no session, so the role has nothing to narrow: a
		// signed-in user would otherwise be refused what an anonymous caller gets.
		if class != ClassPublic && !roleAllows(role, r.Method, r.URL.Path) {
			deny(ctx, nethttp.StatusForbidden, "")
			return
		}
		ctx.Request().Next()
	case verdictUnauthorized:
		msg := ""
		if class == ClassLLM {
			msg = "API key required"
		}
		deny(ctx, nethttp.StatusUnauthorized, msg)
	case verdictForbidden:
		deny(ctx, nethttp.StatusForbidden, "Local access only")
	case verdictLogin:
		nethttp.Redirect(ctx.Response().Writer(), r, "/login", nethttp.StatusFound)
		ctx.Request().Abort(nethttp.StatusFound)
	default:
		deny(ctx, nethttp.StatusForbidden, "")
	}
}

// Fallback answers a request no route matched. It is reached only by callers
// the guard let through, so it may say a path exists under another method:
// 405 with Allow, else 404. The driver does not turn on Gin's own 405.
func (g *Guard) Fallback(routes func() []contractshttp.Info) contractshttp.HandlerFunc {
	return func(ctx contractshttp.Context) contractshttp.Response {
		if allowed := allowedMethods(routes(), ctx.Request().Origin().URL.Path); len(allowed) > 0 {
			ctx.Response().Header("Allow", strings.Join(allowed, ", "))
			deny(ctx, nethttp.StatusMethodNotAllowed, "")
			return nil
		}
		deny(ctx, nethttp.StatusNotFound, "Not found")
		return nil
	}
}

func allowedMethods(infos []contractshttp.Info, path string) []string {
	var out []string
	for _, info := range infos {
		if matchPath(info.Path, path) {
			out = append(out, strings.Split(info.Method, "|")...)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// matchPath reports whether a request path fits a route pattern: {name}
// matches one segment, *name matches the rest.
func matchPath(pattern, path string) bool {
	ps, rs := strings.Split(pattern, "/"), strings.Split(path, "/")
	for i, seg := range ps {
		if strings.HasPrefix(seg, "*") {
			return i < len(rs)
		}
		if i >= len(rs) {
			return false
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			if rs[i] == "" {
				return false
			}
			continue
		}
		if seg != rs[i] {
			return false
		}
	}
	return len(ps) == len(rs)
}
