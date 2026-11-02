package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/goexec"
)

// Validating generated code without linking it.
//
// The validate step asks one question — does the tree compile? — and used to
// answer it with `go build -o /dev/null ./...`. That compiles every package and
// then LINKS every main package, writing each binary to /dev/null. Measured on
// reliant (19 main packages, cgo, so each link is an external gcc/ld run of
// 30-40s): the compiles came from the build cache and the links never do, so
// about 130s of a 258s generate went to producing binaries nobody kept. A
// second pass then loaded the whole tree again through go/packages, type
// checking every dependency from source (~85s), only to typecheck the _test.go
// files `go build` never compiles.
//
// One `go list -e -export -test -deps ./...` answers both. -export makes go
// list run the compiler on every package it lists (it reports their export
// data), through the same build cache and action IDs `go build` and `go test`
// use, and go list never links: cmd/go skips the link action under list. -test
// adds each package's test variants, so _test.go files are compiled by the
// real compiler too — the work `go test` does next, which then finds it
// cached. With -e a package's compile error lands in its JSON Error field and
// propagates to every dependent's DepsErrors, so nothing has to be scraped
// from stderr.
//
// What only the linker reports, and how each is covered:
//
//   - a main package with no func main ("function main is undeclared in the
//     main package"): checked here, by parsing each main package's files.
//   - push-linkname violations, duplicate C or assembly symbols, an assembly
//     declaration with no body: none of them can come from Go source forge
//     generates, and catching them is exactly the link this stopped paying for.

// compileFields are the `go list -json` fields compileTree reads.
const compileFields = "ImportPath,Name,Dir,ForTest,DepOnly,GoFiles,CgoFiles,Error,DepsErrors"

// listedPackage is one package from `go list -json`. Only the JSON decoder
// in compileTree writes it; the tags name cmd/go's field spellings, which
// are the wire contract (and keep compileFields and this struct readable
// side by side).
type listedPackage struct {
	ImportPath string                `json:"ImportPath"`
	Name       string                `json:"Name"`
	Dir        string                `json:"Dir"`
	ForTest    string                `json:"ForTest"`
	DepOnly    bool                  `json:"DepOnly"`
	GoFiles    []string              `json:"GoFiles"`
	CgoFiles   []string              `json:"CgoFiles"`
	Error      *listedPackageError   `json:"Error"`
	DepsErrors []*listedPackageError `json:"DepsErrors"`
}

// listedPackageError is cmd/go's PackageError as `go list -json` renders it.
type listedPackageError struct {
	ImportStack []string `json:"ImportStack"`
	Pos         string   `json:"Pos"`
	Err         string   `json:"Err"`
}

// text renders the error the way `go build` prints it: compiler output
// already carries its `# package` header and file:line:col lines; a load
// error is its position (or its import chain) and its message.
func (e *listedPackageError) text() string {
	msg := strings.TrimRight(e.Err, "\n")
	switch {
	case strings.HasPrefix(msg, "# "):
		return msg
	case e.Pos != "":
		return e.Pos + ": " + msg
	case len(e.ImportStack) > 0:
		return "package " + strings.Join(e.ImportStack, "\n\timports ") + ": " + msg
	}
	return msg
}

// testBuild reports whether p belongs to a test build: a package recompiled
// with its _test.go files, an external _test package, or the generated test
// main.
func (p *listedPackage) testBuild() bool {
	return p.ForTest != "" || strings.HasSuffix(p.ImportPath, ".test")
}

// compileReport is what compiling the tree found.
type compileReport struct {
	// Build is what `go build ./...` would have reported, in the order go
	// list met it (dependencies first, so the root cause leads). Empty means
	// every non-test package compiles.
	Build string
	// TestFiles are the diagnostics from the test builds that cite a
	// _test.go file, each with its continuation lines. Diagnostics that cite
	// non-test files are the Build's, reported there.
	TestFiles []string
}

// listFailure is `go list` itself failing (a non-zero exit even under -e): an
// unreadable go.mod, a toolchain that cannot run. It says nothing about the
// code, so callers fall back to the `go build` it replaced, whose report of
// the same problem is the one users know.
type listFailure struct {
	stderr string
	err    error
}

func (e *listFailure) Error() string {
	return fmt.Sprintf("go list: %v: %s", e.err, strings.TrimSpace(e.stderr))
}

