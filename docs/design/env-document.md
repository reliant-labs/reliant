# Design: the env document — an env's declaration as a public, schema'd API

**Status:** PR 1 (schema + this design) implemented; PR 2 (the loader) builds on it. **Date:** 2026-11-13.
**Owner direction (2026-11-13):** "I don't like forge.yaml becoming a master list of everything. Instead
users can populate the KCL, or instead they can populate what the KCL generates into (we can define a
yaml/json schema for the end result as the api)."

## Summary

- An environment's resources (workloads, databases, APIs, monitoring, frontends, …) are declared in ONE of
  two first-class ways:
  1. **KCL**, `deploy/kcl/<env>/main.k`, ending `output = forge.render(forge.Bundle {...})`, as today.
  2. **An env document**, `deploy/envs/<env>.yaml` (or `.yml` / `.json`): a file that IS the document the
     KCL render evaluates to.
- **The schema is the API.** `schema/env.v1.schema.json` (JSON Schema draft 2020-12, apiVersion
  `forge.dev/env/v1`) is GENERATED from the Go types forge decodes an env with, committed, embedded in the
  binary, printed by `forge project schema env`, and published at every release tag.
- **One loader.** A KCL render and an env document are parsed by the same Go parser (`parseKCLEntities`
  into `KCLEntities`, and `cluster.ExtractManifests` for the apply stream). There is no second model.
- **forge.yaml is project and tooling config only.** Its merged `envs:`, `monitoring:` and
  `database.migrations` keys keep working (semver 1) as **legacy shorthands** that lower into the same
  models; new users are steered to an env document or KCL. No new resource list is ever added to forge.yaml.

## 1. Where an env document lives

**Decision: `deploy/envs/<env>.yaml`, `deploy/envs/<env>.yml` or `deploy/envs/<env>.json`.** The file name
(without extension) is the env name, and must equal the document's `env`.

Why not the proposal's `deploy/<env>.yaml`: brownfield repos already put arbitrary YAML in `deploy/` —
helm values (`deploy/prod.yaml` is a common spelling), compose files, k8s manifests. Claiming every
`deploy/*.yaml` would turn a stranger's file into a load error, or worse, an env. A directory forge owns by
convention cannot collide, and it lists envs the way `deploy/kcl/*/` already does.

Rules (PR 2 enforces them at the env-declaration seam, `declareEnv`):

- An env is declared by **exactly one** of `deploy/kcl/<env>/main.k`, `deploy/envs/<env>.{yaml,yml,json}`,
  or (legacy) forge.yaml `envs:`. Two of them for one env is a load error naming both files, never a silent
  precedence: they could disagree about what runs where.
- Two extensions for one env (`prod.yaml` and `prod.json`) is a load error.
- A file under `deploy/envs/` that does not carry `apiVersion: forge.dev/env/v1` is a load error naming the
  file. The directory is forge's; a file in it that is not an env document is a mistake, not something to
  skip quietly.

## 2. Schema identity and versioning

**Decision: `apiVersion: forge.dev/env/v1`**, with an optional `$schema` (editor hint, ignored by forge).

- `forge.dev` is the domain forge already stamps on everything it owns (`forge.dev/v1alpha1` Workload
  records, `forge.dev/cluster` labels). `env` names the schema; `v1` is its major.
- The env document is not a Kubernetes object, so it has no `kind`; the apiVersion alone names the schema.
- `$id` is `https://raw.githubusercontent.com/reliant-labs/forge/main/schema/env.v1.schema.json`. Each release
  publishes the file at its tag (`.../reliant-labs/forge/<tag>/schema/env.v1.schema.json`); pin a tag in
  `$schema` to validate against the forge you run. `forge project schema env` prints the copy embedded in the
  running binary, which is exactly what it validates with.
- **v1 only grows.** A new optional property is a v1 change (the schema regenerates). A change that would
  reject a document v1 accepted — a removed property, a new required one, a narrowed type — is `v2`, a new
  file beside `env.v1.schema.json`, and forge keeps reading v1. forge's major is locked at 1, so in practice
  v1 is the schema for the life of forge 1.
