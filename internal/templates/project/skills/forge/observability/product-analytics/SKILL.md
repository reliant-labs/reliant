---
name: observability/product-analytics
description: The built-in product analytics dashboards every hosted forge app gets with no code — the Overview's Traffic and Users sections, and the Users, Product and Acquisition dashboards (pkg/obsbuiltin) — with every definition they count by (cookieless visitor-day, visit, bounce, channel table, source, same-day stitch, person, active/new/returning, sign-up attribution, activation), how to extend them (forge.Analytics goals, activation, channels; forge.Funnel by visitor/person), override one by a same-named file, or opt out (dashboards = False).
---

# Built-in product analytics

A forge app on Reliant hosting gets product analytics with nothing declared:
the Overview's **Traffic** and **Users** sections, and three dashboards in the
console's Product group, **Users**, **Product** and **Acquisition**. Every panel
is a `sql` panel over the hosted per-org `product_events` table, which the
platform fills from the events forge already emits: `page_viewed` from the
browser runtime, the standard `auth_*` events, the automatic CRUD events, the
app's own `track()` / `observe.Track` events. Where those come from:
`observability/product-events`.

`forge generate` writes them into the dashboard spec and `forge env build`
ships them in the release bundle, like every generated dashboard. They are
**hosted only**: the local ClickStack has no `product_events` table, so no
HyperDX file is written and the local Overview is unchanged. The builders are
the public package `forge/pkg/obsbuiltin`, so the control plane serves the same
definitions for an environment that has no bundle yet.

| Dashboard | id | Opens on | When |
|---|---|---|---|
| Overview, Traffic section | `overview` | 24h | the project has a browser frontend |
| Overview, Users section | `overview` | 24h | always |
| Users | `users` | 30d, compared with the previous period | always |
| Product | `product` | 7d, compared with the previous period | always; it **replaces** the old generated _Product usage_ (`usage`), so stars and copies move to it |
| Acquisition | `acquisition` | 30d, compared with the previous period | the project has a browser frontend |

A static site's environment (no process) keeps Users, Product and Acquisition
beside its frontend dashboard.

## The definitions

Every number on the four dashboards is one of these. They are the same rules
`forge obs users` and the console's Users card count by.

**Visitor.** The platform's ingest edge hashes each browser request into a
cookieless daily visitor id, `DayVisitor`: a hash of a salt that is deleted
after the day, the address and the user agent. Nothing is stored on the device
and no fingerprinting script runs. A hash is new every UTC day, so a visitor is
a **visitor-day**: one person on two days, or on two devices, is two. Its day
is the day the edge **received** the batch, not the event's (an event of 23:59
sent after midnight carries the next day's hash), so the dashboards key a
visitor by `(EnvId, DayVisitor)` alone and never pair it with
`toDate(Timestamp)`. Bots get no hash and are never counted. People behind one
office NAT with the same browser collapse into one visitor.

**Web rows.** The events of the `web` and `landing` clients that carry a hash,
except the ones that are not interaction: `auth_signed_in` (an identity
heartbeat), `auth_signed_out`, `flag_evaluated`, `page_engaged` and the
platform's `auth_accounts`.

**Visit.** One visitor's web rows in time order; a new visit starts after a gap
of more than 30 minutes. Visits are built at query time, so there is no session
id on the wire. A visit that crosses midnight inside one batch stays one visit.

- **Bounce**: a visit with exactly one `page_viewed` and nothing else.
- **Visit duration**: last event minus first; a bounce lasts 0 and counts.
- **Where a visit came from**: its first `page_viewed`'s `referrer_host`,
  `referrer_path`, `utm_source` (else `ref`), `utm_medium`, `utm_campaign`,
  `utm_term`, `utm_content` and `click_id`.
- **First touch**: a visitor-day's first visit. Visitors are credited to the
  channel, source, landing page, campaign, country, device, browser and OS of
  their first touch.

**Channel.** The first rule that matches wins. The project's own
`forge.Analytics.channels` rules are tried first (a rule matches when every
condition it sets matches: `host` is the referrer host or a subdomain of it,
`source` the `utm_source`, `medium` the `utm_medium`, case-insensitive). Then:

