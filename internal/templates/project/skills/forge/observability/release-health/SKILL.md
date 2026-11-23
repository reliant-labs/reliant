---
name: observability/release-health
description: How `forge obs check` judges a release in depth — when the after window starts (the live time a promotion's apply and cluster-rollout gates record, which a retried promotion reaches after its first promoted_at), per-endpoint latency, per-endpoint error rate with its error_rate_min_errors floor and baseline_window, attribution — version_share, window_not_live, rollout_incomplete and findings[].unattributed, which keep another version's findings from failing a release — the issue thresholds (new_issue_min_events, regressed_issue_min_events) with issue_alerts, which keep the NewIssue/IssueRegressed alerts from failing a release twice, alert_baseline_min_firings with baseline_alerts, which keep an alert that was already flapping before the promotion from failing it, pre_existing_alerts, which keep an alert on an issue older than the release and firing no faster under it from failing it, the per-op changed_op_errors / changed_op_latency findings on the ops the release's commits changed, and the control plane's stored verdict the promotion health gate reads.
---

# Release health: what a release answers for

`forge obs check` (`observability/check`) compares the window after a
promotion with the window before it. This is the detail behind two of its
rules: how latency and error rate are compared, and which findings are the release's.

## Signals: spans, plus the browser

Each service's requests, error rate and p95 come from its own OpenTelemetry server spans. A service that sent none in either window is not judged on its requests today: the ingress figures Reliant hosting records (per-workload request counts, 5xx share and time to headers) are not yet passed to the judgement, so a static site or an app without OpenTelemetry gets `no_request_telemetry` unless its pages send browser telemetry. Load the pages in a real browser (curl and uptime probes send none) for a static site to be judged. A frontend that sends forge's browser telemetry is judged on it too, beside either or alone: `browser_errors` (JavaScript errors per page view), `browser_failed_requests` (its fetch/XHR requests that got a 5xx or no answer) and `browser_page_load` (p95 page load), against `max_error_rate_increase` and `max_p95_ratio`, and judged once it has `min_requests` page views in both windows (a thinner before window is no baseline; `require_baseline`, `observability/check`). Page views can prove a release good only when no service saw a request in the after window (a frontend alone): beside a backend or site with requests too few to judge, the verdict stays INCONCLUSIVE (`low_traffic`). They fail a release by default, as `error_rate` does; `warn_on = ["browser_page_load"]` in `release_health` demotes one to a warning without restating `fail_on` (an explicit `fail_on` that leaves them out also keeps them warnings). Each service in the document says which it was judged on (`signal`: `spans` or `browser`; `ingress` is reserved for when ingress figures are judged; its browser figures are under `browser`).

## Latency, per endpoint

Where the backend supplies endpoint figures, `max_p95_ratio` applies to the
after-window request-weighted mean of each endpoint's after/before p95, over
the endpoints called in the after window. One with no before p95 (new since
the promotion) is judged against the service-level before p95, never skipped.
So a window filled with one client's fast calls (a deploy polling its own
release) and then emptied of them does not read as the service getting slower.
When a window holds more endpoints than the query returns (1000), the endpoint
set is incomplete and latency is judged on the service-level p95 instead.

## Error rate, per endpoint, with a floor: `error_rate_min_errors`, `baseline_window`