- **`apiVersion` is required in an env document and optional in the schema.** A KCL render does not carry
  it (the forge that rendered it is the version), and the render-contract goldens must validate unchanged.
  The loader (PR 2) refuses a document without it, by name.

### How the schema is generated (and cannot drift)

- The root type is `envDocumentWire` (`internal/cli/env_document_schema.go`): the entity parser's wire type
  `kclRenderRaw`, embedded, plus the keys only `internal/cluster`'s manifest extraction decodes
  (`network_policy`, `statefulset_resize`, `overrides`, `bundled_charts`, `generated`, `manifests`), whose
  decode types are now exported from `internal/cluster/output_types.go` so both readers and the schema
  share them. `workloads[].spec` is `pkg/deploy/v1alpha1.WorkloadSpec`, the same type `decodeWorkload`
  decodes strictly.
- `internal/schemagen` is a small reflection generator that follows encoding/json's own rules. Its only
  inputs besides the types are in `envSchemaOptions`, next to the types:
  - the **tagged unions** the parser dispatches on through `json.RawMessage`: `workloads[].runtime`
    (`dispatchRuntime`), `workloads[].build` (`dispatchBuild`) and `frontends[].runtime`
    (`FrontendRuntime.UnmarshalJSON`). `TestEnvSchemaUnionsMatchDispatch` holds each union to its
    dispatcher;
  - the two types whose wire shape is not their fields (`DockerBuild`, `config.BuildContext`: a path or a
    GitSource) and `wireInt64` (a number or a numeric string);
  - which properties are **required**: `project`, `env`, and each workload's `name` and `runtime`.
- Doc comments become `description`s (read from source by the drift test, first paragraph only), so an
  editor hovering a field shows the Go comment that defines it.
- `TestEnvSchemaIsCurrent` regenerates the schema and fails on drift (`FORGE_UPDATE_GOLDEN=1` rewrites it);
  a field added to any type the env decodes into flows into the schema with no other edit. The parallel
  `databases[]` / `apis[]` additions arrive this way.
- `TestEnvSchemaRenderContractsValidate` validates every `internal/cli/testdata/render_contract/*.json`
  golden twice: with forge's validator, and with an independent draft 2020-12 implementation
  (`github.com/google/jsonschema-go`, already in the module graph through the MCP SDK; now a direct
  dependency, test-only use), so the schema is a real JSON Schema and not one only forge agrees with.

### Strictness

Every object accepts only the properties its Go struct declares (`additionalProperties: false`), and every
non-required property may be `null` (encoding/json accepts null everywhere, and the render projects unset
optionals as null). Unknown keys are what a typo looks like, and a hand-written document is where typos
happen. Writing the schema this way immediately found four places the render emits keys the Go types did
not declare (`cluster_target.image_pull_secrets`, `control_plane_secrets[].type`, `required_secrets[].type`,
`openbao.warnings`) — now declared — and one real bug: build variants' `tags` were rendered as `tags` and
read as `build_tags`, so a build variant's Go build tags were silently dropped. The Go field now reads
`tags`.

Validation errors name the path and the field, with a did-you-mean:

```
workloads[0].runtime.prot: unknown field (did you mean "port"?)
workloads[1].runtime.type: "kubernetes" is not one of: build-only, cluster, compose, external, host, hosted
env: required, and missing
```

forge validates **documents**, not KCL renders: a render is produced by the forge that reads it, and its
goldens are validated in tests. So a render carrying a key a pinned forge does not know never fails a deploy.

## 3. Lowering: what the loader re-applies, and what a document must state

A KCL render is post-lowering: `kcl/render.k` `_lower` (per workload) and `render` (per env) decide things
the author did not write. A hand-written document must not silently skip those guarantees. **Rule: a
document may omit what Go can derive; the loader applies the same default or check in Go, tested against
the KCL version so KCL stays the single source. Where only KCL can compute something, the document must
state it, or the loader refuses the declaration that needs it, naming why.**

