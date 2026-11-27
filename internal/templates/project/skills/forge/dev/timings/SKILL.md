---
name: dev/timings
description: Measure where forge env up spent its time — phase timings, JSONL records and dev-loop comparisons.
---

# Where `forge env up` spent its time

Every `up`, green or red, ends with a timings table on stderr (just above
the summary box) and appends one JSON line to
`.forge/logs/<env>/up-timings.jsonl`. Measure a dev-loop change with it
before claiming a speedup. Each row is a phase: its start offset from the
run's start (`@`) and its duration, nested phases indented under their
parent. Offsets, not a sum, because phases overlap: `infra` runs beside
`build`.

| Phase | What it times |
|---|---|
| `render`, `secrets` | the env's KCL render; secret pull, generation and validation |
| `clusters/<name>`, `kubeconfigs` | each declared cluster's ensure, one after another; the cross-cluster kubeconfig mint |
| `preflight/…` | `go-mod-tidy`, `air`, `host-runners`, `frontend-deps` |
| `infra` | the infra pre-warm (host infra, compose), concurrent with `build` |
| `build/…` | `generate`, `render`, then stages `go` (Go binaries, frontends, the project image), `images` (DockerBuild), `variants`, `shell` (ShellBuild); one child per image or binary |
| `deploy` | the apply and its rollout waits |
| `replace-stack` | stopping this project's previous host processes for the env |
| `host/…` | `infra` (when the pre-warm did not converge), `database`, `identity`, `jobs`, `launch` |
| `frontends` | dependency install and dev-server start |
| `readiness/<workload>` | time-to-ready: from that workload's launch to its port bound by this run (so it can start before `readiness` does); `skipped` when this run never launched it |
| `seed`, `cluster-health` | the first-boot seed; the one look at cluster workloads |

A status column appears only when a row is not `ok`: `failed`, `skipped`, or
`incomplete` (still running when the run ended; on a failed run, usually
the phase that failed). `FORGE_UP_TIMINGS=0` hides the table; the record is
always written.

The record (`"schema": "forge.env.up.timings/v1"`; fields are only ever
added) is one object per run: `run_id`, `started_at`, `env`,
`forge_version`, `targets`, `flags` (the `--no-*`, `--background` and
`--watch` booleans, and `options`: the `-D` names, never their values),
`status` (`ok`/`failed`), `error`, `total_ms` (to ready: the summary box,
storage maintenance and the foreground hold are not counted), and `phases[]`
of `{name, parent, start_ms, duration_ms, status, error}`, in start order.
`name` is the full path (`build/go/api (docker)`), `parent` its enclosing
phase.

```bash
# the slowest phases of the last run
tail -1 .forge/logs/dev/up-timings.jsonl | jq -r '.phases | sort_by(-.duration_ms)[:5][] | "\(.duration_ms)ms \(.name)"'
# total over the last ten runs
tail -10 .forge/logs/dev/up-timings.jsonl | jq -r '"\(.started_at) \(.status) \(.total_ms)ms"'
```
