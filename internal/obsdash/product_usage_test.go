package obsdash

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestProductUsageDashboardGolden(t *testing.T) {
	d := withReleaseMarkers(BuildProductUsage("shop"))
	b, err := marshalDashboardFile(d)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "shop-usage_gen.spec.json", b)
	if _, err := ParseDashboard("shop-usage_gen.json", b); err != nil {
		t.Errorf("the product usage dashboard does not validate as a spec dashboard:\n%v", err)
	}
}

// Every panel's statement must pass what the console runs a sql panel through:
// the alert-rule guard (dashspec.CheckSQL) and obsquery's compile, which binds
// the dashboard scope. A statement that names a parameter the scope does not
// bind would fail at query time in a customer's console, so that is checked
// too.
func TestProductUsagePanelsPassTheSQLGuard(t *testing.T) {
	sc := obsquery.Scope{Database: "t_org_a", Start: time.Unix(1700000000, 0), End: time.Unix(1700086400, 0),
		EnvRestricted: true, EnvIDs: []string{"e1"}, Envs: []obsquery.Env{{ID: "e1", Name: "prod"}}}
	param := regexp.MustCompile(`\{([a-z_]+):[^}]+\}`)
	d := BuildProductUsage("shop")
	if len(d.Panels) == 0 {
		t.Fatal("no panels")
	}
	for _, p := range d.Panels {
		if p.Kind != KindSQL {
			t.Errorf("%s: kind %s, want sql (product_events is read with sql panels)", p.ID, p.Kind)
			continue
		}
		// A figure's trend is run exactly as its statement is, so it is held
		// to the same rules.
		stmts := map[string]string{p.ID: p.SQL}
		if p.TrendSQL != "" {
			stmts[p.ID+" trendSql"] = p.TrendSQL
		}
		for name, sql := range stmts {
			if err := dashspec.CheckSQL(sql); err != nil {
				t.Errorf("%s: refused by the SQL guard: %v\n%s", name, err, sql)
			}
			st, err := obsquery.SQLPanel{Scope: sc, SQL: sql, StepSeconds: 3600}.Compile()
			if err != nil {
				t.Errorf("%s: does not compile: %v", name, err)
				continue
			}
			for _, m := range param.FindAllStringSubmatch(sql, -1) {
				if _, ok := st.Params[m[1]]; !ok {
					t.Errorf("%s: uses {%s}, which the console does not bind", name, m[1])
				}
			}
			if !strings.Contains(sql, "FROM product_events") {
				t.Errorf("%s: does not read product_events", name)
			}
			// Scoped to the viewer's environments and services, always.
			if !strings.Contains(sql, "{env_ids:Array(String)}") || !strings.Contains(sql, "{services:Array(String)}") {
				t.Errorf("%s: ignores the environment or service filter", name)
			}
			// Generic: nothing of one product's own vocabulary.
			for _, word := range []string{"internal_user", "message_sent", "workflow_", "reliant"} {
				if strings.Contains(sql, word) {
					t.Errorf("%s: carries %q, which belongs to one product, not every forge project", name, word)
				}
			}
		}
	}
}

// The automatic CRUD events are matched by suffix, never by a list of
// entities, so an entity the project adds later shows without a regenerate;
// and pages are read from page_viewed's route.
func TestProductUsageReadsTheAutomaticEvents(t *testing.T) {
	panels := map[string]Panel{}
	for _, p := range BuildProductUsage("shop").Panels {
		panels[p.ID] = p
	}
	for _, id := range []string{"records-by-entity", "actions-per-entity", "records-written"} {
		if !strings.Contains(panels[id].SQL, "match(EventName, '_(created|updated|deleted)$')") {
			t.Errorf("%s does not select the CRUD events by suffix:\n%s", id, panels[id].SQL)
		}
	}
	if sql := panels["top-pages"].SQL; !strings.Contains(sql, "EventName = 'page_viewed'") || !strings.Contains(sql, "Props['route']") {
		t.Errorf("top-pages does not read page_viewed by route:\n%s", sql)
	}
	for _, id := range []string{"dau", "wau", "mau", "stickiness", "new-users", "events", "active-users-over-time", "new-vs-returning",
		"retention-curve", "weekly-retention", "most-active-users", "events-by-name"} {
		if _, ok := panels[id]; !ok {
			t.Errorf("missing panel %s", id)
		}
	}
}

