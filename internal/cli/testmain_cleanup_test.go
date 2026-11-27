package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/ledgerfile"
)

// Every temp dir TestMain creates for the whole process is removed after the
// suite: it lives as long as the process, so no test's t.TempDir can own it,
// and an unregistered one is left behind on every run.
func TestTestMainRegistersItsProcessWideTempDirs(t *testing.T) {
	dir := connectStateDir()
	if !strings.HasPrefix(filepath.Base(dir), "forge-cli-state-") || !strings.HasPrefix(dir, os.TempDir()) {
		t.Fatalf("connectStateDir is %q, not TestMain's temp dir", dir)
	}
	if !slices.Contains(processStateDirs(), dir) {
		t.Errorf("TestMain's connect state dir %s is not registered for removal, so every run leaks it", dir)
	}
}

// tempDirGuardEnv selects what TestTempDirGuardHelper does in a process
// TestTestMainLeavesNothingInTMPDIR launches.
const tempDirGuardEnv = "FORGE_CLI_TEMPDIR_GUARD_HELPER"

// TestTempDirGuardHelper is not a test: it is the process the leak guard
// launches, selected by FORGE_CLI_TEMPDIR_GUARD_HELPER.
func TestTempDirGuardHelper(t *testing.T) {
	switch os.Getenv(tempDirGuardEnv) {
	case "pass":
		_ = connectStateDir()
	case "fail":
		_ = connectStateDir()
		t.Fatal("failing on purpose: a failed run removes its state dirs too")
	case "exit":
		// The shape of every helper process this package launches: TestMain
		// runs, then the helper exits from inside a test, so TestMain's
		// cleanup never runs.
		os.Exit(0)
	}
}

// The test binary leaves nothing in $TMPDIR, however it exits. Each case runs
// this binary in a fresh TMPDIR and asserts the dir is empty afterwards. Before
// this, every helper process left an empty forge-cli-state-* (~8.7k a day on
// one machine) and every failed run a forge-test-ledger-*.
func TestTestMainLeavesNothingInTMPDIR(t *testing.T) {
	if os.Getenv(tempDirGuardEnv) != "" {
		t.Skip("running as the guard's helper")
	}
	// A helper inherits the parent's ledger home, as every real helper does;
	// the other cases make their own, as a top-level run does.
	inheritLedger := map[string]bool{"exit": true}
	cases := []struct {
		name     string
		args     []string
		helper   string
		wantFail bool
	}{
		{name: "passing run", args: []string{"-test.run=^TestTempDirGuardHelper$"}, helper: "pass"},
		{name: "failing run", args: []string{"-test.run=^TestTempDirGuardHelper$"}, helper: "fail", wantFail: true},
		{name: "helper exits from a test", args: []string{"-test.run=^TestTempDirGuardHelper$"}, helper: "exit"},
		// TestMain's own dispatches, which exit before m.Run.
		{name: "plugin dispatch", args: []string{"forge", "protoc-gen-forge"}},
		{name: "credential helper dispatch", args: []string{fakeCredentialHelperArg, "tok"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			cmd := exec.Command(os.Args[0], tc.args...)
			cmd.Stdin = strings.NewReader("")
			for _, kv := range os.Environ() {
				key, _, _ := strings.Cut(kv, "=")
				switch {
				case key == "TMPDIR", key == "TMP", key == "TEMP", key == "FORGE_KEEP_TEST_TREES", key == tempDirGuardEnv:
					continue
				case key == ledgerfile.DefaultHomeEnv && !inheritLedger[tc.helper]:
					continue
				}
				cmd.Env = append(cmd.Env, kv)
			}
			// TMP and TEMP are what os.TempDir reads on Windows.
			cmd.Env = append(cmd.Env, "TMPDIR="+tmp, "TMP="+tmp, "TEMP="+tmp, tempDirGuardEnv+"="+tc.helper)
			out, err := cmd.CombinedOutput()
			if gotFail := err != nil; gotFail != tc.wantFail {
				t.Fatalf("exit error = %v, want failure %v\n%s", err, tc.wantFail, out)
			}
			entries, err := os.ReadDir(tmp)
			if err != nil {
				t.Fatal(err)
			}
			var left []string
			for _, e := range entries {
				// golang.org/x/tools' module index makes $TMPDIR/goimports
				// at package init under testing.Testing(): one fixed name,
				// reused by every run, not a per-process leak.
				if e.Name() == "goimports" {
					continue
				}
				left = append(left, e.Name())
			}
			if len(left) > 0 {
				t.Errorf("the test binary left %v in $TMPDIR\n%s", left, out)
			}
		})
	}
}
