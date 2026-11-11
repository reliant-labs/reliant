---
name: v1.4.0
description: Upgrade a project to forge v1.4.0. Session replay is a per-frontend, per-environment switch, forge.BrowserTelemetry {replay = True}, which renders the frontend's OTEL_REPLAY and opts the env in to replay ingestion. The env-wide Observability.signals.replay is deprecated (it still opts in) and an explicit runtime_config OTEL_REPLAY outranks the new switch. Use when a deploy/kcl env declares ObservabilitySignals {replay = …} or a frontend's runtime_config OTEL_REPLAY, as environments scaffolded with _session_replay before v1.4.0 do.
version: v1.4.0
detection: grep -rqsE 'ObservabilitySignals *\{[^}]*replay *=|OTEL_REPLAY *=' --include='*.k' deploy/kcl && exit 0; exit 1
---

# Upgrading to v1.4.0

## 1. Session replay moves onto the frontend

**Use when** an env's KCL (`deploy/kcl/<env>/main.k`) declares
`observability = forge.Observability {signals = forge.ObservabilitySignals {replay = …}}`
or a frontend's `runtime_config = {OTEL_REPLAY = …}`. Environments scaffolded
with the `_session_replay` switch before v1.4.0 declare both.

### What changed

Replay is a choice about one frontend in one environment, so it is declared
there:

```kcl
_web_frontend = forge.Frontend {
    name = "web"
    # …
    telemetry = forge.BrowserTelemetry {replay = _session_replay}
}
```

`telemetry.replay = True` renders that frontend's `OTEL_REPLAY = "true"` into
its runtime config (a hosted site's StaticSite spec too, so the platform reads
it live) and opts the env's observability policy in to replay, so the platform
keeps the events. Off renders no key: unset is off. A hosted frontend with it on
needs the env to declare `observability`. With observability or browser
telemetry off, nothing records, whatever is declared.

Nothing breaks: `signals.replay` still opts the env in (it is deprecated, and
switches no frontend on) and a `runtime_config` `OTEL_REPLAY` still reaches the
browser. Upgrading changes no env's behavior and rewrites none of your KCL.

### What to do

In each env that uses `_session_replay` (or replay at all):

1. On each web frontend, set `telemetry = forge.BrowserTelemetry {replay = _session_replay}`
   (in the dev env scaffold: `replay = _observability and _session_replay`).
   An existing `telemetry` block takes the `replay` field instead.
2. Delete the frontend's `runtime_config` `OTEL_REPLAY` entry. **It outranks
   `telemetry.replay`**: an explicit `"false"` left behind keeps that frontend
   off with the switch on.
3. Drop `signals = forge.ObservabilitySignals {replay = _session_replay}` from
   `observability` (leave `observability = forge.Observability {}` if it was
   the only field). The frontend's switch now opts the env in.

Then `forge env render <env>` and check the frontend's `runtime_config` (or
`runtime_config_spec` when hosted) carries `OTEL_REPLAY = "true"` exactly when
`_session_replay` is True, and `observability.policy.opt_in` lists `replay`.