// Retention reads at most twelve weekly cohorts: the default product_events
// retention is 90 days, so a thirteenth week would be a partial cohort.
func TestProductUsageRetentionFitsTheDefaultRetention(t *testing.T) {
	sql := weeklyRetentionSQL()
	if retentionWeeks*7 > 90 {
		t.Fatalf("%d weeks exceed the 90-day default retention", retentionWeeks)
	}
	if !strings.Contains(sql, `AS "W11"`) || strings.Contains(sql, `"W12"`) {
		t.Errorf("retention should report weeks 1..11 after the cohort week:\n%s", sql)
	}
	if !strings.Contains(sql, "toMonday({to:DateTime64(9)}) - INTERVAL 11 WEEK") {
		t.Errorf("retention should keep the 12 cohorts before the end of the range:\n%s", sql)
	}
}

// First-seen is read from a fixed window before the end of the range, never
// from whatever the table still holds: product_events expires rows, so the
// oldest row would make a long-time user new again the day their earlier
// events aged out. The window fits the default retention, and covers every
// retention cohort.
func TestProductUsageFirstSeenIsBoundedToAWindow(t *testing.T) {
	if firstSeenDays > 90 {
		t.Fatalf("first-seen window of %d days exceeds the 90-day default retention", firstSeenDays)
	}
	if firstSeenDays < retentionWeeks*7 {
		t.Fatalf("first-seen window of %d days does not cover the %d retention cohorts", firstSeenDays, retentionWeeks)
	}
	for name, c := range map[string]struct{ sql, bound string }{
		"new-vs-returning": {newVsReturningSQL, "Timestamp >= least({from:DateTime64(9)}, {to:DateTime64(9)} - INTERVAL 90 DAY) AND Timestamp < {to:DateTime64(9)}"},
		"new-users":        {newUsersSQL, "Timestamp >= least({from:DateTime64(9)}, {to:DateTime64(9)} - INTERVAL 90 DAY) AND Timestamp < {to:DateTime64(9)}"},
		// Retention's window ignores the range, as its cohorts do.
		"weekly-retention": {weeklyRetentionSQL(), "Timestamp >= {to:DateTime64(9)} - INTERVAL 90 DAY AND Timestamp < {to:DateTime64(9)}"},
		"retention-curve":  {retentionCurveSQL(), "Timestamp >= {to:DateTime64(9)} - INTERVAL 90 DAY AND Timestamp < {to:DateTime64(9)}"},
	} {
		sql, bound := c.sql, c.bound
		if strings.Count(sql, "FROM product_events") != strings.Count(sql, "Timestamp >= ") {
			t.Errorf("%s: a product_events read has no lower time bound:\n%s", name, sql)
		}
		if !strings.Contains(sql, bound) {
			t.Errorf("%s: first-seen is not bounded to the %d-day window:\n%s", name, firstSeenDays, sql)
		}
	}
}

