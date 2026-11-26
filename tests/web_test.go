package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"rllmx/app/gateway/plan"
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

// svgNS is the namespace a hand-drawn SVG node needs. It is an identifier, not
// an address the browser fetches, so the origin scan allows this one string.
const svgNS = "http://www.w3.org/2000/svg"

func TestOurWebFilesLoadNothingFromAnotherOrigin(t *testing.T) {
	ours(t, func(name, body string) {
		if m := absoluteURL.FindString(strings.ReplaceAll(body, svgNS, "")); m != "" {
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
		"sso.sso_invalid", "sso.sso_no_email", "sso.sso_unverified", "sso.sso_idp_error",
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

// A dialog that holds a typed secret forgets it on the dialog's own close event,
// which fires for Escape and the corner button as well as Cancel.
func TestEveryDialogWithASecretFieldForgetsItOnClose(t *testing.T) {
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		for _, block := range strings.Split(body, "<dialog")[1:] {
			block, _, _ = strings.Cut(block, "</dialog>")
			open, _, _ := strings.Cut(block, ">")
			if strings.Contains(block, `type="password"`) && !strings.Contains(open, "x-on:close=") {
				t.Errorf("%s: a dialog with a password field never forgets it on close: <dialog%s>", name, open)
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
// A button that does not apply is disabled, never left out of one row. The alias
// is cleared from its own dialog, so a row has one alias button, not two.
func TestTheModelActionButtonsAreNeverHiddenPerRow(t *testing.T) {
	for _, m := range regexp.MustCompile(`<button\b(?:[^>"]|"[^"]*")*>`).FindAllString(modelRowBlock(t), -1) {
		if strings.Contains(m, "removeCustom") && !strings.Contains(m, "x-bind:disabled") {
			t.Errorf("the delete button must be disabled for a built-in model: %s", m)
		}
		if strings.Contains(m, "removeAlias") || strings.Contains(m, "dropAlias") {
			t.Errorf("the alias is cleared in its dialog, not from the row: %s", m)
		}
		if strings.Contains(m, "removeCustom") && strings.Contains(m, "x-show=") {
			t.Errorf("a per-row x-show shifts the buttons of other rows: %s", m)
		}
	}
	body := readWeb(t, "static/pages/provider.html")
	start := strings.Index(body, `x-ref="aliasDlg"`)
	end := strings.Index(body[start:], "</dialog>")
	if start < 0 || end < 0 {
		t.Fatal("the alias dialog was not found")
	}
	dialog := body[start : start+end]
	action := ""
	for _, m := range regexp.MustCompile(`<button\b(?:[^>"]|"[^"]*")*>`).FindAllString(dialog, -1) {
		if strings.Contains(m, "dropAlias()") {
			action = m
		}
	}
	if action == "" {
		t.Fatal("the alias dialog cannot clear an alias")
	}
	if !strings.Contains(action, `x-show="aliasForm.alias"`) {
		t.Errorf("the remove action must hide when there is no alias: %s", action)
	}
	if !strings.Contains(action, "btn-outline-danger") {
		t.Errorf("the remove action is not drawn as a destructive button: %s", action)
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

// An alias key is a name no provider owns, so a picker that offers one shows a
// model that cannot be told apart from another provider's.
func TestTheModelPickerNeverOffersABareAliasAsAModel(t *testing.T) {
	js := readWeb(t, "static/js/pages/model-choices.js")
	if strings.Contains(js, "Object.keys(aliases") {
		t.Error("model-choices.js offers the alias keys, so a bare alias name reaches the picker")
	}
	if !strings.Contains(js, "Object.values(aliases") {
		t.Error("model-choices.js drops every alias target as well as its keys")
	}
	for _, want := range []string{`api("/api/models")`, `api("/api/providers")`, "combos.combos", "= liveModels(", "fullModel"} {
		if !strings.Contains(js, want) {
			t.Errorf("model-choices.js lacks %q", want)
		}
	}
	if !strings.Contains(js, "c.trim())") || !strings.Contains(js, ".filter(Boolean)") {
		t.Error("a name reaches the datalist without being trimmed, so a padded one shows up padded")
	}
}

// The Auto plan card follows the card wrapper, so the card gap has to come from
// the wrapper instead of the sibling rule.
func TestTheSkillsCardStackKeepsItsGap(t *testing.T) {
	page := readWeb(t, "static/pages/skills.html")
	css := readWeb(t, "static/css/components.css")
	if !strings.Contains(page, `class="rl-card-stack"`) {
		t.Error("the skills cards are wrapped without the class that carries their gap")
	}
	stack := strings.Index(css, ".rl-card-stack {")
	if stack < 0 || !strings.Contains(css[stack:min(stack+200, len(css))], "display: grid") || !strings.Contains(css[stack:min(stack+200, len(css))], "gap: 1rem") {
		t.Error("components.css gives the card wrapper no grid gap")
	}
	if !strings.Contains(css, ".rl-skill-group:first-of-type") {
		t.Error("every skill group draws a line, including the first one under a card header")
	}
	// A row's content stops short of its actions, so a switch does not sit
	// against the last line of text.
	if !strings.Contains(css, ".rl-list-row:has(> .rl-list-actions)") || !strings.Contains(css, "max-width: 75%") {
		t.Error("a list row runs its content into its actions")
	}
}

// A modal is left by its × as well as its footer: the × closes the same way a
// Cancel does, so every dialog carries one.
func TestEveryDialogOffersACloseButton(t *testing.T) {
	for _, name := range []string{
		"static/pages/combos.html", "static/pages/endpoint.html", "static/pages/provider.html",
		"static/pages/providers.html", "static/pages/skills.html", "static/pages/users.html",
		"static/shell.html",
	} {
		body := readWeb(t, name)
		dialogs := strings.Count(body, "<dialog")
		closes := strings.Count(body, "rl-dialog-close")
		if dialogs == 0 {
			t.Errorf("%s holds no dialog", name)
			continue
		}
		if closes != dialogs {
			t.Errorf("%s has %d dialogs and %d close buttons", name, dialogs, closes)
		}
		if !strings.Contains(body, "$el.closest('dialog').close()") {
			t.Errorf("%s: a close button does not close its dialog", name)
		}
	}
	css := readWeb(t, "static/css/components.css")
	if !strings.Contains(css, ".rl-dialog-close {") {
		t.Error("components.css places no close button")
	}
	h2 := strings.Index(css, "dialog.rl-dialog h2 {")
	if h2 < 0 || !strings.Contains(css[h2:min(h2+200, len(css))], "padding-inline-end") {
		t.Error("a dialog title can run under its close button")
	}
	// The title and the button share one 1.75rem line box, so their centres meet.
	if !strings.Contains(css, "min-block-size: 1.75rem") {
		t.Error("a dialog title and its close button have different line boxes")
	}
	close := strings.Index(css, ".rl-dialog-close {")
	if close < 0 || !strings.Contains(css[close:min(close+240, len(css))], "block-size: 1.75rem") {
		t.Error("the close button has no fixed box to align with the title")
	}
}

// A log line names the key that called it, so the page needs a column for it
// and has to read the field at its new index.
func TestTheRequestLogNamesTheKeyThatCalled(t *testing.T) {
	page := readWeb(t, "static/pages/usage.html")
	js := readWeb(t, "static/js/pages/usage.js")
	css := readWeb(t, "static/css/components.css")
	if !strings.Contains(page, `x-t="usage.apiKey"`) || !strings.Contains(page, `x-text="row.key"`) {
		t.Error("the request log has no key column")
	}
	time := strings.Index(page, `x-t="usage.time"`)
	if time < 0 {
		t.Fatal("the request log has no time column")
	}
	log := page[time:]
	key, model := strings.Index(log, `x-t="usage.apiKey"`), strings.Index(log, `x-t="usage.model"`)
	if key < 0 || model < key {
		t.Error("the key column does not follow the time column")
	}
	for _, want := range []string{"key: parts[4]", "inTokens: parts[5]", "outTokens: parts[6]", "status: parts[7]", "parts.slice(8)"} {
		if !strings.Contains(js, want) {
			t.Errorf("parseLog does not read %q", want)
		}
	}
	if !strings.Contains(methodBody(js, "logRows"), "row.key") {
		t.Error("a search cannot find a line by its key")
	}
	if !strings.Contains(page, `class="table table-sm rl-log-table"`) {
		t.Error("the request log does not carry its own table class")
	}
	rule := strings.Index(css, ".rl-log-table > :not(caption) > * > * {")
	if rule < 0 || !strings.Contains(css[rule:min(rule+200, len(css))], "padding-block") {
		t.Error("the request log keeps the dense table padding")
	}
}

// A dialog's hint line sits under its title, in the corner button's column: it
// keeps clear of the × the same way the title does.
func TestADialogSubtitleKeepsClearOfTheCloseButton(t *testing.T) {
	css := readWeb(t, "static/css/components.css")
	at := strings.Index(css, "dialog.rl-dialog > form > h2:first-child + .rl-hint {")
	if at < 0 {
		t.Fatal("a dialog subtitle is not held clear of the close button")
	}
	if !strings.Contains(css[at:min(at+200, len(css))], "padding-inline-end: 2.5rem") {
		t.Error("the subtitle rule does not clear the 1.75rem button")
	}
}

// Test again is its own row between the weights and the ranked list, so it
// touches neither the sliders above nor the heading below.
func TestTestAgainSitsOnItsOwnRowAboveTheRankedList(t *testing.T) {
	page := readWeb(t, "static/pages/combos.html")
	weights, again, ranked := strings.Index(page, `x-t="combos.weights"`), strings.Index(page, `x-t="combos.retest"`), strings.Index(page, `x-t="combos.ranked"`)
	if weights < 0 || again < 0 || ranked < 0 || again < weights || ranked < again {
		t.Fatal("the suggest dialog does not order the weights, Test again, then the ranked list")
	}
	between := page[weights:ranked]
	if strings.Contains(between, "rl-section-head") {
		t.Error("Test again still shares a row with a heading")
	}
	if !strings.Contains(page[weights:again], "rl-actions") {
		t.Error("Test again is not on a row of its own")
	}
}

// A spent key budget and a provider's pace are both 429s; the log names the
// class, and the page turns the two into words.
func TestTheRequestLogNamesABudgetRefusal(t *testing.T) {
	js := readWeb(t, "static/js/pages/usage.js")
	for _, want := range []string{`key_quota: "usage.class.key_quota"`, `rate_limit: "usage.class.rate_limit"`} {
		if !strings.Contains(js, want) {
			t.Errorf("statusWord does not map %q", want)
		}
	}
	en, id := loadTranslations(t, "en"), loadTranslations(t, "id")
	for _, key := range []string{"usage.class.key_quota", "usage.class.rate_limit"} {
		if strings.TrimSpace(en[key]) == "" || strings.TrimSpace(id[key]) == "" {
			t.Errorf("%s is missing from a locale", key)
		}
	}
	// The two 429s read apart in words, and the code survives beside them.
	for key, want := range map[string]string{
		"usage.class.key_quota":  "Budget Rate Limited",
		"usage.class.rate_limit": "Upstream Rate Limited",
	} {
		if en[key] != want {
			t.Errorf("%s reads %q, want %q", key, en[key], want)
		}
	}
	if !strings.Contains(js, `/^\d{3}$/.test(parts[0])`) {
		t.Error("a spelled-out failure drops its status code")
	}
	if !strings.Contains(readWeb(t, "static/pages/usage.html"), `x-bind:class="muted(row.account)"`) {
		t.Error("an account that was never chosen does not read as one")
	}
}

// Stacked fields live in a two-column grid; the password line belongs under the
// switch, not beside it.
func TestTheSecurityCardGivesThePasswordItsOwnRow(t *testing.T) {
	page := readWeb(t, "static/pages/settings.html")
	css := readWeb(t, "static/css/components.css")
	start := strings.Index(page, `x-t="settings.security"`)
	end := strings.Index(page[start:], "</section>")
	if start < 0 || end < 0 {
		t.Fatal("settings.html has no security card")
	}
	card := page[start : start+end]
	if strings.Contains(card, `class="rl-fields"`) {
		t.Error("the password row sits in the two-column fields grid")
	}
	if !strings.Contains(card, `class="rl-password-row"`) {
		t.Error("the security card lost its password row")
	}
	rule := strings.Index(css, ".rl-password-row {")
	if rule < 0 || !strings.Contains(css[rule:min(rule+400, len(css))], "border-block-start") {
		t.Error("the password row has no rule above it")
	}
}

func TestTheModelWhitelistOffersEveryLiveModel(t *testing.T) {
	js := readWeb(t, "static/js/pages/model-choices.js")
	for _, want := range []string{"export function liveModels", `api("/api/models")`, `api("/api/providers")`, "= liveModels("} {
		if !strings.Contains(js, want) {
			t.Errorf("model-choices.js lacks %q: a picker would offer only aliases and combos", want)
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

// Every page a browser opens first declares the icon; without the link a
// browser asks for /favicon.ico, which no route answers.
func TestEveryPageDeclaresTheFavicon(t *testing.T) {
	link := `rel="icon" href="/static/img/favicon.ico"`
	for _, page := range []string{"static/shell.html", "static/login.html", "static/callback.html"} {
		if !strings.Contains(readWeb(t, page), link) {
			t.Errorf("%s does not declare the favicon", page)
		}
	}
	ico, err := fs.ReadFile(web.Static, "static/img/favicon.ico")
	if err != nil {
		t.Fatal(err)
	}
	if len(ico) < 6 || ico[0] != 0 || ico[1] != 0 || ico[2] != 1 || ico[3] != 0 {
		t.Fatal("static/img/favicon.ico is not an ICO file")
	}
	if n := int(ico[4]) | int(ico[5])<<8; n < 2 {
		t.Errorf("the favicon carries %d image(s); a 16px tab and a larger size are both needed", n)
	}
}

func TestTheBrandShowsTheMark(t *testing.T) {
	mark := regexp.MustCompile(`<a class="rl-brand"[^>]*>(.*?)</a>`)
	for _, page := range []string{"static/shell.html", "static/login.html"} {
		m := mark.FindStringSubmatch(readWeb(t, page))
		if m == nil {
			t.Errorf("%s has no brand link", page)
			continue
		}
		if !strings.Contains(m[1], `src="/static/img/logo.png"`) || !strings.Contains(m[1], `alt=""`) {
			t.Errorf("%s: the brand link does not start with the mark and an empty alt: %s", page, m[1])
		}
	}
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

// The Show all button sits under the provider grid, not against the last card.
func TestTheProvidersShowAllButtonClearsTheGrid(t *testing.T) {
	body := readWeb(t, "static/pages/providers.html")
	css := readWeb(t, "static/css/components.css")
	if !strings.Contains(body, `class="rl-provider-more"`) {
		t.Error("the Show all buttons sit loose under the provider grid")
	}
	rule := strings.Index(css, ".rl-provider-more {")
	if rule < 0 || !strings.Contains(css[rule:min(rule+160, len(css))], "margin-block-start") {
		t.Error("the Show all row has no gap above it")
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

// The pause and play buttons name the state the click produces, not the word
// "Resume" or "Pause"; the dialog copy keeps those words.
func TestPauseAndPlayTooltipsNameTheResult(t *testing.T) {
	controlKey := regexp.MustCompile(`\bkeys\.(pause|resume)\b`)
	for _, name := range []string{"static/pages/endpoint.html", "static/pages/provider.html", "static/pages/providers.html"} {
		body := readWeb(t, name)
		if controlKey.MatchString(body) {
			t.Errorf("%s: a pause control still says Resume or Pause", name)
		}
	}
	if body := readWeb(t, "static/pages/endpoint.html"); !strings.Contains(body, `x-tip="keys.inactive"`) || !strings.Contains(body, `x-tip="keys.active"`) {
		t.Error("the key pause and resume buttons do not name Active and Inactive")
	}
	for _, name := range []string{"static/pages/provider.html", "static/pages/providers.html"} {
		if body := readWeb(t, name); !strings.Contains(body, "? label('keys.active') : label('keys.inactive')") {
			t.Errorf("%s: the connection toggle still names Resume or Pause", name)
		}
	}
}

// "OpenAI- " was the suspended hyphen that read as a stray dash when the hint
// wrapped. The hyphen inside "OpenAI-compatible" is a normal compound.
func TestNoProviderHintLeavesAWordOnAHyphen(t *testing.T) {
	suspended := regexp.MustCompile(`\w+-\s`)
	for _, locale := range []string{"en", "id"} {
		for key, value := range loadTranslations(t, locale) {
			if suspended.MatchString(value) {
				t.Errorf("%s.json %s ends a word in a hyphen: %q", locale, key, value)
			}
		}
	}
}

// The old badge printed the count, the lowercase word and a tag that said
// "Error" again. One helper now builds "2 Error (429)".
func TestAProviderCardShowsOneFailureBadge(t *testing.T) {
	body := readWeb(t, "static/pages/providers.html")
	if !strings.Contains(body, `x-text="errorBadge(card)"`) {
		t.Error("the failure badge is not built by errorBadge")
	}
	if strings.Contains(body, `x-t="providers.error"`) {
		t.Error("the failure badge prints the word beside the tag again")
	}
	if !strings.Contains(readWeb(t, "static/js/pages/providers.js"), "errorBadge(card) {") {
		t.Error("providers.js has no errorBadge")
	}
	for _, locale := range []string{"en", "id"} {
		if value := loadTranslations(t, locale)["providers.connected"]; value == "" || strings.ToUpper(value[:1]) != value[:1] {
			t.Errorf("%s.json providers.connected does not start with a capital: %q", locale, value)
		}
	}
}

// A connection that healed keeps its last failure out of sight: a stored code
// or message shows only while the connection cools down, failed a check, or its
// login expired.
func TestAHealedConnectionShowsNoFailureBadge(t *testing.T) {
	js := readWeb(t, "static/js/pages/providers.js")
	for _, want := range []string{
		"export function showsFailure(conn, now)",
		"export function currentTag(conn, now)",
		`effectiveStatus(conn, now) === "unavailable" ? errorTag(conn) : ""`,
		"showsFailure(conn, now) && conn.lastErrorAt",
		`tag: latest ? currentTag(latest, now) : ""`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("providers.js lacks %q", want)
		}
	}
	if page := readWeb(t, "static/pages/provider.html"); !strings.Contains(page, `x-show="failed(conn) && conn.lastError"`) {
		t.Error("the row's message is not tied to the connection's state")
	}
	pjs := readWeb(t, "static/js/pages/provider.js")
	for _, want := range []string{"failed(conn) {", "return showsFailure(conn, this.now)", "return currentTag(conn, this.now)"} {
		if !strings.Contains(pjs, want) {
			t.Errorf("provider.js lacks %q", want)
		}
	}
}

// Copying a key is silent; the eye button is the only way to put it on screen.
func TestCopyingAKeyDoesNotRevealIt(t *testing.T) {
	js := readWeb(t, "static/js/pages/endpoint.js")
	start := strings.Index(js, "async copyKey(")
	if start < 0 {
		t.Fatal("copyKey was not found")
	}
	end := strings.Index(js[start:], "async toggleActive(")
	if end < 0 {
		t.Fatal("the end of copyKey was not found")
	}
	if strings.Contains(js[start:start+end], "reveal(") {
		t.Error("copyKey reveals the key on screen")
	}
}

// The card body already pads; a first row that pads again leaves the list
// looking indented under its header.
func TestAFirstListRowAddsNoTopPadding(t *testing.T) {
	css := readWeb(t, "static/css/components.css")
	if !regexp.MustCompile(`(?s)\.rl-list-row:first-of-type \{[^}]*padding-block-start: 0;`).MatchString(css) {
		t.Error("the first row keeps its own top padding above the card's")
	}
	if !regexp.MustCompile(`(?s)dialog\.rl-dialog \.form-check \{[^}]*margin-block-start: 0\.75rem;`).MatchString(css) {
		t.Error("a checkbox in a dialog sits flush against the field above it")
	}
	block := regexp.MustCompile(`(?s)\.rl-provider-name:hover \{[^}]*\}`).FindString(css)
	if block == "" {
		t.Fatal("the provider-name hover rule was not found")
	}
	if !strings.Contains(block, "text-decoration: none;") {
		t.Errorf("a hovered provider name is underlined: %s", block)
	}
}

// Deleting stored data is red and asks first. The handlers that delete are
// checked in their own module.
func TestDeleteButtonsAreRedAndAskFirst(t *testing.T) {
	button := regexp.MustCompile(`(?s)<button\b((?:[^>"]|"[^"]*")*)>(.*?)</button>`)
	handlers := map[string]bool{}
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		for _, m := range button.FindAllStringSubmatch(body, -1) {
			if !strings.Contains(m[2], `x-icon="trash"`) {
				continue
			}
			// The quiet style for an icon alone, the outline style for a label;
			// TestADeleteWithTextReadsAsADangerButton holds that split.
			if !strings.Contains(m[1], "danger") {
				t.Errorf("%s: a delete button is not drawn as destructive: <button%s>", name, strings.TrimSpace(m[1]))
			}
			for _, h := range regexp.MustCompile(`x-on:click="([A-Za-z]+)\(`).FindAllStringSubmatch(m[1], -1) {
				handlers[h[1]] = true
			}
		}
	})
	if len(handlers) == 0 {
		t.Fatal("no delete handler was found")
	}
	for _, h := range []string{"remove", "removeSelected", "deleteNode", "removeCustom", "dropAlias", "removeCombo"} {
		if !handlers[h] {
			t.Errorf("no delete button calls %s", h)
		}
	}
	var pages strings.Builder
	for _, name := range []string{"endpoint.js", "provider-connections.js", "provider-models.js", "provider-settings.js", "combos.js", "console-log.js", "pricing.js", "skills-studio.js", "users.js"} {
		pages.WriteString(readWeb(t, "static/js/pages/"+name))
		pages.WriteString("\n")
	}
	for h := range handlers {
		ask := regexp.MustCompile(`(?s)(?:async )?` + h + `\([^)]*\)\s*\{.{0,400}?ask\(`)
		if !ask.MatchString(pages.String()) {
			t.Errorf("%s deletes without asking first", h)
		}
	}
}

func TestTheBackLinkCarriesAnArrow(t *testing.T) {
	body := readWeb(t, "static/pages/provider.html")
	line := ""
	for l := range strings.SplitSeq(body, "\n") {
		if strings.Contains(l, "provider.back") {
			line = l
		}
	}
	if line == "" {
		t.Fatal("the back link was not found")
	}
	if !strings.Contains(line, `x-icon="arrow-left"`) {
		t.Errorf("the back link has no arrow: %s", strings.TrimSpace(line))
	}
}

// A standard user configures nothing; the console narrows the same pages it
// narrows on the server, so a refused control is also not shown.
func TestAStandardUserSeesNoAdminControls(t *testing.T) {
	shell := readWeb(t, "static/shell.html")
	nav := ""
	for l := range strings.SplitSeq(shell, "\n") {
		if strings.Contains(l, "/dashboard/providers") && strings.Contains(l, "<li") {
			nav = l
		}
	}
	if !strings.Contains(nav, `x-show="isAdmin()"`) {
		t.Errorf("the providers entry is offered to every role: %s", strings.TrimSpace(nav))
	}

	endpoint := readWeb(t, "static/pages/endpoint.html")
	buttons := regexp.MustCompile(`<button\b(?:[^>"]|"[^"]*")*>`).FindAllString(endpoint, -1)
	for _, call := range []string{"openCreate()", "remove(key)"} {
		found := false
		for _, tag := range buttons {
			if !strings.Contains(tag, call) {
				continue
			}
			found = true
			if !strings.Contains(tag, `x-show="isAdmin()"`) {
				t.Errorf("%s is offered to every role: %s", call, tag)
			}
		}
		if !found {
			t.Errorf("no button calls %s", call)
		}
	}
	if !strings.Contains(endpoint, `x-on:click="openEdit(key)"`) {
		t.Error("a standard user can rename nothing: the edit button is admin only")
	}
	// Everything a user may not change sits in one fieldset the role disables.
	if !strings.Contains(endpoint, `<fieldset class="rl-fieldset" x-bind:disabled="!isAdmin()">`) {
		t.Error("the edit dialog does not disable the admin-only fields for a user")
	}
	for _, id := range []string{"create-owner", "edit-owner"} {
		if !strings.Contains(endpoint, `id="`+id+`"`) {
			t.Errorf("the key dialog has no %s owner control", id)
		}
	}
	// A standard user sees their own name where the owner would be, not a
	// disabled select reading "Nobody".
	if !strings.Contains(endpoint, `x-bind:value="meName"`) || !strings.Contains(endpoint, `x-if="!isAdmin()"`) {
		t.Error("a standard user sees the owner select instead of their own name")
	}
	if !strings.Contains(readWeb(t, "static/js/pages/endpoint.js"), "meName = who.user") {
		t.Error("a standard user's own name is not read for the owner field")
	}
	if strings.Contains(endpoint, `x-t="users.title"`) {
		t.Error("the users card still lives on the endpoint page")
	}
	// The accounts page is admin-only in the sidebar, which is its only gate.
	for l := range strings.SplitSeq(shell, "\n") {
		if strings.Contains(l, "/dashboard/users") && strings.Contains(l, "<li") && !strings.Contains(l, `x-show="isAdmin()"`) {
			t.Errorf("the users entry is offered to every role: %s", strings.TrimSpace(l))
		}
	}
	if !strings.Contains(readWeb(t, "static/pages/users.html"), `x-t="users.title"`) {
		t.Error("the users page is missing")
	}
	if !strings.Contains(readWeb(t, "static/js/pages/endpoint.js"), "this.role = who.role") {
		t.Error("the page never reads its role")
	}
}

func TestTheSignInFormOffersAUsername(t *testing.T) {
	body := readWeb(t, "static/login.html")
	if !strings.Contains(body, `autocomplete="username"`) {
		t.Error("the sign-in form has no username field")
	}
	js := readWeb(t, "static/js/login.js")
	if !strings.Contains(js, "username: this.username") {
		t.Error("the form never sends the username")
	}
}

// The CAPTCHA widget has to sit inside the form and follow the page's theme,
// not the provider's default.
func TestTheCaptchaWidgetFollowsTheFormAndTheTheme(t *testing.T) {
	js := readWeb(t, "static/js/login.js")
	if !strings.Contains(js, `getAttribute("data-bs-theme")`) || !strings.Contains(js, "theme:") {
		t.Error("the CAPTCHA does not follow the page's theme")
	}
	if !strings.Contains(js, `provider === "turnstile"`) || !strings.Contains(js, `size = "flexible"`) {
		t.Error("the Turnstile widget does not fill the form")
	}
	css := readWeb(t, "static/css/components.css")
	start := strings.Index(css, ".rl-captcha {")
	if start < 0 {
		t.Fatal("the CAPTCHA slot has no rule")
	}
	end := strings.Index(css[start:], "}")
	rule := css[start : start+end]
	for _, want := range []string{"display: flex", "justify-content: center", "width: 100%"} {
		if !strings.Contains(rule, want) {
			t.Errorf("the CAPTCHA slot is missing %q", want)
		}
	}
}

// A row with actions is as tall as its buttons, so its text and drag handle have
// to sit on the buttons' line rather than the row's top.
func TestAListRowCentresItsContentAgainstItsActions(t *testing.T) {
	css := readWeb(t, "static/css/components.css")
	start := strings.Index(css, ".rl-list-row:has(> .rl-list-actions) {")
	if start < 0 {
		t.Fatal("an action row has no rule")
	}
	end := strings.Index(css[start:], "}")
	if rule := css[start : start+end]; !strings.Contains(rule, "align-items: center") {
		t.Error("an action row aligns its content to the top")
	}
}

// The expiry dialog carries five columns, and its refills must read like the
// quota table's windows instead of the provider's own ids.
func TestTheExpiryDialogIsWideAndNamesItsWindows(t *testing.T) {
	if page := readWeb(t, "static/pages/quota.html"); !strings.Contains(page, `class="rl-dialog rl-dialog-lg"`) {
		t.Error("the expiry dialog keeps the narrow width")
	}
	css := readWeb(t, "static/css/components.css")
	start := strings.Index(css, "dialog.rl-dialog-lg {")
	if start < 0 {
		t.Fatal("the wide dialog has no rule")
	}
	end := strings.Index(css[start:], "}")
	if rule := css[start : start+end]; !strings.Contains(rule, "width: min(48rem") {
		t.Error("the wide dialog is no wider than the base one")
	}
	js := readWeb(t, "static/js/pages/quota.js")
	if expiry := methodBody(js, "async openExpiry"); !strings.Contains(expiry, `clears || []).map(windowName)`) {
		t.Error("the expiry table prints a raw window id")
	}
	for _, want := range []string{`case "five_hour":`, `case "seven_day":`, `startsWith("seven_day_")`, `"Session (5h)"`, `"Weekly (7d)"`} {
		if !strings.Contains(js, want) {
			t.Errorf("windowName lacks %q", want)
		}
	}
}

// An account single sign-on provisioned has no password the user knows, so the
// dialog asks only for the new one until they set it.
func TestASetPasswordDialogAsksNoCurrentPasswordWhenNoneIsKnown(t *testing.T) {
	shell := readWeb(t, "static/shell.html")
	if !strings.Contains(shell, `x-show="!needsPassword"`) {
		t.Error("the current-password field is shown to a user who has none")
	}
	app := readWeb(t, "static/js/app.js")
	for _, want := range []string{"this.needsPassword = who.needsPassword", `this.needsPassword ? "" : this.pw.current`} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	users := readWeb(t, "static/pages/users.html")
	if !strings.Contains(users, "user.username") {
		t.Error("the users page still reads a field the API does not send")
	}
	if strings.Contains(users, "user.name") {
		t.Error("the users page reads user.name, which the API no longer sends")
	}
	if !strings.Contains(users, `x-t="users.sso"`) {
		t.Error("a provisioned account is not marked")
	}
}

// methodBody is the text of one method in a page component, matched by braces,
// so a scan can look inside it without matching the rest of the file.
func methodBody(body, name string) string {
	start := strings.Index(body, "\n  "+name+"(")
	if start < 0 {
		start = strings.Index(body, "\n  async "+name+"(")
	}
	if start < 0 {
		return ""
	}
	open := start + strings.Index(body[start:], "{")
	depth := 0
	for i := open; i < len(body); i++ {
		switch body[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[open : i+1]
			}
		}
	}
	return ""
}

// A stream outlives the fragment unless the page closes it: the router destroys
// the old tree, and a stream nobody closes keeps writing into a detached node.
func TestEveryPageScriptClosesItsEventSource(t *testing.T) {
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".js") || !strings.Contains(body, "new EventSource(") {
			return
		}
		destroy := methodBody(body, "destroy")
		if destroy == "" {
			t.Errorf("%s opens a stream and defines no destroy", name)
			return
		}
		if !strings.Contains(destroy, ".close()") {
			t.Errorf("%s: destroy does not close the stream", name)
		}
	})
}

// A Chart.js instance holds a canvas and its own listeners; the page that built
// it has to destroy it when the router leaves the page.
func TestEveryChartIsBuiltAndDestroyedByThePage(t *testing.T) {
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".js") || !strings.Contains(body, "makeChart(") || name == "static/js/ui/charts.js" {
			return
		}
		if !strings.Contains(body, "destroyChart(") {
			t.Errorf("%s builds a chart and does not destroy it", name)
		}
	})
}

// The strategy select writes the four names the combo policy acts on, and the
// router policy offers the reference's four, because a stored fifth value would
// save and do nothing.
func TestTheComboStrategySelectOffersTheServerValues(t *testing.T) {
	body := readWeb(t, "static/pages/combos.html")
	for _, want := range []string{`value="fallback"`, `value="round-robin"`, `value="smart"`, `value="fusion"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the strategy select lacks %s", want)
		}
	}
	for _, want := range []string{`value="smart"`, `value="strict"`, `value="per-turn"`, `value="custom"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the router policy select lacks %s", want)
		}
	}
	js := readWeb(t, "static/js/pages/combos.js")
	if !strings.Contains(js, "patch.confidenceThreshold = 0.8") {
		t.Error("the smart policy does not write the 80% threshold the policy reads")
	}
}

// Drag order is a nicety; the arrows are what a keyboard can reach. Both move
// the same array, and the page never sends the DOM's order.
func TestTheComboModelEditorKeepsAnAccessibleOrder(t *testing.T) {
	body := readWeb(t, "static/pages/combos.html")
	for _, want := range []string{`x-tip="combos.moveUp"`, `x-tip="combos.moveDown"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the model editor has no %s button", want)
		}
	}
	if !strings.Contains(body, `x-text="model"`) {
		t.Error("a chosen model is not shown as text")
	}
	js := readWeb(t, "static/js/pages/combo-models.js")
	if !strings.Contains(js, "models.splice(from, 1)") {
		t.Error("a reorder does not move the stored list")
	}
	if strings.Contains(js, "setModelAt") || strings.Contains(body, "setModelAt") {
		t.Error("a model row is still an editable input with no picker")
	}
}

// A standard user may read the totals, the chart and the log; the live feed,
// the active count and the latency stream are admin reads, so the page asks for
// them only behind isAdmin().
func TestTheUsagePageAsksOnlyForWhatTheRoleAllows(t *testing.T) {
	js := readWeb(t, "static/js/pages/usage.js")
	for _, path := range []string{"/api/usage/stats", "/api/usage/chart", "/api/usage/logs"} {
		if !strings.Contains(js, path) {
			t.Errorf("usage.js never reads %s", path)
		}
	}
	if !strings.Contains(js, "if (this.isAdmin()) {") {
		t.Error("the page does not gate the admin reads on the role")
	}
	for _, path := range []string{"/api/usage/stream", "/api/usage/active-providers", "/api/health/latency-stream"} {
		at := strings.Index(js, path)
		if at < 0 {
			t.Errorf("usage.js never reads %s", path)
			continue
		}
		start := strings.Index(js, "async start()")
		if start < 0 || at < start {
			t.Errorf("%s is read outside start()", path)
		}
	}
	body := readWeb(t, "static/pages/usage.html")
	for _, value := range []string{"today", "24h", "7d", "30d", "60d", "all"} {
		if !strings.Contains(body, `value="`+value+`"`) {
			t.Errorf("the period select lacks %q", value)
		}
	}
}

// One reading per card is a provider call, so the client list pages; a great
// many accounts must not become one unstyled list.
func TestTheQuotaPagePagesItsAccountsAndLimits(t *testing.T) {
	body := readWeb(t, "static/pages/quota.html")
	for _, want := range []string{`value="10"`, `value="20"`, `value="50"`, `value="100"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page-size select lacks %s", want)
		}
	}
	js := readWeb(t, "static/js/pages/quota.js")
	if !strings.Contains(js, "const quotaPageSize = 10") {
		t.Error("a card shows every limit at once")
	}
	if !strings.Contains(js, "async function readQuota") {
		t.Error("a 401 from a quota reading would send the browser to the sign-in page")
	}
}

// A skill row's routing select carries the same two choices as the Token Saver
// page, and reads them in the same words.
func TestTheSkillsRoutingSelectReadsLikeTheTokenSaver(t *testing.T) {
	page := readWeb(t, "static/pages/skills.html")
	for _, want := range []string{`<option value="smart" x-t="saver.smart">`, `<option value="always" x-t="saver.always">`} {
		if !strings.Contains(page, want) {
			t.Errorf("skills.html lacks %q", want)
		}
	}
	for _, gone := range []string{`x-t="skills.smart"`, `x-t="skills.always"`} {
		if strings.Contains(page, gone) {
			t.Errorf("skills.html still uses %q", gone)
		}
	}
	for _, name := range []string{"static/i18n/en.json", "static/i18n/id.json"} {
		var words map[string]string
		if err := json.Unmarshal([]byte(readWeb(t, name)), &words); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"skills.smart", "skills.always"} {
			if _, kept := words[key]; kept {
				t.Errorf("%s still carries %q", name, key)
			}
		}
	}
}

// A provider names some windows in its own lowercase ("session", "weekly");
// the table writes them as its own labels do. A name with a digit, a space or
// a capital is the provider's or a model's and is left alone.
func TestTheQuotaPageTitlecasesAProviderWindowName(t *testing.T) {
	js := readWeb(t, "static/js/pages/quota.js")
	for _, want := range []string{
		`const windowWords = ["session", "weekly", "monthly", "rolling", "hourly", "daily"]`,
		"function windowTitle(name)",
		"name: windowTitle(row.displayName || key)",
		"return windowTitle(clear)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("quota.js lacks %q", want)
		}
	}
}

// The console page follows the server's stream; it has nothing to poll and
// nothing to read from disk.
func TestTheConsoleLogPageReadsOnlyTheStream(t *testing.T) {
	js := readWeb(t, "static/js/pages/console-log.js")
	if !strings.Contains(js, `new EventSource("/api/translator/console-logs/stream")`) {
		t.Error("the page does not open the log stream")
	}
	if strings.Contains(js, "setInterval(") {
		t.Error("the page polls; the stream already pushes")
	}
	if strings.Contains(js, "/api/translator/console-logs?") {
		t.Error("the page fetches the ring instead of taking the stream's init frame")
	}
}

// The settings page writes the keys the controller validates. A key the page
// sends but the allow list does not hold is a 400 a person finds in a browser.
func TestTheSettingsPageWritesOnlyAllowedKeys(t *testing.T) {
	raw, err := os.ReadFile("../app/http/controllers/settings_controller.go")
	if err != nil {
		t.Fatal(err)
	}
	controller := string(raw)
	js := readWeb(t, "static/js/pages/settings.js")
	for _, key := range []string{
		"requireLogin", "authMode", "ssoType", "oidcIssuerUrl", "oidcClientId", "oidcClientSecret",
		"oidcScopes", "oidcLoginLabel", "samlEntryPoint", "samlIssuer", "samlCert", "samlIdpEntityId",
		"samlLoginLabel", "samlAttributeEmail", "samlAttributeName", "fallbackStrategy",
		"stickyRoundRobinLimit", "comboStrategy", "comboStickyRoundRobinLimit",
	} {
		if !strings.Contains(js, key) {
			t.Errorf("settings.js never writes %s", key)
		}
		if !strings.Contains(controller, `"`+key+`"`) {
			t.Errorf("settingsRules does not accept %s", key)
		}
	}
}

// The client secret is write-only: the server never returns it, and the page
// never puts a stored value back into the field.
func TestTheOIDCSecretIsWriteOnly(t *testing.T) {
	body := readWeb(t, "static/pages/settings.html")
	if !strings.Contains(body, `id="oidc-secret" type="password"`) {
		t.Error("the client secret field is not a password field")
	}
	js := readWeb(t, "static/js/pages/settings.js")
	if strings.Contains(js, "settings.oidcClientSecret") {
		t.Error("the page reads the stored secret back")
	}
	if !strings.Contains(js, `if (secret !== "") {`) {
		t.Error("an empty secret field would overwrite the stored secret")
	}
}

// The addresses the IdP is configured with are this origin's, never a value
// from the store.
func TestTheSSOAddressesComeFromTheBrowser(t *testing.T) {
	js := readWeb(t, "static/js/pages/settings.js")
	for _, want := range []string{`location.origin + "/api/auth/oidc/callback"`, `location.origin + "/api/auth/saml/acs"`, `location.origin + "/api/auth/saml/metadata"`} {
		if !strings.Contains(js, want) {
			t.Errorf("settings.js lacks %s", want)
		}
	}
}

// A price is a non-negative number; the input says so and the page drops a
// value it cannot read rather than storing a NaN.
func TestAPriceInputIsANonNegativeNumber(t *testing.T) {
	body := readWeb(t, "static/pages/pricing.html")
	if !strings.Contains(body, `type="number" min="0" step="0.01"`) {
		t.Error("a price input is not a bounded number field")
	}
	js := readWeb(t, "static/js/pages/pricing.js")
	if !strings.Contains(js, "if (isNaN(value) || value < 0) {") {
		t.Error("pricing.js stores a value it cannot read")
	}
	if !strings.Contains(js, "if (Object.keys(body).length === 0) {") {
		t.Error("a save with nothing edited would still send a write")
	}
}

// A hook the manifest check refuses would be a save that can never succeed.
func TestTheStudioOffersOnlyAcceptedHooks(t *testing.T) {
	js := readWeb(t, "static/js/pages/skills-studio.js")
	for _, hook := range []string{"system-prompt", "pre-route", "pre-request"} {
		if !strings.Contains(js, `"`+hook+`"`) {
			t.Errorf("the studio never offers the %s hook", hook)
		}
	}
	manifest, err := os.ReadFile("../app/gateway/skill/manifest.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"system-prompt", "pre-route", "pre-request"} {
		if !strings.Contains(string(manifest), `"`+hook+`": true`) {
			t.Errorf("the manifest check does not accept %s", hook)
		}
	}
}

// The draft is data. A field that names a process or claims another writer's
// folder is refused by the server, so the page must never send one.
func TestTheStudioSendsNoRefusedKey(t *testing.T) {
	js := readWeb(t, "static/js/pages/skills-studio.js")
	for _, key := range []string{"hook_script", "install_command", "update_command", "uninstall_command"} {
		if strings.Contains(js, key) {
			t.Errorf("the studio writes %s", key)
		}
	}
}

// The Studio asks for a name and nothing else: the server derives the id from
// it, so the dialog offers no id field and the draft sends none.
func TestTheStudioAsksForANameNotAnID(t *testing.T) {
	page := readWeb(t, "static/pages/skills.html")
	for _, gone := range []string{"studio-id", "setStudioID", "skills.studioID"} {
		if strings.Contains(page, gone) {
			t.Errorf("the studio form still carries %s", gone)
		}
	}
	js := readWeb(t, "static/js/pages/skills-studio.js")
	if strings.Contains(js, "body.id") || strings.Contains(js, "setStudioID") {
		t.Error("the studio draft still sends or edits an id")
	}
}

// The model list in the combo dialog is a section of its own, so its heading
// is a title rather than the count label the badge reuses.
func TestTheComboModelHeadingIsATitle(t *testing.T) {
	page := readWeb(t, "static/pages/combos.html")
	if !strings.Contains(page, `x-t="combos.modelsTitle"`) {
		t.Fatal("the model heading reuses the count label")
	}
	for _, name := range []string{"static/i18n/en.json", "static/i18n/id.json"} {
		var words map[string]string
		if err := json.Unmarshal([]byte(readWeb(t, name)), &words); err != nil {
			t.Fatal(err)
		}
		if v := words["combos.modelsTitle"]; v == "" || strings.ToUpper(v[:1]) != v[:1] {
			t.Errorf("%s: combos.modelsTitle = %q, want it to start with a capital", name, v)
		}
	}
}

// The button that adds a provider field belongs to the block above it, not to
// the last row inside it, so it stands clear of the rows in both dialogs.
func TestTheAddFieldButtonStandsClearOfTheRows(t *testing.T) {
	page := readWeb(t, "static/pages/provider.html")
	if n := strings.Count(page, `class="btn btn-sm rl-field-add"`); n != 2 {
		t.Errorf("%d add-field buttons carry the class, want 2 (add and edit)", n)
	}
	css := readWeb(t, "static/css/components.css")
	i := strings.Index(css, ".rl-field-add {")
	if i < 0 || !strings.Contains(css[i:min(i+120, len(css))], "margin-block-start: 0.75rem") {
		t.Error("the add-field button has no gap above it")
	}
}

// The suggest dialog reads like the rest of the page: each weight shows its
// value under its label, the fields sit on the shared grid, and the ranked
// models hang under a heading with the re-probe beside it. A model row keeps
// its three buttons on one line however long the name is.
func TestTheSuggestDialogUsesThePageComponents(t *testing.T) {
	page := readWeb(t, "static/pages/combos.html")
	start := strings.Index(page, `x-ref="suggest"`)
	if start < 0 {
		t.Fatal("the page has no suggest dialog")
	}
	dlg := page[start:]
	dlg = dlg[:strings.Index(dlg, "</dialog>")]
	if n := strings.Count(dlg, `class="rl-range-value"`); n != 4 {
		t.Errorf("the suggest dialog shows %d weight values, want 4", n)
	}
	if !strings.Contains(dlg, `class="rl-fields"`) {
		t.Error("the suggest fields are not on the shared grid")
	}
	if strings.Contains(dlg, "form-control-sm") {
		t.Error("the suggest dialog uses the small input the rest of the page does not")
	}
	if !strings.Contains(dlg, `x-t="combos.ranked"`) {
		t.Error("the ranked models have no heading of their own")
	}
	css := readWeb(t, "static/css/components.css")
	i := strings.Index(css, ".rl-model-row .rl-list-actions {")
	if i < 0 || !strings.Contains(css[i:min(i+160, len(css))], "flex-wrap: nowrap") {
		t.Error("a model row's actions still shrink and wrap beside a long name")
	}
	// The adapter pools draw the same three buttons beside a name, so they take
	// the same rule.
	for _, name := range []string{"vision", "audioInput"} {
		block := page[strings.Index(page, `adapterBlock('`+name+`').models`):]
		block = block[:strings.Index(block, "</ul>")]
		if !strings.Contains(block, "rl-model-row") {
			t.Errorf("the %s adapter pool does not use the model row", name)
		}
	}
}

// Any member can answer, so the only window a combo can promise is its
// smallest. The reference's card takes the largest output instead, which
// overstates what the combo serves.
func TestTheComboListShowsTheSmallestLimits(t *testing.T) {
	js := readWeb(t, "static/js/pages/combos.js")
	body := methodBody(js, "comboLimits")
	if body == "" {
		t.Fatal("combos.js has no comboLimits")
	}
	if !strings.Contains(body, "Math.min") {
		t.Error("a limit is not folded with Math.min")
	}
	if strings.Contains(body, "Math.max") {
		t.Error("a limit is folded with Math.max, which promises more than a member serves")
	}
	if !strings.Contains(body, "seen") || !strings.Contains(body, "> 3") {
		t.Error("a nested combo is followed with no visited set or depth bound")
	}
	if !strings.Contains(body, "compactTokens") {
		t.Error("the limits are not written by the shared formatter")
	}
	page := readWeb(t, "static/pages/combos.html")
	for _, want := range []string{"comboLimits(combo).context", "comboLimits(combo).output"} {
		if !strings.Contains(page, want) {
			t.Errorf("the combo row does not show %s", want)
		}
	}
	// The row's shared paragraph rule outranks a bare class, so the line needs
	// a selector at least as specific as that rule to keep its own gap.
	css := readWeb(t, "static/css/components.css")
	if !strings.Contains(css, ".rl-combo-row .rl-list-main > .rl-combo-limits") {
		t.Error("the limits line has no rule of its own under the chips")
	}
}

// Every saver row writes a key the settings write accepts; a key outside the
// allow list is a switch that answers 400.
func TestTheSaverKeysAreInTheAllowList(t *testing.T) {
	raw, err := os.ReadFile("../app/http/controllers/settings_controller.go")
	if err != nil {
		t.Fatal(err)
	}
	controller := string(raw)
	body := readWeb(t, "static/pages/token-saver.html")
	for _, key := range []string{
		"rtkEnabled", "headroomEnabled", "headroomUrl", "headroomCompressUserMessages", "headroomTimeoutMs",
		"cavemanEnabled", "cavemanLevel", "cavemanRoutingMode", "ponytailEnabled", "ponytailLevel",
		"ponytailRoutingMode", "customPromptEnabled", "customPrompt",
	} {
		if !strings.Contains(body, key) {
			t.Errorf("token-saver.html never writes %s", key)
		}
		if !strings.Contains(controller, `"`+key+`"`) {
			t.Errorf("settingsRules does not accept %s", key)
		}
	}
}

// The skills page writes the global switch and the plan keys; a key outside the
// allow list would answer 400.
func TestTheSkillsPageWritesOnlyAllowedKeys(t *testing.T) {
	raw, err := os.ReadFile("../app/http/controllers/settings_controller.go")
	if err != nil {
		t.Fatal(err)
	}
	controller := string(raw)
	page := readWeb(t, "static/pages/skills.html")
	js := readWeb(t, "static/js/pages/skills.js")
	for _, key := range []string{
		"autoPlanEnabled", "autoPlanMode", "autoPlanComboId", "autoCodeComboId",
		"autoPlanComplexityThreshold", "autoPlanSmartClassify", "autoPlanMaxTokens", "autoPlanTimeoutMs",
		"autoPlanShowInResponse",
	} {
		if !strings.Contains(page, "'"+key+"'") {
			t.Errorf("skills.html never writes %s", key)
		}
		if !strings.Contains(controller, `"`+key+`"`) {
			t.Errorf("settingsRules does not accept %s", key)
		}
	}
	if !strings.Contains(js, "globalSkillsEnabled") || !strings.Contains(controller, `"globalSkillsEnabled"`) {
		t.Error("the global skills switch is not written or not accepted")
	}
	for _, key := range []string{"ecc_auto_skill_routerEnabled", "ecc_auto_skill_routerConfidence", "ecc_auto_skill_routerMaxSkills",
		"builtinSkillsEnabled", "customSkillsEnabled", "eccSkillsEnabled"} {
		if !strings.Contains(js, key) {
			t.Errorf("skills.js never writes %s", key)
		}
		if !strings.Contains(controller, `"`+key+`"`) {
			t.Errorf("settingsRules does not accept %s", key)
		}
	}
}

// A card's switch is a kill switch for its group: it never writes a row's own
// switch or starts the router, and REA asks before it is enabled.
func TestTheSkillsCardsAreKillSwitches(t *testing.T) {
	js := readWeb(t, "static/js/pages/skills.js")
	page := readWeb(t, "static/pages/skills.html")
	body := methodBody(js, "toggleCard")
	for _, key := range []string{"builtinSkillsEnabled", "customSkillsEnabled", "eccSkillsEnabled"} {
		if !strings.Contains(body, key) {
			t.Errorf("the card switch never writes %s", key)
		}
	}
	if strings.Contains(body, "Enabled\"]") || strings.Contains(body, "globalSkillsEnabled") {
		t.Error("a card switch writes a row's switch or the master")
	}
	if !strings.Contains(methodBody(js, "setRouter"), "ecc_auto_skill_routerEnabled") {
		t.Error("the catalog router has no switch of its own")
	}
	for _, want := range []string{"skills.eccRouter", "skills.groupOff", "rowOn(skill)", `x-t="skills.reaHint"`} {
		if !strings.Contains(page, want) {
			t.Errorf("skills.html lacks %s", want)
		}
	}
	toggle := methodBody(js, "async toggle")
	if !strings.Contains(toggle, "rea-allow") || !strings.Contains(toggle, "ask(") {
		t.Error("REA Allow can be enabled with no warning")
	}
}

// The built-in list, the imported catalog and the operator's own skills each get
// a card; the catalog's switch is the auto-router's, not the master one.
func TestTheSkillsPageSeparatesTheCatalogFromTheBuiltInList(t *testing.T) {
	page := readWeb(t, "static/pages/skills.html")
	js := readWeb(t, "static/js/pages/skills.js")
	if !strings.Contains(page, `x-for="card in cards()"`) {
		t.Error("skills.html does not draw one card per list")
	}
	// The plan card opens the page, and every card shares one stack so the gap
	// between them comes from one rule.
	plan := strings.Index(page, "skills.planTitle")
	drawn := strings.Index(page, `x-for="card in cards()"`)
	if plan < 0 || drawn < 0 || plan > drawn {
		t.Error("the Auto-Plan card does not open the card stack")
	}
	if !strings.Contains(page, `class="rl-card-stack"`) || strings.Contains(page, `class="rl-card-stack" x-show=`) {
		t.Error("the card stack is conditional, so the plan card cannot live in it")
	}
	if !strings.Contains(page, `<section class="card" x-show="!loading && visible().length > 0">`) {
		t.Error("a list card renders while the list has no rows")
	}
	for _, want := range []string{"skills.builtinTitle", "skills.catalogTitle", "skills.groupCustom", "cards()"} {
		if !strings.Contains(js, want) {
			t.Errorf("skills.js lacks %s", want)
		}
	}
	cards := methodBody(js, "cards")
	if !strings.Contains(cards, "subgroups") {
		t.Error("skills.js builds no card with its subgroups")
	}
	if strings.Contains(cards, "skills.groupPrompt") {
		t.Error("the built-in card repeats its own title over the first list")
	}
	if custom, catalog := strings.Index(cards, "custom"), strings.Index(cards, "catalog"); custom < 0 || catalog < 0 || custom > catalog {
		t.Error("the custom skills card does not come before the catalog's")
	}
	for name, want := range map[string]string{
		"static/i18n/en.json": `"ECC Catalog skills"`,
		"static/i18n/id.json": `"Skill katalog ECC"`,
	} {
		if !strings.Contains(readWeb(t, name), want) {
			t.Errorf("%s lacks %s", name, want)
		}
	}
	body := methodBody(js, "toggleCard")
	for _, want := range []string{"builtinSkillsEnabled", "customSkillsEnabled", "eccSkillsEnabled"} {
		if !strings.Contains(body, want) {
			t.Errorf("the card switch never writes %s", want)
		}
	}
}

// The catalog sync asks for no repository: the server fixes the official one,
// and the page reports how many skills it counted.
func TestTheCatalogSyncAsksForNoRepository(t *testing.T) {
	page := readWeb(t, "static/pages/skills.html")
	for _, gone := range []string{"sync-source", "sync-tag"} {
		if strings.Contains(page, gone) {
			t.Errorf("the sync card still asks for %s", gone)
		}
	}
	js := readWeb(t, "static/js/pages/skills.js")
	if strings.Contains(js, "source:") || strings.Contains(js, "tag:") {
		t.Error("the sync still sends a repository or a branch")
	}
	if !strings.Contains(js, `t("skills.syncedCount")`) {
		t.Error("the sync never shows how many skills it counted")
	}
}

// The auto-plan inputs are bounded where the server clamps, so a person cannot
// type a value the plan reader would silently change.
func TestTheAutoPlanBoundsMatchTheServer(t *testing.T) {
	body := readWeb(t, "static/pages/skills.html")
	for _, want := range []string{
		`min="` + strconv.Itoa(plan.MinThreshold) + `" max="` + strconv.Itoa(plan.MaxThreshold) + `"`,
		`min="` + strconv.Itoa(plan.MinMaxTokens) + `" max="` + strconv.Itoa(plan.MaxMaxTokens) + `"`,
		`min="` + strconv.Itoa(plan.MinTimeoutMS) + `" max="` + strconv.Itoa(plan.MaxTimeoutMS) + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("skills.html lacks %s", want)
		}
	}
}

