package obsbuiltin

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// The Overview's grid: the last overviewCohorts weekly cohorts, over their
// first overviewCohorts weeks. The whole grid is the Users dashboard's.
const overviewCohorts = 6

// overviewTop is how many rows each of the Overview's rankings keeps.
const overviewTop = 10

var productOverviewSections = []dashspec.Section{
	{ID: "glance", Title: "At a glance",
		Description: "The headline figures against the previous period, each week's active users new and returning, and the figures that moved by 5% or more."},
	{ID: "conversion", Title: "Conversion",
		Description: "How visits become sign-ups and sign-ups become active users, and where the visitors come from."},
	{ID: "use", Title: "Retention and use",
		Description: "Whether sign-ups come back week after week, and what people view and use."},
}

// The tabs an Overview block links to.
var (
	drillUsers       = dashspec.PanelDrill{Dashboard: UsersID, Label: "Users"}
	drillProduct     = dashspec.PanelDrill{Dashboard: ProductID, Label: "Product"}
	drillAcquisition = dashspec.PanelDrill{Dashboard: AcquisitionID, Label: "Acquisition"}
)

// drill links p to the tab with its detail, scrolled to panel.
func drill(p dashspec.Panel, to dashspec.PanelDrill, panel string) dashspec.Panel {
	to.Panel = panel
	p.Drill = &to
	return p
}

// kpi is one of the Overview's headline cards (dashspec.StyleKPI): its first
// figure large, with its change and its trend, which way is better, and the
// panel on the tab that has its detail.
func kpi(id, title, desc, sql, trend string, unit dashspec.Unit, color dashspec.ChartColor, to dashspec.PanelDrill, panel string) dashspec.Panel {
	p := figure("glance", id, title, desc, sql, trend, unit, color)
	p.Display.Style = dashspec.StyleKPI
	p.Display.Good = up
	p.Display.Exact = unit == dashspec.UnitCount
	return drill(p, to, panel)
}

