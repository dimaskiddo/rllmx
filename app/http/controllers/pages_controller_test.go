package controllers

import (
	"io/fs"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/tests"
	"rllmx/web"
)

func pagesRoute(t *testing.T) *PagesController {
	t.Helper()
	return &PagesController{Files: fstest.MapFS{
		"static/callback.html":         {Data: []byte("<p>cb</p>")},
		"static/css/app.css":           {Data: []byte("a{}")},
		"static/js/app.js":             {Data: []byte("export {}")},
		"static/lib/x.mjs":             {Data: []byte("export {}")},
		"static/pages/home.html":       {Data: []byte("<p>home</p>")},
		"static/shell.html":            {Data: []byte("<p>shell</p>")},
		"static/login.html":            {Data: []byte("<p>login</p>")},
		"static/.secret.json":          {Data: []byte("{}")},
		"static/notes.go":              {Data: []byte("package x")},
		"static/i18n/en.json":          {Data: []byte("{}")},
		"static/lib/licenses/alpine.t": {Data: []byte("x")},
	}}
}

func getAsset(t *testing.T, p *PagesController, target string, header nethttp.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware([]contractshttp.Middleware{})
	r.Get("/static/*file", p.Asset)
	req := httptest.NewRequest(nethttp.MethodGet, target, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAnAssetIsServedWithItsOwnContentType(t *testing.T) {
	p := pagesRoute(t)
	for path, kind := range map[string]string{
		"/static/css/app.css":     "text/css; charset=utf-8",
		"/static/js/app.js":       "text/javascript; charset=utf-8",
		"/static/lib/x.mjs":       "text/javascript; charset=utf-8",
		"/static/i18n/en.json":    "application/json; charset=utf-8",
		"/static/pages/home.html": "text/html; charset=utf-8",
	} {
		rec := getAsset(t, p, path, nil)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != kind {
			t.Errorf("%s: %d %q, want %q", path, rec.Code, rec.Header().Get("Content-Type"), kind)
		}
		if rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("ETag") == "" {
			t.Errorf("%s: no revalidation headers: %v", path, rec.Header())
		}
	}
}

// getPage answers one of the HTML routes; the guard is not in the way here.
func getPage(t *testing.T, p *PagesController, path string, handler contractshttp.HandlerFunc, header nethttp.Header) *httptest.ResponseRecorder {
	t.Helper()
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware([]contractshttp.Middleware{})
	r.Get(path, handler)
	req := httptest.NewRequest(nethttp.MethodGet, path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestTheShellAndLoginAreRevalidatedAndTheCallbackIsNotStored(t *testing.T) {
	p := pagesRoute(t)
	for path, handler := range map[string]contractshttp.HandlerFunc{
		"/login":     p.Login,
		"/dashboard": p.Dashboard,
	} {
		first := getPage(t, p, path, handler, nil)
		if first.Code != 200 || first.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Errorf("%s: %d %q", path, first.Code, first.Header().Get("Content-Type"))
		}
		tag := first.Header().Get("ETag")
		if tag == "" || first.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: no revalidation headers: %v", path, first.Header())
		}
		if rec := getPage(t, p, path, handler, nethttp.Header{"If-None-Match": {tag}}); rec.Code != nethttp.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("%s unchanged: %d with %d bytes", path, rec.Code, rec.Body.Len())
		}
	}
	if rec := getPage(t, p, "/callback", p.Callback, nil); rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("callback: Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
}

func TestTheShellAndLoginPagesAreEmbedded(t *testing.T) {
	p := &PagesController{Files: web.Static}
	for path, page := range map[string]struct {
		h      contractshttp.HandlerFunc
		script string
	}{
		"/login":     {p.Login, "/static/js/login.js"},
		"/dashboard": {p.Dashboard, "/static/js/app.js"},
	} {
		rec := getPage(t, p, path, page.h, nil)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), page.script) {
			t.Errorf("%s: %d, does not load %s", path, rec.Code, page.script)
		}
	}
}

