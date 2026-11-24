package testreport_test

import (
	"math"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testreport"
)

func goID(pkg, name string) testreport.TestID {
	return testreport.TestID{Lang: testreport.LangGo, Package: pkg, Name: name}
}

func TestMatchHost(t *testing.T) {
	cases := map[string]string{
		"dial tcp 10.0.0.1:443: connect: connection refused":                   "network",
		"Get https://proxy.golang.org/x: dial tcp: lookup proxy: no such host": "network",
		"toomanyrequests: You have reached your pull rate limit":               "rate_limit",
		"write /tmp/x: no space left on device":                                "disk",
		"listen tcp :8080: bind: address already in use":                       "port",
		"Cannot connect to the Docker daemon at unix:///var/run/docker.sock":   "docker",
		"docker run: context deadline exceeded":                                "docker",
		"panic: test timed out after 2m0s":                                     "timeout",
		"too many open files":                                                  "file_descriptors",
		"got 3, want 4":                                                        "",
		"unexpected EOF while parsing config":                                  "",
	}
	for out, want := range cases {
		got, ok := testreport.MatchHost(out, nil)
		if got != want || ok != (want != "") {
			t.Errorf("MatchHost(%q) = %q, %v; want %q", out, got, ok, want)
		}
	}
}

func TestClassify(t *testing.T) {
	id := goID("p", "TestX")
	pass := &testreport.Case{ID: id, Status: testreport.StatusPass}
	fail := &testreport.Case{ID: id, Status: testreport.StatusFail, Output: "got 1"}
	cases := []struct {
		name    string
		failure testreport.Case
		rerun   *testreport.Case
		want    testreport.Verdict
	}{
		{"race beats everything", testreport.Case{ID: id, Output: "WARNING: DATA RACE\nread at", RetryPassed: true}, pass, testreport.VerdictRaced},
		{"host before scoring", testreport.Case{ID: id, Output: "dial tcp: i/o timeout"}, pass, testreport.VerdictHost},
		{"no test", testreport.Case{ID: goID("p", ""), Output: "build failed"}, nil, testreport.VerdictUnattributed},
		{"retry pass", testreport.Case{ID: id, Output: "got 1", RetryPassed: true}, nil, testreport.VerdictConfirmed},
		{"rerun passed", testreport.Case{ID: id, Output: "got 1"}, pass, testreport.VerdictConfirmed},
		{"rerun failed", testreport.Case{ID: id, Output: "got 1"}, fail, testreport.VerdictReal},
		{"rerun hit the host", testreport.Case{ID: id, Output: "got 1"}, &testreport.Case{Status: testreport.StatusFail, Output: "no space left on device"}, testreport.VerdictInconclusive},
		{"rerun raced", testreport.Case{ID: id, Output: "got 1"}, &testreport.Case{Status: testreport.StatusFail, Output: "WARNING: DATA RACE"}, testreport.VerdictRaced},
		{"no rerun", testreport.Case{ID: id, Output: "got 1"}, nil, testreport.VerdictInconclusive},
		{"rerun skipped", testreport.Case{ID: id, Output: "got 1"}, &testreport.Case{Status: testreport.StatusSkip}, testreport.VerdictInconclusive},
	}
	for _, c := range cases {
		if got := testreport.Classify(c.failure, c.rerun, nil); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestGroupCorrelated(t *testing.T) {
	failures := []testreport.Case{
		{ID: goID("a", "TestOne"), Output: "clickhouse: connection lost"},
		{ID: goID("b", "TestTwo"), Output: "query failed: ClickHouse unavailable"},
		{ID: goID("c", "TestThree/sub"), Output: "clickhouse down"},
		{ID: goID("c", "TestThree/other"), Output: "clickhouse down"},
		{ID: goID("d", "TestAlone"), Output: "got 1, want 2"},
	}
	groups, rest := testreport.GroupCorrelated(failures, testreport.CorrelateOptions{})
	if len(groups) != 1 || groups[0].Reason != "harness:clickhouse" || len(groups[0].Members) != 3 {
		t.Fatalf("groups %+v", groups)
	}
	if len(rest) != 1 || rest[0].ID.Name != "TestAlone" {
		t.Errorf("rest %+v", rest)
	}
	// The same co-failing set is the same event, whatever the run.
	again, _ := testreport.GroupCorrelated(failures[:3], testreport.CorrelateOptions{})
	if len(again) != 1 || again[0].Key != groups[0].Key {
		t.Errorf("same set, different key: %+v vs %+v", again, groups)
	}
	// Two failures are not a group.
	if g, r := testreport.GroupCorrelated(failures[:2], testreport.CorrelateOptions{}); len(g) != 0 || len(r) != 2 {
		t.Errorf("two failures grouped: %+v", g)
	}
	// Three in one package are.
	pkg := []testreport.Case{{ID: goID("p", "TestA"), Signature: "1"}, {ID: goID("p", "TestB"), Signature: "2"}, {ID: goID("p", "TestC"), Signature: "3"}}
	if g, _ := testreport.GroupCorrelated(pkg, testreport.CorrelateOptions{}); len(g) != 1 || g[0].Reason != "package:p" {
		t.Errorf("package group %+v", g)
	}
}

func results(lane testreport.Lane, cases ...testreport.Case) *testreport.Results {
	pkgs := map[string]bool{}
	r := &testreport.Results{Lane: lane, Cases: cases}
	for _, c := range cases {
		if !pkgs[c.ID.Package] {
			pkgs[c.ID.Package] = true
			r.Packages = append(r.Packages, testreport.PackageResult{Lang: c.ID.Lang, Path: c.ID.Package, Status: testreport.StatusPass})
		}
	}
	return r
}

func TestDetectFlakeEvents(t *testing.T) {
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	split := goID("p", "TestSplit")
	retry := goID("p", "TestRetry")
	real := goID("q", "TestReal")
	hostT := goID("r", "TestHost")
	runs := []testreport.RunRecord{
		{ID: "r1", Commit: "c1", Tree: "t1", At: at, Results: results("unit",
			testreport.Case{ID: goID("p", "TestSplit/case"), Status: testreport.StatusFail, Output: "boom", Signature: "s1"},
			testreport.Case{ID: retry, Status: testreport.StatusPass, Attempts: 2, FailedAttempts: 1, RetryPassed: true, Output: "first"},
			testreport.Case{ID: real, Status: testreport.StatusFail, Output: "got 1"},
			testreport.Case{ID: hostT, Status: testreport.StatusFail, Output: "toomanyrequests"},
		)},
		{ID: "r2", Commit: "c1", Tree: "t1", At: at.Add(time.Minute), Results: results("unit",
			testreport.Case{ID: goID("p", "TestSplit/case"), Status: testreport.StatusPass},
			testreport.Case{ID: real, Status: testreport.StatusFail, Output: "got 1"},
		)},
	}
	det := testreport.DetectFlakeEvents(runs, testreport.DetectOptions{})
	got := map[string]testreport.FlakeEvent{}
	for _, e := range det.Events {
		got[e.Run+" "+e.Test.String()] = e
	}
	if e := got["r1 "+split.String()]; e.Verdict != testreport.VerdictConfirmed || e.Signal != testreport.SignalSameTreeSplit || e.Test != split {
		t.Errorf("same-tree split = %+v", e)
	}
	if e := got["r1 "+retry.String()]; e.Verdict != testreport.VerdictConfirmed || e.Signal != testreport.SignalRetryPass {
		t.Errorf("retry pass = %+v", e)
	}
	if e := got["r1 "+real.String()]; e.Verdict != testreport.VerdictReal {
		t.Errorf("real = %+v", e)
	}
	if _, ok := got["r1 "+hostT.String()]; ok {
		t.Error("a host failure became a test event")
	}
	if len(det.Host) != 1 || det.Host[0].Rule != "rate_limit" || det.Host[0].Test != hostT {
		t.Errorf("host %+v", det.Host)
	}
	hh := testreport.HostHealthOf(append(det.Host, testreport.HostEvent{Rule: "rate_limit", Test: hostT, Run: "r9"}, testreport.HostEvent{Rule: "disk", Test: real, Run: "r1"}))
	if len(hh) != 2 || hh[0].Signature != "disk" || hh[1].Events != 2 || len(hh[1].Runs) != 2 || len(hh[1].Tests) != 1 {
		t.Errorf("host health %+v", hh)
	}

	// Three failures sharing a harness are one infra event, and the same set
	// on a later commit is the same event.
	infraRun := func(id, commit string) testreport.RunRecord {
		return testreport.RunRecord{ID: id, Commit: commit, Tree: commit, At: at, Results: results("unit",
			testreport.Case{ID: goID("a", "TestA"), Status: testreport.StatusFail, Output: "pgtest: template busy"},
			testreport.Case{ID: goID("b", "TestB"), Status: testreport.StatusFail, Output: "pgtest: template busy"},
			testreport.Case{ID: goID("c", "TestC"), Status: testreport.StatusFail, Output: "pgtest: template busy"},
		)}
	}
	det = testreport.DetectFlakeEvents([]testreport.RunRecord{infraRun("r3", "c2"), infraRun("r4", "c3")}, testreport.DetectOptions{})
	if len(det.Infra) != 1 || len(det.Infra[0].Occurrences) != 2 {
		t.Fatalf("infra %+v", det.Infra)
	}
	if len(det.Events) != 6 {
		t.Fatalf("each member of each occurrence is a correlated event: %+v", det.Events)
	}
	for _, e := range det.Events {
		if e.Verdict != testreport.VerdictCorrelated || e.InfraGroup != det.Infra[0].Key || e.Signal != "" {
			t.Errorf("correlated event %+v", e)
		}
	}

	// A confirm-lane rerun that passes on the tree confirms with kind confirm.
	confirmRuns := []testreport.RunRecord{
		{ID: "r5", Commit: "c4", Tree: "t4", At: at, Results: results("unit", testreport.Case{ID: real, Status: testreport.StatusFail, Output: "got 1"})},
		{ID: "r6", Commit: "c4", Tree: "t4", At: at, Results: results(testreport.LaneConfirm, testreport.Case{ID: real, Status: testreport.StatusPass})},
	}
	det = testreport.DetectFlakeEvents(confirmRuns, testreport.DetectOptions{})
	if len(det.Events) != 1 || det.Events[0].Signal != testreport.SignalConfirm || det.Events[0].Verdict != testreport.VerdictConfirmed {
		t.Errorf("confirm rerun: %+v", det.Events)
	}
}

func TestFlakeState(t *testing.T) {
	day := 24 * time.Hour
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	ev := func(commit string, d int) testreport.FlakeEvent {
		return testreport.FlakeEvent{Commit: commit, At: t0.Add(time.Duration(d) * day), Verdict: testreport.VerdictConfirmed}
	}
	cases := []struct {
		name string
		h    testreport.History
		now  int
		want testreport.State
	}{
		{"no events", testreport.History{}, 10, testreport.StateHealthy},
		{"one event is suspected", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0)}}, 5, testreport.StateSuspected},
		{"suspected decays after 14 quiet days", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0)}}, 14, testreport.StateHealthy},
		{"a same-commit burst counts once", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0), ev("c1", 1), ev("c1", 2)}}, 3, testreport.StateSuspected},
		{"two commits in 30 days is flaky", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0), ev("c2", 20)}}, 21, testreport.StateFlaky},
		{"two commits further apart is not", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0), ev("c2", 31)}}, 32, testreport.StateSuspected},
		{"flaky does not decay on its own", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0), ev("c2", 1)}}, 90, testreport.StateFlaky},
		{"flaky is fixed 14 days after its file changes", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0), ev("c2", 1)}, FileChanges: []time.Time{t0.Add(5 * day)}}, 19, testreport.StateFixed},
		{"not yet fixed", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0), ev("c2", 1)}, FileChanges: []time.Time{t0.Add(5 * day)}}, 18, testreport.StateFlaky},
		{"a file change before the last event does not fix", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0), ev("c2", 10)}, FileChanges: []time.Time{t0.Add(5 * day)}}, 40, testreport.StateFlaky},
		{"an event after the fix starts over at suspected", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0), ev("c2", 1), ev("c3", 25)}, FileChanges: []time.Time{t0.Add(5 * day)}}, 26, testreport.StateSuspected},
		{"non-confirmed events never count", testreport.History{Events: []testreport.FlakeEvent{{Commit: "c1", At: t0, Verdict: testreport.VerdictHost}, {Commit: "c2", At: t0, Verdict: testreport.VerdictReal}}}, 1, testreport.StateHealthy},
		{"quarantine wins", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0), ev("c2", 1)}, Quarantined: true, Absent: true}, 2, testreport.StateQuarantined},
		{"a flaky test that disappeared is removed", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0), ev("c2", 1)}, Absent: true}, 2, testreport.StateRemoved},
		{"a suspected test that disappeared is removed", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0)}, Absent: true}, 2, testreport.StateRemoved},
		{"a healthy absent test is just healthy", testreport.History{Absent: true}, 2, testreport.StateHealthy},
		{"future events are ignored", testreport.History{Events: []testreport.FlakeEvent{ev("c1", 10)}}, 5, testreport.StateHealthy},
	}
	for _, c := range cases {
		got := testreport.FlakeState(c.h, t0.Add(time.Duration(c.now)*day))
		if got.State != c.want {
			t.Errorf("%s: %+v, want %s", c.name, got, c.want)
		}
	}
	st := testreport.FlakeState(testreport.History{Events: []testreport.FlakeEvent{ev("c1", 0), ev("c2", 20)}}, t0.Add(21*day))
	if st.Events30d != 2 || !st.Since.Equal(t0.Add(20*day)) || !st.LastEvent.Equal(t0.Add(20*day)) {
		t.Errorf("status details %+v", st)
	}
}

