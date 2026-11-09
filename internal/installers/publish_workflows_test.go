package installers

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// readRepoFile returns rel's content, read by this process so Go's test cache
// sees it.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(repoFile(t, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// codeLines drops YAML comment lines, so a comment that names a forbidden
// spelling (to explain why it is absent) is not mistaken for using it.
func codeLines(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			out = append(out, line)
		}
	}
	return out
}

// The tap's workflow runs on ubuntu-latest, whose Homebrew is installed but NOT
// on PATH. A `brew` call before `brew shellenv` is "command not found", the job
// fails, and no formula PR ever opens — which the first version of this file
// did. The renderer's output is installed and tested before it reaches a PR.
func TestTapWorkflowPutsBrewOnPathBeforeUsingIt(t *testing.T) {
	lines := codeLines(readRepoFile(t, "docs/installers/update-forge-formula.yml"))
	shellenv := -1
	for i, line := range lines {
		s := strings.TrimSpace(line)
		if strings.Contains(s, "/home/linuxbrew/.linuxbrew/bin/brew shellenv") {
			if shellenv < 0 {
				shellenv = i
			}
			continue
		}
		if strings.HasPrefix(s, "brew ") && (shellenv < 0 || i < shellenv) {
			t.Errorf("line %q calls brew before `eval \"$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)\"`", s)
		}
	}
	if shellenv < 0 {
		t.Fatal("update-forge-formula.yml never puts Homebrew on PATH")
	}
	body := strings.Join(lines, "\n")
	for _, want := range []string{
		"brew install --formula reliant-labs/reliant/forge",
		"brew test reliant-labs/reliant/forge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("update-forge-formula.yml no longer runs %q before opening the PR", want)
		}
	}
}

// npm accepts a publish of @reliantlabs/forge only from the workflow registered
// as its trusted publisher: reliant-labs/forge's release-npm.yml. Renaming the
// file fails this read, and the registration must move with it
// (docs/releasing.md, "Installers").
func TestNpmPublishWorkflowUsesTrustedPublishing(t *testing.T) {
	body := strings.Join(codeLines(readRepoFile(t, ".github/workflows/release-npm.yml")), "\n")
	for _, want := range []string{
		"id-token: write",      // the OIDC token npm exchanges
		"types: [published]",   // after the release's archives exist
		`npm pkg set version=`, // the tag's version, not the tagged manifest's
		"npm publish --provenance --access public",
		"scripts/verify-npm-published.sh",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("release-npm.yml lacks %q", want)
		}
	}
	// A token would take precedence over the OIDC exchange and silently turn
	// this back into token auth.
	for _, banned := range []string{"NODE_AUTH_TOKEN", "NPM_TOKEN"} {
		if strings.Contains(body, banned) {
			t.Errorf("release-npm.yml sets %s; publishing must use trusted publishing only", banned)
		}
	}
}

// @reliantlabs/forge-web-runtime is published only by the workflow registered
// as its trusted publisher: reliant-labs/forge's release-web-runtime.yml, run
// by the web-runtime/v* tag that scripts/release-web-runtime.sh pushes (at a
// release train cut and at every forge release). Renaming the file or moving
// the publish to another trigger breaks publishing until the npm registration
// moves with it, so this pins the file, its trigger and the OIDC publish.
func TestWebRuntimePublishWorkflowUsesTrustedPublishing(t *testing.T) {
	body := strings.Join(codeLines(readRepoFile(t, ".github/workflows/release-web-runtime.yml")), "\n")
	for _, want := range []string{
		"id-token: write",          // the OIDC token npm exchanges
		`tags: ["web-runtime/v*"]`, // the tag release-web-runtime.sh pushes
		`npm pkg set version=`,     // the tag's computed version, not the tagged manifest's
		"npm publish --provenance --access public",
		"scripts/verify-npm-published.sh",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("release-web-runtime.yml lacks %q", want)
		}
	}
	for _, banned := range []string{"NODE_AUTH_TOKEN", "NPM_TOKEN"} {
		if strings.Contains(body, banned) {
			t.Errorf("release-web-runtime.yml sets %s; publishing must use trusted publishing only", banned)
		}
	}
	script := readRepoFile(t, "scripts/release-web-runtime.sh")
	if !strings.Contains(script, `WORKFLOW_FILE="release-web-runtime.yml"`) {
		t.Errorf("scripts/release-web-runtime.sh no longer waits on release-web-runtime.yml, the workflow npm trusts")
	}
}

// pinnedActionRef is an external action pinned to a full commit SHA. A tag or
// branch (`@v7`) is mutable: whoever controls the action's repository can move
// it, and the moved code runs with the publish job's OIDC token.
var pinnedActionRef = regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)

// The workflows that publish to npm hold an id-token: write credential, so
// every action they run must be pinned to an immutable commit.
func TestPublishWorkflowsPinActionsToCommitSHAs(t *testing.T) {
	for _, rel := range []string{
		".github/workflows/release-npm.yml",
		".github/workflows/release-web-runtime.yml",
	} {
		uses := 0
		for _, line := range codeLines(readRepoFile(t, rel)) {
			ref, ok := actionRef(line)
			if !ok {
				continue
			}
			uses++
			// A local action (./path) is code from this commit, already pinned.
			if strings.HasPrefix(ref, "./") {
				continue
			}
			if !pinnedActionRef.MatchString(ref) {
				t.Errorf("%s: uses %q; pin it to a full commit SHA (owner/repo@<40 hex> # vX.Y.Z)", rel, ref)
			}
		}
		if uses == 0 {
			t.Errorf("%s: found no `uses:` lines; has the file's shape changed?", rel)
		}
	}
}

// actionRef returns the reference of a `uses:` step key, without its trailing
// comment or quotes.
func actionRef(line string) (string, bool) {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "- ")
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), "uses:")
	if !ok {
		return "", false
	}
	if i := strings.Index(rest, "#"); i >= 0 {
		rest = rest[:i]
	}
	return strings.Trim(strings.TrimSpace(rest), `"'`), true
}
