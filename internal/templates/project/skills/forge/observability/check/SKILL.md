---
name: observability/check
description: `forge obs check` after a deploy: the subject and windows, the env's `forge.ReleaseHealth` policy (thresholds, `fail_on`, `warn_on`), the exit codes a CI gate branches on (0 PASS, 1 FAIL, 2 INCONCLUSIVE, 8 check again later), the INCONCLUSIVE `reason_code`s (`low_traffic`, `thin_baseline`, `no_request_telemetry`, ...), `--wait`, `--early-pass` and `--gate-json`, and the warn-only site-audit findings of a static site (`seo_regressed`, `a11y_regressed`).
---

# Release health from the CLI: `forge obs check`

The subject is a promotion: the env's current one, or `--release v` / `--promotion id`. Two equal windows are compared, before and after it. Each window stops at the neighbouring promotion and at now. Error rate is weighted for tail sampling, so it is not inflated by dropped OK spans. A request the client walked away from is not a server error: a span with `error_class=canceled`, Connect code `canceled` or HTTP 499 counts as a request and never as an error, here or in any error count `obs` reports. Latency and error rate are judged per endpoint where the backend supplies endpoint figures, and `error_rate` needs `error_rate_min_errors` errors in the after window. A finding the after window cannot pin on the release (a retried promotion's window from before it went live, a rollout under 50%, an issue whose events are another version's) never fails it. How latency, error rate and attribution are judged: `observability/release-health`. A local env has no promotions: `--since 20m` (or the start of the last `forge env up`) names the instant to judge from, and regressed issues, alerts and restarts are listed under `not_evaluated`.

The env's KCL declares the thresholds and which findings fail (`forge.ReleaseHealth` on the Bundle; the defaults are below). They are recorded in the release shape, so the console, the CLI and alerting judge alike. `--threshold k=v` overrides one for one call, `--fail-on` the failing set, `--window` the width, and the document says where each threshold came from.

```
release_health = forge.ReleaseHealth {
    window = "15m"                      # 1m to 6h
    min_requests = 50                   # below this, in either window, a service is not compared
    max_error_rate_increase = 0.01      # a fraction: one percentage point
    error_rate_min_errors = 10          # error_rate needs this many after-window errors; 1 is the old rule
    baseline_window = "24h"             # an endpoint with a thin before window is compared against this much history
    max_p95_ratio = 1.5
    new_issue_min_events = 3
    regressed_issue_min_events = 3      # unset: follows new_issue_min_events; 1 counts every regression
    alert_baseline_min_firings = 1
    seo_max_score_drop = 10             # site audits: points a static site's SEO score may drop; 0 fires on any drop
    a11y_max_score_drop = 10            # the same for accessibility
    fail_on = ["new_issue", "regressed_issue", "error_rate", "alert_firing", "probe_failed",
               "browser_errors", "browser_failed_requests", "browser_page_load"]   # the default; latency, workload_restarts, seo_regressed, a11y_regressed warn
    warn_on = ["browser_page_load"]     # demote named findings to warnings without restating fail_on
    require_baseline = "any"            # or "every", "none" (observability/release-health)
}
```

| Exit | Verdict |
|---|---|
| 0 | PASS (also INCONCLUSIVE with `--allow-inconclusive`) |
| 1 | FAIL: a `fail_on` finding fired |
| 2 | INCONCLUSIVE (the after window has not finished, or no service reached `min_requests` in both windows), or forge could not look (`reason_code` `permission_denied` or `unauthenticated` when the credential may not read release health: a CI token needs `obs:read`) |
| 8 | check again later: `--wait` timed out (`--timeout`, default 30m) before the after window finished, or the control plane refused the read because a query allowance is spent (`reason_code` `quota_exhausted` or `rate_limited`) |

**A refused read is exit 8, not 2:** nothing was judged. `--wait` waits it out until `--timeout`; the document has no `verdict`, and has `reason_code` and `retry_at` (rerun after it). Details, and how an unreachable control plane is retried: `observability/release-health`, A refused read.