// $root is the element a component sits on, not a component, so a method called
// through it throws at click time.
func TestNoPageReachesAMethodThroughRoot(t *testing.T) {
	ours(t, func(name, body string) {
		if path.Dir(name) == "static/pages" && strings.Contains(body, "$root.") {
			t.Errorf("%s calls through $root; dispatch an event the shell listens for", name)
		}
	})
}

// definedMethod matches a method written at the top level of a component or
// mixin object: two spaces in, a parameter list, and an opening brace.
var definedMethod = regexp.MustCompile(`(?m)^  (?:async\s+)?([A-Za-z_]\w*)\([^)]*\)\s*\{$`)

// A mixin's method overwrites a page method of the same name, so the page's own
// handler stops running without a sign of it.
func TestNoMixinMethodShadowsAPageMethod(t *testing.T) {
	ours(t, func(name, body string) {
		if path.Dir(name) != "static/js/pages" || !strings.HasSuffix(name, ".js") {
			return
		}
		mixed := mixinNames(body)
		if len(mixed) == 0 {
			return
		}
		page := body
		seen := map[string]bool{}
		for _, m := range definedMethod.FindAllStringSubmatch(page, -1) {
			seen[m[1]] = true
		}
		for _, imp := range scopeImport.FindAllStringSubmatch(body, -1) {
			file := "static/js/pages/" + strings.TrimPrefix(imp[1], "./")
			more, err := fs.ReadFile(web.Static, file)
			if err != nil || len(exportedMixin.FindAllString(string(more), -1)) == 0 || !mixedIn(string(more), mixed) {
				continue
			}
			for _, m := range definedMethod.FindAllStringSubmatch(string(more), -1) {
				if seen[m[1]] {
					t.Errorf("%s defines %s() and so does %s: the mixin wins", name, m[1], file)
				}
			}
		}
	})
}

