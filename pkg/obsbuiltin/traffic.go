package obsbuiltin

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

// The Traffic dashboard is the card set of a project's Traffic section
// (dashboards content design §4.2): four headline figures, each beside the
// previous period, the visitors on the site now, and the breakdowns a viewer
// filters the whole page by. Its panel ids and filter params are a contract
// with the Reliant console, which reads the cards by id: they are exported
// below and never renamed.
//
// Every card reads the same definitions as the Acquisition and Product
// dashboards (a visitor is a visitor-day, a visit ends after 30 minutes
// without an event, a visitor-day's channel, referrer and campaign are its
// first visit's), from the same fragments.
//
// A filter narrows every card to the visitor-days that match it. A
// visitor-day matches a page when it viewed it, an entry or exit page when one
// of its visits started or ended there, a channel, referrer or campaign when
// its first visit came from it, and a country, device, browser or OS when its
// first visit was seen with it (the hash is of the address and the browser, so
// they hold for the whole day). Filters combine with AND. So a row's Visitors
// is exactly what the Visitors card reads once the row is clicked.

// The Traffic dashboard's id and its panels' ids.
const (
	TrafficID = "traffic"

	// The headline figures: each one row of the range's figure, then
	// "Previous period" (the same figure over the equal-length window just
	// before the range), and, for a rate or a median, the visits each was
	// measured over ("Visits", "Previous visits"). TrendSQL is the range's
	// series by {step}.
	TrafficVisitorsPanel      = "visitors"
	TrafficPageViewsPanel     = "pageviews"
	TrafficBouncePanel        = "bounce"
	TrafficVisitDurationPanel = "visit-duration"
	// TrafficLiveVisitorsPanel is the visitors of the 5 minutes before the
	// end of the range, "Visitors now".
	TrafficLiveVisitorsPanel = "live-visitors"

	// The breakdowns: one row per value, most visitors first, at most
	// trafficRows; the first column is the value (the one the panel's
	// Select sets its filter param to), then "Visitors", the breakdown's own
	// count where it has one, and "Visitors %" (of every visitor-day the
	// filters keep).
	TrafficPagesPanel      = "pages"
	TrafficEntryPagesPanel = "entry-pages"
	TrafficExitPagesPanel  = "exit-pages"
	TrafficChannelsPanel   = "channels"
	TrafficReferrersPanel  = "referrers"
	TrafficCampaignsPanel  = "campaigns"
	TrafficCountriesPanel  = "countries"
	TrafficDevicesPanel    = "devices"
	TrafficBrowsersPanel   = "browsers"
	TrafficOSPanel         = "os"
)

// The Traffic dashboard's filter params (dashspec.FilterParam), each the
// String parameter its id names.
const (
	FilterPage     = "f_route"
	FilterEntry    = "f_entry"
	FilterExit     = "f_exit"
	FilterChannel  = "f_channel"
	FilterReferrer = "f_referrer"
	FilterCampaign = "f_campaign"
	FilterCountry  = "f_country"
	FilterDevice   = "f_device"
	FilterBrowser  = "f_browser"
	FilterOS       = "f_os"
)

// UnknownValue is a breakdown's row for a visitor-day the edge could not
// place (no country, no browser): a value a filter can select, unlike "".
const UnknownValue = "(unknown)"

// trafficRows is how many rows a breakdown returns: the card shows the first
// few and offers the rest.
const trafficRows = 100

// TrafficFilterParams are the Traffic dashboard's filter params, in the order
// a console lists their chips.
var TrafficFilterParams = []dashspec.FilterParam{
	{ID: FilterPage, Label: "Page", Description: "Visitors who viewed the page."},
	{ID: FilterEntry, Label: "Entry page", Description: "Visitors with a visit that started on the page."},
	{ID: FilterExit, Label: "Exit page", Description: "Visitors with a visit that ended on the page."},
	{ID: FilterChannel, Label: "Channel", Description: "Visitors whose first visit of the day came from the channel."},
	{ID: FilterReferrer, Label: "Referrer", Description: "Visitors whose first visit of the day came from the referring site."},
	{ID: FilterCampaign, Label: "Campaign", Description: "Visitors whose first visit of the day carried the utm_campaign."},
	{ID: FilterCountry, Label: "Country", Description: "Visitors from the country."},
	{ID: FilterDevice, Label: "Device", Description: "Visitors on the device type."},
	{ID: FilterBrowser, Label: "Browser", Description: "Visitors on the browser."},
	{ID: FilterOS, Label: "OS", Description: "Visitors on the operating system."},
}

