package ledgerfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

const testReport = `{"schema":"forge.test.report/v1","ok":true,"exit_code":0,"runs":[],"totals":{"passed":3}}`

func TestTestReportRoundTripIsContentAddressed(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	digest, err := s.PutTestReport([]byte(testReport))
	if err != nil {
		t.Fatal(err)
	}
	if digest != sbomDigest([]byte(testReport)) {
		t.Fatalf("PutTestReport digest = %q", digest)
	}
	path := filepath.Join(s.Dir(), "test-reports", "sha256-"+strings.TrimPrefix(digest, "sha256:")+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("report not at its content address: %v", err)
	}
	got, err := s.GetTestReport(digest)
	if err != nil || string(got) != testReport {
		t.Fatalf("GetTestReport = %q, %v", got, err)
	}
	if again, err := s.PutTestReport([]byte(testReport)); err != nil || again != digest {
		t.Fatalf("second PutTestReport = %q, %v", again, err)
	}
	assertNoTempFiles(t, filepath.Dir(path))

	if err := os.WriteFile(path, []byte(`{"tampered":true}`), fileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTestReport(digest); !errors.Is(err, ErrTestReportCorrupt) {
		t.Fatalf("tampered report: %v", err)
	}
	if _, err := s.GetTestReport("sha256:" + strings.Repeat("0", 64)); !errors.Is(err, ErrTestReportNotFound) {
		t.Fatalf("missing report: %v", err)
	}
	for _, bad := range []string{"", "not json"} {
		if _, err := s.PutTestReport([]byte(bad)); !errors.Is(err, release.ErrInvalid) {
			t.Fatalf("PutTestReport(%q) = %v", bad, err)
		}
	}
}

func testsRef(c, status string, lanes ...string) release.TestsRef {
	return release.TestsRef{ReportDigest: "sha256:" + strings.Repeat(c, 64), Lanes: lanes, Status: status, Totals: &release.TestsTotals{Passed: 1}}
}

func TestRecordReleaseTests(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if _, err := s.CutRelease(ociRelease("v1.0.0")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordReleaseTests("v9", testsRef("c", release.TestsStatusOK, "unit")); !errors.Is(err, ErrReleaseNotFound) {
		t.Fatalf("unknown version: %v", err)
	}
	if _, err := s.RecordReleaseTests("v1.0.0", release.TestsRef{Status: release.TestsStatusOK}); !errors.Is(err, release.ErrInvalid) {
		t.Fatalf("invalid ref: %v", err)
	}
	if recorded, err := s.RecordReleaseTests("v1.0.0", testsRef("c", release.TestsStatusPartial, "unit")); err != nil || !recorded {
		t.Fatalf("backfill: %v, %v", recorded, err)
	}
	// More lanes of the same commit supersede a partial report...
	if recorded, err := s.RecordReleaseTests("v1.0.0", testsRef("d", release.TestsStatusOK, "integration", "unit")); err != nil || !recorded {
		t.Fatalf("grow: %v, %v", recorded, err)
	}
	// ...and an ok one is never replaced.
	if recorded, err := s.RecordReleaseTests("v1.0.0", testsRef("e", release.TestsStatusOK, "e2e", "integration", "unit")); err != nil || recorded {
		t.Fatalf("replace: recorded=%v err=%v (want kept)", recorded, err)
	}
	got, _ := s.Release("v1.0.0")
	if got.Tests == nil || got.Tests.ReportDigest != testsRef("d", "").ReportDigest {
		t.Fatalf("Tests = %+v", got.Tests)
	}
	if n := releaseLines(t, s); n != 3 {
		t.Fatalf("%d release lines, want 3 (the cut and two fills)", n)
	}
}

// A re-cut that knows test evidence the record lacks backfills it, exactly as
// it backfills an SBOM.
func TestCutReleaseBackfillsTests(t *testing.T) {
	t.Parallel()
	s := openTest(t)
	if _, err := s.CutRelease(ociRelease("v1.0.0")); err != nil {
		t.Fatal(err)
	}
	r := ociRelease("v1.0.0")
	ref := testsRef("c", release.TestsStatusOK, "unit")
	r.Tests = &ref
	if created, err := s.CutRelease(r); err != nil || created {
		t.Fatalf("re-cut with tests: created=%v err=%v (want a retry)", created, err)
	}
	got, _ := s.Release("v1.0.0")
	if got.Tests == nil || got.Tests.ReportDigest != ref.ReportDigest {
		t.Fatalf("tests not backfilled: %+v", got.Tests)
	}
	if _, err := s.CutRelease(ociRelease("v1.0.0")); err != nil {
		t.Fatal(err)
	}
	if n := releaseLines(t, s); n != 2 {
		t.Fatalf("%d release lines, want 2", n)
	}
}
