// Copyright (c) 2025 Reliant Labs
package version_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestRCShapeAcceptsTheTrainsSetsAndNothingElse runs scripts/rc-shape.sh's
// rc_shaped, which release-tag.sh --rc and check-release-tags.sh both judge an
// off-main release commit by, over the shapes the RC train makes and the ones
// it never does. A set fixed in place (control-plane docs/design/gitops-promotion.md
// 3.4) is main, the cut's pin commit (an RC-Set: trailer), then fix commits:
// v1.7.88-rc.7's reliant R' got its v1.8.16 bump and two fixes that way.
func TestRCShapeAcceptsTheTrainsSetsAndNothingElse(t *testing.T) {
	t.Parallel()
	testkit.Slow(t, "builds a git repository and runs bash over it")
	script, err := os.ReadFile(filepath.Join(repoRootFromTest(t), "scripts", "rc-shape.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(msg ...string) string {
		args := []string{"commit", "-q", "--allow-empty"}
		for _, m := range msg {
			args = append(args, "-m", m)
		}
		git(args...)
		return git("rev-parse", "HEAD")
	}
	git("init", "-q", "-b", "main")
	commit("m1")
	m2 := commit("m2")

	git("checkout", "-q", "-b", "cut", m2)
	cut := commit("chore(rc): pin forge for v1.8.15-rc.17", "RC-Set: v1.7.88-rc.7")
	fixed := commit("fix: in place")
	git("checkout", "-q", "-b", "legacy", m2)
	legacy := commit("pin with no trailer") // a set cut before the trailer existed: one commit
	legacyFixed := commit("fix on top of it")
	git("checkout", "-q", "-b", "side", m2)
	commit("a PR head")
	sideFixed := commit("chore(rc): pin", "RC-Set: v1.7.88-rc.7") // the trailer is not the first off-main commit
	git("checkout", "-q", "-b", "merged", fixed)
	git("merge", "-q", "--no-ff", "-m", "merge", "legacy")
	merged := git("rev-parse", "HEAD")
	git("checkout", "-q", "main")

	for _, tc := range []struct {
		name   string
		commit string
		want   bool
	}{
		{"main itself is not off main", m2, false},
		{"the cut as made: main + one pin commit", cut, true},
		{"a set fixed in place: pin commit (RC-Set) + fix", fixed, true},
		{"a pre-trailer cut, unfixed", legacy, true},
		{"a pre-trailer cut with a fix on top has no provable pin commit", legacyFixed, false},
		{"a side branch whose pin commit is not first off main", sideFixed, false},
		{"a merge off main", merged, false},
	} {
		cmd := exec.Command("bash", "-c", string(script)+"\nrc_shaped \"$1\" main", "rc-shape", tc.commit)
		cmd.Dir = dir
		got := cmd.Run() == nil
		if got != tc.want {
			t.Errorf("%s: rc_shaped = %v, want %v", tc.name, got, tc.want)
		}
	}
}
