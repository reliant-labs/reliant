---
name: observability/alerts
description: Alert rules declared in KCL per env — `forge.AlertRule` (Prometheus rule shape, the condition inside a native ClickHouse SQL `expr`), `forge.AlertRoute` (labels to a receiver NAME), `forge.AlertDefault` (tune or disable forge's generated defaults, including the customer-metadata and platform ones), the local loop (`forge env up` evaluates rules continuously and delivers them through a local Alertmanager rendered by the control plane's own renderer; `forge.AlertReceiver`; `.forge/logs/<env>/alerts.log`), what render refuses, and what lands in the release shape for the control plane.
---

# Alerts

An env **declares** its alert rules in `deploy/kcl/<env>/main.k`, on `forge.Observability.alerts`. Forge validates them at render, resolves them together with its generated defaults, and records the result in the release's declared shape (`shape.alerts`, `shape.alert_routes`), the same path as `control_plane_secrets`. The control plane reads that record and evaluates the rules, so promoting a release applies its rules and rolling back restores the previous ones. Nothing is created by hand in a UI and no receiver secret is in the repo.

```python
observability = forge.Observability {
    local_port = _otlp_http_port
    alerts = forge.Alerts {
        rules = [
            forge.AlertRule {
                alert = "OrdersBacklog"
                expr = """
SELECT ResourceAttributes['service.name'] AS service, max(Value) AS value
FROM otel_metrics_gauge
WHERE MetricName = 'orders.backlog' AND TimeUnix >= now() - INTERVAL 5 MINUTE
GROUP BY service HAVING value > 500"""
                "for" = "10m"
                labels = {severity = "critical", team = "fulfilment"}
                annotations = {summary = "{{ $labels.service }} backlog is {{ $value }}"}
            }
        ]
        defaults = {"service-silent": forge.AlertDefault {enabled = False}}
        routes = [
            forge.AlertRoute {match = {severity = "critical"}, receiver = "pagerduty-oncall", continue = True}
            forge.AlertRoute {match = {}, receiver = "slack"}
        ]
    }
}
```

## A rule is a SELECT

Rules follow the Prometheus shape, with the condition **inside** `expr`: `alert`, `expr`, `for`, `interval`, `keep_firing_for`, `labels`, `annotations`. There is no separate query/condition object.

- `expr` is **one ClickHouse SELECT** (or `WITH … SELECT`) over the OpenTelemetry tables: `otel_metrics_sum`, `otel_metrics_gauge`, `otel_metrics_histogram`, `otel_traces`, `otel_logs`. Your project's database is already selected.
- **Every row it returns is a firing instance.** Columns other than `value` become the instance's labels; `value` (numeric) is the sample. Put the threshold in the query: `… GROUP BY service HAVING value > 0.05`. A query that returns no rows is healthy.
- `for` holds the condition before it fires (default `5m`). It is a KCL keyword, so write it quoted: `"for" = "10m"`.
- `interval` is the evaluation period, floor `1m`. `keep_firing_for` holds a cleared alert.
- `severity` is a label: `critical | warning | info`, default `warning`. Add any other labels you route on.
- `release_health = "exclude"` (a label) says the rule watches something other than the env's own workloads: the machines that build it, a fleet the env monitors. Its alerts still page, but they never judge a release: `forge obs check` and the release-health verdict list them as `excluded_alerts` and count them as no `alert_firing` finding and no standing critical alert. Leave it off any rule about what the env serves.
- `annotations` are Go templates in the Prometheus convention: `{{ $labels.<col> }}` (or `.Labels.<col>`) and `{{ $value }}` (or `.Value`, six significant digits). Functions: `printf` (`{{ printf "%.0f" $value }}`; a numeric verb also formats a label holding a number), `humanize`, `humanize1024`, `humanizeDuration`, `humanizePercentage`, `toUpper`, `toLower`, the float arithmetic `addf` / `subf` / `mulf` / `divf` (`{{ printf "%.0f" (mulf $value 100.0) }}%`), and text/template's builtins (`if`, `with`, `eq`, `gt`, …). `range`, `define` / `block` / `template`, a printf width or precision over 64, `*` and `%[n]` are refused. A template that fails at evaluation is sent as its raw text and logged; it never stops the alert.

