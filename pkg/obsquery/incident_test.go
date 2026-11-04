package obsquery

import (
	"strings"
	"testing"
	"time"
)

var inc0 = time.Date(2026, 10, 23, 12, 12, 0, 0, time.UTC)

func cand(id, cause string, min int) IncidentCandidate {
	return IncidentCandidate{ID: id, Cause: cause, FirstSeen: inc0.Add(time.Duration(min) * time.Minute)}
}

// T6: a dead database fails every caller, each logging from its own site. They
// are many issues with one cause, first seen together: one incident.
func TestGroupIncidentsMarksIssuesSharingACauseWithinTheWindow(t *testing.T) {
	groups := GroupIncidents([]IncidentCandidate{
		cand("temporal", "SQLSTATE 57P01", 0),
		cand("worker-a", "SQLSTATE 57P01", 1),
		cand("reconciler", "SQLSTATE 57P01", 4),
		cand("unrelated", "context deadline exceeded", 2),
		cand("no-cause", "", 3),
	}, DefaultIncidentWindow)
	if len(groups) != 1 {
		t.Fatalf("groups = %+v, want one incident", groups)
	}
	g := groups[0]
	if got := strings.Join(g.IDs, ","); got != "temporal,worker-a,reconciler" || g.Cause != "SQLSTATE 57P01" || g.RelatedCount() != 2 {
		t.Errorf("incident = %+v", g)
	}
	if !strings.HasPrefix(g.Key, "inc1-") {
		t.Errorf("key %q", g.Key)
	}
	by := IncidentOf(groups)
	if by["unrelated"].Key != "" || by["no-cause"].Key != "" {
		t.Error("an issue alone with its cause must belong to no incident")
	}
	if by["temporal"].Key != by["reconciler"].Key {
		t.Error("members must share one key")
	}
}

// The window is anchored on the incident's first issue: a slow drip of one cause
// is several incidents, never one that grows without bound.
func TestGroupIncidentsAreAnchoredNotChained(t *testing.T) {
	var cs []IncidentCandidate
	for i := 0; i < 8; i++ { // one issue every 4 minutes for 28 minutes
		cs = append(cs, cand("i"+string(rune('a'+i)), "SQLSTATE 08006", i*4))
	}
	groups := GroupIncidents(cs, 10*time.Minute)
	if len(groups) != 3 {
		t.Fatalf("a 28 minute drip in 10 minute windows = 3 incidents (0-8, 12-20, 24-28), got %+v", groups)
	}
	for _, g := range groups {
		first := g.First
		for _, id := range g.IDs {
			for _, c := range cs {
				if c.ID == id && c.FirstSeen.Sub(first) > 10*time.Minute {
					t.Errorf("%s is %s after its incident's first issue", id, c.FirstSeen.Sub(first))
				}
			}
		}
	}
	keys := map[string]bool{}
	for _, g := range groups {
		keys[g.Key] = true
	}
	if len(keys) != 3 {
		t.Error("distinct incidents of one cause must have distinct keys")
	}
}

func TestGroupIncidentsAreIndependentOfInputOrderAndStable(t *testing.T) {
	cs := []IncidentCandidate{cand("c", "x", 5), cand("a", "x", 0), cand("b", "x", 0), cand("z", "y", 0), cand("y", "y", 1)}
	want := GroupIncidents(cs, 0)
	rev := make([]IncidentCandidate, len(cs))
	for i, c := range cs {
		rev[len(cs)-1-i] = c
	}
	got := GroupIncidents(rev, 0)
	if len(got) != 2 || len(want) != 2 {
		t.Fatalf("groups %d / %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Key != want[i].Key || strings.Join(got[i].IDs, ",") != strings.Join(want[i].IDs, ",") {
			t.Errorf("order changed the result: %+v vs %+v", got[i], want[i])
		}
	}
	if IncidentKey("x", inc0) != IncidentKey("x", inc0) || IncidentKey("x", inc0) == IncidentKey("y", inc0) {
		t.Error("IncidentKey is not a function of cause and anchor")
	}
}

func TestServerIssueIncidentsStatement(t *testing.T) {
	st, err := ServerIssueIncidents{Scope: issueScope()}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{ServerFingerprintSQL, ServerCauseSQL, ServerErrorFilterSQL, "HAVING cause != ''", "min(Timestamp) AS first_seen"} {
		if !strings.Contains(st.SQL, want) {
			t.Errorf("statement misses %q:\n%s", want[:min(len(want), 60)], st.SQL)
		}
	}
	if strings.Contains(st.SQL, "ServiceName = ") {
		t.Errorf("an incident spans services; the statement must not narrow to one:\n%s", st.SQL)
	}
}

// A server_error_events row keeps the cause text in Attributes: the same cause
// expression, re-pointed. A row from before the attributes were kept has no cause.
func TestServerEventCauseSQLReadsTheStoredAttributes(t *testing.T) {
	if !strings.Contains(ServerEventCauseSQL, "Attributes['error']") || !strings.Contains(ServerEventCauseSQL, "Attributes['exception.message']") {
		t.Errorf("the event cause must read the stored attributes:\n%s", ServerEventCauseSQL)
	}
	if strings.Contains(ServerEventCauseSQL, "LogAttributes") {
		t.Error("a server_error_events row has no LogAttributes")
	}
	// Same classification chain as the otel_logs spelling.
	norm := func(s string) string { return strings.NewReplacer("LogAttributes", "Attributes").Replace(s) }
	if norm(ServerCauseSQL) != ServerEventCauseSQL {
		t.Error("ServerEventCauseSQL is not ServerCauseSQL over the stored attributes")
	}
}
