// File: internal/linter/forgeconv/handler_file_size.go
//
// The forgeconv-handler-file-size analyzer warns when any single Go file
// under handlers/<svc>/ grows past a configurable LOC threshold. The
// threshold is plumbed through forge.yaml as `lint.handler_file_max_loc`
// (see config.LintConfig); a project that doesn't set the field gets the
// built-in default of [config.DefaultHandlerFileMaxLOC] lines.
//
// "LOC" here means non-blank, non-comment Go source lines — both `//`
// line comments and `/* ... */` block comments are stripped before the
// count. Blank lines (after comment stripping) also don't contribute.
// The counter is intentionally simple rather than running the full
// go/scanner over each file: the goal is a tight, eyeball-friendly
// "this file is too big" warning, not a sub-line-accurate token count.
//
// The rule is advisory (warning, not error). The remediation message
// points at the `forge scaffold handler-file` subcommand — the canonical
// way to split a fat handler file into per-RPC ones, which is the move
// this analyzer exists to nudge. Freshly scaffolded projects are already
// there: forge births one `rpc_<name>.go` per custom RPC, so this rule
// only fires on files a user grew by hand (or on a pre-split project's
// legacy handlers.go).

package forgeconv

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LintHandlerFileSize walks rootDir/handlers/ for *.go files and warns on
// any whose source-LOC count exceeds threshold. Test files (*_test.go)
// and generated files (*_gen.go) are excluded — they have their own size
// dynamics (table-driven tests legitimately go long; generated files
// aren't user-owned). A missing handlers/ directory is not an error
// (CLI / library projects).
//
// Findings are emitted in deterministic order (file, then rule).
func LintHandlerFileSize(rootDir string, threshold int) (Result, error) {
	if threshold <= 0 {
		// Treat 0/negative as "rule disabled" — callers normally pass
		// EffectiveHandlerFileMaxLOC() which folds the default in for
		// them, so reaching here means the caller deliberately opted
		// out (e.g. a test that wants to verify the no-op path).
		return Result{}, nil
	}

	handlersDir := filepath.Join(rootDir, "internal", "handlers")
	if _, err := os.Stat(handlersDir); os.IsNotExist(err) {
		return Result{}, nil
	}

	var files []string
	err := filepath.WalkDir(handlersDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if shouldSkipHandlerSubdir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		// Skip tests (own size dynamics) and generated files (not user
		// authored). The handler-tests-use-tdd analyzer covers the test
		// side; gen files are forge's problem if they grow.
		if strings.HasSuffix(p, "_test.go") || strings.HasSuffix(p, "_gen.go") {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("walk %s: %w", handlersDir, err)
	}
	sort.Strings(files)

	var result Result
	for _, f := range files {
		loc, lerr := countGoSourceLOC(f)
		if lerr != nil {
			return Result{}, lerr
		}
		if loc <= threshold {
			continue
		}
		rel, relErr := filepath.Rel(rootDir, f)
		if relErr != nil {
			rel = f
		}
		result.Findings = append(result.Findings, Finding{
			Rule:     "forgeconv-handler-file-size",
			Severity: SeverityWarning,
			File:     rel,
			Line:     0, // file-level finding
			Message: fmt.Sprintf(
				"handler file is %d > %d lines; consider splitting it with 'forge scaffold handler-file'",
				loc, threshold),
			Remediation: "extract one or more RPCs into sibling rpc_<name>.go files — the same name " +
				"forge itself births per custom RPC; per-RPC files keep diffs scoped and let two " +
				"authors work the same service without merging",
		})
	}

	// Stable: file, then rule (only one rule here, but keeps the sort
	// uniform with the other analyzers in this package).
	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		return result.Findings[i].Rule < result.Findings[j].Rule
	})
	return result, nil
}

// countGoSourceLOC returns the number of non-blank, non-comment lines in
// a Go source file. The counter strips `//` line comments and `/* ... */`
// block comments before checking blank-ness, so files dominated by
// docstrings (the generated-message shape, the package-doc shape) don't
// trip the size rule. Block comments may span multiple lines; the
// scanner tracks the in-block state across iterations.
//
// We deliberately don't use go/scanner here: this is a "is the file
// uncomfortably large" heuristic, not a token-accurate measurement. The
// extra dependency and per-file allocation cost isn't worth it for what
// is effectively a glorified `wc -l`.
func countGoSourceLOC(path string) (int, error) {
	f, err := os.Open(path) //nolint:gosec // lint walker drives paths
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck // read-only file, close error is harmless

	scanner := bufio.NewScanner(f)
	// Some generated files (and the occasional fat hand-rolled handler)
	// land on multi-kB lines; default bufio buffer (64KiB) is plenty for
	// real source, but bump it to 1MiB to be safe against pathological
	// minified or single-line-array fixtures.
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var (
		loc       int
		inBlock   bool
		nestedCnt int // /*/* nesting — Go doesn't allow it but tolerate
	)
	for scanner.Scan() {
		line := scanner.Text()
		stripped, stillInBlock := stripGoComments(line, inBlock, &nestedCnt)
		inBlock = stillInBlock
		if strings.TrimSpace(stripped) == "" {
			continue
		}
		loc++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan %s: %w", path, err)
	}
	return loc, nil
}

// stripGoComments removes line and block comment runs from a single line
// of Go source, returning the stripped text and whether a block comment
// is still open at end-of-line (carries into the next line). The startIn
// parameter is the in-block state inherited from the previous line.
//
// The implementation is a tiny character-walker — not a real lexer.
// It does NOT track string literals, so a string containing the
// characters `//` or `/*` would be mis-stripped. In practice this is
// fine for a size-counting heuristic (the worst case is undercounting
// by a handful of lines on files dominated by stringly-typed code), and
// the alternative (running go/scanner) is overkill for the job.
func stripGoComments(line string, startIn bool, nestedCnt *int) (string, bool) {
	var b strings.Builder
	inBlock := startIn
	for i := 0; i < len(line); i++ {
		if inBlock {
			// Look for `*/` to close the current block.
			if i+1 < len(line) && line[i] == '*' && line[i+1] == '/' {
				if *nestedCnt > 0 {
					*nestedCnt--
				} else {
					inBlock = false
				}
				i++ // skip the '/'
				continue
			}
			// Track potential `/*` opening a nested block (Go doesn't
			// allow nesting, but we behave defensively — see comment
			// at the top of countGoSourceLOC).
			if i+1 < len(line) && line[i] == '/' && line[i+1] == '*' {
				*nestedCnt++
				i++
				continue
			}
			continue
		}
		// Not in a block — check for `//` (rest-of-line) and `/*` (open block).
		if i+1 < len(line) && line[i] == '/' && line[i+1] == '/' {
			// Discard rest of line.
			break
		}
		if i+1 < len(line) && line[i] == '/' && line[i+1] == '*' {
			inBlock = true
			i++
			continue
		}
		b.WriteByte(line[i])
	}
	return b.String(), inBlock
}
