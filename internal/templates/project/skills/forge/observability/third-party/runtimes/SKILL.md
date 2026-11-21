---
name: observability/third-party/runtimes
description: Where a forge.Telemetry declaration is collected, per runtime — forge env up (the local telemetry agent, the .forge/logs/.telemetry includes, the agent log, the upgrade row for older otel-collector.yaml), forge's cluster node agent (no address pulls, mounted Secrets), Reliant hosted (shape-telemetry + collects-telemetry, the 5000/20 caps, allow required, scope platform refused), and your own collector (Observability.telemetry_collector, forge.telemetry_targets and forge.telemetry_collector with the exact call and what to splice), and the default telemetry of a forge.ManagedDatabase (ManagedDatabase.telemetry, default_telemetry, forge.managed_database_telemetry, forge.cnpg_operator_telemetry).
---

# Where a Telemetry is collected

The declaration is the same on every runtime; what collects it follows the **env**. forge resolves each `forge.Telemetry` into one normalized target (`forge env render <env> --json | jq .telemetry`), and every collector consumes that one contract through one translator. Declaration and attachment are in `observability/third-party`; this page is what runs.

| Env runs on | Collected by | Pulls (metrics, pprof, checks) | Logs | OTLP facts resolve to |
|---|---|---|---|---|
| `forge env up` (host, compose) | the **local telemetry agent**, plus the local ClickStack collector | yes, on loopback or published ports | the collector already tails `.forge/logs/<env>/` | `http://127.0.0.1:<local_port>`; no gRPC fact |
| a cluster, env with a `destination` | forge's **node agent** (DaemonSet) | pods only, **no `address`** | the agent's filelog | the agent's Service |
| a cluster, `Observability.telemetry_collector` declared | **your** collector | yours | yours | `telemetry_collector.otlp_*` |
| `forge.OnHosted` | the **platform** | declared ports of the workload's pods | the platform's | the platform's (recorded as a ref, value empty) |

Declaring `telemetry_collector` moves every target of the env off forge's agent: it is the collector that scrapes this env's Telemetry when that is not forge's.

## `forge env up`

A host target (a `HostInfra` engine, an `OnHost` process, a compose service on its published port, an `Observability` target with an `address`) is collected by the **local telemetry agent**: the pinned `otelcol-contrib`, run as a *host process* (a container could not reach 127.0.0.1) and fetched and checksummed through forge's release cache. It starts after the infrastructure is up and the local collector is serving, never when no host target pulls, and it only reports problems, never failing `env up`:

```
  telemetry: log lines parsed as declared for temporal (in the local collector)
  telemetry agent: started (pid 4242, otelcol-contrib 0.162.0) → the local ClickStack
    collecting temporal: metrics 127.0.0.1:9233/metrics, tcp check :7233
    config .forge/observability/dev/telemetry-agent.yaml · log .forge/logs/dev/telemetry-agent.log
```

A rerun with an identical config adopts the running agent; any change restarts it, but only after the collector's own `validate` accepts the new config. `forge env down` stops it. Read its config and log from the paths it prints.

- **Credentials** are written `0600` under `.forge/observability/<env>/secrets/<secret>/<key>` from the env's secret store, via the `SecretRef`'s `store_key`; values never enter the config. A missing key is reported with the exact fix, `forge secret set --env <env> <store_key>`.
- **Logs.** HostInfra logs are in `.forge/logs/<env>/<name>.log`, so the local collector sees them; a `LogParse` is applied by splicing pieces into the project's `deploy/observability/otel-collector.yaml`: three `${file:/var/log/forge/.telemetry/...}` lines, whose files (`logs-preserve.yaml`, `logs-identity.yaml`, `logs-parse.yaml` under `.forge/logs/.telemetry/`) `env up` rewrites on every run and reloads into the running collector. They sit inside the mount the collector already has, so `docker-compose.yml` is unchanged, and nothing in that directory ends in `.log`, so none is tailed. **Do not edit those files.**
- **An older project's `otel-collector.yaml` predates that hook.** A pristine copy is updated by `forge project upgrade`; an edited copy is reported with the missing lines. Until then `env up` warns that the declared log parsing is not applied, naming the lines to add. Metrics and checks are unaffected.
- With **no local collector** (hosted dev telemetry, observability off, no docker) `env up` prints a note and starts nothing.
- `HostInfra.temporal_metrics_port` is passed to the server as `--metrics-port`. An adopted server started before you declared it keeps its old port, and `env up` warns to restart it. Engines without a declared metrics port (Zitadel, Mailpit) cannot be scraped this way.

