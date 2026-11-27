package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/hostlaunch"
)

// Editing examples/ (a separate binary the server never imports) restarted
// the air-run API three times in one demo. `forge env up` now launches air
// with the directories the binary is not built from added to the config's
// own exclude_dir, says so in one line, and leaves .air.toml untouched; a
// failure to derive the list launches air as configured, with one notice.
func TestEnvUpAirIgnoresDirsTheBinaryNeverImports(t *testing.T) {
	clearProjectDir(t)
	project := writeProjectDirFixture(t, "acme")
	airToml := "[build]\n  cmd = \"go build -o ./tmp/api ./cmd/api\"\n  exclude_dir = [\"tmp\", \"my-own\"]\n"
	for name, body := range map[string]string{
		".air.toml":             airToml,
		"cmd/api/main.go":       "package main",
		"examples/demo/main.go": "package main",
	} {
		p := filepath.Join(project, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmdutil.SetProjectDir(project); err != nil {
		t.Fatal(err)
	}
	w := hostWL("api", func(w *WorkloadEntity) { w.Runtime.Host.Runner = "air" })
	orig := airDepsLister
	t.Cleanup(func() { airDepsLister = orig })

	airDepsLister = func(context.Context, string, string, string) (hostlaunch.GoDeps, error) {
		main := filepath.Join(project, "cmd", "api")
		return hostlaunch.GoDeps{Main: main, Paths: []string{main}}, nil
	}
	var cmdArgs []string
	out := captureStdout(t, func() {
		cmd, _, err := buildHostServiceCmd(t.Context(), nil, w, nil, "dev")
		if err != nil {
			t.Fatal(err)
		}
		cmdArgs = cmd.Args
	})
	if want := []string{"air", "-c", ".air.toml", "-build.exclude_dir", "tmp,my-own,examples"}; !slices.Equal(cmdArgs, want) {
		t.Errorf("argv = %q, want %q", cmdArgs, want)
	}
	if !strings.Contains(out, "[up] host api: air ignores examples/ (no package ./cmd/api imports") {
		t.Errorf("env up did not say which directories air ignores:\n%s", out)
	}
	if got, _ := os.ReadFile(filepath.Join(project, ".air.toml")); string(got) != airToml {
		t.Errorf(".air.toml was edited:\n%s", got)
	}

	airDepsLister = func(context.Context, string, string, string) (hostlaunch.GoDeps, error) {
		return hostlaunch.GoDeps{}, errors.New("go list ./cmd/api: build constraints exclude all Go files\nmore")
	}
	out = captureStdout(t, func() {
		cmd, _, err := buildHostServiceCmd(t.Context(), nil, w, nil, "dev")
		if err != nil {
			t.Fatal(err)
		}
		cmdArgs = cmd.Args
	})
	if want := []string{"air", "-c", ".air.toml"}; !slices.Equal(cmdArgs, want) {
		t.Errorf("argv after a go list failure = %q, want air as configured %q", cmdArgs, want)
	}
	if !strings.Contains(out, "[up] host api: air watches every directory: could not list the packages ./cmd/api imports (go list ./cmd/api: build constraints exclude all Go files)") {
		t.Errorf("env up did not name the go list failure in one line:\n%s", out)
	}
}
