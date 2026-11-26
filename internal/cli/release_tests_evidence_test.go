package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/testlocal"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// These tests read run directories written into a temp dir, with git
// answered by a fake: nothing execs git or go.

const evOther = "1111111111111111111111111111111111111111"

var evAt = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// evProject is a checkout with one Go file; its runs are added by evRun.
func evProject(t *testing.T) (string, testlocal.Git) {
	t.Helper()
	root := t.TempDir()
	src := "package a\n\nfunc F() int {\n\treturn 1\n}\n"
	trWrite(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.24\n")
	trWrite(t, filepath.Join(root, "a/a.go"), src)
	blob := testreport.GitBlobID([]byte(src))
	git := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case args[0] == "rev-parse" && args[1] == "--show-toplevel":
			return []byte(root + "\n"), nil
		case args[0] == "ls-tree":
			return []byte("100644 blob " + blob + "\ta/a.go\x00"), nil
		}
		return nil, fmt.Errorf("fake git: no %v", args)
	}
	return root, git
}

// evRun records a run of commit with one passing test per lane.
func evRun(t *testing.T, root, id, commit string, at time.Time, dirty bool, lanes ...string) {
	t.Helper()
	run := filepath.Join(testreport.RunsRoot(root), id)
	trJSON(t, filepath.Join(run, testreport.RunManifestFile), testreport.RunManifest{
		Schema: testreport.RunManifestSchema, ID: id, CreatedAt: at, Commit: commit, Dirty: dirty, Project: "shop", Lanes: lanes,
	})
	for _, lane := range lanes {
		trWrite(t, filepath.Join(run, lane, "results.json"), trStream("pass"))
		trWrite(t, filepath.Join(run, lane, "cover.out"), "mode: atomic\nexample.com/m/a/a.go:3.14,5.2 1 1\n")
		trJSON(t, filepath.Join(run, lane, testreport.LaneManifestFile), testreport.LaneManifest{
			Schema: testreport.LaneManifestSchema, Lane: lane, StartedAt: at, FinishedAt: at,
			Format: testreport.LaneFormatGoJSON, Results: testreport.LaneResultsParsed, ResultsFile: "results.json",
			Coverage: []testreport.LaneCoverageInput{{Path: "cover.out", Format: testreport.CoverageGoProfile}},
		})
	}
}

// evStore is a put that keeps what it was given.
type evStore struct{ reports map[string][]byte }

func (s *evStore) put(b []byte) (string, error) {
	if s.reports == nil {
		s.reports = map[string][]byte{}
	}
	d := "sha256:" + strings.Repeat(fmt.Sprintf("%x", len(s.reports)%16), 64)
	s.reports[d] = b
	return d, nil
}

func TestReleaseTestsRefUnavailable(t *testing.T) {
	t.Parallel()
	root, git := evProject(t)
	var store evStore
	for _, tc := range []struct {
		name   string
		commit string
		dirty  bool
		put    func([]byte) (string, error)
		want   string
	}{
		{"no commit", "", false, store.put, "no commit"},
		{"dirty tree", trCommit, true, store.put, "dirty tree"},
		{"no runs", trCommit, false, store.put, "no clean-tree test run of commit " + trCommit[:12]},
	} {
		ref := releaseTestsRef(context.Background(), releaseTestsInput{Root: root, Commit: tc.commit, Dirty: tc.dirty, At: evAt, Git: git, Put: tc.put})
		if ref.Status != release.TestsStatusUnavailable || !strings.Contains(ref.Reason, tc.want) || ref.HasReport() {
			t.Errorf("%s: %+v", tc.name, ref)
		}
		if err := (release.Release{Version: "v1", Git: release.Git{Commit: tc.commit}, RecordOnly: true, Tests: &ref}).Validate(); err != nil {
			t.Errorf("%s: the ref is not valid on a release: %v", tc.name, err)
		}
	}

	// A clean run with no commit, or of a longer commit that merely starts
	// with the release's, proves nothing about the release's commit.
	evRun(t, root, "019", "", evAt, false, "unit")
	evRun(t, root, "01Z", trCommit+"ff", evAt, false, "unit")
	ref := releaseTestsRef(context.Background(), releaseTestsInput{Root: root, Commit: trCommit, At: evAt, Git: git, Put: store.put})
	if ref.Status != release.TestsStatusUnavailable || ref.HasReport() || !strings.Contains(ref.Reason, "no clean-tree test run") {
		t.Fatalf("a commit-less or other-commit run joined the evidence: %+v", ref)
	}

	evRun(t, root, "01A", trCommit, evAt, false, "unit")
	failing := func([]byte) (string, error) { return "", errors.New("disk full") }
	ref = releaseTestsRef(context.Background(), releaseTestsInput{Root: root, Commit: trCommit, At: evAt, Git: git, Put: failing})
	if ref.Status != release.TestsStatusUnavailable || !strings.Contains(ref.Reason, "disk full") {
		t.Fatalf("a report that cannot be stored: %+v", ref)
	}
}

