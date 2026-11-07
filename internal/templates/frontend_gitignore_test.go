package templates_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"
)

// The scaffolded frontend .gitignore has to track forge's renamed hook
// output — and, just as importantly, must not sweep up the other `_gen`
// files beside it.
//
// Both halves are regressions that actually happened. Forge renamed the
// hook emitter's output to a `_gen` suffix and left the ignore rule
// spelling the OLD name, so all 28 renamed hook files in control-plane
// were untracked: invisible to `forge ci verify-generated` (which diffs a
// fresh generate against the checkout via `git status --porcelain`, and
// therefore honours .gitignore) and missing entirely from a fresh clone.
// The obvious repair — a `src/lib/*_gen.ts` glob — is the second bug: it
// silently untracks apiurl_gen.ts, basepath_gen.ts and config_gen.ts,
// which forge emits but the project commits. Hence: enumerate.
func TestFrontendGitignore_TracksRenamedHooksAndCommittedGenFiles(t *testing.T) {
	// The current hook filename comes from the emitter's own naming
	// helper rather than a literal, so a future rename fails this test
	// instead of silently re-opening the same hole.
	hookFile := naming.ServiceHookFile("UserService") // user-service-hooks_gen.ts
	if !strings.HasSuffix(hookFile, "_gen.ts") {
		t.Fatalf("ServiceHookFile returned %q; expected the Tier-1 `_gen` suffix", hookFile)
	}

	for _, kind := range []string{"nextjs", "vite-spa", "react-native"} {
		t.Run(kind, func(t *testing.T) {
			content, err := templates.FrontendTemplates().Get(filepath.Join(kind, ".gitignore"))
			if err != nil {
				t.Fatalf("read %s .gitignore template: %v", kind, err)
			}
			patterns := gitignorePatterns(string(content))

			// MUST be committed: the renamed hooks, the barrel that
			// re-exports them, and the src/lib + src/mocks gen files.
			for _, tracked := range []string{
				"src/hooks/" + hookFile,
				"src/hooks/index.ts",
				"src/lib/apiurl_gen.ts",
				"src/lib/config_gen.ts",
			} {
				if pat := firstMatch(patterns, tracked); pat != "" {
					t.Errorf("%s is a committed forge-emitted file but the template ignores it via %q — "+
						"`forge ci verify-generated` cannot see an ignored file, so drift in it passes CI silently",
						tracked, pat)
				}
			}

			// The pre-rename spelling stays ignored so an un-upgraded
			// checkout does not start reporting its leftovers.
			legacy := "src/hooks/" + naming.ServiceHookFileLegacy("UserService")
			if firstMatch(patterns, legacy) == "" {
				t.Errorf("the pre-`_gen` hook name %s is no longer ignored; a tree generated before the rename "+
					"would start reporting files nothing regenerates", legacy)
			}
		})
	}
}

// TestFrontendGitignore_NoBlanketGenGlob is the specific trap, pinned by
// shape rather than by outcome: a `*_gen.ts` glob over src/lib or
// src/hooks reads as tidy and quietly untracks the committed gen files.
func TestFrontendGitignore_NoBlanketGenGlob(t *testing.T) {
	for _, kind := range []string{"nextjs", "vite-spa", "react-native"} {
		content, err := templates.FrontendTemplates().Get(filepath.Join(kind, ".gitignore"))
		if err != nil {
			t.Fatalf("read %s .gitignore template: %v", kind, err)
		}
		for _, pat := range gitignorePatterns(string(content)) {
			if strings.HasSuffix(pat, "*_gen.ts") || strings.HasSuffix(pat, "*_gen.tsx") {
				t.Errorf("%s/.gitignore has blanket glob %q — apiurl_gen.ts, basepath_gen.ts and "+
					"config_gen.ts are forge-emitted but committed; enumerate the ignored files instead",
					kind, pat)
			}
		}
	}
}

// gitignorePatterns returns the non-comment, non-blank rules in a
// .gitignore body.
func gitignorePatterns(body string) []string {
	var out []string
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// firstMatch returns the first pattern that ignores relPath, or "".
//
// Covers the two rule shapes these templates use — an exact path and a
// single `*` glob within one directory — which is enough to catch the
// mistakes above without reimplementing gitignore. TestFrontendGitignore
// asserts against real emitter filenames, and the behaviour is confirmed
// end-to-end against `git check-ignore` in the scaffold e2e corpus.
func firstMatch(patterns []string, relPath string) string {
	for _, pat := range patterns {
		p := strings.TrimPrefix(pat, "/")
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(relPath, p) {
				return pat
			}
			continue
		}
		if ok, err := filepath.Match(p, relPath); err == nil && ok {
			return pat
		}
		if p == relPath {
			return pat
		}
	}
	return ""
}