| Lowering step (`kcl/render.k`)                                                                                                                                                        | What it decides                                                                                                                                                                                                                                                                                     | Decision for an env document                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              |
| ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `_bound_runtime`                                                                                                                                                                      | each workload's runtime (no env default, ADR 0002 §2)                                                                                                                                                                                                                                               | **Required** in the schema (`workloads[].runtime`, a tagged union). Nothing to derive: placement is the author's.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `_mask_violations` (hosted)                                                                                                                                                           | fields a hosted workload may not set (generated from `v1alpha1.FieldProfiles`)                                                                                                                                                                                                                      | **Go already owns it**: `Workload.Validate(ProfileRestricted)` is the real gate (KCL's is a copy), and the deploy path runs it on the PINNED spec (`deploytarget.PreflightHosted`), for a document as for KCL. The loader does not run it early: before the hosted publish pins the artifact name to `repo@digest` it would refuse every forge-built hosted workload.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| `_mask_violations` (host / compose / build-only)                                                                                                                                      | fields those runtimes cannot honour (`_LOCAL_REFUSED`, `_COMPOSE_REFUSED`, `_BUILD_ONLY_REFUSED`, the host runner rules)                                                                                                                                                                            | **Port to Go** (three small tables). A parity test reads the tables out of `kcl/render.k` and fails when the two differ.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `_artifact`, `_build_image`, `_check_built_digest`, `_check_image_names_registry`, `_spec_image`; `kcl/base.k` `image_tag` / `workload_image_ref_for`; `kcl/lib/images.k` `image_ref` | the exact reference a cluster (or an external tool) pulls, pinned per release: the env's tag (`-D image_tag`), the digest the push captured (`-D image_digests`), a recorded release's per-slot reference (`-D forge_workload_image_digests`, refused when unbound under `forge_immutable_release`) | **Port to Go** (`internal/envdoc/image.go`; follow-up PR to #2216). A built workload states `image` (KCL's `Workload.image`: the repository, registry included, `:tag` to pin the build's tag); `image` / `build_image` are normalized from it as render.k does, and `spec.image` (with its Workload record's) is **derived**. A stated `spec.image` is accepted only when it is the unbound derivation (a render saved as a document), and is re-derived; any other is refused (it would deploy something other than what forge pushes). Prebuilt, sidecar and release-input references are **stated** in full; a snapshot slot pin replaces them and an immutable render refuses an unbound one, as in KCL (KCL appends the env tag to an untagged prebuilt image; a document's stated reference is the resolved form, so it is kept). The tag: the render's binding, else the document's `image_tag`, else the env default (`_IMAGE_TAG_DEFAULTS`) — a binding outranks a stated `image_tag`, since a document cannot say whether it is a pin or the tag a saved render carried. Parity: `TestEnvDocumentImages_KCLMatchesGo` renders the fixtures with built cluster workloads under a deploy's bindings and loads their unbound goldens as documents under the same, requiring equal workloads and applied streams; `TestLoweringRederivesEveryRenderContract` re-derives each golden's `spec.image`; `TestImageTagDefaultsMatchKCL` reads the tag table. Hosted (`spec.image` = the artifact name, pinned by Go's hosted publish), host, compose and build-only need no resolution. |
| env map → wire list, `WorkloadURL` resolution                                                                                                                                         | each env value in its channel; a URL resolved for host/cluster referrers, kept as a reference for hosted                                                                                                                                                                                            | **Stated.** The document IS the wire form (`spec.env[]`); a URL a host or cluster workload reads is written as its value. A hosted reference stays a reference (the control plane resolves it).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| `_derived_env`: `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES`                                                                                                                       | the identity every span, log and metric carries: service, env, project, release                                                                                                                                                                                                                     | **Port to Go** (a few lines). Applied for runtimes that run a process, only where the author left the key unset. `service.version` takes the release being deployed. These are what make a deploy's telemetry attributable to its release (`forge obs check`), so they are the derived keys that must not depend on KCL.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| `_derived_env`: `OTEL_EXPORTER_OTLP_*`, sampler, signal-off keys, profiling, `GOMEMLIMIT`, the control-plane OTLP headers file                                                        | where a process exports telemetry, given the collector topology KCL lays out                                                                                                                                                                                                                        | **Stated** (when the document's workloads should export): they depend on the local collector / node agent topology a KCL env lays out, which a document env typically does not run. A hosted workload's endpoint is injected by the platform.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| `_default_probes`                                                                                                                                                                     | `/readyz` + `/healthz` on the `http` port for a forge-built service (ADR 0002 §5)                                                                                                                                                                                                                   | **Port to Go** (small). Same rule: only when `spec.probes` is unset; tested against the probes the render-contract goldens carry.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| `_workload_record` (`manifests[]`)                                                                                                                                                    | one `forge.dev/v1alpha1` Workload record per cluster-bound, non-tool workload, spec byte-identical                                                                                                                                                                                                  | **Port to Go** (small): synthesized when the document's `manifests` carries none for a cluster workload; the labels follow the render-contract invariant. Other objects (Namespaces, ConfigMaps, Gateway API objects, `forge.Manifests`) are **stated** in `manifests[]`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| `_check_requires`, `_check_before`, duplicate names                                                                                                                                   | references name declared workloads                                                                                                                                                                                                                                                                  | **Port to Go** (dangling `requires` / `before` refused; duplicates are already refused by `parseKCLEntities`).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| `_check_config_secrets`, `_check_built_images`, `_check_built_digest`                                                                                                                 | KCL-authoring checks (config projection, shell build edges, `-D` digests)                                                                                                                                                                                                                           | **Not applicable**: their inputs (`config_secrets`, `built_images`, `-D image_digests`) are authoring fields that do not exist in the output.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| identity (`_idn_*`), control-plane secrets (`_cps_*`), derived uptime checks, `data_access` default                                                                                   | derived records                                                                                                                                                                                                                                                                                     | **Stated**: the document carries the resolved form, as a render does. Absent means none (`data_access` absent is audited, never open — that default already lives in Go).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| schema defaults: `control_plane` (`type`, `endpoint`, `token_env`, `registry_host`), `observability` (`enabled`, `signals`, `sampling.traces_ratio`)                                  | what an author who wrote `{}` gets                                                                                                                                                                                                                                                                  | **Port to Go**: an omitted value takes the KCL schema default, tested against a KCL evaluation of the empty schema. This is what lets the monitoring-only document below be four lines.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |

