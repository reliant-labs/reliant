package obsbuiltin

import (
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/dashspec"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

// cohortWeeks is how many weekly sign-up cohorts the retention grid draws:
// twelve weeks fit inside product_events' default 90-day retention and the
// 90-day window obsquery.NewUsersSource reads, so the oldest cohort is whole.
const cohortWeeks = 12

// l28Days is the power-user curve's window (Statsig's L28).
const l28Days = 28

var usersSections = []dashspec.Section{
	{ID: "glance", Title: "At a glance",
		Description: "Accounts, and the users active in the range: new against returning, and how habitual the product is. Each figure opens beside its change against the previous period."},
	{ID: "engagement", Title: "Engagement",
		Description: "Whether people come back: each week's new, retained, resurrected and churned users, how many days of the last four weeks people were active, weekly sign-up cohorts, and how long sign-ups take to activate."},
	{ID: "auth", Title: "Authentication",
		Description: "How people sign up and sign in, from the standard auth events (auth_signed_up, auth_signed_in): managed sign-in reports them with no code; any other auth reports them with observe.TrackAuth or trackAuthEvent."},
	{ID: "people", Title: "People",
		Description: "The most active users by their opaque id. Names and emails stay with the identity provider."},
}

// Users is the Users dashboard: accounts, active, new and returning users,
// engagement, retention and authentication, over the hosted product_events
// table. Active and new compile from the fragments the console's Users card
// does (obsquery.UserActivityEvidence, obsquery.NewUsersSource).
func Users(in Input) dashspec.Dashboard {
	a := in.Analytics
	d := dashspec.Dashboard{
		ID:    UsersID,
		Title: in.Project + ": users",
		Description: "Who uses the product: accounts, active users (any signed-in product event), new users (sign-ups, or the users first seen " +
			"when the environment reports no sign-ups) and returning users, engagement, weekly retention and how people sign up and sign in. " +
			"A user is counted once per environment. Hosted environments only: a local forge env up has no product_events table.",
		Group:          dashspec.GroupProduct,
		Filters:        envFilter(),
		Sections:       usersSections,
		DefaultRange:   "30d",
		DefaultCompare: dashspec.ComparePeriod,
	}
	newRule := fmt.Sprintf("Users whose first auth_signed_up is in the range; where no sign-up was reported in the %d days before its end, "+
		"the users first seen in the range, except those within %d days of the earliest event retained (the data begins there; they count as returning). "+
		"No figure when no part of the range is decided. The Authentication section says which rule applies, and from when.",
		obsquery.NewUsersWindowDays, obsquery.FirstSeenGraceDays)

	dauWauMau := chart("glance", "dau-wau-mau", "Daily, weekly and monthly active users",
		"Distinct signed-in users each UTC day, and over the 7 and 30 days ending that day.",
		rollingUsers(30, `uniqExactIf(`+user+`, offset = 0) AS "Daily active",
       uniqExactIf(`+user+`, offset < 7) AS "Weekly active",
       uniqExact(`+user+`) AS "Monthly active"`),
		&dashspec.Display{Style: dashspec.StyleLine, Colors: &dashspec.SeriesColors{"Daily active": colorUsers, "Weekly active": colorWeekly, "Monthly active": colorMonthly}}, 18, 8)
	dauWauMau.MinStep = "1d"
	lifecycle := chart("engagement", "lifecycle-weekly", "Lifecycle per week",
		"Each ISO week's active users in exactly one class: new (their sign-up week), retained (also active the week before), "+
			"resurrected (back after a week or more away), and below zero the users active the week before but not this week (churned). "+
			"The current week counts no churn until it is over.",
		lifecycleSQL, bars(true, dashspec.SeriesColors{"New": colorNew, "Retained": colorUsers, "Resurrected": colorWeekly, "Churned": colorDanger}), 24, 9)
	lifecycle.MinStep = "7d"
	cohorts := chart("engagement", "retention-cohorts", "Weekly retention by sign-up cohort",
		fmt.Sprintf("One row per weekly cohort of new users (by the Monday, UTC, of their sign-up, or first event when sign-ups are not reported), "+
			"the %d weeks before the end of the range, with its size. Each cell is the share of the cohort active in week N after it (W0 is the sign-up week, "+
			"100%% by construction). A week that has not happened yet is empty, not churn.", cohortWeeks),
		retentionCohortsSQL(), &dashspec.Display{Style: dashspec.StyleHeatmap, Color: colorUsers}, 24, 11)
	cohorts.Unit = dashspec.UnitPercent
	signupsByMethod := chart("auth", "signups-by-method", "Sign-ups by method",
		"Each day's sign-ups (first auth_signed_up per user) by method: password, passkey, magic link, social, invite or sso.",
		signupsByMethodSQL, bars(true, nil), 12, 8)
	signupsByMethod.MinStep = "1d"
	signinsByMethod := chart("auth", "signins-by-method", "Sign-ins by method",
		"Sign-ins that verified a credential (auth_signed_in with kind = credential) each day, by method. Events, not users: the sign-in attempts that succeeded. "+
			"A sign-in recorded as a state event (a synthetic monitor's, under observe.WithStateEvents) and an excluded user's are left out.",
		signinsByMethodSQL, bars(true, nil), 12, 8)
	signinsByMethod.MinStep = "1d"
	verified := figure("auth", "verified-share", "Verified sign-ups", "The share of the range's sign-ups that arrived verified (auth_signed_up verified = true).",
		verifiedShareSQL, "", dashspec.UnitPercent, colorSignups)

	d.Panels = []dashspec.Panel{
		exact(figure("glance", "accounts", "Accounts",
			"Accounts in the environment's identity instance, from the platform's daily auth_accounts snapshot (the newest in the 3 days before the end of the range). "+
				fmt.Sprintf("An environment with no snapshot shows its distinct users seen in the %d days before the end of the range instead. The trend is each day's snapshot.", obsquery.NewUsersWindowDays),
			accountsSQL, accountsTrendSQL, dashspec.UnitCount, colorNeutral)),
		exact(figure("glance", "active", "Active users", "Distinct users with any signed-in product event in the range (a sign-in, the daily session heartbeat, a CRUD write, an app event). The trend is each UTC day's.",
			activeSQL, activeTrendSQL, dashspec.UnitCount, colorUsers)),
		exact(figure("glance", "new", "New users", newRule+" The trend is each UTC day's.", newSQL, newTrendSQL, dashspec.UnitCount, colorNew)),
		exact(figure("glance", "returning", "Returning users", "Active users in the range who are not new in it. No figure when the range "+
			"decides no new users (New's description says when).", returningSQL, "", dashspec.UnitCount, colorWeekly)),
		dauWauMau,
		figure("glance", "stickiness", "Stickiness (DAU / MAU)",
			"The average daily active users over the average 30-day active users across the range's days: how habitual the product is. DAU over MAU, not Statsig's rolling stickiness.",
			stickinessSQL, "", dashspec.UnitPercent, colorMonthly),

		lifecycle,
		chart("engagement", "power-users-l28", "Power users (L28)",
			fmt.Sprintf("Users by the number of days they were active in the %d days ending at the end of the range (whatever range is selected): a curve leaning right is a habit.", l28Days),
			powerUsersSQL, bars(false, dashspec.SeriesColors{"Users": colorUsers}), 12, 8),
		chart("engagement", "time-to-activation", "Time to activation",
			"The range's sign-ups by the time from signing up to "+activationTitle(a)+". Not yet: no activation by the end of the range.",
			timeToActivationSQL(a), bars(false, dashspec.SeriesColors{"Sign-ups": colorSignups}), 12, 8),
		cohorts,

		signupsByMethod,
		signinsByMethod,
		verified,
		table("auth", "signups-by-provider", "Sign-ups by provider",
			"The range's sign-ups by auth provider, and the social provider of a social sign-up (forge/google).", signupsByProviderSQL, 6, 8),
		table("auth", "auth-sources", "Where auth events come from",
			"The clients that reported auth events in the range (backend, web, …). A last row says when the environment reports no sign-ups, so new users are the ones first seen, "+
				"and from when, when that is inside the range: before then a user first seen may be returning.",
			authSourcesSQL, 12, 8),

		table("people", "most-active-users", "Most active users",
			"Signed-in users by product events in the range, with the days they were active and when they were last seen. `forge obs sessions <env> --user <id>` lists a user's sessions.",
			mostActiveUsersSQL, 24, 9),
	}
	return in.withActivity(d)
}

// rollingUsers is one row per UTC day of the range over each user's active
// days in the window of days ending that day: every (user, day seen) counts
// toward that day and the days-1 after it. The read starts at the start of
// the day days-1 days before the range's first day, and a day is drawn only
// when it starts before the range's exclusive end. offset 0 is the day itself.
func rollingUsers(days int, columns string) string {
	return fmt.Sprintf(`SELECT addDays(seen, offset) AS day, %s
FROM (
    SELECT DISTINCT EnvId, UserId, toDate(Timestamp) AS seen
    FROM product_events
    WHERE %s AND Timestamp >= toStartOfDay(%s) - INTERVAL %d DAY AND Timestamp < %s AND %s
)
ARRAY JOIN range(%d) AS offset
WHERE day >= toDate(%s) AND toDateTime(day) < %s
GROUP BY day ORDER BY day`, columns, active, from, days-1, to, envScope, days, from, to)
}

// accountsFrom is, per environment, the newest auth_accounts snapshot in the
// 3 days before end, and the users seen in the NewUsersWindowDays before it.
func accountsFrom(end string) string {
	return fmt.Sprintf(`FROM (
    SELECT EnvId,
           countIf(EventName = '%[1]s' AND Timestamp >= %[2]s - INTERVAL 3 DAY) > 0 AS has_snapshot,
           argMaxIf(toInt64OrZero(Props['total']), Timestamp, EventName = '%[1]s' AND Timestamp >= %[2]s - INTERVAL 3 DAY) AS snapshot,
           uniqExactIf(UserId, %[3]s) AS seen
    FROM product_events
    WHERE %[4]s AND %[5]s AND (EventName = '%[1]s' OR (%[3]s))
    GROUP BY EnvId)`, obsquery.EventAuthAccounts, end, active, lastDays(obsquery.NewUsersWindowDays, end), envScope)
}

var (
	accountsSQL = `SELECT sum(if(has_snapshot, snapshot, toInt64(seen))) AS "Accounts"
` + accountsFrom(to)

	accountsTrendSQL = `SELECT day, sum(total) AS "Accounts"
FROM (
    SELECT toDate(Timestamp) AS day, EnvId, argMax(toInt64OrZero(Props['total']), Timestamp) AS total
    FROM product_events
    WHERE EventName = '` + obsquery.EventAuthAccounts + `' AND ` + scope + `
    GROUP BY day, EnvId)
GROUP BY day ORDER BY day`

	activeSQL = `SELECT uniqExact(` + user + `) AS "Active users"
FROM product_events
WHERE ` + active + ` AND ` + scope

	activeTrendSQL = `SELECT toDate(Timestamp) AS day, uniqExact(` + user + `) AS "Active users"
FROM product_events
WHERE ` + active + ` AND ` + scope + `
GROUP BY day ORDER BY day`

	newSQL = `SELECT ` + unlessUndecided(from, to, `(SELECT count() FROM (`+newUsers(to)+`) WHERE NewAt >= `+from+`)`) + ` AS "New users"`

	newTrendSQL = `SELECT toDate(NewAt) AS day, count() AS "New users"
FROM (` + newUsers(to) + `)
WHERE NewAt >= ` + from + `
GROUP BY day ORDER BY day`

	returningSQL = `SELECT ` + unlessUndecided(from, to, `uniqExact(`+user+`)`) + ` AS "Returning users"
FROM product_events
WHERE ` + active + ` AND ` + scope + `
  AND ` + user + ` NOT IN (SELECT EnvId, UserId FROM (` + newUsers(to) + `) WHERE NewAt >= ` + from + `)`

	stickinessSQL = `SELECT round(avg(dau) / nullIf(avg(mau), 0), 4) AS "DAU / MAU"
FROM (
` + rollingUsers(30, `uniqExactIf(`+user+`, offset = 0) AS dau, uniqExact(`+user+`) AS mau`) + `)`

	powerUsersSQL = fmt.Sprintf(`SELECT toString(days) AS "Days active", count() AS "Users"
FROM (
    SELECT EnvId, UserId, uniqExact(toDate(Timestamp)) AS days
    FROM product_events
    WHERE %s AND %s AND %s
    GROUP BY EnvId, UserId)
GROUP BY days ORDER BY days`, active, lastDays(l28Days, to), envScope)
)

// lifecycleSQL classes each ISO week's active users: new (their NewAt week),
// retained (active the week before), resurrected (otherwise); and counts the
// users active a week who were not the week after (churned in that later
// week), negative so the bars hang below zero. A week is drawn when it starts
// in the range; the current week's churn waits for the week to end.
var lifecycleSQL = `WITH
u AS (
  SELECT EnvId, UserId, groupUniqArray(toMonday(Timestamp)) AS wks
  FROM product_events
  WHERE ` + active + ` AND ` + envScope + ` AND Timestamp >= toMonday(` + from + `) - INTERVAL 7 DAY AND Timestamp < ` + to + `
  GROUP BY EnvId, UserId),
s AS (SELECT EnvId, UserId, toMonday(NewAt) AS sw FROM (` + newUsers(to) + `))
SELECT week, countIf(state = 'new') AS "New", countIf(state = 'retained') AS "Retained",
       countIf(state = 'resurrected') AS "Resurrected", -countIf(state = 'churned') AS "Churned"
FROM (
  SELECT wk AS week, multiIf(s.sw = wk, 'new', has(u.wks, wk - 7), 'retained', 'resurrected') AS state
  FROM u ARRAY JOIN u.wks AS wk
  LEFT JOIN s ON s.EnvId = u.EnvId AND s.UserId = u.UserId
  UNION ALL
  SELECT wk + 7 AS week, 'churned' AS state
  FROM u ARRAY JOIN u.wks AS wk
  WHERE NOT has(u.wks, wk + 7) AND wk + 7 < toMonday(` + to + `))
WHERE week >= toMonday(` + from + `) AND toDateTime(week) < ` + to + `
GROUP BY week ORDER BY week`

// retentionCohortsSQL is one row per weekly cohort of new users, named by its
// week and size, with the share of it active in each week W0..W11 after (a
// fraction; the panel's unit is percent), NULL while that week is to come.
func retentionCohortsSQL() string {
	var weeks strings.Builder
	for w := 0; w < cohortWeeks; w++ {
		// A week is shown once it has begun inside the range: its Monday is
		// before the range's (exclusive) end. toMonday(end) would also show
		// the week starting AT end when the range ends on a Monday midnight.
		fmt.Fprintf(&weeks, ",\n       if(toDateTime(cohort + INTERVAL %d WEEK) < %s, round(countIf(has(weeks, cohort + INTERVAL %d WEEK)) / count(), 4), NULL) AS \"W%d\"", w, to, w, w)
	}
	return `SELECT concat(toString(cohort), ' · ', toString(count()), if(count() = 1, ' user', ' users')) AS "Cohort (week of)"` + weeks.String() + `
FROM (
    SELECT c.cohort AS cohort, a.weeks AS weeks
    FROM (SELECT EnvId, UserId, toMonday(NewAt) AS cohort FROM (` + newUsers(to) + `)
          WHERE NewAt >= toMonday(` + to + `) - INTERVAL ` + fmt.Sprint(cohortWeeks-1) + ` WEEK) AS c
    INNER JOIN (SELECT EnvId, UserId, groupUniqArray(toMonday(Timestamp)) AS weeks
                FROM product_events
                WHERE ` + active + ` AND ` + envScope + ` AND Timestamp >= toMonday(` + to + `) - INTERVAL ` + fmt.Sprint(cohortWeeks-1) + ` WEEK AND Timestamp < ` + to + `
                GROUP BY EnvId, UserId) AS a
      ON a.EnvId = c.EnvId AND a.UserId = c.UserId)
GROUP BY cohort ORDER BY cohort`
}

// activationBuckets are time-to-activation's buckets, in order.
var activationBuckets = []string{"< 1 hour", "1-24 hours", "1-3 days", "3-7 days", "> 7 days", "Not yet"}

// timeToActivationSQL is the range's sign-ups by the time from signing up to
// their first activation event, read up to the end of the range.
func timeToActivationSQL(a *Analytics) string {
	return with(suCTE(from, to), actCTE(a, from, to)) + `
SELECT bucket AS "Time to activation", count() AS "Sign-ups"
FROM (
  SELECT multiIf(x.UserId = '', 'Not yet',
                 secs < 3600, '< 1 hour', secs < 86400, '1-24 hours', secs < 3 * 86400, '1-3 days', secs < 7 * 86400, '3-7 days', '> 7 days') AS bucket
  FROM (
    SELECT x.UserId AS UserId, greatest(0, dateDiff('second', s.at, x.at)) AS secs
    FROM su AS s LEFT JOIN act AS x ON x.EnvId = s.EnvId AND x.UserId = s.UserId) AS x)
GROUP BY bucket ORDER BY indexOf(` + lits(activationBuckets) + `, bucket)`
}

var (
	signupsByMethodSQL = with(suCTE(from, to)) + `
SELECT toDate(at) AS day, if(method = '', 'unknown', method) AS method, count() AS "Sign-ups"
FROM su GROUP BY day, method ORDER BY day, method`

	signinsByMethodSQL = `SELECT toDate(Timestamp) AS day, if(Props['method'] = '', 'unknown', Props['method']) AS method, count() AS "Sign-ins"
FROM product_events
WHERE EventName = 'auth_signed_in' AND Props['kind'] = 'credential' AND ` + active + ` AND ` + scope + `
GROUP BY day, method ORDER BY day, method`

	verifiedShareSQL = with(suCTE(from, to)) + `
SELECT round(countIf(verified = 'true') / nullIf(count(), 0), 4) AS "Verified"
FROM su`

	signupsByProviderSQL = with(suCTE(from, to)) + `
SELECT if(social != '', concat(if(provider = '', 'unknown', provider), '/', social), if(provider = '', 'unknown', provider)) AS "Provider",
       count() AS "Sign-ups", round(100 * count() / sum(count()) OVER (), 1) AS "Share %"
FROM su GROUP BY "Provider" ORDER BY "Sign-ups" DESC, "Provider"`

	authSourcesSQL = `SELECT client AS "Client", signups AS "Sign-ups", credential AS "Sign-ins (credential)", resumed AS "Sign-ins (resumed)", signouts AS "Sign-outs"
FROM (
  SELECT 0 AS o, if(Client = '', 'unknown', Client) AS client,
         countIf(EventName = 'auth_signed_up') AS signups,
         countIf(EventName = 'auth_signed_in' AND Props['kind'] = 'credential') AS credential,
         countIf(EventName = 'auth_signed_in' AND Props['kind'] = 'resumed') AS resumed,
         countIf(EventName = 'auth_signed_out') AS signouts
  FROM product_events
  WHERE EventName IN ('auth_signed_up', 'auth_signed_in', 'auth_signed_out') AND ` + scope + `
  GROUP BY client
  UNION ALL
  SELECT 1, if(decided > ` + from + `,
               concat('sign-ups not reported: new users are the users first seen from ', formatDateTime(decided, '%Y-%m-%d %H:%i', 'UTC'),
                      ' UTC, ` + fmt.Sprint(obsquery.FirstSeenGraceDays) + ` days after the earliest event retained; a user first seen before then may be returning and counts as returning'),
               'sign-ups not reported: new users are the users first seen'), 0, 0, 0, 0
  FROM (SELECT count() AS n, (SELECT max(DecidedFrom) FROM (` + newDecided(to) + `)) AS decided
        FROM product_events WHERE EventName = 'auth_signed_up' AND ` + envScope + ` AND ` + lastDays(obsquery.NewUsersWindowDays, to) + `)
  WHERE n = 0)
ORDER BY o, client`

	mostActiveUsersSQL = `SELECT UserId AS "User", count() AS "Events",
       uniqExact(toDate(Timestamp)) AS "Active days",
       uniqExact(EventName) AS "Distinct events",
       toDateTime(max(Timestamp), 'UTC') AS "Last seen"
FROM product_events
WHERE ` + active + ` AND ` + scope + `
GROUP BY UserId ORDER BY count() DESC, UserId LIMIT 25`
)