`error_rate` fires when the error rate rose by more than
`max_error_rate_increase` AND the service had at least `error_rate_min_errors`
(default 10) errors in the after window. A rise of a few errors on a quiet
service (one client's broken sessions) is not evidence a release broke
something, as one event is not an issue. `--threshold error_rate_min_errors=1`
is the rule before the floor existed.

Where the backend supplies endpoint figures, the rise is the after-window
request-weighted mean of each endpoint's own rise, so a change in WHICH
endpoints a window holds does not read as a change in how often they fail. An
endpoint is compared against its before window when that held `min_requests`
requests; else against its own figures over `baseline_window` (default `24h`,
from the window to `168h`) before the promotion, when those did; else against
the service's before rate, so an endpoint with no history is never read as
error-free. A busy endpoint that got worse dominates the mean, and a rise
across many endpoints adds up as it did service-wide, so both still fail.

The case it was built for (control-plane v1.20.1-rc.1): reliant-api-server went
0.0% -> 3.3%, 8 errors in 246 requests, every one a `terminal.session`
websocket from one daemon whose machine did not answer. That endpoint had
failed 9% of the time all day; the 15-minute before window held none of it, so
the service read 0%. The floor passes it, and judged per endpoint the rise is
measured against the endpoint's own 9%.

The local backend reads endpoint figures for both windows and the baseline
window. A control plane that does not supply them is judged service-wide, and
`obs check` holds its answer to the floor even when the control plane
predates it, listing `error_rate_min_errors` in `thresholds`.

## Changed ops: `changed_op_errors`, `changed_op_latency`

A service-wide rate hides a regression in one op on a busy service. So the
check also judges each op (`observability/query/ops`) the release's commits
changed, before vs after, on that op's own spans. Which ops changed:

- on hosting: the commits the release recorded, mapped in this checkout;
- locally: the commits since `--since`, plus uncommitted changes.

| Finding | Fires when |
|---|---|
| `changed_op_errors` | the op's error rate rose by more than `changed_op_max_error_rate_increase` (default 0.05) with at least `changed_op_min_errors` (5) errors after |
| `changed_op_latency` | the op's p95 after is more than `changed_op_max_p95_ratio` (2.0) times its p95 before |

An op is compared only with `changed_op_min_calls` (50) calls in both
windows; the thinner ones are named in one `not_evaluated` entry,
`changed_op: N op(s) below changed_op_min_calls …`.
On a hosted env an op's after-window figures count only the judged release's
`service.version` (`changed_ops[].after_version`) and its before-window
figures only one baseline version's (`changed_ops[].before_version`: the
previous release, else the version that served most of the op's before window,
never the judged release itself), so an old version still in rotation on
either side never makes it a finding, and an op without per-version figures
for either window is listed under `not_evaluated` instead of judged.
`--threshold k=v` overrides each, and `thresholds` lists each with its source.
Both warn by default; add them to `fail_on` (KCL `release_health`, or
`--fail-on`) to gate. Each finding carries `op`, `commit` and `pr` (from a
trailing `(#123)` in the commit subject), and the document's `changed_ops[]`
lists every changed op with both windows and its commits.

When the mapping cannot run (no checkout, the release's commits not in it, a
backend that cannot rank ops), `not_evaluated` says why and the check goes
on: changed ops never fail a check by being unreadable.

## The stored verdict

For an env on a control plane, its release-health evaluator judges every promotion once the after window closes and settles (~2m), with the same rule and the env's declared policy (no `--threshold`/`--fail-on` overrides), and stores it once per promotion, append-only: `pass`, `fail` or `inconclusive` — the verdicts `obs check` reports as 0, 1 and 2. A FAIL raises `ReleaseRegressed`. That stored verdict, not the CLI's exit code, is what the **promotion health gate** reads: an env declaring `delivery_policy.health_gate.after = ["staging"]` refuses a release until staging's verdict for it is `pass` (`deploy/delivery-policy`). So in a staging → prod pipeline, `forge obs check staging --release <v> --wait` is how to wait for the evidence prod's promotion needs; its exit code tells you what the gate will see, and loosening it with `--threshold` or `--allow-inconclusive` changes nothing on the server.

## Attribution: a finding fails the release only when the window measured it

A promotion is intent. Its release is live when an attempt shipped it, and a
promotion whose first attempt failed ships on a later attempt, under the same
promotion id. So the check asks three questions before a finding may fail the
release.