## 4. Minimal documents per entry point

Each validates against the schema; PR 2's tests load each through the loader.

Monitoring only (the adoption hook: sign in, `forge obs init`, telemetry flows):

```yaml
apiVersion: forge.dev/env/v1
project: shop
env: prod
control_plane: {} # Reliant's control plane; endpoint/token_env take their defaults
observability:
  enabled: true
  destination: { token_from_control_plane: true }
```

Record deploys from the repo's existing CI (no hooks: record-only, `forge.OnExternal`):

```yaml
apiVersion: forge.dev/env/v1
project: shop
env: prod
control_plane: {}
observability:
  { enabled: true, destination: { token_from_control_plane: true } }
workloads:
  - name: api
    runtime: { type: external }
  - name: worker
    runtime: { type: external, status: ["./scripts/status.sh"] }
```

One database with its migrations (the `dialect` and `migrations` fields land with the parallel
`databases[]` work; they are generated into the schema the moment the Go types gain them):

```yaml
apiVersion: forge.dev/env/v1
project: shop
env: prod
databases:
  - name: main
    dialect: postgres
    migrations: { dir: db/migrations, driver: goose }
```

An OpenAPI API (`apis[]`, the same parallel work; validated and listed, nothing generated yet):

```yaml
apiVersion: forge.dev/env/v1
project: shop
env: prod
apis:
  - name: public
    kind: openapi
    spec: api/openapi.yaml
```

## 5. forge.yaml's resource keys are legacy shorthands

