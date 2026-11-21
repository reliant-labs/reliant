package obsbuiltin

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// The Overview's figures are the tabs' own statements: the text is shared,
// so the numbers cannot drift apart. (The ClickHouse population test pins
// the figures the Overview derives rather than shares.)
func TestProductOverviewSharesTheTabsStatements(t *testing.T) {
	in := fullInput()
	ov, users, product, acq := ProductOverview(in), Users(in), Product(in), Acquisition(in)
	same := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s differs from its tab's:\n%s\nwant\n%s", what, got, want)
		}
	}
	same("visitors", panel(t, ov, "visitors").SQL, panel(t, acq, "visitors").SQL)
	same("visitors trend", panel(t, ov, "visitors").TrendSQL, panel(t, acq, "visitors").TrendSQL)
	same("sign-ups", panel(t, ov, "signups").SQL, panel(t, acq, "signups").SQL)
	same("sign-ups trend", panel(t, ov, "signups").TrendSQL, panel(t, acq, "signups").TrendSQL)
	same("visit to sign-up", panel(t, ov, "conversion").SQL, panel(t, acq, "visit-to-signup").SQL)
	same("active trend", panel(t, ov, "active").TrendSQL, panel(t, users, "active").TrendSQL)
	if !strings.Contains(panel(t, ov, "active").SQL, "SELECT ("+panel(t, users, "active").SQL+`) AS "Active users"`) {
		t.Errorf("the active KPI does not lead with the Users tab's active users:\n%s", panel(t, ov, "active").SQL)
	}
	same("funnel", panel(t, ov, "funnel").SQL, panel(t, product, "funnel-activation").SQL)
	same("channel trend", panel(t, ov, "top-channels").TrendSQL, panel(t, acq, "visitors-by-channel").SQL)
	att := panel(t, ov, "attention").Attention
	figure := func(id string) dashspec.Panel {
		for _, f := range att.Figures {
			if f.ID == id {
				return f
			}
		}
		t.Fatalf("attention reads no figure %s", id)
		return dashspec.Panel{}
	}
	same("bounce", figure("attention-bounce").SQL, panel(t, product, "bounce").SQL)
	same("stickiness", figure("attention-stickiness").SQL, panel(t, users, "stickiness").SQL)
	if !strings.Contains(panel(t, ov, "active-new-returning").SQL, weeklyUsersCTEs) || !strings.Contains(panel(t, users, "lifecycle-weekly").SQL, weeklyUsersCTEs) {
		t.Error("new and returning do not read the lifecycle's weekly users")
	}
	if got, want := panel(t, ov, "active-new-returning").SubtitleSQL, panel(t, users, "new").SubtitleSQL; got == "" || got != want {
		t.Errorf("new and returning do not say \"since\" as the Users tab's New does:\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(panel(t, ov, "retention").SQL, newUsers(to)) {
		t.Error("the retention grid does not read the shared new-user source")
	}
	if att.Threshold != 0.05 || strings.Join(att.Watch, ",") != "visitors,signups,active,conversion" {
		t.Errorf("attention watches %v at %v", att.Watch, att.Threshold)
	}
}

// Every block links to a panel that exists on a dashboard the project has,
// with and without a frontend.
func TestProductOverviewDrillsResolve(t *testing.T) {
	for _, in := range []Input{fullInput(), {Project: "shop"}} {
		byID := map[string]dashspec.Dashboard{}
		for _, d := range Dashboards(in) {
			byID[d.ID] = d
		}
		ov := byID[ProductOverviewID]
		var drills []dashspec.Panel
		for _, p := range ov.Panels {
			drills = append(drills, p)
			if p.Attention != nil {
				drills = append(drills, p.Attention.Figures...)
			}
		}
		for _, p := range drills {
			if p.Kind == dashspec.KindAttention {
				continue
			}
			if p.Drill == nil {
				t.Errorf("web %v: %s links to no tab", in.Web, p.ID)
				continue
			}
			target, ok := byID[p.Drill.Dashboard]
			if !ok {
				t.Errorf("web %v: %s links to %s, which the project does not have", in.Web, p.ID, p.Drill.Dashboard)
				continue
			}
			found := false
			for _, q := range target.Panels {
				found = found || q.ID == p.Drill.Panel
			}
			if !found {
				t.Errorf("web %v: %s links to %s/%s, which does not exist", in.Web, p.ID, p.Drill.Dashboard, p.Drill.Panel)
			}
		}
	}
}

