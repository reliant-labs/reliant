// File: internal/cli/lint/lint_test_waits.go
//
// test-waits — `forge lint --test-waits`, and a step of every `forge lint`.
//
// A Go test that waits on the wall clock where it should wait on an event:
// a fixed time.Sleep before an assertion, an event wait that fails the test
// after a deadline under 5s, an upper bound asserted on real elapsed time.
// Measured across forge, reliant and control-plane, that shape was ~40% of
// the flakes a loaded shared machine produced. The analyzer, what it flags
// and what it deliberately does not live in internal/linter/testwait; the
// fix it points to is forge/pkg/testkit's Eventually and Receive.
//
// It is syntactic — a parse per _test.go file, no package load — so it runs
// in the full sweep, unlike --slow-tests, whose call-graph analysis is
// opt-in for cost. testdata/, vendor/, gen/, node_modules/ and hidden
// directories are skipped, as the go-markers lane skips them.
//
// GATES on its findings, through the warn-first rollout: a project pinned
// below the release after the one that ships it sees warnings
// (lint_rollout_table.go). A deliberate wait is silenced at its line, with
// the reason:
//
//	// forge:lint-disable-next-line forge-test-fixed-sleep: asserts nothing is sent within 100ms

package lint

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
	"github.com/reliant-labs/forge/internal/linter/testwait"
)

// collectTestWaitFindings walks root for _test.go files and returns the
// analyzer's findings, suppression directives applied, sorted by (file,
// line).
func collectTestWaitFindings(root string) ([]finding.Finding, error) {
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
		if !strings.HasSuffix(d.Name(), "_test.go") {
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
		raw = append(raw, testwait.CheckFile(filepath.ToSlash(rel), src)...)
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

// collectTestWaitsJSON maps the findings onto the JSON contract; the bool is
// the gating verdict.
func collectTestWaitsJSON(root string) ([]lintJSONFinding, bool, error) {
	findings, err := collectTestWaitFindings(root)
	if err != nil {
		return nil, false, fmt.Errorf("test-waits lint failed: %w", err)
	}
	return findingsToJSON(findings), countErrors(findings) > 0, nil
}
