---
name: panels
description: The dashboard panels beyond the basic queries — postgres panels (a live read-only query of your own database), text panels (Markdown), and display options that draw one query as area, stacked, bars, pie, donut, gauge or funnel.
---

# Postgres, text and display

The rest of the spec vocabulary (kinds, series, filters, sections) is in
`observability/dashboards`.

## Postgres panels

A `postgres` panel runs one statement against a database of yours, live, each
time the panel loads. It is the panel for what only the database knows: signups
per hour, rows by state, the size of a queue.

```json
{
  "id": "signups",
  "title": "Signups per hour",
  "kind": "postgres",
  "postgres": {
    "database": "app",
    "draw": "line",
    "time": "t",
    "values": ["signups"],
    "sql": "SELECT date_trunc('hour', created_at) AS t, count(*) AS signups FROM users WHERE created_at >= $1 AND created_at < $2 GROUP BY 1 ORDER BY 1"
  }
}
```

- `database` is a lowercase name (`a-z`, `0-9`, `-`, `_`; at most 63 bytes),
  the same name `forge db query` takes.
- `sql` is one read-only `SELECT`, `WITH`, `TABLE` or `VALUES` statement of at
  most 16 KiB, checked by the same guard as `forge db query`. It may use three
  bound parameters, never spliced: `$1` the start of the dashboard's range
  (`timestamptz`, inclusive), `$2` its end (`timestamptz`, exclusive) and `$3`
  a bucket width in seconds suited to the range (`int4`). A statement need not
  use them, but one that uses `$2` must use `$1` as well (Postgres cannot type a
  skipped parameter), and none may use `$4`. Give a parameter with no context a
  cast (`$3::int4`).
- `draw` is `auto` (the default), `line`, `number`, `table` or `bar`. Auto draws
  a line when `time` is set, a number for one row of one value, a table
  otherwise. `line` requires `time`, the column holding each row's timestamp;
  `number` takes at most one of `values`, the numeric columns to plot (empty
  means every column that is not `time` or in `group`); `group` names the
  columns that split rows into one series each. Column names are 1 to 63 bytes.
- The statement is read only and runs at read committed with a 5 s statement
  timeout and a 1 s lock timeout, so a dashboard never blocks other queries and
  waits behind no writer. Keep it cheap: an indexed range scan on the time
  column, not a table scan.

HyperDX skips a `postgres` panel, as it does the other console kinds.

## Text panels

A `text` panel is prose, not data: `markdown` (CommonMark with GFM tables and
links, at most 10,000 bytes) and nothing else, no `signal`, `series` or
`where`. Use it for a heading over a row of panels, what a dashboard is for, or
a runbook link. The console runs no query for it; HyperDX skips it.

```json
{ "id": "about", "title": "About", "kind": "text", "width": 24, "height": 2,
  "markdown": "Flux reconciles every minute. **Errors** page the on-call; see [the runbook](https://example.com/runbooks/flux)." }
```

## Display: one query, several pictures

`display` changes how a `line`, `bar`, `number` or `sql` panel is drawn, never
what it queries, so a reader that does not know an option draws the kind's
default and is still right about the numbers. Every field is optional:

| kind | `style` (first is the default) | other fields |
|---|---|---|
| `line` | `line`, `area`, `bars` (one bar per bucket) | `stack` (area or bars), `topN` |
| `bar` | `bar`, `pie`, `donut` (a pie takes exactly one series) | `stack` and `horizontal` (bars), `topN` (pie, donut) |
| `number` | `stat`, `gauge` | `min` (default 0) and `max` (gauge; required unless `unit` is `percent`, whose scale is 0 to 1) |
| `sql` | `line`, `area`, `bars`, `funnel` | `stack` (area or bars), `topN` (line styles) |

- `stack` adds the series up, so the top edge is their total: right for counts,
  sums and rates, wrong for a p95 or an average. You decide.
- `topN` (1 to 50) is how many series (or slices) are drawn before the rest
  fold into one "Other", summed where the values add up. The console keeps the
  full list behind "Show all"; without `topN` it picks its own.
- `funnel` on a `sql` panel draws its rows as funnel steps in the order the
  statement returns them: the first text column names the step, the first
  numeric column counts it, and the console shows the conversion and drop-off
  between steps. Use it for a funnel the `funnel` kind cannot express, such as
  one stitched across visitor and user identities.

```json
{ "id": "reconciles", "title": "Reconciles by result", "kind": "line", "signal": "metrics",
  "groupBy": ["metric.attr.controller", "metric.attr.result"],
  "display": { "style": "area", "stack": true, "topN": 8 },
  "series": [{ "label": "reconciles", "agg": "sum", "field": "controller_runtime_reconcile_total", "metricType": "sum" }] }
```
