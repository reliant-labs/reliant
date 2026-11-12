package obsbuiltin

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

var acquisitionSections = []dashspec.Section{
	{ID: "glance", Title: "At a glance",
		Description: "Visitors, visits, sign-ups and the share of visitor-days that signed up the same day. Each figure opens beside its change against the previous period."},
	{ID: "channels", Title: "Channels",
		Description: "Where visitors come from, by a published channel table (the project's forge.Analytics channel rules first), and which channels bring sign-ups that activate."},
	{ID: "sources", Title: "Sources",
		Description: "The sources, referrers, campaigns and landing pages behind the channels."},
	{ID: "audience", Title: "Audience",
		Description: "Countries, devices, browsers and operating systems, as the platform's ingest edge saw them."},
	{ID: "quality", Title: "Quality",
		Description: "Whether a channel's sign-ups stay: the share active in their fourth week."},
}

// The W4 retention cohort: sign-ups between w4From and w4To days before the
// end of the range, so every one has lived its whole fourth week (days 28 to
// 35 after signing up) inside the 90 days obsquery.NewUsersSource reads.
const (
	w4FromDays = obsquery.NewUsersWindowDays
	w4ToDays   = 35
	// w4MinCohort is the smallest cohort whose share is shown.
	w4MinCohort = 20
)