// The first screen is a designed layout, not a list: two full rows of four
// headline figures, each with a trend and a colour, then charts in rows that
// fill the console's four-column grid exactly, every panel in a declared and
// described section, the panels in their sections' order (so every renderer
// draws the same page), and retention as a full-width heatmap.
func TestProductUsageLayout(t *testing.T) {
	d := BuildProductUsage("shop")
	if n := len(d.Panels); n < 12 || n > 18 {
		t.Errorf("%d panels: the dashboard should stay a readable 12 to 18", n)
	}
	if d.DefaultCompare != dashspec.ComparePeriod {
		t.Errorf("defaultCompare = %q: the headline figures should open beside their change", d.DefaultCompare)
	}
	sections := map[string]int{}
	for i, s := range d.Sections {
		sections[s.ID] = i
		if s.Description == "" {
			t.Errorf("section %s has no description under its heading", s.ID)
		}
	}
	row, section := 0, 0
	for i, p := range d.Panels {
		at, ok := sections[p.Section]
		if !ok {
			t.Errorf("%s: section %q is not declared", p.ID, p.Section)
		}
		if at < section {
			t.Errorf("%s: section %q after a later section's panels", p.ID, p.Section)
		}
		section = at
		if p.Width%6 != 0 || p.Width == 0 {
			t.Errorf("%s: width %d is not a whole column of the console's four-column grid", p.ID, p.Width)
		}
		row += p.Width
		if row > dashspec.GridColumns {
			t.Errorf("%s: overflows its row (%d of %d)", p.ID, row, dashspec.GridColumns)
		}
		if row == dashspec.GridColumns {
			row = 0
		}
		if i < 8 {
			// A headline figure: one row of one value, drawn as a number, with
			// its trend in its colour.
			if p.Section != "glance" || p.Width != 6 || p.Display == nil || p.Display.Style != "" || p.Display.Color == "" || p.TrendSQL == "" {
				t.Errorf("%s: panel %d should be a quarter-width headline figure with a trend and a colour", p.ID, i)
			}
			continue
		}
		if p.TrendSQL != "" {
			t.Errorf("%s: only a headline figure draws a trend", p.ID)
		}
		if p.Section == "glance" {
			t.Errorf("%s: a headline figure after the headline rows", p.ID)
		}
	}
	if row != 0 {
		t.Errorf("the last row is not full (%d of %d)", row, dashspec.GridColumns)
	}
	// The charts are drawn as charts: an area, stacked bars, a heatmap.
	want := map[string]string{
		"active-users-over-time": "area", "new-vs-returning": "bars+stack", "records-by-entity": "bars+stack",
		"actions-per-entity": "bars+stack", "top-pages": "bars+stack", "events-by-name": "bars+stack",
		"weekly-retention": "heatmap",
	}
	for _, p := range d.Panels {
		w, ok := want[p.ID]
		if !ok {
			continue
		}
		got := ""
		if p.Display != nil {
			got = string(p.Display.Style)
			if p.Display.Stack {
				got += "+stack"
			}
		}
		if got != w {
			t.Errorf("%s draws as %q, want %q", p.ID, got, w)
		}
	}
}

// Colours are deliberate: every chart of several fixed series names each one,
// and one concept is one colour across panels (the DAU figure and the daily
// active area, new users and the "New" bars, signed in on every split).
func TestProductUsageColours(t *testing.T) {
	panels := map[string]Panel{}
	for _, p := range BuildProductUsage("shop").Panels {
		panels[p.ID] = p
	}
	colour := func(id, series string) dashspec.ChartColor {
		t.Helper()
		p, ok := panels[id]
		if !ok || p.Display == nil {
			t.Fatalf("%s has no display", id)
		}
		if series == "" {
			return p.Display.Color
		}
		c, ok := p.Display.Colors.Color(series)
		if !ok {
			t.Errorf("%s does not colour %q", id, series)
		}
		return c
	}
	for _, same := range [][2][2]string{
		{{"dau", ""}, {"active-users-over-time", "Daily active"}},
		{{"wau", ""}, {"active-users-over-time", "Weekly active (rolling 7 days)"}},
		{{"new-users", ""}, {"new-vs-returning", "New"}},
		{{"top-pages", "Signed in"}, {"events-by-name", "Signed in"}},
		{{"top-pages", "Anonymous"}, {"events-by-name", "Anonymous"}},
		{{"records-written", ""}, {"actions-per-entity", "Created"}},
	} {
		if a, b := colour(same[0][0], same[0][1]), colour(same[1][0], same[1][1]); a != b || a == "" {
			t.Errorf("%v is %q but %v is %q: one concept, one colour", same[0], a, same[1], b)
		}
	}
	for id, series := range map[string][]string{
		"actions-per-entity": {"Created", "Updated", "Deleted"},
		"new-vs-returning":   {"New", "Returning"},
		"retention-curve":    {"Week 1", "Week 2", "Week 4", "Week 8"},
	} {
		seen := map[dashspec.ChartColor]string{}
		for _, s := range series {
			c := colour(id, s)
			if c == dashspec.ColorDanger {
				t.Errorf("%s colours %q danger: none of these is a failure", id, s)
			}
			if other, dup := seen[c]; dup {
				t.Errorf("%s colours %q and %q both %s", id, s, other, c)
			}
			seen[c] = s
		}
	}
	if c := colour("weekly-retention", ""); c == "" {
		t.Error("the retention heatmap has no colour scale")
	}
}