`envs:` and `monitoring:` (forge#2096) and `database.migrations` / `database.migrations_dir` (forge#2084,
#2097) shipped before this direction. Under semver 1 they are **not removed**:

- they keep working exactly as today, lowering into the same models (`obsbackend.Facts` through
  `yamlObsTarget`, `config.DatabaseConfig`);
- their config comments and the schema-surface docs mark them **deprecated: legacy shorthand**, naming the
  env document as the replacement;
- no runtime warning: forge's config loader has no deprecation-warning channel for keys that still work
  (its removed-key handling is for keys that no longer do), and a warning on every command for a key that
  works would be noise;
- skills (`deploy`, `adopt`, `observability/monitoring-only`) teach the env document first.

forge.yaml keeps what it is for: name, the forge pin, harness, lint, scaffold, ci, codegen knobs, dev_stack.

## 6. The seam (PR 2)

`declareEnv` (`internal/cli/env_declaration.go`) gains the third source, `envDeclDocument`. Every command
reads an env's entities through `RenderKCLWith` and its manifests through `cluster.RenderManifests*`; both
branch at ONE place: when `deploy/envs/<env>.*` exists, the raw bytes come from the document (converted to
JSON, validated, lowered as in §3, and wrapped as `{"output": …}`) instead of a KCL evaluation. Everything
downstream — `env render`, `env status`, `env deploy`, `env verify`, `obs`, `release record`, `build` — is
unchanged, because it already reads the parsed entities.

## 7. What PR 2 changed around the seam

- `ListEnvs` / `EnvExists` include document envs; the "environment not found" checks (`env render`, `env
shape`, `env deploy`, `env status`, the ledger choice of `release record` / `env releases` / promote,
  `forge cloud token`, `forge build`) ask `envHasDeclaration` instead of stat'ing `main.k`.
- `declareEnv` has the fourth source, `document`; a document plus a KCL tree, or plus forge.yaml `envs:`,
  for one env is a load error naming both.
- `forge obs init` with document envs registers them and mints tokens, taking the project and control plane
  from the documents, and writes no forge.yaml (its `envs:` would declare them twice).
- `forge obs init` on an env nothing declares writes its env document, `deploy/envs/<env>.yaml` (the minimal
  monitoring-only one in §4, its destination copied from forge.yaml's legacy `monitoring:` when there is one,
  or `--control-plane`), never forge.yaml `envs:`. It writes no forge.yaml at all: the document names the
  project. An env forge.yaml `envs:` already declares stays there (init only switches a missing
  `monitoring:` on). A KCL env is refused as before. One init signs in once, so envs whose declarations
  send telemetry to different places are refused together.
- With no forge.yaml, the project name the control-plane verbs use (`hostedProjectName`) is the one the
  env documents agree on.
- Readers that gate on the KCL tree alone (for example the parallel `databases[]` declarer, which
  fingerprints `deploy/kcl/**/*.k` before it renders envs) must count `deploy/envs/*` too; whichever of the
  two changes lands second wires that.

## Alternatives considered

- **Hand-written JSON Schema.** Rejected: a second copy of the model, drifting from the first the day a field
  is added. Generating it is what makes "the schema is the API" true.
- **`invopop/jsonschema`.** Not in the module graph; adding it buys reflection we have in ~300 lines, and its
  output still needs our unions and required set. `google/jsonschema-go`'s inference inlines every type
  (no `$defs`, a cycle is an error) and requires every non-omitempty field, which a null-projecting render
  violates; it is used only as the independent validator in tests.
- **Using `google/jsonschema-go` as the runtime validator.** Its errors name the schema location, not the
  document path (`validating /$defs/ClusterTargetEntity: unexpected additional properties`). A user needs
  `cluster_target.image_pull_secrets: unknown field`. forge's validator implements exactly the keywords the
  generator emits; `schemagen.Compile` refuses any other, so no rule is skipped silently.
- **Moving every entity type into an exported package (`pkg/envdoc`).** The right long-term home (the
  `max-public-structs` note in `kcl_render.go` says so), but a ~50-type move would bury this change and
  collide with the parallel database/API work. The generated schema is independent of where the types live,
  so the move can happen later without changing the API.
