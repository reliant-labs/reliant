// File: internal/linter/forgeconv/frontend_hook_tests.go
//
// The forgeconv-frontend-hook-tests analyzer warns when a generated
// frontend hooks file at frontends/<name>/src/hooks/<svc>-hooks.ts has no
// sibling test (<svc>-hooks.test.tsx).
//
// The lint mirrors forgeconv-handler-tests-use-tdd's job on the backend:
// surface drift toward "untested generated surface" before it ossifies.
// Warning-only — never gates the build. A frontend with no `src/hooks/`
// directory at all (no proto-driven services for that frontend) is a no-op.
//
// The rule used to treat an inert `<svc>-hooks.test.tsx.starter` as
// satisfying it, on the theory that activation was "a one-rename step the
// agent can take". Nobody ever took it: a whole dogfood run shipped a
// generated suite per service that had never executed, with this lint
// silent throughout. `forge generate` now writes the test live, so a
// missing test means a missing test.

package forgeconv

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LintFrontendHookTests walks rootDir/frontends/*/src/hooks/ for
// *-hooks.ts files and warns when no sibling test file or starter is
// present. Returns findings ordered by (file, line) to keep CI logs
// stable. A missing frontends/ tree is not an error — projects without
// any frontends produce an empty Result.
func LintFrontendHookTests(rootDir string) (Result, error) {
	frontendsDir := filepath.Join(rootDir, "frontends")
	if _, err := os.Stat(frontendsDir); os.IsNotExist(err) {
		return Result{}, nil
	}

	var hookFiles []string
	err := filepath.WalkDir(frontendsDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if shouldSkipFrontendSubdir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		// Match: frontends/<name>/src/hooks/<svc>-hooks.ts (NOT .test.ts,
		// NOT .test.tsx, NOT .tsx.starter — only the generated hooks
		// file itself).
		if !strings.HasSuffix(p, "-hooks.ts") {
			return nil
		}
		// Filter to files actually inside src/hooks/. The naive suffix
		// match would also pick up something like
		// frontends/foo/some-other-hooks.ts that lives outside src/hooks.
		// We want the lint to be tied to the codegen seam, not any file
		// whose name ends with -hooks.ts.
		rel := strings.TrimPrefix(filepath.ToSlash(p), filepath.ToSlash(frontendsDir)+"/")
		parts := strings.Split(rel, "/")
		if len(parts) < 4 {
			return nil
		}
		if parts[1] != "src" || parts[2] != "hooks" {
			return nil
		}
		hookFiles = append(hookFiles, p)
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("walk %s: %w", frontendsDir, err)
	}
	sort.Strings(hookFiles)

	var result Result
	for _, f := range hookFiles {
		finding, ok := lintFrontendHookFile(f, rootDir)
		if !ok {
			continue
		}
		result.Findings = append(result.Findings, finding)
	}

	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		if result.Findings[i].Line != result.Findings[j].Line {
			return result.Findings[i].Line < result.Findings[j].Line
		}
		return result.Findings[i].Rule < result.Findings[j].Rule
	})
	return result, nil
}

func shouldSkipFrontendSubdir(name string) bool {
	switch name {
	case "node_modules", ".next", "dist", "build", "out", "coverage", "gen":
		return true
	}
	return false
}

// lintFrontendHookFile checks one hooks file for a sibling test.
// Returns (finding, true) when there isn't one.
func lintFrontendHookFile(hookPath, relRoot string) (Finding, bool) {
	base := strings.TrimSuffix(hookPath, ".ts")
	testPath := base + ".test.tsx"

	if _, err := os.Stat(testPath); err == nil {
		return Finding{}, false
	}

	rel := relPath(hookPath, relRoot)
	return Finding{
		Rule:     "forgeconv-frontend-hook-tests",
		Severity: SeverityWarning,
		File:     rel,
		Line:     1,
		Message: "generated hooks file has no sibling test (.test.tsx) — re-run " +
			"`forge generate` to scaffold one, or write a test using " +
			"mockTransport/renderWithTransport from src/lib/test-utils.",
		Remediation: "add `" + filepath.Base(base) + ".test.tsx` next to the hooks file. " +
			"See the `frontend-testing` skill for recipes and the mockTransport seam.",
	}, true
}
