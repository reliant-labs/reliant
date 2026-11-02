package ciguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	premergeScript = filepath.Join("..", "..", "scripts", "ci-premerge.sh")
	taskfile       = filepath.Join("..", "..", "Taskfile.yml")
)

// TestLocalPremergeGateMirrorsThePullRequestTier holds `task ci` — the
// command a merge steward gates a forge pull request with — to ci.yml's
// pull-request tier. A local gate that drifted from it would pass a pull
// request the merge queue then rejects, or (worse) run a check the queue does
// not and teach the steward to waive it.
//
//   - `task ci` runs scripts/ci-premerge.sh: one spelling of the gate.
//   - its tests are the tier's own commands: scripts/ci-test-shard.sh over
//     every package and scripts/ci-changed-tests.sh in its short mode, the
//     same two ci.yml's test-short matrix runs. So a flag change there
//     reaches both, and the replayed results are keyed the same way.
//   - it never adds -race or -count=1 itself: the pre-merge tier reuses Go's
//     test cache, and -race runs on main (the owner's rule for every repo's
//     pre-merge gate).
//   - it runs the checks job's steps, by the same commands.
func TestLocalPremergeGateMirrorsThePullRequestTier(t *testing.T) {
	script := readText(t, premergeScript)
	tasks := readText(t, taskfile)
	ci := readText(t, ciWorkflow)

	if !regexp.MustCompile(`(?m)^  ci:\n(?:    .*\n|\n)*?      - scripts/ci-premerge\.sh`).MatchString(tasks) {
		t.Errorf("%s: the `ci` task must run scripts/ci-premerge.sh", taskfile)
	}

	for _, want := range []string{
		"scripts/ci-test-shard.sh 0 1",
		`scripts/ci-changed-tests.sh "$base"`,
		"scripts/check-changelog-untouched.sh",
		"go run ./internal/apigate",
		"forge\" ci verify-generated",
		"forge\" ci validate-kcl",
		"SKIP=go-vet-mod,proto-generated",
		"scripts/check-licenses.sh",
		"scripts/check-npm-licenses.sh web-runtime",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("%s does not run %q, which ci.yml's pull-request tier does", premergeScript, want)
		}
	}
	for _, want := range []string{
		"scripts/ci-test-shard.sh ${{ matrix.args }}",
		"scripts/ci-changed-tests.sh HEAD^1\n",
		"scripts/check-changelog-untouched.sh HEAD^1",
		"go run ./internal/apigate",
		"forge ci verify-generated",
		"forge ci validate-kcl",
		"SKIP: go-vet-mod,proto-generated",
		"./scripts/check-licenses.sh",
		"./scripts/check-npm-licenses.sh web-runtime",
	} {
		if !strings.Contains(ci, want) {
			t.Errorf("%s no longer runs %q: update %s to match (and this list)", ciWorkflow, want, premergeScript)
		}
	}

	for _, line := range strings.Split(script, "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") {
			continue
		}
		for _, banned := range []string{"-race", "-count=1"} {
			if strings.Contains(code, banned) {
				t.Errorf("%s runs %s (%q): the pre-merge tier reuses the test cache and leaves -race to main", premergeScript, banned, code)
			}
		}
		if strings.Contains(code, "ci-changed-tests.sh") && strings.Contains(code, " full") {
			t.Errorf("%s runs the flake gate in full mode (%q), which adds -race: that is main's", premergeScript, code)
		}
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// A Windows checkout may carry CRLF line endings.
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}
