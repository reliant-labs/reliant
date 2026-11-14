package obsbuiltin

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/name, or rewrites it under -update.
// The goldens pin the text; the ClickHouse tests pin what it means.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run `go test ./pkg/obsbuiltin -update`)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file; run `go test ./pkg/obsbuiltin -update` and review the diff", name)
	}
}

func marshal(t *testing.T, d dashspec.Dashboard) []byte {
	t.Helper()
	b, err := json.MarshalIndent(struct {
		Version string `json:"version"`
		dashspec.Dashboard
	}{dashspec.SpecVersion, d}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// every is each dashboard the package builds for in, the Overview extended
// from an empty one.
func every(in Input) []dashspec.Dashboard {
	return append(Dashboards(in), ExtendOverview(dashspec.Dashboard{ID: "overview", Title: in.Project + ": overview"}, in))
}

func TestDashboardsGolden(t *testing.T) {
	for _, d := range every(fullInput()) {
		b := marshal(t, d)
		golden(t, d.ID+".json", b)
		if _, err := dashspec.ParseDashboard(d.ID+".json", b); err != nil {
			t.Errorf("%s does not validate as a spec dashboard:\n%v", d.ID, err)
		}
	}
	// The defaults: nothing declared.
	for _, d := range every(Input{Project: "shop", Web: true}) {
		b := marshal(t, d)
		golden(t, d.ID+"-defaults.json", b)
		if _, err := dashspec.ParseDashboard(d.ID+".json", b); err != nil {
			t.Errorf("%s (defaults) does not validate:\n%v", d.ID, err)
		}
	}
}

var param = regexp.MustCompile(`\{([a-z_]+):`)

// Every statement passes the alert-rule guard, compiles against the
// console's binding, uses only the parameters the console binds, reads the
// tenant tables, and is scoped to the dashboard's environments.
func TestEveryStatementIsAGuardedScopedRead(t *testing.T) {
	bound := map[string]bool{dashspec.SQLParamFrom: true, dashspec.SQLParamTo: true, dashspec.SQLParamStep: true,
		dashspec.SQLParamEnvIDs: true, dashspec.SQLParamEnvs: true, dashspec.SQLParamServices: true}
	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	sc := obsquery.Scope{Database: "t_org_a", Start: end.Add(-24 * time.Hour), End: end, EnvRestricted: true, EnvIDs: []string{"e1"}}
	for _, d := range every(fullInput()) {
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
				if !strings.Contains(sql, "FROM product_events") {
					t.Errorf("%s: does not read product_events", name)
				}
				if strings.Count(sql, "FROM product_events") > strings.Count(sql, envScope) {
					t.Errorf("%s: a product_events read ignores the environment filter", name)
				}
			}
		}
	}
}

// Visitors are keyed by the hash alone: its day is the day the edge
// received the batch, so pairing it with the event's date would split one
// visitor (and one visit) at midnight.
func TestVisitorsAreKeyedByTheHashAlone(t *testing.T) {
	for _, d := range every(fullInput()) {
		for _, p := range d.Panels {
			for _, sql := range []string{p.SQL, p.TrendSQL} {
				if regexp.MustCompile(`\(\s*(toDate\(Timestamp\)|d)\s*,\s*(h|DayVisitor)\s*\)`).MatchString(sql) {
					t.Errorf("%s/%s pairs a visitor hash with a date:\n%s", d.ID, p.ID, sql)
				}
				if strings.Contains(sql, "PARTITION BY EnvId, d,") {
					t.Errorf("%s/%s sessionises by date:\n%s", d.ID, p.ID, sql)
				}
			}
		}
	}
}

// Every section is declared, described and in order, every row of the
// console's 24-column grid is full, and a headline figure draws its colour.
func TestLayout(t *testing.T) {
	for _, d := range Dashboards(fullInput()) {
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
		if d.DefaultCompare != dashspec.ComparePeriod || d.DefaultRange == "" || d.Group != dashspec.GroupProduct {
			t.Errorf("%s: compare %q, range %q, group %q", d.ID, d.DefaultCompare, d.DefaultRange, d.Group)
		}
	}
}

