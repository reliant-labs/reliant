package obsbuiltin

import (
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// The shared fragments every panel inlines (product-analytics design §2.1).
// Each panel is ONE read-only SELECT over the hosted per-org product_events
// table, bound by the console like any sql panel ({from}, {to}, {env_ids}),
// so these are text, composed in Go, never views or rollups.
//
// The cookieless visitor is the edge's daily hash, DayVisitor. A hash is new
// every UTC day and its day is the day the ingest edge RECEIVED the batch, not
// the event's: a row of 23:59 sent after midnight carries the next day's hash.
// So a visitor is keyed by (EnvId, DayVisitor) alone, never paired with
// toDate(Timestamp); counting distinct hashes counts visitor-days, and a visit
// that crosses midnight inside one batch stays one visit.

// The time and environment scope of a read.
const (
	from     = "{from:DateTime64(9)}"
	to       = "{to:DateTime64(9)}"
	envScope = "(empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))"
	// inRange is the dashboard's range.
	inRange = "Timestamp >= " + from + " AND Timestamp < " + to
	// scope is the range and the environments: product-analytics' {scope}.
	scope = inRange + " AND " + envScope
)

// active is the rows that count their UserId as active, shared with the
// console's Users card (GetUserActivity) and `forge obs users`.
const active = obsquery.ActiveEvidence

// user is the distinct-user key. A user is counted once per environment, as
// the Users card counts them (per env), so a project dashboard over several
// environments equals the sum of their cards.
const user = "(EnvId, UserId)"

// visitor is the distinct visitor-day key.
const visitor = "(EnvId, h)"

// newUsers is obsquery.NewUsersSource over product_events for the window that
// ends at end: one row per new user, EnvId, UserId, NewAt, ReportsSignups.
func newUsers(end string) string {
	return obsquery.NewUsersSource("product_events", end, envScope)
}

// lastDays is the predicate on Timestamp for the n days before end.
func lastDays(n int, end string) string {
	return fmt.Sprintf("Timestamp >= %s - INTERVAL %d DAY AND Timestamp < %s", end, n, end)
}

// webCTE is the analytics rows of browsers in rng: the edge-hashed events of
// the web and landing clients, bots excluded (the edge computes no hash for a
// bot, so this is a second guard), without the events that are not
// interaction (an identity heartbeat, a sign-out, a flag exposure, an
// engagement beacon).
func webCTE(rng string) string {
	return `web AS (
  SELECT Timestamp AS ts, EnvId, DayVisitor AS h, UserId, EventName, Props, Country, DeviceType, Browser, OS
  FROM product_events
  WHERE ` + rng + ` AND ` + envScope + ` AND Client IN ('web', 'landing') AND DayVisitor != '' AND DeviceType != 'bot'
    AND EventName NOT IN ('auth_signed_in', 'auth_signed_out', 'flag_evaluated', 'page_engaged', '` + obsquery.EventAuthAccounts + `'))`
}

// visitsCTE sessionises web into visits: one visitor's events in time order,
// a new visit after a gap of more than 30 minutes. Each visit carries where it
// came from (its first page view's referrer, campaign and ad-network flag),
// its entry and exit routes, and what the edge saw.
const visitsCTE = `visits AS (
  SELECT EnvId, h, vix, min(ts) AS start, max(ts) AS stop,
         countIf(EventName = 'page_viewed') AS views,
         countIf(EventName != 'page_viewed') AS actions,
         argMinIf(Props['route'], ts, EventName = 'page_viewed') AS entry_route,
         argMaxIf(Props['route'], ts, EventName = 'page_viewed') AS exit_route,
         argMinIf(lower(Props['referrer_host']), ts, EventName = 'page_viewed') AS ref,
         argMinIf(Props['referrer_path'], ts, EventName = 'page_viewed') AS ref_path,
         argMinIf(lower(if(Props['utm_source'] != '', Props['utm_source'], Props['ref'])), ts, EventName = 'page_viewed') AS src,
         argMinIf(lower(Props['utm_medium']), ts, EventName = 'page_viewed') AS med,
         argMinIf(Props['utm_campaign'], ts, EventName = 'page_viewed') AS campaign,
         argMinIf(Props['utm_term'], ts, EventName = 'page_viewed') AS term,
         argMinIf(Props['utm_content'], ts, EventName = 'page_viewed') AS content,
         argMinIf(Props['click_id'], ts, EventName = 'page_viewed') AS click,
         anyIf(Country, Country != '') AS country, anyIf(DeviceType, DeviceType != '') AS device,
         anyIf(Browser, Browser != '') AS browser, anyIf(OS, OS != '') AS os
  FROM (
    SELECT *, sum(brk) OVER (PARTITION BY EnvId, h ORDER BY ts ROWS UNBOUNDED PRECEDING) AS vix
    FROM (
      SELECT *, dateDiff('second',
               lagInFrame(ts, 1, ts - INTERVAL 1 HOUR) OVER (PARTITION BY EnvId, h ORDER BY ts
                                                         ROWS BETWEEN 1 PRECEDING AND CURRENT ROW), ts) > 1800 AS brk
      FROM web))
  GROUP BY EnvId, h, vix)`

// firstCTE is each visitor-day's first touch: the channel and source of its
// first visit, its landing page, campaign and audience, its visits and
// bounces. d is the day of its last event, the day a visitor is drawn on.
// The names differ from visits' columns so no alias shadows one.
func firstCTE(rules []ChannelRule) string {
	return `first AS (
  SELECT EnvId, h, toDate(max(stop)) AS d,
         argMin(` + channelExpr(rules, "ref", "src", "med", "click") + `, start) AS channel,
         argMin(` + sourceExpr("ref", "src") + `, start) AS source,
         argMin(entry_route, start) AS landing,
         argMin(ref, start) AS referrer, argMin(ref_path, start) AS referrer_path,
         argMin(src, start) AS utm_source, argMin(med, start) AS utm_medium, argMin(campaign, start) AS utm_campaign,
         argMin(term, start) AS utm_term, argMin(content, start) AS utm_content,
         argMin(country, start) AS country_code, argMin(device, start) AS device_type,
         argMin(browser, start) AS browser_name, argMin(os, start) AS os_name,
         count() AS n_visits, countIf(views = 1 AND actions = 0) AS bounces
  FROM visits GROUP BY EnvId, h)`
}

// stitchCTE links a visitor-day to the one user seen on it: the hash's rows
// that also carry a UserId, when exactly one user did (a hash two users share,
// an office NAT, links nobody), never from a row whose browser asked not to be
// tracked. d is the day of the hash's last linking row.
func stitchCTE(rng string) string {
	return `stitch AS (
  SELECT EnvId, DayVisitor AS h, any(UserId) AS uid, toDate(max(Timestamp)) AS d
  FROM product_events
  WHERE ` + rng + ` AND ` + envScope + ` AND DayVisitor != '' AND UserId != '' AND Privacy = ''
  GROUP BY EnvId, h HAVING uniqExact(UserId) = 1)`
}

// suCTE is the sign-ups whose first auth_signed_up (in the window
// obsquery.NewUsersSource reads: the 90 days before {to}) is in [lo, hi), with
// the visitor hash the sign-up itself carried (a hosted backend's request is
// stamped by the edge, so devidp's sign-up has one with no browser code) and
// its method, provider and verification.
func suCTE(lo, hi string) string {
	return `su AS (
  SELECT * FROM (
    SELECT EnvId, UserId, min(Timestamp) AS at, toDate(min(Timestamp)) AS d,
           argMinIf(DayVisitor, Timestamp, DayVisitor != '') AS own_h,
           argMin(Props['method'], Timestamp) AS method, argMin(Props['provider'], Timestamp) AS provider,
           argMin(Props['social_provider'], Timestamp) AS social, argMin(Props['verified'], Timestamp) AS verified
    FROM product_events
    WHERE EventName = 'auth_signed_up' AND UserId != '' AND ` + envScope + ` AND ` +
		lastDays(obsquery.NewUsersWindowDays, to) + `
    GROUP BY EnvId, UserId)
  WHERE at >= ` + lo + ` AND at < ` + hi + `)`
}

// suhCTE gives each sign-up its visitor hash: its own, else the one hash the
// stitch links to the user on the sign-up's day. ” is a sign-up with no
// visit: Unattributed, never Direct.
const suhCTE = `su_h AS (
  SELECT su.EnvId AS EnvId, su.UserId AS UserId, su.at AS at, su.d AS d,
         if(su.own_h != '', su.own_h, l.h) AS h,
         su.method AS method, su.provider AS provider, su.social AS social, su.verified AS verified
  FROM su LEFT JOIN (SELECT EnvId, uid, d, any(h) AS h FROM stitch GROUP BY EnvId, uid, d HAVING count() = 1) AS l
    ON l.EnvId = su.EnvId AND l.uid = su.UserId AND l.d = su.d)`

// activationPredicate is the event that marks a user activated: the declared
// one, else any <entity>_created (the automatic CRUD event).
func activationPredicate(a *Analytics) string {
	if a != nil && a.Activation != "" {
		return "EventName = " + lit(a.Activation)
	}
	return "match(EventName, '_created$')"
}

// activationTitle names the activation event in a description.
func activationTitle(a *Analytics) string {
	if a != nil && a.Activation != "" {
		return "the first " + a.Activation + " event"
	}
	return "the first <entity>_created event (the automatic CRUD event)"
}

// actCTE is each user's first activation event in [lo, hi), and the user's
// person key.
func actCTE(a *Analytics, lo, hi string) string {
	return `act AS (
  SELECT EnvId, UserId, concat('u:', UserId) AS person, min(Timestamp) AS at
  FROM product_events
  WHERE UserId != '' AND ` + activationPredicate(a) + ` AND ` + envScope + ` AND Timestamp >= ` + lo + ` AND Timestamp < ` + hi + `
  GROUP BY EnvId, UserId)`
}

// personsCTE is the rows of rng matching extra (bots aside), each with its person key:
// the signed-in user, else the user the stitch links the row's visitor-day
// to, else the consented persistent visitor id, else the visitor-day. ”
// is a row no identity reaches (a server event of an anonymous request). It
// needs stitchCTE.
func personsCTE(rng, extra string) string {
	if extra != "" {
		extra = " AND " + extra
	}
	return `persons AS (
  SELECT e.ts AS ts, e.EnvId AS EnvId, e.EventName AS EventName, e.Props AS Props, e.UserId AS UserId,
         multiIf(e.UserId != '', concat('u:', e.UserId),
                 l.uid != '' AND e.Privacy = '', concat('u:', l.uid),
                 e.VisitorId != '', concat('v:', e.VisitorId),
                 e.DayVisitor != '', concat('d:', e.DayVisitor), '') AS person
  FROM (SELECT Timestamp AS ts, EnvId, EventName, Props, UserId, VisitorId, DayVisitor, Privacy
        FROM product_events WHERE ` + rng + ` AND ` + envScope + ` AND DeviceType != 'bot'` + extra + `) AS e
  LEFT JOIN stitch AS l ON l.EnvId = e.EnvId AND l.h = e.DayVisitor)`
}

// with joins CTEs into a WITH clause.
func with(ctes ...string) string {
	return "WITH " + strings.Join(ctes, ",\n")
}

// channelExpr is CHANNEL(ref, src, med, click): the deterministic channel
// table (published in the observability/product-analytics skill), with the
// project's own rules tried first. Every input is lowercase already.
func channelExpr(rules []ChannelRule, ref, src, med, click string) string {
	var b strings.Builder
	b.WriteString("multiIf(\n")
	for _, r := range rules {
		var conds []string
		if r.Host != "" {
			h := strings.ToLower(r.Host)
			conds = append(conds, fmt.Sprintf("(%s = %s OR endsWith(%s, %s))", ref, lit(h), ref, lit("."+h)))
		}
		if r.Source != "" {
			conds = append(conds, src+" = "+lit(strings.ToLower(r.Source)))
		}
		if r.Medium != "" {
			conds = append(conds, med+" = "+lit(strings.ToLower(r.Medium)))
		}
		if len(conds) == 0 {
			continue
		}
		fmt.Fprintf(&b, "  %s, %s,\n", strings.Join(conds, " AND "), lit(r.Channel))
	}
	r := strings.NewReplacer("REF", ref, "SRC", src, "MED", med, "CLICK", click)
	b.WriteString(r.Replace(`  MED IN ('cpc', 'ppc', 'paid', 'paidsearch', 'paid_search') OR CLICK IN ('google', 'microsoft'), 'Paid search',
  MED IN ('paid_social', 'paidsocial', 'social_paid', 'paid-social') OR CLICK IN ('meta', 'tiktok', 'linkedin'), 'Paid social',
  MED IN ('display', 'banner', 'cpm', 'programmatic'), 'Display',
  MED IN ('email', 'e-mail', 'e_mail', 'newsletter'), 'Email',
  MED = 'affiliate', 'Affiliate',
  match(REF, '(^|\\.)(chatgpt\\.com|chat\\.openai\\.com|perplexity\\.ai|claude\\.ai|gemini\\.google\\.com|copilot\\.microsoft\\.com|you\\.com)$')
    OR SRC IN ('chatgpt.com', 'chatgpt', 'perplexity', 'claude'), 'AI assistants',
  MED IN ('social', 'social-network', 'social-media', 'sm')
    OR match(REF, '(^|\\.)(facebook|instagram|linkedin|lnkd|reddit|youtube|tiktok|pinterest|threads|bsky|mastodon|t)\\.(com|co|in|app|social|net)$|(^|\\.)(x\\.com|news\\.ycombinator\\.com)$'), 'Organic social',
  MED = 'organic' OR match(REF, '(^|\\.)(google|bing|duckduckgo|yahoo|baidu|yandex|ecosia|brave|kagi|startpage|qwant)\\.'), 'Organic search',
  SRC != '' OR MED != '', 'Other campaign',
  REF = '', 'Direct',
  'Referral')`))
	return b.String()
}

// sourceExpr is SOURCE(ref, src): the campaign source (utm_source, else
// Plausible's ?ref=), else the referrer host, else "(direct)".
func sourceExpr(ref, src string) string {
	return fmt.Sprintf("if(%s != '', %s, if(%s != '', %s, '(direct)'))", src, src, ref, ref)
}

// lit is s as a ClickHouse string literal.
func lit(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// lits is ss as a ClickHouse array of string literals.
func lits(ss []string) string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = lit(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// windowSeconds is a declared window ("7d", "30m") in seconds, or the
// fallback when it does not parse (KCL validates it, so only a hand-built
// Analytics reaches the fallback).
func windowSeconds(w, fallback string) int64 {
	d, err := obsquery.FunnelWindow(w)
	if err != nil || d <= 0 {
		d, _ = obsquery.FunnelWindow(fallback)
	}
	return int64(d / time.Second)
}

// activationWindow is the declared activation window as an INTERVAL.
func activationWindow(a *Analytics) string {
	return fmt.Sprintf("INTERVAL %d SECOND", windowSeconds(a.EffectiveActivationWindow(), DefaultActivationWindow))
}