var numberInput = regexp.MustCompile(`<input[^>]*type="(?:number|range)"[^>]*x-on:(?:change|input)="(\w+)\(([^)]*)\)"`)

// A number input's value is a string, and the server's integer rules refuse one.
func TestEveryNumberInputSendsANumber(t *testing.T) {
	ours(t, func(name, body string) {
		if path.Dir(name) != "static/pages" || !strings.HasSuffix(name, ".html") {
			return
		}
		page := strings.TrimSuffix(path.Base(name), ".html")
		src := readWeb(t, "static/js/pages/"+page+".js")
		for _, in := range numberInput.FindAllStringSubmatch(body, -1) {
			handler := methodBody(src, in[1])
			if handler == "" {
				for _, imp := range scopeImport.FindAllStringSubmatch(src, -1) {
					if more, err := fs.ReadFile(web.Static, "static/js/pages/"+strings.TrimPrefix(imp[1], "./")); err == nil {
						handler = methodBody(string(more), in[1])
					}
					if handler != "" {
						break
					}
				}
			}
			if handler == "" {
				continue
			}
			// A handler that only stores the text is fine when a later step parses it.
			if in[1] == "setText" || in[1] == "setEnum" {
				t.Errorf("%s: a number input calls %s(), which sends the raw text", name, in[1])
			}
		}
	})
}

