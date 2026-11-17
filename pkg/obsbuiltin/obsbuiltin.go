// Package obsbuiltin builds forge's built-in product analytics dashboards:
// the Traffic and Users sections of a project's Overview, and the Product
// analytics hub's Overview, Users, Product and Acquisition dashboards. Every
// panel is a sql panel over the hosted per-org product_events table (and, for one figure, error_events), in
// the pkg/dashspec vocabulary, so the Reliant console draws them with no
// chart code of its own.
//
// forge generate writes them into the project's dashboard spec, and forge env
// build ships them in the release bundle, exactly as every other generated
// dashboard. They live in a public package so the control plane can
// synthesise the same definitions for an environment with no bundle (one
// never promoted, or running an older forge): one builder, one answer.
//
// The definitions are the product-analytics design (§2) made executable:
//
//   - a visitor is the ingest edge's cookieless daily hash (DayVisitor),
//     keyed by the hash alone: its day is the day the edge received the
//     batch, not the event's;
//   - a visit is one visitor's events with gaps of 30 minutes or less;
//   - a user is a UserId per environment, active by obsquery.UserActivityEvidence
//     and new by obsquery.NewUsersSource, the two fragments the console's
//     Users card compiles from, so the card and the dashboards agree;
//   - a sign-up is attributed to the first touch of its own day (its own
//     visitor hash, else the one hash a same-day sign-in links to the user),
//     and is Unattributed, never Direct, when there is none.
//
// The project extends them by declaration (forge.Analytics: goals, an
// activation event and window, channel rules; forge.Funnel{by = "visitor" |
// "person"}), overrides one by a dashboard file of the same name
// (deploy/observability/dashboards/<project>-users.json), and drops them all
// with forge.Analytics{dashboards = False}.
package obsbuiltin