var trafficSections = []dashspec.Section{
	{ID: "headline", Title: "Traffic",
		Description: "Visitors, page views, bounce rate and visit duration in the range, each beside the period of the same length before it, and the visitors on the site now."},
	{ID: "pages", Title: "Pages",
		Description: "The pages visitors viewed, and the pages their visits started and ended on. Click a row to filter every card to it."},
	{ID: "sources", Title: "Sources",
		Description: "Where each visitor's first visit of the day came from: its channel, referring site and campaign."},
	{ID: "audience", Title: "Audience",
		Description: "Countries, devices, browsers and operating systems, as the platform's ingest edge saw them."},
}

// Traffic is the Traffic dashboard: the card set of the project's Traffic
// section, for a project with a browser frontend. Hosted environments only:
// a local stack has no product_events table.
func Traffic(in Input) dashspec.Dashboard {
	rules := in.channelRules()
	now := trafficChain(false, rules)
	both := trafficChain(true, rules)

	headline := func(id, title, desc, sql, trend string, unit dashspec.Unit, good dashspec.Direction, units dashspec.ColumnUnits, w int) dashspec.Panel {
		d := &dashspec.Display{Style: dashspec.StyleKPI, Color: colorVisitors, Good: good}
		if units != nil {
			d.Units = &units
		}
		return dashspec.Panel{Section: "headline", ID: id, Title: title, Description: desc, Kind: dashspec.KindSQL, SQL: sql, TrendSQL: trend,
			Unit: unit, Display: d, Width: w, Height: 4}
	}
	visits := dashspec.ColumnUnits{"Visits": dashspec.UnitCount, "Previous visits": dashspec.UnitCount}

	panels := []dashspec.Panel{
		headline(TrafficLiveVisitorsPanel, "Visitors now",
			"Visitors in the 5 minutes before the end of the range.",
			now+`
SELECT uniqExactIf((EnvId, h), ts >= `+to+` - INTERVAL 5 MINUTE) AS "Visitors now" FROM web`, "", dashspec.UnitCount, "", nil, 4),
		headline(TrafficVisitorsPanel, "Visitors",
			"One visitor per person per day, counted without cookies: someone who visits on two days, or on two devices, counts twice. "+
				"The chart counts each visitor once, in the bucket of their first visit, so its bars add up to the figure.",
			both+`
SELECT countIf(cur) AS "Visitors", countIf(NOT cur) AS "Previous period" FROM first`,
			now+`
SELECT toStartOfInterval(fts, toIntervalSecond({step:UInt32})) AS t, count() AS "Visitors"
FROM (SELECT min(start) AS fts FROM visits GROUP BY EnvId, h) GROUP BY t ORDER BY t`,
			dashspec.UnitCount, dashspec.GoodUp, nil, 5),
		headline(TrafficPageViewsPanel, "Page views",
			"Each page a visitor opened (page_viewed), including in-app navigation.",
			both+`
SELECT countIf(cur AND EventName = 'page_viewed') AS "Page views", countIf(NOT cur AND EventName = 'page_viewed') AS "Previous period" FROM web`,
			now+`
SELECT toStartOfInterval(ts, toIntervalSecond({step:UInt32})) AS t, count() AS "Page views"
FROM web WHERE EventName = 'page_viewed' GROUP BY t ORDER BY t`,
			dashspec.UnitCount, dashspec.GoodUp, nil, 5),
		headline(TrafficBouncePanel, "Bounce rate",
			"The share of visits with one page view and nothing else (Plausible's definition). A visit ends after 30 minutes without activity.",
			both+`
SELECT if(countIf(cur) = 0, NULL, round(avgIf(views = 1 AND actions = 0, cur), 4)) AS "Bounce rate",
       if(countIf(NOT cur) = 0, NULL, round(avgIf(views = 1 AND actions = 0, NOT cur), 4)) AS "Previous period",
       countIf(cur) AS "Visits", countIf(NOT cur) AS "Previous visits"
FROM visits`,
			now+`
SELECT toStartOfInterval(start, toIntervalSecond({step:UInt32})) AS t, round(avg(views = 1 AND actions = 0), 4) AS "Bounce rate"
FROM visits GROUP BY t ORDER BY t`,
			dashspec.UnitPercent, dashspec.GoodDown, visits, 5),
		headline(TrafficVisitDurationPanel, "Visit duration",
			"The median engaged time of a visit: the time its pages were visible, focused and in use (page_engaged), so an idle tab does not count. "+
				"Visits that reported no engagement are left out.",
			both+",\n"+engagedCTEs(true)+`
SELECT if(countIf(cur) = 0, NULL, round(quantileExactIf(0.5)(ms, cur))) AS "Visit duration",
       if(countIf(NOT cur) = 0, NULL, round(quantileExactIf(0.5)(ms, NOT cur))) AS "Previous period",
       countIf(cur) AS "Visits", countIf(NOT cur) AS "Previous visits"
FROM engaged`,
			now+",\n"+engagedCTEs(false)+`
SELECT toStartOfInterval(start, toIntervalSecond({step:UInt32})) AS t, round(quantileExact(0.5)(ms)) AS "Visit duration"
FROM engaged GROUP BY t ORDER BY t`,
			dashspec.UnitMillis, dashspec.GoodUp, visits, 5),

		trafficBreakdown("pages", TrafficPagesPanel, "Pages", "Page", FilterPage,
			"Each page's visitors (visitor-days that viewed it) and page views. Ids in a path are shown as :id.",
			now+`
SELECT `+known("Props['route']")+` AS "Page", uniqExact(EnvId, h) AS "Visitors", count() AS "Page views", `+share+`
FROM web WHERE EventName = 'page_viewed' GROUP BY "Page"`, 8),
		trafficBreakdown("pages", TrafficEntryPagesPanel, "Entry pages", "Entry page", FilterEntry,
			"The pages visits started on: the visitors with a visit that started there, and those visits.",
			now+`
SELECT entry_route AS "Entry page", uniqExact(EnvId, h) AS "Visitors", count() AS "Visits", `+share+`
FROM visits WHERE entry_route != '' GROUP BY "Entry page"`, 8),
		trafficBreakdown("pages", TrafficExitPagesPanel, "Exit pages", "Exit page", FilterExit,
			"The pages visits ended on: the visitors with a visit that ended there, and those visits.",
			now+`
SELECT exit_route AS "Exit page", uniqExact(EnvId, h) AS "Visitors", count() AS "Visits", `+share+`
FROM visits WHERE exit_route != '' GROUP BY "Exit page"`, 8),

		firstTouch("sources", TrafficChannelsPanel, "Channels", "Channel", FilterChannel,
			"Visitors by the channel of their first visit of the day, by a published table (the project's forge.Analytics channel rules first): search, social, AI assistants, email, referral, direct and the rest.",
			now, "channel", "", 8),
		firstTouch("sources", TrafficReferrersPanel, "Referrers", "Referrer", FilterReferrer,
			"Visitors by the site their first visit of the day came from. Visitors who came direct are under Channels.",
			now, "referrer", "referrer != ''", 8),
		firstTouch("sources", TrafficCampaignsPanel, "Campaigns", "Campaign", FilterCampaign,
			"Visitors by the utm_campaign of their first visit of the day.",
			now, "utm_campaign", "utm_campaign != ''", 8),

		firstTouch("audience", TrafficCountriesPanel, "Countries", "Country", FilterCountry,
			"Visitors by ISO country code, from their address at the ingest edge, which keeps no address.",
			now, known("country_code"), "", 6),
		firstTouch("audience", TrafficDevicesPanel, "Devices", "Device", FilterDevice,
			"Visitors by device type: desktop, mobile or tablet.",
			now, known("device_type"), "", 6),
		firstTouch("audience", TrafficBrowsersPanel, "Browsers", "Browser", FilterBrowser,
			"Visitors by browser (no version).",
			now, known("browser_name"), "", 6),
		firstTouch("audience", TrafficOSPanel, "Operating systems", "OS", FilterOS,
			"Visitors by operating system (no version).",
			now, known("os_name"), "", 6),
	}
	d := dashspec.Dashboard{
		ID:    TrafficID,
		Title: in.Project + ": traffic",
		Description: "Who visits the project's frontends and where they come from, counted without cookies by the platform's daily visitor hash. " +
			"Click a row of a breakdown to filter every card to it. Hosted environments only: a local forge env up has no product_events table.",
		Group:        dashspec.GroupProduct,
		Filters:      envFilter(),
		FilterParams: TrafficFilterParams,
		Sections:     trafficSections,
		DefaultRange: "7d",
		Panels:       panels,
	}
	return in.withActivity(d)
}

