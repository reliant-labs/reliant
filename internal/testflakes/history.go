package testflakes

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/testreport"
)

// The local record: every run under .forge/tests/runs/, read into
// pkg/testreport's RunRecords so flake detection and the state machine run
// on it exactly as the control plane runs them on a project's uploads.
//
// Locally there is no default branch to filter by, so every recorded run
// counts, whichever branch it ran on; a lane killed by a signal (exit status
// over 128: cancelled, interrupted) is left out, as a cancelled CI run is.

// LocalRun is one lane of one recorded run.
type LocalRun struct {
	testreport.RunRecord
	Lane   string
	Branch string
	// Command is the lane's command as capture recorded it: what tells a
	// complete package run from a filtered one (completeFor).
	Command []string
}

// LoadRecord reads every captured lane of every run under root's
// .forge/tests/runs/, oldest run first. A confirm lane also yields the red
// run's failures it read (as lane <source lane>, on the source's tree),
// unless those are a local run already in the record, or the source's tree
// is unknown or not the tree the reruns ran on. Unreadable runs are
// skipped and named in the warnings.
func LoadRecord(root string) ([]LocalRun, []string) {
	runsRoot := testreport.RunsRoot(root)
	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{err.Error()}
	}
	var (
		out      []LocalRun
		warnings []string
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runDir := filepath.Join(runsRoot, e.Name())
		m, err := testreport.ReadRunManifest(runDir)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("run %s: %v", e.Name(), err))
			continue
		}
		lanes, _ := testreport.CapturedLanes(runDir)
		for _, lane := range lanes {
			lm, err := testreport.ReadLaneManifest(runDir, lane)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("run %s lane %s: %v", m.ID, lane, err))
				continue
			}
			if lm.ExitCode > 128 {
				continue // cancelled or interrupted
			}
			at := lm.FinishedAt
			if at.IsZero() {
				at = m.CreatedAt
			}
			if res := readLaneResults(runDir, lane, lm); res != nil {
				out = append(out, LocalRun{RunRecord: testreport.RunRecord{ID: m.ID, Commit: m.Commit, Tree: m.TreeHash, At: at, Results: res}, Lane: lane, Branch: m.Branch, Command: lm.Command})
			}
			if lane != LaneConfirm {
				continue
			}
			doc, err := ReadConfirm(testreport.LaneDir(runDir, lane))
			if err != nil || doc.Source.LocalRun != "" || len(doc.Failures) == 0 {
				continue
			}
			if doc.Source.Tree == "" || doc.Source.Tree != m.TreeHash {
				// Not proven to be the reruns' snapshot: kept out of the
				// record, so a pass here never confirms a failure there.
				continue
			}
			out = append(out, LocalRun{RunRecord: testreport.RunRecord{
				ID: m.ID + ":source", Commit: doc.Source.Commit, Tree: doc.Source.Tree,
				// Just before the reruns, so the failure precedes its confirmation.
				At: at.Add(-time.Second), Results: sourceResults(doc),
			}, Lane: doc.Source.Lane})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, warnings
}

func readLaneResults(runDir, lane string, lm testreport.LaneManifest) *testreport.Results {
	if lm.ResultsFile == "" || lm.Results == testreport.LaneResultsMissing {
		return nil
	}
	f, err := os.Open(filepath.Join(testreport.LaneDir(runDir, lane), lm.ResultsFile))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	opts := testreport.ResultsOptions{Lane: testreport.Lane(lane)}
	switch lm.Format {
	case testreport.LaneFormatGoJSON:
		opts.Format = testreport.FormatGoJSON
	case testreport.LaneFormatJUnit:
		opts.Format = testreport.FormatJUnit
	case testreport.LaneFormatVitest:
		opts.Format = testreport.FormatVitest
	}
	res, err := testreport.ParseResults(f, opts)
	if err != nil || res.Stats.Events == 0 {
		return nil
	}
	return res
}

// sourceResults is a confirm's failures as a results document of the source
// lane.
func sourceResults(doc *Confirm) *testreport.Results {
	r := &testreport.Results{Head: testreport.Head{Schema: testreport.SchemaResults, ExitCode: 1}, Lane: testreport.Lane(doc.Source.Lane), Format: testreport.FormatGoJSON}
	for _, f := range doc.Failures {
		if f.ID.Name == "" {
			r.Packages = append(r.Packages, testreport.PackageResult{Lang: f.ID.Lang, Path: f.ID.Package, Status: testreport.StatusFail, Output: f.Output, Signature: f.Signature})
			continue
		}
		r.Cases = append(r.Cases, f)
	}
	sort.Slice(r.Cases, func(i, j int) bool { return r.Cases[i].ID.String() < r.Cases[j].ID.String() })
	return r
}

