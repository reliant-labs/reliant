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
| `hosted` | `forge.OnHosted` service and job, a hosted database declaring its schema (`migrations`, `orm`), and a frontend on `forge.OnHosted`. References stay references: `managedSecret` (from a config `SecretRef`'s `store_key`), `databaseRef`, `workloadURL` |
| `build-only` | `forge.BuildOnly` tools: a Go CLI with two build variants (one declaring its own `env` and `output_name`), and a docker-built image |
| `build-outputs` | Declared build `outputs`: a files-only `forge.BuildOnly` ShellBuild (no image, so `image` is `""` and no `build_image`) whose outputs are relative to its `cwd`, and an image-bearing build-only workload whose ShellBuild also writes an installer |
| `mixed` | One env binding host (the default), compose, cluster, hosted and build-only side by side. The cluster workload reaches the host `api` through the cluster's `host_gateway`. A schema-only `forge.Database` lands in `schema_only_databases`, and an OpenAPI and a proto `forge.API` in `apis` |
| `identity-hosted` | `forge.ManagedIdentity` on a hosted env: the API reads reserved managed secrets (`RELIANT_IDENTITY_*`), declared role members and an SMTP relay are recorded as names, and the one delivered Secret is a machine user's key |
| `identity-cluster` | The same declaration on a cluster env: roles only; applications, consumers, the delivered `identity` (required) and `identity_jwks` Secrets and the API's whole typed config are derived, with the broker token and JWKS snapshot as re-read files |
| `identity-share-with` | A preview env reusing staging's users: only derived applications and consumers, the owner named |
| `identity-host` | The dev loop: the identity on a host-run Zitadel, a seed user, config read from the env's secret store |
| `identity-cluster-list` | `identity-cluster` spelled `identities = [...]`. Its golden is byte-identical to `identity-cluster.json` (pinned by `TestRenderContract_IdentitiesSugarIsByteIdentical`): one identity renders the same whichever way it is declared |
| `identities-two` | `Bundle.identities`: two instances in one env, each with its own broker and Secrets, a gateway reading both under remapped fields, and a named `forge.IdentityRef`. The render carries `identities` and no `identity` |
| `secrets-components` | `forge.OpenBao`, `forge.ExternalSecretsOperator`, `bootstrap_source` and a store-backed `forge.ExternalSecret`. The ExternalSecret renders its ESO objects into `manifests` (the `reliant-secrets` ServiceAccount, the token Role + RoleBinding, a `SecretStore`, the `ExternalSecret`); the two components are still declarations only. This golden changes the day a later PR wires a component into the output |
| `flux-delivery` | `Bundle.applier = "flux"`: a self-hosted env with no control plane. The resolved `flux_delivery` (the env's one cluster, its delivery namespace and ResourceSet, the copied pull Secret, `signing` and `flux_verify` capped at `audit`) and the one bootstrap chart `flux-delivery` the render adds to `helm_charts` (the Flux Operator, the per-env delivery objects riding it) |
| `ml-stack` | `forge.MLStack` on a GPU ClusterTarget: an adopted, scraped exporter lands in `telemetry` (and that cluster's agent record); a bundle Install in `bundled_charts` with the `labels` Go stamps on its objects, plus its Namespace in `manifests`; a bootstrap Install (an operator with CRDs) in `helm_charts` with the same `labels`; the declared stack in `ml_stacks` |
| `telemetry-hosted` | `forge.Telemetry` on a `forge.OnHosted` image-only workload: the hosted target the release shape records for the platform to collect (`Shape.telemetry`). Its OTLP env names a fact with an empty `value` (the platform resolves it) beside a literal; a forge-built workload with no Telemetry contributes no target |
| `email` | `forge.Email` handed to workloads by `forge.email_env`: a host process reads the env's Mailpit (`forge.EmailOnMailpit`) as literals on 127.0.0.1, and a cluster worker reads a second sender on `forge.EmailOnSMTP`, its username as `managedSecret`, its password as `secretRef`, and a quoted display name in `MAIL_FROM`. No top-level key: an Email is env values on its readers |

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
- `databases[]` is `{name, runtime: cluster|hosted, cluster, namespace, spec}`,
  plus `{dialect, migrations, orm, migration_safety}` when the database
  declares `migrations` (so does an `external_databases[]` entry).
  `schema_only_databases[]` is `{name, dialect, migrations, orm,
  migration_safety}`, present only when declared.
- `apis[]` is `{name, kind: proto|openapi, spec, base_path?, database?}`,
  present only when declared.
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