// A selected chip must look selected: aria-pressed alone paints nothing.
func TestAPressedButtonLooksPressed(t *testing.T) {
	css := readWeb(t, "static/css/components.css")
	if !strings.Contains(css, `[aria-pressed="true"]`) {
		t.Error(`components.css has no [aria-pressed="true"] rule, so a chosen level or hook looks like the others`)
	}
}

// Two controls with one id leave a label pointing at whichever comes first,
// which may be hidden. x-show keeps every control in the page, so a field's
// controls must be rendered by x-if, one at a time.
func TestAFieldRendersOneControlPerType(t *testing.T) {
	body := readWeb(t, "static/pages/skills.html")
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, `x-bind:id="skill.id + '-' + field.key"`) && strings.Contains(line, "x-show=") {
			t.Errorf("a field control is hidden with x-show, so its id stays in the page: %s", strings.TrimSpace(line))
		}
	}
	if !strings.Contains(body, `<template x-if="field.type === 'enum'">`) {
		t.Error("the enum control is not rendered by x-if")
	}
}

// Alpine sets a select's value before a nested x-for has made its options, so
// the value is lost; the option marks itself selected instead.
func TestASelectWithRenderedOptionsMarksTheOption(t *testing.T) {
	body := readWeb(t, "static/pages/skills.html")
	i := strings.Index(body, `x-show="field.type === 'enum'"`)
	if i < 0 {
		i = strings.Index(body, `field.type === 'enum'`)
	}
	if i < 0 {
		t.Fatal("skills.html has no enum control")
	}
	end := strings.Index(body[i:], "</select>")
	block := body[i : i+end]
	if strings.Contains(block, "<template x-for") && strings.Contains(block, "x-bind:value=\"fieldValue") {
		t.Error("the enum select binds its value before its options exist; bind selected on each option")
	}
}

