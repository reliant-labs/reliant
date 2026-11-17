package obsbuiltin

import (
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// TestProductOverviewOnAPopulation runs every Overview statement over the
// population (population_clickhouse_test.go) and pins each figure twice: to
// the answer worked by hand, and to what the tab it links to answers over the
// same rows, so the Overview and its tabs must agree.
func TestProductOverviewOnAPopulation(t *testing.T) {
	ch := newClickHouse(t, "forge_obsbuiltin_population", populationSeed+w4Cohort())
	end := time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)
	start := end.Add(-30 * 24 * time.Hour)
	in := fullInput()
	byID := map[string]dashspec.Dashboard{}
	for _, d := range Dashboards(in) {
		byID[d.ID] = d
	}
	ov := byID[ProductOverviewID]
	figures := map[string]dashspec.Panel{}
	for _, f := range panel(t, ov, "attention").Attention.Figures {
		figures[f.ID] = f
	}
	runs := func(p dashspec.Panel, trend bool, env string) string {
		t.Helper()
		sql := p.SQL
		if trend {
			sql = p.TrendSQL
		}
		return ch.run(sql, start, end, env)
	}
	rows := func(r ...string) string { return strings.Join(r, "\n") }
	// equal checks the Overview's answer against the hand-worked one and
	// against its tab's (tab, already projected to the Overview's columns).
	equal := func(what, got, worked, tab string) {
		t.Helper()
		if got != worked {
			t.Errorf("%s =\n%s\nwant (worked by hand)\n%s", what, got, worked)
		}
		if got != tab {
			t.Errorf("%s =\n%s\nbut its tab answers\n%s", what, got, tab)
		}
	}
	tab := func(dash, id string, trend bool) string { return runs(panel(t, byID[dash], id), trend, "e1") }
	ovr := func(id string, trend bool) string { return runs(panel(t, ov, id), trend, "e1") }

	// The headline figures are the tabs' figures.
	equal("visitors", ovr("visitors", false), "9", tab(AcquisitionID, "visitors", false))
	equal("visitors trend", ovr("visitors", true), rows("2026-03-01\t1", "2026-03-13\t2", "2026-03-15\t1", "2026-03-16\t5"), tab(AcquisitionID, "visitors", true))
	equal("sign-ups", ovr("signups", false), "5", tab(AcquisitionID, "signups", false))
	equal("visit to sign-up", ovr("conversion", false), "0.3333", tab(AcquisitionID, "visit-to-signup", false))
	// Active users, then DAU, WAU and MAU of 03-16: the Users tab's active
	// figure and the last row of its DAU / WAU / MAU chart.
	dww := strings.Split(tab(UsersID, "dau-wau-mau", false), "\n")
	last := strings.SplitN(dww[len(dww)-1], "\t", 2)
	equal("active", ovr("active", false), "6\t3\t6\t6", tab(UsersID, "active", false)+"\t"+last[1])
	equal("active trend", ovr("active", true), tab(UsersID, "active", true), tab(UsersID, "active", true))
	// Each day's attributed sign-ups over its visitor-days: u6 on 03-01 (h10),
	// u4 and u5 on 03-16 (h1, h3) of its five.
	if got, want := ovr("conversion", true), rows("2026-03-01\t1", "2026-03-13\t0", "2026-03-15\t0", "2026-03-16\t0.4"); got != want {
		t.Errorf("visit to sign-up trend =\n%s\nwant\n%s", got, want)
	}

	// New and returning per week are the lifecycle's New, and its Retained
	// plus Resurrected.
	var fromLifecycle []string
	for _, r := range strings.Split(tab(UsersID, "lifecycle-weekly", false), "\n") {
		c := strings.Split(r, "\t")
		fromLifecycle = append(fromLifecycle, c[0]+"\t"+c[1]+"\t"+sum(t, c[2], c[3]))
	}
	equal("new and returning", ovr("active-new-returning", false),
		rows("2026-02-16\t1\t0", "2026-02-23\t2\t0", "2026-03-02\t0\t3", "2026-03-09\t0\t4", "2026-03-16\t2\t1"), rows(fromLifecycle...))

	// The funnel is the Product tab's; the activation rate its last step over
	// its sign-ups (u2, u4 and u6 of five).
	equal("funnel", ovr("funnel", false), rows("Visited\t9", "Signed up\t5", "Activated\t3"), tab(ProductID, "funnel-activation", false))
	if got := runs(figures["attention-activation"], false, "e1"); got != "0.6" {
		t.Errorf("activation rate = %s, want 0.6 (3 of 5)", got)
	}
	equal("bounce", runs(figures["attention-bounce"], false, "e1"), "0.5", tab(ProductID, "bounce", false))
	equal("stickiness", runs(figures["attention-stickiness"], false, "e1"), "0.1757", tab(UsersID, "stickiness", false))

	// Channels: the Acquisition tab's Channels, its Visitors, Sign-ups and
	// Conversion % columns.
	equal("channels", ovr("top-channels", false), rows(
		"Direct\t2\t0\t0", "AI assistants\t1\t0\t0", "Email\t1\t0\t0", "Hacker News\t1\t1\t100", "Organic search\t1\t0\t0",
		"Organic social\t1\t0\t0", "Paid search\t1\t1\t100", "Partners\t1\t1\t100", "Unattributed\t0\t2\t\\N"),
		project(tab(AcquisitionID, "channels", false), 0, 1, 4, 5))
	equal("channels trend", ovr("top-channels", true), tab(AcquisitionID, "visitors-by-channel", false), tab(AcquisitionID, "visitors-by-channel", false))

	// The retention grid is the Users tab's last six cohorts (all three here)
	// and their first six weeks.
	nulls := func(n int) string { return strings.Repeat("\t\\N", n) }
	var grid []string
	for _, r := range strings.Split(tab(UsersID, "retention-cohorts", false), "\n") {
		grid = append(grid, strings.Join(strings.Split(r, "\t")[:1+overviewCohorts], "\t"))
	}
	equal("retention", ovr("retention", false), rows(
		"2026-02-16 · 1 user\t1\t0\t1\t1\t0"+nulls(1), "2026-02-23 · 2 users\t1\t0.5\t1\t0"+nulls(2), "2026-03-16 · 2 users\t1"+nulls(5)), rows(grid...))

	// Pages: the Product tab's Pages by its Visitors; the trend each day's.
	byVisitors := project(tab(ProductID, "top-pages", false), 0, 2)
	equal("top pages", ovr("top-pages", false), rows("/\t8", "/orders\t3", "/pricing\t2", "/docs\t1", "/signup\t1"), sortedByCount(byVisitors))
	if got, want := ovr("top-pages", true), rows("2026-03-01\t/\t1", "2026-03-13\t/\t1", "2026-03-13\t/orders\t2", "2026-03-14\t/\t1", "2026-03-15\t/docs\t1",
		"2026-03-16\t/\t5", "2026-03-16\t/orders\t1", "2026-03-16\t/pricing\t2", "2026-03-16\t/signup\t1"); got != want {
		t.Errorf("top pages trend =\n%s\nwant\n%s", got, want)
	}

	// Features: the Product tab's Events by name without page views, auth
	// and flag events, by persons.
	var events []string
	for _, r := range strings.Split(tab(ProductID, "events-by-name", false), "\n") {
		c := strings.Split(r, "\t")
		if c[0] == "page_viewed" || c[0] == "flag_evaluated" || strings.HasPrefix(c[0], "auth_") {
			continue
		}
		events = append(events, c[0]+"\t"+c[2]+"\t"+c[1])
	}
	equal("top features", ovr("top-features", false), rows("order_created\t4\t4", "order_updated\t2\t2", "add_to_cart\t1\t1", "purchase\t1\t1"),
		sortedByCount(rows(events...)))

	// With no frontend: Accounts and Stickiness lead, and the funnel starts
	// at the sign-up.
	bare := ProductOverview(Input{Project: "shop", Analytics: in.Analytics})
	equal("accounts (no frontend)", runs(panel(t, bare, "accounts"), false, "e1"), "42", tab(UsersID, "accounts", false))
	equal("stickiness (no frontend)", runs(panel(t, bare, "stickiness"), false, "e1"), "0.1757", tab(UsersID, "stickiness", false))
	equal("funnel (no frontend)", runs(panel(t, bare, "funnel"), false, "e1"), rows("Signed up\t5", "Activated\t3"),
		strings.SplitN(tab(ProductID, "funnel-activation", false), "\n", 2)[1])

	// The statements that join answer the same with join_use_nulls.
	nullJoin := ch.with("--join_use_nulls=1")
	for _, id := range []string{"conversion", "top-channels", "active-new-returning", "funnel"} {
		p := panel(t, ov, id)
		for _, trend := range []bool{false, true} {
			sql := p.SQL
			if trend {
				sql = p.TrendSQL
			}
			if sql == "" {
				continue
			}
			def := ch.run(sql, start, end, "e1")
			if nul := nullJoin.run(sql, start, end, "e1"); nul != def {
				t.Errorf("%s (trend %v) with join_use_nulls=1 =\n%s\nwant\n%s", id, trend, nul, def)
			}
		}
	}
	if got := runs(figures["attention-activation"], false, "e1"); got != nullJoin.run(figures["attention-activation"].SQL, start, end, "e1") {
		t.Errorf("activation rate differs under join_use_nulls=1")
	}
}

