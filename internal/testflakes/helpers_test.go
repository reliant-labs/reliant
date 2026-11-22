package testflakes

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/testcapture"
	"github.com/reliant-labs/forge/pkg/release"
)

const testModule = "example.com/m"

// newModule is a temporary module root with go.mod and the named package
// directories, each holding the given test files (name -> source).
func newModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "go.mod"), "module "+testModule+"\n\ngo 1.22\n")
	for name, src := range files {
		write(t, filepath.Join(root, filepath.FromSlash(name)), src)
	}
	return root
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func noEnv(string) string { return "" }

// begin opens runs under root as a checkout at commit/tree would.
func begin(root, commit, tree string, at time.Time) testcapture.BeginOptions {
	return testcapture.BeginOptions{
		Dir: root, ProjectRoot: root, Getenv: noEnv,
		Now: func() time.Time { return at },
		Provenance: func(context.Context, string) (release.Provenance, error) {
			return release.Provenance{Commit: commit, Tree: tree}, nil
		},
	}
}

// ev is one `go test -json` event line.
func ev(action, pkg, test, output string) string {
	s := fmt.Sprintf(`{"Action":%q,"Package":%q`, action, pkg)
	if test != "" {
		s += fmt.Sprintf(`,"Test":%q`, test)
	}
	if output != "" {
		s += fmt.Sprintf(`,"Output":%q`, output)
	}
	return s + "}\n"
}

// passRun and failRun are one attempt of a test.
func passRun(pkg, test string) string {
	return ev("run", pkg, test, "") + ev("pass", pkg, test, "")
}

func failRun(pkg, test, output string) string {
	return ev("run", pkg, test, "") + ev("output", pkg, test, output) + ev("fail", pkg, test, "")
}

// fakeRunner answers each invocation with the stream script returns for its
// argv, and records the argv.
type fakeRunner struct {
	calls  [][]string
	envs   [][]string
	script func(argv []string) (stream string, exit int)
}

func (f *fakeRunner) run(_ context.Context, _ string, argv, env []string, stdout, _ io.Writer) (int, error) {
	f.calls = append(f.calls, argv)
	f.envs = append(f.envs, env)
	stream, exit := f.script(argv)
	_, _ = io.WriteString(stdout, stream)
	return exit, nil
}

// argAfter is the value after flag in argv.
func argAfter(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func hasArg(argv []string, arg string) bool {
	for _, a := range argv {
		if a == arg {
			return true
		}
	}
	return false
}
