package installers

import (
	"os"
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
