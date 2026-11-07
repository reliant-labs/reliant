---
name: observability/destination
description: Where an environment's telemetry goes — the typed `forge.Observability` declaration on a Bundle (destination endpoint + headers secret, signals, sampling, retention), what forge renders for it on host/compose, on a cluster (the node-agent DaemonSet) and on forge.OnHosted, and how to send telemetry to your own OTLP backend.
---

# Observability destination

An environment declares **where its telemetry goes**. Forge renders the plumbing for the runtime. Apps keep exporting OTLP to a local endpoint and never learn the backend.

```
app ──OTLP──▶ local endpoint (forge-generated) ──▶ destination (declared per env)
              compose collector │ node agent        local ClickStack │ any OTLP URL │ the platform's
```

It is one typed field on the Bundle, not a mode:

```python
observability = forge.Observability {
    destination = forge.ObservabilityDestination {
        endpoint = "https://otlp.example.com"        # absolute http(s) URL
        protocol = "http/protobuf"                   # or "grpc"
        headers_secret = forge.ObservabilitySecretRef {name = "otlp-auth", key = "headers"}
    }
}
```

`headers_secret` is read when the agent starts. For a credential that gets renewed, declare `token_secret = forge.ObservabilitySecretRef {name = "otlp-token", key = "token"}` instead (the key holds just the token).

## The fields

| Field | Default | Meaning |
|---|---|---|
| `Bundle.observability` | unset | **Unset = telemetry off, cleanly.** No endpoint is injected anywhere and nothing is rendered. |
| `enabled` | `True` | `False` is the same as unset, spelled where it can be flipped |
| `destination.endpoint` | — | OTLP base URL (required inside a destination) |
| `destination.protocol` | `http/protobuf` | `grpc` also supported |
| `destination.headers_secret` | none | `{name, key}`: a reference, **never an inline value**. The value is a JSON object of header → value, e.g. `{"authorization": "<key>"}` |
| `destination.token_secret` | none | `{name, key}`: a **bare bearer token** that is renewed in place. The agent mounts the Secret as a file (whole-directory volume, optional, so it starts before the Secret exists) and `bearertokenauth{filename}` sends `Authorization: Bearer <token>`, re-reading it on change: **no restart on renewal**. Exclusive with `headers_secret`. Cluster only; the local forwarder refuses it |
| `destination.token_from_control_plane` | `False` | the destination is the **control plane's OTLP ingest**, authenticated with the per-environment token the control plane delivers and renews. forge declares a `forge.ControlPlaneSecret` (`forge-otlp-token`, key `token`, in `agent.namespace`) on every cluster running the node agent and points `token_secret` at it, so nothing else is written. Exclusive with `headers_secret` / `token_secret`; needs `control_plane` and a connected cluster per agent cluster. See `deploy/control-plane-secret` |
| `signals.traces / metrics / logs` | `True` | an off signal is `OTEL_<SIGNAL>_EXPORTER=none` at the app and has no collector pipeline |
| `signals.profiles` | unset | unset = **on with the local ClickStack** (no destination, `profiles_port` set), off everywhere else; `True` adds a profiles pipeline (and the `service.profilesSupport` gate), so the backend must store them. See `observability/profiles` |
| `sampling.traces_ratio` | `1.0` | **head sampling in the SDK**: every workload gets `OTEL_TRACES_SAMPLER=parentbased_traceidratio` + `OTEL_TRACES_SAMPLER_ARG=<ratio>`, so each service follows the root span and a trace is whole or absent. No collector samples traces. Needs a destination; your own sampler env wins |
| `sampling.errors_always` | `False` | keeping every ERROR trace is a *tail* decision (a head sampler cannot know a trace will fail), which needs the whole trace in one gateway. Forge renders none, so with `traces_ratio < 1` it is refused unless `destination_tail_samples = True` declares that the destination's gateway keeps error traces itself |
| `sampling.destination_tail_samples` | `False` | the destination's promise (e.g. the hosted platform's gateway), carried in the render; forge does not enforce it |
| `retention.{traces,metrics,logs}_days` | `0` | hints. The local ClickStack takes one table TTL (the longest); a node agent has no storage and ignores them |
| `local_port` | `0` | the loopback port the env's collector publishes; host processes export there |
| `profiles_port` | `0` | the loopback port of the local profiles collector (`otel-profiles`, beside ClickStack only); host processes get `OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT` aimed there |
| `agent.namespace / image / memory_limit_mib` | `forge-observability`, contrib pinned by digest, 256 | the node agent |