func TestTheRootRedirectsToTheDashboard(t *testing.T) {
	rec := getPage(t, pagesRoute(t), "/", (&PagesController{}).Root, nil)
	if rec.Code != nethttp.StatusFound || rec.Header().Get("Location") != "/dashboard" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAPageThatIsNotEmbeddedIsNotFound(t *testing.T) {
	p := &PagesController{Files: fstest.MapFS{}}
	for path, handler := range map[string]contractshttp.HandlerFunc{
		"/login": p.Login, "/dashboard": p.Dashboard,
	} {
		rec := getPage(t, p, path, handler, nil)
		if rec.Code != 404 {
			t.Errorf("%s with no embedded page: %d", path, rec.Code)
		}
		// A page a later build adds must not be hidden by a cached 404.
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("ETag") != "" {
			t.Errorf("%s: a 404 carries %v", path, rec.Header())
		}
	}
}

func TestAnAssetThatIsNotOnTheListIsNotFound(t *testing.T) {
	p := pagesRoute(t)
	for _, path := range []string{
		"/static/shell.html", "/static/callback.html", "/static/notes.go", "/static/.secret.json",
		"/static/%2e%2e/callback.html", "/static/css/%2e%2e/%2e%2e/x", "/static/css/", "/static/missing.css",
		"/static/css%5capp.css", "/static/lib/licenses/alpine.t", "/static/js/app.js%00.css",
	} {
		if rec := getAsset(t, p, path, nil); rec.Code != 404 {
			t.Errorf("%s: %d, want 404 (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

func TestAnUnchangedAssetAnswersNotModified(t *testing.T) {
	p := pagesRoute(t)
	first := getAsset(t, p, "/static/css/app.css", nil)
	tag := first.Header().Get("ETag")
	for _, in := range []string{tag, "W/" + tag, `"other", ` + tag, "*"} {
		rec := getAsset(t, p, "/static/css/app.css", nethttp.Header{"If-None-Match": {in}})
		if rec.Code != nethttp.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("If-None-Match %q: %d with %d bytes", in, rec.Code, rec.Body.Len())
		}
	}
	if rec := getAsset(t, p, "/static/css/app.css", nethttp.Header{"If-None-Match": {`"stale"`}}); rec.Code != 200 {
		t.Errorf("a stale tag: %d", rec.Code)
	}
}

// countingFS counts how often a file is opened, so a test can see whether an
// answer needed the bytes.
type countingFS struct {
	fs.FS
	opens map[string]int
}

func (c *countingFS) Open(name string) (fs.File, error) {
	c.opens[name]++
	return c.FS.Open(name)
}

// A browser that already holds the current file asks only whether it changed.
// The answer is known from the tag alone, so the file is not read again, which
// matters for the 200 KB of Alpine every page load revalidates.
func TestARevalidationDoesNotReadTheFileAgain(t *testing.T) {
	files := &countingFS{FS: pagesRoute(t).Files, opens: map[string]int{}}
	p := &PagesController{Files: files}
	first := getAsset(t, p, "/static/js/app.js", nil)
	tag := first.Header().Get("ETag")
	if first.Code != 200 || tag == "" {
		t.Fatalf("first load: %d %q", first.Code, tag)
	}
	before := files.opens["static/js/app.js"]
	for range 5 {
		if rec := getAsset(t, p, "/static/js/app.js", nethttp.Header{"If-None-Match": {tag}}); rec.Code != nethttp.StatusNotModified {
			t.Fatalf("revalidation: %d", rec.Code)
		}
	}
	if got := files.opens["static/js/app.js"]; got != before {
		t.Fatalf("five revalidations opened the file %d more times", got-before)
	}
	// A different tag still gets the file.
	if rec := getAsset(t, p, "/static/js/app.js", nethttp.Header{"If-None-Match": {`"stale"`}}); rec.Code != 200 || rec.Body.Len() == 0 {
		t.Fatalf("stale tag: %d with %d bytes", rec.Code, rec.Body.Len())
	}
}

func TestTheCallbackPageAndItsScriptComeFromTheEmbeddedFolder(t *testing.T) {
	p := &PagesController{Files: web.Static}
	if rec := getAsset(t, p, "/static/js/callback.js", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "/api/oauth/callback") {
		t.Errorf("callback script: %d", rec.Code)
	}
	if rec := getAsset(t, p, "/static/callback.html", nil); rec.Code != 404 {
		t.Errorf("the callback page is reachable as an asset: %d", rec.Code)
	}
	body, err := fs.ReadFile(web.Static, "static/callback.html")
	if err != nil || !strings.Contains(string(body), `src="/static/js/callback.js"`) {
		t.Fatalf("the callback page does not load its script: %v", err)
	}
}

func TestAssetNameNeverLeavesTheStaticFolder(t *testing.T) {
	for _, c := range []struct {
		in   string
		name string
		ok   bool
	}{
		{"css/app.css", "static/css/app.css", true},
		{"/css/app.css", "static/css/app.css", true},
		{"CSS/APP.CSS", "static/CSS/APP.CSS", true},
		{"pages/home.html", "static/pages/home.html", true},
		{"home.html", "", false},
		{"../x.css", "", false},
		{"a/../b.css", "", false},
		{"a//b.css", "", false},
		{".hidden.css", "", false},
		{"a/.b/c.css", "", false},
		{"a\\b.css", "", false},
		{"c:evil.css", "", false},
		{"a\x00.css", "", false},
		{"a\n.css", "", false},
		{"a\x7f.css", "", false},
		{"", "", false},
		{"app", "", false},
		{"app.go", "", false},
	} {
		name, _, ok := assetName(c.in)
		if ok != c.ok || name != c.name {
			t.Errorf("assetName(%q) = %q, %v; want %q, %v", c.in, name, ok, c.name, c.ok)
		}
	}
}

func FuzzAssetName(f *testing.F) {
	for _, s := range []string{"css/app.css", "../a.css", "a/./b.js", "pages/x.html", ".x.js", "a\\b.js", "%2e%2e/a.js", "a.js\x00", "///"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		name, kind, ok := assetName(in)
		if !ok {
			return
		}
		if !fs.ValidPath(name) || !strings.HasPrefix(name, "static/") || kind == "" {
			t.Fatalf("assetName(%q) = %q, %q", in, name, kind)
		}
		for _, seg := range strings.Split(name, "/") {
			if seg == "" || strings.HasPrefix(seg, ".") || strings.ContainsAny(seg, "\\:") {
				t.Fatalf("assetName(%q) = %q holds the segment %q", in, name, seg)
			}
		}
		if strings.HasSuffix(name, ".html") && !strings.HasPrefix(name, "static/pages/") {
			t.Fatalf("assetName(%q) = %q serves HTML outside pages/", in, name)
		}
	})
}
