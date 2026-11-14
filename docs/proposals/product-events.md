# Proposal: product events as a forge feature (typed, consent-aware, declarative retention)

**Status:** superseded on 2026-11-12 by the Events design (reliant-labs/growth
`docs/product/forge-events-design-2026-11-12.md`, approved by the owner). Kept
for its history; read the section below for where each point landed.
**Date:** 2026-10-21
**Origin:** forge#1013, moved here on 2026-11-04 and closed.

## Where this proposal landed (2026-11-12)

Product events are a forge feature, called **Events** in the docs (the code
keeps `Track`/`track` and "product events"). One generic primitive, an INFO OTel
log record `event.name = product.<name>` (`pkg/observe/track.go`), with these
emitters: Go `observe.Track`, the browser's `track`, generated CRUD, auth
events, and CLIs (`pkg/observe/cliev`).

| This proposal                            | Now                                                                                                                                                                                                           |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Long-term retention (option 2)           | Built: the per-org `product_events` table, retention stamped per row (default 90 days, plan cap 90 or 400; `Observability.retention.product_events_days`).                                                    |
| Consent in the primitive, failing closed | Browser: the analytics tiers (`off < aggregate < identified < full`, GPC/DNT). Server: `observe.SetConsentSource`, fail-closed (forge#2169).                                                                  |
| The pipe is the app's telemetry          | Yes: logs over OTLP or stdout, browser spans or logs through `/_otel` or the keyed ingest; CLIs through a native ingest key.                                                                                  |
| Retention declared                       | `Observability.retention.product_events_days`.                                                                                                                                                                |
| Deletion: a per-subject purge            | Approved for v1; the mechanism is with the owner.                                                                                                                                                             |
| Queries for funnels and cohorts          | Built-in Users/Product/Acquisition dashboards; `forge.Funnel` (by session, user, visitor, person, org; `forge.Step` where/within) and `forge.Count`, project-level on `Observability.analytics` (forge#2176). |
| Declared, typed events in proto          | Not built: events stay `Track(ctx, name, attrs...)`, and a forge lint (`forgeconv-product-event-pii`) plus the emitter's PII guard keep props to tags.                                                        |
| Pseudonymous ids                         | Ids are opaque only (auth subject, org id, random install or visitor id); the edge's daily visitor hash counts anonymous visitors.                                                                            |

Also added by the Events design: an org actor (`org.id`, `observe.WithActor`;
control-plane tenant schema v29 `OrgId`) and the forge CLI's own anonymous
`forge_cli_first_run` (on by default with a notice; `DO_NOT_TRACK`,
`FORGE_TELEMETRY=off`, off in CI).

The rest of this file is the proposal as it stood on 2026-10-21.

## Summary

Product events (funnels, retention cohorts) should be a **forge feature**: a typed, consent-aware product-event primitive that any forge app gets, and that Reliant's hosted customers can use for their own apps, not something Reliant builds for itself alone. Reliant is the first consumer.

**Decision (owner, 2026-10-21):** keep today's retention until launch. Before launch, pick a long-term retention design from the options below and build it as this feature.

## Where we are today (reliant #1108, control-plane #1059)

- Statsig is gone. Each product event is one INFO log line, `msg = "product.<event>"`, with:
  - `event.name`, `user.id`, `app.version`;
  - the typed fields as `product.<snake_case>`;
  - `trace_id`/`span_id` when the event happens inside a request.
- Prod server stdout is collected into ClickStack's `otel_logs`, so a funnel is a ClickHouse query (`windowFunnel`). See `reliant/dev-docs/product-events.md`.
- Web `trackEvent` emits a `product.<event>` span, and still goes to Sentry until web Sentry is retired. The web ClickStack sink isn't live in any environment, because `VITE_OTEL_ENDPOINT` is unset; that work belongs with retiring web Sentry.
- Consent:
  - Each user's analytics opt-out is honoured.
  - Consent fails closed: if the opt-out can't be read, nothing is recorded.
  - A kill switch exists.
  - Each event is bound to the user who triggered it.
- **Retention:** backend events last as long as INFO logs (14 days); web spans last 7 days. That covers short funnels such as onboarding drop-off, but not retention cohorts.

## Options for long-term retention

| #   | Option                                                                                                     | Answers                                                                  | Privacy cost                                                                                                               | Work                                             |
| --- | ---------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------ |
| 1   | Keep as-is                                                                                                 | Funnels over days                                                        | None                                                                                                                       | None                                             |
| 2   | ClickHouse materialized view copies `product.*` rows into their own table with a long TTL (e.g. 13 months) | Per-user funnels and retention cohorts; new questions asked of past data | Per-user ids kept for 13 months. The privacy page changes, and account deletion must purge this table. Needs legal review. | Small                                            |
| 3   | Keep only aggregates long-term (daily counts, cohort tables, no user id)                                   | Only metrics defined up front                                            | Minimal: no personal data past 14 days                                                                                     | Medium: metrics have to be defined ahead of time |
| 4   | Raise the TTL on all logs                                                                                  | Everything                                                               | Worst: every log line is kept                                                                                              | Expensive                                        |

**Recommendation:** option 2 with a 13-month TTL. Make the account-deletion purge a hard requirement, and get legal to sign off on the privacy page before launch. Option 3 is the fallback if legal prefers no long-lived per-user data. Option 4 is rejected.

## What "a feature customers can leverage" means

The design should come out of forge rather than reliant:

1. **Declared events.** Events are declared and typed in proto, for example an event message or a method option, the same way `(forge.v1.method).activity` declares activity events (forge #911). Forge then generates the typed `Track*` client for Go and TS, so call sites can't misspell an event or a field.
2. **Consent is part of the primitive.** Opt-out is per subject, consent fails closed, and there is a kill switch. The app supplies the consent lookup through a one-method interface. No event leaves the process for an opted-out subject.
3. **The pipe is the app's existing telemetry.** Events flow as OTel log records through forge `observe`, so there is no second exporter. The sink must survive OTLP log export being off by default; today that is why Reliant writes stdout log lines.
4. **Retention is declarative.** The chosen option (2 or 3) is declared in config or KCL, for example `product_events: {retention: 13mo, store: per_subject|aggregate}`, and forge's ClickStack deploy renders the materialized view and TTL. Hosted customers get it without writing ClickHouse DDL.
5. **Deletion is part of the primitive too.** A generated purge for one subject, which the app calls from its account-deletion path. A lint or test checks that it is wired in.
6. **Queries.** A small, documented query surface (funnel and cohort helpers, or saved ClickHouse queries), so customers don't each re-derive `windowFunnel` SQL. This could later surface in the console.
7. **Reliant is the first consumer.** Migrate `internal/analytics` and `web/src/lib/analytics.ts` onto the forge primitive and delete the hand-rolled client.

## Open questions

- Whether web events go through the browser OTel sink to ClickStack directly, or through the app's API. The latter is better for ad-blockers and consent enforcement.
- Where customers' event data lives in the shared hosted ClickStack: per-project database or table, and how one project is kept from reading another's events.
- Pseudonymous subject ids (salted hash) versus raw user ids for option 2.

## References

- reliant #1108, control-plane #1059: Statsig removal; product events on the telemetry pipeline
- forge #911: the typed activity stream, the closest existing primitive
- `reliant/dev-docs/product-events.md`
