---
name: panels
description: The dashboard panels beyond the basic queries — postgres panels (a live read-only query of your own database), row links into the data browser, saved data-browser views (browse), text panels (Markdown), display options that draw one query as area, stacked, bars, pie, donut, gauge, funnel or heatmap, palette colours per series, and sparklines on sql figures (trendSql).
---

# Postgres, links, saved views, text, display and colours

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

## Row links: `link`

A `table` or `bar` panel, or a `postgres` panel drawn as `table`, `bar` or
`auto`, may carry `link`: clicking a row opens the console's data browser.
HyperDX ignores it. Two forms:

```json
"link": { "database": "app", "table": "public.users", "column": "id", "fromField": "span.attr.user_id" }
"link": { "database": "app", "table": "public.orders", "row": true, "fromFields": ["order_id"] }
```

- The first opens `table` filtered to `column` = the row's `fromField`; the
  second opens one row, its primary key read from `fromFields` in key order (at
  most 8).
- `table` is schema-qualified (`public.users`). `database` is a postgres
  panel's database name.
- On a `table` or `bar` panel a field must be one of its `groupBy` fields, the
  only values its rows carry, so a telemetry panel can link into Postgres (the
  top `span.attr.user_id` bars, linked to `users` by id). On a `postgres` panel
  it is a result column, which only the query knows: a row without it is drawn
  unlinked.
- The console checks the table exists; a link to one that does not is drawn
  disabled.

## Saved views: `browse`

A `browse` panel is a saved data-browser view: one table, drawn as the
browser's grid, with "Open in Data" opening the same view. It takes `browse`
and nothing else: no `signal`, `series`, `sql`, `where`, `groupBy` or `unit`.

```json
{ "id": "new-pro-users", "title": "New pro users", "kind": "browse",
  "browse": { "database": "app", "table": "public.users",
    "columns": ["id", "email", "created_at"],
    "filters": [{ "column": "created_at", "op": "gte", "values": ["{from}"] },
                { "column": "plan", "op": "eq", "values": ["pro"] }],
    "sort": [{ "column": "created_at", "desc": true }],
    "size": 50 } }
```

- `columns` (at most 100; empty is every column the read-only role reads),
  `filters` (at most 32, ANDed), `sort` (at most 8; the primary key follows as a
  tiebreak) and `size` (25, 50, 100, 200 or 500; default 50).
- `op` is the data browser's: `eq`, `neq`, `lt`, `lte`, `gt`, `gte`, `like`,
  `ilike` (one value), `is_null`, `not_null` (none), `in` (1 to 100). A value
  is text as Postgres reads it for the column, at most 4 KiB.
- A value that is exactly `{from}` or `{to}` is the dashboard's range, and
  `{<filter id>}` a dashboard filter's value (a toggle's is `true`/`false`).
  They are bound, never spliced. A filter whose dashboard filter the viewer has
  not set is left out. Any other `{name}` is refused.
- It is not SQL: it reads only what the role may, and cannot be a bad query.

`browse` and `link` are refused by a control plane pinned to an older forge, so
add them once the control plane runs the forge that ships them.

## Releases and delivery: `release_health`, `promotions`, `delivery`

Three kinds draw what the team ships rather than what its services emit. The
data is the control plane's (the release ledger and the release-health
verdict), so a panel carries no query: no `signal`, `series`, `sql`, `where`,
`groupBy`, `unit` or `display`.

```json
{ "id": "release-health", "title": "Latest release by environment", "kind": "release_health", "width": 24, "height": 10 }
{ "id": "delivery", "title": "Delivery performance", "kind": "delivery", "width": 24, "height": 5 }
{ "id": "promotions", "title": "Promotions", "kind": "promotions", "limit": 25 }
```

- `release_health` judges each environment's running release against the one
  it replaced with release health (`GetReleaseHealth`, the rule and the env's
  thresholds `forge obs check` uses), so it can never disagree with the
  release's Health tab or the CLI. It ignores the range: the rule plans its own
  windows around the promotion.
- `promotions` lists the promotions and rollbacks in the range, newest first
  (`limit` 1 to 200, default 50); `delivery` is the four DORA figures over the
  range, each with its definition.
- They follow the viewer's environments (every environment of the project when
  none is selected); a service selection does not narrow them.