func TestReleaseTestsRefOKAndPartial(t *testing.T) {
	t.Parallel()
	root, git := evProject(t)
	evRun(t, root, "01A", trCommit, evAt.Add(-time.Hour), false, "unit")
	// Lanes the project never runs regularly are not expected...
	evRun(t, root, "01B", evOther, evAt.Add(-2*time.Hour), false, "confirm", "flake-gate", "unit")
	// ...nor are lanes last seen before the window, on a dirty tree, or
	// after the release was cut.
	evRun(t, root, "01C", evOther, evAt.Add(-31*24*time.Hour), false, "e2e")
	evRun(t, root, "01D", evOther, evAt.Add(-time.Hour), true, "web")
	evRun(t, root, "01E", evOther, evAt.Add(time.Hour), false, "later")

	var store evStore
	in := releaseTestsInput{Root: root, Commit: trCommit, At: evAt, Git: git, Put: store.put}
	ref := releaseTestsRef(context.Background(), in)
	if ref.Status != release.TestsStatusOK || ref.Reason != "" || ref.Commit != trCommit {
		t.Fatalf("ok: %+v", ref)
	}
	if strings.Join(ref.Lanes, ",") != "unit" || *ref.Totals != (release.TestsTotals{Passed: 1}) ||
		ref.Coverage == nil || ref.Coverage.Combined != 100 || ref.Coverage.ByLane["unit"] != 100 {
		t.Fatalf("figures: %+v %+v %+v", ref, ref.Totals, ref.Coverage)
	}
	var rep testreport.Report
	if err := json.Unmarshal(store.reports[ref.ReportDigest], &rep); err != nil || rep.Schema != testreport.SchemaReport || rep.Commit != trCommit {
		t.Fatalf("stored report: %v %s", err, store.reports[ref.ReportDigest])
	}

	// A regular lane recorded on another commit, and missing here.
	evRun(t, root, "01F", evOther, evAt.Add(-3*time.Hour), false, "integration", "unit")
	ref = releaseTestsRef(context.Background(), in)
	if ref.Status != release.TestsStatusPartial || !strings.Contains(ref.Reason, "no integration lane") || !ref.HasReport() {
		t.Fatalf("partial: %+v", ref)
	}
	if err := (release.Release{Version: "v1", Git: release.Git{Commit: trCommit}, RecordOnly: true, Tests: &ref}).Validate(); err != nil {
		t.Fatalf("the ref is not valid on a release: %v", err)
	}
}

// fakeHostedReleases is a release ledger forge does not fill.
type fakeHostedReleases struct{ releaseLedger }

func TestCaptureReleaseTestsOnlyOnTheMachineLedger(t *testing.T) {
	t.Parallel()
	root, _ := evProject(t)
	rel := release.Release{Version: "v1.0.0", Git: release.Git{Commit: trCommit}, CreatedAt: evAt}
	if got := captureReleaseTests(context.Background(), root, rel, fakeHostedReleases{}, &bytes.Buffer{}); got != nil {
		t.Fatalf("a hosted ledger got test evidence from forge: %+v", got)
	}
	store, err := ledgerfile.Open(t.TempDir(), "shop-abc")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	got := captureReleaseTests(context.Background(), root, rel, machineReleaseLedger{store: store}, &out)
	if got == nil || got.Status != release.TestsStatusUnavailable || !strings.Contains(out.String(), "[build] tests: unavailable") {
		t.Fatalf("machine ledger: %+v %q", got, out.String())
	}
}

