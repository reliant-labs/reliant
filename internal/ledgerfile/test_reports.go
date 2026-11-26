package ledgerfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/reliant-labs/forge/pkg/release"
)

// The test-report store: the forge.test.report/v1 documents a release's
// TestsRef names, content-addressed exactly as the SBOM store is.
//
//	<ledger>/<project-id>/
//	  test-reports/sha256-<hex>.json
//
// The runs a report is computed from live in a checkout (.forge/tests/), and
// a machine ledger is shared by every worktree of the project, so the report
// a release names is kept HERE: any worktree can read the evidence a release
// was cut with. Written whole and renamed into place, re-hashed on every
// read; it needs no lock, for the reasons the SBOM store gives.
const (
	testReportsDir    = "test-reports"
	testReportsSuffix = ".json"
)

// ErrTestReportNotFound is GetTestReport naming a digest this ledger holds no
// report for.
var ErrTestReportNotFound = errors.New("test report not found in the machine ledger")

// ErrTestReportCorrupt is a stored report whose bytes no longer hash to its
// name.
var ErrTestReportCorrupt = errors.New("test report in the machine ledger does not match its digest")

// PutTestReport stores a test report's bytes under their sha256 digest and
// returns it — the value a release's release.TestsRef.ReportDigest records.
// Storing bytes the store already holds is a no-op.
func (s *Store) PutTestReport(data []byte) (digest string, err error) {
	if len(data) == 0 {
		return "", fmt.Errorf("%w: test report is empty", release.ErrInvalid)
	}
	if !json.Valid(data) {
		return "", fmt.Errorf("%w: test report is not JSON", release.ErrInvalid)
	}
	digest = sbomDigest(data)
	stem, err := sbomFileStem(digest)
	if err != nil {
		return "", err
	}
	path := s.path(testReportsDir, stem+testReportsSuffix)
	if existing, err := os.ReadFile(path); err == nil && sbomDigest(existing) == digest { //nolint:gosec // path comes from Store.path
		return digest, nil
	}
	if err := writeFileAtomic(path, data); err != nil {
		return "", err
	}
	return digest, nil
}

// GetTestReport returns the report stored under digest, verified:
// ErrTestReportNotFound when there is none, ErrTestReportCorrupt when its
// bytes no longer hash to it.
func (s *Store) GetTestReport(digest string) ([]byte, error) {
	stem, err := sbomFileStem(digest)
	if err != nil {
		return nil, err
	}
	path := s.path(testReportsDir, stem+testReportsSuffix)
	data, err := os.ReadFile(path) //nolint:gosec // path comes from Store.path
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: %w", digest, ErrTestReportNotFound)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if got := sbomDigest(data); got != digest {
		return nil, fmt.Errorf("%s holds %s: %w", path, got, ErrTestReportCorrupt)
	}
	return data, nil
}

// RecordReleaseTests records ref as the test evidence of a release already
// cut — the backfill for a release cut before its commit's runs were
// recorded, or before forge recorded tests at all. recorded is false when
// the release already holds evidence ref does not add to (release.BackfillTests
// has the rule: a report is never replaced, except a partial one by one with
// more lanes). Not a conflict either way: test evidence is not identity.
//
// Like RecordReleaseSBOM it APPENDS the release again, and the newest line
// for a version is the one releasesLocked keeps.
func (s *Store) RecordReleaseTests(version string, ref release.TestsRef) (recorded bool, err error) {
	err = s.withLock(func() error {
		all, err := s.releasesLocked()
		if err != nil {
			return err
		}
		for _, existing := range all {
			if existing.Version != version {
				continue
			}
			requested := existing
			requested.Tests = &ref
			filled, changed := release.BackfillTests(existing, requested)
			if !changed {
				return nil
			}
			if err := filled.Validate(); err != nil {
				return err
			}
			recorded = true
			return appendRecord(s.path(releasesFile), filled)
		}
		return fmt.Errorf("release %q: %w", version, ErrReleaseNotFound)
	})
	if err != nil {
		return false, err
	}
	return recorded, nil
}