// compileTree compiles every package under projectDir — and, with tests, the
// test variant of each — without linking anything.
func compileTree(projectDir string, tests bool) (compileReport, error) {
	args := []string{"list", "-e", "-export", "-deps", "-json=" + compileFields}
	if tests {
		args = append(args, "-test")
	}
	args = append(args, "./...")
	cmd := exec.Command("go", args...)
	cmd.Dir = projectDir
	// Never the caller's -mod=mod: see goexec.Env. -trimpath, so the
	// compile reuses the build cache a sibling worktree, the checkout this
	// scratch copy came from or the last verify-generated filled: see
	// goexec.WithTrimpath.
	cmd.Env = goexec.CheckEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	// go list -e repeats some load errors on stderr; the JSON carries them
	// all, so stderr is kept only to explain a failure of go list itself.
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return compileReport{}, &listFailure{stderr: stderr.String(), err: err}
	}
	var pkgs []*listedPackage
	dec := json.NewDecoder(&stdout)
	for {
		p := new(listedPackage)
		if err := dec.Decode(p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return compileReport{}, &listFailure{stderr: stderr.String(), err: fmt.Errorf("decode go list output: %w", err)}
		}
		pkgs = append(pkgs, p)
	}
	return classifyCompile(projectDir, pkgs), nil
}

// classifyCompile splits go list's verdict into the non-test build and the
// test builds.
//
// The non-test build is judged by the packages ./... names (not the
// dependencies, not test variants): each one's own Error and its DepsErrors.
// DepsErrors is transitive, so a broken dependency reaches every root that
// imports it, and an error that only a test import reaches never does.
//
// Everything else that failed belongs to a test build. Only the lines citing
// a _test.go file are kept: a non-test file failing in a test variant fails
// in its non-test build too, which reported it already; a test-only load
// problem with no file to cite is go test's to report, and was never this
// step's.
func classifyCompile(projectDir string, pkgs []*listedPackage) compileReport {
	var (
		build []string
		seen  = map[string]bool{}
	)
	for _, p := range pkgs {
		if p.DepOnly || p.testBuild() {
			continue
		}
		for _, e := range p.errors() {
			if t := e.text(); !seen[t] {
				seen[t] = true
				build = append(build, t)
			}
		}
		if p.Error == nil && len(p.DepsErrors) == 0 && p.Name == "main" {
			if t := missingMainFunc(projectDir, p); t != "" && !seen[t] {
				seen[t] = true
				build = append(build, t)
			}
		}
	}

	var testFiles []string
	for _, p := range pkgs {
		for _, e := range p.errors() {
			t := e.text()
			if seen[t] {
				continue
			}
			seen[t] = true
			testFiles = append(testFiles, testFileDiagnostics(t)...)
		}
	}

	rep := compileReport{TestFiles: testFiles}
	if len(build) > 0 {
		rep.Build = strings.Join(build, "\n") + "\n"
	}
	return rep
}

// errors returns the package's own error followed by its dependencies'.
func (p *listedPackage) errors() []*listedPackageError {
	var out []*listedPackageError
	if p.Error != nil {
		out = append(out, p.Error)
	}
	for _, e := range p.DepsErrors {
		if e != nil {
			out = append(out, e)
		}
	}
	return out
}

// testFileDiagnostics returns each diagnostic in output that cites a _test.go
// file, with the indented continuation lines (have/want, and the like) that
// follow it.
func testFileDiagnostics(output string) []string {
	var (
		out     []string
		current []string
	)
	flush := func() {
		if len(current) > 0 {
			out = append(out, strings.Join(current, "\n"))
			current = nil
		}
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if len(current) > 0 {
				current = append(current, line)
			}
			continue
		}
		flush()
		if strings.HasSuffix(errorLineFile(line), "_test.go") {
			current = []string{strings.TrimSpace(line)}
		}
	}
	flush()
	return out
}

// missingMainFunc reports a main package that declares no func main — the one
// error `go build` reports from the linker that a forge generator could
// plausibly cause. It names the package's first file so the line carries a
// coordinate the failure report can quote and classify. "" when main exists,
// or when the files cannot be read (the compiler, which just read them, is
// the authority on unreadable source).
func missingMainFunc(projectDir string, p *listedPackage) string {
	files := append(append([]string(nil), p.GoFiles...), p.CgoFiles...)
	if len(files) == 0 {
		return ""
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return ""
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "main" {
				return ""
			}
		}
	}
	cite := filepath.Join(p.Dir, files[0])
	if rel, err := filepath.Rel(projectDir, cite); err == nil && !strings.HasPrefix(rel, "..") {
		cite = rel
	}
	return fmt.Sprintf("# %s\n%s: function main is undeclared in the main package", p.ImportPath, filepath.ToSlash(cite))
}

// goBuildFallback runs the `go build` compileTree replaced, for when go list
// itself could not run. It returns the build's captured stderr and its error.
// os.DevNull: a compile check must never write a binary into the project (a
// lone main package in ./echo once failed validation with `build output
// "echo" already exists and is a directory`).
func goBuildFallback(projectDir string, stream io.Writer) (string, error) {
	cmd := exec.Command("go", "build", "-o", os.DevNull, "./...")
	cmd.Dir = projectDir
	cmd.Env = goexec.CheckEnv()
	var out strings.Builder
	if stream != nil {
		cmd.Stdout = stream
		cmd.Stderr = io.MultiWriter(stream, &out)
	} else {
		cmd.Stdout = &out
		cmd.Stderr = &out
	}
	err := cmd.Run()
	return out.String(), err
}