func TestDashboardsFollowTheProject(t *testing.T) {
	ids := func(ds []dashspec.Dashboard) string {
		var out []string
		for _, d := range ds {
			out = append(out, d.ID)
		}
		return strings.Join(out, ",")
	}
	if got := ids(Dashboards(Input{Project: "shop", Web: true})); got != "users,product,acquisition" {
		t.Errorf("with a frontend = %s", got)
	}
	// A backend alone has users and CRUD events, and no visitors.
	if got := ids(Dashboards(Input{Project: "shop"})); got != "users,product" {
		t.Errorf("without a frontend = %s", got)
	}
	off := &Analytics{NoDashboards: true}
	if got := Dashboards(Input{Project: "shop", Web: true, Analytics: off}); got != nil {
		t.Errorf("dashboards = False still builds %s", ids(got))
	}
	ov := dashspec.Dashboard{ID: "overview", Panels: []dashspec.Panel{{ID: "requests"}}}
	if got := ExtendOverview(ov, Input{Project: "shop", Web: true, Analytics: off}); len(got.Panels) != 1 || got.Sections != nil || got.DefaultRange != "" {
		t.Errorf("dashboards = False still extends the overview: %+v", got)
	}
	if p := Product(Input{Project: "shop"}); len(p.Replaces) != 1 || p.Replaces[0] != SupersededUsageID {
		t.Errorf("product replaces %v, want the generated usage dashboard", p.Replaces)
	}
}

func TestExtendOverviewKeepsItsHealthFirst(t *testing.T) {
	ov := dashspec.Dashboard{ID: "overview", Panels: []dashspec.Panel{{ID: "requests"}, {ID: "errors"}}}
	got := ExtendOverview(ov, Input{Project: "shop", Web: true})
	if got.Panels[0].ID != "requests" || got.Panels[0].Section != "health" || got.Panels[1].Section != "health" {
		t.Errorf("health panels moved: %+v", got.Panels[:2])
	}
	var sections []string
	for _, s := range got.Sections {
		sections = append(sections, s.ID)
	}
	if strings.Join(sections, ",") != "health,traffic,users" || got.DefaultRange != "24h" {
		t.Errorf("sections %v, range %q", sections, got.DefaultRange)
	}
	if ov.Panels[0].Section != "" {
		t.Error("ExtendOverview modified its argument's panels")
	}
	// No frontend: no Traffic section.
	got = ExtendOverview(ov, Input{Project: "shop"})
	for _, p := range got.Panels {
		if p.Section == "traffic" {
			t.Fatalf("a project with no frontend has a traffic panel %s", p.ID)
		}
	}
}

// The declarations reach the panels: goals become rows, the activation event
// replaces the CRUD default, channel rules come first, product funnels become
// panels and session funnels stay the frontend dashboard's.
func TestDeclarationsReachThePanels(t *testing.T) {
	in := fullInput()
	in.Funnels = append(in.Funnels, Funnel{ID: "sess", Funnel: dashspec.Funnel{Steps: []string{"a", "b"}, By: dashspec.FunnelBySession}})
	p := Product(in)
	goals := panel(t, p, "goals").SQL
	if !strings.Contains(goals, "EventName = 'purchase'") || !strings.Contains(goals, "Props['route'] = '/pricing'") {
		t.Errorf("goals do not match the declared event and route:\n%s", goals)
	}
	if act := panel(t, p, "funnel-activation"); !strings.Contains(act.SQL, "EventName = 'order_created'") || strings.Contains(act.SQL, "_created$") {
		t.Errorf("activation does not use the declared event:\n%s", act.SQL)
	}
	panel(t, p, "funnel-buy")
	panel(t, p, "funnel-browse")
	for _, q := range p.Panels {
		if q.ID == "funnel-sess" {
			t.Error("a session funnel is drawn on the Product dashboard")
		}
	}
	for _, q := range Product(Input{Project: "shop"}).Panels {
		if q.ID == "goals" {
			t.Error("a goals panel with no goals declared")
		}
	}
	ch := panel(t, Acquisition(in), "channels").SQL
	hn, paid := strings.Index(ch, "'Hacker News'"), strings.Index(ch, "'Paid search'")
	if hn < 0 || paid < 0 || hn > paid {
		t.Errorf("custom channel rules are not tried first:\n%s", ch)
	}
}

