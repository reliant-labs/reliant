// File: internal/cli/lint/lint_caller_paths.go
//
// caller-paths — `forge lint --caller-paths`, and a step of every `forge lint`.
//
// Go code that builds a filesystem path from the source file runtime.Caller
// reports: `_, file, _, _ := runtime.Caller(0)`, then filepath.Dir(file) to
// find testdata or the repository root. -trimpath is forge's default build
// mode, and under it that file is a module-relative path
// ("example.com/app/internal/x/x_test.go") rather than one on disk, so the
// test passes in an untrimmed run and fails in a trimmed one. The analyzer,
// what it flags and what it deliberately does not live in
// internal/linter/callerpath.
//
// It is its own lane, not a rule of --test-waits or --slow-tests: those are
// about how long a test takes, this is about where code looks for files, and
// it reads every .go file rather than only _test.go ones. It is syntactic —
// a parse per file that mentions runtime.Caller, no package load — so it runs
// in the full sweep. testdata/, vendor/, gen/, node_modules/ and hidden
// directories are skipped, as the go-markers lane skips them.
//
// GATES on its findings, through the warn-first rollout: a project pinned
// below the release after the one that ships it sees warnings
// (lint_rollout_table.go). A deliberate use is silenced at its line, with
// the reason:
//
//	// forge:lint-disable-next-line forge-runtime-caller-path: <why>

package lint

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/callerpath"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// collectCallerPathFindings walks root for .go files and returns the
// analyzer's findings, suppression directives applied, sorted by (file,
// line).
func collectCallerPathFindings(root string) ([]finding.Finding, error) {
	var raw []finding.Finding
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipGoMarkerDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		raw = append(raw, callerpath.CheckFile(filepath.ToSlash(rel), src)...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	res := suppress.ApplyFiles(root, raw, os.ReadFile)
	out := slices.Concat(res.Kept, res.Violations)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// collectCallerPathsJSON maps the findings onto the JSON contract; the bool
// is the gating verdict.
func collectCallerPathsJSON(root string) ([]lintJSONFinding, bool, error) {
	findings, err := collectCallerPathFindings(root)
	if err != nil {
		return nil, false, fmt.Errorf("caller-paths lint failed: %w", err)
	}
	return findingsToJSON(findings), countErrors(findings) > 0, nil
}
