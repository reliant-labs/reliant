package obsbuiltin

import (
	"fmt"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// The Overview's sections. Its existing health panels move under the first;
// Traffic (projects with a browser frontend) and Users follow.
var (
	overviewHealth = dashspec.Section{ID: "health", Title: "Health",
		Description: "Request rate, faults and latency of the deployed processes, their saturation and restarts, and recent error logs."}
	overviewTraffic = dashspec.Section{ID: "traffic", Title: "Traffic",
		Description: "Cookieless visitors to the project's frontends, counted by the platform's daily visitor hash: visitors, page views, bounce, top pages and channels, and JS errors per page view."}
	overviewUsers = dashspec.Section{ID: "users", Title: "Users",
		Description: "The console's Users card in numbers: active, new and returning users over the last 7 days (whatever range is selected), daily active users and accounts."}
)

// ExtendOverview adds the built-in Traffic and Users sections to a project's
// overview d: the panels it already has become its Health section, which
// stays first. d is returned unchanged when the project opted out
// (Analytics.DashboardsEnabled). It opens on 24 hours unless d names a range.
//
// The sections are sql panels the local HyperDX render leaves out, so a
// local stack's overview is unchanged.
func ExtendOverview(d dashspec.Dashboard, in Input) dashspec.Dashboard {
	if !in.Analytics.DashboardsEnabled() {
		return d
	}
	panels := make([]dashspec.Panel, 0, len(d.Panels)+16)
	for _, p := range d.Panels {
		if p.Section == "" {
			p.Section = overviewHealth.ID
		}
		panels = append(panels, p)
	}
	sections := append([]dashspec.Section{}, d.Sections...)
	if !hasSection(sections, overviewHealth.ID) {
		sections = append([]dashspec.Section{overviewHealth}, sections...)
	}
	if in.Web {
		sections = append(sections, overviewTraffic)
		panels = append(panels, trafficPanels(in.channelRules())...)
	}
	sections = append(sections, overviewUsers)
	panels = append(panels, overviewUserPanels()...)
	d.Sections, d.Panels = sections, panels
	if d.DefaultRange == "" {
		d.DefaultRange = "24h"
	}
	return in.withActivity(d)
}

func hasSection(ss []dashspec.Section, id string) bool {
	for _, s := range ss {
		if s.ID == id {
			return true
		}
	}
	return false
}

func trafficPanels(rules []ChannelRule) []dashspec.Panel {
	web := webCTE(inRange)
	visits := with(web, visitsCTE)
	topPages := chart("traffic", "top-pages-5", "Top pages", "The five routes the most visitors viewed in the range.",
		with(web)+`
SELECT Props['route'] AS route, uniqExact(`+visitor+`) AS "Visitors"
FROM web WHERE EventName = 'page_viewed'
GROUP BY route ORDER BY "Visitors" DESC, route LIMIT 5`, bars(false, dashspec.SeriesColors{"Visitors": colorVisitors}), 9, 7)
	topChannels := chart("traffic", "top-channels-5", "Top channels",
		"The five channels the most visitors came from: each visitor-day counted once, by the channel of its first visit.",
		with(web, visitsCTE, firstCTE(rules))+`
SELECT channel, count() AS "Visitors"
FROM first GROUP BY channel ORDER BY "Visitors" DESC, channel LIMIT 5`, bars(false, dashspec.SeriesColors{"Visitors": colorVisitors}), 9, 7)
	visitorsFig := figure("traffic", "visitors", "Visitors",
		"Distinct visitor-days in the range: the platform counts a visitor with a daily hash it forgets, so one person on two days or two devices is two. The trend is each hour's.",
		with(web)+`
SELECT uniqExact(`+visitor+`) AS "Visitors" FROM web`,
		with(web)+`
SELECT toStartOfHour(ts) AS hour, uniqExact(`+visitor+`) AS "Visitors" FROM web GROUP BY hour ORDER BY hour`,
		dashspec.UnitCount, colorVisitors)
	return []dashspec.Panel{
		figure("traffic", "live-visitors", "Visitors now", "Distinct visitors in the 5 minutes before the end of the range.",
			with(web)+`
SELECT uniqExact(`+visitor+`) AS "Visitors now" FROM web WHERE ts >= `+to+` - INTERVAL 5 MINUTE`, "", dashspec.UnitCount, colorVisitors),
		visitorsFig,
		{Section: "traffic", ID: "pageviews", Title: "Page views", Kind: dashspec.KindSQL, Width: 6, Height: 4,
			Description: "page_viewed events in the range, and page views per visit (a visit ends after 30 minutes without an event).",
			SQL: visits + `
SELECT sum(views) AS "Page views", round(sum(views) / nullIf(count(), 0), 2) AS "Views per visit" FROM visits`,
			Display: &dashspec.Display{Color: colorVisitors}},
		figure("traffic", "bounce", "Bounce rate", "The share of visits with exactly one page view and nothing else (Plausible's definition).",
			visits+`
SELECT round(avg(views = 1 AND actions = 0), 4) AS "Bounce rate" FROM visits`, "", dashspec.UnitPercent, colorNeutral),
		topPages,
		topChannels,
		figure("traffic", "js-errors-per-1k", "JS errors per 1,000 page views",
			"Browser errors (error_events, kept 14 days by default) per thousand page views in the range. The project's frontend dashboard has the errors, issues and web vitals.",
			with(web)+`
SELECT round(1000 * (SELECT count() FROM error_events WHERE `+scope+`) / nullIf((SELECT countIf(EventName = 'page_viewed') FROM web), 0), 2) AS "Errors per 1,000 views"`,
			"", dashspec.UnitNone, colorDanger),
	}
}

func overviewUserPanels() []dashspec.Panel {
	const week = "{to:DateTime64(9)} - INTERVAL 7 DAY"
	activeIn := func(end string) string {
		return `SELECT uniqExact(` + user + `) FROM product_events WHERE ` + active + ` AND ` + envScope + ` AND ` + lastDays(7, end)
	}
	newIn := func(end string) string {
		return `SELECT ` + unlessUndecided(end+` - INTERVAL 7 DAY`, end, `(SELECT count() FROM (`+newUsers(end)+`) WHERE NewAt >= `+end+` - INTERVAL 7 DAY)`)
	}
	returningIn := func(end string) string {
		return `SELECT ` + unlessUndecided(end+` - INTERVAL 7 DAY`, end, `(`+activeIn(end)+` AND `+user+` NOT IN (SELECT EnvId, UserId FROM (`+newUsers(end)+`) WHERE NewAt >= `+end+` - INTERVAL 7 DAY))`)
	}
	twoWeeks := func(title string, q func(string) string) string {
		return `SELECT (` + q(to) + `) AS "` + title + `",
       (` + q(week) + `) AS "Previous 7 days"`
	}
	fixed := " Always the 7 days before the end of the range, whatever range is selected, beside the 7 days before them, as the Users card counts."
	dau := chart("users", "dau-30d", "Daily active users, last 30 days",
		"Distinct signed-in users each UTC day of the 30 days ending at the end of the range.",
		`SELECT toDate(Timestamp) AS day, uniqExact(`+user+`) AS "Daily active users"
FROM product_events
WHERE `+active+` AND `+envScope+` AND Timestamp >= toStartOfDay(`+to+`) - INTERVAL 29 DAY AND Timestamp < `+to+`
GROUP BY day ORDER BY day`, bars(false, dashspec.SeriesColors{"Daily active users": colorUsers}), 24, 7)
	dau.MinStep = "1d"
	return []dashspec.Panel{
		exact(figure("users", "active-7d", "Active users", "Distinct users with any signed-in product event."+fixed,
			twoWeeks("Active users (7 days)", activeIn), "", dashspec.UnitCount, colorUsers)),
		exact(figure("users", "new-7d", "New users",
			fmt.Sprintf("Users who signed up; in an environment that reported no sign-up in the %d days before, the users first seen, except those first seen "+
				"within %d days of the earliest event retained (they may be returning, and count as returning). No figure when nothing in the 7 days can be decided.",
				obsquery.NewUsersWindowDays, obsquery.FirstSeenGraceDays)+fixed,
			twoWeeks("New users (7 days)", newIn), "", dashspec.UnitCount, colorNew)),
		exact(figure("users", "returning-7d", "Returning users", "Active users who are not new; no figure when New has none."+fixed,
			twoWeeks("Returning users (7 days)", returningIn), "", dashspec.UnitCount, colorWeekly)),
		exact(figure("users", "accounts", "Accounts",
			"Accounts in the environment's identity instance, from the platform's daily snapshot; an environment with none shows its users seen in the last 90 days.",
			accountsSQL, "", dashspec.UnitCount, colorNeutral)),
		dau,
	}
}