// A page that opens a stream after an await may be gone by then: the router has
// already destroyed it, so nothing would ever close what it opens.
func TestAStreamOpenedAfterAnAwaitChecksThePageIsStillShown(t *testing.T) {
	ours(t, func(name, body string) {
		if path.Dir(name) != "static/js/pages" || !strings.HasSuffix(name, ".js") || !strings.Contains(body, "new EventSource(") {
			return
		}
		start := methodBody(body, "async start")
		if start == "" || !strings.Contains(start, "await ") {
			return
		}
		if !strings.Contains(start, "this.gone") || !strings.Contains(methodBody(body, "destroy"), "this.gone = true") {
			t.Errorf("%s: start() awaits and then opens streams, so it must stop when destroy() ran first", name)
		}
	})
}

// A slower answer for an earlier period must not replace the newer one.
func TestTheUsageLoadDropsAnAnswerThatIsNoLongerTheLatest(t *testing.T) {
	body := methodBody(readWeb(t, "static/js/pages/usage.js"), "async load")
	if !strings.Contains(body, "this.loadSeq") || !strings.Contains(body, "seq !== this.loadSeq") {
		t.Error("usage load() has no sequence guard")
	}
}

// The expiry dialog assigns a plain object, and the component reads a reactive
// proxy back, so comparing the two by identity never matches and the busy line
// never clears. The open it belongs to is counted instead.
func TestTheExpiryDialogDropsAnAnswerForAnEarlierOpen(t *testing.T) {
	body := methodBody(readWeb(t, "static/js/pages/quota.js"), "async openExpiry")
	if !strings.Contains(body, "this.expirySeq") || !strings.Contains(body, "seq !== this.expirySeq") ||
		!strings.Contains(body, "seq === this.expirySeq") {
		t.Error("openExpiry has no sequence guard, so a stale answer can leave the dialog busy")
	}
}

// Spending a reset is irreversible, so the confirmation names the grant it
// spends and what that grant refills, not only the count.
func TestTheResetConfirmationNamesTheGrant(t *testing.T) {
	js := readWeb(t, "static/js/pages/quota.js")
	reset := methodBody(js, "async reset")
	if !strings.Contains(reset, "resetMessage") || !strings.Contains(reset, "danger: true") || !strings.Contains(reset, `confirm: t("quota.reset")`) {
		t.Error("the reset is not confirmed through resetMessage")
	}
	message := methodBody(js, "resetMessage")
	for _, want := range []string{"label", "resetsLeft", "clears", "endsAt"} {
		if !strings.Contains(message, want) {
			t.Errorf("the confirmation does not name the grant's %s", want)
		}
	}
	if !strings.Contains(methodBody(js, "grantID"), "nextGrant(") {
		t.Error("grantID does not use the one rule for picking a grant")
	}
}

// The usage page opens on today; the API's own fallback only serves a caller
// that omits the period.
func TestTheUsagePageOpensOnToday(t *testing.T) {
	body := readWeb(t, "static/js/pages/usage.js")
	if !strings.Contains(body, `period: "today"`) || strings.Contains(body, `period: "7d"`) {
		t.Error("the usage page does not open on today")
	}
}

// Sorting names with subtraction gives NaN, and a header that sorts a column the
// reader cannot see is a trap.
func TestTheUsageBreakdownSortsWhatItShows(t *testing.T) {
	js := readWeb(t, "static/js/pages/usage.js")
	html := readWeb(t, "static/pages/usage.html")
	if !strings.Contains(js, "localeCompare") {
		t.Error("usage.js sorts names without localeCompare")
	}
	if strings.Contains(html, "setSort('cost')\" x-text=\"costMode") {
		t.Error("the last header sorts cost even when it shows total tokens")
	}
	if strings.Count(html, "aria-sort") < 6 {
		t.Error("a breakdown column header has no aria-sort")
	}
}

// A key's spend is the question an operator opens this page for, so the
// breakdown starts on API key rather than on model.
func TestTheUsageBreakdownStartsOnAPIKeys(t *testing.T) {
	js := readWeb(t, "static/js/pages/usage.js")
	if !strings.Contains(js, `group: "byApiKey"`) || strings.Contains(js, `group: "byModel"`) {
		t.Error("the usage breakdown does not open on API key")
	}
}

// A key's spend is only useful split by model, and two accounts that share a
// label are still two accounts.
func TestTheUsageBreakdownOpensAGroupIntoItsModels(t *testing.T) {
	js := readWeb(t, "static/js/pages/usage.js")
	html := readWeb(t, "static/pages/usage.html")
	for _, want := range []string{"apiKeyKey", "connectionId", "rawModel"} {
		if !strings.Contains(js, want) {
			t.Errorf("usage.js never reads %s", want)
		}
	}
	if !strings.Contains(methodBody(js, "groups"), "groupBy") {
		t.Error("usage.js groups without a rule per selector")
	}
	if !strings.Contains(js, "this.open") || !strings.Contains(methodBody(js, "toggle"), "this.open") {
		t.Error("a usage group cannot be opened")
	}
	if !strings.Contains(js, "itemName: (e) => e.provider") || !strings.Contains(js, "itemDetail: (e) => e.rawModel") {
		t.Error("an opened usage row reads the model before the provider")
	}
	for _, want := range []string{"aria-expanded", "openItems(row)", "rl-disclosure"} {
		if !strings.Contains(html, want) {
			t.Errorf("usage.html lacks %s", want)
		}
	}
}

// The picker was handed the two lists inside the statement that read them, so
// the page stopped on "Cannot access 'models' before initialization".
func TestTheComboPageReadsTheListsBeforeItHandsThemToThePicker(t *testing.T) {
	body := methodBody(readWeb(t, "static/js/pages/combos.js"), "async load")
	call := strings.Index(body, "const choices = await loadModelChoices(")
	reads := strings.Index(body, "await Promise.all(")
	if call < 0 {
		t.Fatal("combos load() never builds its model choices")
	}
	if reads < 0 || call < reads {
		t.Error("loadModelChoices runs inside the statement that declares the lists it is given")
	}
}

// A reset spends a credit, so a second confirmation while the first call is in
// flight must not spend another; a reading pass must also stop with its page.
func TestTheQuotaResetAndReadingPassAreGuarded(t *testing.T) {
	js := readWeb(t, "static/js/pages/quota.js")
	html := readWeb(t, "static/pages/quota.html")
	reset := methodBody(js, "async reset")
	if !strings.Contains(reset, "resetting") || !strings.Contains(html, "resetting") {
		t.Error("the reset button can be confirmed twice while the first call runs")
	}
	all := methodBody(js, "async readAll")
	if !strings.Contains(all, "this.gone") || !strings.Contains(all, "this.pass") {
		t.Error("readAll outlives the page or overlaps itself")
	}
	if !strings.Contains(methodBody(js, "destroy"), "this.gone = true") {
		t.Error("destroy() does not stop a running reading pass")
	}
	// The Claude reset names the grant it spends; the page also lists the
	// credits and their expiry.
	if !strings.Contains(reset, "grantId") || !strings.Contains(methodBody(js, "grantID"), "nextGrant(") {
		t.Error("the Claude reset sends no grant to spend")
	}
	if !strings.Contains(js, "/codex-reset-credits") || !strings.Contains(methodBody(js, "async openExpiry"), "/codex-reset-credits") {
		t.Error("the expiry dialog never reads the Codex credits")
	}
	iReset := strings.Index(html, `x-on:click="reset(conn)"`)
	iExpiry := strings.Index(html, `x-on:click="openExpiry(conn)"`)
	iRefresh := strings.Index(html, `x-on:click="read(conn, true)"`)
	iActive := strings.Index(html, `x-on:click="toggleActive(conn)"`)
	if iReset < 0 || iExpiry < iReset || iRefresh < iExpiry || iActive < iRefresh {
		t.Error("a card's actions are not reset, expiry, refresh, active switch")
	}
	for _, want := range []string{`x-ref="expiry"`, "quota.useBy", "quota.refills", "quota.grantedAt", "quota.expiresAt", "quota.noGrant", "expiry.rows.length === 0"} {
		if !strings.Contains(html, want) {
			t.Errorf("quota.html lacks %s", want)
		}
	}
}

// The studio fills itself after a fetch; by then the reader may have closed it or
// opened another skill, and the answer must not land in that one.
func TestTheStudioIgnoresAnAnswerForAnEarlierSkill(t *testing.T) {
	body := methodBody(readWeb(t, "static/js/pages/skills-studio.js"), "async openEdit")
	if !strings.Contains(body, "const studio = this.studio") || !strings.Contains(body, "studio !== this.studio") {
		t.Error("openEdit writes the fetched skill into whatever studio is open by then")
	}
}

// A failed reload must not throw out of a test run, which would leave the stop
// button up and the run flag set.
func TestAFailedConnectionReloadIsReportedNotThrown(t *testing.T) {
	body := methodBody(readWeb(t, "static/js/pages/provider-connections.js"), "async reloadConnections")
	if !strings.Contains(body, "catch") || !strings.Contains(body, "toast().error") {
		t.Error("reloadConnections lets a failed fetch escape its callers")
	}
}

// Every settings write goes through one helper, and a control whose save failed
// goes back to what is stored: the browser keeps the value it was given and an
// unchanged state never re-applies it.
func TestSettingsWritesShareOneHelperAndRollBack(t *testing.T) {
	if !strings.Contains(readWeb(t, "static/js/ui/api.js"), "export function patchSettings(") {
		t.Error("ui/api.js has no patchSettings")
	}
	ours(t, func(name, body string) {
		if path.Dir(name) == "static/js/pages" && strings.Contains(body, `"/api/settings", { method: "PATCH"`) {
			t.Errorf("%s: writes /api/settings by hand instead of patchSettings", name)
		}
	})
	saver := readWeb(t, "static/js/pages/token-saver.js")
	for _, m := range []string{"async setBool", "async setText", "async setNumber", "async setEnum"} {
		if !strings.Contains(methodBody(saver, m), "restore") && !strings.Contains(methodBody(saver, m), "checked = ") {
			t.Errorf("token-saver %s leaves the control changed after a failed save", m)
		}
	}
	combos := readWeb(t, "static/js/pages/combos.js")
	for _, m := range []string{"async setStrategy", "async setPolicy", "async setThreshold", "async setRouter", "async setJudge"} {
		if !strings.Contains(methodBody(combos, m), "restore") {
			t.Errorf("combos %s leaves the control changed after a failed save", m)
		}
	}
	if !strings.Contains(methodBody(readWeb(t, "static/js/pages/provider-settings.js"), "async saveStrategy"), "this.applyStrategy()") {
		t.Error("a failed strategy save leaves the switch flipped")
	}
}

// The combos page keeps a module-level Sortable handle, so leaving with a dialog
// open must release it; suggestions arrive in any order, and a strategy write
// merges into what is stored now, not into a copy loaded at page open.
func TestTheCombosPageReleasesAndOrdersWhatItStarts(t *testing.T) {
	js := readWeb(t, "static/js/pages/combos.js")
	if !strings.Contains(methodBody(js, "destroy"), "this.stopSortable()") {
		t.Error("combos destroy() does not release the Sortable handle")
	}
	run := methodBody(js, "async runSuggest")
	if !strings.Contains(run, "++this.suggestSeq") || !strings.Contains(run, "seq !== this.suggestSeq") {
		t.Error("runSuggest lets a slower earlier answer replace the latest")
	}
	if !strings.Contains(methodBody(js, "async writeEntry"), `api("/api/settings")`) {
		t.Error("writeEntry merges into a stale copy of the strategies")
	}
	if strings.Contains(methodBody(js, "async removeCombo"), "saveStrategies") {
		t.Error("removeCombo repeats the strategy cleanup the server already does")
	}
}

