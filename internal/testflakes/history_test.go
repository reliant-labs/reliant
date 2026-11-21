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
	dir, ok, warnings := openRun(context.Background(), begin(root, commit, tree, at))
	if !ok {
		t.Fatalf("no run: %v", warnings)
	}
	if _, err := recordLane(dir, laneRun{Lane: lane, Command: []string{"go", "test"}, StartedAt: at, FinishedAt: at, ExitCode: exit, Stream: []byte(stream)}); err != nil {
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
	root := newModule(t, nil)
	pkg := testModule + "/internal/a"
	end := ev("pass", pkg, "", "")
	fail := ev("fail", pkg, "", "")

	// TestSplit: failed on tree t1 in one run, passed on t1 in another (a
	// same-tree split), on two commits: flaky.
	addRun(t, root, "c1", "t1", day0, "unit", failRun(pkg, "TestSplit", "x\n")+passRun(pkg, "TestGone")+fail, 1)
	addRun(t, root, "c1", "t1", day0.Add(time.Hour), "unit", passRun(pkg, "TestSplit")+passRun(pkg, "TestGone")+end, 0)
	addRun(t, root, "c2", "t2", day0.Add(48*time.Hour), "unit", failRun(pkg, "TestSplit", "x\n")+fail, 1)
	addRun(t, root, "c2", "t2", day0.Add(49*time.Hour), "unit", passRun(pkg, "TestSplit")+end, 0)
	// TestGone retry-passed once, then disappeared from its package: removed.
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
