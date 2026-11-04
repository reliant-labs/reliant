package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The child reads only copies of scripts this process opened: the short tier's
// cache sees the production input, and no command touches the real git checkout.
func changedTestsFixture(t *testing.T, mode string, fail bool) (string, error) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"bin", "one", "two", "scripts"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci-changed-tests.sh"))
	if err != nil {
		t.Fatal(err)
	}
	write("changed.sh", string(body))
	helper, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci-go-test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	write("scripts/ci-go-test.sh", string(helper))
	write("one/one_test.go", "package one\nfunc TestOne(t *testing.T) {}\n")
	write("two/two_test.go", "package two\nfunc TestTwo(t *testing.T) {}\n")
	// These are fixture executables, not invocations of git/go against the
	// checkout. Both changed packages are selected, with different test names.
	write("bin/git", `#!/usr/bin/env bash
if [[ "$*" == *--name-only* ]]; then
  printf 'one/one_test.go\ntwo/two_test.go\n'
elif [[ "$*" == *one/one_test.go* ]]; then
  echo '+func TestOne(t *testing.T) {'
else
  echo '+func TestTwo(t *testing.T) {'
fi
`)
	// The physical directory, as the real `go list -f {{.Dir}}` prints it and
	// as the script's own `pwd -P` spells it. $PWD is not that on Windows,
	// where the temp dir's 8.3 short name (RUNNER~1) survives in it: every
	// package read as "not a package of this module" and the Windows job
	// failed these tests on every push.
	write("bin/go", `#!/usr/bin/env bash
if [[ "$1" == list ]]; then
  printf '%s/one\n%s/two\n' "$(pwd -P)" "$(pwd -P)"
else
  printf 'TEST %s\n' "$*"
  [[ "$FIXTURE_FAIL" != 1 ]]
fi
`)
	cmd := exec.Command("bash", "changed.sh", "fixture-base", mode)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"), "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"))
	if fail {
		cmd.Env = append(cmd.Env, "FIXTURE_FAIL=1")
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestChangedTestsUseOneParallelGoInvocation(t *testing.T) {
	t.Parallel()
	out, err := changedTestsFixture(t, "short", false)
	if err != nil {
		t.Fatalf("changed tests: %v\n%s", err, out)
	}
	var invocations []string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "TEST ") {
			invocations = append(invocations, line)
		}
	}
	if len(invocations) != 1 {
		t.Fatalf("want one go test invocation so Go schedules selected packages together, got %d:\n%s", len(invocations), out)
	}
	for _, want := range []string{"-short", "-count=3", "TestOne", "TestTwo", "./one", "./two"} {
		if !strings.Contains(invocations[0], want) {
			t.Errorf("missing %q in %s", want, invocations[0])
		}
	}
	if strings.Contains(invocations[0], "-race") {
		t.Errorf("premerge should reuse the non-race short-tier artifacts: %s", invocations[0])
	}
}

func TestChangedTestsFullRetainsRaceAndRepetition(t *testing.T) {
	t.Parallel()
	out, err := changedTestsFixture(t, "full", false)
	if err != nil {
		t.Fatalf("full changed tests: %v\n%s", err, out)
	}
	for _, want := range []string{"-short", "-race", "-count=3"} {
		if !strings.Contains(out, want) {
			t.Errorf("full mode lost %q:\n%s", want, out)
		}
	}
}

func TestChangedTestsPropagateFailure(t *testing.T) {
	t.Parallel()
	out, err := changedTestsFixture(t, "short", true)
	if err == nil {
		t.Fatalf("failed go test must fail the gate:\n%s", out)
	}
}

func TestChangedTestsRejectInvalidMode(t *testing.T) {
	t.Parallel()
	out, err := changedTestsFixture(t, "typo", false)
	if err == nil {
		t.Fatalf("unknown mode must not silently pick test flags:\n%s", out)
	}
}