**Was the window the live release's?** The check reads the promotion's `apply`
and `cluster-rollout` gates. `subject.live_at` is the end of the first attempt
(the gates sharing a run id) whose gates all passed, or were skipped by a
deploy that did not wait. A re-apply after that does not move it. If the after
window ended before `live_at`, or no attempt has shipped yet, the window
measured the release before it. `window_not_live` then says so, every finding
is unattributed, and the verdict is INCONCLUSIVE (exit 2). `--wait` keeps
waiting while no attempt has shipped, and exits 8 if none ships before
`--timeout`. A promotion that records no deploy gate keeps its promoted_at.

**Did the release serve the window?** `version_share` is the judged release's
share of the after window's measured traffic, weighted by requests, over the
services whose share was measured. Under 50%, `rollout_incomplete` says the
rollout had not happened, or had not finished, in that window, so the window
cannot prove the release good: a verdict that would be PASS is INCONCLUSIVE.
A low share takes no finding off the release by itself. A failing finding
still fails it: an alert or a restart carries no version, and a release on a
tenth of the traffic can still take the env down, so a critical alert opened
in the window fails it whatever the share.

**Is the finding the release's?** Only version-specific evidence says no. A
service's error rate or latency is not the release's when the release served
under half of that service's own traffic; the finding is unattributed and the
release is unproven. Each issue
finding is checked against the issue's after-window events by version
(`window_versions` on each of `new_issues` and `regressed_issues`). An issue with an event of the release
is the release's. An issue whose events are all another version's belongs to
that version, and it alone does not keep the release from passing.