## What render refuses

Each is an error naming the rule, at `forge env render` / `shape` / `deploy`, not at the first evaluation:

| Refused | Why |
|---|---|
| missing `alert` or `expr`; a name that is not `[A-Za-z_][A-Za-z0-9_]*` | required fields |
| `for` / `interval` / `keep_firing_for` not a duration (`90s`, `5m`, `1h30m`; no `d` or `w`) | the evaluator parses them with Go |
| `interval` under `1m` | evaluation floor |
| `expr` not starting with SELECT/WITH, or containing `;` | one read-only statement |
| labels `alertname`, `reliant_*` | set by the platform, so a rule cannot claim another project's identity |
| `severity` other than critical / warning / info | |
| an annotation template that does not parse, calls an unknown function, or uses a refused construct | it would only ever be sent as raw text |
| two rules with one `alert` in an env; a rule named like a default | unique per env |
| a route `receiver` that is a URL, a token, or contains `hooks.slack.com` | a route names a receiver; its secret is entered in the control plane console |
| routes with no empty-`match` route | every alert must have somewhere to go |
| `defaults` naming an unknown default or param | a typo would silently keep the old threshold |
| `alerts` on an env with `enabled = False` | nothing to evaluate |

The expr check is syntactic. The evaluator re-checks it and runs it read-only with a timeout and row cap; the database grants are the real boundary.

## Routing

`forge.AlertRoute {match, receiver, continue}`: an alert goes to the first route whose `match` labels it carries; `continue = True` keeps matching so one alert can reach several receivers. A **receiver is a name**, resolved by the control plane to a Slack channel, PagerDuty service and so on. With **no routes declared** the shape records `reliant-inapp` then `slack`: in-app plus Slack, **never a pager**. Declaring routes replaces that, and you own paging.

## Alerts fire locally too

`forge env up dev` closes the loop on your machine, with the same routing as production:

- **`alerts`** (a host workload in `deploy/kcl/dev/main.k`, running `forge obs alerts run`) evaluates the env's rules continuously against the local ClickStack, `for` applied, and posts what fires to the local Alertmanager. It labels alerts `reliant_org_id="local"`, `reliant_env_id=<env>`.
- **`alertmanager`** (docker-compose.yml) routes them. `forge env up` renders its configuration into `.forge/observability/alertmanager/` with **`forge/pkg/alerting/amconfig`**, the renderer the control plane uses for every org. So a route that reaches `hook` here reaches `hook` there.
- **`reliant-inapp`** is always present, and locally it is the evaluator's own sink. Every alert it receives becomes one INFO record in **`.forge/logs/dev/alerts.log`** (JSON, like every forge process's log, so the collector stores it at its real level), whose message reads:

  ```
  ALERT firing   OrdersBacklog severity=critical service=orders — orders backlog is 812
  ```

  `forge obs logs dev --service alerts` lists them; `grep 'ALERT firing' .forge/logs/dev/alerts.log` finds them on disk. They are not issues: the fault an alert reports is logged by the service that has it.

Other receivers are declared on the env as `forge.AlertReceiver` entries. Only local stacks use them: a hosted env's receivers live in the control-plane console, and this list is ignored there.

```python
alerts = forge.Alerts {
    receivers = [
        forge.AlertReceiver {name = "hook", type = "webhook", url = "http://localhost:9999/alerts"}
        forge.AlertReceiver {name = "slack", type = "slack", secret = "SLACK_WEBHOOK_URL"}
        forge.AlertReceiver {name = "autopilot", type = "autopilot", url = "http://localhost:8080/hooks/<id>", secret = "AUTOPILOT_TOKEN"}
    ]
    routes = [
        forge.AlertRoute {match = {severity = "critical"}, receiver = "hook", continue = True}
        forge.AlertRoute {match = {}, receiver = "reliant-inapp"}
    ]
}
```

- **`secret` names a key in the env's secret store**, never a value: `forge secret set --env dev SLACK_WEBHOOK_URL` (the value is read from stdin).
  - forge writes the value as a file that the config references through `*_file`. The value never enters KCL or the config.
  - A key that is missing leaves its receiver out, and `forge env up` names the key and the fix.