func TestBackfillReleaseTests(t *testing.T) {
	t.Parallel()
	root, git := evProject(t)
	store, err := ledgerfile.Open(t.TempDir(), "shop-abc")
	if err != nil {
		t.Fatal(err)
	}
	cut := func(version, commit string, tests *release.TestsRef) {
		t.Helper()
		r := release.Release{Version: version, Git: release.Git{Commit: commit}, CreatedAt: evAt, RecordOnly: true, Tests: tests}
		if _, err := store.CutRelease(r); err != nil {
			t.Fatal(err)
		}
	}
	cut("v1.0.0", trCommit, &release.TestsRef{Commit: trCommit, Status: release.TestsStatusUnavailable, Reason: "no runs yet"})
	cut("v0.9.0", evOther, nil)

	evRun(t, root, "01A", trCommit, evAt.Add(-time.Hour), false, "unit")
	evRun(t, root, "01B", evOther, evAt.Add(-2*time.Hour), false, "integration", "unit")
	backfill := func() string {
		var out bytes.Buffer
		backfillReleaseTestsIn(context.Background(), root, trCommit, machineReleaseLedger{store: store}, store.Releases, git, &out)
		return out.String()
	}

	if out := backfill(); !strings.Contains(out, "on release v1.0.0: partial") {
		t.Fatalf("first backfill: %q", out)
	}
	got, _ := store.Release("v1.0.0")
	if got.Tests.Status != release.TestsStatusPartial {
		t.Fatalf("Tests = %+v", got.Tests)
	}
	if _, err := store.GetTestReport(got.Tests.ReportDigest); err != nil {
		t.Fatalf("the report the release names is not in the ledger: %v", err)
	}
	if other, _ := store.Release("v0.9.0"); other.Tests != nil {
		t.Fatalf("another commit's release was filled: %+v", other.Tests)
	}

	// The commit gains its integration lane: the partial report grows.
	evRun(t, root, "01C", trCommit, evAt.Add(-30*time.Minute), false, "integration")
	if out := backfill(); !strings.Contains(out, "on release v1.0.0: ok") {
		t.Fatalf("second backfill: %q", out)
	}
	// An ok report is never replaced: nothing is said, nothing is written.
	evRun(t, root, "01D", trCommit, evAt.Add(-20*time.Minute), false, "e2e")
	if out := backfill(); out != "" {
		t.Fatalf("an ok report was revisited: %q", out)
	}
	got, _ = store.Release("v1.0.0")
	if got.Tests.Status != release.TestsStatusOK || strings.Join(got.Tests.Lanes, ",") != "integration,unit" {
		t.Fatalf("Tests = %+v", got.Tests)
	}
}

// A project with no machine ledger gets none from a report.
func TestExistingMachineLedgerNeverCreatesOne(t *testing.T) {
	t.Parallel()
	if _, ok := existingMachineLedger(t.TempDir()); ok {
		t.Fatal("a directory with no forge.yaml name has a ledger")
	}
}

// A clean run that records no commit (legacy or malformed) does not make a
// lane expected of the release's commit either.
func TestExpectedLanesIgnoreRunsWithNoCommit(t *testing.T) {
	t.Parallel()
	root, git := evProject(t)
	evRun(t, root, "01NOCOMMIT", "", evAt.Add(-time.Hour), false, "integration")
	evRun(t, root, "01A", trCommit, evAt.Add(-2*time.Hour), false, "unit")
	var store evStore
	ref := releaseTestsRef(context.Background(), releaseTestsInput{Root: root, Commit: trCommit, At: evAt, Git: git, Put: store.put})
	if ref.Status != release.TestsStatusOK || strings.Join(ref.Lanes, ",") != "unit" || *ref.Totals != (release.TestsTotals{Passed: 1}) {
		t.Fatalf("the run with no commit leaked into the evidence: %+v %+v", ref, ref.Totals)
	}
}