// A page that leaves while a request is out must not start a clock, and a
// failed translation fetch must not blank the dashboard before it starts.
func TestAGonePageStartsNoClockAndATranslationFailureIsSurvived(t *testing.T) {
	if !strings.Contains(methodBody(readWeb(t, "static/js/pages/provider.js"), "startTimer"), "this.gone") {
		t.Error("provider startTimer restarts after the page was left")
	}
	i18n := readWeb(t, "static/js/ui/i18n.js")
	if !strings.Contains(i18n, "catch (err) {\n    messages = {};") {
		t.Error("i18n load() throws on a failed fetch, which stops app.js and login.js at the top level")
	}
}

// A saved secret is cleared only when the save worked, and a page's error line
// does not outlive the request that raised it.
func TestAFailedSaveKeepsTheSecretAndALoadClearsTheError(t *testing.T) {
	settings := readWeb(t, "static/js/pages/settings.js")
	if !strings.Contains(settings, "ok) {") && !strings.Contains(settings, "if (saved)") {
		t.Error("the OIDC secret is cleared whether or not the save worked")
	}
	for _, name := range []string{"settings", "pricing", "token-saver"} {
		body := methodBody(readWeb(t, "static/js/pages/"+name+".js"), "async load")
		if !strings.Contains(body, `this.error = ""`) {
			t.Errorf("%s: load() leaves an earlier error on the page", name)
		}
	}
}

// A frame the server sent is data from outside the script: one bad frame must
// not throw out of the event handler.
func TestEveryStreamFrameParseIsGuarded(t *testing.T) {
	ours(t, func(name, body string) {
		if path.Dir(name) != "static/js/pages" || !strings.HasSuffix(name, ".js") {
			return
		}
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			if strings.Contains(line, "JSON.parse(event.data)") && !strings.Contains(strings.Join(lines[max(0, i-2):i+1], "\n"), "try {") {
				t.Errorf("%s:%d: a stream frame is parsed outside a try", name, i+1)
			}
		}
	})
}

// The role is read once through ui/session.js, and a status that cannot be read
// leaves the narrow view: the server enforces either way, but a failed read
// must not hand a standard user the admin controls.
func TestTheRoleIsReadOnceAndDefaultsToTheNarrowView(t *testing.T) {
	session := readWeb(t, "static/js/ui/session.js")
	if !strings.Contains(session, `role: "user"`) {
		t.Error("a failed status read does not fall back to the standard-user view")
	}
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".js") || name == "static/js/ui/session.js" || name == "static/js/login.js" {
			return
		}
		if strings.Contains(body, "/api/auth/status") {
			t.Errorf("%s: reads /api/auth/status itself instead of session()", name)
		}
	})
	for _, name := range []string{"endpoint", "usage"} {
		if !strings.Contains(readWeb(t, "static/js/pages/"+name+".js"), `role: "user"`) {
			t.Errorf("%s: the role starts as admin before it is known", name)
		}
	}
}

// The topology is redrawn on every live frame. Replacing the nodes under a
// keyboard user drops their focus, and role="img" would hide the nodes from a
// screen reader altogether.
func TestTheTopologyKeepsFocusAndNamesEachNode(t *testing.T) {
	js := methodBody(readWeb(t, "static/js/pages/usage-charts.js"), "drawTopology")
	if !strings.Contains(js, "svg.contains(document.activeElement)") {
		t.Error("drawTopology redraws under a focused node")
	}
	if !strings.Contains(js, `"aria-label"`) || !strings.Contains(js, `"role", "link"`) {
		t.Error("a topology node has no role and name")
	}
	html := readWeb(t, "static/pages/usage.html")
	if strings.Contains(html, `class="rl-topology" x-ref="topology" viewBox="0 0 640 360" role="img"`) {
		t.Error("the topology svg is role=img, which hides its focusable nodes")
	}
}

// Design doc 18 records that a first visit writes nothing: a key is created
// when a person asks for one. A page view that posts would also create one key
// per tab opened on an empty list.
func TestOpeningTheEndpointPageCreatesNoKey(t *testing.T) {
	if strings.Contains(readWeb(t, "static/js/pages/endpoint.js"), `name: "Default Key"`) {
		t.Error("endpoint.js creates a Default Key when the list is empty")
	}
}

// A canvas cannot parse light-dark(), which is how Tabler declares the body
// colour, so chart text must come from plain tokens that each theme sets, and a
// theme switch must repaint the charts already drawn.
func TestChartColoursAreTokensACanvasCanRead(t *testing.T) {
	charts := readWeb(t, "static/js/ui/charts.js")
	if strings.Contains(charts, "--tblr-body-color") || strings.Contains(charts, "--tblr-border-color") {
		t.Error("charts.js reads a token Tabler declares with light-dark()")
	}
	theme := readWeb(t, "static/css/theme.css")
	if strings.Count(theme, "--rl-chart-text:") != 2 || strings.Count(theme, "--rl-chart-grid:") != 2 {
		t.Error("each theme must set --rl-chart-text and --rl-chart-grid")
	}
	if !strings.Contains(readWeb(t, "static/js/app.js"), `dispatchEvent(new Event("rl-theme"))`) {
		t.Error("the theme switch announces nothing the charts can hear")
	}
	usage := readWeb(t, "static/pages/usage.html")
	if !strings.Contains(usage, `x-on:rl-theme.window="drawAll()"`) {
		t.Error("the usage page does not repaint on a theme switch")
	}
	if strings.Count(usage, "<canvas") != strings.Count(usage, `<canvas role="img" x-bind:aria-label=`) {
		t.Error("a canvas has no role and name")
	}
}

// One selector, one block; status colours come from tokens each theme sets; a
// colour variable never carries a fallback literal that hides a missing token.
func TestTheStylesheetKeepsOneBlockPerSelectorAndTokenColours(t *testing.T) {
	css := readWeb(t, "static/css/components.css")
	for _, sel := range []string{".rl-toolbar {", ".rl-provider-card {"} {
		if n := strings.Count("\n"+css, "\n"+sel); n != 1 {
			t.Errorf("%s is declared %d times", sel, n)
		}
	}
	if regexp.MustCompile(`var\(--[a-z-]+, #`).MatchString(css) {
		t.Error("a var() carries a literal fallback")
	}
	if regexp.MustCompile(`light-dark\(#`).MatchString(css) {
		t.Error("a status colour is a literal light-dark() pair, not a --rl-ok/--rl-err/--rl-warn token")
	}
	theme := readWeb(t, "static/css/theme.css")
	for _, tok := range []string{"--rl-ok:", "--rl-err:", "--rl-warn:"} {
		if strings.Count(theme, tok) != 2 {
			t.Errorf("each theme must set %s", tok)
		}
	}
	for _, want := range []string{".rl-split > .card {", ".rl-topo-node:focus-visible", "var(--tblr-primary-fg)"} {
		if !strings.Contains(css, want) {
			t.Errorf("components.css lacks %s", want)
		}
	}
	if strings.Contains(css, ".rl-topo-node:focus {") {
		t.Error("the topology node's focus ring shows on a mouse click")
	}
	head := css[strings.Index(css, ".rl-card-head {"):]
	head = head[:strings.Index(head, "}")]
	if !strings.Contains(head, "flex-wrap: wrap") {
		t.Error("a card head cannot wrap at 375px")
	}
}

// Small layout rules the 375px pass needs: the tight table keeps its tight
// padding, a sub-heading has room above it, a price input never collapses, the
// topology scrolls instead of shrinking, and no class names a rule nobody wrote.
func TestSmallLayoutRulesExist(t *testing.T) {
	css := readWeb(t, "static/css/components.css")
	for _, want := range []string{".table:not(.table-sm) > :not(caption) > * > *", "h3.rl-card-title {", ".rl-topology-wrap {", "td .form-control {"} {
		if !strings.Contains(css, want) {
			t.Errorf("components.css lacks %s", want)
		}
	}
	if !strings.Contains(readWeb(t, "static/pages/usage.html"), `class="rl-topology-wrap"`) {
		t.Error("the topology has no scroll wrapper")
	}
	ours(t, func(name, body string) {
		if strings.HasSuffix(name, ".html") && strings.Contains(body, "rl-provider-logo") {
			t.Errorf("%s uses rl-provider-logo, which no stylesheet defines", name)
		}
	})
}

// A control with no name is announced as "edit text". Every input, select and
// textarea has an aria-label, a label that points at its id, or a label around it.
func TestEveryControlHasAName(t *testing.T) {
	control := regexp.MustCompile(`<(input|select|textarea)\b((?:[^>"]|"[^"]*")*)>`)
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		for _, m := range control.FindAllStringSubmatchIndex(body, -1) {
			attrs := body[m[4]:m[5]]
			if strings.Contains(attrs, `type="hidden"`) || strings.Contains(attrs, "aria-label") {
				continue
			}
			if id := regexp.MustCompile(`\bid="([^"]+)"`).FindStringSubmatch(attrs); id != nil && strings.Contains(body, `for="`+id[1]+`"`) {
				continue
			}
			if strings.Contains(attrs, "x-bind:id=") && strings.Contains(body, "x-bind:for=") {
				continue
			}
			before := body[:m[0]]
			if strings.LastIndex(before, "<label") > strings.LastIndex(before, "</label>") {
				continue
			}
			t.Errorf("%s:%d: a control has no accessible name", name, strings.Count(before, "\n")+1)
		}
	})
}

// A tab strip without panels and arrow keys is a broken tab pattern, so these
// are buttons that say which is pressed; a radio group has a name; a route
// change sets the tab title and moves focus into the new view.
func TestTheSwitchersAreButtonsAndARouteChangeIsAnnounced(t *testing.T) {
	for _, page := range []string{"settings", "usage"} {
		html := readWeb(t, "static/pages/"+page+".html")
		if strings.Contains(html, `role="tab`) || strings.Contains(html, "aria-selected") {
			t.Errorf("%s: a switcher claims the tab pattern it does not implement", page)
		}
		if !strings.Contains(html, `class="rl-tabs" role="group"`) {
			t.Errorf("%s: the switcher is not a group", page)
		}
	}
	if !strings.Contains(readWeb(t, "static/css/components.css"), `.rl-tabs button[aria-pressed="true"]`) {
		t.Error("the pressed switcher button is not styled")
	}
	settings := readWeb(t, "static/pages/settings.html")
	if !strings.Contains(settings, "<fieldset") || !strings.Contains(settings, "<legend") {
		t.Error("the sign-in mode radios have no group name")
	}
	router := readWeb(t, "static/js/router.js")
	if !strings.Contains(router, "document.title =") || !strings.Contains(router, "view.focus(") {
		t.Error("a route change leaves the tab title and the focus where they were")
	}
}

// Enter in a dialog's text field saves it, as in every other dialog: the form
// submits through the save method and the primary button is the submit button.
func TestTheCombosAndSkillsDialogsSubmitOnEnter(t *testing.T) {
	cases := []struct{ page, form, save string }{
		{"combos", "form", "saveForm()"},
		{"combos", "suggest", "acceptSuggest()"},
		{"skills", "studio", "saveStudio()"},
	}
	for _, c := range cases {
		html := readWeb(t, "static/pages/"+c.page+".html")
		dlg := html[strings.Index(html, `x-ref="`+c.form+`"`):]
		dlg = dlg[:strings.Index(dlg, "</dialog>")]
		if !strings.Contains(dlg, `x-on:submit.prevent="`+c.save+`"`) {
			t.Errorf("%s/%s: the form does not submit through %s", c.page, c.form, c.save)
		}
		if !regexp.MustCompile(`type="submit"[^>]*`).MatchString(dlg) {
			t.Errorf("%s/%s: the primary button is not a submit button", c.page, c.form)
		}
		if strings.Contains(dlg, `x-on:click="`+c.save+`"`) {
			t.Errorf("%s/%s: the button also calls %s on click, which would run it twice", c.page, c.form, c.save)
		}
	}
}

// One pattern each: a save row outside a dialog is .rl-actions, a dialog title
// is a bare h2, and an empty state is a hint line.
func TestThePagesUseOneSavePatternOneTitleAndOneEmptyState(t *testing.T) {
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		if strings.Contains(body, "modal-title") {
			t.Errorf("%s: a dialog title carries modal-title", name)
		}
		for i := 0; ; {
			k := strings.Index(body[i:], "rl-dialog-actions")
			if k < 0 {
				break
			}
			at := i + k
			if strings.LastIndex(body[:at], "<dialog") < strings.LastIndex(body[:at], "</dialog>") || !strings.Contains(body[:at], "<dialog") {
				t.Errorf("%s:%d: a save row outside a dialog uses rl-dialog-actions", name, strings.Count(body[:at], "\n")+1)
			}
			i = at + 1
		}
		if regexp.MustCompile(`class="text-secondary"[^>]*x-t="[a-z.A-Z]*[eE]mpty"`).MatchString(body) || strings.Contains(body, `class="rl-empty"`) {
			t.Errorf("%s: an empty state is not a .rl-hint line", name)
		}
	})
	if !strings.Contains(readWeb(t, "static/css/components.css"), "\n.rl-actions,\n.rl-dialog-actions {") {
		t.Error("no .rl-actions rule")
	}
}

// The remaining one-pattern rules: a table sits in .rl-table-wrap (it scrolls
// instead of widening the page), a range input is .form-range, a toolbar
// control is the small size, and a hint is .rl-hint.
func TestTablesRangesToolbarsAndHintsFollowOnePattern(t *testing.T) {
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		for _, m := range regexp.MustCompile(`<table\b`).FindAllStringIndex(body, -1) {
			pre := body[:m[0]]
			if strings.LastIndex(pre, "rl-table-wrap") < strings.LastIndex(pre, "</table>") || !strings.Contains(pre, "rl-table-wrap") {
				t.Errorf("%s:%d: a table is not inside .rl-table-wrap", name, strings.Count(pre, "\n")+1)
			}
		}
		for _, m := range regexp.MustCompile(`<input\b[^>]*type="range"[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(m, "form-range") {
				t.Errorf("%s: a range input is not .form-range: %.60s", name, m)
			}
		}
		for _, m := range regexp.MustCompile(`(?s)<div class="rl-toolbar"[^>]*>.*?\n  </div>`).FindAllString(body, -1) {
			for _, c := range regexp.MustCompile(`<(?:input|select)\b[^>]*class="(form-(?:control|select)[^"]*)"`).FindAllStringSubmatch(m, -1) {
				if !strings.Contains(c[1], "-sm") {
					t.Errorf("%s: a toolbar control is not the small size: %s", name, c[1])
				}
			}
		}
		if regexp.MustCompile(`class="text-secondary"[^>]*x-t="[a-zA-Z.]*Hint"`).MatchString(body) {
			t.Errorf("%s: a hint line is not .rl-hint", name)
		}
	})
}

