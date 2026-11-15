// File: internal/cli/generate_validate_compile_test.go
//
// The validate step compiles without linking (generate_validate_compile.go).
// These pin the half of that trade nothing else would notice regressing: a
// broken MAIN package still fails validation even though no binary is ever
// linked, the one linker-only error a generator can cause is still caught,
// and the step never shells out to `go build` while go list can answer.

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// TestRunGoBuildValidate_MainPackageTypeErrorFails is the regression the
// no-link validate exists to keep: main packages are no longer linked, so the
// one thing that must not slip is a main package that does not COMPILE.
func TestRunGoBuildValidate_MainPackageTypeErrorFails(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a temp module with the real toolchain; full mode only")
	}
	dir := writeValidateFixture(t, map[string]string{
		"cmd/app/main.go": "package main\n\nimport validateapp \"example.com/validateapp\"\n\n" +
			"func main() { var n string = validateapp.F(); _ = n }\n",
	})

	_, restore := captureStderr(t)
	err := runGoBuildValidate(dir)
	restore()

	if err == nil {
		t.Fatal("a main package with a type error must fail validation")
	}
	if !strings.Contains(err.Error(), "cmd/app/main.go:5:") {
		t.Errorf("the ROOT CAUSE must quote the main package's compiler error by file:line; got: %v", err)
	}
	if !strings.Contains(errOutputOf(err), "# example.com/validateapp/cmd/app") {
		t.Errorf("the compiler output must carry the failing package's header, as go build prints it; got:\n%s", errOutputOf(err))
	}
}

// TestRunGoBuildValidate_BrokenDependencyOfMainFailsOnce: a type error in a
// library reaches the main that imports it through DepsErrors, and must be
// reported once — as the library's error — not once per dependent.
func TestRunGoBuildValidate_BrokenDependencyOfMainFailsOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a temp module with the real toolchain; full mode only")
	}
	dir := writeValidateFixture(t, map[string]string{
		"lib.go":          "package validateapp\n\nfunc F() int { return \"not an int\" }\n",
		"cmd/app/main.go": "package main\n\nimport validateapp \"example.com/validateapp\"\n\nfunc main() { _ = validateapp.F() }\n",
	})

	_, restore := captureStderr(t)
	err := runGoBuildValidate(dir)
	restore()

	if err == nil {
		t.Fatal("a library type error must fail validation")
	}
	out := errOutputOf(err)
	if got := strings.Count(out, "lib.go:3:"); got != 1 {
		t.Errorf("the library's error must be reported exactly once, got %d times:\n%s", got, out)
	}
}

// TestRunGoBuildValidate_MissingMainFuncFails covers the linker-only error a
// generator can plausibly cause: `go build` reports a main package without
// func main from the LINKER, so a check that never links must look for it.
func TestRunGoBuildValidate_MissingMainFuncFails(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a temp module with the real toolchain; full mode only")
	}
	dir := writeValidateFixture(t, map[string]string{
		"cmd/app/app.go": "package main\n\nfunc helper() {}\n",
	})

	_, restore := captureStderr(t)
	err := runGoBuildValidate(dir)
	restore()

	if err == nil {
		t.Fatal("a main package with no func main must fail validation, as go build fails it")
	}
	if !strings.Contains(err.Error(), "cmd/app/app.go: function main is undeclared in the main package") {
		t.Errorf("the failure must name the package's file and the missing main; got: %v", err)
	}
}

// TestRunGoBuildValidate_NeverRunsGoBuild pins the cost property: validation
// (and the post-rollback check) answer with `go list -export`, which compiles
// and never links. A `go build` here means every main package is linked again
// — on reliant, ~130s of a 258s generate. A stand-in `go` records each
// invocation and hands it to the real toolchain.
func TestRunGoBuildValidate_NeverRunsGoBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a temp module with the real toolchain; full mode only")
	}
	if runtime.GOOS == "windows" {
		t.Skip("stand-in toolchain is a POSIX shell script")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH")
	}
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	wrapper := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$FAKE_GO_LOG\"\nexec " + realGo + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GO_LOG", logPath)

	dir := writeValidateFixture(t, map[string]string{
		"cmd/app/main.go": "package main\n\nfunc main() {}\n",
		"lib_test.go":     "package validateapp\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { _ = F() }\n",
	})
	_, restore := captureStderr(t)
	err = runGoBuildValidate(dir)
	cons := goBuildRestoredTree(dir)
	restore()
	if err != nil {
		t.Fatalf("a valid tree must validate; got: %v\n%s", err, errOutputOf(err))
	}
	if !cons.Checked || !cons.Builds {
		t.Fatalf("a valid tree must be reported as compiling after a rollback; got %+v", cons)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("no go invocation was recorded, so this proves nothing: %v", err)
	}
	calls := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, c := range calls {
		if strings.HasPrefix(c, "build") {
			t.Errorf("validation ran `go %s`, which links every main package; it must compile with go list -export instead", c)
		}
	}
	if !strings.Contains(string(data), "list -e -export -deps") {
		t.Errorf("expected the compile to go through `go list -e -export -deps`; calls were:\n%s", data)
	}
}