// ProductOverview is the Product analytics hub's first tab: the product
// story on one screen. Every figure is the statement of the tab its block
// links to (the same text, or the same fragments with a LIMIT), so the
// Overview and the tabs agree: Visitors is Acquisition's, Active users and
// the retention grid are Users', the funnel is Product's.
//
// Without a browser frontend (in.Web false) it has no visitors: the headline
// figures are Accounts, Sign-ups, Active users and Stickiness, the funnel
// starts at the sign-up, and channels and pages are left out.
func ProductOverview(in Input) dashspec.Dashboard {
	a := in.Analytics
	rules := in.channelRules()
	d := dashspec.Dashboard{
		ID:    ProductOverviewID,
		Title: in.Project + ": product overview",
		Description: "The product on one screen: visitors, sign-ups and active users against the previous period, each week's new and returning users, " +
			"the figures that moved, the visit → sign-up → activation funnel, where visitors come from, whether sign-ups come back, and what people use. " +
			"Each block links to the tab with its detail, and counts what that tab counts. Hosted environments only: a local forge env up has no product_events table.",
		Group:          dashspec.GroupProduct,
		Filters:        envFilter(),
		Sections:       productOverviewSections,
		DefaultRange:   "30d",
		DefaultCompare: dashspec.ComparePeriod,
	}

	signups := kpi("signups", "Sign-ups", "Users whose first auth_signed_up is in the range. The trend is each UTC day's.",
		signupsSQL, signupsTrendSQL, dashspec.UnitCount, colorSignups, drillUsers, "signups-by-method")
	active := kpi("active", "Active users",
		"Distinct users with any signed-in product event in the range, as the Users tab counts them; under it, the daily, weekly and monthly active users "+
			"of the range's last UTC day. The trend is each UTC day's.",
		activeKPISQL, activeTrendSQL, dashspec.UnitCount, colorUsers, drillUsers, "active")
	var lead []dashspec.Panel
	if in.Web {
		lead = []dashspec.Panel{
			kpi("visitors", "Visitors", "Distinct visitor-days in the range (the platform's cookieless daily hash: one person on two days is two). The trend is each UTC day's.",
				visitorsSQL(rules), visitorsTrendSQL(rules), dashspec.UnitCount, colorVisitors, drillAcquisition, "visitors"),
			signups,
			active,
			kpi("conversion", "Visit → sign-up",
				"Sign-ups linked to a visit on their sign-up day, over visitor-days. The trend is each UTC day's.",
				visitToSignupSQL(rules), visitToSignupTrendSQL(rules), dashspec.UnitPercent, colorSignups, drillAcquisition, "visit-to-signup"),
		}
	} else {
		lead = []dashspec.Panel{
			kpi("accounts", "Accounts",
				"Accounts in the environment's identity instance, from the platform's daily snapshot; an environment with none shows its users seen in the last 90 days.",
				accountsSQL, accountsTrendSQL, dashspec.UnitCount, colorNeutral, drillUsers, "accounts"),
			signups,
			active,
			kpi("stickiness", "Stickiness (DAU / MAU)",
				"The average daily active users over the average 30-day active users across the range's days: how habitual the product is.",
				stickinessSQL, "", dashspec.UnitPercent, colorMonthly, drillUsers, "stickiness"),
		}
	}

	// "since <date>" while sign-ups are reported only from inside the range,
	// the subtitle the Users tab's New and Returning figures carry.
	newReturning := drill(sinceSignups(chart("glance", "active-new-returning", "Active users, new and returning",
		"Each ISO week's active users: new (whose sign-up, or first event where sign-ups are not reported, is in that week) and returning (everyone else active that week). "+
			"New is the Users tab's lifecycle New; returning is its Retained and Resurrected. \"since\" a day when New is counted only from it.",
		newReturningSQL, bars(true, dashspec.SeriesColors{"New": colorNew, "Returning": colorUsers}), 18, 8), from), drillUsers, "lifecycle-weekly")
	newReturning.MinStep = "7d"

	d.Panels = append(lead, newReturning, attentionPanel(in, lead))

	var funnel dashspec.Panel
	window := a.EffectiveActivationWindow()
	if in.Web {
		funnel = chart("conversion", "funnel", "Visit → sign-up → activation",
			"Visitor-days in the range, the range's sign-ups, and those that reached "+activationTitle(a)+" within "+window+" of signing up, as the Product tab counts them. "+
				"The first step counts visitor-days, the others persons.",
			activationFunnelSQL(a), &dashspec.Display{Style: dashspec.StyleFunnel, Color: colorSignups}, 12, 8)
	} else {
		funnel = chart("conversion", "funnel", "Sign-up → activation",
			"The range's sign-ups, and those that reached "+activationTitle(a)+" within "+window+" of signing up.",
			signupActivationSQL(a), &dashspec.Display{Style: dashspec.StyleFunnel, Color: colorSignups}, 24, 8)
	}
	d.Panels = append(d.Panels, drill(steps(funnel), drillProduct, "funnel-activation"))
	if in.Web {
		channels := ranking("conversion", "top-channels", "Where visitors come from",
			"Visitor-days by the channel of their first visit, the sign-ups each brought and its conversion, as the Acquisition tab counts them. "+
				"The trend is each channel's visitor-days per UTC day.",
			breakdown{in: in}.sql([]dim{{"channel", "Channel"}}, []string{"Visitors", "Sign-ups", "Conversion %"}, "", overviewTop),
			dashspec.Display{Color: colorVisitors}, 12, 8)
		channels.TrendSQL = visitorsByChannelSQL(rules)
		d.Panels = append(d.Panels, drill(channels, drillAcquisition, "channels"))
	}

	retention := drill(chart("use", "retention", "Weekly retention by sign-up cohort",
		"The last six weekly cohorts of new users, and the share of each active in its first six weeks (W0 is the sign-up week). "+
			"The Users tab has twelve.", retentionGridSQL(overviewCohorts, overviewCohorts),
		&dashspec.Display{Style: dashspec.StyleHeatmap, Color: colorUsers}, 12, 10), drillUsers, "retention-cohorts")
	retention.Unit = dashspec.UnitPercent
	features := drill(ranking("use", "top-features", "Top features",
		"The app's own product events (page views, auth and flag events left out) by the people who used them: a signed-in user, the user a same-day "+
			"sign-in links a visitor to, else the visitor-day. Empty until the app records an event of its own.",
		topFeaturesSQL(in.Activity), dashspec.Display{Color: dashspec.ColorViolet}, 6, 10), drillProduct, "events-by-name")
	if in.Web {
		pages := drill(ranking("use", "top-pages", "Top pages",
			"Routes by the visitor-days that viewed them, as the Product tab's Pages counts them. The trend is each route's per UTC day.",
			topPagesSQL, dashspec.Display{Color: colorVisitors}, 6, 10), drillProduct, "top-pages")
		pages.TrendSQL = topPagesTrendSQL
		d.Panels = append(d.Panels, retention, pages, features)
	} else {
		features.Width = 12
		d.Panels = append(d.Panels, retention, features)
	}
	return in.withActivity(d)
}

