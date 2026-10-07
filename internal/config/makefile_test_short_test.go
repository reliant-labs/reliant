// Copyright (c) 2025 Reliant Labs
package config_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMakeTestShortCoversTheRepoByDefault pins what `make test-short` actually
// hands to `go test`, because the inner-loop tier is only useful if the
// unscoped run covers the whole module and the scoped run covers exactly what
// was asked for.
//
// It shipped broken: the target read its scope from PKG, which the Makefile
// already defines as the version package for -ldflags. So a bare
// `make test-short` silently tested internal/version alone and reported a
// green run over one package — indistinguishable, at a glance, from a green
// run over the repo.
func TestMakeTestShortCoversTheRepoByDefault(t *testing.T) {
	makeBin, err := exec.LookPath("make")
	if err != nil {
		t.Skip("make is not installed")
	}
	root := repoRootFromTest(t)
	// Read the Makefile in-process: `make` below is a child process, and Go's
	// test cache keys only on files the test process itself opens, so without
	// this read a cached pass would outlive an edit to the Makefile.
	readRepoFile(t, filepath.Join(root, "Makefile"))

	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "unscoped run covers the whole module", want: "./..."},
		{name: "PKGS scopes the run", args: []string{"PKGS=./internal/config/..."}, want: "./internal/config/..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(makeBin, append([]string{"-n", "-s", "test-short"}, tc.args...)...)
			cmd.Dir = root
			cmd.Env = withoutEnv(os.Environ(), "PKGS", "PKG", "MAKEFLAGS")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("make -n test-short %v: %v\n%s", tc.args, err, out)
			}
			line := goTestLine(t, string(out))
			fields := strings.Fields(line)
			if got := fields[len(fields)-1]; got != tc.want {
				t.Fatalf("make test-short %v runs `%s`;\nits package argument is %q, want %q", tc.args, line, got, tc.want)
			}
		})
	}
}

// goTestLine returns the single `go test` command make printed.
func goTestLine(t *testing.T, out string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "go test") {
			found = append(found, strings.TrimSpace(line))
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one `go test` command from make -n test-short, got %d:\n%s", len(found), out)
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
