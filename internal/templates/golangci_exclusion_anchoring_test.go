package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// golangci-lint's `linters.exclusions.paths` entries are REGEXES matched
// against the file path, not directory names. An unanchored entry therefore
// exempts every path that merely CONTAINS it.
//
// Both configs carried a bare `gen`, intended for the generated proto package
// at the project root. It also matched any package whose path contains those
// three letters — `internal/agent/` most obviously (a-GEN-t), and `legend`,
// `agenda`, `regenerate`, `genome`, plus every file named `*generate*.go`
// anywhere in the tree. Those packages linted green because nothing looked at
// them, which is the worst way to be green.
//
// Measured on forge's own repo: the bare entry excluded 311 of 1177 Go files
// (26.4%) — all of internal/codegen/ and internal/generator/ — and hid 223
// findings. With it anchored, 39 files (3.3%) are excluded.
//
// Two guards, because there are two ways to lose the lint surface:
//
//   - TestGolangciExclusionPathsAreAnchored is SYNTACTIC. It holds the rule
//     for every path entry in BOTH configs, not just the one that was wrong:
//     an entry must be anchored, or scoped by a separator or extension, so it
//     cannot silently widen to a package name.
//   - TestGolangciExclusionsLeaveMostOfTheRepoLinted is EMPIRICAL, and it is
//     the one that matters. A syntactically-anchored entry can still be far
//     too broad (`.*internal.*` is "scoped" and exempts everything). It
//     measures what the exclusions actually remove from forge's own tree and
//     fails if the linted surface shrinks past a stated fraction. It would
//     have caught the bare `gen` by its EFFECT, however it was spelled.

// lintConfigs are the two configs this rule governs. They are deliberately
// not the same file — forge is a CLI/code generator, the template's consumers
// are servers, and their enabled rule sets differ. The ANCHORING rule is
// identical for both.
func lintConfigs(t *testing.T) map[string]string {
	t.Helper()

	tmpl, err := templateFS.ReadFile("project/golangci.yml.tmpl")
	if err != nil {
		t.Fatalf("read shipped template: %v", err)
	}
	// forge's own config, relative to internal/templates/. Read, never
	// skipped-on-absence: the file is tracked in this repository, so a
	// missing one means the layout moved, and that must be loud.
	own, err := os.ReadFile(filepath.Join("..", "..", ".golangci.yml"))
	if err != nil {
		t.Fatalf("read forge's own .golangci.yml: %v", err)
	}
	return map[string]string{
		"internal/templates/project/golangci.yml.tmpl": string(tmpl),
		".golangci.yml": string(own),
	}
}

func TestGolangciExclusionPathsAreAnchored(t *testing.T) {
	t.Parallel()

	for name, body := range lintConfigs(t) {
		for _, entry := range exclusionPathEntries(t, name, body) {
			if pathEntryIsScoped(entry) {
				continue
			}
			t.Errorf("%s: exclusion path %q is a bare word. golangci-lint matches it as a REGEX "+
				"against the whole path, so it exempts every package CONTAINING it while reading "+
				"like the name of one directory. Anchor it as \"(^|/)%s/\", or spell the breadth "+
				"you mean (\".*%s.*\") so a reviewer can see it.",
				name, entry, entry, entry)
		}
	}
}