// attentionPanel is "Needs attention": the headline figures lead, and the
// figures read only here, each linked to its tab.
func attentionPanel(in Input, lead []dashspec.Panel) dashspec.Panel {
	watch := make([]string, len(lead))
	for i, p := range lead {
		watch[i] = p.ID
	}
	only := func(id, title, desc, sql string, unit dashspec.Unit, good dashspec.Direction, to dashspec.PanelDrill, panel string) dashspec.Panel {
		return drill(dashspec.Panel{ID: id, Title: title, Description: desc, Kind: dashspec.KindSQL, SQL: sql, Unit: unit,
			Display: &dashspec.Display{Good: good}}, to, panel)
	}
	figures := []dashspec.Panel{
		only("attention-activation", "Activation rate", "The share of the range's sign-ups that reached "+activationTitle(in.Analytics)+
			" within "+in.Analytics.EffectiveActivationWindow()+": the funnel's last step over its sign-ups.",
			activationRateSQL(in.Analytics), dashspec.UnitPercent, up, drillProduct, "funnel-activation"),
	}
	if in.Web {
		figures = append(figures,
			only("attention-bounce", "Bounce rate", "The share of visits with exactly one page view and nothing else.",
				bounceSQL, dashspec.UnitPercent, down, drillProduct, "bounce"),
			only("attention-stickiness", "Stickiness (DAU / MAU)", "The average daily active users over the average 30-day active users.",
				stickinessSQL, dashspec.UnitPercent, up, drillUsers, "stickiness"))
	}
	return dashspec.Panel{Section: "glance", ID: "attention", Title: "Needs attention",
		Description: "The figures that moved by 5% or more against the comparison, the ones that moved the wrong way first, then those that improved. " +
			"A declared comparison of each figure with its own earlier value, not a statistical test.",
		Kind: dashspec.KindAttention, Attention: &dashspec.Attention{Watch: watch, Figures: figures, Threshold: dashspec.DefaultAttentionThreshold},
		Width: 6, Height: 8}
}

// activeKPISQL is the Users tab's active users, followed by the daily,
// weekly and monthly active users of the range's last UTC day (the Users
// tab's DAU / WAU / MAU chart's last day; 0 when nobody was active in them).
var activeKPISQL = `SELECT (` + activeSQL + `) AS "Active users",
       sumIf(dau, day = toDate(` + to + ` - INTERVAL 1 SECOND)) AS "DAU",
       sumIf(wau, day = toDate(` + to + ` - INTERVAL 1 SECOND)) AS "WAU",
       sumIf(mau, day = toDate(` + to + ` - INTERVAL 1 SECOND)) AS "MAU"
FROM (
` + rollingUsers(30, `uniqExactIf(`+user+`, offset = 0) AS dau, uniqExactIf(`+user+`, offset < 7) AS wau, uniqExact(`+user+`) AS mau`) + `)`

// newReturningSQL is each ISO week's active users, new (its sign-up week) or
// returning: the lifecycle's classes without churn, Retained and Resurrected
// together, from the same weekly CTEs.
var newReturningSQL = `WITH
` + weeklyUsersCTEs + `
SELECT week, countIf(state = 'new') AS "New", countIf(state = 'returning') AS "Returning"
FROM (
  SELECT wk AS week, if(s.sw = wk, 'new', 'returning') AS state
  FROM u ARRAY JOIN u.wks AS wk
  LEFT JOIN s ON s.EnvId = u.EnvId AND s.UserId = u.UserId)
WHERE week >= toMonday(` + from + `) AND toDateTime(week) < ` + to + `
GROUP BY week ORDER BY week`

