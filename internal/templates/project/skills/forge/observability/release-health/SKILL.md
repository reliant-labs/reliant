---
name: observability/release-health
description: How `forge obs check` judges a release in depth — when the after window starts (the live time a promotion's apply and cluster-rollout gates record, which a retried promotion reaches after its first promoted_at), per-endpoint latency, and attribution — version_share, window_not_live, rollout_incomplete and findings[].unattributed, which keep another version's findings from failing a release.
---

# Release health: what a release answers for

`forge obs check` (`observability/query`) compares the window after a
promotion with the window before it. This is the detail behind two of its
rules: how latency is compared, and which findings are the release's.

## Latency, per endpoint

Where the backend supplies endpoint figures, `max_p95_ratio` applies to the
after-window request-weighted mean of each endpoint's after/before p95, over
the endpoints called in the after window. One with no before p95 (new since
the promotion) is judged against the service-level before p95, never skipped.
So a window filled with one client's fast calls (a deploy polling its own
release) and then emptied of them does not read as the service getting slower.
When a window holds more endpoints than the query returns (1000), the endpoint
set is incomplete and latency is judged on the service-level p95 instead.

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
