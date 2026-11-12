package obsbuiltin

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/dashspec"
)

var productSections = []dashspec.Section{
	{ID: "glance", Title: "At a glance",
		Description: "Visits and how deep they go: page views, visits, views per visit, bounce rate, visit duration and every product event. Each figure opens beside its change against the previous period."},
	{ID: "pages", Title: "Pages",
		Description: "The routes people view, the ones visits start on and the ones they leave from."},
	{ID: "events", Title: "Goals and events",
		Description: "The conversions declared in forge.Analytics, every product event by name, and the records people write through the generated CRUD handlers."},
	{ID: "funnels", Title: "Funnels",
		Description: "Visit to sign-up to activation, built in, and every forge.Funnel counted by visitor or person."},
	{ID: "experiments", Title: "Experiments",
		Description: "Feature-flag exposures (flag_evaluated, from trackExposure or the OpenFeature hook) and how each variant's people went on to activate. Descriptive, not a significance test; empty until an exposure is recorded."},
}

// The CRUD actions' colours: deleting is not an error, so not danger.
const (
	colorCreated = dashspec.ColorCyan
	colorUpdated = dashspec.ColorViolet
	colorDeleted = dashspec.ColorAmber
)

// crudEvent matches the automatic CRUD product events (pkg/crud): the entity
// is the name with the verb suffix removed, so an entity added later needs no
// regenerate to appear.
const (
	crudEventRE = `'_(created|updated|deleted)$'`
	crudEntity  = "replaceRegexpOne(EventName, " + crudEventRE + ", '')"
)