- **Localhost URLs are rewritten for the container.** forge rewrites `localhost` / `127.0.0.1` to `host.docker.internal`, because Alertmanager runs in a container. A receiver on your machine must therefore listen on `0.0.0.0`.
- **`autopilot` observes rather than routes.** Each alert whose `severity` is in `severities` (default `critical`, `error`), or whose alertname is in `always_alertnames` (the issue alerts), reaches the automation. The alert then carries on through your routes. No route names an autopilot receiver.
- **A route to a receiver this env lacks is dropped and reported.** The default `slack` route, with no `slack` receiver declared, is the usual example. It is reported as `not routed: …` and does not fail anything.

Check: `curl -s localhost:<alertmanager port>/api/v2/alerts` shows what is active and which receivers it reached. The port is in the `forge env up` summary. To turn local delivery off, drop the `alerts` and `alertmanager` workloads. `forge obs alerts check` (evaluate once, print what returns now) works either way.

## The built-in alerts

`forge generate` writes `deploy/observability/alert-defaults_gen.json` (the project and frontend the defaults are derived from, and the catalog) and each service's entry in `deploy/observability/spec/alert-service-<svc>_gen.json` — the same input as the dashboards, one file per service so parallel branches never conflict. `forge env shape <env>` renders them per env and records them with `source: default`. Every built-in has an id; **one `forge.Alerts.defaults` map tunes or disables all of them**, whichever signal they read.

**An env that runs another forge project's workloads watches that project's services too.** Each non-tool workload built from another forge project's source (a ShellBuild `cwd` or `source`, a DockerBuild build context, a RemoteBuild `source`) adds that project's `alert-service-*` parts. They are read at the revision this project pins: the GitSource ref, or the go.mod require (go.work and replace honoured, as for a `[forge_projects]` import), else the checkout. Each project owns its own services' alerts, and none keeps a copy of another's. A service both declare is watched once, and this env's `defaults` tune it. If the source can't be read, `env shape` warns and omits it.