A control plane pinned to a forge older than the one that added them still
shows a release's dashboard that uses them, but refuses to save a copy of it.

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
| `number` | `stat`, `gauge` | `min` (default 0) and `max` (gauge; required unless `unit` is `percent`, whose scale is 0 to 1), `exact` |
| `sql` | `line`, `area`, `bars`, `funnel`, `heatmap` | `stack` (area or bars), `topN` (line styles), `exact` (figures) |

Every kind with a `display` also takes `color` and `colors` (below).

- `stack` adds the series up, so the top edge is their total: right for counts,
  sums and rates, wrong for a p95 or an average. You decide.
- `exact: true` draws a figure in full (1,284), never abbreviated (1.3k): for
  a count a reader checks against another page that states it exactly.
- `topN` (1 to 50) is how many series (or slices) are drawn before the rest
  fold into one "Other", summed where the values add up. The console keeps the
  full list behind "Show all"; without `topN` it picks its own.
- `funnel` on a `sql` panel draws its rows as funnel steps in the order the
  statement returns them: the first text column names the step, the first
  numeric column counts it, and the console shows the conversion and drop-off
  between steps. Use it for a funnel the `funnel` kind cannot express, such as
  one stitched across visitor and user identities.
- `heatmap` on a `sql` panel draws its result as a grid of cells shaded by
  value on one colour scale. A wide result (text columns, then numeric ones,
  like a retention table) is a row of cells per row, one column per numeric
  column; a long result (exactly two text columns and one number) puts the
  first text column down the side and the second across; a time series is a
  row per series, a cell per bucket. A NULL is an empty cell, not a zero, so a
  week that has not happened yet is missing rather than churned. It takes no
  `stack`, `topN` or `colors`.

```json
{ "id": "reconciles", "title": "Reconciles by result", "kind": "line", "signal": "metrics",
  "groupBy": ["metric.attr.controller", "metric.attr.result"],
  "display": { "style": "area", "stack": true, "topN": 8 },
  "series": [{ "label": "reconciles", "agg": "sum", "field": "controller_runtime_reconcile_total", "metricType": "sum" }] }
```

### Colours: palette keys, never hex

A renderer deals series their colours in rank order. To make a colour mean
something ("signed in" always blue, "anonymous" always slate), name it:

```json
"display": { "style": "bars", "stack": true,
             "colors": { "Signed in": "blue", "Anonymous": "slate" } }
```

- The keys are the palette's: `blue`, `violet`, `cyan`, `amber`, `fuchsia`,
  `slate`, and `danger` for a series that is a failure. A raw colour (`#ff0000`,
  `green`) is refused: the console maps each key to a chart token that keeps
  its contrast on every colour scheme, light and dark, which a hex cannot.
- `colors` names series by what the chart labels them: a series' `label`, a
  `sql` panel's value column, or a group value (a bar group, a pie slice, a
  line per group). Unnamed series take the palette colours no named series
  has, so naming two never repaints the rest. At most 50 names.
- `color` is the colour of what a panel draws in one: a heatmap's scale, a
  figure's sparkline, a chart of one series.
- Red already means "error" in the console: a series whose name says it is one
  (`errors`, `failed`, `5xx`) is drawn in `danger` without asking. Use `danger`
  for one whose name does not.

### Sparklines on sql figures: `trendSql`

A `sql` panel whose statement returns one row of figures draws them as numbers.
Give it `trendSql`, a second read-only `SELECT` under the same guard and with the
same `{from}`, `{to}`, `{step}`, ... parameters, returning a time series (first
column a date or time, then one numeric column), and the console draws it as a
sparkline under the figure. It never changes the figure, so the two statements
must agree on what they count: a "Daily active users" figure for the last day
over a `trendSql` of each day's active users.

```json
{ "id": "dau", "title": "Daily active users", "kind": "sql", "unit": "count",
  "sql": "SELECT uniqExact(UserId) AS \"Daily active users\" FROM product_events WHERE ...",
  "trendSql": "SELECT toDate(Timestamp) AS day, uniqExact(UserId) AS \"Daily active users\" FROM product_events WHERE ... GROUP BY day ORDER BY day",
  "display": { "color": "blue" } }
```

A panel drawn as a `funnel` or a `heatmap` draws no figure and takes no
`trendSql`. HyperDX ignores it.

`heatmap`, `trendSql`, `color` and `colors` (and a dashboard's `defaultCompare`
and a section's `description`) are refused by a control plane pinned to an
older forge, so add them to a hand-written dashboard once the control plane
runs the forge that ships them.