A finding that is not the release's stays in `findings` with policy `warn` and
a `findings[].unattributed` reason. It never returns exit 1, and `--fail-on`
cannot make it fail. A read that fails (the gates, an issue's events) leaves
the finding standing and says so on stderr: nothing disproved it.

```bash
forge obs check prod --wait --json | jq '{verdict, live: .subject.live_at, version_share, window_not_live, rollout_incomplete, unattributed: [.findings[] | select(.unattributed) | {code, unattributed}]}'
```

## Issues and issue alerts: `new_issue_min_events`, `regressed_issue_min_events`

An issue fails a release only with enough after-window events: `new_issue` needs `new_issue_min_events` (default 3) and an issue first seen in the release, `regressed_issue` needs `regressed_issue_min_events` (unset, the same as `new_issue_min_events`) and a regression at or after the promotion. One event of an old, transient signature is not a release regressing. Per call: `--threshold regressed_issue_min_events=1` counts every regression, the rule before the threshold existed.

The platform's `NewIssue` and `IssueRegressed` alerts fire on those same issues with no threshold and no version, so they are judged as their issue: each is listed in `issue_alerts`, not counted again as an `alert_firing` finding. Three exceptions keep a real failure failing: an issue alert whose rule the env made critical (`severity=critical`) fails the release like any critical alert; when the backend could not read issues (`not_evaluated` names `new_issue` or `regressed_issue`) the alert is the only evidence and counts; and every other rule (`IssueSpike`, your own) counts as before, unless its issue predates the release (below). The check holds a control plane's answer to both rules even when that control plane predates them.

## Alerts that were already flapping: `alert_baseline_min_firings`

An alert that opened after the promotion is evidence against the release only if the release could have caused it. One that also fired at least `alert_baseline_min_firings` times (default 1) in the `baseline_window` before the promotion (default `24h`) was flapping before the release: it is listed in `baseline_alerts` (each with `baseline_firings`), and its `alert_firing` finding stays in `findings` with policy `warn` and a `findings[].unattributed` reason, so it is visible and never returns exit 1. The case it was built for (control-plane v1.21.0-rc.6): `IdentityLatency`, a warning that fired 2-27 times a day and twice that day before any rc.6 pod existed, failed prod on a sign-in path the release did not touch.

What still fails: an alert whose rule never fired in the baseline window; one that fired only for something else (another `service`, `uri`, `pod`); one that only went pending; and every critical alert (`severity=critical`), however noisy, because an env that made a rule critical means it to page.

"The same alert" is the rule plus its labels, not the fingerprint. A rule's query returns its measurements as columns, and every column but the value becomes a label, so one condition fingerprints as a new instance each time a sample count moves (`slow=4`, `slow=6`). A label whose value is a number is therefore read as a measurement and ignored, and the rest must be exactly the same set with the same values: a baseline firing with an extra or a missing non-numeric label is another alert. So an alert whose only labels are measurements matches only baseline firings that have no other label either, never another service's. The trade-off: a rule grouped by a numeric label (`code="500"`) is one identity across its values. If that matters for a rule, make it critical, or turn the rule off for the env.

The evidence is the env's alert history (the control plane's `ListAlertHistory`, one call of up to 1000 transitions since the baseline window's start). A firing transition in the window is one firing, and an instance already firing when the window opened is one more. A history that cannot be read discounts nothing, and the check says so on stderr: the rule only ever gets as strict as it was, never looser. A local stack keeps no alert history.

Per env: `alert_baseline_min_firings = 3` in `release_health` asks for a flap rate rather than one prior firing, and `0` counts every alert opened after the promotion, the rule before this one. Per call: `--threshold alert_baseline_min_firings=0`. The check holds a control plane's answer to the rule even when that control plane predates it.

**The stored verdict does not apply this rule yet.** The control plane's evaluator applies it only once its judge reads the env's alert history and passes it to forge's rule; until a control-plane release does, the stored verdict, `ReleaseRegressed` and the promotion health gate still count every alert opened after the promotion, flapping or not. So for a baseline alert, `obs check` exiting 0 is not what the gate will see: `--threshold alert_baseline_min_firings=0` reproduces the stored verdict.

```bash
forge obs check prod --json | jq '{verdict, baseline_alerts: [.baseline_alerts[]? | {rule, baseline_firings, labels}], discounted: [.findings[] | select(.code == "alert_firing" and .unattributed) | .unattributed]}'
```

## Alerts on an issue older than the release: `pre_existing_alerts`

An alert that names an issue (label `issue_id`) and is not `NewIssue` or `IssueRegressed` — `IssueSpike`, or a rule of your own over an issue — is about that issue's condition, which can be older than the alert instance. The case it was built for (control-plane v1.23.0-rc.9): `IssueSpike` for an issue first seen 3.5h before the promotion in the previous build, firing ~27 times an hour before and after it. The spike detector compares the last one to two hours with the day before, so that instance first fired after the promotion and had never fired before: there was nothing for `alert_baseline_min_firings` to discount.

So the check reads the issue (`GetIssue`) twice, its events by version in each window on its own: the after window, from the promotion to its end, and the `baseline_window` before the promotion in whole hours (the control plane counts an issue's events by the hour, so the hour holding the promotion, which the rollout shares, is left out). The alert is pre-existing when the issue was first seen before the promotion in another version (`first_build`), and in the after window the release's own version fired it at least once (no event of it there is no evidence, and the alert counts), and either fewer than `new_issue_min_events` times, or at most twice as fast (events per hour between its first and last event there) as the other versions did in the baseline window, or by fewer than `new_issue_min_events` events above that rate. A window whose counted events reach outside it (an hour the after window's end cuts, read after it closed) cannot tell the release's events from later ones, so the alert counts. Each alert is matched to its own finding (`findings[].alert`); a finding that names no alert (a control plane that predates the field) is matched only when it is its rule's one counted finding and the answer lists exactly one alert of that rule, and otherwise counts. A pre-existing alert is listed in `pre_existing_alerts` (each with `issue`: its first seen time and version, and both rates), and its `alert_firing` finding stays in `findings` with policy `warn`, `pre_existing: true` and a `findings[].unattributed` reason starting `pre-existing alert (first seen <build>, rate unchanged)`, so it never returns exit 1. A baseline alert's finding carries `pre_existing: true` too.

What still fails: an alert whose issue was first seen after the promotion or in the release; an issue the issue rule already judged (a new or regressed issue); one that fires faster under the release beyond those bars; a dormant issue (no event of another version in the baseline window) the release fired `new_issue_min_events` times; an issue the check could not read (it says so on stderr); evidence a window cannot separate; a finding no exact alert matches; and every critical alert.

```bash
forge obs check prod --json | jq '{verdict, pre_existing_alerts: [.pre_existing_alerts[]? | {rule, issue}]}'
```

Like the baseline rule, the stored verdict applies this only once the control plane's judge passes the issues to forge's rule (`Input.AlertIssues`).

## The baseline: `require_baseline`

Error rate, latency and the browser findings compare the after window with the before window, so a service is compared only with `min_requests` in BOTH. One with enough after the promotion but fewer before it is listed in `not_evaluated` as `service:<name>` (`browser:<name>` for its page views). With `require_baseline = "any"` (the default) another compared service can still pass the release, and with none compared it is INCONCLUSIVE, `reason_code` `thin_baseline`, naming each service's before-window count. `"every"` holds the release INCONCLUSIVE on any thin baseline; `"none"` compares against whatever the before window held. New and regressed issues, firing alerts and restarts need no baseline and fail a release under every setting (an alert that already fired in the `baseline_window` is the exception above). Per call: `--threshold require_baseline=every`. A low-traffic env (a landing page under `min_requests` per window) is INCONCLUSIVE until it has traffic in both windows; lower `min_requests`, set `require_baseline = "none"`, or gate with `--allow-inconclusive` if that is acceptable for it.

## Low traffic: exit 2, final for the promotion

`reason_code` `low_traffic` ("no service had at least N requests after the promotion") or `thin_baseline` is INCONCLUSIVE, exit 2: not 8. Exit 8 says only that checking again later can change the answer. Either `--wait` ran out while the after window was still open, or the control plane refused the read because the org's query quota was spent or the caller was throttled (`reason_code` `quota_exhausted` / `rate_limited`, no verdict; A refused read, below). A low-traffic verdict is reached on a CLOSED window, and a later check of the same promotion reads the same window and says the same thing. Report it as "not proven good", never "passed", and do not poll it. To make a quiet env (a new service, a landing page) judgeable for its next promotion, lower `min_requests` in its `release_health` (or `--threshold min_requests=N` for one call), widen `--window` (max 6h), or set `require_baseline = "none"`; `--allow-inconclusive` turns exit 2 into 0, a decision to accept "not proven" that the caller makes deliberately. `window_open` (no `--wait`, window still open) is the exit 2 that checking again does change.

## A refused read: exit 8, check again later

The control plane can refuse `obs check`'s read. One cause is a spent org query quota: ResourceExhausted, reason `obs_query_quota`, with a Retry-After that runs until the quota refills at the top of the hour. The quota is shared with every other CLI and token read in the org, so a busy hour of agents can spend it. The other cause is a throttle on the caller (`in_flight_limit`, `rate_limited`, or a 429 with Retry-After). Nothing was judged, so a refusal is not "not proven good":

- With `--wait`, obs check waits the refusal out. It sleeps for the Retry-After (at most 5m at a time, jittered) and asks again, until `--timeout`.
- It exits **8** when the allowance would refill only after `--timeout`. Without `--wait` it exits 8 at once.
- The document has no `verdict`. It has `reason_code` `quota_exhausted` or `rate_limited`, `retry_at` (when the allowance refills, when the control plane said) and `suggested[]` (the check to run again after `retry_at`). With `--gate-json`, the gate is `error`, never a pass.
- Under `--wait`, an Unavailable or DeadlineExceeded control plane, a 502/503/504 or a dropped connection is also asked again, with a backoff (15s, doubling, at most 2m). A control plane that stays down for all of `--timeout` exits **2**, as it does without `--wait`: forge could not look.
- Every other failure keeps its exit code: auth, an unknown procedure, a request too large, a ResourceExhausted that gives neither a reason nor a retry time.
