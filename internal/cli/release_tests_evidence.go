package cli

// A release's test evidence (release.Release.Tests) on the machine ledger.
//
// A release references its commit's test report; it runs no test. On a
// control-plane env the server binds the commit report when the release is
// cut, and again as lanes arrive. On the machine ledger nobody else will, so
// forge fills it: from .forge/tests/ when the release is cut, and again from
// `forge test report` when a later run of the release's commit records more.
//
// It is REPORT-ONLY. Nothing here can block or fail a cut: every way of not
// knowing becomes status "unavailable" with the reason, and a failure to
// record it is a warning.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/ledgerfile"
	"github.com/reliant-labs/forge/internal/testlocal"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// releaseTestsRecorder is the capability a ledger has when FORGE fills its
// releases' test evidence: the machine ledger. A hosted ledger does not
// implement it — its control plane binds the commit report itself.
type releaseTestsRecorder interface {
	// PutTestReport stores a forge.test.report/v1 document and returns its
	// digest.
	PutTestReport(data []byte) (string, error)
	// RecordTests backfills a recorded release's test evidence
	// (release.BackfillTests); false when it adds nothing.
	RecordTests(ctx context.Context, version string, ref release.TestsRef) (bool, error)
}

// irregularTestLanes are lanes that are never one of a project's regular
// lanes, so a commit without them is not missing anything: `forge test flakes
// confirm` reruns (confirm) and the changed-tests flake gate (flake-gate).
var irregularTestLanes = []string{"confirm", "flake-gate"}

// expectedLanesWindow is how far back a lane the project recorded makes it
// expected of a release's commit — the flake record's window.
const expectedLanesWindow = 30 * 24 * time.Hour

// releaseTestsInput is what the evidence of one release is computed from.
type releaseTestsInput struct {
	// Root is the project root whose .forge/tests/ holds the runs.
	Root   string
	Commit string
	Dirty  bool
	// At anchors the expected-lanes window: the release's cut time.
	At  time.Time
	Git testlocal.Git
	// Put stores the report and returns its digest.
	Put func([]byte) (string, error)
}

// releaseTestsRef is the test evidence of a release cut at in.Commit: the
// report of the commit's clean-tree runs, stored through in.Put. It never
// fails; what it cannot know is status unavailable with the reason.
func releaseTestsRef(ctx context.Context, in releaseTestsInput) (ref release.TestsRef) {
	unavailable := func(format string, a ...any) release.TestsRef {
		return release.TestsRef{Commit: in.Commit, Status: release.TestsStatusUnavailable, Reason: fmt.Sprintf(format, a...)}
	}
	defer func() {
		// Report-only means report-only: a reader bug must not take a
		// release cut down with it.
		if p := recover(); p != nil {
			ref = unavailable("forge could not read the test record (%v); this is a forge bug", p)
		}
	}()
	switch {
	case in.Commit == "":
		return unavailable("the release has no commit (not cut from a git checkout), so no test report can be tied to it")
	case in.Dirty:
		return unavailable("the release was cut from a dirty tree, and a test run on a dirty tree never counts toward a commit")
	}
	all, _, err := testlocal.List(in.Root)
	if err != nil {
		return unavailable("the test record could not be read: %v", err)
	}
	runs := exactCommitRuns(all, in.Commit)
	if len(runs) == 0 {
		return unavailable("no clean-tree test run of commit %s is recorded under %s", testShortID(in.Commit), testreport.RunsRoot(in.Root))
	}
	a, err := testlocal.Assemble(ctx, testlocal.Options{ProjectRoot: in.Root, Runs: runs, NoDiff: true, Git: in.Git})
	if err != nil {
		return unavailable("the commit's test runs could not be merged: %v", err)
	}
	rep := a.Report("")
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return unavailable("the report could not be encoded: %v", err)
	}
	digest, err := in.Put(append(data, '\n'))
	if err != nil {
		return unavailable("the report could not be stored: %v", err)
	}

	lanes := runLaneNames(runs)
	ref = release.TestsRef{
		Commit:       in.Commit,
		ReportDigest: digest,
		Lanes:        lanes,
		// A package that failed with no failing case (a build failure, a
		// timeout) is a failure too: the report's verdict line counts it.
		Totals: &release.TestsTotals{Passed: rep.Totals.Passed, Failed: rep.Totals.Failed + rep.Totals.PackagesFailed, Skipped: rep.Totals.Skipped},
		Status: release.TestsStatusOK,
	}
	if c := rep.Coverage; c != nil {
		ref.Coverage = &release.TestsCoverage{Combined: c.Combined}
		if len(c.Lanes) > 0 {
			ref.Coverage.ByLane = make(map[string]float64, len(c.Lanes))
			for l, v := range c.Lanes {
				ref.Coverage.ByLane[string(l)] = v
			}
		}
	}
	var reasons []string
	if missing := missingLanes(expectedLanes(all, in.At), lanes); len(missing) > 0 {
		ref.Status = release.TestsStatusPartial
		reasons = append(reasons, fmt.Sprintf("no %s lane recorded for this commit (recorded on other commits in the last %d days)",
			strings.Join(missing, ", "), int(expectedLanesWindow/(24*time.Hour))))
	}
	if rep.ExitCode == exitUndetermined && rep.Error != "" {
		reasons = append(reasons, "the report is undetermined: "+rep.Error)
	}
	ref.Reason = strings.Join(reasons, "; ")
	return ref
}

