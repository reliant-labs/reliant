# render contract goldens

Each `<case>.json` is the document the KCL fixture `<case>.k` beside it
renders: `{"output": <forge.render(bundle)>}`, which is exactly what forge's
render seam (kclrender.Run) hands to `parseKCLEntities` and
`cluster.ExtractManifests`. There is one golden per runtime, plus
one mixed env. Together they are the §9.1 contract between three parties:

- the KCL lowering (`kcl/`, owner P2a), which produces the object;
- its Go decoder (`internal/cli` `parseKCLEntities`, owner P2b), which reads it;
- the KCL behaviour tests (owner P2c), which check the render reproduces it.

| Case | What it pins |
|---|---|
| `host` | `forge.OnHost` workloads: an air service with `listen_ports`, a go-run migrate job. Also `HostInfra` infra entries (postgres, nats with a monitor port, temporal with a namespace), a dev frontend, a `SecretRef` with `optional`, and a WorkloadURL resolved to `localhost` |
| `compose` | `forge.OnCompose` third-party services: no build, `spec.image` is `""`, and literal env is merged into the compose process `env` |
| `cluster` | `forge.OnCluster` workloads of every scheduled kind, a third-party image, a cluster `ManagedDatabase`, an opted-in `network_policy`, and the `forge.dev/v1alpha1 Workload` records in `manifests` |
| `hosted` | `forge.OnHosted` service and job, a hosted database, and a frontend on `forge.OnHosted`. References stay references: `managedSecret` (from a config `SecretRef`'s `store_key`), `databaseRef`, `workloadURL` |
| `build-only` | `forge.BuildOnly` tools: a Go CLI with a build variant, and a docker-built image |
| `mixed` | One env binding host (the default), compose, cluster, hosted and build-only side by side. The cluster workload reaches the host `api` through the cluster's `host_gateway` |
| `identity-hosted` | `forge.ManagedIdentity` on a hosted env: the API reads reserved managed secrets (`RELIANT_IDENTITY_*`), declared role members and an SMTP relay are recorded as names, and the one delivered Secret is a machine user's key |
| `identity-cluster` | The same declaration on a cluster env: roles only; applications, consumers, the delivered `identity` (required) and `identity_jwks` Secrets and the API's whole typed config are derived, with the broker token and JWKS snapshot as re-read files |
| `identity-share-with` | A preview env reusing staging's users: only derived applications and consumers, the owner named |
| `identity-host` | The dev loop: the identity on a host-run Zitadel, a seed user, config read from the env's secret store |
| `secrets-components` | `forge.OpenBao`, `forge.ExternalSecretsOperator`, `bootstrap_source` and a store-backed `forge.ExternalSecret`. The ExternalSecret renders its ESO objects into `manifests` (the `reliant-secrets` ServiceAccount, the token Role + RoleBinding, a `SecretStore`, the `ExternalSecret`); the two components are still declarations only. This golden changes the day a later PR wires a component into the output |

## Invariants

- `workloads[].spec` is `pkg/deploy/v1alpha1.WorkloadSpec` JSON. It decodes
  with `DisallowUnknownFields`.
- Every cluster-bound workload except `kind = "tool"` has exactly ONE
  `manifests[kind=Workload]` record. The record's `spec` equals
  `workloads[].spec` byte for byte. Its labels carry:
  - `forge.dev/cluster`: the kubectl context;
  - `app.kubernetes.io/part-of`: the project;
  - `forge.dev/env`.

  A tool is never scheduled, so it has no record, and no other runtime
  produces one.
- `workloads[].image` is the registry-less artifact name forge builds the
  workload into, or `""` when forge builds nothing for it.
- `spec.image` depends on the runtime:
  - cluster: the resolved reference (`<registry>/<image>:<tag>`, or
    `@<digest>` when `-D image_digests` has one);
  - hosted: the artifact name (the hosted publish pins it to `repo@digest`)
    or the author's third-party image;
  - host, compose and build-only: `""`.
- A forge-built `service` carries explicit probes: `/readyz` + `/healthz` on
  its `http` port (ADR 0002 §5). A third-party image gets none, and Go then
  defaults a TCP probe.
- How a `forge.WorkloadURL` in env lowers depends on the referrer:
  - host referrer: resolved to a value through `localhost`;
  - cluster referrer: resolved through its target's `host_gateway` when the
    target runs on the host (default `host.k3d.internal`);
  - hosted referrer: kept as `{"workloadURL": {"name": ...}}`.
- `network_policy` is `null` unless the Bundle sets `forge.NetworkPolicy`
  (opt-in). Go passes it as `deploy.Context.Network`.
- `databases[]` is `{name, runtime: cluster|hosted, cluster, namespace, spec}`.
- Optional values in the runtime, build, infra and frontend blocks project as
  `null` rather than being omitted.

## Regenerating

The goldens are GENERATED from the `.k` fixtures. Never hand-edit them.
`TestKCLModule_RenderContract` (internal/templates) renders each fixture and
compares the whole document against its golden. The comparison is key-sorted
with 2-space indentation. To regenerate:

    FORGE_UPDATE_GOLDEN=1 go test ./internal/templates -run TestKCLModule_RenderContract

Review a regenerated diff as a contract change. A changed key here is a
change that P2b's decoder and every consumer of `output` must follow.
