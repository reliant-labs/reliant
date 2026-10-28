// Copyright (c) 2025 Reliant Labs
package version_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This file guards the property that makes THIS package's version number mean
// anything: that a release tag is reachable from main.
//
// Every tag from v1.7.8 through v1.7.12 was not. scripts/release.sh cut a
// `release-*` branch, bumped the version there, tagged that commit and opened
// a PR; main squash-merges, so the merged commit was a different object and
// the tag stayed behind on the branch. `git describe --tags --abbrev=0
// origin/main` answered v1.7.7 for five consecutive releases and nothing
// noticed, because from the release's point of view everything succeeded.
//
// The consequence that forced the fix: `go get` derives a pseudo-version from
// the highest tag REACHABLE from the pinned commit, so pinning reliant's main
// produced v1.7.8-0.<date>-<sha> — sorting BELOW the released v1.7.12. Pinning
// main was a content upgrade that was a version-number downgrade, which Go's
// minimum-version selection can silently undo.
//
// The tests below are cheap and hermetic: they read the release scripts as
// text and assert on their structure. They deliberately do NOT shell out to
// git for ancestry — that lives in scripts/check-release-tags.sh, which CI
// runs on the tag push where the tag actually exists. A unit test cannot see a
// tag that has not been cut yet, and a test that depends on remote refs is a
// test that fails on an airplane.

// repoRootFromTest locates the repository root two levels above this
// package's source directory, which is the working directory `go test` runs
// the package in whatever directory it was invoked from.
//
// Not runtime.Caller: under -trimpath (the Reliant workspace image's GOFLAGS
// default) it reports "github.com/reliant-labs/reliant/internal/version/...",
// a module-relative path that is not a file on disk.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve the repo root: %v", err)
	}
	return root
}

func readScript(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(repoRootFromTest(t), "scripts", name)
	// Read in-process, never via a child like `cat`: Go's test cache keys only
	// on files the test process itself opens, so a script read by a child is
	// invisible to it and a cached `ok` would outlive an edit to the script.
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(out)
}

// TestReleaseScriptDoesNotTag is the direct regression test for the defect.
//
// scripts/release.sh runs BEFORE the release PR merges, so any commit it can
// reach is a commit that main will squash into something else. It must
// therefore never create a tag — that is the whole bug, in one line of shell.
//
// The assertion is on the absence of `git tag`, not on the presence of some
// comment, because the comment is not what breaks the release.
func TestReleaseScriptDoesNotTag(t *testing.T) {
	t.Parallel()

	script := readScript(t, "release.sh")

	for i, line := range strings.Split(script, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || trimmed == "" {
			continue
		}
		// `git tag` with any argument creates or moves a tag. A bare
		// `git tag` (listing) and `git tag --list` are reads, but neither
		// belongs in this script, so the simplest correct rule is: no
		// `git tag` at all.
		if strings.Contains(trimmed, "git tag") {
			t.Errorf("scripts/release.sh:%d creates a tag: %q\n\n"+
				"release.sh runs before the release PR merges. main squash-merges, so the\n"+
				"commit this script can reach is NOT the commit that lands — tagging here is\n"+
				"exactly what stranded v1.7.8..v1.7.12 off the trunk.\n"+
				"Tagging belongs in scripts/release-tag.sh, which runs after the merge.",
				i+1, trimmed)
		}
	}

	// And it must push no tags either — `git push origin vX.Y.Z` would
	// publish a tag created some other way.
	if strings.Contains(script, `git push origin "v$NEW_VERSION"`) {
		t.Error("scripts/release.sh pushes a version tag; tag publishing belongs in release-tag.sh")
	}
}