// Acquisition is the Acquisition dashboard: where visitors come from, which
// channels, sources and campaigns bring sign-ups, and whether those
// sign-ups activate and stay. A sign-up is attributed to the first touch of
// its own day (cookieless); one with no matched visit is Unattributed.
func Acquisition(in Input) dashspec.Dashboard {
	d := dashspec.Dashboard{
		ID:    AcquisitionID,
		Title: in.Project + ": acquisition",
		Description: "Where visitors come from and which channels bring sign-ups that activate. A visitor is a visitor-day (the platform's cookieless daily hash), " +
			"credited to the channel of its first visit that day; a sign-up is credited to the first touch of its own day, so a visitor who came on Monday " +
			"and signed up on Wednesday counts Wednesday's source, and a sign-up with no visit that day is Unattributed. " +
			"Hosted environments only: a local forge env up has no product_events table.",
		Group:          dashspec.GroupProduct,
		Filters:        envFilter(),
		Sections:       acquisitionSections,
		DefaultRange:   "30d",
		DefaultCompare: dashspec.ComparePeriod,
	}
	rules := in.channelRules()
	traffic := with(webCTE(inRange), visitsCTE, firstCTE(rules))
	signups := with(stitchCTE(inRange), suCTE(from, to), suhCTE)
	attributed := with(webCTE(inRange), visitsCTE, firstCTE(rules), stitchCTE(inRange), suCTE(from, to), suhCTE)
	b := breakdown{in: in}
	stacked := bars(true, nil)

	visitorsByChannel := chart("channels", "visitors-by-channel", "Visitors by channel",
		"Each day's visitor-days, by the channel of their first visit, stacked.",
		traffic+`
SELECT d AS day, channel, count() AS "Visitors" FROM first GROUP BY day, channel ORDER BY day, channel`, stacked, 24, 8)
	visitorsByChannel.MinStep = "1d"
	signupsWeekly := chart("channels", "signups-by-channel-weekly", "Sign-ups by channel per week",
		"Each ISO week's sign-ups by the channel of the first visit of their sign-up day; Unattributed when no visit that day is linked to the sign-up.",
		attributed+`
SELECT toMonday(u.at) AS week, if(f.h = '', 'Unattributed', f.channel) AS channel, count() AS "Sign-ups"
FROM su_h AS u LEFT JOIN first AS f ON f.EnvId = u.EnvId AND f.h = u.h
GROUP BY week, channel ORDER BY week, channel`, stacked, 24, 8)
	signupsWeekly.MinStep = "7d"

	d.Panels = []dashspec.Panel{
		figure("glance", "visitors", "Visitors", "Distinct visitor-days in the range. The trend is each UTC day's.",
			traffic+`
SELECT count() AS "Visitors" FROM first`, traffic+`
SELECT d AS day, count() AS "Visitors" FROM first GROUP BY day ORDER BY day`, dashspec.UnitCount, colorVisitors),
		figure("glance", "visits", "Visits", "Visits in the range (a visit ends after 30 minutes without an event). The trend is each UTC day's.",
			with(webCTE(inRange), visitsCTE)+`
SELECT count() AS "Visits" FROM visits`, with(webCTE(inRange), visitsCTE)+`
SELECT toDate(start) AS day, count() AS "Visits" FROM visits GROUP BY day ORDER BY day`, dashspec.UnitCount, colorVisitors),
		figure("glance", "signups", "Sign-ups", "Users whose first auth_signed_up is in the range. The trend is each UTC day's.",
			signups+`
SELECT count() AS "Sign-ups" FROM su_h`, signups+`
SELECT toDate(at) AS day, count() AS "Sign-ups" FROM su_h GROUP BY day ORDER BY day`, dashspec.UnitCount, colorSignups),
		figure("glance", "visit-to-signup", "Visit to sign-up",
			"Attributed sign-ups (linked to a visit on their sign-up day) over visitor-days. Cookieless, so a visitor who signs up on a later day is not linked to the earlier visit.",
			attributed+`
SELECT round((SELECT count() FROM su_h AS u INNER JOIN first AS f ON f.EnvId = u.EnvId AND f.h = u.h) / nullIf((SELECT count() FROM first), 0), 4) AS "Visit to sign-up"`,
			"", dashspec.UnitPercent, colorSignups),

		visitorsByChannel,
		table("channels", "channels", "Channels",
			"Per channel: visitor-days, visits, bounce rate, sign-ups, conversion (sign-ups over visitor-days) and the share of the sign-ups that reached "+
				activationTitle(in.Analytics)+" within "+in.Analytics.EffectiveActivationWindow()+": channel quality, not just volume.",
			b.sql([]dim{{"channel", "Channel"}}, []string{"Visitors", "Visits", "Bounce %", "Sign-ups", "Conversion %", "Activated %"}, "", 50), 24, 9),
		signupsWeekly,

		table("sources", "sources", "Sources",
			"The campaign source (utm_source, or ?ref=), else the referrer host, else (direct), of each visitor-day's first visit.",
			b.sql([]dim{{"source", "Source"}}, []string{"Visitors", "Sign-ups", "Conversion %"}, "", 50), 12, 9),
		table("sources", "referrers", "Referrers",
			"The referring sites and pages of the range's visits (the referrer of a visit's first page view), most visits first.",
			with(webCTE(inRange), visitsCTE)+`
SELECT ref AS "Referrer", ref_path AS "Path", uniqExact(`+visitor+`) AS "Visitors", count() AS "Visits"
FROM visits WHERE ref != ''
GROUP BY ref, ref_path ORDER BY "Visits" DESC, "Referrer", "Path" LIMIT 50`, 12, 9),
		table("sources", "campaigns", "Campaigns",
			"Visitor-days whose first visit carried utm_* parameters, by source, medium, campaign, content and term: visitors, sign-ups, conversion and activation.",
			b.sql([]dim{{"utm_source", "Source"}, {"utm_medium", "Medium"}, {"utm_campaign", "Campaign"}, {"utm_content", "Content"}, {"utm_term", "Term"}},
				[]string{"Visitors", "Sign-ups", "Conversion %", "Activated %"},
				`"Source" NOT IN ('', 'Unattributed') OR "Medium" != '' OR "Campaign" != ''`, 50), 24, 9),
		table("sources", "landing-pages", "Landing pages",
			"The route each visitor-day's first visit started on, by channel: visits and sign-ups.",
			b.sql([]dim{{"landing", "Landing page"}, {"channel", "Channel"}}, []string{"Visits", "Sign-ups"}, "", 50), 24, 9),

		table("audience", "countries", "Countries",
			"Visitor-days by ISO country code (from the visitor's address at the ingest edge, which keeps no address), with sign-ups and conversion.",
			b.sql([]dim{{"country_code", "Country"}}, []string{"Visitors", "Sign-ups", "Conversion %"}, "", 50), 12, 9),
		table("audience", "devices", "Devices",
			"Visitor-days by device type (desktop, mobile, tablet), with sign-ups and conversion.",
			b.sql([]dim{{"device_type", "Device"}}, []string{"Visitors", "Sign-ups", "Conversion %"}, "", 10), 12, 9),
		chart("audience", "browsers", "Browsers", "Visitor-days by browser (no version).",
			traffic+`
SELECT if(browser_name = '', 'unknown', browser_name) AS browser, count() AS "Visitors" FROM first GROUP BY browser ORDER BY "Visitors" DESC, browser LIMIT 15`,
			bars(false, dashspec.SeriesColors{"Visitors": colorVisitors}), 12, 8),
		chart("audience", "os", "Operating systems", "Visitor-days by operating system (no version).",
			traffic+`
SELECT if(os_name = '', 'unknown', os_name) AS os, count() AS "Visitors" FROM first GROUP BY os ORDER BY "Visitors" DESC, os LIMIT 15`,
			bars(false, dashspec.SeriesColors{"Visitors": colorVisitors}), 12, 8),

		table("quality", "retention-by-channel", "Week-4 retention by channel",
			fmt.Sprintf("The sign-ups of the %d to %d days before the end of the range (whatever range is selected, so each has lived its fourth week), by channel: "+
				"the share active in days 28 to 35 after signing up. A channel with fewer than %d such sign-ups shows no share.", w4FromDays, w4ToDays, w4MinCohort),
			retentionByChannelSQL(rules), 24, 8),
	}
	return d
}