// known is expr with "" read as UnknownValue, in a breakdown's rows and in
// the filter that selects one alike.
func known(expr string) string {
	return "if(" + expr + " = '', " + lit(UnknownValue) + ", " + expr + ")"
}

// share is a breakdown row's "Visitors %": its visitors over every
// visitor-day the filters keep.
const share = `round(100 * "Visitors" / nullIf((SELECT count() FROM first), 0), 1) AS "Visitors %"`

// trafficBreakdown is a ranking whose rows set param to their column value.
func trafficBreakdown(section, id, title, column, param, desc, sql string, w int) dashspec.Panel {
	p := ranking(section, id, title, desc, sql+`
ORDER BY "Visitors" DESC, "`+column+`" LIMIT `+fmt.Sprint(trafficRows), dashspec.Display{Bar: "Visitors", Color: colorVisitors}, w, 9)
	p.Select = &dashspec.RowSelect{Param: param, Column: column}
	return p
}

// firstTouch is a breakdown of the visitor-days by expr, a column of their
// first touch (first), those where keep holds.
func firstTouch(section, id, title, column, param, desc, chain, expr, keep string, w int) dashspec.Panel {
	where := ""
	if keep != "" {
		where = " WHERE " + keep
	}
	return trafficBreakdown(section, id, title, column, param, desc, chain+`
SELECT `+expr+` AS "`+column+`", count() AS "Visitors", `+share+`
FROM first`+where+` GROUP BY "`+column+`"`, w)
}

