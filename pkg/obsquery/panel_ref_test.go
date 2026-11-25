package obsquery

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

// The body is the control plane's QueryPanelRequest in protojson: the scope,
// and a panelRef in place of a panel.
func TestPanelRefQuery_Body(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	body, err := PanelRefQuery{
		OrgID: "org-1", ProjectID: "proj-1", EnvIDs: []string{"env-1"},
		From: from, To: from.AddDate(0, 0, 7),
		Ref: PanelRef{Dashboard: "product-usage", Panel: "engagement-headline"},
	}.Body()
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"scope": map[string]any{
			"orgId":     "org-1",
			"projectId": "proj-1",
			"envIds":    []any{"env-1"},
			"timeRange": map[string]any{"start": "2026-11-02T00:00:00Z", "end": "2026-11-09T00:00:00Z"},
		},
		"panelRef": map[string]any{"dashboardId": "product-usage", "panelId": "engagement-headline"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %s", body)
	}
	if _, sent := got["panel"]; sent {
		t.Fatal("a ref query must not send a panel")
	}
}

func TestPanelRefQuery_BodyRefusesAnIncompleteQuery(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	ok := PanelRefQuery{OrgID: "org-1", From: from, To: from.Add(time.Hour), Ref: PanelRef{Dashboard: "d", Panel: "p"}}
	for name, mut := range map[string]func(*PanelRefQuery){
		"no org":       func(q *PanelRefQuery) { q.OrgID = "" },
		"no dashboard": func(q *PanelRefQuery) { q.Ref.Dashboard = "" },
		"no panel":     func(q *PanelRefQuery) { q.Ref.Panel = "" },
		"no range":     func(q *PanelRefQuery) { q.From = time.Time{} },
		"backwards":    func(q *PanelRefQuery) { q.From, q.To = q.To, q.From },
		"negative step": func(q *PanelRefQuery) {
			q.StepSeconds = -1
		},
	} {
		q := ok
		mut(&q)
		if _, err := q.Body(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	if _, err := ok.Body(); err != nil {
		t.Fatalf("a complete query: %v", err)
	}
}

// Rows are keyed by column, a NULL (missingValueIndexes) is absent rather
// than zero, and the statement the server ran comes back with the result.
func TestDecodePanelResult(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"panelId": "stitched-funnel", "kind": "sql", "unit": "",
		"columns": ["visitors", "signups"],
		"rows": [
			{"group": {"step": "1"}, "values": [120, 0], "missingValueIndexes": [1]},
			{"group": {"step": "2"}, "values": [40, 7]}
		],
		"panel": {"id": "stitched-funnel", "kind": "sql", "sql": "SELECT 1"},
		"compiledSql": [{"sql": "SELECT 1", "params": {"from": "x"}}],
		"futureField": true
	}`)
	got, err := DecodePanelResult(body)
	if err != nil {
		t.Fatal(err)
	}
	want := PanelResult{
		PanelID: "stitched-funnel", Kind: "sql", Columns: []string{"visitors", "signups"},
		Rows: []PanelRow{
			{Group: map[string]string{"step": "1"}, Values: map[string]float64{"visitors": 120}},
			{Group: map[string]string{"step": "2"}, Values: map[string]float64{"visitors": 40, "signups": 7}},
		},
		SQL:      "SELECT 1",
		Compiled: []CompiledStatement{{SQL: "SELECT 1", Params: map[string]string{"from": "x"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	ts, err := DecodePanelResult([]byte(`{"kind": "sql", "series": [{"label": "n"}]}`))
	if err != nil || !ts.TimeSeries {
		t.Fatalf("a time-series answer: %+v, %v", ts, err)
	}
	if _, err := DecodePanelResult([]byte(`not json`)); err == nil {
		t.Fatal("a body that is not JSON must fail")
	}
}
