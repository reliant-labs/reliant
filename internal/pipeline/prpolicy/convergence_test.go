package prpolicy

import (
	"encoding/json"
	"reflect"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

func blocker(id, p string) Finding {
	return Finding{ID: id, Severity: SeverityBlocker, Path: p, Body: "broken: " + id}
}

func nit(id, p string) Finding {
	return Finding{ID: id, Severity: SeverityNit, Path: p, Body: "style: " + id}
}

func ids(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}

func blockingIDs(d Decision) map[string]BlockReason {
	out := map[string]BlockReason{}
	for _, b := range d.Blocking {
		out[b.ID] = b.Reason
	}
	return out
}

func TestConverge(t *testing.T) {
	cases := []struct {
		name    string
		prior   []Finding
		current []Finding
		newCode NewCode
		block   map[string]BlockReason
		late    []string
		fixed   []string
	}{
		{
			name:    "first round: every blocker blocks, a path-less one too",
			current: []Finding{blocker("a", "x.go"), blocker("b", ""), nit("c", "x.go")},
			newCode: FirstRound(),
			block:   map[string]BlockReason{"a": BlockFirstRound, "b": BlockFirstRound},
		},
		{
			name:    "a prior blocker still there blocks, even on unchanged code",
			prior:   []Finding{blocker("a", "x.go")},
			current: []Finding{blocker("a", "x.go")},
			newCode: ChangedSince("y.go"),
			block:   map[string]BlockReason{"a": BlockUnfixed},
		},
		{
			name:    "a prior blocker left out is fixed",
			prior:   []Finding{blocker("a", "x.go"), nit("n", "x.go")},
			current: nil,
			newCode: ChangedSince("x.go"),
			block:   map[string]BlockReason{},
			fixed:   []string{"a"},
		},
		{
			name:    "a new blocker in code changed since the prior head blocks",
			prior:   []Finding{blocker("a", "x.go")},
			current: []Finding{blocker("b", "./y.go")},
			newCode: ChangedSince("y.go"),
			block:   map[string]BlockReason{"b": BlockNewCode},
			fixed:   []string{"a"},
		},
		{
			name:    "a new blocker on unchanged code is late",
			prior:   []Finding{blocker("a", "x.go")},
			current: []Finding{blocker("a", "x.go"), blocker("late", "z.go")},
			newCode: ChangedSince("x.go"),
			block:   map[string]BlockReason{"a": BlockUnfixed},
			late:    []string{"late"},
		},
		{
			name:    "a new path-less blocker in a later round is late",
			prior:   []Finding{},
			current: []Finding{blocker("general", "")},
			newCode: ChangedSince("x.go"),
			block:   map[string]BlockReason{},
			late:    []string{"general"},
		},
		{
			name:    "a finding about a directory blocks when a file under it changed",
			current: []Finding{blocker("d", "internal/foo")},
			newCode: ChangedSince("internal/foo/bar.go"),
			block:   map[string]BlockReason{"d": BlockNewCode},
		},
		{
			name:    "a sibling directory with a common prefix is not under it",
			current: []Finding{blocker("d", "internal/foo")},
			newCode: ChangedSince("internal/foobar/x.go"),
			block:   map[string]BlockReason{},
			late:    []string{"d"},
		},
		{
			name:    "a prior nit raised again as a blocker on unchanged code is late",
			prior:   []Finding{nit("n", "x.go")},
			current: []Finding{blocker("n", "x.go")},
			newCode: ChangedSince("y.go"),
			block:   map[string]BlockReason{},
			late:    []string{"n"},
		},
		{
			name:    "a prior blocker downgraded to a nit neither blocks nor is fixed",
			prior:   []Finding{blocker("a", "x.go")},
			current: []Finding{nit("a", "x.go")},
			newCode: ChangedSince("x.go"),
			block:   map[string]BlockReason{},
		},
		{
			name:    "an unknown severity fails closed",
			current: []Finding{{ID: "u", Severity: "BLOCKING", Path: "x.go", Body: "?"}},
			newCode: ChangedSince("x.go"),
			block:   map[string]BlockReason{"u": BlockNewCode},
		},
		{
			name:    "a repeated id counts once",
			current: []Finding{blocker("a", "x.go"), blocker("a", "y.go")},
			newCode: FirstRound(),
			block:   map[string]BlockReason{"a": BlockFirstRound},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Converge(c.prior, c.current, c.newCode)
			if got := blockingIDs(d); !reflect.DeepEqual(got, c.block) {
				t.Errorf("blocking = %v, want %v", got, c.block)
			}
			if d.Blocks() != (len(c.block) > 0) {
				t.Errorf("Blocks() = %v", d.Blocks())
			}
			if got := ids(d.Late); !reflect.DeepEqual(got, c.late) {
				t.Errorf("late = %v, want %v", got, c.late)
			}
			if got := ids(d.Fixed); !reflect.DeepEqual(got, c.fixed) {
				t.Errorf("fixed = %v, want %v", got, c.fixed)
			}
			for _, b := range d.Blocking {
				if b.Why == "" {
					t.Errorf("blocker %s has no why", b.ID)
				}
			}
		})
	}
}

// A late finding is recorded as a nit, so when the next round raises it
// again on still-unchanged code it does not become a "prior blocker".
func TestConvergeLateFindingStaysANit(t *testing.T) {
	round2 := Converge([]Finding{blocker("a", "x.go")}, []Finding{blocker("late", "z.go")}, ChangedSince("x.go"))
	if round2.Blocks() || len(round2.Late) != 1 {
		t.Fatalf("round 2: %+v", round2)
	}
	if round2.Findings[0].Severity != SeverityNit || round2.Late[0].Severity != SeverityBlocker {
		t.Fatalf("round 2 findings: %+v late: %+v", round2.Findings, round2.Late)
	}
	round3 := Converge(round2.Findings, []Finding{blocker("late", "z.go")}, ChangedSince("x.go"))
	if round3.Blocks() {
		t.Fatalf("round 3 blocks on a late finding: %+v", round3.Blocking)
	}
	if LateFindingNote(round2.Late[0]) == "" {
		t.Fatal("no note")
	}
}

func TestFindingFromProtoAndJSON(t *testing.T) {
	got := FindingsFromProto([]*reliantv1.ReviewFinding{{Id: "b1", Severity: "blocker", Path: "main.go", Line: 12, Body: "nil deref"}})
	want := []Finding{{ID: "b1", Severity: SeverityBlocker, Path: "main.go", Line: 12, Body: "nil deref"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	// findings_json uses the review output's keys.
	b, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, map[string]any{"id": "b1", "severity": "blocker", "path": "main.go", "line": float64(12), "body": "nil deref"}) {
		t.Fatalf("%s", b)
	}
	if string(SeverityBlocker) != model.ReviewSeverityBlocker || string(SeverityNit) != model.ReviewSeverityNit {
		t.Fatal("severities differ from the review node's")
	}
}