// A level shown only by colour is lost to a reader who cannot see it, and an
// "Enabled" switch repeated on several cards must say which card it belongs to.
func TestLevelsAndSwitchesAreNamedInText(t *testing.T) {
	if !strings.Contains(methodBody(readWeb(t, "static/js/pages/console-log.js"), "lineText"), "row.level") {
		t.Error("a log line shows its level by colour only")
	}
	saver := readWeb(t, "static/pages/token-saver.html")
	if n := strings.Count(saver, `x-bind:aria-label="label('saver.enabled') + ' ' + label('saver.`); n != 6 {
		t.Errorf("%d of 6 saver switches name their card", n)
	}
}

// A focus ring needs 3:1 against the surface (WCAG 1.4.11). The ring token is a
// translucent tint, so its alpha decides whether it clears that.
func TestTheFocusRingIsOpaqueEnoughToSee(t *testing.T) {
	theme := readWeb(t, "static/css/theme.css")
	for _, m := range regexp.MustCompile(`--rl-ring: rgba\([0-9, ]+, ([0-9.]+)\)`).FindAllStringSubmatch(theme, -1) {
		if a, _ := strconv.ParseFloat(m[1], 64); a < 0.6 {
			t.Errorf("--rl-ring alpha %s is too faint", m[1])
		}
	}
}

// One implementation of each small helper: label() and num() live in
// ui/format.js, setField() in ui/fields.js, and the integer clamp in fields.js.
func TestSmallHelpersHaveOneImplementation(t *testing.T) {
	defs := map[string]*regexp.Regexp{
		"label(key)":            regexp.MustCompile(`(?m)^  label\(key\) \{`),
		"num(value)":            regexp.MustCompile(`(?m)^  num\(value\) \{`),
		"setField(path, event)": regexp.MustCompile(`(?m)^  setField\(path, event\) \{`),
		"a clamp function":      regexp.MustCompile(`(?m)^function clamp\(`),
		"setCheckedField":       regexp.MustCompile(`(?m)^  setCheckedField\(path, event\) \{`),
		"nodeLabel(card)":       regexp.MustCompile(`(?m)^  nodeLabel\(card\) \{`),
		"closeResults()":        regexp.MustCompile(`(?m)^  closeResults\(\) \{`),
	}
	owner := map[string]string{
		"label(key)": "static/js/ui/format.js", "num(value)": "static/js/ui/format.js", "setField(path, event)": "static/js/ui/fields.js",
		"setCheckedField": "static/js/pages/provider-card.js", "nodeLabel(card)": "static/js/pages/provider-card.js",
		"closeResults()": "static/js/pages/provider-card.js",
	}
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".js") {
			return
		}
		for what, re := range defs {
			if re.MatchString(body) && owner[what] != name {
				t.Errorf("%s: defines %s, which has one home", name, what)
			}
		}
	})
	for what, file := range owner {
		_ = what
		if !defs[what].MatchString(readWeb(t, file)) {
			t.Errorf("%s does not define %s", file, what)
		}
	}
	if !strings.Contains(readWeb(t, "static/js/ui/fields.js"), "export function clampInt(") {
		t.Error("fields.js has no clampInt")
	}
	if !strings.Contains(readWeb(t, "static/js/ui/fields.js"), "export const fieldMethods = {") {
		t.Error("fields.js has no fieldMethods")
	}
	for _, page := range []string{"provider", "providers", "combos", "settings", "endpoint"} {
		if !regexp.MustCompile(`\}, [A-Za-z, ]*fieldMethods[A-Za-z, ]*\)\);\s*$`).MatchString(readWeb(t, "static/js/pages/"+page+".js")) {
			t.Errorf("%s does not mix in fieldMethods", page)
		}
	}
	ours(t, func(name, body string) {
		if strings.HasSuffix(name, ".js") && strings.Contains(body, `Alpine.store("toasts")`) && name != "static/js/ui/toast.js" && name != "static/js/app.js" {
			t.Errorf("%s: reaches the toast store directly instead of toast()", name)
		}
	})
	if strings.Contains(readWeb(t, "static/js/app.js"), `window.Alpine.store("toasts")`) {
		t.Error("app.js reaches the toast store directly instead of toast()")
	}
}

// State nothing reads and helpers nobody calls are weight a reader has to
// carry; an export no other module imports is a public surface nobody asked for.
func TestNoDeadStateOrUnusedExportsInThePages(t *testing.T) {
	dead := []string{"addSuggested", "suggest.open", "suggest.context", "bulk.open", "nodeForm.open", "modelAdd.open", "context: {}", "keys.pause\"", "keys.resume\"", "setModelAt", "testModels(", "provider.modelsTitle"}
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".js") && !strings.HasSuffix(name, ".html") && !strings.HasSuffix(name, ".json") {
			return
		}
		for _, d := range dead {
			if strings.Contains(body, d) {
				t.Errorf("%s: still has %q, which nothing reads", name, d)
			}
		}
	})
	for _, c := range []struct{ file, decl string }{
		{"static/js/ui/api.js", "export class ApiError"},
		{"static/js/ui/i18n.js", "export const locales"},
		{"static/js/router.js", "export function match"},
		{"static/js/router.js", "export function navFor"},
		{"static/js/pages/providers.js", "export function statsOf"},
	} {
		if strings.Contains(readWeb(t, c.file), c.decl) {
			t.Errorf("%s exports something no module imports: %s", c.file, c.decl)
		}
	}
	if strings.Contains(readWeb(t, "static/pages/token-saver.html"), "Mode', 'always')") {
		t.Error("text() takes one argument; the second is ignored")
	}
}

// Every vendored icon file is mapped, and every mapped icon is used by a page:
// a file nothing draws is weight that ships to every browser.
func TestEveryVendoredIconIsMappedAndUsed(t *testing.T) {
	icons := readWeb(t, "static/js/ui/icons.js")
	entries, err := fs.ReadDir(web.Static, libDir+"lucide/icons")
	if err != nil {
		t.Fatal(err)
	}
	used := ""
	ours(t, func(name, body string) {
		if (strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".js")) && name != "static/js/ui/icons.js" {
			used += body
		}
	})
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".mjs")
		if !strings.Contains(icons, `"`+id+`": () => import`) {
			t.Errorf("%s is vendored but not mapped", id)
		}
		if !strings.Contains(used, `"`+id+`"`) && !strings.Contains(used, `'`+id+`'`) {
			t.Errorf("%s is vendored but no page draws it", id)
		}
	}
}

// The quota pager's bounds come from the one integer clamp, like every other
// page; a hand-written Math.max/Math.min pair for a page number is a second copy.
func TestThePagerBoundsUseTheSharedClamp(t *testing.T) {
	js := readWeb(t, "static/js/pages/quota.js")
	for _, m := range []string{"turnPage", "turnClientPage"} {
		body := methodBody(js, m)
		if !strings.Contains(body, "clampInt(") || strings.Contains(body, "Math.max(") {
			t.Errorf("quota %s clamps by hand", m)
		}
	}
}

// A row's "Enabled" switch is announced with the row's name, or a list of
// skills reads as the same word repeated.
func TestASkillSwitchNamesItsSkill(t *testing.T) {
	html := readWeb(t, "static/pages/skills.html")
	i := strings.Index(html, `x-on:change="toggle(skill, $event)"`)
	if i < 0 {
		t.Fatal("no skill switch")
	}
	line := html[strings.LastIndex(html[:i], "<input"):i]
	if !strings.Contains(line, "aria-label") || !strings.Contains(line, "skill.name") {
		t.Error("a skill's switch does not name the skill")
	}
}

// The sidebar and the router list the same entries in the same order, so the
// page a user is on marks its own entry current.
func TestTheSidebarOrderMatchesTheRouter(t *testing.T) {
	want := []string{
		"/dashboard/endpoint", "/dashboard/providers", "/dashboard/combos", "/dashboard/token-saver", "/dashboard/skills",
		"/dashboard/usage", "/dashboard/quota", "/dashboard/pricing", "/dashboard/users", "/dashboard/settings", "/dashboard/about", "/dashboard/console-log",
	}
	shell := readWeb(t, "static/shell.html")
	navStart := strings.Index(shell, "<nav")
	navEnd := strings.Index(shell[navStart:], "</nav>")
	if navStart < 0 || navEnd < 0 {
		t.Fatal("the shell has no nav")
	}
	nav := shell[navStart : navStart+navEnd]
	var have []string
	for _, m := range regexp.MustCompile(`<a href="(/dashboard/[a-z-]+)"`).FindAllStringSubmatch(nav, -1) {
		have = append(have, m[1])
	}
	if strings.Join(have, " ") != strings.Join(want, " ") {
		t.Errorf("the sidebar order is %v, want %v", have, want)
	}
	for _, group := range []string{"nav.groupLlm", "nav.groupSystem"} {
		if !strings.Contains(nav, `x-t="`+group+`"`) {
			t.Errorf("the sidebar has no %s heading", group)
		}
	}

	router := readWeb(t, "static/js/router.js")
	start := strings.Index(router, "const navRoots = [")
	end := strings.Index(router[start:], "];")
	if start < 0 || end < 0 {
		t.Fatal("no navRoots")
	}
	var roots []string
	for _, m := range regexp.MustCompile(`"(/dashboard/[a-z-]+)"`).FindAllStringSubmatch(router[start:start+end], -1) {
		roots = append(roots, m[1])
	}
	if strings.Join(roots, " ") != strings.Join(want, " ") {
		t.Errorf("navRoots is %v, want %v", roots, want)
	}
}

// A provider served without a credential has no key to add, no switch to pause
// and nothing to edit or delete.
func TestANoCredentialProviderHasNoKeyPath(t *testing.T) {
	page := readWeb(t, "static/pages/provider.html")
	if !strings.Contains(page, `x-t="provider.noAuth"`) {
		t.Error("the provider page does not say a connection is unnecessary")
	}
	if !strings.Contains(page, `x-show="conn.noAuth !== true"`) {
		t.Error("a virtual account offers its controls")
	}
	if !strings.Contains(readWeb(t, "static/pages/providers.html"), "card.noAuth !== true") {
		t.Error("a no-credential provider offers a switch it cannot use")
	}
	if !strings.Contains(readWeb(t, "static/js/pages/providers.js"), "conn.noAuth === true") {
		t.Error("the provider cards do not read the virtual account")
	}
}

// The About page names the build and asks GitHub for the newest release only
// when the reader asks, from a menu entry after Settings.
func TestTheAboutPageOffersAnUpdateCheck(t *testing.T) {
	if !strings.Contains(readWeb(t, "static/js/routes.json"), `"/dashboard/about"`) {
		t.Fatal("routes.json has no About page")
	}
	page := readWeb(t, "static/pages/about.html")
	for _, want := range []string{`x-t="about.title"`, `x-t="about.versionTitle"`, `x-t="about.check"`, `x-t="about.creditsTitle"`, `x-t="about.licenseTitle"`} {
		if !strings.Contains(page, want) {
			t.Errorf("about.html lacks %s", want)
		}
	}
	js := readWeb(t, "static/js/pages/about.js")
	if !strings.Contains(js, `Alpine.data("aboutPage"`) || !strings.Contains(js, `api("/api/about/update")`) {
		t.Error("the About page never asks the router to check for updates")
	}
	for _, want := range []string{`x-t="about.updateHow"`, `x-t="about.updateNow"`, `x-show="admin"`} {
		if !strings.Contains(page, want) {
			t.Errorf("about.html lacks %s", want)
		}
	}
	if !strings.Contains(js, `api("/api/about/upgrade"`) || !strings.Contains(js, "session()") {
		t.Error("the About page offers no upgrade action, or cannot tell who may run it")
	}
	shell := readWeb(t, "static/shell.html")
	settings := strings.Index(shell, `href="/dashboard/settings"`)
	about := strings.Index(shell, `href="/dashboard/about"`)
	console := strings.Index(shell, `href="/dashboard/console-log"`)
	if about < 0 || settings < 0 || console < 0 || about < settings || about > console {
		t.Error("the About entry does not sit between Settings and Console Log")
	}
}

// A menu entry and the page it opens say the same thing, so a reader is never
// sent to a page named differently from the link they followed.
func TestEveryMenuLabelMatchesItsPageTitle(t *testing.T) {
	var routes []struct {
		Path string `json:"path"`
		Page string `json:"page"`
	}
	if err := json.Unmarshal([]byte(readWeb(t, "static/js/routes.json")), &routes); err != nil {
		t.Fatal(err)
	}
	shell := readWeb(t, "static/shell.html")
	title := regexp.MustCompile(`<h1 class="rl-page-title" x-t="([^"]+)"`)
	for _, locale := range []string{"en", "id"} {
		messages := loadTranslations(t, locale)
		for _, r := range routes {
			if strings.Contains(r.Path, ":") {
				continue
			}
			link := regexp.MustCompile(`(?s)<a href="` + regexp.QuoteMeta(r.Path) + `"[^>]*>(.*?)</a>`).FindStringSubmatch(shell)
			if link == nil {
				continue
			}
			nav := regexp.MustCompile(`x-t="([^"]+)"`).FindStringSubmatch(link[1])
			heading := title.FindStringSubmatch(readWeb(t, "static/pages/"+r.Page+".html"))
			if nav == nil || heading == nil {
				continue
			}
			if messages[nav[1]] != messages[heading[1]] {
				t.Errorf("%s: the menu says %q and the page %q", r.Path, messages[nav[1]], messages[heading[1]])
			}
		}
	}
}