import (
	"strings"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// The ids of the built-in product dashboards.
const (
	// ProductOverviewID is the Product analytics hub's first tab, the
	// product story on one screen (not the project's "overview" dashboard,
	// which ExtendOverview extends).
	ProductOverviewID = "product-overview"
	UsersID           = "users"
	ProductID         = "product"
	AcquisitionID     = "acquisition"
	// SupersededUsageID is the generated "Product usage" dashboard the
	// Product dashboard replaces (Dashboard.Replaces), so a reader moves its
	// stars and copies.
	SupersededUsageID = "usage"
)

// IsDashboard reports whether id is one of the built-in product dashboards.
// They read product_events, which only a hosted environment has.
func IsDashboard(id string) bool {
	return id == ProductOverviewID || id == UsersID || id == ProductID || id == AcquisitionID
}

// Input is what the builders read.
type Input struct {
	// Project names the dashboards ("<project>: users").
	Project string
	// Web says the project has a browser frontend (in this repository or
	// built from another): the Overview's Traffic section and the
	// Acquisition dashboard are drawn only for one.
	Web bool
	// Analytics is the project's declared product analytics; nil is every
	// default.
	Analytics *Analytics
	// Funnels are the project's declared funnels; the Product dashboard draws
	// the ones IsProductFunnel counts.
	Funnels []Funnel
	// Counts are the project's declared event counts (forge.Count), drawn on
	// the Product dashboard.
	Counts []Count
	// Activity is the project's declared user-activity rules
	// (forge.ProjectAnalytics.not_activity, .exclude_users_with and
	// .exclude_user_ids): every user count reads obsquery.ActiveEvidenceFor
	// them. Zero: the convention alone.
	Activity obsquery.Activity
}

// Dashboards are the built-in product dashboards for in, in the hub's tab
// order: the Overview, Users and Product always, Acquisition when the project
// has a browser frontend. Empty when the project opted out
// (Analytics.DashboardsEnabled).
func Dashboards(in Input) []dashspec.Dashboard {
	if !in.Analytics.DashboardsEnabled() {
		return nil
	}
	out := []dashspec.Dashboard{ProductOverview(in), Users(in), Product(in)}
	if in.Web {
		out = append(out, Acquisition(in))
	}
	return out
}

// withActivity compiles the project's user-activity rules (in.Activity) into
// d. Every builder writes obsquery.UserActivityEvidence, the one predicate a user
// count reads (obsquery.NewUsersSource embeds it too), so the rules replace
// that fragment wherever it appears, and every panel, the Overview's Users
// card included, counts the same users as `forge obs users`.
func (in Input) withActivity(d dashspec.Dashboard) dashspec.Dashboard {
	if in.Activity.IsZero() {
		return d
	}
	evidence, err := obsquery.ActiveEvidenceFor("product_events", in.Activity)
	if err != nil { // the table is a constant: unreachable
		return d
	}
	d.Panels = withEvidence(d.Panels, evidence)
	return d
}

// withEvidence is panels reading evidence wherever they read
// obsquery.UserActivityEvidence, the figures an attention panel reads alone
// included.
func withEvidence(panels []dashspec.Panel, evidence string) []dashspec.Panel {
	out := make([]dashspec.Panel, len(panels))
	for i, p := range panels {
		p.SQL = strings.ReplaceAll(p.SQL, obsquery.UserActivityEvidence, evidence)
		p.TrendSQL = strings.ReplaceAll(p.TrendSQL, obsquery.UserActivityEvidence, evidence)
		if p.Attention != nil {
			a := *p.Attention
			a.Figures = withEvidence(a.Figures, evidence)
			p.Attention = &a
		}
		out[i] = p
	}
	return out
}

// envFilter is the built-in dashboards' one dropdown. They have no service
// filter: a visitor and a user span the frontend and the backend.
func envFilter() []dashspec.Filter {
	return []dashspec.Filter{{ID: "env", Label: "deployment.environment.name", Field: dashspec.FieldEnv}}
}

// figure is a headline figure: one row of figures, with its trend across the
// range drawn under it when trend is set.
func figure(section, id, title, desc, sql, trend string, unit dashspec.Unit, color dashspec.ChartColor) dashspec.Panel {
	return dashspec.Panel{Section: section, ID: id, Title: title, Description: desc, Kind: dashspec.KindSQL, SQL: sql, TrendSQL: trend,
		Unit: unit, Display: &dashspec.Display{Color: color}, Width: 6, Height: 4}
}

// exact keeps a count figure's value in full (1,284, not 1.3k): a reader
// compares it with another page that states it exactly.
func exact(p dashspec.Panel) dashspec.Panel {
	d := *p.Display
	d.Exact = true
	p.Display = &d
	return p
}

// chart is a sql panel drawn as display, width w.
func chart(section, id, title, desc, sql string, display *dashspec.Display, w, h int) dashspec.Panel {
	return dashspec.Panel{Section: section, ID: id, Title: title, Description: desc, Kind: dashspec.KindSQL, SQL: sql, Display: display, Width: w, Height: h}
}

// table is a sql panel drawn as a table.
func table(section, id, title, desc, sql string, w, h int) dashspec.Panel {
	return dashspec.Panel{Section: section, ID: id, Title: title, Description: desc, Kind: dashspec.KindSQL, SQL: sql, Width: w, Height: h}
}

// ranking is a sql panel drawn as a bar list (dashspec.StyleBarList): rows
// ranked by their first value column unless display names another (Bar). A
// reader that predates the style draws the rows as the table they were.
func ranking(section, id, title, desc, sql string, display dashspec.Display, w, h int) dashspec.Panel {
	display.Style = dashspec.StyleBarList
	return dashspec.Panel{Section: section, ID: id, Title: title, Description: desc, Kind: dashspec.KindSQL, SQL: sql, Unit: dashspec.UnitCount,
		Display: &display, Width: w, Height: h}
}

// steps lays a funnel out as steps on a log scale (dashspec.LayoutSteps):
// the built-in funnels' first step counts visitors and the later ones people,
// orders of magnitude apart, which bars scaled to the first step flatten.
func steps(p dashspec.Panel) dashspec.Panel {
	d := *p.Display
	d.Layout = dashspec.LayoutSteps
	p.Display = &d
	return p
}

// withGood says which way each figure of panels named in good is better
// (Display.Good), so a reader colours its change and an attention panel can
// watch it. It is applied after a builder lays its panels out, so the
// statements stay where they are.
func withGood(panels []dashspec.Panel, good map[string]dashspec.Direction) []dashspec.Panel {
	out := make([]dashspec.Panel, len(panels))
	for i, p := range panels {
		if dir, ok := good[p.ID]; ok {
			var d dashspec.Display
			if p.Display != nil {
				d = *p.Display
			}
			d.Good = dir
			p.Display = &d
		}
		out[i] = p
	}
	return out
}

// The directions, short.
const (
	up   = dashspec.GoodUp
	down = dashspec.GoodDown
)

// bars draws a sql result as bars, one colour per value column (or per
// series of a long result), stacked when stack is set.
func bars(stack bool, colors dashspec.SeriesColors) *dashspec.Display {
	d := &dashspec.Display{Style: dashspec.StyleBars, Stack: stack}
	if colors != nil {
		d.Colors = &colors
	}
	return d
}

// The colours one concept keeps on every built-in dashboard: users blue, the
// rolling windows cyan and violet, new users amber, visitors and traffic
// cyan, sign-ups fuchsia, anonymous slate.
const (
	colorUsers    = dashspec.ColorBlue
	colorWeekly   = dashspec.ColorCyan
	colorMonthly  = dashspec.ColorViolet
	colorNew      = dashspec.ColorAmber
	colorVisitors = dashspec.ColorCyan
	colorSignups  = dashspec.ColorFuchsia
	colorNeutral  = dashspec.ColorSlate
	colorDanger   = dashspec.ColorDanger
)