// Product is the Product dashboard: what visitors and users do, from page
// views to goals, funnels and flag exposures. It supersedes the generated
// "Product usage" dashboard (SupersededUsageID) with Users.
func Product(in Input) dashspec.Dashboard {
	a := in.Analytics
	d := dashspec.Dashboard{
		ID:    ProductID,
		Title: in.Project + ": product",
		Description: "What people do in the product: visits and page views (cookieless, by the platform's daily visitor hash), the pages visits start " +
			"and end on, declared goals, every product event, the records written through the generated CRUD handlers, the built-in visit → sign-up → " +
			"activation funnel, declared funnels and flag exposures. Hosted environments only: a local forge env up has no product_events table.",
		Group:          dashspec.GroupProduct,
		Replaces:       []string{SupersededUsageID},
		Filters:        envFilter(),
		Sections:       productSections,
		DefaultRange:   "7d",
		DefaultCompare: dashspec.ComparePeriod,
	}
	web := webCTE(inRange)
	visits := with(web, visitsCTE)
	visitTrend := func(column, measure string) string {
		return visits + `
SELECT toDate(start) AS day, ` + measure + ` AS "` + column + `" FROM visits GROUP BY day ORDER BY day`
	}
	duration := figure("glance", "visit-duration", "Visit duration", "The average time from a visit's first event to its last; a bounce lasts 0 and counts.",
		visits+`
SELECT round(avg(dateDiff('second', start, stop))) AS "Visit duration" FROM visits`, "", dashspec.UnitSeconds, colorVisitors)
	duration.Width = 12
	events := figure("glance", "events", "Product events", "Every product event in the range, automatic or tracked by the app, signed in or anonymous, browser or server.",
		`SELECT count() AS "Product events" FROM product_events WHERE `+scope,
		`SELECT toDate(Timestamp) AS day, count() AS "Product events" FROM product_events WHERE `+scope+` GROUP BY day ORDER BY day`,
		dashspec.UnitCount, dashspec.ColorFuchsia)
	events.Width = 12
	window := a.EffectiveActivationWindow()
	activation := chart("funnels", "funnel-activation", "Visit → sign-up → activation",
		"Visitor-days in the range, the sign-ups of the range (persons; a sign-up with no visit, an API sign-up or one on another device, counts too), "+
			"and those that reached "+activationTitle(a)+" within "+window+" of signing up. The first step counts visitor-days, the others persons.",
		activationFunnelSQL(a), &dashspec.Display{Style: dashspec.StyleFunnel, Color: colorSignups}, 18, 8)

	d.Panels = []dashspec.Panel{
		figure("glance", "pageviews", "Page views", "page_viewed events in the range. The trend is each UTC day's.",
			visits+`
SELECT sum(views) AS "Page views" FROM visits`, visitTrend("Page views", "sum(views)"), dashspec.UnitCount, colorVisitors),
		figure("glance", "visits", "Visits", "Visits in the range: one visitor's events with gaps of 30 minutes or less. The trend is each UTC day's.",
			visits+`
SELECT count() AS "Visits" FROM visits`, visitTrend("Visits", "count()"), dashspec.UnitCount, colorVisitors),
		figure("glance", "views-per-visit", "Views per visit", "Page views over visits.",
			visits+`
SELECT round(sum(views) / nullIf(count(), 0), 2) AS "Views per visit" FROM visits`, "", dashspec.UnitNone, colorVisitors),
		figure("glance", "bounce", "Bounce rate", "The share of visits with exactly one page view and nothing else (Plausible's definition).",
			visits+`
SELECT round(avg(views = 1 AND actions = 0), 4) AS "Bounce rate" FROM visits`, "", dashspec.UnitPercent, colorNeutral),
		duration,
		events,

		table("pages", "top-pages", "Pages",
			"Each route's page views, distinct visitor-days, signed-in users, and the visits that started (Entries) and ended (Exits) on it.",
			visits+`,
p AS (SELECT Props['route'] AS route, count() AS views, uniqExact(`+visitor+`) AS visitors, uniqExactIf(`+user+`, UserId != '') AS users
      FROM web WHERE EventName = 'page_viewed' GROUP BY route),
en AS (SELECT entry_route AS route, count() AS entries FROM visits WHERE entry_route != '' GROUP BY route),
ex AS (SELECT exit_route AS route, count() AS exits FROM visits WHERE exit_route != '' GROUP BY route)
SELECT p.route AS "Route", p.views AS "Views", p.visitors AS "Visitors", p.users AS "Signed-in users", en.entries AS "Entries", ex.exits AS "Exits"
FROM p LEFT JOIN en ON en.route = p.route LEFT JOIN ex ON ex.route = p.route
ORDER BY "Views" DESC, "Route" LIMIT 50`, 24, 9),
		table("pages", "entry-pages", "Entry pages",
			"Visits by the route they started on: the share that bounced, and the share whose visitor signed up the same day.",
			with(web, visitsCTE, stitchCTE(inRange), suCTE(from, to), suhCTE)+`
SELECT entry_route AS "Entry page", count() AS "Visits",
       round(100 * avg(views = 1 AND actions = 0), 1) AS "Bounce %",
       round(100 * countIf(`+visitor+` IN (SELECT EnvId, h FROM su_h WHERE h != '')) / count(), 1) AS "Sign-up %"
FROM visits WHERE entry_route != ''
GROUP BY entry_route ORDER BY "Visits" DESC, "Entry page" LIMIT 50`, 12, 9),
		table("pages", "exit-pages", "Exit pages",
			"Visits by the route they ended on, and the exit rate: exits over the route's page views.",
			visits+`,
ex AS (SELECT exit_route AS route, count() AS exits FROM visits WHERE exit_route != '' GROUP BY route),
p AS (SELECT Props['route'] AS route, count() AS views FROM web WHERE EventName = 'page_viewed' GROUP BY route)
SELECT ex.route AS "Exit page", ex.exits AS "Exits", round(100 * ex.exits / nullIf(p.views, 0), 1) AS "Exit rate %"
FROM ex LEFT JOIN p ON p.route = ex.route
ORDER BY "Exits" DESC, "Exit page" LIMIT 50`, 12, 9),
	}
	if a != nil && len(a.Goals) > 0 {
		d.Panels = append(d.Panels, table("events", "goals", "Goals",
			"One row per goal declared in forge.Analytics: the persons who reached it (a signed-in user, the user a same-day sign-in links a visitor to, else the visitor-day), "+
				"every time it was reached, and its conversion rate: persons over the range's visitor-days.",
			goalsSQL(a.Goals), 24, 7))
	}
	d.Panels = append(d.Panels,
		table("events", "events-by-name", "Events by name",
			"Every product event in the range but page views: how often, by how many persons, and when it was last seen.",
			with(stitchCTE(inRange), personsCTE(inRange, "EventName NOT IN ('page_viewed', 'auth_accounts')"))+`
SELECT EventName AS "Event", count() AS "Events", uniqExactIf((EnvId, person), person != '') AS "Persons",
       toDateTime(max(ts), 'UTC') AS "Last seen"
FROM persons GROUP BY EventName ORDER BY "Events" DESC, "Event" LIMIT 50`, 24, 9),
		chart("events", "records-by-entity", "Records written per entity",
			"The automatic CRUD product events (creates, updates and deletes together), one colour per entity, stacked: the top edge is every write.",
			`SELECT toStartOfInterval(Timestamp, toIntervalSecond({step:UInt32})) AS t,
       `+crudEntity+` AS entity,
       count() AS records
FROM product_events
WHERE match(EventName, `+crudEventRE+`) AND `+scope+`
GROUP BY t, entity ORDER BY t`, &dashspec.Display{Style: dashspec.StyleBars, Stack: true, TopN: 12}, 12, 8),
		chart("events", "actions-per-entity", "Creates, updates and deletes per entity",
			"The CRUD mix of each entity in the range: one bar per entity, a colour per action.",
			`SELECT `+crudEntity+` AS entity,
       countIf(endsWith(EventName, '_created')) AS "Created",
       countIf(endsWith(EventName, '_updated')) AS "Updated",
       countIf(endsWith(EventName, '_deleted')) AS "Deleted"
FROM product_events
WHERE match(EventName, `+crudEventRE+`) AND `+scope+`
GROUP BY entity ORDER BY count() DESC, entity LIMIT 20`,
			bars(true, dashspec.SeriesColors{"Created": colorCreated, "Updated": colorUpdated, "Deleted": colorDeleted}), 12, 8),

		activation,
		figure("funnels", "signups-from-visit", "Sign-ups from a visit",
			"The share of the range's sign-ups linked to a visit: the sign-up carried a visitor hash, or a same-day sign-in links one to the user. The rest signed up through the API or on another device.",
			with(stitchCTE(inRange), suCTE(from, to), suhCTE)+`
SELECT round(countIf(h != '') / nullIf(count(), 0), 4) AS "From a visit" FROM su_h`, "", dashspec.UnitPercent, colorSignups),
	)
	for _, f := range in.Funnels {
		if IsProductFunnel(f.By) {
			d.Panels = append(d.Panels, productFunnel(f))
		}
	}
	d.Panels = append(d.Panels, table("experiments", "flag-exposure", "Flag exposures",
		"Each flag and variant: the persons exposed in the range, and the share of them whose first "+activationTitle(a)+
			" came within "+window+" after their first exposure. Descriptive, not a significance test.",
		flagExposureSQL(a), 24, 8))
	return d
}

