package tests

import (
	"go/parser"
	"go/token"
	"io/fs"
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
	allowed := map[string]bool{"rllmx/app/dialect": true, "rllmx/app/document": true, "rllmx/app/file": true, "rllmx/app/sealer": true}
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
