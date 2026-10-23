// File: internal/linter/forgeconv/frontend_hook_test_shape.go
//
// The forgeconv-frontend-hook-test-shape analyzer catches a scaffolded hook
// test whose assertions contradict the hook it exercises: a query-shaped
// test (await isSuccess) against a useMutation hook, or a mutation-shaped
// test (mutateAsync) against a useQuery hook. Either one can NEVER pass.
//
// Why this can happen at all, when one classification feeds both files.
// generateFrontendHooks computes the query/mutation split once — the name
// heuristic in codegen.isQueryMethod, then the `// forge:mutation` override
// — and renders BOTH the hooks file and its starter test from that single
// decision. The two therefore agree the moment they are written.
//
// They are pinned together in TIME, not in structure. The hooks file is
// Tier-1: rewritten on every `forge generate`, and gitignored in the
// default scaffold. The test is scaffold-once: written when absent and
// never overwritten, because it becomes the user's file the moment it
// lands. So when the classification for an RPC CHANGES, the hook moves and
// the test does not:
//
//   - forge sharpens the heuristic. Matching query prefixes as characters
//     rather than whole words read "Is" out of IssueMyReliantAPIKey and
//     generated a useQuery for a key-minting write. Fixing that flipped the
//     hook to useMutation and left every already-scaffolded test asserting
//     isSuccess on a mutation that nothing fires.
//   - an author adds `// forge:mutation` to an RPC the heuristic read as a
//     query. Same flip, same frozen test.
//
// Neither case is a template bug, and neither is reachable by making the
// emitters share a classifier — they already do. The gap is that nothing
// NOTICES the frozen half. `forge generate` writes the hook and skips the
// test in silence; the existing forgeconv-frontend-hook-tests rule only
// asks whether a test EXISTS, not whether it can pass. So a project can
// carry a permanently red test that no forge command mentions, which is how
// two of them reached a dogfood branch.
//
// This rule closes that gap by comparing the two files that already exist,
// with no proto parse and no classification of its own: it reads which
// factory each hook was built with and which idiom the test uses on it.
// Reading the ARTIFACTS rather than re-deriving the answer is deliberate —
// a second classifier here would be a third opinion that could itself drift.
//
// Severity is error, not warning: unlike a missing test (a gap in coverage),
// a drifted test is a red suite. It is scoped to the starter's two idioms,
// so a hand-written test that asserts something else is never second-guessed.

package forgeconv

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// hookFactoryRE captures the hook name and the factory it was built from
// in a generated hooks file:
//
//	export const useGetKey = createQueryHook<...>(
//
// Matching the generated line shape (rather than parsing TS) is safe here
// because this file is Tier-1: forge wrote every line of it from
// hooks.ts.tmpl on the most recent generate.
var hookFactoryRE = regexp.MustCompile(`export\s+const\s+(use\w+)\s*=\s*create(Query|Mutation)Hook\b`)

// hookTestBlockRE splits a scaffolded test into per-RPC `it(...)` blocks and
// captures the hook each one is about. The starter writes exactly one block
// per RPC, titled with the hook name.
var hookTestBlockRE = regexp.MustCompile(`it\(\s*"(use\w+) `)

// hookShape is which of the two starter idioms a file uses for one hook.
type hookShape int

const (
	shapeUnknown hookShape = iota
	shapeQuery
	shapeMutation
)

func (s hookShape) String() string {
	switch s {
	case shapeQuery:
		return "query"
	case shapeMutation:
		return "mutation"
	default:
		return "unknown"
	}
}

// LintFrontendHookTestShape walks rootDir/frontends/*/src/hooks/ and
// rootDir/packages/hooks/src/generated/ for generated hooks files, pairs
// each with its scaffold-once sibling test, and reports every hook whose
// test asserts the opposite shape. A missing frontends/ tree, or a hooks
// file with no sibling test, produces no finding — the latter belongs to
// forgeconv-frontend-hook-tests.
func LintFrontendHookTestShape(rootDir string) (Result, error) {
	var result Result

	for _, base := range hookSearchRoots(rootDir) {
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if shouldSkipFrontendSubdir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !isGeneratedHooksFile(p) {
				return nil
			}
			findings, ferr := lintOneHookPair(p, rootDir)
			if ferr != nil {
				return ferr
			}
			result.Findings = append(result.Findings, findings...)
			return nil
		})
		if err != nil {
			return Result{}, fmt.Errorf("walk %s: %w", base, err)
		}
	}

	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		return result.Findings[i].Message < result.Findings[j].Message
	})
	return result, nil
}

// hookSearchRoots are the two places generateFrontendHooks writes to: the
// per-frontend tree, and the shared workspace package when
// frontend.workspaces is enabled.
func hookSearchRoots(rootDir string) []string {
	return []string{
		filepath.Join(rootDir, "frontends"),
		filepath.Join(rootDir, "packages", "hooks", "src", "generated"),
	}
}

// isGeneratedHooksFile matches the hooks module forge emits, in both its
// current `_gen` spelling and the pre-rename one still present in older
// projects — while excluding the test that sits beside it.
func isGeneratedHooksFile(path string) bool {
	name := filepath.Base(path)
	if strings.Contains(name, ".test.") {
		return false
	}
	return strings.HasSuffix(name, "-hooks_gen.ts") || strings.HasSuffix(name, "-hooks.ts")
}

