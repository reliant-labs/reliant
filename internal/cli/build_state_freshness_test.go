package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// gitHeadSHA reads the HEAD commit of a repo dir. Test helper for the
// freshness tests, which need to compare a recorded build commit against
// the live HEAD.
func gitHeadSHA(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// gitignoreForgeState writes and commits a `.gitignore` that excludes
// `.forge/` in the repo, matching real forge projects. Without it, the
// `.forge/state/build-*.json` file WriteBuildState drops would show up as
// untracked and `git status --porcelain` would report a dirty tree —
// which would (correctly, in production-with-gitignore) never happen.
func gitignoreForgeState(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".forge/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@e.x",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@e.x",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("add", ".gitignore")
	run("commit", "-q", "-m", "gitignore forge state")
}

// gitCommitEmpty makes an additional commit in dir so HEAD advances past
// the previously-recorded build commit, simulating "you committed/pushed
// a fix after the last build."
func gitCommitEmpty(t *testing.T, dir, msg string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@e.x",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@e.x",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("commit", "-q", "--allow-empty", "-m", msg)
}

// TestResolveDeployImageTag_StaleCommitRefuses is the core fr-02d44d2b03
// guard: build state records commit C, HEAD has moved to C', the tree is
// clean — deploy must REFUSE rather than silently ship the old image.
func TestResolveDeployImageTag_StaleCommitRefuses(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := useTestLedger(t, newGitRepo(t))
	gitignoreForgeState(t, dir)
	builtCommit := gitHeadSHA(t, dir)
	// The build was at builtCommit; now advance HEAD past it.
	gitCommitEmpty(t, dir, "fix shipped after last build")

	if err := WriteBuildState(dir, "prod", BuildState{
		Tag:    "v0.1.0",
		Image:  "app",
		Commit: builtCommit,
		GitTag: "v0.1.0", // tagged, so the dirty/untagged warnings don't fire
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err == nil {
		t.Fatal("expected stale-image refusal, got nil error (would silently ship old code)")
	}
	if !strings.Contains(err.Error(), "stale") || !strings.Contains(err.Error(), "v0.1.0") {
		t.Fatalf("error should name the stale tag and explain; got: %v", err)
	}
}

// TestResolveDeployImageTag_FreshCommitAllows: build state commit ==
// HEAD, clean tree — the image is current, deploy proceeds.
//
// It needs a REAL ledger (useTestLedger), because the guard now reports an
// unopenable one as an error. It previously passed on a project with no
// forge.yaml name, where the ledger could not be keyed at all — so the
// "allow" it asserted came from the guard being silently disabled rather
// than from the commit matching. That shape cannot occur in production,
// where a nameless project is refused outright.
func TestResolveDeployImageTag_FreshCommitAllows(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := useTestLedger(t, newGitRepo(t))
	gitignoreForgeState(t, dir)
	head := gitHeadSHA(t, dir)

	if err := WriteBuildState(dir, "prod", BuildState{
		Tag:    "v0.1.0",
		Image:  "app",
		Commit: head,
		GitTag: "v0.1.0",
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	tag, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err != nil {
		t.Fatalf("fresh build should deploy, got error: %v", err)
	}
	if tag != "v0.1.0" {
		t.Fatalf("tag = %q, want v0.1.0", tag)
	}
}

// TestResolveDeployImageTag_StaleButFlagOverrides: the --tag escape hatch
// bypasses build-state entirely, so the staleness guard never runs. This
// is the documented override for "I really do want to ship this tag."
func TestResolveDeployImageTag_StaleButFlagOverrides(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	builtCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "fix after build")
	if err := WriteBuildState(dir, "prod", BuildState{Tag: "v0.1.0", Commit: builtCommit}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	tag, _, src, err := resolveDeployImageTag(context.Background(), dir, "prod", "v0.1.0", false)
	if err != nil {
		t.Fatalf("--tag override should bypass the staleness guard, got: %v", err)
	}
	if tag != "v0.1.0" || !strings.Contains(src, "--tag") {
		t.Fatalf("override path: tag=%q src=%q", tag, src)
	}
}

// TestResolveDeployImageTag_DirtyTrackedFileSkipsFreshnessCheck: a tree
// with uncommitted edits to a TRACKED file has no single HEAD the build
// can be "behind," so the staleness guard is skipped (the dirty-build
// warning covers reproducibility). Even though the recorded commit
// differs from HEAD, deploy proceeds.
// It needs a real ledger for the reason TestResolveDeployImageTag_FreshCommitAllows
// does: the stand-down under test is the DIRTY-TREE one, and on a nameless
// project the guard would stand down for an unrelated reason it must now
// report as an error instead.
func TestResolveDeployImageTag_DirtyTrackedFileSkipsFreshnessCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := useTestLedger(t, newGitRepo(t))
	gitignoreForgeState(t, dir)
	builtCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "advance head")
	// Dirty a TRACKED file (README.md is committed by newGitRepo).
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteBuildState(dir, "prod", BuildState{Tag: "v0.1.0", Commit: builtCommit, Dirty: true}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	tag, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err != nil {
		t.Fatalf("dirty tracked file should skip the staleness guard, got: %v", err)
	}
	if tag != "v0.1.0" {
		t.Fatalf("tag = %q, want v0.1.0", tag)
	}
}

// TestResolveDeployImageTag_UntrackedFileDoesNotMaskStaleness: an
// untracked file (editor dir, artifact) must NOT disable the guard — it
// doesn't move HEAD. A stale build with only untracked clutter present
// still refuses.
func TestResolveDeployImageTag_UntrackedFileDoesNotMaskStaleness(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := useTestLedger(t, newGitRepo(t))
	gitignoreForgeState(t, dir)
	builtCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "advance head")
	// Add an UNTRACKED file — should be ignored by the clean check.
	if err := os.WriteFile(filepath.Join(dir, "scratch.tmp"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteBuildState(dir, "prod", BuildState{Tag: "v0.1.0", Commit: builtCommit, GitTag: "v0.1.0"}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	_, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err == nil {
		t.Fatal("untracked clutter must not mask the staleness guard; expected refusal")
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected stale refusal, got: %v", err)
	}
}

// TestResolveDeployImageTag_NoCommitSkipsFreshnessCheck: older build-state
// files predating commit-stamping have an empty Commit. Those must not be
// blocked — the guard only fires when it can prove staleness.
func TestResolveDeployImageTag_NoCommitSkipsFreshnessCheck(t *testing.T) {
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	gitCommitEmpty(t, dir, "advance head")
	if err := WriteBuildState(dir, "prod", BuildState{Tag: "legacy"}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	tag, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err != nil {
		t.Fatalf("commit-less state should not be blocked, got: %v", err)
	}
	if tag != "legacy" {
		t.Fatalf("tag = %q, want legacy", tag)
	}
}

// bindEnvToRelease writes both halves of a release-bound env: the release
// ledger (which records the commit the images were built from) and the
// env→release binding. This is the state a real `forge env build --release` +
// `forge env deploy` pair leaves behind.
func bindEnvToRelease(t *testing.T, dir, envName, version, builtCommit string) {
	t.Helper()
	cutReleaseAt(t, dir, version, builtCommit)
	if _, err := testBindings(t, dir).Append(context.Background(), release.Promotion{
		Env: envName, Release: version, Kind: release.KindPromote,
		Resolved: map[string]string{"app": sha("a")},
	}, appendGuard{}); err != nil {
		t.Fatalf("promote: %v", err)
	}
}

// cutReleaseAt records a release whose images were built from builtCommit,
// WITHOUT promoting any env to it — the state `forge env deploy <env>
// <version>` sees while it plans and preflights, before the promotion is
// written.
func cutReleaseAt(t *testing.T, dir, version, builtCommit string) {
	t.Helper()
	if err := testCutRelease(t, dir, release.Release{
		Version: version,
		Git:     release.Git{Commit: builtCommit, Tag: version},
		Artifacts: map[string]release.Artifact{
			"app": {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: sha("a")}},
		},
	}); err != nil {
		t.Fatalf("write release: %v", err)
	}
}

// TestResolveDeployImageTag_ReleaseCommitAllowsHEADAhead is the incident
// this fix exists for. Cutting a release ledger adds a commit ON TOP of the
// commit the images were built from, so HEAD is legitimately one ahead of a
// perfectly current release — and comparing against HEAD refused a deploy
// that was exactly right, forcing --tag every single time.
//
// The images match the release's recorded commit, so this MUST be allowed.
func TestResolveDeployImageTag_ReleaseCommitAllowsHEADAhead(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)

	// The images were built here...
	builtCommit := gitHeadSHA(t, dir)
	// ...and then cutting the release ledger advanced HEAD past them.
	gitCommitEmpty(t, dir, "chore: release v1.4.0")
	headAfterLedger := gitHeadSHA(t, dir)
	if headAfterLedger == builtCommit {
		t.Fatal("precondition: HEAD should have advanced past the build commit")
	}

	bindEnvToRelease(t, dir, "prod", "v1.4.0", builtCommit)
	if err := WriteBuildState(dir, "prod", BuildState{
		Tag: "ship-20260909-192914", Image: "app", Commit: builtCommit, GitTag: "v1.4.0",
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	tag, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err != nil {
		t.Fatalf("image built from the bound release's commit must deploy without --tag, got: %v", err)
	}
	if tag != "ship-20260909-192914" {
		t.Fatalf("tag = %q, want ship-20260909-192914", tag)
	}
}

// TestResolveDeployImageTag_ReleaseCommitStillRefusesOlderImage: anchoring
// to the release must not defang the guard. An image built BEFORE the
// release it claims to be is genuinely stale and must still refuse — even
// though (unlike the case above) nothing about HEAD is involved.
func TestResolveDeployImageTag_ReleaseCommitStillRefusesOlderImage(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)

	olderCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "work that went into the release")
	releaseCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "chore: release v1.4.0")

	bindEnvToRelease(t, dir, "prod", "v1.4.0", releaseCommit)
	// The build state records an image from BEFORE the release's commit.
	if err := WriteBuildState(dir, "prod", BuildState{
		Tag: "stale-build", Image: "app", Commit: olderCommit, GitTag: "v1.3.0",
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err == nil {
		t.Fatal("an image older than the release it claims to be is stale; expected refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "stale") {
		t.Fatalf("expected a stale refusal, got: %v", err)
	}
	// The message must name WHAT it compared against, so the next operator
	// is not guessing between HEAD and the release commit.
	if !strings.Contains(msg, "v1.4.0") || !strings.Contains(msg, shortSHA(releaseCommit)) {
		t.Fatalf("refusal must name the release and its commit; got: %v", msg)
	}
	if !strings.Contains(msg, shortSHA(olderCommit)) {
		t.Fatalf("refusal must name the image's build commit; got: %v", msg)
	}
}

// TestResolveDeployImageTag_ReleaseAnchorIgnoresDirtyTree: a release-anchored
// comparison is ledger-vs-ledger and needs no git state at all, so it stays
// correct in a dirty tree — where the HEAD-anchored path deliberately
// stands down. A stale image is caught even mid-edit.
func TestResolveDeployImageTag_ReleaseAnchorIgnoresDirtyTree(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	olderCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "release work")
	releaseCommit := gitHeadSHA(t, dir)

	bindEnvToRelease(t, dir, "prod", "v1.4.0", releaseCommit)
	if err := WriteBuildState(dir, "prod", BuildState{Tag: "stale-build", Commit: olderCommit}); err != nil {
		t.Fatalf("write state: %v", err)
	}
	// Dirty a TRACKED file — irrelevant to a release-anchored comparison.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("mid-edit"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err == nil {
		t.Fatal("release-anchored staleness does not depend on the working tree; expected refusal")
	}
	if !strings.Contains(err.Error(), "v1.4.0") {
		t.Fatalf("expected a release-anchored refusal, got: %v", err)
	}
}

// TestResolveDeployImageTag_ReleaseWithoutCommitSkipsCheck: a release ledger
// cut on a non-git tree records no commit. There is then no anchor the
// build can be measured against — and falling back to HEAD is exactly the
// false refusal this fix removes — so the guard stands down.
func TestResolveDeployImageTag_ReleaseWithoutCommitSkipsCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	builtCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "chore: release v1.4.0")

	// Release ledger with NO recorded git commit.
	bindEnvToRelease(t, dir, "prod", "v1.4.0", "")
	if err := WriteBuildState(dir, "prod", BuildState{Tag: "ship-1", Commit: builtCommit, GitTag: "v1.4.0"}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	tag, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err != nil {
		t.Fatalf("commit-less release must not fall back to a HEAD comparison, got: %v", err)
	}
	if tag != "ship-1" {
		t.Fatalf("tag = %q, want ship-1", tag)
	}
}

// TestResolveDeployImageTag_DirtyDefaultRecordDoesNotBlockReleaseDeploy is
// the v1.5.18 regression. A release-bound env must not be refused because of
// a DIRTY `default` record — the env-agnostic file a plain local
// `forge build` drops, which every env reads as its fallback.
//
// Measured, and the reason this exists: `forge env deploy prod` was refused
// three times during the v1.5.18 control-plane release by an Aug-25
// `.forge/state/build-default.json` recording a dirty build pushed to a
// localhost:5051 dev registry. All four images deployed from release-ledger
// digests in prod GAR that the record had no part in — its digest appeared
// nowhere in the release. The only ways forward were hand-deleting local
// state files or passing --tag, which disarms the guard wholesale.
//
// A release is cut from a clean checkout, so a dirty build is by construction
// not the artifact a release-bound env ships.
func TestResolveDeployImageTag_DirtyDefaultRecordDoesNotBlockReleaseDeploy(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	builtCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "chore: release v1.4.0")

	bindEnvToRelease(t, dir, "prod", "v1.4.0", gitHeadSHA(t, dir))

	// The env-agnostic `default` record from someone's local dirty build.
	if err := WriteBuildState(dir, "default", BuildState{
		Tag:    "v0.1.0-50-gabcdef12-dirty",
		Image:  "app",
		Commit: builtCommit,
		Dirty:  true,
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err != nil {
		t.Fatalf("a dirty local `default` build cannot be what a release-bound env ships; "+
			"the guard must stand down, got: %v", err)
	}
}

// TestResolveDeployImageTag_CleanDefaultRecordStillRefuses is the other half,
// and the reason the exemption requires BOTH conditions rather than just
// "the record is `default`". A clean `default` build could genuinely be the
// image that ships — `forge build --docker && forge env deploy prod` is a
// supported flow — so a stale one is exactly the footgun the guard exists
// for and must still refuse.
func TestResolveDeployImageTag_CleanDefaultRecordStillRefuses(t *testing.T) {
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	builtCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "chore: release v1.4.0")

	bindEnvToRelease(t, dir, "prod", "v1.4.0", gitHeadSHA(t, dir))

	if err := WriteBuildState(dir, "default", BuildState{
		Tag:    "v0.1.0",
		Image:  "app",
		Commit: builtCommit,
		GitTag: "v0.1.0",
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err == nil {
		t.Fatal("a clean `default` build can genuinely ship; a stale one must still refuse")
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected a staleness refusal, got: %v", err)
	}
}

// TestResolveDeployImageTag_DirtyPerEnvRecordStillRefuses pins the other
// half of the `default` scoping: a dirty build-<env>.json NAMES this env, so
// its commit is evidence about this env and the guard still applies. Only
// the env-agnostic `default` record is ambiguous about which env it describes.
func TestResolveDeployImageTag_DirtyPerEnvRecordStillRefuses(t *testing.T) {
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	builtCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "chore: release v1.4.0")

	bindEnvToRelease(t, dir, "prod", "v1.4.0", gitHeadSHA(t, dir))

	if err := WriteBuildState(dir, "prod", BuildState{
		Tag:    "prod-dirty-build",
		Image:  "app",
		Commit: builtCommit,
		Dirty:  true,
	}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err == nil {
		t.Fatal("a record naming THIS env is evidence about this env; expected refusal")
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Fatalf("expected a staleness refusal, got: %v", err)
	}
}

// TestResolveDeployImageTag_UnboundEnvStillUsesHEAD: an env with no release
// binding keeps the original HEAD-anchored behaviour, and its message names
// HEAD as what it compared against.
func TestResolveDeployImageTag_UnboundEnvStillUsesHEAD(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories and worktrees; runs in task test")
	}
	dir := newGitRepo(t)
	gitignoreForgeState(t, dir)
	builtCommit := gitHeadSHA(t, dir)
	gitCommitEmpty(t, dir, "fix shipped after last build")
	head := gitHeadSHA(t, dir)

	// A release exists and is bound to a DIFFERENT env — prod is unbound.
	bindEnvToRelease(t, dir, "staging", "v1.4.0", builtCommit)
	if err := WriteBuildState(dir, "prod", BuildState{Tag: "v0.1.0", Commit: builtCommit, GitTag: "v0.1.0"}); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, _, _, err := resolveDeployImageTag(context.Background(), dir, "prod", "", false)
	if err == nil {
		t.Fatal("unbound env must still be measured against HEAD; expected refusal")
	}
	if !strings.Contains(err.Error(), "HEAD is "+shortSHA(head)) {
		t.Fatalf("unbound refusal must name HEAD as the anchor; got: %v", err)
	}
}
