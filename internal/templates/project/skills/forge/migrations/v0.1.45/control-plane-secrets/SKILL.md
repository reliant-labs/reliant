---
name: migration-v0.1.45-control-plane-secrets
description: Upgrade v0.1.45 environments from optional or hand-provisioned telemetry secrets to declared ControlPlaneSecret delivery.
relevance: migration
---

# v0.1.45 — control-plane delivered secrets

Use when an env declares a control plane (`control_plane = forge.ControlPlane {}`) that mints and renews a Secret your workloads read, and that Secret is modelled today as an env var with `secret_optional = True` (to dodge the first-deploy block), as a `required_secrets` `ExternalSecret` you then provision by hand, or as a Secret someone mints on a calendar.

### What changed

- New primitive `forge.ControlPlaneSecret` on `Bundle.control_plane_secrets`. It declares a Secret whose value the control plane supplies and renews. It is **recorded in the env shape** (`shape.control_plane_secrets`: name, namespace, cluster, connected cluster, kind, keys; never a value), **never blocks a first deploy**, and is **refused without a connected-cluster binding** (and without `control_plane`). There is no k3d exemption: delivery is the control plane dialing the cluster.
- A cluster workload in the same cluster and namespace as a `telemetry_token` gets, automatically, an **optional** Secret volume `forge-otlp-auth` at `/var/run/secrets/forge/otlp` and `OTEL_EXPORTER_OTLP_HEADERS_FILE=/var/run/secrets/forge/otlp/headers`. `pkg/observe` re-reads that file, so a renewal needs **no restart**.
- `forge.ObservabilityDestination` gained `token_from_control_plane = True`: for a destination that is the control plane's OTLP ingest, forge declares the node agent's `ControlPlaneSecret` (`forge-otlp-token` in `agent.namespace`, key `token`) on every cluster that runs the agent. Exclusive with `headers_secret` / `token_secret`.
- The Secret volume source gained `optional` (an author can set it on a volume of their own). `output.control_plane_secrets[]` is a new render-contract key (empty when none); every `render_contract` golden gained `"control_plane_secrets": []`.

### Steps

1. Delete the workaround. Remove `secret_optional = True` from the `SecretRef` that read the delivered token, and remove the `required_secrets` `ExternalSecret` for it if there is one. Declaring the same Secret as both is refused ("one Secret has one provider").
2. Declare the Secret, on a cluster that names its connected cluster:

   ```python
   control_plane_secrets = [
       forge.ControlPlaneSecret {name = "reliant-otel-auth", cluster = _prod}
   ]
   ```

   `_prod` needs `connected_cluster = "<name>"` (or `connected_clusters = {"<context>": "<name>"}` on the Bundle); register the cluster once with `forge cluster connect <name> --context <ctx> --env <env>`.
3. Drop the env var that carried the headers (`OTEL_EXPORTER_OTLP_HEADERS` from a `SecretRef`). A workload that still sets it, or `OTEL_EXPORTER_OTLP_HEADERS_FILE`, is left alone and gets no volume.
4. For a node agent that exports to the control plane's ingest, replace `headers_secret` / `token_secret` plus the `required_secrets` entry with `token_from_control_plane = True` on the destination.
5. `forge env render <env>`, then `forge env deploy <env> --dry-run`: the preflight no longer asks for the Secret, and the pod spec gains the `forge-otlp-auth` volume (one rolling restart).

### Verify

`forge env shape <env> --json | jq .shape.control_plane_secrets` lists the Secret; a first `forge env deploy` where it does not exist yet passes the preflight.
