package dashspec

import (
	"strings"
	"testing"
)

func TestOptionalSectionsAndFilterTargets(t *testing.T) {
	base := `{"id":"ops","title":"Operations","panels":[{"id":"requests","title":"Requests","kind":"number","signal":"traces","series":[{"label":"requests","agg":"count"}]}]}`
	if _, err := ParseDashboard("legacy", []byte(base)); err != nil {
		t.Fatal(err)
	}
	valid := `{"id":"ops","title":"Operations","sections":[{"id":"rpc","title":"RPC"}],"filters":[{"id":"method","label":"Method","field":"span.attr.rpc.method","panelIds":["requests"]}],"panels":[{"id":"requests","section":"rpc","title":"Requests","kind":"number","signal":"traces","series":[{"label":"requests","agg":"count"}]}]}`
	got, err := ParseDashboard("sections", []byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sections) != 1 || got.Panels[0].Section != "rpc" || len(got.Filters[0].PanelIDs) != 1 {
		t.Fatalf("metadata lost: %+v", got)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"unknown-panel", `"panelIds":["requests"]`, `"panelIds":["missing"]`},
		{"duplicate-target", `"panelIds":["requests"]`, `"panelIds":["requests","requests"]`},
		{"non-string-target", `"panelIds":["requests"]`, `"panelIds":[12]`},
		{"non-array-target", `"panelIds":["requests"]`, `"panelIds":true`},
		{"undeclared-section", `"section":"rpc"`, `"section":"missing"`},
		{"bad-section-type", `"section":"rpc"`, `"section":12`},
		{"duplicate-section", `"sections":[{"id":"rpc","title":"RPC"}]`, `"sections":[{"id":"rpc","title":"RPC"},{"id":"rpc","title":"Duplicate"}]`},
		{"unknown-section-field", `"title":"RPC"`, `"title":"RPC","mode":"hidden"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseDashboard(tc.name, []byte(strings.Replace(valid, tc.from, tc.to, 1))); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
}
