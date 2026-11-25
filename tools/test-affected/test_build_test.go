package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// Exercise the real discovery and compiler, not the gate's phase-launch stub.
func TestTestBuild(t *testing.T) {
	testkit.Slow(t, "compiles throwaway Go test packages through test-build.sh",
		testkit.Inputs("scripts/test-build.sh"))
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "test-build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		broken  string
		exclude string
		wantErr bool
	}{
		{name: "valid untagged external and tagged tests"},
		{name: "untagged compile error", broken: "plain", wantErr: true},
		{name: "external compile error", broken: "external", wantErr: true},
		{name: "tagged compile error", broken: "tagged", wantErr: true},
		{name: "exact exclusion", broken: "plain", exclude: "./plain\n"},
		{name: "exclusion does not omit other packages", broken: "external", exclude: "./plain\n", wantErr: true},
		{name: "full untagged exclusion", broken: "plain", exclude: "./...\n"},
		{name: "exact exclusion keeps tagged checks", broken: "tagged", exclude: "./tagged\n", wantErr: true},
		{name: "full exclusion keeps tagged checks", broken: "tagged", exclude: "./...\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, content string) {
				t.Helper()
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write("go.mod", "module example.com/testbuild\n\ngo 1.23\n")
			write("test-build.sh", string(script))
			for _, pkg := range []string{"plain", "external", "tagged"} {
				header := "package " + pkg
				if pkg == "external" {
					write(pkg+"/source.go", header+"\n")
					header += "_test"
				}
				if pkg == "tagged" {
					header = "//go:build fixturetag\n\n" + header
				}
				body := "\nimport \"testing\"\n\nfunc TestFixture(t *testing.T) { panic(\"must compile, never run\") }\nfunc TestMain(m *testing.M) { panic(\"must not run TestMain\") }\n"
				if tc.broken == pkg {
					body += "var _ = missingFixtureSymbol\n"
				}
				write(pkg+"/fixture_test.go", header+body)
			}
			// Tagged discovery uses git ls-files, so stage every fixture, including
			// the package whose only source file is behind a custom build tag.
			for _, args := range [][]string{{"init", "-q"}, {"add", "go.mod", "plain", "external", "tagged"}} {
				cmd := exec.CommandContext(t.Context(), "git", args...)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			args := []string{"test-build.sh", "-p", "2"}
			if tc.exclude != "" {
				write("exclude", tc.exclude)
				args = append(args, "--exclude", "exclude")
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOPROXY=off", "GOTOOLCHAIN=local")
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("test-build timed out: %v\n%s", ctx.Err(), out)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("test-build error = %v, want error = %v\n%s", err, tc.wantErr, out)
			}
			if tc.wantErr && (!strings.Contains(string(out), tc.broken+"/fixture_test.go") || !strings.Contains(string(out), "undefined: missingFixtureSymbol")) {
				t.Fatalf("failure must name the broken fixture and compiler error, not an unrelated failure:\n%s", out)
			}
		})
	}
}
