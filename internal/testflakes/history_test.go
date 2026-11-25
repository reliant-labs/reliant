package testflakes

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testreport"
)

var day0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func addRun(t *testing.T, root, commit, tree string, at time.Time, lane, stream string, exit int) string {
	t.Helper()
	return addRunCmd(t, root, runAt{commit, tree, at}, lane, []string{"go", "test", "-json", "./..."}, stream, exit)
}

// runAt is the checkout a recorded run tested, and when.
type runAt struct {
	commit, tree string
	at           time.Time
}

func addRunCmd(t *testing.T, root string, ra runAt, lane string, cmd []string, stream string, exit int) string {
	t.Helper()
	commit, tree, at := ra.commit, ra.tree, ra.at
	dir, ok, warnings := openRun(context.Background(), begin(root, commit, tree, at))
	if !ok {
		t.Fatalf("no run: %v", warnings)
	}
	if _, err := recordLane(dir, laneRun{Lane: lane, Command: cmd, StartedAt: at, FinishedAt: at, ExitCode: exit, Stream: []byte(stream)}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func stateOf(doc *testreport.Flakes, name string) testreport.State {
	for _, t := range doc.Tests {
		if t.ID.Name == name {
			return t.State
		}
	}
	return ""
}

func TestComputeFlakesStatesFromTheLocalRecord(t *testing.T) {
	// The source declares the tests that still exist; TestGone is deleted.
	root := newModule(t, map[string]string{
		"internal/a/a_test.go": "package a\n\nfunc TestSplit(t *testing.T) {}\n\nfunc TestReal(t *testing.T) {}\n",
	})
	pkg := testModule + "/internal/a"
	end := ev("pass", pkg, "", "")
	fail := ev("fail", pkg, "", "")

	// TestSplit: failed on tree t1 in one run, passed on t1 in another (a
	// same-tree split), on two commits: flaky.
	addRun(t, root, "c1", "t1", day0, "unit", failRun(pkg, "TestSplit", "x\n")+passRun(pkg, "TestGone")+fail, 1)
	addRun(t, root, "c1", "t1", day0.Add(time.Hour), "unit", passRun(pkg, "TestSplit")+passRun(pkg, "TestGone")+end, 0)
	addRun(t, root, "c2", "t2", day0.Add(48*time.Hour), "unit", failRun(pkg, "TestSplit", "x\n")+fail, 1)
	addRun(t, root, "c2", "t2", day0.Add(49*time.Hour), "unit", passRun(pkg, "TestSplit")+end, 0)
	// TestGone retry-passed once, and its source is gone: removed.
	addRun(t, root, "c3", "t3", day0.Add(50*time.Hour), "flake-gate",
		failRun(pkg, "TestGone", "boom\n")+passRun(pkg, "TestGone")+passRun(pkg, "TestSplit")+end, 0)
	addRun(t, root, "c4", "t4", day0.Add(51*time.Hour), "flake-gate", passRun(pkg, "TestSplit")+end, 0)
	// TestReal failed on its own tree and nowhere passed: a break, no state.
	addRun(t, root, "c5", "t5", day0.Add(52*time.Hour), "unit", failRun(pkg, "TestReal", "no\n")+passRun(pkg, "TestSplit")+fail, 1)
	// A cancelled lane is not in the record.
	addRun(t, root, "c6", "t6", day0.Add(53*time.Hour), "unit", failRun(pkg, "TestCancelled", "killed\n"), 130)

	if _, err := QuarantineAdd(root, testModule, Entry{Lang: testreport.LangGo, Package: pkg, Test: "TestQuarantined", ExpiresAt: day0}, false); err != nil {
		t.Fatal(err)
	}

	now := day0.Add(72 * time.Hour)
	doc, warnings := ComputeFlakes(FlakesOptions{Root: root, Now: now})
	if len(warnings) > 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	want := map[string]testreport.State{
		"TestSplit": testreport.StateFlaky, "TestGone": testreport.StateRemoved, "TestQuarantined": testreport.StateQuarantined,
	}
	for name, st := range want {
		if got := stateOf(doc, name); got != st {
			t.Errorf("%s: %q, want %q", name, got, st)
		}
	}
	for _, name := range []string{"TestReal", "TestCancelled"} {
		if got := stateOf(doc, name); got != "" {
			t.Errorf("%s has state %q, want none", name, got)
		}
	}
	split := doc.Tests[0]
	if split.ID.Name != "TestSplit" || split.N != 7 || split.Events30d != 2 || split.Score <= 0 || split.Events[0].Signal != testreport.SignalSameTreeSplit {
		t.Errorf("flaky test = %+v", split)
	}
	for _, tt := range doc.Tests {
		if tt.ID.Name == "TestQuarantined" && (tt.Quarantine == nil || !tt.Quarantine.Overdue) {
			t.Errorf("an expired entry stays applied and is overdue: %+v", tt.Quarantine)
		}
	}
	if len(doc.Suggested) != 2 {
		t.Errorf("suggested = %v", doc.Suggested)
	}

	// --state narrows, and healthy is included only when asked for.
	only, _ := ComputeFlakes(FlakesOptions{Root: root, Now: now, States: []testreport.State{testreport.StateQuarantined}})
	if len(only.Tests) != 1 || only.Tests[0].ID.Name != "TestQuarantined" {
		t.Errorf("filtered = %+v", only.Tests)
	}
	// Fourteen quiet days do not heal a flaky test (its file must change).
	later, _ := ComputeFlakes(FlakesOptions{Root: root, Now: now.Add(20 * 24 * time.Hour)})
	if got := stateOf(later, "TestSplit"); got != testreport.StateFlaky {
		t.Errorf("later: %s", got)
	}
}

func TestComputeFlakesOnAnEmptyRecord(t *testing.T) {
	doc, warnings := ComputeFlakes(FlakesOptions{Root: t.TempDir()})
	if len(warnings) > 0 || len(doc.Tests) != 0 || doc.Schema != testreport.SchemaFlakes || doc.Host == nil {
		t.Errorf("%+v %v", doc, warnings)
	}
}

// A test left out of a run is removed only when that run would have run it.
// The flake gate and confirm run chosen tests, and a lane with -run (or a
// -skip that matches the test) ran a subset: none of them is evidence, and a
// test the source still declares is never removed.
func TestRemovalNeedsACompleteRunOrTheSource(t *testing.T) {
	pkg := testModule + "/internal/a"
	end := ev("pass", pkg, "", "")
	record := func(t *testing.T, root string) {
		// A retry pass in the unit lane: suspected.
		addRun(t, root, "c1", "t1", day0, "unit", failRun(pkg, "TestFlaky", "boom\n")+passRun(pkg, "TestFlaky")+passRun(pkg, "TestOther")+end, 0)
		// Later subsets of the package that leave TestFlaky out.
		addRunCmd(t, root, runAt{"c2", "t2", day0.Add(time.Hour)}, "flake-gate",
			[]string{"go", "test", "-json", "-count=3", "-run", "^(TestOther)$", "./internal/a"}, passRun(pkg, "TestOther")+end, 0)
		addRunCmd(t, root, runAt{"c3", "t3", day0.Add(2 * time.Hour)}, "unit",
			[]string{"go", "test", "-json", "-run=^TestOther$", "./..."}, passRun(pkg, "TestOther")+end, 0)
		addRunCmd(t, root, runAt{"c4", "t4", day0.Add(3 * time.Hour)}, "unit",
			[]string{"go", "test", "-json", "-skip", "^(TestFlaky)$", "./..."}, passRun(pkg, "TestOther")+end, 0)
	}
	now := day0.Add(24 * time.Hour)

	// The source still declares it: suspected, whatever runs left it out.
	declared := newModule(t, map[string]string{"internal/a/a_test.go": "package a\n\nfunc TestFlaky(t *testing.T) {}\n\nfunc TestOther(t *testing.T) {}\n"})
	record(t, declared)
	doc, _ := ComputeFlakes(FlakesOptions{Root: declared, Now: now})
	if got := stateOf(doc, "TestFlaky"); got != testreport.StateSuspected {
		t.Errorf("declared in source: %q, want suspected", got)
	}

	// No go.mod: the source cannot say, so only complete runs decide.
	bare := t.TempDir()
	record(t, bare)
	doc, _ = ComputeFlakes(FlakesOptions{Root: bare, Now: now})
	if got := stateOf(doc, "TestFlaky"); got != testreport.StateSuspected {
		t.Errorf("only filtered runs omit it: %q, want suspected", got)
	}
	// A -skip of the quarantine, which does not match it, keeps a run complete.
	addRunCmd(t, bare, runAt{"c5", "t5", day0.Add(4 * time.Hour)}, "unit",
		[]string{"go", "test", "-json", "-skip", "^(TestQuarantinedElsewhere)$", "./..."}, passRun(pkg, "TestOther")+end, 0)
	doc, _ = ComputeFlakes(FlakesOptions{Root: bare, Now: now})
	if got := stateOf(doc, "TestFlaky"); got != testreport.StateRemoved {
		t.Errorf("a complete unit run of its package omits it: %q, want removed", got)
	}
}

func TestCompleteFor(t *testing.T) {
	id := testreport.TestID{Lang: testreport.LangGo, Package: testModule + "/x", Name: "TestA"}
	res := &testreport.Results{}
	for _, c := range []struct {
		lane string
		cmd  []string
		want bool
	}{
		{"unit", []string{"task", "test"}, true},
		{"integration", []string{"go", "test", "-tags", "integration", "./..."}, true},
		{"unit", []string{"go", "test", "-run", "TestB", "./..."}, false},
		{"unit", []string{"go", "test", "-test.run=TestB"}, false},
		{"unit", []string{"go", "test", "-skip=^(TestA|TestC)$"}, false},
		{"unit", []string{"go", "test", "-skip", "^(TestC)$"}, true},
		{"unit", []string{"go", "test", "-skip", "("}, false},
		{"flake-gate", []string{"go", "test"}, false},
		{"confirm", []string{"go", "test"}, false},
		{"e2e", []string{"go", "test"}, false},
	} {
		r := LocalRun{RunRecord: testreport.RunRecord{ID: "r", Results: res}, Lane: c.lane, Command: c.cmd}
		if got := completeFor(r, id); got != c.want {
			t.Errorf("%s %v: %v, want %v", c.lane, c.cmd, got, c.want)
		}
	}
}