// sum is the sum of two TSV integers.
func sum(t *testing.T, a, b string) string {
	t.Helper()
	var x, y int
	for _, c := range []struct {
		s string
		n *int
	}{{a, &x}, {b, &y}} {
		for _, r := range c.s {
			if r < '0' || r > '9' {
				t.Fatalf("not a count: %q", c.s)
			}
			*c.n = *c.n*10 + int(r-'0')
		}
	}
	return itoa(x + y)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// project is each TSV row of rows with only the columns cols, in order.
func project(rows string, cols ...int) string {
	var out []string
	for _, r := range strings.Split(rows, "\n") {
		c := strings.Split(r, "\t")
		var keep []string
		for _, i := range cols {
			keep = append(keep, c[i])
		}
		out = append(out, strings.Join(keep, "\t"))
	}
	return strings.Join(out, "\n")
}

// sortedByCount is rows (label, count, ...) ordered as a ranking: the second
// column descending, then the label.
func sortedByCount(rows string) string {
	rs := strings.Split(rows, "\n")
	n := func(r string) int {
		v := 0
		for _, ch := range strings.Split(r, "\t")[1] {
			v = v*10 + int(ch-'0')
		}
		return v
	}
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0; j-- {
			a, b := rs[j-1], rs[j]
			if n(a) > n(b) || (n(a) == n(b) && strings.Split(a, "\t")[0] <= strings.Split(b, "\t")[0]) {
				break
			}
			rs[j-1], rs[j] = b, a
		}
	}
	return strings.Join(rs, "\n")
}
