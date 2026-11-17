package obsbuiltin

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestAIDashboardsGolden(t *testing.T) {
	for _, d := range AIDashboards(Input{Project: "shop"}) {
		b := marshal(t, d)
		golden(t, d.ID+".json", b)
		if _, err := dashspec.ParseDashboard(d.ID+".json", b); err != nil {
			t.Errorf("%s does not validate as a spec dashboard:\n%v", d.ID, err)
		}
	}
}

// Every AI statement passes the alert-rule guard, compiles against the
// console's binding, uses only the parameters it binds, and reads otel_traces
// scoped to the dashboard's environments.
func TestEveryAIStatementIsAGuardedScopedRead(t *testing.T) {
	bound := map[string]bool{dashspec.SQLParamFrom: true, dashspec.SQLParamTo: true, dashspec.SQLParamStep: true,
		dashspec.SQLParamEnvIDs: true, dashspec.SQLParamEnvs: true, dashspec.SQLParamServices: true}
	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	sc := obsquery.Scope{Database: "t_org_a", Start: end.Add(-24 * time.Hour), End: end, EnvRestricted: true, EnvIDs: []string{"e1"}}
	for _, d := range AIDashboards(Input{Project: "shop"}) {
		if !IsAIDashboard(d.ID) || IsDashboard(d.ID) {
			t.Errorf("%s: IsAIDashboard %v, IsDashboard %v", d.ID, IsAIDashboard(d.ID), IsDashboard(d.ID))
		}
		for _, p := range d.Panels {
			for name, sql := range map[string]string{p.ID: p.SQL, p.ID + " trend": p.TrendSQL} {
				if sql == "" {
					continue
				}
				name = d.ID + "/" + name
				if p.Kind != dashspec.KindSQL {
					t.Errorf("%s: kind %s, want sql", name, p.Kind)
				}
				if err := dashspec.CheckSQL(sql); err != nil {
					t.Errorf("%s: refused by the SQL guard: %v", name, err)
				}
				if _, err := (obsquery.SQLPanel{Scope: sc, SQL: sql, StepSeconds: 3600}).Compile(); err != nil {
					t.Errorf("%s: does not compile: %v", name, err)
				}
				for _, m := range param.FindAllStringSubmatch(sql, -1) {
					if !bound[m[1]] {
						t.Errorf("%s: uses {%s}, which the console does not bind", name, m[1])
					}
				}
				if strings.Contains(sql, "product_events") || !strings.Contains(sql, "FROM otel_traces") {
					t.Errorf("%s: does not read otel_traces alone", name)
				}
				if strings.Count(sql, "FROM otel_traces") > strings.Count(sql, aiEnvScope) {
					t.Errorf("%s: an otel_traces read ignores the environment filter", name)
				}
			}
		}
	}
}

// Every section is declared, described and in order, every row of the grid
// is full, and every dashboard is in the AI group.
func TestAILayout(t *testing.T) {
	for _, d := range AIDashboards(Input{Project: "shop"}) {
		declared := map[string]int{}
		for i, s := range d.Sections {
			declared[s.ID] = i
			if s.Description == "" {
				t.Errorf("%s: section %s has no description", d.ID, s.ID)
			}
		}
		last, row := 0, 0
		for _, p := range d.Panels {
			i, ok := declared[p.Section]
			if !ok {
				t.Errorf("%s/%s: section %q is not declared", d.ID, p.ID, p.Section)
			}
			if i < last {
				t.Errorf("%s/%s: section %q after a later section's panels", d.ID, p.ID, p.Section)
			}
			if i != last && row != 0 {
				t.Errorf("%s: section %q ends on a part-filled row (%d of %d)", d.ID, d.Sections[last].ID, row, dashspec.GridColumns)
				row = 0
			}
			last = i
			if p.Width%6 != 0 {
				t.Errorf("%s/%s: width %d is not a whole column of the four-column grid", d.ID, p.ID, p.Width)
			}
			row += p.Width
			if row > dashspec.GridColumns {
				t.Errorf("%s/%s: overflows its row (%d)", d.ID, p.ID, row)
			}
			if row == dashspec.GridColumns {
				row = 0
			}
			if p.Description == "" {
				t.Errorf("%s/%s: no description", d.ID, p.ID)
			}
		}
		if row != 0 {
			t.Errorf("%s: the last row is not full (%d of %d)", d.ID, row, dashspec.GridColumns)
		}
		if d.Group != dashspec.GroupAI || d.DefaultRange == "" || dashspec.GroupOf(d) != dashspec.GroupAI {
			t.Errorf("%s: group %q, range %q", d.ID, d.Group, d.DefaultRange)
		}
	}
}

// No AI panel prices anything: a cost is only ever the reported one.
func TestAIPanelsEstimateNoCost(t *testing.T) {
	for _, d := range AIDashboards(Input{Project: "shop"}) {
		for _, p := range d.Panels {
			for _, sql := range []string{p.SQL, p.TrendSQL} {
				if strings.Contains(sql, "dictGet") || strings.Contains(sql, "price") {
					t.Errorf("%s/%s estimates a cost:\n%s", d.ID, p.ID, sql)
				}
			}
			if strings.Contains(strings.ToLower(p.Title), "cost") && !strings.Contains(p.Title, "reported") {
				t.Errorf("%s/%s: a cost title must say it is reported: %q", d.ID, p.ID, p.Title)
			}
		}
	}
}
