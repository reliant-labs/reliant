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

// TestReleaseTagSelectsTheReleaseCommit runs scripts/release-tag.sh --dry-run
// against a local origin, with the release gate stubbed green, and checks which
// commit it would tag and which it refuses. The version bump squash-merges to
// main as a different object than the one on the RC branch, so --rc must tag the
// RC head on what IT carries (version, notes, RC shape), while a plain --commit
// must still contain main's bump.
func TestReleaseTagSelectsTheReleaseCommit(t *testing.T) {
	t.Parallel()
	testkit.Slow(t, "builds git repositories and runs release-tag.sh over them")
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("release-tag.sh reads electron/package.json with node, which is not on PATH")
	}

	// The scripts under test, read in-process (cache-sound) and copied beside a
	// stub release-gate.sh that records the commit it was asked about.
	scripts := t.TempDir()
	for _, name := range []string{"release-tag.sh", "rc-shape.sh", "release-lock.sh"} {
		writeFile(t, filepath.Join(scripts, name), readScript(t, name), 0o755)
	}
	gateLog := filepath.Join(t.TempDir(), "gate.log")
	writeFile(t, filepath.Join(scripts, "release-gate.sh"),
		"#!/usr/bin/env bash\necho \"$2\" >> \""+gateLog+"\"\nexit 0\n", 0o755)

	gitEnv := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	origin := filepath.Join(t.TempDir(), "origin.git")
	dir := t.TempDir()
	run := func(d string, name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = d
		cmd.Env = gitEnv
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	git := func(args ...string) string {
		t.Helper()
		out, err := run(dir, "git", args...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return out
	}
	// commit writes electron/package.json at version (and the stable notes when
	// notes is set), then commits.
	commit := func(version string, notes bool, msg ...string) string {
		t.Helper()
		writeFile(t, filepath.Join(dir, "electron", "package.json"), `{"version": "`+version+`"}`+"\n", 0o644)
		if notes {
			writeFile(t, filepath.Join(dir, "docs", "data", "releases", "v"+version+".yaml"), "version: v"+version+"\n", 0o644)
		}
		git("add", "-A")
		args := []string{"commit", "-q", "--allow-empty"}
		for _, m := range msg {
			args = append(args, "-m", m)
		}
		git(args...)
		return git("rev-parse", "HEAD")
	}
	if out, err := run(".", "git", "init", "-q", "--bare", "-b", "main", origin); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	git("init", "-q", "-b", "main")
	git("remote", "add", "origin", origin)

	m1 := commit("1.0.0", false, "m1")
	// RC branches, each cut off m1 with the train's pin commit.
	rcBranch := func(name string, fix func()) string {
		t.Helper()
		git("checkout", "-q", "-b", name, m1)
		commit("1.0.0", false, "chore(rc): pin forge", "RC-Set: v1.1.0-rc.1")
		fix()
		head := git("rev-parse", "HEAD")
		git("push", "-q", "origin", "HEAD:refs/heads/"+name)
		git("checkout", "-q", "main")
		return head
	}
	// The set fixed in place: its own bump and notes, a different object than main's.
	rcGood := rcBranch("release/v1.1.0-rc.1", func() { commit("1.1.0", true, "chore: release v1.1.0") })
	rcWrongVersion := rcBranch("release/v1.1.0-rc.2", func() { commit("1.0.5", true, "chore: release v1.0.5") })
	rcNoNotes := rcBranch("release/v1.1.0-rc.3", func() { commit("1.1.0", false, "chore: release v1.1.0") })
	// A side branch (no RC-Set pin commit first off main) that heads an RC branch name.
	git("checkout", "-q", "-b", "side", m1)
	commit("1.0.0", false, "a PR head")
	rcBadShape := commit("1.1.0", true, "chore: release v1.1.0")
	git("push", "-q", "origin", "HEAD:refs/heads/release/v1.1.0-rc.4")
	git("checkout", "-q", "main")

	// main: the bump squash-merged independently, then a later fix.
	bump := commit("1.1.0", true, "chore: release v1.1.0 (#1)")
	later := commit("1.1.0", true, "fix: after the bump (#2)")
	git("push", "-q", "origin", "main")
	if bump == rcGood {
		t.Fatal("fixture: main's bump and the RC head must be different objects")
	}

	tag := func(args ...string) (string, error) {
		_ = os.Remove(gateLog)
		return run(dir, "bash", append([]string{filepath.Join(scripts, "release-tag.sh"), "--dry-run"}, args...)...)
	}

	for _, tc := range []struct {
		name    string
		args    []string
		want    string // the commit it would tag; empty means it must refuse
		wantErr string // a fragment of the refusal
	}{
		{"--rc tags the RC head, not main's bump", []string{"--rc", "--commit", rcGood}, rcGood, ""},
		{"--rc refuses a head that does not carry main's version", []string{"--rc", "--commit", rcWrongVersion}, "", "does not carry version 1.1.0"},
		{"--rc refuses a head without the stable notes", []string{"--rc", "--commit", rcNoNotes}, "", "docs/data/releases/v1.1.0.yaml is not present"},
		{"--rc refuses a head that is not main + pin + fixes", []string{"--rc", "--commit", rcBadShape}, "", "is not main + the train's pin commit"},
		{"no flags tag main's bump", nil, bump, ""},
		{"--commit tags a later main commit containing the bump", []string{"--commit", later}, later, ""},
		{"--commit refuses a main commit without the bump", []string{"--commit", m1}, "", "does not contain the version bump"},
		{"--commit refuses the RC head without --rc", []string{"--commit", rcGood}, "", "does not contain the version bump"},
	} {
		out, err := tag(tc.args...)
		if tc.want == "" {
			if err == nil {
				t.Errorf("%s: release-tag.sh succeeded, want a refusal\n%s", tc.name, out)
			} else if !strings.Contains(out, tc.wantErr) {
				t.Errorf("%s: refusal does not say %q\n%s", tc.name, tc.wantErr, out)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: release-tag.sh failed: %v\n%s", tc.name, err, out)
			continue
		}
		if !strings.Contains(out, "git tag v1.1.0 "+tc.want) {
			t.Errorf("%s: would not tag %s\n%s", tc.name, tc.want[:9], out)
		}
		gated, _ := os.ReadFile(gateLog)
		if strings.TrimSpace(string(gated)) != tc.want {
			t.Errorf("%s: release gate checked %q, want %s", tc.name, strings.TrimSpace(string(gated)), tc.want)
		}
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