// siblingTestPath returns the scaffold-once test that belongs to a hooks
// file. The test deliberately drops the `_gen` suffix its subject carries,
// because it is the user's file rather than a regenerated one.
func siblingTestPath(hookPath string) string {
	base := strings.TrimSuffix(hookPath, ".ts")
	return strings.TrimSuffix(base, "_gen") + ".test.tsx"
}

// lintOneHookPair compares one hooks file against its sibling test.
func lintOneHookPair(hookPath, relRoot string) ([]Finding, error) {
	testPath := siblingTestPath(hookPath)
	testSrc, err := os.ReadFile(testPath)
	if err != nil {
		// No sibling test: forgeconv-frontend-hook-tests' finding, not ours.
		return nil, nil //nolint:nilerr // absence is another rule's business
	}
	hookSrc, err := os.ReadFile(hookPath)
	if err != nil {
		return nil, nil //nolint:nilerr // unreadable generated file is not a shape defect
	}

	hookShapes := parseHookFactories(string(hookSrc))
	if len(hookShapes) == 0 {
		return nil, nil
	}
	testShapes := parseTestShapes(string(testSrc))

	names := make([]string, 0, len(testShapes))
	for name := range testShapes {
		names = append(names, name)
	}
	sort.Strings(names)

	var findings []Finding
	for _, hookName := range names {
		asserted := testShapes[hookName]
		actual, known := hookShapes[hookName]
		// A hook the test exercises but this file does not declare belongs
		// to another service's module; and a block using neither starter
		// idiom is a hand-written test we do not secondguess.
		if !known || asserted == shapeUnknown || asserted == actual {
			continue
		}
		findings = append(findings, hookShapeFinding(hookName, actual, asserted, testPath, relRoot))
	}
	return findings, nil
}

// parseHookFactories maps each exported hook to the factory it was built
// with in the generated module.
func parseHookFactories(src string) map[string]hookShape {
	out := map[string]hookShape{}
	for _, m := range hookFactoryRE.FindAllStringSubmatch(src, -1) {
		if m[2] == "Query" {
			out[m[1]] = shapeQuery
		} else {
			out[m[1]] = shapeMutation
		}
	}
	return out
}

// parseTestShapes maps each hook the test exercises to the starter idiom it
// uses on it. The starter emits one `it(...)` block per RPC titled with the
// hook name, so we split on those titles and read the body of each block.
//
// A block using neither idiom maps to shapeUnknown, which the caller
// ignores: that is a hand-written test, and its assertions are the user's
// business.
func parseTestShapes(src string) map[string]hookShape {
	locs := hookTestBlockRE.FindAllStringSubmatchIndex(src, -1)
	out := make(map[string]hookShape, len(locs))
	for i, loc := range locs {
		name := src[loc[2]:loc[3]]
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := src[loc[1]:end]
		switch {
		case strings.Contains(body, ".mutateAsync("), strings.Contains(body, ".mutate("):
			out[name] = shapeMutation
		case strings.Contains(body, ".isSuccess"):
			out[name] = shapeQuery
		default:
			out[name] = shapeUnknown
		}
	}
	return out
}

// hookShapeFinding renders one drift, naming both halves and the reason the
// pair could come apart — the user did not do anything wrong, so the
// message explains the mechanism rather than implying a mistake.
func hookShapeFinding(hookName string, actual, asserted hookShape, testPath, relRoot string) Finding {
	var why, remedy string
	if actual == shapeMutation {
		why = fmt.Sprintf("%s is a useMutation hook, but its test asserts `isSuccess` without firing it — "+
			"a mutation stays idle until something calls mutateAsync, so this test can never pass.", hookName)
		remedy = fmt.Sprintf("rewrite the %s block to fire the mutation:\n"+
			"    const { result } = renderHook(() => %s(), { wrapper });\n"+
			"    await result.current.mutateAsync({} as never).catch(() => {});\n"+
			"    expect(result.current).toBeDefined();\n"+
			"  Or delete the test file and run `forge project rescaffold %s` to scaffold a fresh one.",
			hookName, hookName, relPath(testPath, relRoot))
	} else {
		why = fmt.Sprintf("%s is a useQuery hook, but its test calls mutateAsync on the result — "+
			"a query result has no mutateAsync, so this test can never pass.", hookName)
		remedy = fmt.Sprintf("rewrite the %s block to await the query:\n"+
			"    const { result } = renderHook(() => %s({} as never), { wrapper });\n"+
			"    await waitFor(() => expect(result.current.isSuccess).toBe(true));\n"+
			"  Or delete the test file and run `forge project rescaffold %s` to scaffold a fresh one.",
			hookName, hookName, relPath(testPath, relRoot))
	}

	return Finding{
		Rule:     "forgeconv-frontend-hook-test-shape",
		Severity: SeverityError,
		File:     relPath(testPath, relRoot),
		Line:     1,
		Message: why + " The hooks file is regenerated every run while this test is scaffold-once, " +
			"so the two drift apart whenever an RPC's query/mutation classification changes — " +
			"forge sharpening the name heuristic, or a `// forge:mutation` marker being added to the proto.",
		Remediation: remedy,
	}
}
