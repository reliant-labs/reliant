package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// resetCmdRoute clears the recorded self-invocation route so each test
// observes only its own execution. The route is package state recorded
// by the root PersistentPreRun; these tests must not run in parallel
// with each other.
func resetCmdRoute(t *testing.T) {
	t.Helper()
	cmdutil.ResetCmdRoute()
	t.Cleanup(cmdutil.ResetCmdRoute)
}

// newTestRootCmd is the only way a test builds the real root command. Running
// one records the mount route (cmdutil.RecordCmdRoute) in package state, which
// changes cmdutil.Name() for every later test in the process ("forge generate"
// becomes "cli.test generate"); a test that forgot to restore it broke
// whichever test happened to run next. The route is cleared before the root is
// built and again when the test ends.
//
// The --project-dir / -C override is the same kind of state: the root's
// PersistentPreRun installs it (cmdutil.SetProjectDir), and nothing clears it
// until the NEXT root runs. A test that ran `forge ... -C <dir>` left every
// later test that resolves a project WITHOUT the root (runDisown, the pin
// guard) reading <dir> instead of its own cwd. It went unnoticed while
// internal/cli ran in one process in file order; sharding its tests across
// jobs (scripts/ci-test-full-shard.sh) put two such tests side by side.
func newTestRootCmd(t *testing.T) *cobra.Command {
	t.Helper()
	resetCmdRoute(t)
	prev, _ := cmdutil.ProjectDirOverride()
	t.Cleanup(func() {
		// prev may be an earlier test's TempDir, gone by now: SetProjectDir
		// refuses a missing directory, so fall back to no override.
		if cmdutil.SetProjectDir(prev) != nil {
			_ = cmdutil.SetProjectDir("")
		}
	})
	return NewRootCmd()
}

// A test that builds the root command through NewRootCmd instead of
// newTestRootCmd leaks the recorded route into the tests after it. Exempt:
// TestMain (the re-exec'd plugin process, outside any test) and this file,
// whose tests reset the route themselves.
func TestNoTestBuildsTheRootCommandWithoutRestoringTheRoute(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f == "main_test.go" || f == "root_selfexec_test.go" {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if strings.Contains(code, "NewRootCmd()") && !strings.Contains(code, "return NewRootCmd()") {
				t.Errorf("%s:%d builds the root command with NewRootCmd(); use newTestRootCmd(t), which restores the recorded route", f, i+1)
			}
		}
	}
}

// A STANDALONE forge binary must self-invoke as [exe] no matter what the
// binary file is named. The test binary's basename is not "forge"
// (…/cli.test), which is exactly the trap: the old basename heuristic
// returned [exe, "forge"], the plugin invocation hit cobra's
// "unknown command", and generate silently degraded.
func TestForgeExecCommand_StandaloneRouteRecorded(t *testing.T) {
	resetCmdRoute(t)

	root := NewRootCmd()
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute standalone `version`: %v", err)
	}

	tokens, recorded := cmdutil.CmdRouteTokens()
	if !recorded {
		t.Fatal("root PersistentPreRun must record the self-invocation route")
	}
	if len(tokens) != 0 {
		t.Fatalf("standalone route tokens = %v, want none", tokens)
	}

	parts, err := forgeExecCommand()
	if err != nil {
		t.Fatalf("forgeExecCommand: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(exe) == "forge" {
		t.Skip("test binary is named forge; the basename trap cannot be exercised")
	}
	if len(parts) != 1 || parts[0] != exe {
		t.Fatalf("standalone self-invocation = %v, want [%s] regardless of the binary's basename", parts, exe)
	}

	// Next-step hints must name a command the user can actually type:
	// the standalone binary itself, never "<basename> forge".
	if got, want := cmdutil.Name(), filepath.Base(os.Args[0]); got != want {
		t.Fatalf("cmdutil.Name() = %q, want the standalone binary name %q", got, want)
	}
}

// When forge is mounted under another cobra root (`reliant forge …`),
// the recorded route must carry the mount name so self-invocation
// re-enters through the parent binary's forge subcommand.
func TestForgeExecCommand_EmbeddedRouteRecorded(t *testing.T) {
	resetCmdRoute(t)

	parent := &cobra.Command{Use: "reliant"}
	parent.AddCommand(NewRootCmd())
	parent.SetArgs([]string{"forge", "version"})
	if err := parent.Execute(); err != nil {
		t.Fatalf("execute embedded `reliant forge version`: %v", err)
	}

	tokens, recorded := cmdutil.CmdRouteTokens()
	if !recorded {
		t.Fatal("root PersistentPreRun must record the self-invocation route when embedded")
	}
	if len(tokens) != 1 || tokens[0] != "forge" {
		t.Fatalf("embedded route tokens = %v, want [forge]", tokens)
	}

	parts, err := forgeExecCommand()
	if err != nil {
		t.Fatalf("forgeExecCommand: %v", err)
	}
	if len(parts) != 2 || parts[1] != "forge" {
		t.Fatalf("embedded self-invocation = %v, want [<exe> forge]", parts)
	}

	if got, want := cmdutil.Name(), filepath.Base(os.Args[0])+" forge"; got != want {
		t.Fatalf("cmdutil.Name() = %q, want %q for the embedded mount", got, want)
	}
}

// Descriptor generation is a HARD pipeline gate: when it fails (here:
// no `buf` on PATH), stepDescriptorGenerate must return the error even
// outside --strict. Pre-fix it routed through warnOrFail, so a failed
// (e.g. misrouted) plugin run printed one warning and generate then
// "succeeded" while emitting no handlers/CRUD/mocks at all.
func TestStepDescriptorGenerate_FailureIsHardError(t *testing.T) {
	dir := t.TempDir()
	protoDir := filepath.Join(dir, "proto", "services", "demo", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proto := "syntax = \"proto3\";\npackage services.demo.v1;\n"
	if err := os.WriteFile(filepath.Join(protoDir, "demo.proto"), []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}

	// An empty PATH makes the `buf` invocation fail deterministically.
	t.Setenv("PATH", t.TempDir())

	ctx := &pipelineContext{ProjectDir: dir, Strict: false}
	err := stepDescriptorGenerate(ctx)
	if err == nil {
		t.Fatal("descriptor-generation failure must fail generate, not degrade to a warning")
	}
	if !strings.Contains(err.Error(), "descriptor generation failed") {
		t.Fatalf("error must name the descriptor gate, got: %v", err)
	}
}
