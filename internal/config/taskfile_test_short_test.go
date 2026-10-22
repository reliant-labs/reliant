// Copyright (c) 2025 Reliant Labs
package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTaskTestShortCoversTheRepoByDefault pins what `task test:short` actually
// hands to `go test`, because the inner-loop tier is only useful if the
// unscoped run covers the whole module and the scoped run covers exactly what
// was asked for.
//
// Its Make predecessor shipped broken: the target read its scope from PKG,
// which the Makefile already defined as the version package for -ldflags, so a
// bare `make test-short` silently tested internal/version alone and reported a
// green run over one package. The scope is `--` arguments now; while the
// Makefile forwards to task (P8, one merge), `make test-short PKGS=...` must
// still reach them.
func TestTaskTestShortCoversTheRepoByDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("execs task, which evaluates the Taskfile's sh: vars (~2-3s a run); skipped under -short")
	}
	taskBin, err := exec.LookPath("task")
	if err != nil {
		t.Skip("task is not installed")
	}
	root := repoRootFromTest(t)
	// Read the Taskfile and Makefile in-process: `task` and `make` below are
	// child processes, and Go's test cache keys only on files the test process
	// itself opens, so without these reads a cached pass would outlive an edit.
	readRepoFile(t, filepath.Join(root, "Taskfile.yml"))
	readRepoFile(t, filepath.Join(root, "Makefile"))

	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "unscoped run covers the whole module", want: "./..."},
		{name: "-- args scope the run", args: []string{"--", "./internal/config/..."}, want: "./internal/config/..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(taskBin, append([]string{"--dry", "test:short"}, tc.args...)...)
			cmd.Dir = root
			cmd.Env = withoutEnv(os.Environ(), "CLI_ARGS", "PKGS", "PKG")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("task --dry test:short %v: %v\n%s", tc.args, err, out)
			}
			line := goTestLine(t, string(out))
			fields := strings.Fields(line)
			if got := fields[len(fields)-1]; got != tc.want {
				t.Fatalf("task test:short %v runs `%s`;\nits package argument is %q, want %q", tc.args, line, got, tc.want)
			}
		})
	}

	t.Run("make test-short PKGS= forwards the scope", func(t *testing.T) {
		makeBin, err := exec.LookPath("make")
		if err != nil {
			t.Skip("make is not installed")
		}
		cmd := exec.Command(makeBin, "-n", "-s", "test-short", "PKGS=./internal/config/...")
		cmd.Dir = root
		cmd.Env = withoutEnv(os.Environ(), "PKGS", "PKG", "MAKEFLAGS")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n test-short: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "test:short -- ./internal/config/...") {
			t.Fatalf("make test-short PKGS=./internal/config/... must run `task test:short -- ./internal/config/...`, got:\n%s", out)
		}
	})
}

// goTestLine returns the single `go test` command task printed.
func goTestLine(t *testing.T, out string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "go test") {
			found = append(found, strings.TrimSpace(line))
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one `go test` command from task --dry test:short, got %d:\n%s", len(found), out)
	}
	return found[0]
}

func withoutEnv(env []string, names ...string) []string {
	kept := env[:0:0]
	for _, kv := range env {
		drop := false
		for _, name := range names {
			if strings.HasPrefix(kv, name+"=") {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, kv)
		}
	}
	return kept
}
