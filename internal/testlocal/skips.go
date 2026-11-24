package testlocal

import (
	"errors"
	"fmt"

	skipcheck "github.com/reliant-labs/forge/internal/testreport"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// SkipAnalysis applies `forge ci verify-test-run`'s rules (zero-evidence,
// mass-skip, failures, undetermined input) to every go-json lane of the
// assembly, one lane at a time, and folds the verdicts into one Analysis:
// what `forge test report --gate-json` writes, in verify-test-run's gate
// shape.
//
// Lanes of other formats (JUnit, Vitest) are not skip-checked, as
// verify-test-run never read them, but their tallies and failures count, so
// the gate is red when any lane failed. A lane whose facts could not be
// obtained is undetermined here as it is in the report.
func (a *Assembly) SkipAnalysis(policy skipcheck.Policy) skipcheck.Analysis {
	var parts []skipcheck.Analysis
	for _, s := range a.LaneStreams() {
		parts = append(parts, analyzeStream(s, policy))
	}
	out := combineAnalyses(parts, policy)
	for i, l := range a.Lanes {
		if l.Format == testreport.LaneFormatGoJSON || a.Results[i] == nil {
			continue
		}
		addNonGo(&out, a.Results[i])
	}
	for _, u := range a.Undetermined {
		out.Undetermined = append(out.Undetermined, skipcheck.Unknown{Reason: u})
	}
	if len(a.Lanes) == 0 {
		out.Undetermined = append(out.Undetermined, skipcheck.Unknown{Reason: "the runs captured no lane"})
	}
	if out.Totals.Tests > 0 {
		out.Totals.SkipRatio = float64(out.Totals.Skipped) / float64(out.Totals.Tests)
	}
	return out
}

func analyzeStream(s LaneStream, policy skipcheck.Policy) skipcheck.Analysis {
	f, err := s.Open()
	if err != nil {
		return skipcheck.Analysis{Policy: policy, Undetermined: []skipcheck.Unknown{{Reason: fmt.Sprintf("lane %s: %v", s.Lane, err)}}}
	}
	defer func() { _ = f.Close() }()
	run, err := skipcheck.Parse(f)
	if err != nil {
		reason := err.Error()
		if errors.Is(err, skipcheck.ErrNoInput) {
			reason = "the results file is empty"
		}
		return skipcheck.Analysis{Policy: policy, Undetermined: []skipcheck.Unknown{{Reason: fmt.Sprintf("lane %s: %s", s.Lane, reason)}}}
	}
	return skipcheck.Analyze(run, policy)
}

// combineAnalyses folds per-lane verdicts. Findings, suppressions and
// unknowns are concatenated; totals summed. An exemption is stale only if it
// was stale in every lane, since another lane may be what it is for.
func combineAnalyses(parts []skipcheck.Analysis, policy skipcheck.Policy) skipcheck.Analysis {
	out := skipcheck.Analysis{Policy: policy}
	staleCount := map[string]int{}
	staleBy := map[string]skipcheck.Exemption{}
	for _, p := range parts {
		out.Findings = append(out.Findings, p.Findings...)
		out.Suppressed = append(out.Suppressed, p.Suppressed...)
		out.Undetermined = append(out.Undetermined, p.Undetermined...)
		out.Malformed += p.Malformed
		t := &out.Totals
		t.Packages += p.Totals.Packages
		t.NoTestFiles += p.Totals.NoTestFiles
		t.NoTestsRan += p.Totals.NoTestsRan
		t.Tests += p.Totals.Tests
		t.Passed += p.Totals.Passed
		t.Failed += p.Totals.Failed
		t.Skipped += p.Totals.Skipped
		t.FailedPackages += p.Totals.FailedPackages
		t.SuiteFailed = t.SuiteFailed || p.Totals.SuiteFailed
		for _, e := range p.Stale {
			staleCount[e.Package]++
			staleBy[e.Package] = e
		}
	}
	for pkg, n := range staleCount {
		if n == len(parts) {
			out.Stale = append(out.Stale, staleBy[pkg])
		}
	}
	return out
}

func addNonGo(a *skipcheck.Analysis, r *testreport.Results) {
	t := &a.Totals
	tt := r.Totals
	t.Tests += tt.Passed + tt.Failed + tt.Skipped
	t.Passed += tt.Passed
	t.Failed += tt.Failed
	t.Skipped += tt.Skipped
	for _, p := range r.Packages {
		if p.Tests > 0 {
			t.Packages++
		}
		if p.Status == testreport.StatusFail || p.BuildFailed {
			t.FailedPackages++
			t.SuiteFailed = true
		}
	}
	if tt.Failed > 0 {
		t.SuiteFailed = true
	}
}

// ZeroEvidence lists the packages every one of whose tests skipped: the
// verify-test-run rule that makes a report undetermined (exit 2), since
// their pass says nothing.
func ZeroEvidence(an skipcheck.Analysis) []string {
	var out []string
	for _, f := range an.Findings {
		if f.Kind == skipcheck.KindZeroEvidence {
			out = append(out, f.Package)
		}
	}
	return out
}