// TestClassifyCompile pins how go list's per-package verdicts become the
// build's failure and the test files' diagnostics, without a toolchain.
func TestClassifyCompile(t *testing.T) {
	compileErr := func(header, body string) *listedPackageError {
		return &listedPackageError{Err: "# " + header + "\n" + body + "\n"}
	}
	libErr := compileErr("example.com/a/lib", "lib/lib.go:3:24: cannot use \"s\" (untyped string constant) as int value")
	pkgs := []*listedPackage{
		// A broken library, its non-test importer, and the error reaching it.
		{ImportPath: "example.com/a/lib", Name: "lib", Error: libErr},
		{ImportPath: "example.com/a/cmd/app", Name: "main", DepsErrors: []*listedPackageError{libErr}},
		// A dependency missing only from a test import: a test diagnostic.
		{ImportPath: "example.com/a/nothere", DepOnly: true, Error: &listedPackageError{
			Pos: "x/x_test.go:4:8", Err: "no required module provides package example.com/a/nothere; to add it:\n\tgo get example.com/a/nothere",
		}},
		// A test variant failing in a test file, and in a NON-test file (the
		// non-test build already reports that one).
		{ImportPath: "example.com/a/x [example.com/a/x.test]", Name: "x", ForTest: "example.com/a/x", Error: compileErr(
			"example.com/a/x [example.com/a/x.test]",
			"x/x.go:9:2: undefined: y\nx/helpers_test.go:5:43: cannot use 1 (untyped int constant) as string value\n\thave int\n\twant string",
		)},
		// The test main repeats its dependency's error.
		{ImportPath: "example.com/a/x.test", Name: "main", DepsErrors: []*listedPackageError{compileErr(
			"example.com/a/x [example.com/a/x.test]",
			"x/x.go:9:2: undefined: y\nx/helpers_test.go:5:43: cannot use 1 (untyped int constant) as string value\n\thave int\n\twant string",
		)}},
		// An import cycle has no position, only its chain.
		{ImportPath: "example.com/a/cyc", Name: "cyc", Error: &listedPackageError{
			ImportStack: []string{"example.com/a/cyc", "example.com/a/cyc2", "example.com/a/cyc"}, Err: "import cycle not allowed",
		}},
	}

	rep := classifyCompile(t.TempDir(), pkgs)

	wantBuild := "# example.com/a/lib\nlib/lib.go:3:24: cannot use \"s\" (untyped string constant) as int value\n" +
		"package example.com/a/cyc\n\timports example.com/a/cyc2\n\timports example.com/a/cyc: import cycle not allowed\n"
	if rep.Build != wantBuild {
		t.Errorf("Build =\n%s\nwant\n%s", rep.Build, wantBuild)
	}
	wantTests := []string{
		"x/x_test.go:4:8: no required module provides package example.com/a/nothere; to add it:\n\tgo get example.com/a/nothere",
		"x/helpers_test.go:5:43: cannot use 1 (untyped int constant) as string value\n\thave int\n\twant string",
	}
	if !reflect.DeepEqual(rep.TestFiles, wantTests) {
		t.Errorf("TestFiles =\n%q\nwant\n%q", rep.TestFiles, wantTests)
	}
}

// TestClassifyCompile_CleanTree: nothing failed, nothing to report — and a
// main package whose func main exists is not mistaken for one without.
func TestClassifyCompile_CleanTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := classifyCompile(dir, []*listedPackage{
		{ImportPath: "example.com/a", Name: "main", Dir: dir, GoFiles: []string{"main.go"}},
	})
	if rep.Build != "" || len(rep.TestFiles) != 0 {
		t.Errorf("a clean tree reported %+v", rep)
	}
}