func TestChannelRulesQuoteTheirValues(t *testing.T) {
	expr := channelExpr([]ChannelRule{{Host: "evil.com') OR (1", Channel: "x'y"}}, "ref", "src", "med", "click")
	if strings.Contains(expr, "evil.com')") || !strings.Contains(expr, `'evil.com\') or (1'`) || !strings.Contains(expr, `'x\'y'`) {
		t.Errorf("a rule's values are not quoted:\n%s", expr)
	}
	if err := dashspec.CheckSQL("SELECT " + expr); err != nil {
		t.Errorf("a quoted rule fails the guard: %v", err)
	}
}

func TestAnalyticsDefaults(t *testing.T) {
	var a *Analytics
	if !a.DashboardsEnabled() || a.EffectiveActivationWindow() != DefaultActivationWindow {
		t.Error("a nil Analytics is not every default")
	}
	if got := activationWindow(&Analytics{ActivationWindow: "30m"}); got != "INTERVAL 1800 SECOND" {
		t.Errorf("30m = %s", got)
	}
	if !IsProductFunnel(FunnelByPerson) || !IsProductFunnel(FunnelByVisitor) || IsProductFunnel(dashspec.FunnelBySession) {
		t.Error("IsProductFunnel")
	}
}

// finalSelect is the statement's last top-level SELECT list: the columns the
// panel draws (the CTEs above it name their own).
var finalSelect = regexp.MustCompile(`(?s)\nSELECT (.*?)\nFROM`)

// A panel grouped by a fixed bucket declares it (minStep), so a reader
// names the bucket from the spec rather than from its result's type: a week
// read back as a Date would otherwise be captioned a day.
func TestFixedBucketPanelsDeclareTheirStep(t *testing.T) {
	for _, d := range every(fullInput()) {
		for _, p := range d.Panels {
			sql := "\n" + p.SQL
			m := finalSelect.FindAllStringSubmatch(sql, -1)
			if len(m) == 0 {
				continue
			}
			cols := m[len(m)-1][1]
			first := strings.TrimSpace(strings.SplitN(cols, ",", 2)[0])
			var want string
			switch {
			case strings.HasSuffix(first, " AS week") || first == "week":
				want = "7d"
			case strings.HasSuffix(first, " AS day") || first == "day":
				want = "1d"
			default:
				continue
			}
			if p.MinStep != want {
				t.Errorf("%s/%s groups by %q but declares minStep %q, want %q", d.ID, p.ID, first, p.MinStep, want)
			}
		}
	}
	for _, id := range []string{"lifecycle-weekly", "signups-by-channel-weekly"} {
		for _, d := range every(fullInput()) {
			for _, p := range d.Panels {
				if p.ID == id && p.MinStep != "7d" {
					t.Errorf("%s: minStep %q, want 7d", id, p.MinStep)
				}
			}
		}
	}
}

// Counts a reader checks against another page (accounts, users) are drawn
// exactly, and "last seen" is a time the reader formats, so it sorts as one.
func TestCountFiguresAreExactAndLastSeenIsATime(t *testing.T) {
	in := fullInput()
	ov := ExtendOverview(dashspec.Dashboard{ID: "overview"}, in)
	for _, c := range []struct {
		d   dashspec.Dashboard
		ids []string
	}{{Users(in), []string{"accounts", "active", "new", "returning"}}, {ov, []string{"accounts", "active-7d", "new-7d", "returning-7d"}}} {
		for _, id := range c.ids {
			if p := panel(t, c.d, id); p.Display == nil || !p.Display.Exact {
				t.Errorf("%s/%s is not drawn exactly", c.d.ID, id)
			}
		}
	}
	for _, p := range []dashspec.Panel{panel(t, Users(in), "most-active-users"), panel(t, Product(in), "events-by-name")} {
		if strings.Contains(p.SQL, "formatDateTime") || !strings.Contains(p.SQL, `toDateTime(max(`) {
			t.Errorf("%s: last seen is not a time:\n%s", p.ID, p.SQL)
		}
	}
	if sql := panel(t, Product(in), "funnel-activation").SQL; !strings.Contains(sql, `AS "Persons"`) {
		t.Errorf("funnel-activation does not name its count Persons:\n%s", sql)
	}
}