| id | alert | signal | fires when (default) | disable |
|---|---|---|---|---|
| `error-rate` | `ServiceErrorRateHigh` (critical, 5m) | rpc metrics | server errors / requests > 5% over 5m, per service, at least 20 requests; `user` and `canceled` errors excluded | `enabled = False` |
| `p95-latency` | `ServiceLatencyP95High` (warning, 10m) | rpc metrics | p95 of `rpc.server.duration` > 1s over 5m, per service, at least 20 requests | same |
| `service-silent` | `ServiceSilent` (warning) | rpc metrics | no request for 10m by a service that served requests in at least 18 of the previous 24 hours. A service is judged when it served one of the project's rpcs in that day, and every call it serves counts as a request, whatever package the procedure is in | same |
| `frontend-js-errors` | `FrontendJSErrorSpike` (warning, 10m) | browser spans | JS error spans in the last 10m > 3x the rate of the 60m before, at least 10; fetch/XHR failures and string-only `console.error("text")` (no exception event) are not counted | same |
| `frontend-lcp-poor` | `FrontendLCPPoor` (info, 10m) | browser spans | p75 of `webvitals` LCP over 30m > 4000 ms, at least 100 measurements | same |
| `new-issue` | `NewIssue` (warning) | **platform** | the control plane's issue registry opened an issue in the last hour (an error first seen within the last day, not ignored or resolved). It fires once per issue and resolves when the hour passes | same |
| `issue-regressed` | `IssueRegressed` (warning) | **platform** | in the last hour, a resolved issue got an event from a release shipped after it was resolved | same |
| `issue-spike` | `IssueSpike` (warning, every 5m) | **platform** | an open issue's events per hour over the last one to two hours are at least 5 times its hourly rate over the day before, and at least 20 events (the thresholds are the control plane's, not tunable params) | same |
| `release-regressed` | `ReleaseRegressed` (warning) | **platform** | a promotion's after-window closed and its release-health verdict is FAIL: any finding in the env's `release_health.fail_on` (default: `alert_firing`, `error_rate`, `new_issue`, `regressed_issue`). It fires once per promotion, and stays silent on PASS and on INCONCLUSIVE (too little traffic to judge). Its why-fired payload names the findings, the release and its commit range; `forge obs check <env> --release <v>` reproduces the verdict. The verdict (pass, fail or inconclusive) is stored once per promotion, append-only, and is what an env's promotion health gate reads (`deploy/delivery-policy`): disabling this alert's notifications does not stop the verdict being recorded | same |
| `service-anomaly` | `ServiceAnomaly` (warning, every 5m) | **platform** | a service's request rate, error rate or p95 (from the `rpc.server.*` metrics, never spans) is more than 4 MADs from the median of the same hour of the week over the previous 4 weeks, and above the floors: at least 50 requests in the 15m window, plus a 1 point (and 5 errors) error-rate rise, or a 1.5x (and 50 ms) p95 rise, or a 2x request-rate change. A service with under 7 days of history is never judged. The limits are `obsquery.DefaultAnomalyParams`, not tunable params; disable it with `defaults = {"service-anomaly": forge.AlertDefault {enabled = False}}`. Its why-fired payload has the figures and `forge obs` commands to run next; an Autopilot receiver gets it by default | same |
| `uptime-check-failing` | `UptimeCheckFailing` (warning), `UptimeCheckDown` (critical) | uptime metrics | an uptime check's last probes failed 3 in a row (warning), then 10 in a row (critical); only for an env that has uptime checks (`observability/uptime`), static sites included (see below). Its why-fired payload names the last status or how the probe failed, its latency, the certificate's expiry and when the failure began | same |
| `certificate-expiring` | `CertificateExpiring` (warning), `CertificateExpiringSoon` (critical) | uptime metrics | the certificate an https check last saw expires within 14 days (warning) or 3 days (critical, an expired one included) | same |
| `crash-loop` | `WorkloadCrashLooping` (critical) | customer metadata | a container's `k8s.container.restarts` grew by at least 3 in 10m, **or** a `CrashLoopBackOff` / "restarting failed container" `BackOff` event in 10m | same, or `signals.metadata = False` |
| `oom-killed` | `WorkloadOOMKilled` (critical) | customer metadata | a container restarted in the last 10m and `k8s.container.last_terminated_reason` is `OOMKilled`, **or** an `OOMKilling` event | same |
| `memory-near-limit` | `WorkloadMemoryNearLimit` (warning, 10m) | customer metadata | `k8s.container.memory_limit_utilization` averaged over 5m > 0.9, per container | same |
| `image-pull-failed` | `WorkloadImagePullFailed` (warning, 5m) | customer metadata (events) | an `ErrImagePull` / `ImagePullBackOff` / failed-pull event in 15m | same |
| `evicted` | `WorkloadEvicted` (warning) | customer metadata (events) | an `Evicted` event in 15m | same |
| `deploy-failed` | `DeployFailed` (critical, 5m) | **platform** | the control plane observed the env's promoted release fail to converge on a cluster, and it stays failed 5m | same |
| `observability-quota` | `ObservabilityQuotaOver100` (warning), `ObservabilityQuotaOver150` (critical) | **platform** | the org's observability usage passed 100% / 150% of its monthly allowance | same |

Tune or disable per env:

```python
defaults = {
    "error-rate": forge.AlertDefault {params = {threshold = 0.02}, labels = {severity = "warning"}}
    "service-silent": forge.AlertDefault {enabled = False}
    "crash-loop": forge.AlertDefault {params = {min_restarts = 5, window_minutes = 30}}
    "deploy-failed": forge.AlertDefault {"for" = "15m", labels = {team = "platform"}}
}
```

`params` per default: `uptime-check-failing` `failures`, `critical_failures`, `window_minutes`; `certificate-expiring` `warn_days`, `critical_days`, `window_minutes`; `error-rate` `threshold`, `window_minutes`, `min_requests`; `p95-latency` `threshold_seconds`, `window_minutes`, `min_requests`; `service-silent` `silent_minutes`, `min_active_hours`, `max_prior_silences`; `frontend-js-errors` `spike_factor`, `window_minutes`, `baseline_minutes`, `min_errors`; `crash-loop` `min_restarts`, `window_minutes`; `oom-killed` `window_minutes`; `memory-near-limit` `threshold`, `window_minutes`; `image-pull-failed` and `evicted` `min_events`, `window_minutes`. The two platform ids take `enabled`, `labels` and `for` and no params. To write a variant, copy a default's SQL from `forge env shape <env> --json` into your own rule (under another name).

