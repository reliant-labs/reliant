// File: internal/linter/forgeconv/frontend_hook_test_shape.go
//
// The forgeconv-frontend-hook-test-shape analyzer catches a scaffolded hook
// test whose assertions contradict the hook it exercises: a query-shaped
// test (await isSuccess) against a useMutation hook, or a mutation-shaped
// test (mutateAsync) against a useQuery hook. Either one can NEVER pass.
//
// Why this can happen at all, when one classification feeds both files.
// generateFrontendHooks computes the query/mutation split once — the
// NO_SIDE_EFFECTS option or the name heuristic in codegen.isQueryRPC, then
// the `// forge:mutation` override
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
//   - an author declares `idempotency_level = NO_SIDE_EFFECTS` on an RPC the
//     heuristic read as a mutation (Health, Version). The opposite flip: the
//     test still calls mutateAsync on what is now a useQuery.
//
// None of these cases is a template bug, and neither is reachable by making the
// emitters share a classifier — they already do. The gap is that nothing
// NOTICES the frozen half. The existing forgeconv-frontend-hook-tests rule
// only asks whether a test EXISTS, not whether it can pass. So a project can
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
//
// It runs in the `--conventions` lane, not `--tests`. It used to sit in
// `--tests` beside two warning-only rules, under a flag documented as
// "warnings only", so the projects that gate on a hand-picked set of fast
// lanes left it out — and every lane they did run passed over the stale
// test. reliant's RunService (#1508) is the measured case: LastRunPerWorkflow
// gained NO_SIDE_EFFECTS, its owned starter test kept calling mutateAsync on
// what was now a useQuery, and `forge lint --conventions` — the lane its
// generate:check runs — reported green while vitest failed. `forge generate`
// now reports the same rows at the moment of the flip (StaleHookTestRows is
// shared with it), and this rule keeps reporting them until they are fixed.

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
// hooks.ts.tmpl on the most recent generate. createInfiniteQueryHook is
// deliberately not matched: an infinite hook exists only for a read, so its
// kind can never flip under its test.
var hookFactoryRE = regexp.MustCompile(`export\s+const\s+(use\w+)\s*=\s*create(Query|Mutation)Hook\b`)

// hookTestBlockRE splits a scaffolded test into per-RPC `it(...)` rows and
// captures the hook each one is about. The starter writes exactly one row
// per hook, titled with the hook name.
var hookTestBlockRE = regexp.MustCompile(`it\(\s*"(use\w+) `)

// HookTestRow is one row of a hook test written in one of the starter's two
// idioms.
type HookTestRow struct {
	// Hook is the hook the row exercises ("useGetKey").
	Hook string
	// Line is the 1-based line of the row's `it(` call.
	Line int
	// Query is true for the query idiom (await isSuccess) and false for
	// the mutation idiom (fire mutateAsync).
	Query bool
}

// StarterHookTestRows returns every row of a hook test that uses one of the
// starter's two idioms, in file order. A row using neither — a hand-written
// test — is left out: its assertions are the user's business.
func StarterHookTestRows(src string) []HookTestRow {
	locs := hookTestBlockRE.FindAllStringSubmatchIndex(src, -1)
	var out []HookTestRow
	for i, loc := range locs {
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		body := src[loc[1]:end]
		row := HookTestRow{Hook: src[loc[2]:loc[3]], Line: strings.Count(src[:loc[0]], "\n") + 1}
		switch {
		case strings.Contains(body, ".mutateAsync("), strings.Contains(body, ".mutate("):
			row.Query = false
		case strings.Contains(body, ".isSuccess"):
			row.Query = true
		default:
			continue
		}
		out = append(out, row)
	}
	return out
}

// StaleHookTestRows returns the rows of testSrc whose idiom contradicts the
// kind of the hook they exercise. isQuery maps a hook name to true for a
// useQuery hook and false for a useMutation hook; a row about a hook absent
// from it (another module's, or one forge no longer generates) is skipped.
//
// It is the one comparison both `forge lint` (reading the kinds off the
// generated hooks file) and `forge generate` (holding them in memory) make,
// so the two cannot disagree about which rows are stale.
func StaleHookTestRows(testSrc string, isQuery map[string]bool) []HookTestRow {
	var stale []HookTestRow
	for _, row := range StarterHookTestRows(testSrc) {
		if q, known := isQuery[row.Hook]; known && q != row.Query {
			stale = append(stale, row)
		}
	}
	return stale
}

