package cli

import (
	"context"
	"strings"
	"testing"
)

// A --no-build cut harvests digests an earlier build produced but seals the
// release with the checkout's HEAD. When those are different commits the
// release is immutable and wrong (it names code the images were never built
// from) and the deploy's freshness check refuses it later, after the hosted
// ledger already holds the record. The cut must refuse instead.
func TestCheckCutMatchesBuild(t *testing.T) {
	const built = "fec19d93310ccbf9502f8131b0db5a16d70a406e"
	const other = "e40d02eb8f77aaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	t.Run("refuses_a_checkout_at_another_commit", func(t *testing.T) {
		err := checkCutMatchesBuild("v1", other, []string{built})
		if err == nil {
			t.Fatal("a cut at commit e40d02eb over a build from fec19d93 was accepted")
		}
		for _, want := range []string{"fec19d93", "e40d02eb", "cannot be amended", "forge env build"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal omits %q: %v", want, err)
			}
		}
	})
	t.Run("accepts_the_commit_that_was_built", func(t *testing.T) {
		if err := checkCutMatchesBuild("v1", built, []string{built}); err != nil {
			t.Errorf("a cut at the built commit was refused: %v", err)
		}
	})
	t.Run("refuses_when_any_recorded_build_differs", func(t *testing.T) {
		if err := checkCutMatchesBuild("v1", built, []string{built, other}); err == nil {
			t.Error("two build states from two commits were accepted")
		}
	})
	t.Run("unknown_commits_do_not_block", func(t *testing.T) {
		// An older forge wrote no commit, and a checkout with no git has no
		// HEAD: neither can be compared, and refusing would break both.
		if err := checkCutMatchesBuild("v1", "", []string{built}); err != nil {
			t.Errorf("a checkout with no HEAD was refused: %v", err)
		}
		if err := checkCutMatchesBuild("v1", other, nil); err != nil {
			t.Errorf("a build state with no commit was refused: %v", err)
		}
	})
}

func TestHarvestedBuildCommits(t *testing.T) {
	dir := newLedgerTestProject(t, "cut-commit-project")
	if err := WriteBuildState(dir, "prod", BuildState{Image: "r.test/app", Tag: "t", Commit: "aaaa"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteBuildState(dir, "default", BuildState{Image: "r.test/app", Tag: "t", Commit: "bbbb"}); err != nil {
		t.Fatal(err)
	}
	got := harvestedBuildCommits(dir, "prod")
	if len(got) != 2 || got[0] != "aaaa" || got[1] != "bbbb" {
		t.Errorf("commits = %v, want the env's and the default's, sorted", got)
	}
	if got := harvestedBuildCommits(t.TempDir(), "prod"); len(got) != 0 {
		t.Errorf("a project with no build state yielded %v", got)
	}
}

// The wiring, not just the predicate: a --no-build cut from a checkout whose
// HEAD is not the commit the build state recorded must not record a release.
func TestCutReleaseFromBuildState_RefusesACheckoutThatIsNotTheBuiltCommit(t *testing.T) {
	dir := useTestLedger(t, t.TempDir())
	gitInit(t, dir)
	t.Chdir(dir)
	const built = "fec19d93310ccbf9502f8131b0db5a16d70a406e"
	if err := WriteBuildState(dir, "staging", BuildState{
		Image: "demo", Tag: "v1.0.0", Pushed: true, PushedAt: nowRFC3339(), Digest: sha("a"), Commit: built,
	}); err != nil {
		t.Fatal(err)
	}

	_, err := cutReleaseFromBuildState(context.Background(), dir, "staging", "v1.0.0", "", nil,
		buildOptions{run: runOptions{None: true}})
	if err == nil {
		t.Fatal("a release was cut at a HEAD that is not the built commit")
	}
	if !strings.Contains(err.Error(), "fec19d93") || !strings.Contains(err.Error(), "cannot be amended") {
		t.Errorf("the refusal must name the built commit and why it matters: %v", err)
	}
	releases, rerr := testStore(t, dir).Releases()
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(releases) != 0 {
		t.Errorf("the refused cut still recorded %d release(s); it must write nothing", len(releases))
	}
}