// TestReleaseTagScriptChecksAncestry pins the guard inside phase 2.
//
// release-tag.sh is the one place that decides which commit gets the tag. If
// its ancestry check is ever removed or weakened, the original defect returns
// silently and the next person to notice is whoever runs `git describe` months
// later.
func TestReleaseTagScriptChecksAncestry(t *testing.T) {
	t.Parallel()

	script := readScript(t, "release-tag.sh")

	if !strings.Contains(script, "git merge-base --is-ancestor") {
		t.Error("scripts/release-tag.sh no longer verifies the tagged commit is an ancestor of main.\n" +
			"That check is the entire point of the script — without it, phase 2 can tag a commit\n" +
			"that is not on the trunk, which is the defect that stranded v1.7.8..v1.7.12.")
	}

	// It must reason about origin/main, not a local main. A local main can be
	// stale or hold another agent's work; the tag has to describe what is
	// actually published.
	if !strings.Contains(script, "origin/main") {
		t.Error("scripts/release-tag.sh must check ancestry against origin/main, not a local branch")
	}

	// It must refuse to overwrite an existing tag. Published tags are
	// referenced by Electron artifacts and the Homebrew cask; moving one is
	// the same class of error as re-cutting a burned Go module version.
	if !strings.Contains(script, "git ls-remote --exit-code --tags origin") {
		t.Error("scripts/release-tag.sh must check the REMOTE for an existing tag before creating one")
	}
	if strings.Contains(script, "git tag -f") || strings.Contains(script, "push --force") {
		t.Error("scripts/release-tag.sh must never force-create or force-push a tag")
	}
}

// TestBaselineTagIsPinned stops the historical baseline from quietly becoming
// a suppression mechanism.
//
// check-release-tags.sh forgives tags at or below BASELINE_TAG because they
// are off main AND published — the Electron artifacts on R2 and the Homebrew
// cask reference them by name, so moving one would break real installs for no
// gain. (The audit that motivated this found twenty such tags, back to
// v1.2.3-rc1; the flow has been stranding them intermittently for months.)
//
// That baseline is a record of when the two-phase flow landed, not a dial. The
// cheapest way to make a future off-main tag "pass" is to nudge BASELINE_TAG
// past it — a one-word change that looks like routine maintenance in a diff.
// This test converts that into a deliberate edit to an assertion with a reason
// attached, which is the difference between a decision and an accident.
func TestBaselineTagIsPinned(t *testing.T) {
	t.Parallel()

	script := readScript(t, "check-release-tags.sh")

	const wantBaseline = `BASELINE_TAG="v1.7.12"`
	if !strings.Contains(script, wantBaseline) {
		t.Errorf("check-release-tags.sh's BASELINE_TAG changed.\n\n"+
			"expected exactly: %s\n\n"+
			"The baseline marks the last release cut by the old single-phase flow. Tags after\n"+
			"it MUST be ancestors of main. Raising it forgives a real off-main release instead\n"+
			"of fixing it — if a new tag landed off main, cut the next version through the\n"+
			"two-phase flow (release.sh, merge, release-tag.sh) rather than moving this.",
			wantBaseline)
	}

	// The floor must be applied by version comparison, not by string equality
	// against a list — that is what keeps it readable as the set grows.
	if !strings.Contains(script, "version_le") {
		t.Error("check-release-tags.sh must compare tags against BASELINE_TAG by version order")
	}
}