// activationFunnelSQL is the built-in funnel: visitor-days, the range's
// sign-ups, and the sign-ups activated within the activation window (read
// past the end of the range, so a past range's late activations count).
func activationFunnelSQL(a *Analytics) string {
	w := activationWindow(a)
	return with(webCTE(inRange), stitchCTE(inRange), suCTE(from, to), suhCTE, actCTE(a, from, to+" + "+w)) + `
SELECT step AS "Step", n AS "Persons"
FROM (
  SELECT 1 AS i, 'Visited' AS step, (SELECT uniqExact(` + visitor + `) FROM web) AS n
  UNION ALL
  SELECT 2, 'Signed up', (SELECT count() FROM su_h)
  UNION ALL
  SELECT 3, 'Activated', (SELECT countIf(x.UserId != '' AND x.at >= s.at AND x.at < s.at + ` + w + `)
                          FROM su_h AS s LEFT JOIN act AS x ON x.EnvId = s.EnvId AND x.UserId = s.UserId))
ORDER BY i`
}

// goalsSQL is one row per declared goal, in declaration order, reached or
// not.
func goalsSQL(goals []Goal) string {
	var ids, titles, matches, either []string
	for _, g := range goals {
		pred := "EventName = " + lit(g.Event)
		if g.Route != "" {
			pred = "EventName = 'page_viewed' AND Props['route'] = " + lit(g.Route)
		}
		title := g.Title
		if title == "" {
			title = g.ID
		}
		ids, titles = append(ids, g.ID), append(titles, title)
		matches = append(matches, fmt.Sprintf("if(%s, %s, '')", pred, lit(g.ID)))
		either = append(either, "("+pred+")")
	}
	return with(webCTE(inRange), stitchCTE(inRange), personsCTE(inRange, "("+strings.Join(either, " OR ")+")")) + `,
hits AS (SELECT goal, EnvId, person FROM persons ARRAY JOIN arrayFilter(g -> g != '', [` + strings.Join(matches, ", ") + `]) AS goal)
SELECT transform(d.goal, ` + lits(ids) + `, ` + lits(titles) + `, d.goal) AS "Goal",
       uniqExactIf((h.EnvId, h.person), h.person != '') AS "Persons",
       countIf(h.goal != '') AS "Total",
       round(100 * uniqExactIf((h.EnvId, h.person), h.person != '') / nullIf((SELECT uniqExact(` + visitor + `) FROM web), 0), 2) AS "Conversion %"
FROM (SELECT arrayJoin(` + lits(ids) + `) AS goal) AS d
LEFT JOIN hits AS h ON h.goal = d.goal
GROUP BY d.goal ORDER BY indexOf(` + lits(ids) + `, d.goal)`
}

