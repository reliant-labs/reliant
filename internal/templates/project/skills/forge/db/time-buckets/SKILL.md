---
name: time-buckets
description: Reading time-series data back — why a two-argument date_trunc over a TIMESTAMPTZ silently attributes every total to the wrong day, why TIMESTAMPTZ is still the right column type, and the three-argument form that pins the zone in the query.
---

# Reading time-series back

`db/seeding` covers writing time-series rows. This is the other half — reading
them back — and it has one trap that produces wrong numbers with no error at
all.

## The trap: `date_trunc` uses the SESSION's timezone

A `google.protobuf.Timestamp` field becomes a `TIMESTAMPTZ` column, which is the
right type: an instant is stored as an instant, correctly, on every host.

But **`date_trunc` with two arguments truncates a `TIMESTAMPTZ` in the SESSION's
timezone**, and the driver sets the session timezone from the *client host*. So
this query does not mean "per UTC day" — it means "per day wherever this process
happens to be running":

```sql
-- WRONG: the bucket boundary moves with the deploy host
SELECT date_trunc('day', recorded_at) AS bucket, sum(distance_m)
FROM telemetry_samples GROUP BY 1 ORDER BY 1;
```

Measured against real postgres, over five samples spanning exactly two UTC
calendar days:

> session `TimeZone=UTC` → 2 buckets
> session `TimeZone=America/New_York` → 2 buckets, boundaries at 05:00Z
> session `TimeZone=Asia/Tokyo` → **3 buckets**

Same rows, same query, three different answers.

## Why this outranks an ordinary SQL gotcha

Nothing fails. No constraint is violated, no type error is raised, no test
breaks, nothing is logged. The chart renders and its bars are simply attributed
to the wrong days, by a fraction of a day that *changes when you deploy to a
differently-configured host*.

A CI box on UTC and a developer laptop on UTC-5 therefore produce different
totals from identical rows, which reads as flakiness rather than as a timezone
bug — so the investigation starts in the wrong place and stays there.

## The fix: pin the zone in the query

The three-argument form takes the zone from the query text rather than from
wherever the process runs:

```sql
-- RIGHT: same answer on every host
SELECT date_trunc('day', recorded_at, 'UTC') AS bucket, sum(distance_m)
FROM telemetry_samples GROUP BY 1 ORDER BY 1;
```

Three things worth being explicit about:

- **`TIMESTAMPTZ` is still the right column type.** Do not "fix" this by storing
  wall-clock `TIMESTAMP`; that throws away the instant and moves the ambiguity to
  write time, where it is worse and no longer recoverable. The instant is stored
  correctly. Only the *bucketing* needs a zone.
- **A local-time bucket can be correct** — a shift report, a business-day
  rollup. Say so by naming the zone
  (`date_trunc('day', recorded_at, 'America/New_York')`), not by omitting the
  argument. Both explicit spellings are stable across hosts; the two-argument
  form is the only one that is not.
- **A `TIMESTAMP WITHOUT TIME ZONE` column has no session dependence** and needs
  nothing. Nor does a unit finer than an hour: every offset in the tz database is
  a whole number of minutes, so a per-minute bucket cannot straddle a boundary.

## Why this bites forge projects specifically

The generated ORM cannot express `GROUP BY` / `SUM`, so any reporting screen goes
through raw SQL — in `internal/db/<entity>_repo_ext.go`, in `db/queries/`, or in
a view. That is exactly where this construct lives, so forge's own guidance about
reporting queries leads authors straight into it.

`forge lint --time-bucketing` flags the two-argument form over a `TIMESTAMPTZ`
column. It is warnings only, since a local-time bucket can be deliberate, and it
stays silent on the three-argument form, on `TIMESTAMP` columns, on sub-hour
units, and on anything whose column type it cannot resolve.

See also: `db` for the schema/ORM half, `db/seeding` for writing the rows this
skill reads back.