// StaleRowsOrphanSettleHelper reports whether rewriting every stale row
// leaves the starter's waitForSettled helper with no caller. The helper
// exists only for mutation rows, so when the last one becomes a query row
// the declaration must go too — or no-unused-vars flags the file.
func StaleRowsOrphanSettleHelper(testSrc string, isQuery map[string]bool) bool {
	if !strings.Contains(testSrc, "function waitForSettled(") {
		return false
	}
	for _, row := range StarterHookTestRows(testSrc) {
		query := row.Query
		if q, known := isQuery[row.Hook]; known {
			query = q
		}
		if !query {
			return false
		}
	}
	return true
}

// HookTestRowFix is the body a stale row's renderHook and assertions are
// replaced with, for a hook of the given kind — the starter template's own
// lines, except that a mutation row waits for the rendered status inline,
// because a file scaffolded for a query-only service declares no
// waitForSettled helper to call.
func HookTestRowFix(hook string, query bool) string {
	if query {
		return fmt.Sprintf("const { result } = renderHook(() => %s({} as never), { wrapper });\n"+
			"await waitFor(() => expect(result.current.isSuccess).toBe(true));\n"+
			"expect(result.current.data).toBeDefined();", hook)
	}
	return fmt.Sprintf("const { result } = renderHook(() => %s(), { wrapper });\n"+
		"await result.current.mutateAsync({} as never).catch(() => {});\n"+
		"await waitFor(() => expect([\"success\", \"error\"]).toContain(result.current.status));", hook)
}

// HookKind names a hook kind the way the findings and generate's report do.
func HookKind(query bool) string {
	if query {
		return "useQuery"
	}
	return "useMutation"
}

// LintFrontendHookTestShape walks rootDir/frontends/*/src/hooks/ and
// rootDir/packages/hooks/src/generated/ for generated hooks files, pairs
// each with its scaffold-once sibling test, and reports every row whose
// test asserts the opposite kind. A missing frontends/ tree, or a hooks
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
		return result.Findings[i].Line < result.Findings[j].Line
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

	kinds := ParseHookKinds(string(hookSrc))
	if len(kinds) == 0 {
		return nil, nil
	}
	var findings []Finding
	for _, row := range StaleHookTestRows(string(testSrc), kinds) {
		findings = append(findings, hookShapeFinding(row, kinds[row.Hook], relPath(testPath, relRoot)))
	}
	return findings, nil
}

// ParseHookKinds maps each exported hook of a generated hooks module to its
// kind: true for a useQuery hook, false for a useMutation hook.
func ParseHookKinds(src string) map[string]bool {
	out := map[string]bool{}
	for _, m := range hookFactoryRE.FindAllStringSubmatch(src, -1) {
		out[m[1]] = m[2] == "Query"
	}
	return out
}

// hookShapeFinding renders one stale row, naming both halves and the reason
// the pair could come apart — the user did not do anything wrong, so the
// message explains the mechanism rather than implying a mistake.
func hookShapeFinding(row HookTestRow, query bool, testRel string) Finding {
	var why string
	if query {
		why = fmt.Sprintf("%s is a useQuery hook, but its test row calls mutateAsync on the result — "+
			"a query result has no mutateAsync, so this test can never pass.", row.Hook)
	} else {
		why = fmt.Sprintf("%s is a useMutation hook, but its test row asserts `isSuccess` without firing it — "+
			"a mutation stays idle until something calls mutateAsync, so this test can never pass.", row.Hook)
	}
	fix := strings.ReplaceAll(HookTestRowFix(row.Hook, query), "\n", "\n    ")
	return Finding{
		Rule:     "forgeconv-frontend-hook-test-shape",
		Severity: SeverityError,
		File:     testRel,
		Line:     row.Line,
		Message: why + " The hooks file is regenerated every run while this test is scaffold-once, " +
			"so the two drift apart whenever an RPC's query/mutation classification changes — " +
			"forge sharpening the name heuristic, a `// forge:mutation` marker being added to the proto, " +
			"or the rpc declaring `idempotency_level = NO_SIDE_EFFECTS`.",
		Remediation: fmt.Sprintf("replace the body of the %s row (line %d) with:\n    %s\n"+
			"  Or delete the test file and run `forge project rescaffold %s` to scaffold a fresh one.",
			row.Hook, row.Line, fix, testRel),
	}
}
