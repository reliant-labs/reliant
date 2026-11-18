package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator/contract"
)

// contractCodegenOutputs are the files contract codegen writes into a
// package. A package holding one can be left uncompilable by an edit to its
// contract until codegen runs again.
var contractCodegenOutputs = []string{"mock_gen.go", "middleware_gen.go", "tracing_gen.go", "metrics_gen.go"}

// TestMockgenImportsNoContractCodegen pins the property mockgen exists for
// (forge#1261): nothing it links carries generated contract code, now or after
// the next `forge generate`. If a package in its import closure had a
// mock_gen.go, adding a method to that package's contract would stop mockgen
// itself from compiling — the bootstrap cycle that forced a second checkout to
// regenerate internal/obsbackend's mock.
//
// So every in-module package mockgen reaches must have no contract-codegen
// output on disk, and every contract.go among them must be opted out
// (`//forge:exclude-contract: <why>` or forge.yaml contracts.exclude), or the
// next generate would give it a mock. The closure is read from source with
// go/parser (every non-test file, build tags ignored, so it is a superset of
// any one build's): the test process opens every file it judges, so Go's test
// cache reruns it when any of them changes.
func TestMockgenImportsNoContractCodegen(t *testing.T) {
	root := moduleRoot(t)
	modulePath := readModulePath(t, root)
	cfg, err := config.LoadProjectDir(root)
	if err != nil {
		t.Fatalf("load forge.yaml: %v", err)
	}

	start := modulePath + "/internal/tools/mockgen"
	seen := map[string]bool{start: true}
	queue := []string{start}
	var problems []string
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		rel := strings.TrimPrefix(strings.TrimPrefix(pkg, modulePath), "/")
		dir := filepath.Join(root, filepath.FromSlash(rel))

		for _, name := range contractCodegenOutputs {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				problems = append(problems, rel+"/"+name+" is generated contract code")
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "contract.go")); err == nil &&
			!contract.ExcludedByDirective(dir) && !cfg.Contracts.IsExcluded(rel) {
			problems = append(problems, rel+"/contract.go is not opted out of contract codegen, so the next forge generate gives it a mock_gen.go")
		}

		for _, imp := range packageImports(t, dir) {
			if (imp == modulePath || strings.HasPrefix(imp, modulePath+"/")) && !seen[imp] {
				seen[imp] = true
				queue = append(queue, imp)
			}
		}
	}
	if len(seen) < 3 {
		t.Fatalf("walked only %d package(s) from %s; the closure walk is broken", len(seen), start)
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Errorf("internal/tools/mockgen must link no package that carries generated contract code: a stale mock there would stop the tool that regenerates it from compiling (forge#1261). Move the dependency out of mockgen's import closure, or opt the package out of contract codegen with a reason:\n  %s",
			strings.Join(problems, "\n  "))
	}
}

// TestMockgenRefreshesAStaleMockThatBreaksTheBuild is the issue's acceptance
// in miniature: a contract gains a method, its mock is now stale, and the
// program that imports the package no longer builds — yet mockgen, which does
// not link the package, regenerates the mock and the build is whole again.
func TestMockgenRefreshesAStaleMockThatBreaksTheBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("execs go list and go build over a temp workspace; runs in task test")
	}
	forgeRoot := moduleRoot(t)
	goVersion := readGoVersion(t, forgeRoot)

	app := t.TempDir()
	// A workspace beside forge's checkout resolves contractkit, which every
	// mock imports, without a require or a module download.
	writeFile(t, filepath.Join(app, "go.work"), "go "+goVersion+"\n\nuse (\n\t.\n\t"+forgeRoot+"\n)\n")
	writeFile(t, filepath.Join(app, "go.mod"), "module example.com/app\n\ngo "+goVersion+"\n")
	writeFile(t, filepath.Join(app, "cmd", "app", "main.go"), `package main

import "example.com/app/internal/greeter"

func main() { _ = greeter.New(greeter.Deps{}) }
`)
	contractPath := filepath.Join(app, "internal", "greeter", "contract.go")
	writeFile(t, contractPath, greeterContract(false))

	generate := func() {
		t.Helper()
		var out bytes.Buffer
		if err := run(app, &out); err != nil {
			t.Fatalf("mockgen: %v\n%s", err, out.String())
		}
	}
	build := func() error {
		t.Helper()
		cmd := exec.Command("go", "build", "./...")
		cmd.Dir = app
		cmd.Env = append(os.Environ(), "GOWORK="+filepath.Join(app, "go.work"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			return &buildError{out: string(out), err: err}
		}
		return nil
	}

	generate()
	if err := build(); err != nil {
		t.Fatalf("fresh mock does not build: %v", err)
	}

	writeFile(t, contractPath, greeterContract(true))
	if err := build(); err == nil {
		t.Fatal("a contract that gained a method still builds against its old mock; the fixture no longer reproduces a stale mock")
	}

	generate()
	if err := build(); err != nil {
		t.Fatalf("mockgen did not repair the stale mock: %v", err)
	}
	mock, err := os.ReadFile(filepath.Join(app, "internal", "greeter", "mock_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mock), "func (m *MockService) Farewell(") {
		t.Errorf("refreshed mock lacks the new method:\n%s", mock)
	}
}

type buildError struct {
	out string
	err error
}

func (e *buildError) Error() string { return e.err.Error() + "\n" + e.out }

// greeterContract is a contract.go whose Service has one method, or two once
// grown.
func greeterContract(grown bool) string {
	method, impl := "", ""
	if grown {
		method = "\tFarewell(name string) string\n"
		impl = "\nfunc (greeter) Farewell(name string) string { return \"bye \" + name }\n"
	}
	return `package greeter

// Service greets.
type Service interface {
	Greet(name string) string
` + method + `}

// Deps is empty.
type Deps struct{}

// New returns the Service.
func New(Deps) Service { return greeter{} }

type greeter struct{}

func (greeter) Greet(name string) string { return "hi " + name }
` + impl
}

// packageImports is every import of dir's non-test .go files.
func packageImports(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Join(dir, name), err)
		}
		for _, spec := range f.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, path)
		}
	}
	return out
}

// moduleRoot is the forge checkout: the nearest ancestor holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}

func readModulePath(t *testing.T, root string) string {
	t.Helper()
	return goModDirective(t, root, "module")
}

func readGoVersion(t *testing.T, root string) string {
	t.Helper()
	return goModDirective(t, root, "go")
}

// goModDirective is the argument of the first top-level `<name> <arg>` line
// of root's go.mod.
func goModDirective(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), name+" "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatalf("go.mod has no %s directive", name)
	return ""
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
