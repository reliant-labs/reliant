---
name: dashboards
description: Diagnose empty dashboards by replaying console panels and separating absent data from window, scope, filter and query failures.
---

# Debugging empty dashboards

#### An empty dashboard: do we have the data, and why isn't it showing?

Two different questions, and one command answers both. `forge obs dashboards
audit` lists the dashboards the console lists for the env and sends each panel
to the same server-side `QueryPanel` the console calls, over the range the
console opens each dashboard on (its `defaultRange` rounded up to a preset, else
the last hour; `--since` replaces it, as a viewer who picked a range), with the
step left to the control plane. A sql panel whose `minStep` is wider than that
window is widened the way the console widens it (to the smallest preset holding
two buckets), and the panel reports `widened` and the window it really ran over.
It rebuilds no panel SQL, so it cannot disagree with the console.

```bash
forge obs dashboards audit prod                                  # every panel, failures first
forge obs dashboards audit prod --dashboard product-usage --json  # one dashboard, as a document
forge obs dashboards audit prod --dashboard product-usage --panel workflows-by-outcome --since 24h
```

Read each panel's `status`, `reason` and `probable_cause`, not just the status:

| status | meaning | next |
|---|---|---|
| `ok` | data the console can draw (`all_zero` in `note` means it is zero, not missing) | none |
| `partial` | data, but a series' metric is absent or stored as another kind, or the answer is **one bucket** where a line needs two *under the console's own window* (a day-bucketed sql panel with no `minStep`, which the console cannot widen) | declare `minStep` / `defaultRange`, or fix the panel's bucketing |
| `empty` | nothing came back; `probe.verdict` says whether the data exists | by verdict, below |
| `error` | the read failed; `error.class` names it | by class, below |

An empty panel is probed over `--probe-window` (default 7d): the same panel in
every env of the project, and without its `where` filters, plus a metric catalog
lookup. The verdict separates **no data** from **data outside the window, scope
or filter**. A panel whose window is already the longest its signal allows (a 7d
traces dashboard) cannot look further back; the env, filter and catalog probes
still run over its own window, and the skipped step says why:

- `data_outside_window`: the data exists; the window is shorter than its cadence
  or the emitter went quiet. Widen `--since`.
- `data_in_other_env`: it exists in another env; rows carry that env's id, or none
  (an env-scoped panel cannot see untagged rows).
- `data_excluded_by_filters`: it exists without the panel's `where`; the filter
  value matches nothing the emitter writes.
- `data_in_other_env_without_filters`: it exists only with both lifted: another
  env, and the filter matches nothing there either.
- `metric_never_seen` / `no_data_anywhere`: the emitter is not shipping it, or the
  name is wrong. `forge obs metrics describe <env> <metric> --since 168h`, then
  `forge obs query` for the table itself.
- `probe_failed` / `not_probed`: the follow-up could not look (a read failed, or
  nothing was left to check); this is **not** "no data". Fix the cause in
  `probe.steps[].error`.

Failure classes are never "no data": `auth` (HTTP 403 or a missing scope such as
`obs:read`: a credential problem for an org admin, not an outage), `quota` (the
org's hourly allowance is spent; the audit stops reading and says so), `in_flight`
and `rate_limited` (retried, then reported; wait and rerun, or audit one
`--dashboard` at a time), `timeout`, `storage`, `invalid` (a spec or range the
control plane refused). Exit 0 means every panel is `ok`, **2** that some panel is
empty or partial (or the audit could not look), **1** that a panel or dashboard
could not be read. A local env has no dashboards to replay: use
`forge obs dashboards check --against <env>` for its metric names.