// productFunnel is a declared visitor or person funnel: windowFunnel over
// the product events of each visitor-day or each person, steps in order.
func productFunnel(f Funnel) dashspec.Panel {
	window := f.Window
	if window == "" {
		window = dashspec.DefaultFunnelWindow
	}
	secs := windowSeconds(window, dashspec.DefaultFunnelWindow)
	var conds []string
	for _, s := range f.Steps {
		conds = append(conds, "EventName = "+lit(s))
	}
	in := "EventName IN (" + strings.Trim(lits(f.Steps), "[]") + ")"
	var src, unit, who string
	if f.By == FunnelByVisitor {
		unit, who = "Visitors", "visitor-day (the platform's daily visitor hash)"
		src = `SELECT EnvId, DayVisitor AS k, Timestamp AS ts, EventName FROM product_events WHERE ` + scope + ` AND DayVisitor != '' AND DeviceType != 'bot' AND ` + in
	} else {
		unit, who = "Persons", "person (the signed-in user, else the user a same-day sign-in links a visitor to, else the visitor-day)"
		src = with(stitchCTE(inRange), personsCTE(inRange, in)) + `
SELECT EnvId, person AS k, ts, EventName FROM persons WHERE person != ''`
	}
	title := f.Title
	if title == "" {
		title = f.ID
	}
	sql := fmt.Sprintf(`SELECT %s[i] AS "Step", countIf(level >= i) AS %q
FROM (
  SELECT windowFunnel(%d)(toDateTime(ts), %s) AS level
  FROM (%s)
  GROUP BY EnvId, k)
ARRAY JOIN range(1, %d) AS i
GROUP BY i ORDER BY i`, lits(f.Steps), unit, secs, strings.Join(conds, ", "), src, len(f.Steps)+1)
	return chart("funnels", "funnel-"+f.ID, title,
		fmt.Sprintf("Product events in order (%s), within %s, counted per %s.", strings.Join(f.Steps, " → "), window, who),
		sql, &dashspec.Display{Style: dashspec.StyleFunnel, Color: colorSignups}, 24, 8)
}

// flagExposureSQL is each flag and variant's exposed persons, and the share
// whose first activation came within the window after their first exposure.
func flagExposureSQL(a *Analytics) string {
	w := activationWindow(a)
	return with(stitchCTE(inRange), personsCTE(inRange, "EventName = 'flag_evaluated'"), actCTE(a, from, to+" + "+w)) + `,
x AS (SELECT Props['flag'] AS flag, Props['variant'] AS variant, EnvId, person, min(ts) AS first_at
      FROM persons WHERE person != '' GROUP BY flag, variant, EnvId, person)
SELECT x.flag AS "Flag", x.variant AS "Variant", count() AS "Exposed persons",
       round(100 * countIf(y.UserId != '' AND y.at >= x.first_at AND y.at < x.first_at + ` + w + `) / count(), 1) AS "Activated %"
FROM x LEFT JOIN act AS y ON y.EnvId = x.EnvId AND y.person = x.person
GROUP BY x.flag, x.variant ORDER BY "Flag", "Variant"`
}