// TestTheSidebarCollapsesToADrawerOrARail covers the one control that changes
// the sidebar: it must name the element it opens, stay reachable by keyboard,
// and close on a tap away or a route change.
func TestTheSidebarCollapsesToADrawerOrARail(t *testing.T) {
	shell := readWeb(t, "static/shell.html")
	if !strings.Contains(shell, `class="rl-sidebar" id="rl-sidebar"`) {
		t.Error("the sidebar has no id for the toggle to point at")
	}
	button := regexp.MustCompile(`(?s)<button[^>]*aria-controls="rl-sidebar"[^>]*>`).FindString(shell)
	if button == "" || !strings.Contains(button, `x-on:click="toggleNav()"`) {
		t.Errorf("no toggle opens the sidebar: %q", button)
	}
	if !strings.Contains(button, "x-bind:aria-expanded") || !strings.Contains(button, "x-tip") {
		t.Errorf("the toggle does not report its state or name itself: %q", button)
	}
	if !strings.Contains(shell, `class="rl-nav-backdrop"`) || !strings.Contains(shell, `"closeNav()"`) {
		t.Error("no backdrop closes the drawer")
	}
	nav := regexp.MustCompile(`(?s)<nav.*?</nav>`).FindString(shell)
	if n := strings.Count(nav, `x-bind:title=`); n != strings.Count(nav, `<a href="/dashboard/`) {
		t.Errorf("%d of the menu's links name themselves on hover", n)
	}

	theme := readWeb(t, "static/js/theme.js")
	if !strings.Contains(theme, `data-rl-nav`) {
		t.Error("the rail width is applied after the first paint, so the sidebar jumps")
	}
	app := readWeb(t, "static/js/app.js")
	for _, want := range []string{`localStorage.setItem("rl-nav"`, `matchMedia("(max-width: 767.98px)")`, `rl-nav-open`} {
		if !strings.Contains(app, want) {
			t.Errorf("the shell does not keep the sidebar's shape: missing %s", want)
		}
	}
}

// A destructive button that shows a word wears the outline danger style, so it
// reads as a warning the way Add and Edit read as actions. An icon-only button
// keeps the quiet style.
func TestADeleteWithTextReadsAsADangerButton(t *testing.T) {
	button := regexp.MustCompile(`(?s)<button\b(?:[^>"']|"[^"]*"|'[^']*')*>(.*?)</button>`)
	hidden := regexp.MustCompile(`(?s)<span class="visually-hidden"[^>]*>.*?</span>`)
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		for _, m := range button.FindAllStringSubmatch(body, -1) {
			open := m[0][:strings.Index(m[0], ">")+1]
			if !strings.Contains(open, "danger") || strings.Contains(open, `type="submit"`) {
				continue
			}
			shown := hidden.ReplaceAllString(m[1], "")
			visible := strings.Contains(shown, "x-t=") || strings.Contains(shown, "x-text=")
			outline := strings.Contains(open, "btn-outline-danger")
			if visible && !outline {
				t.Errorf("%s: %s is a danger action with a label but not the outline style", name, strings.TrimSpace(open))
			}
			if !visible && outline {
				t.Errorf("%s: %s has no label but takes the outline style", name, strings.TrimSpace(open))
			}
		}
	})
}

// A model row says what the model can read and do, and copies the name a client
// would type for it.
func TestAModelRowShowsItsCapabilitiesAndCopiesItsName(t *testing.T) {
	body := readWeb(t, "static/pages/provider.html")
	for _, icon := range []string{"eye", "brain", "audio-lines"} {
		if !strings.Contains(body, `x-icon="`+icon+`"`) {
			t.Errorf("a model row draws no %s badge", icon)
		}
	}
	if !strings.Contains(body, `x-on:click="copyText(aliasOrFull(model))"`) {
		t.Error("a model row cannot copy its name")
	}
	combos := readWeb(t, "static/pages/combos.html")
	if !strings.Contains(combos, `x-on:click="copyName(combo)"`) {
		t.Error("a combo cannot copy its name")
	}
	if !strings.Contains(combos, "capsOf(model)") || !strings.Contains(combos, "comboCaps(combo)") {
		t.Error("a combo does not show its members' capabilities")
	}
}

// A search box that filters a list on every keystroke re-renders the list on
// each character, so its handler waits for a pause in the typing.
func TestEverySearchBoxWaitsForAPauseInTheTyping(t *testing.T) {
	input := regexp.MustCompile(`<input\b[^>]*\btype="search"[^>]*>`)
	handler := regexp.MustCompile(`x-on:input(\.[a-z0-9]+)*=`)
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		for _, tag := range input.FindAllString(body, -1) {
			h := handler.FindString(tag)
			if h == "" || !strings.Contains(h, ".debounce.") {
				t.Errorf("%s: a search box without a debounce: %.120s", name, tag)
			}
		}
	})
}

// A datalist built from `choices` reads an empty list when its page never
// declares or fills it: Alpine logs an undefined variable and the picker offers
// nothing. Every page that builds one fills it from the shared model list.
func TestEveryModelPickerIsFilledFromTheSharedList(t *testing.T) {
	ours(t, func(name, body string) {
		if !strings.HasPrefix(name, "static/pages/") || !strings.Contains(body, `in choices"`) {
			return
		}
		js := readWeb(t, "static/js/pages/"+strings.TrimSuffix(strings.TrimPrefix(name, "static/pages/"), ".html")+".js")
		if !strings.Contains(js, "choices: []") || !strings.Contains(js, "loadModelChoices(") {
			t.Errorf("%s offers model names from `choices`, but its script does not declare and fill them", name)
		}
	})
}

// A page that raises its loading flag on every reload hides its whole list
// while the data comes back, so a toggle or a save makes the page flash empty
// and jump. Only the first load says "Loading".
func TestAReloadDoesNotBlankTheProvidersOrCombosPage(t *testing.T) {
	for _, name := range []string{"providers", "combos", "settings"} {
		load := methodBody(readWeb(t, "static/js/pages/"+name+".js"), "async load")
		if load == "" {
			t.Fatalf("%s.js has no load()", name)
		}
		if strings.Contains(load, "this.loading = true") {
			t.Errorf("%s.js load() raises the loading flag again on a reload", name)
		}
	}
}

// A select whose options are rendered from a list cannot take its value from
// the select alone: the value is applied before the options exist, and the
// browser falls back to the first one. Each option says whether it is chosen.
func TestASelectBuiltFromAListMarksItsChosenOption(t *testing.T) {
	sel := regexp.MustCompile(`(?s)<select\b[^>]*>(.*?)</select>`)
	ours(t, func(name, body string) {
		if !strings.HasSuffix(name, ".html") {
			return
		}
		for _, m := range sel.FindAllStringSubmatch(body, -1) {
			if !strings.Contains(m[1], "<template x-for") {
				continue
			}
			if strings.Contains(m[0], `x-bind:value="`) && !strings.Contains(m[1], "x-bind:selected=") {
				t.Errorf("%s: a select built from a list takes its value from the select, not from its options: %.80s", name, m[0])
			}
		}
	})
}

// A chart's value axis reads in the compact form, so a count of billions does
// not run its labels into the plot.
func TestEveryChartValueAxisIsWrittenInCompactUnits(t *testing.T) {
	js := readWeb(t, "static/js/pages/usage-charts.js")
	if !strings.Contains(js, "compactTokens") || !strings.Contains(js, "callback: (v) => compactTokens(v)") {
		t.Error("the usage charts do not write their value axis in compact units")
	}
	if !strings.Contains(readWeb(t, "static/js/ui/format.js"), "export function compactTokens") {
		t.Error("format.js lacks compactTokens")
	}
}

// OpenDesign's quota needs a second credential. It has a password input of its
// own in both dialogs, is sent only when typed, and never listed in the generic
// field editor, which can show a masked value but not replace it.
func TestTheControlKeyHasItsOwnPasswordInputAndStaysOutOfTheFieldEditor(t *testing.T) {
	html := readWeb(t, "static/pages/provider.html")
	for _, id := range []string{"add-control-key", "edit-control-key"} {
		if !regexp.MustCompile(`<input[^>]*id="` + id + `"[^>]*type="password"|<input[^>]*type="password"[^>]*id="` + id + `"`).MatchString(html) {
			t.Errorf("%s is not a password input", id)
		}
	}
	js := readWeb(t, "static/js/pages/provider-connections.js")
	for _, want := range []string{`const controlKey = "opendesignControlKey"`, `.concat([controlKey])`, `this.add.controlKey = ""`, `this.edit.controlKey = ""`} {
		if !strings.Contains(js, want) {
			t.Errorf("provider-connections.js lacks %q", want)
		}
	}
	if !strings.Contains(readWeb(t, "static/js/pages/provider.js"), `this.id === "opendesign-go"`) {
		t.Error("the control key field is not tied to its provider")
	}
	for _, lang := range []string{"en", "id"} {
		i18n := readWeb(t, "static/i18n/"+lang+".json")
		for _, key := range []string{"provider.controlKey", "provider.controlKeyHint", "provider.controlKeySaved"} {
			if !strings.Contains(i18n, `"`+key+`"`) {
				t.Errorf("%s.json lacks %s", lang, key)
			}
		}
	}
}

// An OAuth login has no key to type, and no provider here sets a priority by
// hand, so the edit dialog offers neither. The default model is the account's
// own answer to what it serves, so the add and edit dialogs offer it alike.
func TestAnOAuthConnectionEditsNoKeyAndNoAutomaticPriority(t *testing.T) {
	html := readWeb(t, "static/pages/provider.html")
	if strings.Contains(html, `id="edit-global"`) {
		t.Error("the edit dialog still offers an automatic priority")
	}
	if !strings.Contains(html, `id="edit-model"`) {
		t.Error("the edit dialog does not offer the default model the add dialog does")
	}
	if !strings.Contains(html, `x-show="editUsesKey()"`) {
		t.Error("the API key input is not tied to the connection's kind")
	}
	js := readWeb(t, "static/js/pages/provider-connections.js")
	for _, want := range []string{`authType: conn.authType || ""`, `editUsesKey() {`, `"oauth"`, `"access_token"`, `"mcode-lease"`, `this.editUsesKey() && this.edit.apiKey !== ""`} {
		if !strings.Contains(js, want) {
			t.Errorf("provider-connections.js lacks %q", want)
		}
	}
	body := methodBody(js, "editBody")
	if strings.Contains(body, "globalPriority") {
		t.Error("editBody still sends an automatic priority")
	}
	if !strings.Contains(body, "defaultModel") {
		t.Error("editBody does not send the default model the dialog edits")
	}
}

// A typed key is checked before it is stored: the add dialog no longer offers
// to save what the check refused, the edit dialog checks a new key, and a node
// dialog checks the optional key it was given. A provider the check cannot
// judge is not a refusal.
func TestAKeyMustPassItsCheckBeforeItIsSaved(t *testing.T) {
	status := readWeb(t, "static/js/pages/provider-status.js")
	for _, want := range []string{"export function checkAllowsSave(check)", "check.valid", "check.unsupported === true"} {
		if !strings.Contains(status, want) {
			t.Errorf("provider-status.js lacks %q", want)
		}
	}

	js := readWeb(t, "static/js/pages/provider-connections.js")
	add := methodBody(js, "saveAdd")
	for _, want := range []string{"validateKey()", "checkAllowsSave"} {
		if !strings.Contains(add, want) {
			t.Errorf("saveAdd lacks %q", want)
		}
	}
	if strings.Contains(add, "saveAnyway") {
		t.Error("saveAdd still offers to save a refused key")
	}
	edit := methodBody(js, "saveEdit")
	for _, want := range []string{"validateEdit()", "checkAllowsSave"} {
		if !strings.Contains(edit, want) {
			t.Errorf("saveEdit lacks %q", want)
		}
	}
	if !strings.Contains(methodBody(js, "setEditKey"), "this.edit.check = null") {
		t.Error("a changed edit key keeps the old result")
	}
	open := methodBody(js, "openEdit")
	for _, want := range []string{"defaultModel:", "check: null", "checking: false"} {
		if !strings.Contains(open, want) {
			t.Errorf("openEdit lacks %q", want)
		}
	}

	nodes := readWeb(t, "static/js/pages/providers.js")
	saveNode := methodBody(nodes, "saveNode")
	for _, want := range []string{"checkNode()", "checkAllowsSave"} {
		if !strings.Contains(saveNode, want) {
			t.Errorf("saveNode lacks %q", want)
		}
	}

	html := readWeb(t, "static/pages/provider.html")
	if !strings.Contains(html, "checkEdit()") {
		t.Error("the edit dialog has no check button")
	}
	for _, want := range []string{"!add.check.unsupported", "!edit.check.unsupported"} {
		if !strings.Contains(html, want) {
			t.Errorf("an invalid badge is not excused by %q", want)
		}
	}
	if !regexp.MustCompile(`<input[^>]*id="add-key"[^>]*required`).MatchString(html) {
		t.Error("the add dialog submits without a key")
	}

	for _, lang := range []string{"en", "id"} {
		i18n := readWeb(t, "static/i18n/"+lang+".json")
		if strings.Contains(i18n, `"provider.saveAnyway"`) {
			t.Errorf("%s.json still offers saveAnyway", lang)
		}
		for _, gone := range []string{"can still be saved", "tetap bisa disimpan"} {
			if strings.Contains(i18n, gone) {
				t.Errorf("%s.json still promises a refused key can be saved", lang)
			}
		}
	}
}

// The live feed is narrowed like the rest of the page: a standard user's frames
// carry only their own keys.
func TestTheUsageFeedNarrowsToTheCallersKeys(t *testing.T) {
	body, err := os.ReadFile("../app/http/controllers/usage_stream.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	if !strings.Contains(src, "c.scope(ctx)") {
		t.Error("the feed never reads the caller's scope")
	}
	if !strings.Contains(src, "c.recent(rctx, keys)") {
		t.Error("the feed asks the store for every key")
	}
}

// Only an administrator checks for updates; a standard user sees a note saying
// so and no button.
func TestOnlyAnAdminChecksForUpdates(t *testing.T) {
	page := readWeb(t, "static/pages/about.html")
	i := strings.Index(page, `x-on:click="checkNow()"`)
	if i < 0 {
		t.Fatal("the About page has no update check")
	}
	line := page[strings.LastIndex(page[:i], "<button"):i]
	if !strings.Contains(line, `x-show="admin"`) {
		t.Error("a standard user is offered the update check")
	}
	if !strings.Contains(page, `x-t="about.checkAdmin"`) {
		t.Error("a standard user is not told only an administrator may check")
	}
}