**Low traffic is exit 2, not 8.** 8 means `--wait` ran out with the after window still open (or a refused read, above): check again later. `low_traffic` and `thin_baseline` mean the window CLOSED with too few requests: final for that promotion, so report "not proven good" and do not loop on it (`observability/release-health`, Low traffic).

An INCONCLUSIVE document carries `reason` (a sentence) and `reason_code`, one of `window_not_live`, `rollout_incomplete` (`observability/release-health`), `window_open`, `low_traffic`, `thin_baseline`, `no_request_telemetry`, `standing_critical_alert` or `alert_state_unread`. Branch on the code, not the sentence.

**Signals: spans, plus the browser** (`observability/release-health`): a service with no server spans is not judged on its requests (the platform's ingress figures are not passed to the judgement yet); a frontend is judged on its browser telemetry (`browser_*` findings, fail by default; `warn_on` demotes one). Each service says which (`signal`).

**`no_request_telemetry`: no request data at all.** No server span or browser telemetry in either window: a service that exports no traces has no request data however much traffic it serves. forge instruments what it builds; an app built from its own Dockerfile or command, or a third-party image, exports traces only if it is instrumented, and the reason names those services (`uninstrumented_services`). Add an OpenTelemetry SDK that exports OTLP to `$OTEL_EXPORTER_OTLP_ENDPOINT` (hosting sets it and `OTEL_SERVICE_NAME` on every workload), then check again. A static site has no service to instrument: it is judged on its pages' browser telemetry, so load them in a real browser (curl and uptime probes send none), then check again. Until then the verdict stays INCONCLUSIVE — not proven good — and what forge sees without telemetry still counts: a restart, a firing alert or a new issue is a finding, and `forge obs workloads <env>` and `forge obs logs <env>` show the app's health and output.

**Site audits: `seo_regressed`, `a11y_regressed`** (warn by default). A hosted static frontend is audited for search and accessibility after each promotion (the console's Site insights), and the control plane compares the audit with the same env's audit of the previous release, so a site that was always imperfect is never blamed for it. `seo_regressed`: the site's or one route's SEO score dropped by more than `seo_max_score_drop` points, or an error-severity SEO finding appeared that the previous audit did not have (the env became noindex, `robots.txt` disallows `/`, a canonical names another host, an internal link answers 404). `a11y_regressed`: the same for the accessibility score (`a11y_max_score_drop`) and a new critical axe violation. Each frontend has at most one finding per code for its score (`value` is the largest drop in points, `threshold` the limit) and one for its new errors (`value` is their count); the message names the routes and the findings. Neither is in the default `fail_on`, and a warning never changes the exit code: list one in `fail_on` (or `--fail-on seo_regressed`) to fail releases on it. A release with no previous audit (the env's first) has neither; one whose audit has not finished, or failed, lists `site_audit` in `not_evaluated`, which never makes the verdict INCONCLUSIVE on its own. A route the previous audit did not crawl has no score baseline, but its new errors count (a page the release added with an error is the release's); two audits by different audit rules are not compared. The audit is of the release's own bytes, not a traffic window, so attribution (`observability/release-health`) never moves these findings. Only the control plane judges them: a local env has no site audit.

`--gate-json FILE` writes the evidence `forge gate record --from` attaches to the promotion.

`--wait --early-pass 8m` ends the wait with a PASS before the after window finishes. It passes only once at least 8m of the window has been observed and all of these hold: every judged service has a measured version share of ≥99% for the judged version (the rollout is over) and has reached `min_requests` on its own (a quiet or unmeasured service waits for the window), and there is no finding of any policy, no new or regressed issue, no firing alert, no restart and no critical alert. Anything found, or a rollout still in progress, keeps the wait going to the end of the window. The document's `early_pass` says why it passed. The control plane's own stored verdict still comes after the full window.

**The control plane records its own verdict** of every promotion, and that stored verdict, not the CLI's exit code, is what the promotion health gate reads (`observability/release-health`, The stored verdict).

The other `forge obs` verbs, backends and the JSON contract: `observability/query`. How each finding is judged in depth: `observability/release-health`.