// prevFrom is the start of the period before the range, of its length.
const prevFrom = "(" + from + " - toIntervalNanosecond(dateDiff('nanosecond', " + from + ", " + to + ")))"

// trafficChain is the WITH clause every Traffic card reads, its rows
// narrowed by the filter params: web (the browser rows), visits and first
// (one row per visitor-day), each with cur, set for the range's own rows.
// With prev it reads the period before the range too (cur unset), each
// period sessionised on its own, as two separate reads would.
//
// The filters pick visitor-days (pick) from the unfiltered visits (v0, f0),
// and web, visits and first keep the picked ones. A visitor-day is a whole
// partition of each, so a kept visit is the same visit unfiltered. With no
// filter set the pick is a constant true the server folds away, so an
// unfiltered card reads exactly what it would without filters.
func trafficChain(prev bool, rules []ChannelRule) string {
	lo := from
	if prev {
		lo = prevFrom
	}
	const keys = "EnvId, h, cur"
	param := func(id string) string { return "{" + id + ":String}" }
	eq := func(id, expr string) string {
		return "(" + param(id) + " = '' OR " + expr + " = " + param(id) + ")"
	}
	in := func(id, set string) string {
		return "(" + param(id) + " = '' OR (" + keys + ") IN (" + set + "))"
	}
	picks := []string{
		eq(FilterChannel, "channel"),
		eq(FilterReferrer, "referrer"),
		eq(FilterCampaign, "utm_campaign"),
		eq(FilterCountry, known("country_code")),
		eq(FilterDevice, known("device_type")),
		eq(FilterBrowser, known("browser_name")),
		eq(FilterOS, known("os_name")),
		in(FilterPage, "SELECT "+keys+" FROM base WHERE EventName = 'page_viewed' AND "+known("Props['route']")+" = "+param(FilterPage)),
		in(FilterEntry, "SELECT "+keys+" FROM v0 WHERE entry_route = "+param(FilterEntry)),
		in(FilterExit, "SELECT "+keys+" FROM v0 WHERE exit_route = "+param(FilterExit)),
	}
	var unset []string
	for _, f := range TrafficFilterParams {
		unset = append(unset, param(f.ID)+" = ''")
	}
	keep := "(" + strings.Join(unset, " AND ") + ")\n     OR (" + keys + ") IN (SELECT " + keys + " FROM pick)"
	return with(
		webOf("base", ", Timestamp >= "+from+" AS cur", "Timestamp >= "+lo+" AND Timestamp < "+to),
		visitsOf("v0", "base", keys),
		firstOf("f0", "v0", keys, rules),
		"pick AS (\n  SELECT "+keys+" FROM f0\n  WHERE "+strings.Join(picks, "\n    AND ")+")",
		"web AS (SELECT * FROM base WHERE "+keep+")",
		"visits AS (SELECT * FROM v0 WHERE "+keep+")",
		"first AS (SELECT * FROM f0 WHERE "+keep+")",
	)
}

// engagedCTEs are each visit's engaged time (eng: the page_engaged beacons;
// engaged: one row per visit with at least one, cur, its start and ms). A
// beacon is sent when a page is hidden or left, after the events of its
// visit, so it belongs to the latest visit of its visitor-day that started
// before it (an ASOF join). It needs trafficChain's visits, over the same
// periods (prev).
func engagedCTEs(prev bool) string {
	lo := from
	if prev {
		lo = prevFrom
	}
	return `eng AS (
  SELECT EnvId, DayVisitor AS h, Timestamp AS ts, toFloat64OrZero(Props['engaged_ms']) AS ms
  FROM product_events
  WHERE Timestamp >= ` + lo + ` AND Timestamp < ` + to + ` AND ` + envScope + ` AND ` + browserRows + ` AND EventName = 'page_engaged'),
engaged AS (
  SELECT v.cur AS cur, v.EnvId AS EnvId, v.h AS h, v.vix AS vix, any(v.start) AS start, sum(e.ms) AS ms
  FROM eng AS e ASOF INNER JOIN visits AS v ON e.EnvId = v.EnvId AND e.h = v.h AND e.ts >= v.start
  GROUP BY cur, EnvId, h, vix)`
}