// FlakesOptions configures ComputeFlakes.
type FlakesOptions struct {
	Root string
	Now  time.Time
	// States keeps only tests in these states; empty keeps every state but
	// healthy.
	States []testreport.State
}

// ComputeFlakes is the forge.test.flakes/v1 document of the local record:
// every test with a flake state, host health, the correlated (infra) events,
// and the quarantine entries, overdue ones flagged.
func ComputeFlakes(o FlakesOptions) (*testreport.Flakes, []string) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	runs, warnings := LoadRecord(o.Root)
	records := make([]testreport.RunRecord, len(runs))
	for i, r := range runs {
		records[i] = r.RunRecord
	}
	det := testreport.DetectFlakeEvents(records, testreport.DetectOptions{})

	entries, errs := QuarantineList(o.Root)
	for _, e := range errs {
		warnings = append(warnings, "quarantine: "+e.Error())
	}
	quarantined := map[testreport.TestID]Entry{}
	for _, e := range entries {
		quarantined[e.ID()] = e
	}

	byTest := map[testreport.TestID][]testreport.FlakeEvent{}
	for _, e := range det.Events {
		byTest[e.Test] = append(byTest[e.Test], e)
	}
	ids := map[testreport.TestID]bool{}
	for id := range byTest {
		ids[id] = true
	}
	for id := range quarantined {
		ids[id] = true
	}

	ran := runsOfTests(runs)
	module, _ := ModulePath(o.Root)
	src := sourceIndex{root: o.Root, module: module}
	keep := map[testreport.State]bool{}
	for _, s := range o.States {
		keep[s] = true
	}
	doc := &testreport.Flakes{Head: testreport.Head{Schema: testreport.SchemaFlakes, OK: true}, Tests: []testreport.FlakyTest{}, Host: testreport.HostHealthOf(det.Host), Infra: det.Infra}
	for id := range ids {
		evs := byTest[id]
		q, isQ := quarantined[id]
		st := testreport.FlakeState(testreport.History{Events: evs, Quarantined: isQ, Absent: absent(id, runs, src)}, o.Now)
		switch {
		case len(keep) > 0 && !keep[st.State]:
			continue
		case len(keep) == 0 && st.State == testreport.StateHealthy:
			continue
		}
		n := ran[id]
		ft := testreport.FlakyTest{ID: id, State: st.State, N: n, Events30d: st.Events30d, Score: testreport.WilsonLowerBound(st.Events30d, n), Events: confirmedOnly(evs)}
		if !st.Since.IsZero() {
			since := st.Since
			ft.Since = &since
		}
		if isQ {
			ft.Quarantine = q.Document(o.Now)
			ft.Issue = q.Issue
		}
		doc.Tests = append(doc.Tests, ft)
	}
	sort.Slice(doc.Tests, func(i, j int) bool {
		a, b := doc.Tests[i], doc.Tests[j]
		if ra, rb := stateRank(a.State), stateRank(b.State); ra != rb {
			return ra < rb
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.ID.String() < b.ID.String()
	})
	doc.Suggested = Suggestions(doc.Tests)
	return doc, warnings
}

// Suggestions are the next commands for tests: quarantine each suspected or
// flaky test that is not quarantined yet, and remove the entry of an overdue
// one in the change that deletes or rewrites it.
func Suggestions(tests []testreport.FlakyTest) []string {
	var out []string
	for _, t := range tests {
		switch {
		case (t.State == testreport.StateSuspected || t.State == testreport.StateFlaky) && t.Quarantine == nil:
			out = append(out, quarantineSuggestion(t.ID, t.Issue))
		case t.Quarantine != nil && t.Quarantine.Overdue:
			out = append(out, fmt.Sprintf("forge test quarantine remove %s  # overdue: delete or rewrite the test in the same change", t.ID))
		}
	}
	return out
}

// confirmedOnly is the events a test's flake record lists: the confirmed
// ones, which are what its state counts.
func confirmedOnly(evs []testreport.FlakeEvent) []testreport.FlakeEvent {
	out := []testreport.FlakeEvent{}
	for _, e := range evs {
		if e.Verdict == testreport.VerdictConfirmed {
			out = append(out, e)
		}
	}
	return out
}

func stateRank(s testreport.State) int {
	switch s {
	case testreport.StateFlaky:
		return 0
	case testreport.StateSuspected:
		return 1
	case testreport.StateQuarantined:
		return 2
	case testreport.StateRemoved:
		return 3
	case testreport.StateFixed:
		return 4
	}
	return 5
}

// quarantineSuggestion is the command that quarantines id.
func quarantineSuggestion(id testreport.TestID, issue string) string {
	cmd := "forge test quarantine add " + id.String()
	if issue != "" {
		cmd += " --issue " + issue
	}
	return cmd
}