## A cluster with forge's agent

An env with an `Observability.destination` gets forge's node agent. It carries the targets of its cluster: pod discovery is node-local (each pod scraped once), the logs parse is spliced after the `container` operator and `k8sattributes`, and pprof pulls go through the agent's `k8s_observer`. Things it refuses at render rather than half-do:

- a target it would pull at an **`address`**: the DaemonSet has no singleton. Use a `selector`, or a collector of your own;
- a signal the env doesn't ship: profiles need `signals.profiles = True` with a destination;
- an `optional` credential (the collector reads every credentials file at config load, so a missing one stops it).

Every credential Secret a scrape names is **mounted non-optional from the agent's namespace**, which is `Observability.agent.namespace`, not the workload's. Supply it where the render can check it, exactly as the destination's own header secret:

```kcl
forge.Observability {
    destination = forge.ObservabilityDestination {endpoint = "https://otlp.example.com", headers_secret = ...}
    required_secrets = [forge.ExternalSecret {name = "ch-metrics", namespace = "forge-observability", keys = ["token", "ca.crt"]}]   # or rendered_secrets / a ControlPlaneSecret
}
```

Without a destination there is no agent, and a cluster Telemetry that nothing collects is flagged: declare a destination or a `telemetry_collector`.

## Reliant hosted (`forge.OnHosted`)

The platform collects what a hosted workload declares, **once the control plane advertises both `shape-telemetry` (it decodes the targets) and `collects-telemetry` (it collects them)**; a forge pin bump alone brings only the first. Until it does, `forge env deploy` warns (`declares telemetry this control plane does not collect yet`, naming the missing capability) and the declaration is withheld from the shape; nothing breaks. Rules, applied at `forge env render` and `env deploy`:

| Rule | Why |
|---|---|
| every scrape sets `allow` | an unbounded scrape stores every series the exporter emits |
| `sample_limit` at most **5000** per scrape; at most **20** pulled targets per env | hosted caps; the control plane may advertise tighter ones, which win |
| only the workload's **declared** ports; no `address`; no `selector` | the platform reaches the workload's own pods |
| no `bearer_token`, no `profiles` | the platform holds no tenant scrape credentials, and pulls no tenant profiles (a forge-built workload pushes its own) |
| `scope = "platform"` is refused | every hosted signal carries the env's identity |
| OTLP: `env` literals travel; `OtlpRef` values are the platform's | an older control plane strips what it does not know |

Alerts and dashboards need no new transport and work on any control plane.

```kcl
fw.Workload {
    name = "searchd", image = "ghcr.io/example/searchd:1.2"
    ports = [fw.Port {name = "metrics", port = 9100}]
    runtime = forge.OnHosted {}
    telemetry = forge.Telemetry {metrics = [forge.MetricsScrape {port = "metrics", allow = "searchd_(requests|errors)_total", sample_limit = 1000}]}
}
```

## Your own collector

When you run the collector (a platform agent of your own, in KCL), name it, and ask forge for the configuration instead of keeping a second copy of the translation.

```kcl
forge.Observability {
    telemetry_collector = forge.TelemetryCollector {
        namespace = "otel"                                  # what a NetworkPolicy admits
        pod_labels = {app = "otel-agent"}
        otlp_http_endpoint = "http://otel-agent.otel.svc:4318"   # a URL with a scheme
        otlp_grpc_address = "otel-agent.otel.svc:4317"           # host:port, no scheme
    }
    telemetry = [...]
}
```

Then, in the file that builds your collector, with the bundle in hand:

```kcl
_targets = forge.telemetry_targets(_bundle)          # every target of the env, normalized
_tel = forge.telemetry_collector(_targets, forge.TelemetryCollectorOptions {
    runtime = "cluster"                              # or "host"
    cluster = "k3d-hub"                              # the kubectl context whose targets this collector reaches
    discovery = "node-pods"                          # "all" (one collector), "node-pods" (a DaemonSet), "addresses" (the singleton beside it)
    mount_root = "/etc/otel/secrets"                 # you mount each referenced Secret at <mount_root>/<secret>
    resource_attrs = {"deployment.environment.name" = "prod"}
    processors = ["memory_limiter"]                  # yours: run first in every pipeline
    post_processors = ["batch"]                      # yours: run last in every metrics pipeline
    metrics_exporters = ["otlp/gateway"]             # yours: where metrics and checks go
    isolated = forge.TelemetryIsolatedExporter {     # required when a target has isolated = True
        name = "otlp/selfmon", config = {endpoint = "gateway.obs.svc:4317", tls = {insecure = True}}
    }
    collector = _bundle.observability.telemetry_collector   # set: the fragment carries standalone NetworkPolicies
})
```