// Only copied scripts and fake git/gate executables run, all inside t.TempDir.
// No real tag, push, lock, gate run or remote is accessible. readScript opens
// inputs in-process so Go's -short cache observes script edits.
func TestReleaseTagExactCommit(t *testing.T) {
	t.Parallel()
	const candidate = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const source = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const main = "cccccccccccccccccccccccccccccccccccccccc"
	steps := []string{"npm", "generate", "docs", "lint", "build", "webbuild", "typecheck", "vet", "test", "integration", "e2e"}
	record := func(sha, result string, names []string) string {
		rows := []map[string]string{}
		for _, n := range names {
			rows = append(rows, map[string]string{"name": n, "result": "PASS"})
		}
		b, err := json.Marshal(map[string]any{"sha": sha, "result": result, "steps": rows})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	green := record(candidate, "PASS", steps)
	cases := []struct {
		name, mode, proof, want string
		args                    []string
		ok                      bool
	}{
		{name: "exact green", want: "Explicit candidate: " + candidate, ok: true},
		{name: "short SHA", args: []string{"--commit", "bbbb"}, want: candidate, ok: true},
		{name: "uppercase SHA", args: []string{"--commit", strings.ToUpper(candidate)}, want: candidate, ok: true},
		{name: "default unchanged", mode: "default", args: []string{}, want: "git tag v1.8.5 " + source, ok: true},
		{name: "red exact", mode: "red", want: "no green release-gate record"},
		{name: "missing despite green carry", proof: "null", want: "exact eleven-step PASS"},
		{name: "borrowed ancestor", proof: record(source, "PASS", steps), want: "exact eleven-step PASS"},
		{name: "red record despite check", proof: record(candidate, "FAIL", steps), want: "exact eleven-step PASS"},
		{name: "red step in PASS record", proof: strings.Replace(green, `"name":"npm","result":"PASS"`, `"name":"npm","result":"FAIL"`, 1), want: "exact eleven-step PASS"},
		{name: "incomplete", proof: record(candidate, "PASS", steps[:10]), want: "exact eleven-step PASS"},
		{name: "duplicate steps", proof: record(candidate, "PASS", append(append([]string{}, steps[:10]...), "npm")), want: "exact eleven-step PASS"},
		{name: "multiple records", proof: green + "\n" + green, want: "exact eleven-step PASS"},
		{name: "malformed record", proof: "{broken", want: "exact eleven-step PASS"},
		{name: "outside main", mode: "outside", want: "not an ancestor"},
		{name: "no version source ancestry", mode: "no-source", want: "must include the version-introducing commit"},
		{name: "wrong package", mode: "package", want: "package and lock versions"},
		{name: "wrong lock top", mode: "lock-top", want: "package and lock versions"},
		{name: "wrong lock root", mode: "lock-root", want: "package and lock versions"},
		{name: "missing notes", mode: "notes-missing", want: "canonical release notes"},
		{name: "wrong notes", mode: "notes-wrong", want: "canonical release notes"},
		{name: "local tag", mode: "local-tag", want: "already exists locally"},
		{name: "remote tag", mode: "remote-tag", want: "already exists on origin"},
		{name: "tag query failed", mode: "tag-query-error", want: "could not establish"},
		{name: "unknown SHA", mode: "unknown", want: "unambiguous commit"},
		{name: "ambiguous SHA", mode: "ambiguous", want: "unambiguous commit"},
		{name: "not commit", mode: "blob", want: "unambiguous commit"},
		{name: "main moved same version", mode: "moved", want: "origin/main moved"},
		{name: "main lookup failed", mode: "remote-error"},
		{name: "wrong requested version", args: []string{"v1.8.4", "--commit", candidate}, want: "origin/main carries version"},
		{name: "missing SHA", args: []string{"--commit"}, want: "requires one unambiguous"},
		{name: "duplicate commit", args: []string{"--commit", candidate, "--commit", candidate}, want: "specified once"},
		{name: "revision expression", args: []string{"--commit", "HEAD^"}, want: "requires one unambiguous"},
		{name: "option as SHA", args: []string{"--commit", "--dry-run"}, want: "requires one unambiguous"},
		{name: "overlong SHA", args: []string{"--commit", candidate + "b"}, want: "requires one unambiguous"},
		{name: "waiver", args: []string{"--skip-release-gate", "--commit", candidate}, want: "cannot be combined"},
		{name: "unknown option", args: []string{"--commit", candidate, "--unknown"}, want: "unrecognized argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			write := func(name, body string) {
				t.Helper()
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write("scripts/release-tag.sh", readScript(t, "release-tag.sh"))
			write("scripts/lib/release-lock.sh", "release_lock_status() { return 0; }\nrelease_lock() { return 0; }\n")
			write("scripts/release-gate.sh", `#!/bin/bash
set -eu
printf 'gate %s\n' "$*" >> "$CALLS"
[[ "$2" == "$CANDIDATE" || "$MODE" == default ]] || exit 90
case "$1" in
 check) [[ "$MODE" != red ]] ;;
 show) cat "$FIXTURE/record.json" ;;
 *) exit 91 ;;
esac
`)
			write("bin/git", `#!/bin/bash
set -eu
printf 'git %s\n' "$*" >> "$CALLS"
case "$*" in
 'rev-parse --git-dir') echo .git ;;
 'fetch origin main --tags --quiet') [[ "$MODE" == default ]] ;;
 'fetch origin +refs/heads/main:refs/remotes/origin/main --tags --quiet') ;;
 'rev-parse --verify --quiet origin/main'|'rev-parse --verify refs/remotes/origin/main^{commit}') echo "$MAIN" ;;
 'rev-parse --verify --quiet refs/tags/v1.8.5') [[ "$MODE" == local-tag ]] ;;
 'rev-parse refs/tags/v1.8.5^{commit}') echo "$SOURCE" ;;
 'ls-remote --exit-code --tags origin refs/tags/v1.8.5')
   [[ "$MODE" != tag-query-error ]] || exit 128
   if [[ "$MODE" == remote-tag ]]; then exit 0; else exit 2; fi ;;
 'rev-list --reverse '*) echo "$SOURCE" ;;
 'rev-parse --disambiguate='*)
   case "$MODE" in
    unknown) ;;
    ambiguous) printf '%s\n%s\n' "$CANDIDATE" "$SOURCE" ;;
    *) echo "$CANDIDATE" ;;
   esac ;;
 "cat-file -t $CANDIDATE") if [[ "$MODE" == blob ]]; then echo blob; else echo commit; fi ;;
 "merge-base --is-ancestor $SOURCE $CANDIDATE") [[ "$MODE" != no-source ]] ;;
 "merge-base --is-ancestor $CANDIDATE $MAIN") [[ "$MODE" != outside ]] ;;
 "merge-base --is-ancestor $SOURCE origin/main") [[ "$MODE" == default ]] ;;
 "show $MAIN:electron/package.json"|'show origin/main:electron/package.json'|"show $SOURCE:electron/package.json") echo '{"version":"1.8.5"}' ;;
 "show $CANDIDATE:electron/package.json")
   if [[ "$MODE" == package ]]; then echo '{"version":"1.8.4"}'; else echo '{"version":"1.8.5"}'; fi ;;
 "show $CANDIDATE:electron/package-lock.json")
   top=1.8.5; root=1.8.5
   [[ "$MODE" != lock-top ]] || top=1.8.4
   [[ "$MODE" != lock-root ]] || root=1.8.4
   printf '{"version":"%s","packages":{"":{"version":"%s"}}}' "$top" "$root" ;;
 "show $CANDIDATE:docs/data/releases/v1.8.5.yaml")
   case "$MODE" in
    notes-missing) exit 1 ;;
    notes-wrong) echo 'version: "v1.8.4"' ;;
    *) echo 'version: "v1.8.5"' ;;
   esac ;;
 'cat-file -e '*':docs/data/releases/v1.8.5.yaml') ;;
 'log -1 --format=%s '*) echo reviewed-candidate ;;
 "rev-parse $CANDIDATE^{tree}") echo dddddddddddddddddddddddddddddddddddddddd ;;
 'ls-remote --exit-code origin refs/heads/main')
   [[ "$MODE" != remote-error ]] || exit 1
   if [[ "$MODE" == moved ]]; then printf '%s\trefs/heads/main\n' "$SOURCE"; else printf '%s\trefs/heads/main\n' "$MAIN"; fi ;;
 tag*|push*) echo 'FORBIDDEN WRITE' >&2; exit 99 ;;
 *) echo "UNEXPECTED GIT: $*" >&2; exit 98 ;;
esac
`)
			proof := tc.proof
			if proof == "" {
				proof = green
			}
			write("record.json", proof)
			args := tc.args
			if args == nil {
				args = []string{"--commit", candidate}
			}
			args = append(append([]string{}, args...), "--dry-run")
			cmd := exec.Command("bash", append([]string{filepath.Join(dir, "scripts/release-tag.sh")}, args...)...)
			cmd.Dir = dir
			cmd.Env = []string{"PATH=" + filepath.Join(dir, "bin") + ":" + os.Getenv("PATH"), "HOME=" + dir,
				"FIXTURE=" + dir, "CALLS=" + filepath.Join(dir, "calls"), "MODE=" + tc.mode,
				"CANDIDATE=" + candidate, "SOURCE=" + source, "MAIN=" + main}
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok || !strings.Contains(string(out), tc.want) {
				t.Fatalf("err=%v, want success=%v and %q; output:\n%s", err, tc.ok, tc.want, out)
			}
			if tc.ok && !strings.Contains(string(out), "DRY RUN") {
				t.Fatalf("not a dry run: %s", out)
			}
			calls, readErr := os.ReadFile(filepath.Join(dir, "calls"))
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			for _, line := range strings.Split(string(calls), "\n") {
				if strings.HasPrefix(line, "git tag") || strings.HasPrefix(line, "git push") || strings.Contains(line, "gate run") {
					t.Fatalf("forbidden write/gate: %s", line)
				}
			}
			if strings.Contains(string(out), "UNEXPECTED GIT") {
				t.Fatalf("unmodeled command: %s", out)
			}
			if tc.ok && tc.mode != "default" {
				for _, want := range []string{"Tree: ddddd", "Version source: " + source, "Trusted main: " + main, fmt.Sprintf("gate show %s", candidate)} {
					if !strings.Contains(string(out)+string(calls), want) {
						t.Errorf("missing evidence %q", want)
					}
				}
			}
		})
	}
}