// Without a frontend there are no visitors: the headline figures are
// Accounts, Sign-ups, Active and Stickiness, the funnel starts at the
// sign-up, and no statement reads the browser's rows.
func TestProductOverviewWithoutAFrontend(t *testing.T) {
	ov := ProductOverview(Input{Project: "shop"})
	var ids []string
	for _, p := range ov.Panels {
		ids = append(ids, p.ID)
		for _, sql := range []string{p.SQL, p.TrendSQL} {
			if strings.Contains(sql, "Client IN ('web', 'landing')") {
				t.Errorf("%s reads visitors in a project with no frontend:\n%s", p.ID, sql)
			}
		}
	}
	if got := strings.Join(ids, ","); got != "accounts,signups,active,stickiness,active-new-returning,attention,funnel,retention,top-features" {
		t.Errorf("panels = %s", got)
	}
	if sql := panel(t, ov, "funnel").SQL; strings.Contains(sql, "'Visited'") || !strings.Contains(sql, "'Signed up'") {
		t.Errorf("the funnel without a frontend:\n%s", sql)
	}
	if w := panel(t, ov, "attention").Attention.Watch; strings.Join(w, ",") != "accounts,signups,active,stickiness" {
		t.Errorf("attention watches %v", w)
	}
}

// The rankings are bar lists, the lists of rows stay tables, and every
// headline figure says which way is good.
func TestTabsDrawRankingsAsBarListsAndSayWhichWayIsGood(t *testing.T) {
	in := fullInput()
	byID := map[string]dashspec.Dashboard{}
	for _, d := range Dashboards(in) {
		byID[d.ID] = d
	}
	rankings := map[string][]string{
		UsersID:       {"signups-by-provider"},
		ProductID:     {"top-pages", "entry-pages", "exit-pages", "goals", "events-by-name", "flag-exposure"},
		AcquisitionID: {"channels", "sources", "referrers", "landing-pages", "countries", "devices", "browsers", "os", "retention-by-channel"},
	}
	tables := map[string][]string{UsersID: {"auth-sources", "most-active-users"}, AcquisitionID: {"campaigns"}}
	good := map[string]map[string]dashspec.Direction{
		UsersID:       {"accounts": up, "active": up, "new": up, "returning": up, "stickiness": up, "verified-share": up},
		ProductID:     {"pageviews": up, "visits": up, "views-per-visit": up, "bounce": down, "visit-duration": up, "signups-from-visit": up},
		AcquisitionID: {"visitors": up, "visits": up, "signups": up, "visit-to-signup": up},
	}
	for dash, ids := range rankings {
		for _, id := range ids {
			if p := panel(t, byID[dash], id); p.Display == nil || p.Display.Style != dashspec.StyleBarList {
				t.Errorf("%s/%s is not a bar list", dash, id)
			}
		}
	}
	for dash, ids := range tables {
		for _, id := range ids {
			if p := panel(t, byID[dash], id); p.Display != nil {
				t.Errorf("%s/%s is drawn as %+v, want the table", dash, id, p.Display)
			}
		}
	}
	for dash, dirs := range good {
		for id, dir := range dirs {
			if p := panel(t, byID[dash], id); p.Display == nil || p.Display.Good != dir {
				t.Errorf("%s/%s: good %v, want %s", dash, id, p.Display, dir)
			}
		}
	}
	for _, id := range []string{"funnel-activation", "funnel-buy"} {
		if p := panel(t, byID[ProductID], id); p.Display.Layout != dashspec.LayoutSteps {
			t.Errorf("product/%s is not laid out in steps", id)
		}
	}
	for id, n := range map[string]int{"dau-wau-mau": 3, "lifecycle-weekly": 4} {
		if p := panel(t, byID[UsersID], id); p.Display.TopN != n {
			t.Errorf("users/%s keeps %d series, want all %d", id, p.Display.TopN, n)
		}
	}
	for _, p := range byID[ProductOverviewID].Panels {
		if p.Section == "glance" && p.Width == 6 && p.Kind == dashspec.KindSQL && (p.Display.Style != dashspec.StyleKPI || p.Display.Good != up) {
			t.Errorf("product-overview/%s is not a KPI that is good up: %+v", p.ID, p.Display)
		}
	}
}
