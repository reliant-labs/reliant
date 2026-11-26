---
name: deploy/control-plane-secret
description: forge.ControlPlaneSecret, a Secret whose value the env's declared control plane supplies and renews. What it declares, why a first deploy is never blocked on it, why it needs a connected cluster, how a workload reads it with no restart on renewal, and how Observability derives one automatically for a control-plane OTLP destination.
---

# Control-plane secrets

Some Secrets are not yours to provision and not forge's to sync: the **control plane** mints them, writes them into the cluster, and renews them before they expire. The OTLP ingest token a Reliant-hosted collector requires is the first one. `forge.ControlPlaneSecret` declares that such a Secret exists, and where.

```python
control_plane = forge.ControlPlane {}
cluster_target = _prod                 # a ClusterTarget with connected_cluster = "acme-prod"
control_plane_secrets = [
    forge.ControlPlaneSecret {
        name = "reliant-otel-auth"
        cluster = _prod
        # namespace defaults to cluster.namespace
        # kind defaults to "telemetry_token"
    }
]
```

## It is a third kind of secret

| | provisioned by | deploy preflight | recorded in the shape |
|---|---|---|---|
| `ExternalSecret` | a person or pipeline, out of band | **blocks** until it exists | name, provider |
| a synced store secret | forge, from the env's secret store | applied by the deploy | name, provider |
| **`ControlPlaneSecret`** | **the control plane, after the deploy** | **never blocks** | name, namespace, cluster, connected cluster, kind, keys |

Because something else delivers it *after* a deploy, it cannot be a precondition of one. So:

- the preflight does not check that it exists, and a **first deploy is never blocked** on it;
- a workload mounts it as an **optional** Secret volume, so its pods start before the first delivery and see the value when it lands;
- the render-time "nothing declares this Secret" gate counts it as supply.

Declaring the same Secret as an `ExternalSecret` or a `rendered_secrets` entry too is refused: one Secret has one provider, and the second declaration would turn a first deploy back into one the preflight blocks.

## It needs a control plane and a connected cluster

The control plane can only write into a cluster it holds a credential for. Both are refused at render, with the fix:

- `control_plane = forge.ControlPlane {}` must be on the Bundle.
- The cluster must name a connected cluster (`forge cluster connect <name> --context <ctx> --env <env>`), by `ClusterTarget.connected_cluster` or `Bundle.connected_clusters`. **There is no k3d exemption** (unlike `refuseUnboundClusterTargets`): delivery is the control plane dialing the cluster, so a k3d cluster qualifies only once connected.

The connect grant that lets the control plane write exactly these Secrets is installed by `forge cluster connect` (see `deploy/cluster-connect`).

## Kinds

`kind = "telemetry_token"` (the default, and the only one you declare) is the per-environment OTLP ingest token. The `identity`, `identity_machine_key` and `identity_jwks` kinds are **derived** from `Bundle.identity` and refused if declared by hand (`deploy/managed-identity`); an `identity` Secret is `required`, so pods wait for its first delivery, where a telemetry token is always optional. For `telemetry_token` the control plane writes two keys of the one Secret:

| key | holds | read by |
|---|---|---|
| `OTEL_EXPORTER_OTLP_HEADERS` | `authorization=Bearer <jwt>` (the OTEL headers format) | workloads, from a file |
| `token` | the bare `<jwt>` | the node agent's `bearertokenauth`, from a file |

At most one `telemetry_token` per cluster and namespace: a workload reads exactly one OTLP credential.

## How a workload reads it, with no restart

A cluster workload in the **same cluster and namespace** as a `telemetry_token` gets, with no further declaration:

| rendered | value |
|---|---|
| volume `forge-otlp-auth` | the Secret, `optional: true`, key `OTEL_EXPORTER_OTLP_HEADERS` projected as `headers`, mounted read-only at `/var/run/secrets/forge/otlp` |
| env `OTEL_EXPORTER_OTLP_HEADERS_FILE` | `/var/run/secrets/forge/otlp/headers` |

`pkg/observe` re-reads that file when it changes (see `observability/destination`), so when the control plane rewrites the Secret the kubelet refreshes the directory and the next export carries the new token. It is a whole-directory mount, never `subPath`: the kubelet does not refresh a `subPath` mount.

A workload that sets `OTEL_EXPORTER_OTLP_HEADERS` or `OTEL_EXPORTER_OTLP_HEADERS_FILE` itself owns its auth and gets neither, as does one that declares its own volume named `forge-otlp-auth`. A tool, a host, a compose or a hosted workload gets neither: it has no cluster Secret to mount.

## Observability derives one for you

For a destination that **is** the control plane's OTLP ingest:

```python
observability = forge.Observability {
    destination = forge.ObservabilityDestination {
        endpoint = "https://otlp.reliantapi.com"
        token_from_control_plane = True
    }
}
```

forge declares a `ControlPlaneSecret` named `forge-otlp-token` in `Observability.agent.namespace` for **every cluster that runs the node agent**, and points the agent's `token_secret` at its `token` key. Nothing else is written, and the "credential must resolve" check is satisfied by the derived secret. It is exclusive with `headers_secret` and `token_secret`, and needs `control_plane` and a connected cluster per agent cluster.

## What the shape records

`forge env shape <env> --json` carries `shape.control_plane_secrets`, one entry per Secret, **no value ever**:

```json
{"name": "reliant-otel-auth", "namespace": "control-plane-prod", "cluster": "gke_p_prod",
 "connected_cluster": "prod-control-plane", "kind": "telemetry_token",
 "keys": ["OTEL_EXPORTER_OTLP_HEADERS", "token"]}
```

The control plane reads this list from the recorded shape to know what to deliver where. It is **not** part of `shape.secrets` (the names a person must set): a Secret the control plane writes is not one nobody has set.

## KCL fields (the contract the control plane consumes)

| Declared | Field |
|---|---|
| `Bundle.control_plane_secrets` | `[forge.ControlPlaneSecret]` |
| `ControlPlaneSecret` | `name`, `cluster` (a `ClusterTarget`), `namespace`, `kind`, `reason` |
| `ObservabilityDestination.token_from_control_plane` | `bool`, default `False` |
| render output | `output.control_plane_secrets[]`: `{type, name, namespace, cluster, connected_cluster, kind, keys, reason}` |
| recorded shape | `shape.control_plane_secrets[]`: `{name, namespace, cluster, connected_cluster, kind, keys}` |
