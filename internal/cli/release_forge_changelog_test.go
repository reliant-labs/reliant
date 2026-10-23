// File: internal/cli/release_forge_changelog_test.go
//
// CHANGELOG.md is written only at release time, by scripts/release-forge.sh
// (through scripts/changelog-release.sh), from the PR titles merged since the
// previous tag. These tests pin that: the grouping and the breaking-change
// flag, the one-time fold of the old [Unreleased] prose, the prettier pass, the
// `gh release create` call, and that a dry run leaves no trace.
//
// The helper tests need only bash and git and are hermetic, so they run under
// -short. The ones that drive the whole release (a build, a push) skip there,
// like the rest of release_forge_script_test.go.
package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func changelogScriptPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(releaseForgeScriptPath(t)), "changelog-release.sh")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("changelog-release.sh not found at %s: %v", p, err)
	}
	return p
}

func commitEmpty(t *testing.T, repo, msg string) {
	t.Helper()
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", msg)
}

func writeRepoFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRepoFile(t *testing.T, repo, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// unreleasedChangelog is a CHANGELOG as the old per-PR habit left it: prose
// under [Unreleased], in two sections, with a link footer.
const unreleasedChangelog = `# Changelog

## [Unreleased]

### Added

- **Hand-written entry.** Written by a PR before this policy.

### Security

- Rotated a key.

## [0.1.0] - 2026-01-01

### Added

- first

[Unreleased]: https://github.com/reliant-labs/forge/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/reliant-labs/forge/compare/v0.0.9...v0.1.0
`

func runChangelogScript(t *testing.T, repo string, args ...string) string {
	t.Helper()
	full := append([]string{changelogScriptPath(t)}, args...)
	cmd := exec.CommandContext(context.Background(), "bash", full...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("changelog-release.sh %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// section returns the text of the "## [ver]" section of a changelog.
func section(t *testing.T, changelog, ver string) string {
	t.Helper()
	start := strings.Index(changelog, "## ["+ver+"]")
	if start < 0 {
		t.Fatalf("no section for %s in:\n%s", ver, changelog)
	}
	rest := changelog[start+1:]
	end := strings.Index(rest, "\n## [")
	if end < 0 {
		end = len(rest)
	}
	return changelog[start : start+1+end]
}

func TestChangelogRelease_GroupsByTypeAndFlagsBreaking(t *testing.T) {
	repo := newForgeFixtureRepo(t) // tagged v0.1.0
	writeRepoFile(t, repo, "CHANGELOG.md", unreleasedChangelog)
	gitIn(t, repo, "add", "CHANGELOG.md")
	gitIn(t, repo, "commit", "-q", "-m", "docs: seed changelog (#100)")

	commitEmpty(t, repo, "feat(orm): arrays bind natively (#101)")
	commitEmpty(t, repo, "fix: a nil map in generate (#102)")
	commitEmpty(t, repo, "refactor(cli): split the root command (#103)")
	commitEmpty(t, repo, "feat(config)!: drop the legacy flag (#104)")
	commitEmpty(t, repo, "fix(db): reject a bad dsn (#105)\n\nBREAKING CHANGE: a bad dsn used to be ignored.")
	commitEmpty(t, repo, "feat: relabelled breaking on GitHub (#106)")
	commitEmpty(t, repo, "Updated the readme without a type (#107)")
	commitEmpty(t, repo, "chore: release v0.1.5")

	runChangelogScript(t, repo, "--changelog", "CHANGELOG.md", "--version", "v0.2.0", "--date", "2026-02-03",
		"--prev-tag", "v0.1.0", "--breaking-prs", "106", "--repo-url", "https://github.com/reliant-labs/forge")
	sec := section(t, readRepoFile(t, repo, "CHANGELOG.md"), "v0.2.0")

	// Headings in Keep-a-Changelog order, each followed by its entries.
	wantInOrder := []string{
		"## [v0.2.0] - 2026-02-03",
		"### Breaking changes",
		"**feat(config):** drop the legacy flag ([#104](https://github.com/reliant-labs/forge/pull/104))",
		"**fix(db):** reject a bad dsn ([#105]",
		"**feat:** relabelled breaking on GitHub ([#106]",
		"### Added",
		"Hand-written entry.",
		"**orm:** arrays bind natively ([#101](https://github.com/reliant-labs/forge/pull/101))",
		"### Changed",
		"**cli:** split the root command ([#103](https://github.com/reliant-labs/forge/pull/103))",
		"Updated the readme without a type ([#107]",
		"### Fixed",
		"a nil map in generate ([#102]",
		"### Security",
		"Rotated a key.",
	}
	pos := 0
	for _, w := range wantInOrder {
		i := strings.Index(sec[pos:], w)
		if i < 0 {
			t.Fatalf("section is missing %q (after offset %d):\n%s", w, pos, sec)
		}
		pos += i + len(w)
	}
	if strings.Contains(sec, "release v0.1.5") {
		t.Errorf("a release commit leaked into the notes:\n%s", sec)
	}
	// A breaking entry is listed once, under Breaking changes only.
	for _, once := range []string{"drop the legacy flag", "reject a bad dsn", "relabelled breaking on GitHub"} {
		if n := strings.Count(sec, once); n != 1 {
			t.Errorf("%q listed %d times, want 1:\n%s", once, n, sec)
		}
	}
}

func TestChangelogRelease_FoldsUnreleasedOnceAndLeavesItEmpty(t *testing.T) {
	repo := newForgeFixtureRepo(t)
	writeRepoFile(t, repo, "CHANGELOG.md", unreleasedChangelog)
	gitIn(t, repo, "add", "CHANGELOG.md")
	gitIn(t, repo, "commit", "-q", "-m", "docs: seed changelog")
	commitEmpty(t, repo, "feat: something new (#9)")

	runChangelogScript(t, repo, "--changelog", "CHANGELOG.md", "--version", "v0.2.0", "--date", "2026-02-03", "--prev-tag", "v0.1.0")
	got := readRepoFile(t, repo, "CHANGELOG.md")

	if n := strings.Count(got, "Hand-written entry."); n != 1 {
		t.Fatalf("the Unreleased entry appears %d times, want exactly 1 (folded once):\n%s", n, got)
	}
	unreleased := got[strings.Index(got, "## [Unreleased]"):strings.Index(got, "## [v0.2.0]")]
	if strings.TrimSpace(strings.TrimPrefix(unreleased, "## [Unreleased]")) != "" {
		t.Errorf("[Unreleased] is not empty after the fold:\n%q", unreleased)
	}
	// Older history is untouched and the link footer moved to the new tag.
	if !strings.Contains(got, "## [0.1.0] - 2026-01-01\n\n### Added\n\n- first\n") {
		t.Errorf("older release section was altered:\n%s", got)
	}
	for _, link := range []string{
		"[Unreleased]: https://github.com/reliant-labs/forge/compare/v0.2.0...HEAD",
		"[v0.2.0]: https://github.com/reliant-labs/forge/compare/v0.1.0...v0.2.0",
		"[0.1.0]: https://github.com/reliant-labs/forge/compare/v0.0.9...v0.1.0",
	} {
		if !strings.Contains(got, link) {
			t.Errorf("link footer missing %q:\n%s", link, got)
		}
	}

	// A second release has nothing left to fold: the old prose is not repeated.
	commitEmpty(t, repo, "fix: later (#10)")
	runChangelogScript(t, repo, "--changelog", "CHANGELOG.md", "--version", "v0.3.0", "--date", "2026-03-03", "--prev-tag", "v0.2.0-none")
	again := readRepoFile(t, repo, "CHANGELOG.md")
	if n := strings.Count(again, "Hand-written entry."); n != 1 {
		t.Errorf("a second release repeated the folded prose (%d copies)", n)
	}
}

func TestChangelogRelease_EmptyReleaseSaysSo(t *testing.T) {
	repo := newForgeFixtureRepo(t)
	writeRepoFile(t, repo, "CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n## [0.1.0] - 2026-01-01\n")
	runChangelogScript(t, repo, "--changelog", "CHANGELOG.md", "--version", "v0.2.0", "--date", "2026-02-03", "--prev-tag", "v0.1.0")
	sec := section(t, readRepoFile(t, repo, "CHANGELOG.md"), "v0.2.0")
	if !strings.Contains(sec, "No notable changes.") {
		t.Errorf("a release with no commits and no Unreleased prose should say so:\n%s", sec)
	}
}

// fakeGH writes a gh stand-in to dir and returns its path plus the log it
// appends to. `pr list` answers with breakingPRs; `release create` records its
// arguments and the notes file's contents; failRelease makes it exit non-zero.
func fakeGH(t *testing.T, breakingPRs string, failRelease bool) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "gh")
	log = filepath.Join(dir, "gh.log")
	fail := "0"
	if failRelease {
		fail = "1"
	}
	script := `#!/usr/bin/env bash
echo "ARGS: $*" >> "` + log + `"
if [ "$1 $2" = "pr list" ]; then printf '%s\n' ` + breakingPRs + `; exit 0; fi
if [ "$1 $2" = "release create" ]; then
  prev=""; for a in "$@"; do
    if [ "$prev" = "--notes-file" ]; then { echo "NOTES-BEGIN"; cat "$a"; echo "NOTES-END"; } >> "` + log + `"; fi
    prev="$a"
  done
  [ "` + fail + `" = "1" ] && { echo "boom" >&2; exit 1; }
fi
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

// githubFixtureRepo is the fixture with CHANGELOG.md, an origin that LOOKS like
// github.com (so the script computes a repo slug and calls gh) but is rewritten
// by insteadOf to a local bare repo, which is where the push really goes.
func githubFixtureRepo(t *testing.T) (repo, origin string) {
	t.Helper()
	repo = newForgeFixtureRepo(t)
	origin = filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, repo, "clone", "--bare", "--quiet", repo, origin)
	fake := "https://github.com/reliant-labs/forge.git"
	gitIn(t, repo, "remote", "add", "origin", fake)
	gitIn(t, repo, "config", "url."+origin+".insteadOf", fake)
	commitEmpty(t, repo, "feat(kcl): a new thing (#201)")
	commitEmpty(t, repo, "fix: a bug (#202)")
	commitEmpty(t, repo, "feat(api): an incompatible change (#203)")
	gitIn(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")
	return repo, origin
}

func TestReleaseForgeScript_WritesChangelogCommitsItAndCreatesTheGitHubRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a build and a git push")
	}
	repo, origin := githubFixtureRepo(t)
	gh, ghLog := fakeGH(t, "203", false)

	out, err := runForgeScriptEnv(t, repo, []string{"FORGE_RELEASE_GH=" + gh}, "v0.2.0")
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}

	// The release commit carries CHANGELOG.md alongside the version files.
	files := gitOut(t, repo, "show", "--pretty=format:", "--name-only", "HEAD")
	for _, want := range []string{"CHANGELOG.md", "VERSION", "internal/buildinfo/VERSION"} {
		if !strings.Contains(files, want) {
			t.Errorf("release commit does not touch %s:\n%s", want, files)
		}
	}
	if status := gitOut(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("the release left the tree dirty:\n%s", status)
	}
	if remote := gitOut(t, origin, "show", "main:CHANGELOG.md"); !strings.Contains(remote, "## [v0.2.0] - ") {
		t.Errorf("the pushed CHANGELOG.md has no v0.2.0 section:\n%s", remote)
	}

	got := readRepoFile(t, repo, "CHANGELOG.md")
	sec := section(t, got, "v0.2.0")
	for _, w := range []string{
		"### Breaking changes", "an incompatible change", // from `gh pr list --label breaking`
		"### Added", "a new thing",
		"### Fixed", "a bug",
	} {
		if !strings.Contains(sec, w) {
			t.Errorf("v0.2.0 section is missing %q:\n%s", w, sec)
		}
	}
	if strings.Contains(sec, "release v0.2.0") {
		t.Errorf("the release commit leaked into its own notes:\n%s", sec)
	}

	logged, _ := os.ReadFile(ghLog)
	log := string(logged)
	for _, want := range []string{
		"ARGS: pr list --repo reliant-labs/forge --state merged --label breaking",
		"ARGS: release create v0.2.0 --repo reliant-labs/forge --verify-tag --title v0.2.0 --notes-file",
		"NOTES-BEGIN",
		"a new thing",
		"an incompatible change",
		"NOTES-END",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("gh was not called as expected; missing %q in:\n%s", want, log)
		}
	}
	// The notes are the section's body: no version heading of their own (the
	// release title carries it).
	notes := log[strings.Index(log, "NOTES-BEGIN"):strings.Index(log, "NOTES-END")]
	if strings.Contains(notes, "## [v0.2.0]") {
		t.Errorf("release notes repeat the version heading:\n%s", notes)
	}
	if strings.Index(log, "pr list") > strings.Index(log, "release create") {
		t.Errorf("gh release create ran before the breaking-PR lookup:\n%s", log)
	}
}

func TestReleaseForgeScript_OversizedSectionPublishesGeneratedBulletsOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a build and a git push")
	}
	repo, _ := githubFixtureRepo(t)
	// A long-lived [Unreleased] folded in makes the section exceed GitHub's
	// 125000-character release-body limit.
	huge := "# Changelog\n\n## [Unreleased]\n\n### Added\n\n" + strings.Repeat("- old per-PR entry that is long enough to add up quickly, padding padding padding\n", 2000) + "\n## [0.1.0] - 2026-01-01\n"
	writeRepoFile(t, repo, "CHANGELOG.md", huge)
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "docs: seed")
	gitIn(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")
	gh, ghLog := fakeGH(t, "", false)

	out, err := runForgeScriptEnv(t, repo, []string{"FORGE_RELEASE_GH=" + gh}, "v0.2.0")
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}
	logged, _ := os.ReadFile(ghLog)
	notes := string(logged)
	notes = notes[strings.Index(notes, "NOTES-BEGIN"):strings.Index(notes, "NOTES-END")]
	if len(notes) > 100000 {
		t.Errorf("release notes are %d bytes; GitHub rejects a body over 125000 characters", len(notes))
	}
	for _, w := range []string{"a new thing", "a bug", "CHANGELOG.md"} {
		if !strings.Contains(notes, w) {
			t.Errorf("fallback notes are missing %q:\n%s", w, notes)
		}
	}
	if strings.Contains(notes, "old per-PR entry") {
		t.Error("fallback notes still carry the folded Unreleased prose")
	}
	// The committed changelog keeps everything.
	if !strings.Contains(readRepoFile(t, repo, "CHANGELOG.md"), "old per-PR entry") {
		t.Error("CHANGELOG.md lost the folded Unreleased prose")
	}
}

func TestReleaseForgeScript_GitHubReleaseFailureStillLeavesTheTagPushed(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a build and a git push")
	}
	repo, origin := githubFixtureRepo(t)
	gh, _ := fakeGH(t, "", true)

	out, err := runForgeScriptEnv(t, repo, []string{"FORGE_RELEASE_GH=" + gh}, "v0.2.0")
	if err == nil {
		t.Fatalf("a failed `gh release create` must fail the script:\n%s", out)
	}
	if !strings.Contains(out, "the GitHub release was not created") || !strings.Contains(out, "gh release create v0.2.0") {
		t.Errorf("the failure must say what to run to retry:\n%s", out)
	}
	if tags := gitOut(t, origin, "tag", "-l"); !strings.Contains(tags, "v0.2.0") {
		t.Errorf("the tag must be pushed regardless of the GitHub release; remote tags:\n%s", tags)
	}
}

func TestReleaseForgeScript_NoGitHubOriginSkipsTheGitHubRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a build and a git push")
	}
	repo := newForgeFixtureRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, repo, "clone", "--bare", "--quiet", repo, origin)
	gitIn(t, repo, "remote", "add", "origin", origin)
	commitEmpty(t, repo, "feat: offline thing (#5)")
	gitIn(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")
	gh, ghLog := fakeGH(t, "", false)

	out, err := runForgeScriptEnv(t, repo, []string{"FORGE_RELEASE_GH=" + gh}, "v0.2.0")
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "skipping the GitHub release") {
		t.Errorf("expected the skip to be announced:\n%s", out)
	}
	if b, _ := os.ReadFile(ghLog); strings.Contains(string(b), "release create") {
		t.Errorf("gh release create ran for a non-GitHub origin:\n%s", b)
	}
	if sec := section(t, readRepoFile(t, repo, "CHANGELOG.md"), "v0.2.0"); !strings.Contains(sec, "offline thing") {
		t.Errorf("CHANGELOG.md was not written offline:\n%s", sec)
	}
}

func TestReleaseForgeScript_DryRunLeavesChangelogUntouched(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script, which builds the fixture module")
	}
	repo := newForgeFixtureRepo(t)
	commitEmpty(t, repo, "feat: dry thing (#7)")
	before := readRepoFile(t, repo, "CHANGELOG.md")
	gh, ghLog := fakeGH(t, "", false)

	out, err := runForgeScriptEnv(t, repo, []string{"FORGE_RELEASE_GH=" + gh}, "--dry-run", "v0.2.0")
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, out)
	}
	if after := readRepoFile(t, repo, "CHANGELOG.md"); after != before {
		t.Errorf("a dry run changed CHANGELOG.md:\n%s", after)
	}
	if status := gitOut(t, repo, "status", "--porcelain"); status != "" {
		t.Errorf("dry run left the tree dirty:\n%s", status)
	}
	if !strings.Contains(out, "dry thing") {
		t.Errorf("the dry run should show the section it would write:\n%s", out)
	}
	if b, _ := os.ReadFile(ghLog); strings.Contains(string(b), "release create") {
		t.Errorf("a dry run created a GitHub release:\n%s", b)
	}
}

// TestReleaseForgeScript_ChangelogIsPrettierClean runs a real release with the
// real formatter: the committed CHANGELOG.md must pass `prettier --check` at the
// version .pre-commit-config.yaml pins, or the release commit meets a red hook
// on main. It needs npx and the network (prettier is fetched on first use).
func TestReleaseForgeScript_ChangelogIsPrettierClean(t *testing.T) {
	if testing.Short() {
		t.Skip("runs npx prettier, which downloads on first use")
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("npx not on PATH under CI: install node so the prettier check runs")
		}
		t.Skip("npx not on PATH")
	}
	pin, err := os.ReadFile(filepath.Join("..", "..", ".pre-commit-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	pinVer := ""
	for _, l := range strings.Split(string(pin), "\n") {
		if i := strings.Index(l, `additional_dependencies: ["prettier@`); i >= 0 {
			pinVer = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(l[i:]), `additional_dependencies: ["prettier@`), `"]`)
		}
	}
	if pinVer == "" {
		t.Fatal("no prettier pin found in forge's .pre-commit-config.yaml")
	}

	repo, _ := githubFixtureRepo(t)
	// The script reads the pin from the repo it releases, and the prose below is
	// what prettier rewrites: a `*` bullet, a long wrapped line, a table.
	writeRepoFile(t, repo, ".pre-commit-config.yaml", string(pin))
	writeRepoFile(t, repo, "CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n\n### Added\n\n* a star bullet\n\n|a|b|\n|-|-|\n|1|2|\n\n## [0.1.0] - 2026-01-01\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "docs: seed")
	gitIn(t, repo, "push", "-q", "origin", "HEAD:refs/heads/main")
	gh, _ := fakeGH(t, "", false)

	out, err := runForgeScriptEnv(t, repo, []string{"FORGE_RELEASE_GH=" + gh, "FORGE_RELEASE_PRETTIER="}, "v0.2.0")
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}
	got := readRepoFile(t, repo, "CHANGELOG.md")
	if strings.Contains(got, "* a star bullet") || !strings.Contains(got, "- a star bullet") {
		t.Errorf("prettier did not run over the written CHANGELOG.md:\n%s", got)
	}
	if out, err := exec.Command(npx, "--yes", "prettier@"+pinVer, "--check", filepath.Join(repo, "CHANGELOG.md")).CombinedOutput(); err != nil {
		t.Fatalf("the released CHANGELOG.md is not prettier-clean: %v\n%s", err, out)
	}
}
