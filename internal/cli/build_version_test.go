package cli

import (
	"context"
	"regexp"
	"testing"
)

// TestResolveBuildVersion_OverrideWins confirms a non-empty override
// (--tag or forge.yaml build.version) short-circuits all git derivation:
// the resolved version is exactly the override even inside a git repo
// with a describable tag. This is the contract that lets `--tag v1.2.3`
// pin the embedded version to the release number.
func TestResolveBuildVersion_OverrideWins(t *testing.T) {
	dir := newGitRepo(t)
	withDir(t, dir, func() {
		info := resolveBuildVersion(context.Background(), "v9.9.9-release")
		if info.version != "v9.9.9-release" {
			t.Errorf("override should win, got version=%q", info.version)
		}
		// commit still comes from git (rev-parse HEAD) — only version is
		// overridden.
		if info.commit == "none" || info.commit == "" {
			t.Errorf("expected git commit in a repo, got %q", info.commit)
		}
		if info.date == "" {
			t.Error("expected a non-empty date")
		}
	})
}

// TestResolveBuildVersion_GitDescribe confirms that with no override, a
// git repo yields the `git describe --tags --always --dirty` value (a
// commit-ish for an untagged repo) and a real commit hash.
func TestResolveBuildVersion_GitDescribe(t *testing.T) {
	dir := newGitRepo(t)
	withDir(t, dir, func() {
		info := resolveBuildVersion(context.Background(), "")
		if info.version == "" {
			t.Fatal("expected a non-empty version from git describe")
		}
		// An untagged repo's describe is the always-fallback short hash,
		// never the time-based dev sentinel.
		if devVersionRE.MatchString(info.version) {
			t.Errorf("git repo should not fall through to time-based dev version, got %q", info.version)
		}
		if info.commit == "none" || info.commit == "" {
			t.Errorf("expected git commit hash, got %q", info.commit)
		}
	})
}

// devVersionRE matches the time-based dev fallback shape produced when
// there's no git at all: "0.0.0-dev.<unix seconds>".
var devVersionRE = regexp.MustCompile(`^0\.0\.0-dev\.\d+$`)

// TestResolveBuildVersion_DevTimeFallback confirms that with no override
// AND no git repo, the version falls through to the time-based dev
// sentinel "0.0.0-dev.<digits>" and commit collapses to "none". This is
// the path a non-git source checkout (e.g. a tarball) takes.
func TestResolveBuildVersion_DevTimeFallback(t *testing.T) {
	dir := t.TempDir()
	withDir(t, dir, func() {
		info := resolveBuildVersion(context.Background(), "")
		if !devVersionRE.MatchString(info.version) {
			t.Errorf("expected 0.0.0-dev.<digits> dev fallback, got %q", info.version)
		}
		if info.commit != "none" {
			t.Errorf("expected commit=none with no git, got %q", info.commit)
		}
		if info.date == "" {
			t.Error("expected a non-empty date")
		}
	})
}