`telemetry_collector` runs forge's own translator (`pkg/telemetry/collector`), so there is one implementation; a target it refuses fails the render, naming it. The fragment is *whole components* to merge and *pieces* to splice:

| Fragment field | You |
|---|---|
| `extensions`, `receivers`, `processors`, `exporters`, `pipelines` | merge into your config as they are (it refuses to overwrite a name); add `service_extensions` to `service.extensions` |
| `logs.preserve` | one operator for your `filelog` receiver: after `container` on a cluster; **before** your own line parsing |
| `logs.processors` | run in your logs pipeline after `k8sattributes`, in order |
| `node_metrics_processors` | add to your `kubeletstats` pipeline after `k8sattributes` |
| `k8sattributes_labels` | append to `k8sattributes`' `extract.labels` (the selector labels identity needs) |
| `network_policies` | add to the cluster's manifests (only for targets with `network_policy = True`) |
| `warnings` | what it skipped, and why (e.g. profiles with `enable_profiles` off) |

Three things bite. **Mount every referenced Secret non-optional** at `<mount_root>/<secret>`, as the collector fails its config load on a missing credentials file; the fragment does not list them, so collect `bearer_token` and `tls.ca` names from the targets. **If your `k8sattributes` also serves pushed OTLP pipelines, put `transform/telemetry_identity` in them too**, because it is what deletes the extracted selector labels. **Pprof needs `enable_profiles = True`** and `--feature-gates=service.profilesSupport` on the collector. With two collectors splitting discovery (a DaemonSet and a singleton), set `collector` on only one of them.

## A ManagedDatabase

A `forge.ManagedDatabase` is the one system forge watches with no line, because forge renders it (a CloudNativePG Cluster, on a cluster or by the platform on `forge.OnHosted`). Its default is an ordinary `forge.Telemetry`, `forge.managed_database_telemetry(name)`, collected like any other (the node agent, your `telemetry_collector`, or the platform). It covers the instances' `:9187` exporter under `forge.MANAGED_DATABASE_METRICS_ALLOW` (`sample_limit` 2000), the Postgres JSON logs (`record.error_severity` → severity), a tcp check on 5432, and a dashboard with id `<name>-postgres`. Its service name is `<name>-postgres`, attachment `manageddatabase`. A host-run Postgres (`HostInfra`) gets no default.

```kcl
forge.ManagedDatabase {name = "orders", ...}                     # watched by default
forge.ManagedDatabase {name = "orders", default_telemetry = False}   # off: the database and its share of the operator
forge.ManagedDatabase {name = "orders",                          # extend: the default, plus an alert
    telemetry = forge.managed_database_telemetry("orders") | {alerts = [_replication_lag]}
}
```

A declared `telemetry` **replaces** the default. To keep series from your own monitoring queries, write the scrape out with `allow = forge.MANAGED_DATABASE_METRICS_ALLOW + "|cnpg_my_query_.*"`. It targets the database's own instances, so `selector`, `namespaces`, `cluster` and `address` are refused. Ports are `metrics` and `postgres`, by name.

**The operator.** Each cluster holding a database that keeps the default also gets `forge.cnpg_operator_telemetry(cluster)`. That covers its `:8080` controller metrics (`forge.CNPG_OPERATOR_METRICS_ALLOW`), its logs and a dashboard, found by `app.kubernetes.io/name: cloudnative-pg` in every namespace. The service is `cloudnative-pg`, or `cloudnative-pg-<cluster>` when the env's databases span clusters. On hosting the operator is the platform's, so there is no target. Replace it with an `Observability.telemetry` entry of the same service name:

```kcl
forge.Observability {telemetry = [forge.cnpg_operator_telemetry("prod-hub") | {namespaces = ["cnpg-system"]}]}
```

**No NetworkPolicy by default.** One selecting the instances would deny every other Postgres caller. When the database's namespace already denies ingress, admit the collector on 9187 and 5432 yourself, or set `network_policy = True` in the override. forge then renders a policy beside the database's objects.
