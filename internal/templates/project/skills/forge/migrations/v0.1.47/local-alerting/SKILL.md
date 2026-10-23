---
name: migration-v0.1.47-local-alerting
description: Upgrade a v0.1.46 dev env to local alert delivery — the compose `alertmanager`, the `alerts` evaluator workload and the alertmanager_port / alerts_port declaration, so forge.Alerts fire locally and reach forge.AlertReceivers and .forge/logs/<env>/alerts.log.
relevance: migration
---

# v0.1.47 — local alert delivery: an Alertmanager and an evaluator beside ClickStack

Use when `deploy/kcl/dev/main.k` declares `_observability` but no `alertmanager_port`.

## What changed

- `forge env up` now closes the alerting loop locally (`observability/alerts`, "Alerts fire locally too"):
  - an `alerts` host workload, `forge obs alerts run`, evaluates the env's rules continuously;
  - a compose `alertmanager` delivers what fires;
  - the Alertmanager's config is rendered by `forge/pkg/alerting/amconfig`, the control plane's own renderer.
- The default `reliant-inapp` receiver writes `.forge/logs/<env>/alerts.log`. More receivers are `forge.Alerts.receivers` (`forge.AlertReceiver`).
- `forge.Observability` gained `alertmanager_port` and `alerts_port`, which are declared together.
- A host workload whose `command` starts with `forge` now runs the forge that launched the env, not the one on PATH.
- `forge obs alerts check` and `forge obs profile` now reach the ClickStack in the compose project `forge env up` started (`forge-<id>-<env>`). Before, they named the default project and reported a running ClickStack as not running.

## Steps

1. In `docker-compose.yml`, copy the `alertmanager` service and the `alertmanager_data` volume from a fresh `forge project new`.
2. In `deploy/kcl/dev/main.k`, below `_profiles_otlp_port`:

   ```python
   _alertmanager_port = plugin.resolve_port("<project>-dev-alertmanager", 9093) if _observability and not _destination else 0
   _alerts_port = plugin.resolve_port("<project>-dev-alerts", 9095) if _observability and not _destination else 0
   ```

   Add `alertmanager_port = _alertmanager_port` and `alerts_port = _alerts_port` to `_observability_decl`.
3. In the same file, append two workloads to the no-destination branch of `_telemetry_workloads`: the `alertmanager` `forge.OnCompose` binding and the `alerts` host workload. Copy both from a fresh `forge project new`.

## Verify

`forge env up dev` prints `local alerting: N rule(s) → Alertmanager → reliant-inapp, …`, and its summary lists `alerts` and `alertmanager`. A rule that returns a row shows up in `curl -s localhost:<alertmanager port>/api/v2/alerts`, and in `.forge/logs/dev/alerts.log` once `group_wait` (30s) has passed.
