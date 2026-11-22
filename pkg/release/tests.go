package release

import (
	"fmt"
	"math"
	"sort"
)

// TestsRef is a release's test evidence: what the test report of the commit
// it was cut from said. It is EVIDENCE about the release, never part of its
// identity: [Release.SameContent] ignores it, so a re-cut of the same digests
// that knows more about the commit's tests is the same release.
//
// The report itself is not here. ReportDigest names it, and it lives where
// the release's ledger keeps it: the machine ledger's content-addressed store
// of test reports, or the control plane's commit report. Every reader
// verifies the bytes against the digest.
//
// Status is an OPEN vocabulary on read: the constants below are what this
// forge writes, and a reader tolerates a value a newer forge adds rather
// than refusing the release that carries it (the rule [SBOMRef] follows, for
// the same reason: a ledger line nobody can read fails every command that
// reads the ledger).
type TestsRef struct {
	// Commit is the commit the report is of: the release's own commit.
	Commit string `json:"commit,omitempty"`
	// ReportDigest is the sha256 of the forge.test.report/v1 document.
	ReportDigest string `json:"report_digest,omitempty"`
	// Lanes are the lanes the report merged, sorted ("unit", "integration").
	Lanes []string `json:"lanes,omitempty"`
	// Totals are the report's test cases. Nil when there is no report.
	Totals *TestsTotals `json:"totals,omitempty"`
	// Coverage is the report's coverage, in percent. Nil when there is no
	// report, or its runs carried no coverage.
	Coverage *TestsCoverage `json:"coverage,omitempty"`
	Status   string         `json:"status,omitempty"` // ok | partial | unavailable
	// Reason says why the status is not ok: which lanes are missing, or
	// why there is no report.
	Reason string `json:"reason,omitempty"`
}

// TestsTotals counts a report's test cases.
type TestsTotals struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// TestsCoverage is a report's coverage figures, in percent.
type TestsCoverage struct {
	// Combined counts a statement covered if any lane covered it.
	Combined float64 `json:"combined"`
	// ByLane is each lane's own figure.
	ByLane map[string]float64 `json:"by_lane,omitempty"`
}

// TestsRef.Status values.
const (
	// TestsStatusOK is a report with every lane the project records.
	TestsStatusOK = "ok"
	// TestsStatusPartial is a report missing some of those lanes; Reason
	// names them. More lanes may arrive for the commit later.
	TestsStatusPartial = "partial"
	// TestsStatusUnavailable records that there is no report for the
	// release's commit, and Reason says why.
	TestsStatusUnavailable = "unavailable"
)

// HasReport reports whether the ref names a stored report. An
// [TestsStatusUnavailable] ref, which records only that there is none, does
// not.
func (t *TestsRef) HasReport() bool { return t != nil && t.ReportDigest != "" }

// validate checks the invariants every value of the vocabulary shares. It is
// deliberately silent on Status values it does not know (see the type doc).
func (t *TestsRef) validate() error {
	if t == nil {
		return nil
	}
	if t.ReportDigest != "" && !ValidDigest(t.ReportDigest) {
		return fmt.Errorf("tests report digest %q is not a canonical sha256 digest", t.ReportDigest)
	}
	if (t.Status == TestsStatusOK || t.Status == TestsStatusPartial) && t.ReportDigest == "" {
		return fmt.Errorf("tests status %q needs a report digest", t.Status)
	}
	if t.Totals != nil && (t.Totals.Passed < 0 || t.Totals.Failed < 0 || t.Totals.Skipped < 0) {
		return fmt.Errorf("tests totals cannot be negative")
	}
	if c := t.Coverage; c != nil {
		if !validPercent(c.Combined) {
			return fmt.Errorf("tests coverage %v is not a percentage", c.Combined)
		}
		for lane, v := range c.ByLane {
			if !validPercent(v) {
				return fmt.Errorf("tests coverage of lane %q (%v) is not a percentage", lane, v)
			}
		}
	}
	return nil
}

func validPercent(v float64) bool { return !math.IsNaN(v) && v >= 0 && v <= 100 }

// grows reports whether next is more of the same report than t: both partial
// reports of one commit, next with every lane t has and at least one more.
// That is the one way a recorded report is superseded — "and later as more
// lanes arrive" — and it never loses a lane already recorded.
func (t *TestsRef) grows(next *TestsRef) bool {
	if t.Status != TestsStatusPartial || !next.HasReport() || next.Commit != t.Commit {
		return false
	}
	have := make(map[string]bool, len(next.Lanes))
	for _, l := range next.Lanes {
		have[l] = true
	}
	for _, l := range t.Lanes {
		if !have[l] {
			return false
		}
	}
	return len(next.Lanes) > len(t.Lanes)
}

// BackfillTests returns existing with its test evidence taken from requested
// where that adds to what existing records, and whether anything was filled.
// It is the half of a re-cut, or of a later report, that is not identity:
// when [Release.SameContent] holds, the newer cut may know a report the
// record lacks (a release cut before forge recorded tests, or before the
// commit's runs were recorded).
//
// It NEVER replaces a report the record already names, with one exception: a
// [TestsStatusPartial] report of the commit is replaced by one with every
// lane it had and more. A [TestsStatusOK] report stands: it is what every
// verdict since was computed against. A ref that only says
// [TestsStatusUnavailable] names no report, so a later ref that names one
// fills it; two that both say "none" keep the first reason.
//
// existing is not modified.
func BackfillTests(existing, requested Release) (Release, bool) {
	cur, next := existing.Tests, requested.Tests
	switch {
	case next == nil:
		return existing, false
	case cur == nil:
	case !cur.HasReport() && next.HasReport():
	case cur.HasReport() && cur.grows(next):
	default:
		return existing, false
	}
	out := existing
	out.Tests = next.clone()
	return out, true
}

func (t *TestsRef) clone() *TestsRef {
	if t == nil {
		return nil
	}
	c := *t
	c.Lanes = append([]string(nil), t.Lanes...)
	sort.Strings(c.Lanes)
	if t.Totals != nil {
		tt := *t.Totals
		c.Totals = &tt
	}
	if t.Coverage != nil {
		cov := TestsCoverage{Combined: t.Coverage.Combined}
		if t.Coverage.ByLane != nil {
			cov.ByLane = make(map[string]float64, len(t.Coverage.ByLane))
			for k, v := range t.Coverage.ByLane {
				cov.ByLane[k] = v
			}
		}
		c.Coverage = &cov
	}
	return &c
}