// Values from the phase-0 table (tests/wilson.txt).
func TestWilsonLowerBound(t *testing.T) {
	cases := []struct {
		k, n int
		want float64
	}{
		{1, 10, 0.0179}, {2, 25, 0.0222}, {5, 100, 0.0215}, {29, 1000, 0.0203}, {28, 1000, 0.0194}, {10, 1000, 0.0054},
	}
	for _, c := range cases {
		if got := testreport.WilsonLowerBound(c.k, c.n); math.Abs(got-c.want) > 0.00015 {
			t.Errorf("WilsonLowerBound(%d, %d) = %.4f, want %.4f", c.k, c.n, got, c.want)
		}
	}
	if testreport.WilsonLowerBound(0, 0) != 0 || testreport.WilsonLowerBound(0, 10) != 0 {
		t.Error("no events or no runs is 0")
	}
}

func TestFailureSignature(t *testing.T) {
	a := testreport.FailureSignature("    x_test.go:42: got 3 at 0xc000123456 after 1.5s in /tmp/TestX123/db\n--- FAIL: TestX (1.52s)\n")
	b := testreport.FailureSignature("    x_test.go:42: got 7 at 0xc000999999 after 2s in /tmp/TestX987/db\n--- FAIL: TestX (2.01s)\n")
	c := testreport.FailureSignature("    x_test.go:42: connection refused\n")
	if a == "" || a != b || a == c {
		t.Errorf("signatures a=%s b=%s c=%s", a, b, c)
	}
	if testreport.FailureSignature("") != "" {
		t.Error("empty output has no signature")
	}
}