// runsOfTests counts, per top-level test, the runs it ran in (not skipped):
// the flake rate's n.
func runsOfTests(runs []LocalRun) map[testreport.TestID]int {
	ran := map[testreport.TestID]int{}
	for _, r := range runs {
		if strings.HasSuffix(r.ID, ":source") {
			continue // a red run's failures, already counted where they ran
		}
		seen := map[testreport.TestID]bool{}
		for _, c := range r.Results.Leaves() {
			if c.Status == testreport.StatusSkip {
				continue
			}
			seen[c.ID.TopLevel()] = true
		}
		for id := range seen {
			ran[id]++
		}
	}
	return ran
}

// absent reports a suspected or flaky test as deleted (the policy's
// outcome, state removed). Not running in a run is evidence only when the
// run would have run it, so the evidence is, in order:
//
//   - the source, when it can be read: the test's package directory in this
//     module, and its `func <Test>(` in a *_test.go there. A test that is
//     still declared is never removed, whatever a run left out;
//   - otherwise the newest COMPLETE run of each lane it ran in (completeFor:
//     a unit or integration lane whose command filters nothing that could
//     select this test away) that tested its package without it.
//
// The flake gate and confirm lanes run chosen tests only, and a lane run with
// -run, or with a -skip that matches the test, ran a subset: none of them
// shows a test is gone.
func absent(id testreport.TestID, runs []LocalRun, src sourceIndex) bool {
	if declared, known := src.declares(id); known {
		return !declared
	}
	lanes := map[string]bool{}
	latest := map[string]LocalRun{}
	for _, r := range runs {
		if !completeFor(r, id) {
			continue
		}
		if cur, ok := latest[r.Lane]; !ok || !r.At.Before(cur.At) {
			latest[r.Lane] = r
		}
		for _, c := range r.Results.Cases {
			if c.ID.TopLevel() == id {
				lanes[r.Lane] = true
				break
			}
		}
	}
	if len(lanes) == 0 {
		return false
	}
	for lane := range lanes {
		l := latest[lane]
		ranPkg, ranTest := false, false
		for _, p := range l.Results.Packages {
			if p.Lang == id.Lang && p.Path == id.Package && !p.NoTestFiles {
				ranPkg = true
			}
		}
		for _, c := range l.Results.Cases {
			if c.ID.TopLevel() == id {
				ranTest = true
				break
			}
		}
		if !ranPkg || ranTest {
			return false
		}
	}
	return true
}

// completeLanes are the lanes whose runs are whole package runs: the
// project's own suites, never the flake gate's or confirm's chosen tests.
var completeLanes = map[string]bool{string(testreport.LaneUnit): true, string(testreport.LaneIntegration): true}

// completeFor reports whether run r would have run test id had it existed:
// a complete lane, with no -run filter and no -skip that matches the test
// (the quarantine's skip-regex matches only quarantined tests, so a run that
// skipped the quarantine is still complete for every other test). A
// -skip that does not compile is taken to match.
func completeFor(r LocalRun, id testreport.TestID) bool {
	if !completeLanes[r.Lane] || strings.HasSuffix(r.ID, ":source") || r.Results == nil {
		return false
	}
	for i := 0; i < len(r.Command); i++ {
		name, val, hasVal := strings.Cut(strings.TrimLeft(r.Command[i], "-"), "=")
		if !strings.HasPrefix(r.Command[i], "-") {
			continue
		}
		name = strings.TrimPrefix(name, "test.")
		switch name {
		case "run":
			return false
		case "skip":
			if !hasVal {
				if i+1 >= len(r.Command) {
					return false
				}
				i++
				val = r.Command[i]
			}
			top, _, _ := strings.Cut(val, "/")
			re, err := regexp.Compile(top)
			if err != nil || re.MatchString(id.Name) {
				return false
			}
		}
	}
	return true
}

// sourceIndex reads whether a Go test is still declared in the module's
// source at root.
type sourceIndex struct {
	root, module string
}

// declares reports whether id's `func <Test>(` is in a *_test.go of its
// package directory; known is false when the source cannot say (no go.mod,
// a package of another module, an unreadable directory).
func (s sourceIndex) declares(id testreport.TestID) (declared, known bool) {
	if id.Lang != testreport.LangGo || id.Name == "" || s.module == "" {
		return false, false
	}
	rel, ok := relPackage(id.Package, s.module)
	if !ok {
		return false, false
	}
	dir := filepath.Join(s.root, filepath.FromSlash(rel))
	if _, err := os.Stat(dir); err != nil {
		// The package directory is gone with its tests.
		return false, os.IsNotExist(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return false, false
	}
	decl := regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(id.Name) + `\(`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return false, false
		}
		if decl.Match(data) {
			return true, true
		}
	}
	return false, true
}
