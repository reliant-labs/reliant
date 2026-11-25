// Package connectv2test builds throwaway Go modules that compile forge's
// connect-go v2 codegen output against REAL generated stubs: buf runs this
// module's own protoc-gen-go and connect-go v2 protoc-gen-connect-go over a
// proto the test supplies, and the module resolves github.com/reliant-labs/forge
// to this checkout through a replace directive. A test then writes the files
// forge renders into the module and runs `go build` / `go vet` / `go test`
// in it — the only check that a v2 template's signatures are the ones the
// generated Handler interface declares.
//
// It is slow (buf, plugin builds, a cold `go build`), so every entry point
// skips under -short; the tests that use it run in `task test`.
//
//forge:exclude-contract: test plumbing, no part of the forge binary: one concrete *Module over a temp directory, used only by tests
package connectv2test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/goexec"
)

// Module is a temp Go module wired to this forge checkout.
type Module struct {
	// Dir is the module root (it holds go.mod).
	Dir string
	// Path is the module path, e.g. "example.com/v2app".
	Path string
}

var (
	pluginsOnce sync.Once
	pluginsDir  string
	pluginsErr  string
)

// forgeRoot is the root of the forge module the running test belongs to:
// a test's working directory is its package directory, inside the module.
// (runtime.Caller cannot say — builds here may be -trimpath.)
func forgeRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("go", "env", "GOMOD")
	cmd.Env = goexec.Env()
	out, err := cmd.Output()
	gomod := strings.TrimSpace(string(out))
	if err != nil || gomod == "" || gomod == os.DevNull {
		t.Fatalf("connectv2test: cannot locate the forge module from the test's directory (go env GOMOD: %q, %v)", gomod, err)
	}
	return filepath.Dir(gomod)
}

// plugins builds protoc-gen-go and the connect-go v2 protoc-gen-connect-go
// from forge's own module graph, once per test process, so the stubs match
// the runtime versions forge's go.mod pins.
func plugins(t *testing.T) string {
	t.Helper()
	root := forgeRoot(t)
	pluginsOnce.Do(func() {
		dir, err := os.MkdirTemp("", "forge-connectv2-plugins-")
		if err != nil {
			pluginsErr = err.Error()
			return
		}
		for name, pkg := range map[string]string{
			"protoc-gen-go":         "google.golang.org/protobuf/cmd/protoc-gen-go",
			"protoc-gen-connect-go": "connectrpc.com/connect/v2/cmd/protoc-gen-connect-go",
		} {
			cmd := exec.Command("go", "build", "-o", filepath.Join(dir, name), pkg)
			cmd.Dir = root
			cmd.Env = goexec.Env()
			if out, err := cmd.CombinedOutput(); err != nil {
				pluginsErr = "build " + pkg + ": " + err.Error() + "\n" + string(out)
				return
			}
		}
		pluginsDir = dir
	})
	if pluginsErr != "" {
		t.Fatalf("connectv2test: %s", pluginsErr)
	}
	return pluginsDir
}

// NewModule creates a module at t.TempDir() whose go.mod requires
// connect-go v2 directly (so internal/connectmajor reads it as a v2
// project) and replaces forge with this checkout, then generates Go and
// connect-go v2 stubs for protoFiles (path under proto/ → source) into
// gen/ with paths=source_relative. It skips under -short, and when buf is
// not installed.
func NewModule(t *testing.T, modulePath string, protoFiles map[string]string) *Module {
	t.Helper()
	if testing.Short() {
		t.Skip("builds codegen plugins, runs buf and compiles a module; runs in task test")
	}
	buf, err := exec.LookPath("buf")
	if err != nil {
		t.Skip("buf is not installed; it generates the connect-go v2 stubs this check compiles against")
	}
	root := forgeRoot(t)
	bin := plugins(t)
	dir := t.TempDir()

	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goSum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	// The module requires everything forge's go.mod requires, at forge's
	// versions, so its build list is forge's and go.sum covers it.
	var requires []string
	for _, line := range strings.Split(string(goMod), "\n") {
		if strings.HasPrefix(line, "go ") || strings.HasPrefix(line, "toolchain ") {
			requires = append(requires, line)
		}
	}
	write(t, filepath.Join(dir, "go.mod"), "module "+modulePath+"\n\n"+strings.Join(requires, "\n")+"\n\n"+
		"require github.com/reliant-labs/forge v0.0.0\n\n"+
		requireBlock(goMod)+"\n"+
		"replace github.com/reliant-labs/forge => "+root+"\n")
	write(t, filepath.Join(dir, "go.sum"), string(goSum))

	for rel, src := range protoFiles {
		write(t, filepath.Join(dir, "proto", rel), src)
	}
	write(t, filepath.Join(dir, "buf.yaml"), "version: v2\nmodules:\n  - path: proto\n")
	write(t, filepath.Join(dir, "buf.gen.yaml"), "version: v2\nplugins:\n"+
		"  - local: "+filepath.Join(bin, "protoc-gen-go")+"\n    out: gen\n    opt: paths=source_relative\n"+
		"  - local: "+filepath.Join(bin, "protoc-gen-connect-go")+"\n    out: gen\n    opt: paths=source_relative\n")
	run(t, dir, buf, "generate")
	return &Module{Dir: dir, Path: modulePath}
}

// requireBlock returns forge's go.mod require directives verbatim.
func requireBlock(goMod []byte) string {
	var b strings.Builder
	in := false
	for _, line := range strings.Split(string(goMod), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "require ("):
			in = true
			b.WriteString(line + "\n")
		case in:
			if strings.Contains(line, "connectrpc.com/connect/v2 ") {
				// v2 must be a DIRECT requirement: that is what makes
				// connectmajor read the module as a v2 project.
				line, _, _ = strings.Cut(line, "//")
			}
			b.WriteString(line + "\n")
			if trimmed == ")" {
				in = false
			}
		case strings.HasPrefix(trimmed, "require "):
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// Write writes a file under the module, creating its directory.
func (m *Module) Write(t *testing.T, rel, content string) {
	t.Helper()
	write(t, filepath.Join(m.Dir, rel), content)
}

// Go runs the go command in the module and fails the test on a non-zero
// exit, printing its output. -mod=mod lets the build list settle on what
// the module's packages import; GOPROXY=off keeps it to the local cache.
func (m *Module) Go(t *testing.T, args ...string) string {
	t.Helper()
	return run(t, m.Dir, "go", args...)
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOWORK=off"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