## What each runtime gets

| Runtime | Rendered | Workload env |
|---|---|---|
| **Host** (`forge env up`) | with no destination: the local ClickStack stores everything (today's default). With one: an `otel-forwarder` compose service replaces it and forwards there; its pipelines are rendered into `.forge/observability/forwarder.yaml` | `OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<local_port>` + `_PROTOCOL=http/protobuf`, **as a pair** |
| **Cluster** (`OnCluster`: k3d or any `ClusterTarget`) | with a destination: the **node agent** — DaemonSet, ServiceAccount, ClusterRole, ConfigMap and a node-local Service `otel-agent` (`internalTrafficPolicy: Local`) in `agent.namespace`, plus the NetworkPolicy telemetry allow. With no destination: nothing — loopback is unreachable from a cluster, so cluster workloads export nothing | `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-agent.<ns>.svc.cluster.local:4318` + `_PROTOCOL` |
| **Hosted** (`OnHosted`) | **nothing.** The platform runs its own agent and injects the endpoint | only `OTEL_EXPORTER_OTLP_PROTOCOL` and the `OTEL_<SIGNAL>_EXPORTER=none` switches; the endpoint is the operator's |

An author's own `OTEL_EXPORTER_OTLP_*` (endpoint or protocol, for any signal) wins **whole**: forge never pairs your endpoint with its protocol.

## The node agent

Apps **push** traces, metrics and profiles over OTLP. The agent pulls only what cannot push: container stdout (`filelog`) and kubelet/node metrics (`kubeletstats`). It scrapes no application endpoints. `k8sattributes` **enriches** (pod, namespace, node, workload labels); it decides nothing about whose data it is. Authentication is the destination's headers, read from the Secret into `FORGE_OTLP_HEADERS`. Attributing records to an owner is the receiving platform's rule, never forge's.

What `filelog` costs: a **read-only hostPath on `/var/log/pods`** (container logs are root-readable on the node), a hostPath for checkpoints and the exporter's persistent queue, root with every capability dropped and a read-only root filesystem. hostPath is forbidden by pod-security `baseline`, so the agent gets its **own namespace labelled `privileged`**. That is why it is never rendered by the Restricted (hosted) profile: its kinds (DaemonSet, ClusterRole) are outside `RestrictedKinds` on purpose.

## Secrets must resolve

On a cluster the headers Secret lives in `agent.namespace`, and nothing in forge creates it for a real env. The Bundle must say who provides it, or the render is refused:

```python
required_secrets = [forge.ExternalSecret {
    name = "otlp-auth", namespace = "forge-observability", keys = ["headers"],
    reason = "OTLP destination credentials",
}]
```

The deploy preflight then checks it exists in the cluster. For a local k3d env use a `rendered_secrets` entry in that namespace. For the local forwarder the value is the secret-store key: `forge secret set --env dev <key>` (value on stdin); `forge env up` refuses to start without it.

## Logs: one path

If the node agent (cluster) or the local collector (host) ships logs, leave `OTEL_LOGS_EXPORTER` unset. Setting it to `otlp` while `signals.logs` is on is refused at render, because every line would be stored twice. Turn `signals.logs` off to ship logs from the app instead.

A line read from stdout has no SDK resource of its own, so every JSON line carries the process resource under `"resource"`: `service.name`, `service.version`, `service.namespace`, `deployment.environment.name` and `service.instance.id`. These are the values the traces and metrics carry (Setup's resource), pre-encoded once per logger. The collectors forge renders (node agent, local forwarder, `deploy/observability/otel-collector.yaml`) lift those keys into the log record's **resource**. A key a trusted source already set wins: the pod's labels on a cluster, the file's path locally. A line without the object is unchanged and has **no version**: a panic, a runtime fatal error, plain text, an image forge did not build. Text output (`LOG_FORMAT=text`) is not stamped. Pass `observe.NewLogHandler` the `Config` you pass `Setup`.

## Hosted dev telemetry (`dev_telemetry`)

A LOCAL env's stack (`forge env up`) can store its telemetry in the org's hosted store instead of the local ClickStack. `observability.dev_telemetry` declares it:

| Value | Meaning |
|---|---|
| `"auto"` (default) | hosted when the env declares a `control_plane` AND a Reliant credential exists; otherwise local. Any failure on the way (control plane unreachable, `Unimplemented`, the org switch off, the gate excluding the org, the per-user instance cap (`dev_instance_limit`), a session it does not see live) falls back to local with the reason printed |
| `"hosted"` | the same, but a failure fails `env up` instead of falling back |
| `"local"` | always the local ClickStack |

`forge env up --telemetry=auto|hosted|local` and `FORGE_DEV_TELEMETRY` override the declaration, and `local` from any of the three wins: it is the opt-out. An org admin can also switch dev telemetry off for the org.

The stack is the **instance**: its session (one per env, host and worktree) is reported live, the control plane mints two 1-hour tokens for it (`MintLocalTelemetryToken`), and forge writes them to `.forge/obs/otlp-headers` and `.forge/obs/otlp-headers-browser` (0600, gitignored). The instance id in the record is the control plane's (the mint response's `instance.id`), not forge's local session id. A **telemetry keeper** (`forge env telemetry-keeper`, `.forge/logs/<env>/telemetry-keeper.log`) runs as one of the stack's tracked processes, detached or not: it heartbeats the session every minute and, on its own timer, renews both tokens at half-life (a refusal is retried from 15s, doubling to 5m), and `forge env down` stops it with the stack. A run that ends local (by choice, fallback or a failed `hosted` setup) first stops the env's running keeper and waits for it, then records `local` and removes any token a previous hosted run left; a keeper also stops itself once `backend-<env>.json` no longer records its instance. A failed renewal is silent: the app is never affected, and exports are dropped once the token expires. The decision is recorded in `.forge/obs/backend-<env>.json` for `forge obs`, and `forge env status <env>` prints `telemetry: hosted (org …, instance …, 3d)`, `local (<reason>)` or `not delivering (…)`. The first hosted run in a project prints what is sent, where, for how long, and how to opt out.

In hosted mode:

- No local collector, forwarder, profiles collector or Alertmanager starts, so a host-only env needs no Docker.
- Host processes get `OTEL_EXPORTER_OTLP_ENDPOINT=<hosted edge>`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, `OTEL_EXPORTER_OTLP_HEADERS_FILE=<abs>/.forge/obs/otlp-headers` and `OTEL_LOGS_EXPORTER=otlp` (no collector tails log files). A transport you exported or declared wins whole. Infra processes' logs (postgres, nats) are not collected. Profiles are off.
- Frontend dev servers get the edge as `OTEL_EXPORTER_OTLP_ENDPOINT` and the browser token file as `FORGE_OTEL_HEADERS_FILE`. The scaffolded `/_otel` proxy attaches it per request, server side (Vite: a `proxyReq` hook; Next.js: a loopback relay behind the rewrite), and drops the page's cookies. A frontend scaffolded before this has no hook: copy the `FORGE_OTEL_HEADERS_FILE` block from a freshly scaffolded `vite.config.ts` / `next.config.ts`, or its browser telemetry is refused.

Retention is declared on the same `retention` block: `traces_days`, `metrics_days`, `logs_days`, `browser_days`, and per-service `overrides = [forge.RetentionOverride {service = "worker", signal = "logs", days = 1}]` (`signal` is traces, metrics, logs or browser; `days` ≥ 1). The control plane clamps each to [1, the plan cap], and 0 means its dev default (3 days).

Database spans never record bind values: the driver tracer (`pkg/observe/dbtrace`) replaces every literal with `?`, and the ORM's own spans record the statement with `?` placeholders. A literal you write into SQL text by hand reaches an ORM span on a pool that is not driver-traced. Anything your code logs is sent as is, scrubbed for credential shapes at the gateway.

## Recipes

- **Local only (the scaffold default):** `observability = forge.Observability {local_port = _otlp_http_port}`.
- **Dev machine → your backend:** set `_destination` in `deploy/kcl/dev/main.k`, `forge secret set --env dev OTLP_HEADERS`.
- **Staging/prod cluster → your backend:** declare `destination`, the `required_secrets` entry above, and bind workloads to `OnCluster`.
- **Hosted:** declare `observability = forge.Observability {destination = ...}` only if you want the contract's protocol/signal switches; the endpoint is the platform's.
- **Off:** leave `observability` unset.
