package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"

	"rllmx/app/llm/upstream"
	"rllmx/web"
)

const libDir = "static/lib/"

// ours walks every embedded text file that is not a vendored library or an
// image: the scans look for source constructs, which binary bytes can match by
// chance.
func ours(t *testing.T, visit func(name string, body string)) {
	t.Helper()
	err := fs.WalkDir(web.Static, "static", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(name, libDir) || strings.HasPrefix(name, "static/img/") {
			return err
		}
		body, err := fs.ReadFile(web.Static, name)
		if err != nil {
			return err
		}
		visit(name, string(body))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var (
	inlineHandler = regexp.MustCompile(`(?i)<[^>]+\son[a-z]+\s*=`)
	inlineScript  = regexp.MustCompile(`(?is)<script\b(?:[^>]*)>`)
	absoluteURL   = regexp.MustCompile(`(?i)\bhttps?://`)
	// staticRef is a literal file reference. A path the router builds from a
	// route name is checked by the page tests instead.
	staticRef = regexp.MustCompile(`/static/[A-Za-z0-9_./-]+\.[a-z0-9]+`)
	relImport = regexp.MustCompile(`(?:from|import)\s*\(?\s*["'](\.{1,2}/[^"']+)["']`)
	// exprAttr is every Alpine directive and binding, whose value the CSP build
	// parses as an expression; the raw-word directives x-t and x-icon are read
	// by our own code and never parsed, and the scan skips them by name.
	exprAttr     = regexp.MustCompile(`(x-[a-z-]+|@[a-z.:-]+|:[a-z-]+)="([^"]*)"`)
	i18nAttr     = regexp.MustCompile(`x-t(?:ip)?="([^"]+)"`)
	i18nCall     = regexp.MustCompile(`\bt\("([^"]+)"\)`)
	i18nLabel    = regexp.MustCompile(`\blabel\('([^']+)'\)`)
	iconAttr     = regexp.MustCompile(`x-icon="([^"]+)"`)
	iconEntry    = regexp.MustCompile(`"([a-z-]+)":\s*\(\)`)
	alpineBanned = []string{"=>", "`", "...", "window.", "document.", "JSON.", "Math."}
)

// forbidden are the constructs that put data into markup or code, or let a
// script run from text. The dashboard's own files carry none of them, so a data
// string can never become markup (the page policy would also refuse the rest).
var forbidden = []string{
	"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "createContextualFragment",
	"eval(", "new Function", "setTimeout(\"", "setInterval(\"", "x-html", "srcdoc=", `style="`, `:style="'`,
}

func TestOurWebFilesCarryNoMarkupFromDataOrInlineCode(t *testing.T) {
	ours(t, func(name, body string) {
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
		if !strings.HasSuffix(name, ".html") {
			return
		}
		if m := inlineHandler.FindString(body); m != "" {
			t.Errorf("%s has an inline event handler: %s", name, m)
		}
		for _, tag := range inlineScript.FindAllString(body, -1) {
			if !strings.Contains(tag, "src=") {
				t.Errorf("%s has an inline script: %s", name, tag)
			}
		}
	})
}

func TestOurWebFilesLoadNothingFromAnotherOrigin(t *testing.T) {
	ours(t, func(name, body string) {
		if m := absoluteURL.FindString(body); m != "" {
			t.Errorf("%s names an absolute address; the page policy would refuse it", name)
		}
	})
}

// resolves reports whether a name is an embedded file.
func resolves(name string) bool {
	st, err := fs.Stat(web.Static, name)
	return err == nil && !st.IsDir()
}

func TestEveryWebReferenceResolvesToAnEmbeddedFile(t *testing.T) {
	err := fs.WalkDir(web.Static, "static", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		switch path.Ext(name) {
		case ".html", ".css", ".js", ".mjs":
		default:
			return nil
		}
		if strings.HasPrefix(name, libDir) && path.Ext(name) != ".mjs" {
			return nil
		}
		body, err := fs.ReadFile(web.Static, name)
		if err != nil {
			return err
		}
		for _, ref := range staticRef.FindAllString(string(body), -1) {
			if !resolves(strings.TrimPrefix(ref, "/")) {
				t.Errorf("%s refers to %s, which is not embedded", name, ref)
			}
		}
		for _, m := range relImport.FindAllStringSubmatch(string(body), -1) {
			if target := path.Join(path.Dir(name), m[1]); !resolves(target) {
				t.Errorf("%s imports %s, which is not embedded", name, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type versions struct {
	Libraries []struct {
		Name    string            `json:"name"`
		Version string            `json:"version"`
		License string            `json:"license"`
		Files   map[string]string `json:"files"`
	} `json:"libraries"`
}

func TestVendoredLibrariesMatchTheirRecordedHashes(t *testing.T) {
	raw, err := fs.ReadFile(web.Static, libDir+"VERSIONS.json")
	if err != nil {
		t.Fatal(err)
	}
	var v versions
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Libraries) == 0 {
		t.Fatal("VERSIONS.json lists no library")
	}
	listed := map[string]bool{}
	for _, lib := range v.Libraries {
		if lib.Name == "" || lib.Version == "" || lib.License == "" || len(lib.Files) == 0 {
			t.Errorf("%q is missing its name, version, licence or files", lib.Name)
		}
		hasLicense := false
		for name, want := range lib.Files {
			if listed[name] {
				t.Errorf("%s is listed twice", name)
			}
			listed[name] = true
			hasLicense = hasLicense || strings.HasPrefix(name, "licenses/")
			body, err := fs.ReadFile(web.Static, libDir+name)
			if err != nil {
				t.Errorf("%s is listed but not embedded: %v", name, err)
				continue
			}
			sum := sha256.Sum256(body)
			if got := hex.EncodeToString(sum[:]); got != want {
				t.Errorf("%s changed: sha256 %s, recorded %s", name, got, want)
			}
		}
		if !hasLicense {
			t.Errorf("%s ships no licence file", lib.Name)
		}
	}
	err = fs.WalkDir(web.Static, "static/lib", func(name string, d fs.DirEntry, err error) error {
		rel := strings.TrimPrefix(name, libDir)
		if err != nil || d.IsDir() || rel == "VERSIONS.json" {
			return err
		}
		if !listed[rel] {
			t.Errorf("%s is embedded but not in VERSIONS.json", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// pages maps each routed page to the fragment and the component module the
// router loads for it.
func pages(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := fs.ReadFile(web.Static, "static/js/routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var routes []struct {
		Path string `json:"path"`
		Page string `json:"page"`
	}
	if err := json.Unmarshal(raw, &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes) == 0 {
		t.Fatal("routes.json declares no page")
	}
	out := map[string]bool{"not-found": true}
	for _, route := range routes {
		if !strings.HasPrefix(route.Path, "/dashboard") {
			t.Errorf("%s is outside the dashboard", route.Path)
		}
		out[route.Page] = true
	}
	return out
}

func TestEveryPageRouteHasATemplateAndAComponent(t *testing.T) {
	for page := range pages(t) {
		if !resolves("static/pages/" + page + ".html") {
			t.Errorf("page %q has no template", page)
		}
		if !resolves("static/js/pages/" + page + ".js") {
			t.Errorf("page %q has no component module", page)
			continue
		}
		body, err := fs.ReadFile(web.Static, "static/js/pages/"+page+".js")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "Alpine.data(") {
			t.Errorf("page %q registers no Alpine component", page)
		}
	}
	err := fs.WalkDir(web.Static, "static/pages", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		page := strings.TrimSuffix(path.Base(name), ".html")
		if !pages(t)[page] {
			t.Errorf("%s belongs to no route", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func loadTranslations(t *testing.T, locale string) map[string]string {
	t.Helper()
	raw, err := fs.ReadFile(web.Static, "static/i18n/"+locale+".json")
	if err != nil {
		t.Fatal(err)
	}
	var messages map[string]string
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatalf("%s.json: %v", locale, err)
	}
	return messages
}

func TestTheTranslationsCoverEveryKeyAndBothLocales(t *testing.T) {
	en := loadTranslations(t, "en")
	id := loadTranslations(t, "id")
	for key := range en {
		if strings.TrimSpace(id[key]) == "" {
			t.Errorf("id.json is missing %q", key)
		}
	}
	for key := range id {
		if strings.TrimSpace(en[key]) == "" {
			t.Errorf("en.json is missing %q", key)
		}
	}
	ours(t, func(name, body string) {
		key := func(k, where string) {
			if strings.TrimSpace(en[k]) == "" {
				t.Errorf("%s uses %q, which en.json does not hold (%s)", name, k, where)
			}
		}
		for _, m := range i18nAttr.FindAllStringSubmatch(body, -1) {
			key(m[1], "x-t")
		}
		// label('key') is the same lookup where markup picks the key at render.
		for _, m := range i18nLabel.FindAllStringSubmatch(body, -1) {
			key(m[1], "label()")
		}
		if strings.HasSuffix(name, ".js") {
			for _, m := range i18nCall.FindAllStringSubmatch(body, -1) {
				if !strings.HasPrefix(m[1], "sso.") {
					key(m[1], "t()")
				}
			}
		}
	})
	// The identity provider's error codes are built at run time, so only the
	// full set both files must hold is checked here.
	for _, code := range []string{
		"sso.sso_disabled", "sso.sso_not_configured", "sso.sso_busy", "sso.sso_expired",
		"sso.sso_invalid", "sso.sso_not_allowed", "sso.sso_no_email", "sso.sso_unverified", "sso.sso_idp_error",
	} {
		if strings.TrimSpace(en[code]) == "" || strings.TrimSpace(id[code]) == "" {
			t.Errorf("%s is missing from a locale", code)
		}
	}
}

func TestEveryIconNameIsVendoredAndMapped(t *testing.T) {
	raw, err := fs.ReadFile(web.Static, "static/js/ui/icons.js")
	if err != nil {
		t.Fatal(err)
	}
	mapped := map[string]bool{}
	for _, m := range iconEntry.FindAllStringSubmatch(string(raw), -1) {
		mapped[m[1]] = true
		if !resolves(libDir + "lucide/icons/" + m[1] + ".mjs") {
			t.Errorf("%s is mapped but not vendored", m[1])
		}
	}
	if len(mapped) == 0 {
		t.Fatal("icons.js maps no icon")
	}
	ours(t, func(name, body string) {
		for _, m := range iconAttr.FindAllStringSubmatch(body, -1) {
			if !mapped[m[1]] {
				t.Errorf("%s draws the unmapped icon %q", name, m[1])
			}
		}
	})
}

// The page policy refuses Function, so an expression may read properties, call
// methods and compare; it may not build or run code. Checking it here means a
// mistake fails in Go rather than silently in a browser console.
func TestAlpineExpressionsStayInsideTheCSPGrammar(t *testing.T) {
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		for _, m := range exprAttr.FindAllStringSubmatch(body, -1) {
			if m[1] == "x-t" || m[1] == "x-icon" || m[1] == "x-tip" {
				continue
			}
			for _, banned := range alpineBanned {
				if strings.Contains(m[2], banned) {
					t.Errorf("%s has an expression the CSP build cannot parse: %q holds %q", name, m[2], banned)
				}
			}
		}
	})
}

// assignment finds `a.b = c` in an event handler: a single equals sign that is
// not part of ==, !=, <=, >= or =>. The design doc forbids it, and a handler is
// a method call so the page's state changes in one named place.
var (
	assignment = regexp.MustCompile(`[A-Za-z_\])]\s*=\s*[^=>]`)
	// handlerAttr is an event handler written either way. exprAttr cannot tell
	// x-on:input from x-bind:input: its first alternative stops at the colon, so
	// both reach it as ":input".
	handlerAttr = regexp.MustCompile(`(?:x-on:[a-z.:-]+|@[a-z.:-]+)="([^"]*)"`)
)

func TestNoEventHandlerAssignsAProperty(t *testing.T) {
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		for _, m := range handlerAttr.FindAllStringSubmatch(body, -1) {
			if assignment.MatchString(strings.NewReplacer("==", "", "!=", "", "<=", "", ">=", "").Replace(m[1])) {
				t.Errorf("%s: handler %q assigns a property; call a method instead", name, m[1])
			}
		}
	})
}

var (
	magicName  = regexp.MustCompile(`\$[A-Za-z_]+`)
	scriptSrc  = regexp.MustCompile(`<script[^>]+src="/static/([^"]+)"`)
	rawWordUse = regexp.MustCompile(`\sx-(?:t|tip|icon)="`)
	// csp is the set of magics the CSP build of Alpine defines. A name outside
	// it, such as $index, throws when the expression runs, and nothing else
	// notices until a person clicks.
	csp = map[string]bool{
		"$data": true, "$dispatch": true, "$el": true, "$event": true, "$id": true,
		"$nextTick": true, "$refs": true, "$root": true, "$store": true, "$watch": true,
	}
)

func TestEveryExpressionUsesOnlyMagicsTheCSPBuildDefines(t *testing.T) {
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		for _, m := range exprAttr.FindAllStringSubmatch(body, -1) {
			if m[1] == "x-t" || m[1] == "x-icon" || m[1] == "x-tip" {
				continue
			}
			for _, magic := range magicName.FindAllString(m[2], -1) {
				if !csp[magic] {
					t.Errorf("%s: %q uses %s, which the CSP build does not define", name, m[2], magic)
				}
			}
		}
	})
}

var (
	callName = regexp.MustCompile(`(?:^|[^.\w$])([A-Za-z_]\w*)\(`)
	// methodDef matches a method written in an Alpine.data object literal.
	methodDef = regexp.MustCompile(`(?m)^\s+(?:async\s+)?([A-Za-z_]\w*)\(`)
	// scopeImport is a module a page's script imports from its own folder or
	// from the shared ui folder: the code an expression in the page's fragment
	// can reach through the component. Another page's module is not in scope.
	scopeImport = regexp.MustCompile(`from\s+"(\./[^"]+\.js|/static/js/ui/[^"]+\.js)"`)
)

// A fragment's expression runs against its own component. A method that only
// another page defines is an undefined variable at click time, which no
// other check sees.
func TestEveryFunctionAPageCallsIsDefinedForThatPage(t *testing.T) {
	ours(t, func(name, body string) {
		if path.Dir(name) != "static/pages" || !strings.HasSuffix(name, ".html") {
			return
		}
		page := strings.TrimSuffix(path.Base(name), ".html")
		src, err := fs.ReadFile(web.Static, "static/js/pages/"+page+".js")
		if err != nil {
			t.Errorf("%s has no component script", name)
			return
		}
		scope := string(src)
		// A shared module reaches the component only through its mix-in, so an
		// import that nothing mixes in does not put its methods in scope.
		mixed := mixinNames(scope)
		for _, m := range scopeImport.FindAllStringSubmatch(scope, -1) {
			file := "static/js/pages/" + strings.TrimPrefix(m[1], "./")
			if strings.HasPrefix(m[1], "/static/") {
				file = strings.TrimPrefix(m[1], "/")
			}
			more, err := fs.ReadFile(web.Static, file)
			if err != nil {
				continue
			}
			if !mixedIn(string(more), mixed) {
				continue
			}
			scope += "\n" + string(more)
		}
		defined := map[string]bool{}
		for _, m := range methodDef.FindAllStringSubmatch(scope, -1) {
			defined[m[1]] = true
		}
		for _, attr := range exprAttr.FindAllStringSubmatch(body, -1) {
			if attr[1] == "x-t" || attr[1] == "x-icon" || attr[1] == "x-tip" {
				continue
			}
			for _, call := range callName.FindAllStringSubmatch(attr[2], -1) {
				if !defined[call[1]] {
					t.Errorf("%s: %q calls %s(), which %s.js and its imports do not define", name, attr[2], call[1], page)
				}
			}
		}
	})
}

var (
	dialogTag  = regexp.MustCompile(`<dialog\b[^>]*>`)
	labelledBy = regexp.MustCompile(`aria-labelledby="([^"]+)"`)
)

// A modal with no name is announced as "dialog" and nothing else. Each one
// points at a heading in the same file, so the name is the title people read.
func TestEveryDialogIsNamedByAHeadingInItsFile(t *testing.T) {
	ours(t, func(name, body string) {
		for _, tag := range dialogTag.FindAllString(body, -1) {
			m := labelledBy.FindStringSubmatch(tag)
			if m == nil {
				t.Errorf("%s: %s has no aria-labelledby", name, tag)
				continue
			}
			if !strings.Contains(body, `id="`+m[1]+`"`) {
				t.Errorf("%s: aria-labelledby=%q names no element", name, m[1])
			}
		}
	})
}

// A toast raised by script is read aloud only inside a live region.
func TestTheToastStackIsALiveRegionInTheTopLayer(t *testing.T) {
	body, err := fs.ReadFile(web.Static, "static/shell.html")
	if err != nil {
		t.Fatal(err)
	}
	stack := regexp.MustCompile(`<div[^>]*class="rl-toasts"[^>]*>`).FindString(string(body))
	for _, want := range []string{`aria-live="polite"`, `role="status"`, `popover="manual"`} {
		if !strings.Contains(stack, want) {
			t.Errorf("the toast stack lacks %s: %q", want, stack)
		}
	}
}

// The page language is what a screen reader pronounces with. Both entry points
// that start Alpine set it from the locale cookie.
func TestEveryEntryScriptSetsTheDocumentLanguage(t *testing.T) {
	for _, name := range []string{"static/js/app.js", "static/js/login.js", "static/js/callback.js"} {
		body, err := fs.ReadFile(web.Static, name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "documentElement.lang") {
			t.Errorf("%s never sets the document language", name)
		}
	}
}

var cssClass = regexp.MustCompile(`\.(rl-[a-z0-9-]+)`)

// A rule nothing uses is read by every person who edits the file. Each rl-
// selector must appear in some page, script or shell, as a word.
func TestEveryComponentClassIsUsedSomewhere(t *testing.T) {
	css, err := fs.ReadFile(web.Static, "static/css/components.css")
	if err != nil {
		t.Fatal(err)
	}
	var corpus strings.Builder
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".css") && !strings.HasSuffix(name, ".json") {
			corpus.WriteString(body)
		}
	})
	seen := map[string]bool{}
	for _, m := range cssClass.FindAllStringSubmatch(string(css), -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		if !regexp.MustCompile(`[\s"'` + "`" + `]` + regexp.QuoteMeta(m[1]) + `[\s"'` + "`" + `]`).MatchString(corpus.String()) {
			t.Errorf("components.css styles .%s, which no page or script uses", m[1])
		}
	}
}

// A mapped icon nothing draws is a request the browser never makes and a line
// every reader of icons.js has to explain.
func TestEveryMappedIconIsDrawnSomewhere(t *testing.T) {
	raw, err := fs.ReadFile(web.Static, "static/js/ui/icons.js")
	if err != nil {
		t.Fatal(err)
	}
	var corpus strings.Builder
	ours(t, func(name, body string) {
		if name != "static/js/ui/icons.js" {
			corpus.WriteString(body)
		}
	})
	for _, m := range iconEntry.FindAllStringSubmatch(string(raw), -1) {
		// The directive reads the literal attribute and nothing else draws an
		// icon, so this is the only way a name can be used.
		if !strings.Contains(corpus.String(), `x-icon="`+m[1]+`"`) {
			t.Errorf("icons.js maps %q, which nothing draws", m[1])
		}
	}
}

var exportedMixin = regexp.MustCompile(`export const (\w+Methods)\b`)

var importLine = regexp.MustCompile(`(?m)^import .*$`)

// mixinNames is the identifiers a component uses outside its import lines, which
// is where an Object.assign names the methods it takes in.
func mixinNames(component string) map[string]bool {
	out := map[string]bool{}
	body := importLine.ReplaceAllString(component, "")
	for _, m := range regexp.MustCompile(`\b(\w+Methods)\b`).FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	return out
}

// mixedIn reports whether a module exports a mixin the component uses. A module
// that exports no mixin, only plain functions, is in scope as imported.
func mixedIn(module string, mixed map[string]bool) bool {
	found := exportedMixin.FindAllStringSubmatch(module, -1)
	if len(found) == 0 {
		return true
	}
	for _, m := range found {
		if mixed[m[1]] {
			return true
		}
	}
	return false
}

// A page that is served on its own, not as a fragment the shell loads, needs
// its script to register x-t and x-icon. Alpine treats an unknown directive as
// a no-op, so a page without them renders with no text and no icons.
func TestEveryStandalonePageRegistersTheRawWordDirectives(t *testing.T) {
	ours(t, func(name, body string) {
		if path.Dir(name) != "static" || !strings.HasSuffix(name, ".html") || !rawWordUse.MatchString(body) {
			return
		}
		scripts := scriptSrc.FindAllStringSubmatch(body, -1)
		for _, s := range scripts {
			src, err := fs.ReadFile(web.Static, "static/"+s[1])
			if err == nil && strings.Contains(string(src), "ui/directives.js") {
				return
			}
		}
		t.Errorf("%s uses x-t or x-icon, but none of its scripts imports ui/directives.js", name)
	})
}

func TestVendoredLibrariesHaveNoSourceMapOrNetworkReference(t *testing.T) {
	err := fs.WalkDir(web.Static, "static/lib", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".json") {
			return err
		}
		body, err := fs.ReadFile(web.Static, name)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "sourceMappingURL") {
			t.Errorf("%s points at a source map that is not shipped", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readWeb(t *testing.T, name string) string {
	t.Helper()
	body, err := fs.ReadFile(web.Static, name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// A card that links only through its title leaves the rest of it dead to a click.
func TestAProviderCardIsClickableAcrossItsWholeFace(t *testing.T) {
	body := readWeb(t, "static/pages/providers.html")
	links := regexp.MustCompile(`<a[^>]*class="[^"]*rl-provider-name[^"]*"`).FindAllString(body, -1)
	if len(links) == 0 {
		t.Fatal("no provider card link found")
	}
	for _, l := range links {
		if !strings.Contains(l, "stretched-link") {
			t.Errorf("a provider card link is not stretched: %s", l)
		}
	}
}

// /api/models rows name the model in `model`; only the custom list says `id`.
// Reading `id` from the registry list gave every built-in the key "undefined".
func TestTheRegistryModelListIsReadByTheFieldTheServerSends(t *testing.T) {
	status := readWeb(t, "static/js/pages/provider-status.js")
	if !strings.Contains(status, "m.model") {
		t.Error("modelRow does not read the server's `model` field")
	}
	models := readWeb(t, "static/js/pages/provider-models.js")
	if !strings.Contains(models, "modelRow") {
		t.Error("loadModels does not map the registry rows through modelRow")
	}
}

// The text a person reads is the short alias form; the node id stays in the
// request targets only.
func TestTheModelListShowsTheAliasFormNotTheRoutingTarget(t *testing.T) {
	body := readWeb(t, "static/pages/provider.html")
	if regexp.MustCompile(`x-text="modelFull\(`).MatchString(body) {
		t.Error("provider.html prints modelFull; show modelLabel")
	}
	if !strings.Contains(readWeb(t, "static/js/pages/provider-models.js"), "this.modelLabel(") {
		t.Error("subtext never falls back to modelLabel")
	}
}

func modelRowBlock(t *testing.T) string {
	t.Helper()
	body := readWeb(t, "static/pages/provider.html")
	start := strings.Index(body, `x-for="model in models.items"`)
	end := strings.Index(body, `x-show="models.disabled.length > 0"`)
	if start < 0 || end < start {
		t.Fatal("the model list block was not found")
	}
	return body[start:end]
}

func TestTheModelTitleIsTheIdAndTheSubtextIsTheNameOrLabel(t *testing.T) {
	block := modelRowBlock(t)
	title := regexp.MustCompile(`(?s)<div class="rl-list-title">(.*?)</div>`).FindStringSubmatch(block)
	if title == nil {
		t.Fatal("the model title was not found")
	}
	if !strings.Contains(title[1], `x-text="model.id"`) {
		t.Errorf("the title does not print the model id: %s", title[1])
	}
	if strings.Contains(title[1], "modelLabel(") {
		t.Errorf("the title prints the alias form: %s", title[1])
	}
	if n := strings.Count(block, `x-text="subtext(model)"`); n != 1 {
		t.Errorf("%d subtext lines, want one", n)
	}
	if strings.Contains(block, `x-text="model.name"`) {
		t.Error("the display name has its own line again; subtext carries it")
	}
}

// A limit box with no hint reads as a broken field.
func TestEveryBudgetInputSaysWhatEmptyMeans(t *testing.T) {
	body := readWeb(t, "static/pages/endpoint.html")
	inputs := regexp.MustCompile(`<input[^>]*setBudget[^>]*>`).FindAllString(body, -1)
	if len(inputs) != 9 {
		t.Fatalf("found %d budget inputs, want 9", len(inputs))
	}
	for _, in := range inputs {
		if !strings.Contains(in, "placeholder") {
			t.Errorf("a budget input has no placeholder: %s", in)
		}
	}
}

// An icon-only button has no visible text. Its hidden label is for a screen
// reader; a title, or x-tip which sets one from a key, is what a sighted
// person reads on hover.
func TestEveryIconOnlyButtonHasATooltip(t *testing.T) {
	button := regexp.MustCompile(`(?s)<button\b((?:[^>"]|"[^"]*")*)>(.*?)</button>`)
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		for _, m := range button.FindAllStringSubmatch(body, -1) {
			if !strings.Contains(m[2], "visually-hidden") {
				continue
			}
			if !strings.Contains(m[1], "title=") && !strings.Contains(m[1], "x-tip=") {
				t.Errorf("%s: an icon-only button has no title: <button%s>", name, strings.TrimSpace(m[1]))
			}
		}
	})
}

// A tooltip script once left `>>` after an attribute list, which printed a
// literal ">" inside the button. No tag may end that way.
func TestNoTagIsFollowedByAStrayAngleBracket(t *testing.T) {
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		if i := strings.Index(body, `">>`); i >= 0 {
			t.Errorf("%s: a tag ends in \">> near %q", name, body[max(0, i-60):i+3])
		}
	})
}

// Every row of a list keeps the same buttons, so the right-hand group lines up.
// A button that does not apply is disabled, never left out of one row.
func TestTheModelActionButtonsAreNeverHiddenPerRow(t *testing.T) {
	removeAlias := 0
	for _, m := range regexp.MustCompile(`<button\b(?:[^>"]|"[^"]*")*>`).FindAllString(modelRowBlock(t), -1) {
		if strings.Contains(m, "removeCustom") && !strings.Contains(m, "x-bind:disabled") {
			t.Errorf("the delete button must be disabled for a built-in model: %s", m)
		}
		if strings.Contains(m, "removeAlias") {
			removeAlias++
			if !strings.Contains(m, "x-bind:disabled") {
				t.Errorf("the remove-alias button must be disabled when there is no alias: %s", m)
			}
			if !strings.Contains(m, `x-show="!node"`) {
				t.Errorf("a custom provider has no alias to remove, so the button must not show there: %s", m)
			}
		}
		if strings.Contains(m, "removeCustom") && strings.Contains(m, "x-show=") {
			t.Errorf("a per-row x-show shifts the buttons of other rows: %s", m)
		}
	}
	if removeAlias != 1 {
		t.Errorf("%d remove-alias buttons, want one", removeAlias)
	}
}

func TestTheOneByOneTestIsOfferedForASingleConnection(t *testing.T) {
	body := readWeb(t, "static/pages/provider.html")
	var m string
	for _, tag := range regexp.MustCompile(`<button\b(?:[^>"]|"[^"]*")*>`).FindAllString(body, -1) {
		if strings.Contains(tag, "testSelected()") {
			m = tag
		}
	}
	if m == "" {
		t.Fatal("the one-by-one button was not found")
	}
	if strings.Contains(m, "connections.length > 1") {
		t.Errorf("one connection cannot be tested one by one: %s", m)
	}
}

func TestTheModelListSortsBuiltInFirst(t *testing.T) {
	if !strings.Contains(readWeb(t, "static/js/pages/provider-models.js"), "sortModels(") {
		t.Error("loadModels does not order the registry list through sortModels")
	}
	if !strings.Contains(readWeb(t, "static/js/pages/provider-status.js"), "export function sortModels") {
		t.Error("provider-status.js does not define sortModels")
	}
}

func TestTheModelWhitelistOffersEveryLiveModel(t *testing.T) {
	js := readWeb(t, "static/js/pages/endpoint.js")
	for _, want := range []string{"export function liveModels", `api("/api/models")`, `api("/api/providers")`, "= liveModels("} {
		if !strings.Contains(js, want) {
			t.Errorf("endpoint.js lacks %q: the whitelist would offer only aliases and combos", want)
		}
	}
	html := readWeb(t, "static/pages/endpoint.html")
	if !strings.Contains(html, `x-for="choice in choices"`) {
		t.Error("the whitelist datalist is not built from choices")
	}
}

func TestNoBudgetTextNamesUTC(t *testing.T) {
	for _, name := range []string{"static/i18n/en.json", "static/i18n/id.json", "static/pages/endpoint.html"} {
		if strings.Contains(readWeb(t, name), "UTC") {
			t.Errorf("%s still names UTC; the windows follow the server's zone", name)
		}
	}
}

func TestSectionTitlesAreTitleCase(t *testing.T) {
	for _, name := range []string{"static/i18n/en.json", "static/i18n/id.json"} {
		var words map[string]string
		if err := json.Unmarshal([]byte(readWeb(t, name)), &words); err != nil {
			t.Fatal(err)
		}
		if v := words["provider.connections"]; v == "" || strings.ToUpper(v[:1]) != v[:1] {
			t.Errorf("%s: provider.connections = %q, want it to start with a capital", name, v)
		}
	}
}

func TestSelectAllShowsForASingleConnection(t *testing.T) {
	body := readWeb(t, "static/pages/provider.html")
	row := regexp.MustCompile(`<li class="[^"]*rl-row-select[^"]*"(?:[^>"]|"[^"]*")*>`).FindString(body)
	if row == "" {
		t.Fatal("the select-all row was not found")
	}
	if strings.Contains(row, "connections.length > 1") {
		t.Errorf("select all waits for a second connection: %s", row)
	}
}

// A list is filled from a template, so its first child is the template and no
// row is ever :first-child; the row must be found by its type.
func TestAListRowLosesItsTopBorderByTypeNotPosition(t *testing.T) {
	css := readWeb(t, "static/css/components.css")
	if strings.Contains(css, ".rl-list-row:first-child") {
		t.Error("a template is the first child of the list, so this never matches and the first row shows a double line")
	}
	if !strings.Contains(css, ".rl-list-row:first-of-type") {
		t.Error("the first row of a list keeps its top border")
	}
}

// Alpine's CSP build turns an undefined result into "" when the expression has a
// dot, and disabled="" disables the control. A map lookup is undefined until it
// is first written.
func TestNoDisabledBindingReadsAMapEntry(t *testing.T) {
	re := regexp.MustCompile(`x-bind:(?:disabled|checked)="([^"]*)"`)
	for _, name := range []string{"static/pages/endpoint.html", "static/pages/provider.html", "static/pages/providers.html", "static/shell.html"} {
		for _, m := range re.FindAllStringSubmatch(readWeb(t, name), -1) {
			if strings.Contains(m[1], "[") && !strings.Contains(m[1], "===") {
				t.Errorf("%s: %q is undefined until the entry is written, which Alpine turns into a disabled control", name, m[1])
			}
		}
	}
}

func TestEveryProviderHasAnIconOrAFallback(t *testing.T) {
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	mapped := providerIconMap(t)
	skip := providerNoIcon(t)
	for _, e := range reg.All() {
		if hasIconFile(t, e.ID) || skip[e.ID] {
			continue
		}
		target, ok := mapped[e.ID]
		if !ok {
			t.Errorf("%s has no icon file and no PROVIDER_ICONS entry", e.ID)
			continue
		}
		if !hasIconFile(t, target) {
			t.Errorf("%s maps to %s, which is not in the image directory", e.ID, target)
		}
	}
	for id, target := range mapped {
		if !hasIconFile(t, target) {
			t.Errorf("PROVIDER_ICONS maps %s to %s, which does not exist", id, target)
		}
	}
}

// providerIconMap reads the literal map out of providers.js.
func providerIconMap(t *testing.T) map[string]string {
	t.Helper()
	body := readWeb(t, "static/js/pages/providers.js")
	block := regexp.MustCompile(`(?s)const PROVIDER_ICONS = \{(.*?)\};`).FindStringSubmatch(body)
	if block == nil {
		t.Fatal("providers.js has no PROVIDER_ICONS map")
	}
	out := map[string]string{}
	for _, m := range regexp.MustCompile(`"?([a-z0-9-]+)"?\s*:\s*"([^"]+)"`).FindAllStringSubmatch(block[1], -1) {
		out[m[1]] = m[2]
	}
	return out
}

// providerNoIcon reads the ids that are meant to keep the initials tile.
func providerNoIcon(t *testing.T) map[string]bool {
	t.Helper()
	body := readWeb(t, "static/js/pages/providers.js")
	block := regexp.MustCompile(`(?s)const NO_ICON = \[(.*?)\];`).FindStringSubmatch(body)
	if block == nil {
		t.Fatal("providers.js has no NO_ICON list")
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([a-z0-9-]+)"`).FindAllStringSubmatch(block[1], -1) {
		out[m[1]] = true
	}
	return out
}

func hasIconFile(t *testing.T, id string) bool {
	t.Helper()
	for _, ext := range []string{".png", ".svg"} {
		if _, err := fs.ReadFile(web.Static, "static/img/providers/"+id+ext); err == nil {
			return true
		}
	}
	return false
}

func TestEveryProviderCardShowsAnIconWithAFallback(t *testing.T) {
	body := readWeb(t, "static/pages/providers.html")
	icons := regexp.MustCompile(`(?s)<span class="rl-provider-icon".*?</span>`).FindAllString(body, -1)
	if len(icons) != 4 {
		t.Fatalf("%d provider icon spans, want one per card block", len(icons))
	}
	for _, icon := range icons {
		if !strings.Contains(icon, "iconSrc(card)") || !strings.Contains(icon, "x-on:error") {
			t.Errorf("an icon span has no image or no fallback: %s", icon)
		}
	}
}

func TestEveryBudgetUnitStartsWithACapital(t *testing.T) {
	for _, name := range []string{"static/i18n/en.json", "static/i18n/id.json"} {
		var words map[string]string
		if err := json.Unmarshal([]byte(readWeb(t, name)), &words); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"keys.requestsUnit", "keys.tokensUnit", "keys.costUnit"} {
			v := words[key]
			if v == "" || strings.ToUpper(v[:1]) != v[:1] {
				t.Errorf("%s: %s = %q, want a capital first letter", name, key, v)
			}
		}
	}
}

// A paragraph's default bottom margin cannot collapse out of a flex row, so
// every row would be taller below than above without this rule.
func TestListRowsShareOneVerticalRhythm(t *testing.T) {
	css := readWeb(t, "static/css/components.css")
	if !regexp.MustCompile(`(?s)\.rl-list-row > \.rl-list-main > p \{[^}]*margin-block: 0\.5rem 0;`).MatchString(css) {
		t.Error("a row's paragraphs keep Tabler's bottom margin, so the rows are unevenly spaced")
	}
	if !regexp.MustCompile(`\.rl-row-select \{[^}]*padding-block: 0\.75rem`).MatchString(css) {
		t.Error("the select-all row has a different padding from the rows under it")
	}
}
