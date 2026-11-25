---
name: frontend-runtime/telemetry/replay
description: Session replay in forge frontends — the per-env `_session_replay` / `telemetry = forge.BrowserTelemetry {replay = ...}` switch and its hosted ingestion opt-in, the `full`-tier and GPC/DNT gates, hosted dev's replay grant, forced masking, sampling, the runtime's own rrweb recorder (lazy chunk, OTLP/JSON gzipped in a Worker, flush on hide/pagehide with keepalive, replayStart "interaction", replayFlushIntervalMs), and upgrading an app that declared @hyperdx/browser.
---

# Session replay: defaults on for new projects, always masked

New app and static-site environments declare `_session_replay = True` in
`deploy/kcl/<env>/main.k`. It sets the frontend's
`telemetry = forge.BrowserTelemetry {replay = _session_replay}`, which renders
its runtime `OTEL_REPLAY` and the env's hosted ingestion opt-in.
Set it to `False` and restart/redeploy that environment to stop capture and
remove its ingestion opt-in. Reload open tabs to pick up the new document.
The choice is **per environment, not baked into a promoted artifact**.

Replay belongs to one frontend in one environment, so the switch for it is
`telemetry = forge.BrowserTelemetry {replay = True}` on that frontend in that
env's KCL. It renders the frontend's `OTEL_REPLAY = "true"` (a hosted site's
StaticSite spec too, so the platform reads it live) and opts the env's
observability policy in, so the platform keeps the events. A hosted frontend
with it on needs the env to declare `observability`. An author's own
`runtime_config.OTEL_REPLAY` still wins over it (so `"false"` there keeps that
frontend off). There is no env-wide switch: `ObservabilitySignals` has no
`replay` field (the render records the result as `signals.replay`), and a
`{NEXT_PUBLIC_,VITE_}OTEL_REPLAY` build variable in `env_vars` is not carried
into the runtime config. Envs scaffolded before v1.4.0 declared both, and
forge v1.9.0 no longer accepts either: delete `replay` from the env's
`ObservabilitySignals` and the `OTEL_REPLAY` build variable, and set
`telemetry = forge.BrowserTelemetry {replay = True}` on the frontend instead.
With observability or browser telemetry off, nothing records.

Replay records a person, so it runs only in the **`full` analytics tier**:
`ANALYTICS_MODE = "full"` in the frontend's runtime config and
`grantAnalyticsConsent()` before `initTelemetry()` runs (consent given later
starts it on the next page load; revoking it stops the recorder). In the
cookieless modes `replay = True` records nothing. An internal tool whose
users have agreed to recording grants consent at startup.

Pin upgrades do not enable replay in existing projects: the SDK and KCL schema
retain their off default. Adding a frontend to an existing project does not
rewrite that environment's owned policy either. To opt in deliberately, set
`telemetry.replay = True` on the frontend;
setting only `OTEL_REPLAY` cannot authorize hosted ingestion. An explicit endpoint `off` or browser telemetry off disables capture. Replay
also respects `navigator.globalPrivacyControl === true`, DNT `1`/`yes` (including
legacy window/MS DNT), before sampling. Check consent before calling
`initTelemetry()` if your product requires it: the scaffold does not invent a
consent policy. On withdrawal call `disableTelemetry()` from the runtime; it
stops the recorder for this page load (already exported data is not recalled,
and ordinary SDK network tracing cannot be undone). A changed deployment
setting requires a page reload.

Dev keeps its normal destination selection. Hosted dev requests a session-bound
replay grant and enables capture only after the server acknowledges it; older
servers leave capture off and print an unavailable notice while ordinary
telemetry continues. Renewal retains the explicit choice; a denied renewal
publishes capture off for future page loads, never silently enables it later,
and asks existing tabs to reload. Tokens authorize ingestion, not the SDK's
capture intent: old grants retain their normal lifetime (currently one hour);
changing the switch is not an immediate revocation of every old token.
A container-free site needs no database or Docker: signed-in
hosted dev provides collection. Offline without a collector, recording is
unavailable and capture stays off. For local collection, supply an OTLP collector
and `observability.local_port`; app dev already declares local ClickStack.

Replay is rrweb, recorded by the runtime: rrweb loads in its own chunk only
when replay runs, batches are encoded as OTLP/JSON and gzipped in a Worker (a
CSP without `worker-src blob:` makes it gzip on the main thread in short
slices instead), sent every 15 s (`replayFlushIntervalMs`) and whenever the
page is hidden or left (with keepalive), and `replayStart: "interaction"`
starts recording only at the person's first tap, click or key. HyperDX's
replay player reads it as before (scope `rum.rr-web`, the same `rr-web.*`
attributes). When enabled, **all text and all inputs are
masked**, canvas/media/fonts are not recorded, and network bodies are off.
There is no unmasked setting. Sessions are sampled at
`frontend.telemetry.replay_sample_rate` (0..1, default 1, so every eligible
session). Sampling does not override a privacy gate. Recording adds browser
work and telemetry volume/storage; review the environment's retention and
access policy before shipping sensitive applications. Masking is defense in
depth, not permission to record without any consent your product requires.

## Upgrading from a runtime that imported `@hyperdx/browser`

web-runtime 0.13.18 and earlier imported `@hyperdx/browser`, and forge declared
it in every web frontend's `package.json`. 0.13.19 through 0.13.x brought
`@hyperdx/otel-web` and `rrweb` as dependencies of the runtime. From 0.14.0 they
are its exact optional peers, so a `/node`-only consumer installs neither
(forge defect ae15c269), and a frontend importing `/telemetry` declares both:
`forge generate` adds them on the run that raises the runtime to `^0.14`. An
app that moves to 0.14 without forge runs
`npm install @hyperdx/otel-web@0.20.0 rrweb@2.1.7` (the versions in the
runtime's `peerDependencies`). The old `@hyperdx/browser` entry is unused and
forge leaves it where it is: remove it (and the
`@hyperdx/otel-web-session-recorder>fflate` override, which guarded only its
recorder) when you next touch the manifest.