| # | Channel | When |
|---|---|---|
| 1 | Paid search | medium `cpc`, `ppc`, `paid`, `paidsearch`, `paid_search`; or a `google`/`microsoft` click id |
| 2 | Paid social | medium `paid_social`, `paidsocial`, `social_paid`, `paid-social`; or a `meta`/`tiktok`/`linkedin` click id |
| 3 | Display | medium `display`, `banner`, `cpm`, `programmatic` |
| 4 | Email | medium `email`, `e-mail`, `e_mail`, `newsletter` |
| 5 | Affiliate | medium `affiliate` |
| 6 | AI assistants | referrer `chatgpt.com`, `chat.openai.com`, `perplexity.ai`, `claude.ai`, `gemini.google.com`, `copilot.microsoft.com`, `you.com` (or a subdomain); or source `chatgpt.com`, `chatgpt`, `perplexity`, `claude` |
| 7 | Organic social | medium `social`, `social-network`, `social-media`, `sm`; or referrer facebook, instagram, linkedin, lnkd, reddit, youtube, tiktok, pinterest, threads, bsky, mastodon, t (`.com`, `.co`, `.in`, `.app`, `.social`, `.net`), `x.com`, `news.ycombinator.com` |
| 8 | Organic search | medium `organic`; or referrer google, bing, duckduckgo, yahoo, baidu, yandex, ecosia, brave, kagi, startpage, qwant |
| 9 | Other campaign | any other `utm_source`, `ref` or `utm_medium` |
| 10 | Direct | no referrer |
| 11 | Referral | anything else |

**Source.** `utm_source` (else `?ref=`), else the referrer host, else `(direct)`.

**Same-day stitch.** A visitor-day is linked to a user when exactly one user id
appears on that hash's rows that day: a browser row after sign-in, or a server
row, since the hosted proxy stamps the hash on the requests it forwards, so
devidp's `auth_signed_up` carries it with no browser code. A hash two users share
links nobody, a browser under Global Privacy Control or Do Not Track is never
linked, and nothing is linked across days without the `full` tier's consent.

**Person.** The signed-in user; else the user the stitch links the row's
visitor-day to; else the consented persistent visitor id (`full` tier); else the
visitor-day. Goals, events by name, flag exposures and `by = "person"` funnels
count persons.

**User.** A user id per environment, the way the Users card counts it, so a
dashboard over several environments equals the sum of their cards.

- **Active**: any user-attributed product event except `auth_signed_out` and
  `auth_accounts` (`obsquery.ActiveEvidence`): a sign-in, the daily session
  heartbeat, a CRUD write, an app event.
- **New**: a user whose first `auth_signed_up` is in the range. An environment
  that reported no sign-up in the 90 days before the end of the range counts the
  users first seen in the range instead (`obsquery.NewUsersSource`); the Users
  dashboard's _Where auth events come from_ table says when.
- **Returning**: active and not new.