// visitToSignupTrendSQL is each UTC day's attributed sign-ups over its
// visitor-days.
func visitToSignupTrendSQL(rules []ChannelRule) string {
	return attributedCTEs(rules) + `
SELECT v.day AS day, round(ifNull(s.n, 0) / v.n, 4) AS "Visit to sign-up"
FROM (SELECT d AS day, count() AS n FROM first GROUP BY day) AS v
LEFT JOIN (SELECT u.d AS day, count() AS n FROM su_h AS u INNER JOIN first AS f ON f.EnvId = u.EnvId AND f.h = u.h GROUP BY day) AS s ON s.day = v.day
ORDER BY day`
}

// visitorsByChannelSQL is each UTC day's visitor-days by the channel of their
// first visit: the Acquisition tab's Visitors by channel.
func visitorsByChannelSQL(rules []ChannelRule) string {
	return with(webCTE(inRange), visitsCTE, firstCTE(rules)) + `
SELECT d AS day, channel, count() AS "Visitors" FROM first GROUP BY day, channel ORDER BY day, channel`
}

// topPagesSQL is the routes the most visitor-days viewed (the Product tab's
// Pages, by its Visitors column), and topPagesTrendSQL their visitor-days per
// UTC day.
var (
	topPagesSQL = with(webCTE(inRange)) + `
SELECT Props['route'] AS "Route", uniqExact(` + visitor + `) AS "Visitors"
FROM web WHERE EventName = 'page_viewed'
GROUP BY "Route" ORDER BY "Visitors" DESC, "Route" LIMIT ` + fmt.Sprint(overviewTop)
	topPagesTrendSQL = with(webCTE(inRange)) + `
SELECT toDate(ts) AS day, Props['route'] AS route, uniqExact(` + visitor + `) AS "Visitors"
FROM web WHERE EventName = 'page_viewed'
  AND Props['route'] IN (SELECT Props['route'] FROM web WHERE EventName = 'page_viewed' GROUP BY Props['route'] ORDER BY uniqExact(` + visitor + `) DESC, Props['route'] LIMIT ` + fmt.Sprint(overviewTop) + `)
GROUP BY day, route ORDER BY day, route`
)

// notFeatures are the product events that are not the app's own: page views
// and their engagement beacons, flag exposures, and (by prefix) every auth_
// event, the account snapshot included.
var notFeatures = []string{"page_viewed", "page_engaged", "flag_evaluated"}

// topFeaturesSQL is the app's own product events by the people who used them
// (the Product tab's Events by name, by its Persons), under the project's
// whole activity policy: the platform's events and the ones it declares are
// not activity are not features, and a person it leaves out of every user
// count (a marked user, a listed id) uses none, whether signed in or a
// visitor a same-day sign-in links to them.
func topFeaturesSQL(act obsquery.Activity) string {
	not := append(append([]string(nil), notFeatures...), act.NotActivity...)
	where := ""
	if excluded := excludedPersons(act); excluded != "" {
		where = "\nWHERE " + excluded
	}
	return with(stitchCTE(inRange), personsCTE(inRange, "EventName NOT IN ("+strings.Trim(lits(not), "[]")+") AND NOT startsWith(EventName, 'auth_')")) + `
SELECT EventName AS "Event", uniqExactIf((EnvId, person), person != '') AS "People", count() AS "Events"
FROM persons` + where + `
GROUP BY EventName HAVING "People" > 0 ORDER BY "People" DESC, "Event" LIMIT ` + fmt.Sprint(overviewTop)
}

// excludedPersons is the predicate on personsCTE's rows that leaves out the
// users the project's activity rules exclude from every user count
// (obsquery.Activity.ExcludeUsersWith, .ExcludeUserIDs), by their person key,
// so the visitor rows a same-day sign-in links to them go too. Empty when
// the rules exclude nobody.
func excludedPersons(act obsquery.Activity) string {
	var preds []string
	if len(act.ExcludeUsersWith) > 0 {
		preds = append(preds, "(EnvId, person) NOT IN (SELECT EnvId, concat('u:', UserId) FROM product_events WHERE EventName IN ("+
			strings.Trim(lits(act.ExcludeUsersWith), "[]")+") AND UserId != '' AND "+envScope+")")
	}
	if len(act.ExcludeUserIDs) > 0 {
		keys := make([]string, len(act.ExcludeUserIDs))
		for i, id := range act.ExcludeUserIDs {
			keys[i] = "u:" + id
		}
		preds = append(preds, "person NOT IN ("+strings.Trim(lits(keys), "[]")+")")
	}
	return strings.Join(preds, " AND ")
}