// TestGolangciExclusionsLeaveMostOfTheRepoLinted applies forge's own exclusion
// patterns to forge's own Go files and fails if they remove more than
// maxExcludedFraction of them.
//
// This is the guard that catches the next one. The bare `gen` was a valid
// regex, in the right key, doing exactly what regexes do — nothing about its
// SPELLING was detectably wrong without knowing the tree. Its EFFECT was
// unmistakable: a quarter of the repo stopped being linted. Measure the
// effect.
func TestGolangciExclusionsLeaveMostOfTheRepoLinted(t *testing.T) {
	if testing.Short() {
		// The denominator is every tracked .go file, listed by a child `git
		// ls-files`. Its reads are not inputs to go test's cache, so a cached
		// ok would survive files being added or removed anywhere in the repo.
		t.Skip("counts the checkout's files via git, which the test cache cannot track; runs in task test")
	}
	t.Parallel()

	// Anchored exclusions remove 3.3% (39/1177). The cap sits well above that
	// so ordinary additions do not trip it, and far below the 26.4% the bare
	// `gen` removed. Raising this number is not the fix for a failure here —
	// find what started matching.
	const maxExcludedFraction = 0.10

	// pkg/ is DISCOUNTED, not exempted. This guard asks "what fraction of the
	// repo does no linter read?", and pkg/ is read — by pkg/.golangci.yml in
	// its own CI job. Counting it as excluded would measure the wrong thing:
	// it is a fifth of the repo, so it would blow the cap while nothing had
	// actually stopped being linted.
	//
	// The discount is only honest while that second config exists, so require
	// it. Delete pkg/.golangci.yml and this fails rather than silently
	// certifying a fifth of the repo as covered by a config that is gone.
	if _, err := os.Stat(filepath.Join("..", "..", "pkg", ".golangci.yml")); err != nil {
		t.Fatalf("pkg/.golangci.yml is missing (%v), but .golangci.yml excludes pkg/ on the "+
			"understanding that it is linted there. Either restore it or drop the pkg/ exclusion — "+
			"as it stands a fifth of the repo is linted by nothing.", err)
	}

	own, err := os.ReadFile(filepath.Join("..", "..", ".golangci.yml"))
	if err != nil {
		t.Fatalf("read forge's own .golangci.yml: %v", err)
	}

	var patterns []*regexp.Regexp
	for _, entry := range exclusionPathEntries(t, ".golangci.yml", string(own)) {
		re, cerr := regexp.Compile(entry)
		if cerr != nil {
			t.Fatalf(".golangci.yml: exclusion path %q does not compile as a regex: %v", entry, cerr)
		}
		patterns = append(patterns, re)
	}

	all := repoGoFiles(t)
	var files []string
	for _, f := range all {
		if strings.HasPrefix(f, "pkg/") {
			continue // linted by pkg/.golangci.yml; see the discount above
		}
		files = append(files, f)
	}
	// A vacuous pass here would be the exact failure this repo guards against
	// elsewhere: zero discovered files makes every assertion below trivially
	// true.
	if len(files) < 100 {
		t.Fatalf("discovered only %d Go files in the repo — the walk is broken and this guard would pass vacuously", len(files))
	}

	var excluded []string
	for _, f := range files {
		for _, re := range patterns {
			if re.MatchString(f) {
				excluded = append(excluded, f)
				break
			}
		}
	}

	got := float64(len(excluded)) / float64(len(files))
	if got > maxExcludedFraction {
		t.Errorf("the lint exclusions in .golangci.yml remove %d of %d Go files (%.1f%%), over the %.0f%% cap.\n"+
			"A green `golangci-lint run` then means the linted remainder is clean, NOT that the repo is.\n"+
			"Find the entry that widened — most likely one that reads like a directory name and behaves "+
			"as a substring match — rather than raising the cap.\nExcluded sample: %s",
			len(excluded), len(files), got*100, maxExcludedFraction*100, sampleOf(excluded, 12))
	}
}

// repoGoFiles lists the repository's tracked .go files via git, so the set
// matches what CI lints rather than whatever build artifacts sit on disk.
func repoGoFiles(t *testing.T) []string {
	t.Helper()

	cmd := exec.Command("git", "ls-files", "*.go")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files
}

func sampleOf(items []string, n int) string {
	if len(items) > n {
		return strings.Join(items[:n], ", ") + ", …"
	}
	return strings.Join(items, ", ")
}

// exclusionPathEntries pulls the `- <value>` items under the
// `exclusions:` -> `paths:` key out of a golangci config (or its template).
func exclusionPathEntries(t *testing.T, name, body string) []string {
	t.Helper()

	lines := strings.Split(body, "\n")
	inPaths := false
	var out []string
	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		if trimmed == "paths:" {
			inPaths = true
			continue
		}
		if !inPaths {
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(trimmed, "- ") {
			// Any other key ends the sequence.
			break
		}
		val := strings.TrimPrefix(trimmed, "- ")
		val = strings.Trim(val, `"'`)
		out = append(out, val)
	}
	if len(out) == 0 {
		t.Fatalf("%s: no exclusion path entries found — this guard is looking at the wrong key and would pass vacuously", name)
	}
	return out
}

// pathEntryIsScoped reports whether an entry says out loud what it matches.
//
// The hazard is the BARE WORD: `gen` carries no regex metacharacter, so it
// reads as the name of a directory while behaving as a substring match, and
// nobody reviewing the config sees the difference. An entry that spells its
// own breadth — `.*frontends.*`, `.*\.pb\.go$`, `(^|/)gen/` — is an explicit
// choice, whatever its breadth, and is left alone.
var bareWord = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func pathEntryIsScoped(entry string) bool {
	return !bareWord.MatchString(entry)
}

// TestGolangciDoesNotShipABareGenExclusion pins the specific entry that was
// wrong, in BOTH configs, so a revert is loud rather than merely detectable.
func TestGolangciDoesNotShipABareGenExclusion(t *testing.T) {
	t.Parallel()

	for name, body := range lintConfigs(t) {
		for _, line := range strings.Split(body, "\n") {
			if strings.TrimSpace(line) == "- gen" {
				t.Errorf("%s carries a bare `- gen` lint exclusion again. "+
					"It is an unanchored regex: it exempts internal/agent/, internal/legend/, "+
					"every *generate*.go, and anything else whose path contains those three letters. "+
					"Use \"(^|/)gen/\" — the generated proto package is a DIRECTORY at the project root.", name)
			}
		}
	}
}
