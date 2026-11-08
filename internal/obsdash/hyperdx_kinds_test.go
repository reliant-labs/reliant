package obsdash

import "testing"

// HyperDX has no top-N bar over groups, no value-bucketed heatmap of a field,
// no raw SQL panel and no arithmetic across series. A dashboard that holds them
// renders without them, and the rest of its tiles are unchanged.
func TestHyperDXLeavesOutThePanelsOnlyTheConsoleDraws(t *testing.T) {
	count := []Series{{Label: "n", Agg: AggCount}}
	two := []Series{{Label: "a", Agg: AggCount}, {Label: "b", Agg: AggCount}}
	d := Dashboard{ID: "d", Title: "D", Panels: []Panel{
		{ID: "kept", Title: "Kept", Kind: KindLine, Signal: Traces, Series: count},
		{ID: "bar", Title: "Bar", Kind: KindBar, Signal: Traces, GroupBy: []string{"span.name"}, Limit: 5, Series: count},
		{ID: "heat", Title: "Heat", Kind: KindHeatmap, Signal: Traces, Series: []Series{{Label: "n", Agg: AggCount, Field: "span.duration"}}},
		{ID: "sql", Title: "SQL", Kind: KindSQL, SQL: "SELECT 1"},
		{ID: "formula", Title: "Formula", Kind: KindLine, Signal: Traces, Formula: "A / B * 100", Series: two},
		{ID: "table", Title: "Table", Kind: KindTable, Signal: Traces, GroupBy: []string{"span.name"}, Limit: 5, Series: count},
	}}
	hd, err := renderDashboard(d)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, tile := range hd.Tiles {
		ids = append(ids, tile.ID)
	}
	if len(ids) != 2 || ids[0] != "t-kept" || ids[1] != "t-table" {
		t.Fatalf("tiles = %v, want [t-kept t-table]", ids)
	}
}