### Static sites: the uptime defaults

A static site has no services, so `forge generate` writes no `alert-defaults_gen.json` for it, and it gets none of the service, frontend, metadata or platform defaults. **It does get the two uptime defaults** (`UptimeCheckFailing` / `UptimeCheckDown`, `CertificateExpiring` / `CertificateExpiringSoon`), because they read only the env's own uptime checks. A hosted site gets a check at its root with no declaration, so a hosted env with `observability` declared records these four rules. They use the same SQL, ids and routes (in-app, then Slack) as in any other project.

They cannot fire on missing data. `UptimeCheckFailing` / `UptimeCheckDown` need at least one probe result for the check in the last 15 minutes. `CertificateExpiring*` needs a certificate the prober saw. So an env whose prober is not running yet, or has not reached it, records the rules and they stay quiet.

To opt out per env, disable them like any other default:

```python
observability = forge.Observability {
    alerts = forge.Alerts {defaults = {
        "uptime-check-failing": forge.AlertDefault {enabled = False}
        "certificate-expiring": forge.AlertDefault {enabled = False}
    }}
}
```

Alternatively, `uptime = forge.Uptime {defaults = False}` removes the derived checks themselves, and with no checks there are no uptime alerts (`observability/uptime`).

### Customer-metadata rules and the opt-out

The five metadata rules read what the platform's node agent collects without the app's help (restarts and last exit reason, memory against its limit, Kubernetes events). That data is stamped with the env's `reliant.env.id`, not its name, and the id is the control plane's to know. So their SQL carries the placeholder `{reliant_env_id:String}`, which the evaluator binds to a quoted literal; `forge obs alerts check` binds your env name in its place.

**When the env sets `signals.metadata = False` (or `enabled = False`), none of the five is recorded.** Nothing collects the data, so a rule could only ever return no rows; leaving it out makes that visible in `forge env shape` instead of looking like a healthy workload. Re-enable `metadata` and the next release records them again. A rule that is simply empty because nothing is wrong looks identical to one with no data, so this is the only honest signal.

The two platform ids are different: the control plane raises them from events it owns (a convergence it observed, a quota state it computed), not from a query on your telemetry. Forge records them in the shape as marker rules (`/* reliant:platform … */`, a SELECT that returns no row, so a plain evaluator never fires them) so that your tuning and your `enabled = False` travel with the release. A disabled one is recorded `off`. `forge obs alerts check` lists them as not applicable locally.

Quota and deploy alerts go through the same routes, receivers, silences and alert history as every other alert, and carry `reliant_org_id` / `reliant_env_id`. The org-level quota alert is raised once per env of the org, so a route on `severity` and the env's own labels apply to it like any other.

## Sampling: why RED comes from metrics

The hosted gateway tail-samples traces: **every error trace is kept, healthy traces are sampled**, and it writes no sample-rate attribute. So a count of non-error spans reports a fraction of the traffic, and an errors-over-spans ratio over-states the error rate. The defaults therefore read the **rpc server metrics** (`rpc.server.*`, not sampled) for rate, errors and latency. The one span-based default counts only error spans and compares them with themselves, so sampling cancels. In your own rules, count metrics, or error spans; do not divide by a count of healthy spans.

Counters are cumulative, so a rule that sums `Value` over a window is wrong; difference per series (grouped by `ResourceAttributes, Attributes`), treating a drop as a restart. `error-rate`'s SQL is the worked example.

## What is recorded

`forge env shape <env> --json | jq '.shape.alerts, .shape.alert_routes'`:

```json
{"alerts": [{"alert": "ServiceErrorRateHigh", "expr": "WITH series AS (…", "for": "5m", "interval": "1m",
             "labels": {"severity": "critical"}, "annotations": {"summary": "…"},
             "source": "default", "default_id": "error-rate"}],
 "alert_routes": [{"receiver": "reliant-inapp", "continue": true}, {"receiver": "slack"}]}
```

The rule fields are `pkg/alerting`'s `RuleSpec` JSON, so the same document feeds an evaluator directly. Disabled rules and defaults are not recorded. Routes keep their order. Shapes with no alerts encode as before.
