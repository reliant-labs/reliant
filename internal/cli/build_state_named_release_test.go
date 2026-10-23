package cli

// A DEPLOY THAT NAMES A RELEASE IS MEASURED AGAINST THAT RELEASE.
//
// `forge env deploy <env> <version>` preflights the release before it records
// the promotion (#583), so while the preflight runs, the env's binding still
// names the release the env runs NOW. The stale-image guard anchored on that
// binding, which measured every new release's build against the OLD release's
// commit and refused every forward deploy. Prod, 2026-10-07:
//
//	refusing to deploy stale image: tag "20261007.183432-7033e9626382" was built
//	from 7033e9626382, but release "20261007.165315-c6881049d093" was cut from
//	7cef449f9e7a.
//
// Before #583 the binding had already moved by the time the check ran, so the
// anchor was the release being deployed. The release a deploy names is the
// release it ships, and its recorded commit is the anchor. A deploy that names
// no release still re-applies the bound one, and is still measured against it.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// deployingNamed is the context `forge env deploy prod v1.5.0` runs its
// pre-record preflight under (see preflightBeforeRecord): it carries the
// release the deploy names.
func deployingNamed() context.Context {
	return withHostedPinRelease(context.Background(), "v1.5.0")
}

// namedReleaseFixture is prod at the moment its next release is deployed:
// bound to v1.4.0, with v1.5.0 cut on top and not yet promoted. Each release's
// ledger commit lands one commit after the images it records, as a real cut
// does, so HEAD matches neither release.
func namedReleaseFixture(t *testing.T) (dir, boundCommit, namedCommit string) {
	t.Helper()
	dir = newGitRepo(t)
	gitignoreForgeState(t, dir)
	boundCommit = gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "chore: release v1.4.0")
	bindEnvToRelease(t, dir, "prod", "v1.4.0", boundCommit)
	gitCommitEmpty(t, dir, "the work v1.5.0 ships")
	namedCommit = gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "chore: release v1.5.0")
	cutReleaseAt(t, dir, "v1.5.0", namedCommit)
	return dir, boundCommit, namedCommit
}

