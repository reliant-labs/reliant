// Copyright (c) 2025 Reliant Labs
package provetest

import (
	"regexp"
	"strings"
)

// Marker is an output line that identifies what kind of failure a run was.
type Marker struct {
	Kind string
	Line string
}

// compileMarkers recognise a run that failed to BUILD or LOAD the test, as
// opposed to running it and failing an assertion. A fix that adds a symbol
// the test calls makes the baseline fail exactly this way, and counting that
// as proof would certify a test that never ran against the old behaviour.
//
// These are heuristics over runner output and are reported as such — the
// matched line is shown so the reader can overrule it. They are deliberately
// specific: a pattern broad enough to catch every compiler would also catch
// a test that asserts on an error message.
var compileMarkers = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"Go build failure", regexp.MustCompile(`\[build failed\]|\[setup failed\]`)},
	{"Go compile error", regexp.MustCompile(`\.go:\d+:\d+: (undefined: |undefined \(type|cannot use |too many arguments|not enough arguments|missing return|declared and not used|"[^"]+" imported and not used|syntax error)`)},
	{"TypeScript compile error", regexp.MustCompile(`error TS\d+:`)},
	{"module failed to load", regexp.MustCompile(`Failed to resolve import|Cannot find module|Failed to load url|does not provide an export named|is not exported by|ERR_MODULE_NOT_FOUND|Test suite failed to run|Transform failed`)},
	{"Python import failure", regexp.MustCompile(`ModuleNotFoundError|ImportError: cannot import name|ERROR collecting|errors? during collection`)},
	{"Rust compile error", regexp.MustCompile(`error\[E\d{4}\]|could not compile`)},
	{"Java/Kotlin compile error", regexp.MustCompile(`COMPILATION ERROR|Compilation failed|error: cannot find symbol`)},
	{"C# compile error", regexp.MustCompile(`error CS\d{4}:`)},
}

// noTestsMarkers recognise a run that executed no test at all — usually a
// filter that matched nothing, which exits 0 under several runners and would
// otherwise read as "passes without the fix".
var noTestsMarkers = regexp.MustCompile(`no tests to run|No test files found|no tests ran|No tests found|Ran 0 tests`)

func findMarker(output string, re *regexp.Regexp) string {
	for _, line := range strings.Split(output, "\n") {
		if re.MatchString(line) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func findCompileMarker(output string) *Marker {
	for _, m := range compileMarkers {
		if line := findMarker(output, m.re); line != "" {
			return &Marker{Kind: m.kind, Line: line}
		}
	}
	return nil
}

// classify sets the verdict, and the hints that go with it, from the runs and
// the restore. Order matters: a restore problem outranks every run result,
// because it means the working tree is not what the caller left.
func classify(r *Report) {
	before, after := r.Before, r.After
	switch {
	case hasRestoreOutcome(r.Restored, RestoreConflict):
		r.Verdict = VerdictConflict
	case hasRestoreOutcome(r.Restored, RestoreFailed):
		r.Verdict = VerdictRestoreFailed
	case !before.completed():
		r.Verdict = VerdictInconclusive
		if before != nil && before.TimedOut {
			r.Hints = append(r.Hints,
				"The before-run timed out. If the fix is for a hang or deadlock, put a timeout in the command itself (e.g. `go test -timeout 30s`) so the hang becomes an ordinary test failure; otherwise raise `timeout`.")
		}
	case after == nil:
		r.Verdict = VerdictInconclusive
	case after.TimedOut || (after.Err == nil && after.ExitCode != 0):
		r.Verdict = VerdictFailsWithFix
		if m := findCompileMarker(after.Output); m != nil {
			r.Hints = append(r.Hints, "The after-run failed to build ("+m.Kind+": "+m.Line+").")
		}
	case after.Err != nil:
		r.Verdict = VerdictInconclusive
	case before.ExitCode == 0:
		r.Verdict = VerdictPassesWithoutFix
	default:
		if m := findCompileMarker(before.Output); m != nil {
			r.CompileFailure = m
			r.Verdict = VerdictBaselineDoesNotCompile
		} else {
			r.Verdict = VerdictProven
		}
	}

	if before.completed() && before.ExitCode == 0 {
		if line := findMarker(before.Output, noTestsMarkers); line != "" {
			r.Hints = append(r.Hints, "The before-run executed no tests ("+line+"). Check the command's test filter, and that the test is not in a file listed in `files`.")
		}
	}
	if after.completed() {
		if line := findMarker(after.Output, noTestsMarkers); line != "" {
			r.Hints = append(r.Hints, "The after-run executed no tests ("+line+"). Check the command's test filter.")
		}
	}
}

func hasRestoreOutcome(results []RestoreResult, want RestoreOutcome) bool {
	for _, r := range results {
		if r.Outcome == want {
			return true
		}
	}
	return false
}