// channelRules are the project's custom channel rules.
func (in Input) channelRules() []ChannelRule {
	if in.Analytics == nil {
		return nil
	}
	return in.Analytics.Channels
}

// dim is one breakdown column: an expression over first's columns, and its
// label.
type dim struct{ expr, label string }

// breakdown builds the attribution tables: visitor-days and sign-ups split by
// first-touch dimensions, joined so a dimension with sign-ups and no visitors
// (Unattributed) keeps its row.
type breakdown struct{ in Input }

// breakdownMetrics are the columns a breakdown can show, over v (visitor-day
// counts per key) and s (sign-up counts per key).
// The side a row lacks counts 0 (ifNull: a session with join_use_nulls gives
// NULL for it).
var breakdownMetrics = map[string]string{
	"Visitors":     "ifNull(v.visitors, 0)",
	"Visits":       "ifNull(v.visits, 0)",
	"Bounce %":     "round(100 * v.bounces / nullIf(ifNull(v.visits, 0), 0), 1)",
	"Sign-ups":     "ifNull(s.signups, 0)",
	"Conversion %": "round(100 * ifNull(s.signups, 0) / nullIf(ifNull(v.visitors, 0), 0), 2)",
	"Activated %":  "round(100 * s.activated / nullIf(ifNull(s.signups, 0), 0), 1)",
}