// exactCommitRuns are the clean-tree runs whose manifest names exactly
// commit. testlocal.CommitRuns matches a prefix (what `forge test report
// <sha>` wants); evidence stored on a release forever takes only a run that
// proves it tested that commit, so a run with no commit, or another commit
// sharing the prefix, never joins it.
func exactCommitRuns(all []*testlocal.Run, commit string) []*testlocal.Run {
	commit = strings.TrimSpace(commit)
	var out []*testlocal.Run
	for _, r := range testlocal.CommitRuns(all, commit) {
		if r.Commit != "" && strings.EqualFold(r.Commit, commit) {
			out = append(out, r)
		}
	}
	return out
}

// runLaneNames are the lanes of runs, sorted and unique.
func runLaneNames(runs []*testlocal.Run) []string {
	seen := map[string]bool{}
	for _, r := range runs {
		for _, l := range r.Lanes {
			seen[l.Lane] = true
		}
	}
	return sortedKeys(seen)
}

// expectedLanes are the lanes the project records: every lane of a clean run
// of a commit in the window before at, irregular lanes aside. A run that
// records no commit proves nothing, here as in the evidence itself.
func expectedLanes(all []*testlocal.Run, at time.Time) []string {
	if at.IsZero() {
		at = time.Now()
	}
	from := at.Add(-expectedLanesWindow)
	seen := map[string]bool{}
	for _, r := range all {
		if r.Dirty || r.Commit == "" || r.CreatedAt.Before(from) || r.CreatedAt.After(at) {
			continue
		}
		for _, l := range r.Lanes {
			seen[l.Lane] = true
		}
	}
	for _, l := range irregularTestLanes {
		delete(seen, l)
	}
	return sortedKeys(seen)
}

func missingLanes(expected, have []string) []string {
	got := map[string]bool{}
	for _, l := range have {
		got[l] = true
	}
	var out []string
	for _, l := range expected {
		if !got[l] {
			out = append(out, l)
		}
	}
	return out
}

// captureReleaseTests is the cut's half: the evidence of the release about to
// be recorded, when its ledger is one forge fills (releaseTestsRecorder), else
// nil — the control plane binds it. It prints one line and never fails.
func captureReleaseTests(ctx context.Context, projectDir string, rel release.Release, ledger releaseLedger, warn io.Writer) *release.TestsRef {
	rec, ok := ledger.(releaseTestsRecorder)
	if !ok {
		return nil
	}
	ref := releaseTestsRef(ctx, releaseTestsInput{
		Root: projectDir, Commit: rel.Git.Commit, Dirty: rel.Git.Dirty, At: rel.CreatedAt, Git: testGit, Put: rec.PutTestReport,
	})
	fmt.Fprintf(warn, "[build] tests: %s\n", describeTestsRef(ref))
	return &ref
}

// describeTestsRef is one line of what a ref says.
func describeTestsRef(ref release.TestsRef) string {
	if !ref.HasReport() {
		return ref.Status + ": " + ref.Reason
	}
	s := fmt.Sprintf("%s (%s): %d passed, %d failed, %d skipped", ref.Status, strings.Join(ref.Lanes, ", "),
		ref.Totals.Passed, ref.Totals.Failed, ref.Totals.Skipped)
	if ref.Coverage != nil {
		s += fmt.Sprintf(", coverage %.1f%%", ref.Coverage.Combined)
	}
	if ref.Reason != "" {
		s += " — " + ref.Reason
	}
	return s
}

// backfillReleaseTests is `forge test report`'s half: every release on this
// machine's ledger cut at commit whose evidence the commit's runs now add to
// gets it (release.BackfillTests decides). It opens the ledger only when the
// project already has one, prints a line per release it fills, and never
// changes the report's verdict.
func backfillReleaseTests(ctx context.Context, root, commit string, dirty bool, warn io.Writer) {
	if commit == "" || dirty {
		return
	}
	store, ok := existingMachineLedger(root)
	if !ok {
		return
	}
	backfillReleaseTestsIn(ctx, root, commit, machineReleaseLedger{store: store}, store.Releases, testGit, warn)
}

// backfillReleaseTestsIn is backfillReleaseTests against a given ledger, the
// seam its tests drive.
func backfillReleaseTestsIn(ctx context.Context, root, commit string, rec releaseTestsRecorder, releases func() ([]release.Release, error), git testlocal.Git, warn io.Writer) {
	all, err := releases()
	if err != nil {
		fmt.Fprintf(warn, "forge test report: warning: release test evidence not recorded: %v\n", err)
		return
	}
	for _, r := range all {
		if r.Git.Commit != commit || r.Git.Dirty || (r.Tests.HasReport() && r.Tests.Status != release.TestsStatusPartial) {
			continue
		}
		ref := releaseTestsRef(ctx, releaseTestsInput{Root: root, Commit: commit, At: r.CreatedAt, Git: git, Put: rec.PutTestReport})
		recorded, err := rec.RecordTests(ctx, r.Version, ref)
		switch {
		case err != nil:
			fmt.Fprintf(warn, "forge test report: warning: test evidence of release %s not recorded: %v\n", r.Version, err)
		case recorded:
			fmt.Fprintf(warn, "forge test report: recorded this commit's test evidence on release %s: %s\n", r.Version, describeTestsRef(ref))
		}
	}
}

// existingMachineLedger opens the project's machine ledger only when it
// already exists: a report must not create a ledger for a project that never
// cut a release.
func existingMachineLedger(projectDir string) (*ledgerfile.Store, bool) {
	home, err := ledgerHome()
	if err != nil {
		return nil, false
	}
	name := ledgerProjectName(projectDir)
	if name == "" {
		return nil, false
	}
	id, err := ledgerfile.ProjectID(name, originURL(projectDir))
	if err != nil {
		return nil, false
	}
	if fi, err := os.Stat(filepath.Join(home, id)); err != nil || !fi.IsDir() {
		return nil, false
	}
	store, err := ledgerfile.Open(home, id)
	if err != nil {
		return nil, false
	}
	return store, true
}
