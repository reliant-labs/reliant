package obsbuiltin

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestCardsGolden(t *testing.T) {
	in := fullInput()
	in.Uptime = true
	cards := Cards(in)
	if len(cards) != 2 {
		t.Fatalf("Cards = %d dashboards, want Traffic and Uptime", len(cards))
	}
	for _, d := range cards {
		b := marshal(t, d)
		golden(t, d.ID+".json", b)
		if _, err := dashspec.ParseDashboard(d.ID+".json", b); err != nil {
			t.Errorf("%s does not validate as a spec dashboard:\n%v", d.ID, err)
		}
	}
}

func TestCardsFollowTheProject(t *testing.T) {
	ids := func(in Input) string {
		var out []string
		for _, d := range Cards(in) {
			out = append(out, d.ID)
			if !IsCardDashboard(d.ID) || IsDashboard(d.ID) {
				t.Errorf("%s: IsCardDashboard %v, IsDashboard %v", d.ID, IsCardDashboard(d.ID), IsDashboard(d.ID))
			}
		}
		return strings.Join(out, ",")
	}
	for _, c := range []struct {
		in   Input
		want string
	}{
		{Input{Project: "shop", Web: true, Uptime: true}, "traffic,uptime"},
		{Input{Project: "shop", Web: true}, "traffic"},
		{Input{Project: "shop", Uptime: true}, "uptime"},
		{Input{Project: "shop"}, ""},
		// Opting out of analytics drops Traffic, not Uptime.
		{Input{Project: "shop", Web: true, Uptime: true, Analytics: &Analytics{NoDashboards: true}}, "uptime"},
	} {
		if got := ids(c.in); got != c.want {
			t.Errorf("Cards(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
	// The hub's dashboards are unchanged: the cards are not among them.
	for _, d := range Dashboards(Input{Project: "shop", Web: true, Uptime: true}) {
		if IsCardDashboard(d.ID) {
			t.Errorf("Dashboards includes the card dashboard %s", d.ID)
		}
	}
}

// The ids are a contract with the console, which reads the cards by id: a
// rename breaks every console that shipped against it. Add, never rename.
func TestCardIDsAreStable(t *testing.T) {
	want := map[string][]string{
		TrafficID: {"live-visitors", "visitors", "pageviews", "bounce", "visit-duration", "pages", "entry-pages", "exit-pages",
			"channels", "referrers", "campaigns", "countries", "devices", "browsers", "os"},
		UptimeID: {"uptime-summary", "uptime-timeline", "uptime-checks", "uptime-incidents"},
	}
	in := Input{Project: "shop", Web: true, Uptime: true}
	for _, d := range Cards(in) {
		var got []string
		for _, p := range d.Panels {
			got = append(got, p.ID)
		}
		if strings.Join(got, ",") != strings.Join(want[d.ID], ",") {
			t.Errorf("%s panels = %v, want %v", d.ID, got, want[d.ID])
		}
	}
	var params []string
	for _, p := range TrafficFilterParams {
		params = append(params, p.ID)
	}
	if got := strings.Join(params, ","); got != "f_route,f_entry,f_exit,f_channel,f_referrer,f_campaign,f_country,f_device,f_browser,f_os" {
		t.Errorf("filter params = %s", got)
	}
}

// Every card statement passes the alert-rule guard, compiles against the
// console's binding with and without filters, uses only the parameters the
// console binds and the filter params its dashboard declares, and scopes
// every read of a tenant table to the dashboard's environments.
func TestEveryCardIsAGuardedScopedRead(t *testing.T) {
	builtin := map[string]bool{dashspec.SQLParamFrom: true, dashspec.SQLParamTo: true, dashspec.SQLParamStep: true,
		dashspec.SQLParamEnvIDs: true, dashspec.SQLParamEnvs: true, dashspec.SQLParamServices: true}
	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	for _, d := range Cards(Input{Project: "shop", Web: true, Uptime: true, Analytics: fullInput().Analytics}) {
		declared := map[string]bool{}
		set := map[string]string{}
		for _, f := range d.FilterParams {
			declared[f.ID] = true
			set[f.ID] = "x' OR 1=1 --"
		}
		for _, p := range d.Panels {
			for name, sql := range map[string]string{p.ID: p.SQL, p.ID + " trend": p.TrendSQL} {
				if sql == "" {
					continue
				}
				name = d.ID + "/" + name
				if err := dashspec.CheckSQL(sql); err != nil {
					t.Errorf("%s: refused by the SQL guard: %v", name, err)
				}
				for _, filters := range []map[string]string{nil, set} {
					sc := obsquery.Scope{Database: "t_org_a", Start: end.Add(-24 * time.Hour), End: end, EnvRestricted: true, EnvIDs: []string{"e1"},
						FilterParams: filters}
					st, err := (obsquery.SQLPanel{Scope: sc, SQL: sql, StepSeconds: 3600}).Compile()
					if err != nil {
						t.Errorf("%s: does not compile: %v", name, err)
						continue
					}
					if st.SQL != sql {
						t.Errorf("%s: compiling changed the statement", name)
					}
				}
				for _, u := range dashspec.UsedSQLParams(sql) {
					if !builtin[u.Name] && !declared[u.Name] {
						t.Errorf("%s: uses {%s}, which the console does not bind", name, u.Name)
					}
				}
				table := "FROM product_events"
				scope := envScope
				if d.ID == UptimeID {
					table, scope = "FROM otel_metrics_gauge", "has({env_ids:Array(String)}, ResourceAttributes['reliant.env.id'])"
				}
				if !strings.Contains(sql, table) {
					t.Errorf("%s: does not read %s", name, strings.TrimPrefix(table, "FROM "))
				}
				if strings.Count(sql, table) > strings.Count(sql, scope) {
					t.Errorf("%s: a read ignores the environment filter", name)
				}
			}
			if p.TrendSQL != "" && !strings.Contains(p.TrendSQL, "\nSELECT toStartOfInterval(") {
				t.Errorf("%s/%s: the trend is not bucketed by {step}", d.ID, p.ID)
			}
		}
	}
}

// Every Traffic card honours every filter param, and a breakdown's select
// names one of the dashboard's params and a column its statement returns.
func TestTrafficCardsHonourEveryFilter(t *testing.T) {
	d := Traffic(Input{Project: "shop", Web: true})
	for _, p := range d.Panels {
		for _, f := range d.FilterParams {
			if !strings.Contains(p.SQL, "{"+f.ID+":String}") {
				t.Errorf("%s ignores %s", p.ID, f.ID)
			}
		}
		if p.Section == "headline" {
			if p.Select != nil {
				t.Errorf("headline %s sets a filter", p.ID)
			}
			continue
		}
		if p.Select == nil {
			t.Errorf("breakdown %s sets no filter", p.ID)
			continue
		}
		m := finalSelect.FindAllStringSubmatch("\n"+p.SQL, -1)
		if len(m) == 0 || !strings.Contains(m[len(m)-1][1], `AS "`+p.Select.Column+`"`) {
			t.Errorf("%s selects column %q, which its statement does not return", p.ID, p.Select.Column)
		}
		for _, col := range []string{`AS "Visitors"`, `AS "Visitors %"`} {
			if !strings.Contains(p.SQL, col) {
				t.Errorf("%s returns no %s", p.ID, col)
			}
		}
	}
}

// Each card dashboard fills every row of the console's 24-column grid, its
// sections declared and in order, and every panel says how it is counted.
func TestCardLayout(t *testing.T) {
	for _, d := range Cards(Input{Project: "shop", Web: true, Uptime: true}) {
		declared := map[string]int{"": -1}
		for i, s := range d.Sections {
			declared[s.ID] = i
		}
		last, row := -1, 0
		for _, p := range d.Panels {
			i, ok := declared[p.Section]
			if !ok || i < last {
				t.Errorf("%s/%s: section %q undeclared or out of order", d.ID, p.ID, p.Section)
			}
			if i != last && row != 0 {
				t.Errorf("%s: a section ends on a part-filled row", d.ID)
			}
			last = i
			if row += p.Width; row > dashspec.GridColumns {
				t.Errorf("%s/%s: overflows its row", d.ID, p.ID)
			} else if row == dashspec.GridColumns {
				row = 0
			}
			if p.Description == "" {
				t.Errorf("%s/%s: no description", d.ID, p.ID)
			}
		}
		if row != 0 {
			t.Errorf("%s: the last row is not full", d.ID)
		}
	}
}