func (b breakdown) sql(dims []dim, metrics []string, where string, limit int) string {
	a := b.in.Analytics
	w := activationWindow(a)
	var vk, sk, keys, on, sel []string
	for i, d := range dims {
		k := fmt.Sprintf("k%d", i)
		unattributed := "''"
		if i == 0 {
			unattributed = "'Unattributed'"
		}
		// Keys are normalized to non-NULL strings on both sides, so an empty
		// or absent dimension joins as '' whatever the session's
		// join_use_nulls; a sign-up with no visit is Unattributed on the first
		// key. The output key is the side that has the row.
		key := fmt.Sprintf("ifNull(toString(%s), '')", d.expr)
		vk = append(vk, key+" AS "+k)
		sk = append(sk, fmt.Sprintf("if(ifNull(f.h, '') = '', %s, %s) AS %s", unattributed, key, k))
		keys = append(keys, k)
		on = append(on, "v."+k+" = s."+k)
		sel = append(sel, fmt.Sprintf("if(ifNull(v.visitors, 0) > 0, v.%s, s.%s) AS %q", k, k, d.label))
	}
	for _, m := range metrics {
		sel = append(sel, fmt.Sprintf("%s AS %q", breakdownMetrics[m], m))
	}
	order := fmt.Sprintf("%q DESC", metrics[0])
	for _, d := range dims {
		order += fmt.Sprintf(", %q", d.label)
	}
	if where != "" {
		where = "\nWHERE " + where
	}
	group := strings.Join(keys, ", ")
	return with(webCTE(inRange), visitsCTE, firstCTE(b.in.channelRules()), stitchCTE(inRange), suCTE(from, to), suhCTE, actCTE(a, from, to+" + "+w)) + `,
v AS (SELECT ` + strings.Join(vk, ", ") + `, uniqExact(` + visitor + `) AS visitors, sum(n_visits) AS visits, sum(bounces) AS bounces
      FROM first GROUP BY ` + group + `),
s AS (SELECT ` + strings.Join(sk, ", ") + `, count() AS signups, countIf(x.UserId != '' AND x.at >= u.at AND x.at < u.at + ` + w + `) AS activated
      FROM su_h AS u LEFT JOIN first AS f ON f.EnvId = u.EnvId AND f.h = u.h
      LEFT JOIN act AS x ON x.EnvId = u.EnvId AND x.UserId = u.UserId
      GROUP BY ` + group + `)
SELECT ` + strings.Join(sel, ",\n       ") + `
FROM v FULL OUTER JOIN s ON ` + strings.Join(on, " AND ") + where + `
ORDER BY ` + order + fmt.Sprintf(" LIMIT %d", limit)
}

// retentionByChannelSQL is the W4 table: sign-ups old enough to have lived
// their fourth week, by the channel of their sign-up day's first visit, and
// the share active in days 28 to 35 after signing up.
func retentionByChannelSQL(rules []ChannelRule) string {
	lo := fmt.Sprintf("%s - INTERVAL %d DAY", to, w4FromDays)
	hi := fmt.Sprintf("%s - INTERVAL %d DAY", to, w4ToDays)
	// The cohort's visits and links are read over the cohort's own days.
	rng := "Timestamp >= " + lo + " AND Timestamp < " + hi + " + INTERVAL 1 DAY"
	return with(webCTE(rng), visitsCTE, firstCTE(rules), stitchCTE(rng), suCTE(lo, hi), suhCTE) + `,
w4 AS (
  SELECT DISTINCT u.EnvId AS EnvId, u.UserId AS UserId
  FROM su_h AS u
  INNER JOIN (SELECT EnvId, UserId, Timestamp FROM product_events
              WHERE ` + active + ` AND ` + envScope + ` AND Timestamp >= ` + lo + ` + INTERVAL 28 DAY AND Timestamp < ` + to + `) AS e
    ON e.EnvId = u.EnvId AND e.UserId = u.UserId
  WHERE e.Timestamp >= u.at + INTERVAL 28 DAY AND e.Timestamp < u.at + INTERVAL 35 DAY)
SELECT if(f.h = '', 'Unattributed', f.channel) AS "Channel", count() AS "Sign-ups",
       if(count() >= ` + fmt.Sprint(w4MinCohort) + `, round(100 * countIf((u.EnvId, u.UserId) IN (SELECT EnvId, UserId FROM w4)) / count(), 1), NULL) AS "Active in week 4 %"
FROM su_h AS u LEFT JOIN first AS f ON f.EnvId = u.EnvId AND f.h = u.h
GROUP BY "Channel" ORDER BY "Sign-ups" DESC, "Channel"`
}
