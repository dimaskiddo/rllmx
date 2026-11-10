package tests

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The database drivers are built on GORM and the framework's console on Bubble
// Tea, so both stay in `go list -deps`. What is checked here is our own code:
// it reads and writes through the DB and Schema facades with plain SQL.
var forbiddenImports = []string{"gorm.io/", "github.com/charmbracelet/"}

// ownedRoots are the folders whose hand-written Go files the rules cover. The
// facade files under app/facades are the scaffold's thin accessors.
var ownedRoots = []string{"app", "bootstrap", "config", "routes", "pkg", "tests"}

func goFiles(t *testing.T) map[string]string {
	t.Helper()
	root := ".."
	files := map[string]string{}
	for _, dir := range ownedRoots {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if d == nil && strings.HasSuffix(path, dir) {
					return nil
				}
				return err
			}
			if !d.IsDir() && strings.HasSuffix(path, ".go") {
				files[filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))] = path
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	files["main.go"] = filepath.Join(root, "main.go")
	return files
}

func importsOf(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestOurCodeImportsNoOrmAndNoTerminalUI(t *testing.T) {
	for name, path := range goFiles(t) {
		for _, imp := range importsOf(t, path) {
			for _, bad := range forbiddenImports {
				if strings.HasPrefix(imp, bad) {
					t.Errorf("%s imports %s", name, imp)
				}
			}
		}
	}
}

func TestOnlyTheGitGateMayImportOsExec(t *testing.T) {
	for name, path := range goFiles(t) {
		if strings.HasPrefix(name, "app/git/") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, imp := range importsOf(t, path) {
			if imp == "os/exec" {
				t.Errorf("%s imports os/exec; only app/git may start a process", name)
			}
		}
	}
}

func TestNoProductCodeCallsTheOrmFacade(t *testing.T) {
	for name, path := range goFiles(t) {
		if strings.HasPrefix(name, "app/facades/") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, imp := range importsOf(t, path) {
			if imp == "github.com/goravel/framework/contracts/database/orm" {
				t.Errorf("%s uses the Orm contract", name)
			}
		}
	}
}

func TestTheImportScanSeesTheFilesItShouldCover(t *testing.T) {
	files := goFiles(t)
	for _, want := range []string{"main.go", "app/http/guard.go", "config/app.go", "bootstrap/app.go", "routes/web.go"} {
		if _, ok := files[want]; !ok {
			t.Errorf("%s is not covered by the scan", want)
		}
	}
}

// The root tests package is imported by the in-package tests of other packages,
// so it may import only packages whose tests do not import it back. A helper
// that needs one of those lives in a sub-package (tests/store, tests/usage).
func TestRootTestsPackageImportsOnlyCycleFreePackages(t *testing.T) {
	allowed := map[string]bool{"rllmx/app/store/dialect": true, "rllmx/app/store/document": true, "rllmx/app/platform/file": true, "rllmx/app/store/sealer": true}
	for name, path := range goFiles(t) {
		if filepath.ToSlash(filepath.Dir(name)) != "tests" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, imp := range importsOf(t, path) {
			if strings.HasPrefix(imp, "rllmx/") && !allowed[imp] {
				t.Errorf("%s imports %s; move the helper to a tests sub-package", name, imp)
			}
		}
	}
}

// domainRank orders the domain folders of app/: a package may import its own
// domain and the ones before it, never one after. Folders that are not a domain
// (the Goravel ones: http, providers, console, facades) sit above every domain
// and may import any of them.
var domainRank = map[string]int{"platform": 0, "protocol": 1, "store": 2, "access": 3, "llm": 4, "gateway": 5}

// domainOf names the domain folder of an app/ import path, or "" for a path
// that is not a domain package.
func domainOf(importPath string) string {
	rest, ok := strings.CutPrefix(importPath, "rllmx/app/")
	if !ok {
		return ""
	}
	top, _, _ := strings.Cut(rest, "/")
	if _, isDomain := domainRank[top]; isDomain {
		return top
	}
	return ""
}

func TestNoPackageImportsADomainAfterItsOwn(t *testing.T) {
	for name, path := range goFiles(t) {
		if !strings.HasPrefix(name, "app/") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		from := domainOf("rllmx/" + filepath.ToSlash(filepath.Dir(name)))
		if from == "" {
			continue
		}
		for _, imp := range importsOf(t, path) {
			if to := domainOf(imp); to != "" && domainRank[to] > domainRank[from] {
				t.Errorf("%s (%s) imports %s (%s): imports run down the domains, not up", name, from, imp, to)
			}
		}
	}
}

// protocol packages are wire-format code with no knowledge of the program, so
// they may import only the standard library and platform packages.
func TestProtocolPackagesImportNothingOfTheProgram(t *testing.T) {
	for name, path := range goFiles(t) {
		if !strings.HasPrefix(name, "app/protocol/") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		for _, imp := range importsOf(t, path) {
			if strings.HasPrefix(imp, "rllmx/") {
				t.Errorf("%s imports %s; a protocol package imports nothing of the program", name, imp)
			}
		}
	}
}

// A sub-package may use its parent's framework, never a sibling: what two
// siblings share belongs in the parent as an exported type, and the wiring that
// needs every sibling is one package of its own.
func TestNoSubPackageImportsASibling(t *testing.T) {
	wiring := map[string]bool{"app/llm/executor/mux": true}
	for name, path := range goFiles(t) {
		dir := filepath.ToSlash(filepath.Dir(name))
		if !strings.HasPrefix(name, "app/") || strings.HasSuffix(name, "_test.go") || wiring[dir] || isGoravelFolder(dir) {
			continue
		}
		parent := filepath.ToSlash(filepath.Dir(dir))
		if !hasGoFiles(t, filepath.Join("..", parent)) {
			continue
		}
		for _, imp := range importsOf(t, path) {
			if strings.HasPrefix(imp, "rllmx/"+parent+"/") {
				t.Errorf("%s imports %s: a sub-package imports its parent, never a sibling package", name, imp)
			}
		}
	}
}

// isGoravelFolder reports a folder the framework scaffold owns, whose shape is
// its own: controllers beside the guard they use, providers beside their wiring.
func isGoravelFolder(dir string) bool {
	top, _, _ := strings.Cut(strings.TrimPrefix(dir, "app/"), "/")
	return top == "http" || top == "providers" || top == "console" || top == "facades"
}

// hasGoFiles reports a directory holding a go file of its own package.
func hasGoFiles(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}