**Sign-up attribution.** A sign-up (a user's first `auth_signed_up`) is credited
to the **first touch of its own day**: its own hash, else the one hash the stitch
links to the user that day. A sign-up with no visit that day (through the API,
on another device) is **Unattributed**, never Direct. This is the cookieless
limit, stated on the panels: a visitor who came from a newsletter on Monday and
typed the URL to sign up on Wednesday is credited to Wednesday's source.

**Activation.** The declared `forge.Analytics.activation` event, else any
`<entity>_created` (the automatic CRUD event, so it needs no code), within
`activation_window` (default `7d`) of signing up.

## What each dashboard shows

**Overview.** The existing panels form its _Health_ section, first. _Traffic_:
visitors now (the last 5 minutes), visitors, page views and views per visit,
bounce rate, the top 5 pages and channels, JS errors per 1,000 page views
(`error_events`, kept 14 days by default). _Users_: active, new and returning
users over the last 7 days beside the 7 before (fixed, whatever range is
picked, as the Users card), accounts and 30 days of daily active users.

**Users.** _At a glance_: accounts (the platform's daily `auth_accounts`
snapshot; an environment with none shows its users seen in 90 days), active,
new, returning, daily/weekly/monthly active users, stickiness (average DAU over
average 30-day active users). _Engagement_: weekly lifecycle (new, retained,
resurrected, and churned below zero; the current week counts no churn until it
ends), power users by days active in the last 28 days, time from sign-up to
activation, and a weekly sign-up cohort retention heatmap (W0 is 100%; a week
not yet happened is an empty cell). _Authentication_: sign-ups by method,
credential sign-ins by method, verified share, sign-ups by provider, and which
clients report auth events. _People_: the most active users by opaque id.

**Product.** _At a glance_: page views, visits, views per visit, bounce rate,
visit duration, every product event. _Pages_: each route's views, visitors,
signed-in users, entries and exits; entry pages with bounce and same-day
sign-up rates; exit pages with exit rate. _Goals and events_: one row per
declared goal (persons, total, conversion over visitor-days), every other event
with its persons and last seen, and the CRUD writes per entity. _Funnels_:
the built-in visit → sign-up → activation funnel (the first step counts
visitor-days, the others persons; sign-ups with no visit count, and the share
that came from a visit is its own figure), and one funnel per `forge.Funnel`
with `by = "visitor"` (per visitor-day) or `"person"`. _Experiments_: each flag
and variant's exposed persons and the share that activated within the window
after their first exposure; descriptive, not a significance test.

**Acquisition.** _At a glance_: visitors, visits, sign-ups, visit-to-sign-up
(attributed sign-ups over visitor-days). _Channels_: visitors by channel per day,
the channel table (visitors, visits, bounce, sign-ups, conversion, and the share
of the sign-ups that activated: channel quality, not just volume), sign-ups by
channel per week. _Sources_: sources, referrers (host and path), campaigns
(`utm_*`), landing pages by channel. _Audience_: countries (ISO codes),
devices, browsers, operating systems. _Quality_: the share of each channel's
sign-ups active in their fourth week, over the sign-ups of 35 to 90 days before
the end of the range; a channel with fewer than 20 shows none.

## Extend, override, opt out

- **Extend by declaration**, no JSON: `forge.Analytics {goals, activation,
  activation_window, channels}` and `forge.Funnel {by = "visitor" | "person"}`
  regenerate the panels on the next `forge generate`. Syntax and the merge
  rules across frontends and environments: `observability/product-events`.
- **Override one dashboard** with a spec-form file of the same name:
  `deploy/observability/dashboards/<project>-users.json` replaces
  `<project>-users_gen.json` in the bundle (the same for `-product`,
  `-acquisition` and `-overview`). Start from the generated one: `forge env
  build` puts it in the bundle, and the panel ids above are stable. `forge obs
  dashboards check` validates it. A project dashboard declaring
  `"replaces": ["users"]` takes over the generated one's stars instead.
- **Opt out**: `forge.Analytics {dashboards = False}` drops the three
  dashboards and the Overview's Traffic and Users sections. Collection still
  follows `mode`; `mode = "off"` stops browser product events.

## Writing your own panel with the same rules

The fragments are Go in `forge/pkg/obsbuiltin` (`fragments.go`); `reliant forge
project libraries obsbuiltin` prints the package's API. A panel of your own
reaches the same answer by keying visitors on `(EnvId, DayVisitor)`, users on
`(EnvId, UserId)`, and active users on `obsquery.ActiveEvidence`:

```sql
SELECT Props['route'] AS route, uniqExact((EnvId, DayVisitor)) AS visitors
FROM product_events
WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}
  AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId))
  AND EventName = 'page_viewed' AND DayVisitor != '' AND DeviceType != 'bot'
GROUP BY route ORDER BY visitors DESC LIMIT 20
```

The edge columns (`DayVisitor`, `Country`, `Region`, `DeviceType`, `Browser`,
`OS`, `Privacy`, `SaltScope`) are `''` on rows stored before the platform added
them, and `SaltScope = 'local'` marks a day a proxy counted with its own salt
(visitors slightly over-counted).