// TestResolveDeployImageTag_NamedReleaseIsTheAnchorNotTheBoundOne is the
// regression. prod is bound to v1.4.0 and the deploy names v1.5.0, whose
// build the state records: the build is exactly the release being deployed,
// so the guard must pass it.
//
// The second half pins that the anchor follows the deploy, not the ledger
// alone: the same state with no release named is `forge env deploy prod`,
// which re-applies v1.4.0, and a build of v1.5.0 is not that.
func TestResolveDeployImageTag_NamedReleaseIsTheAnchorNotTheBoundOne(t *testing.T) {
	dir, boundCommit, namedCommit := namedReleaseFixture(t)
	if err := WriteBuildState(dir, "prod", BuildState{
		Tag: "v1.5.0", Image: "app", Commit: namedCommit, GitTag: "v1.5.0",
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	tag, _, _, err := resolveDeployImageTag(deployingNamed(), dir, "prod", "", false)
	if err != nil {
		t.Fatalf("a build of the release this deploy names must pass while the binding still names "+
			"the release it replaces; got: %v", err)
	}
	if tag != "v1.5.0" {
		t.Fatalf("tag = %q, want v1.5.0", tag)
	}

	_, _, _, err = resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err == nil {
		t.Fatal("a deploy naming no release re-applies the bound v1.4.0; a build of v1.5.0 must be refused")
	}
	want := fmt.Sprintf(`release "v1.4.0" was cut from %s`, shortSHA(boundCommit))
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("an unnamed deploy must still anchor on the bound release (%q); got: %v", want, err)
	}
}

// TestResolveDeployImageTag_NamedReleaseRefusesABuildOfNeither: anchoring on
// the named release must not defang the guard. A build from a commit that is
// neither release is stale for the deploy, and the refusal names the release
// it was measured against — the one being deployed.
func TestResolveDeployImageTag_NamedReleaseRefusesABuildOfNeither(t *testing.T) {
	dir, _, namedCommit := namedReleaseFixture(t)
	gitCommitEmpty(t, dir, "work in neither release")
	strayCommit := gitHeadSHA(t, dir)
	if err := WriteBuildState(dir, "prod", BuildState{
		Tag: "stray-build", Image: "app", Commit: strayCommit, GitTag: "v1.5.0",
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, _, _, err := resolveDeployImageTag(deployingNamed(), dir, "prod", "", false)
	if err == nil {
		t.Fatal("a build of neither release is stale; expected refusal")
	}
	want := fmt.Sprintf(`was built from %s, but release "v1.5.0" was cut from %s`, shortSHA(strayCommit), shortSHA(namedCommit))
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("the refusal must measure against the NAMED release (%q); got: %v", want, err)
	}
}

// TestResolveDeployImageTag_NamedReleaseRefusesTheBoundReleasesBuild: a
// build of the release prod already runs is not the release being deployed.
// Anchored on the binding, the old guard PASSED this one — it would have
// shipped v1.4.0's image under v1.5.0's name.
func TestResolveDeployImageTag_NamedReleaseRefusesTheBoundReleasesBuild(t *testing.T) {
	dir, boundCommit, namedCommit := namedReleaseFixture(t)
	if err := WriteBuildState(dir, "prod", BuildState{
		Tag: "v1.4.0", Image: "app", Commit: boundCommit, GitTag: "v1.4.0",
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, _, _, err := resolveDeployImageTag(deployingNamed(), dir, "prod", "", false)
	if err == nil {
		t.Fatal("a build of the bound v1.4.0 is not the v1.5.0 being deployed; expected refusal")
	}
	want := fmt.Sprintf(`but release "v1.5.0" was cut from %s`, shortSHA(namedCommit))
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("the refusal must measure against the NAMED release (%q); got: %v", want, err)
	}
}

// TestResolveDeployImageTag_NamedReleaseAnchorsAFirstDeploy: an env deployed
// a release for the first time has no binding at all while it preflights, so
// the old guard fell back to HEAD — which the release's own ledger commit
// puts one ahead of its images. That is the false refusal the release anchor
// exists to remove, so the named release anchors here too.
func TestResolveDeployImageTag_NamedReleaseAnchorsAFirstDeploy(t *testing.T) {
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	builtCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "chore: release v1.5.0")
	cutReleaseAt(t, dir, "v1.5.0", builtCommit)
	if err := WriteBuildState(dir, "prod", BuildState{
		Tag: "v1.5.0", Image: "app", Commit: builtCommit, GitTag: "v1.5.0",
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if _, _, _, err := resolveDeployImageTag(deployingNamed(), dir, "prod", "", false); err != nil {
		t.Fatalf("an env's first release deploy is measured against the release, not HEAD; got: %v", err)
	}
}

// TestResolveDeployImageTag_DirtyDefaultRecordDoesNotBlockAFirstReleaseDeploy
// is the same mistake in buildStateIsForeignToEnv. A dirty `default` record
// cannot be what a release deploy ships (see
// TestResolveDeployImageTag_DirtyDefaultRecordDoesNotBlockReleaseDeploy), and
// the exemption keyed that on the env being BOUND — which an env's first
// release deploy is not, until after the preflight has refused it.
func TestResolveDeployImageTag_DirtyDefaultRecordDoesNotBlockAFirstReleaseDeploy(t *testing.T) {
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	localCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "the work v1.5.0 ships")
	cutReleaseAt(t, dir, "v1.5.0", gitHeadSHA(t, dir))
	gitCommitEmpty(t, dir, "chore: release v1.5.0")

	if err := WriteBuildState(dir, "default", BuildState{
		Tag: "v0.1.0-50-gabcdef12-dirty", Image: "app", Commit: localCommit, Dirty: true,
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	if _, _, _, err := resolveDeployImageTag(deployingNamed(), dir, "prod", "", false); err != nil {
		t.Fatalf("a dirty local `default` build cannot be what a release deploy ships; got: %v", err)
	}
}

// TestResolveFreshnessAnchor_NamedReleaseNotInLedgerStandsDown: a deploy
// naming a release this ledger does not hold has no anchor. Neither the
// binding (the release being REPLACED — the regression above) nor HEAD (the
// false refusal) is a fallback, so the guard stands down without error and
// the deploy's digest resolution refuses the unknown release in its own words.
func TestResolveFreshnessAnchor_NamedReleaseNotInLedgerStandsDown(t *testing.T) {
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	bindEnvToRelease(t, dir, "prod", "v1.4.0", gitHeadSHA(t, dir))

	_, enforce, err := resolveFreshnessAnchor(deployingNamed(), dir, "prod")
	if err != nil {
		t.Fatalf("a named release the ledger does not hold is a stand-down, not a failure; got: %v", err)
	}
	if enforce {
		t.Fatal("the bound v1.4.0 is not an anchor for a deploy of v1.5.0; the guard must stand down")
	}
}
