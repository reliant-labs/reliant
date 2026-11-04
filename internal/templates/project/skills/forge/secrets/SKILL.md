---
name: secrets
description: The forge secret-provider model — declare a secret once as a reference, bind its value per-env via a provider (a gitignored YAML secret store for dev/local, external for prod/staging), and let forge resolve + inject it into the services that declare it. forge projects use no .env files. KCL stays pure — no secret values ever in rendered output.
---

# Secrets

## Mental model

A secret is **declared once** as a *reference* (an `EnvVar` with a
`secret_ref`); its **value** comes from a **per-env provider** set on the
bundle. Forge resolves the value once and injects it per runtime
(host / compose / external / k8s). KCL stays pure — secret values never
appear in rendered KCL output, only the references do. Dev/local pulls
values from a gitignored YAML secret store (name -> value);
prod/staging declares that the values live somewhere forge never sees
(External Secrets Operator, sealed secrets, workload identity).

**forge projects contain no `.env` files.** `forge lint` fails on one —
see "Why a directory, not a `.env` file" below.

## Declare (once, env-invariant)

### The normal path: a `sensitive` config field

App config lives in `proto/config/v1/config.proto`. Marking a field
`sensitive: true` is the whole declaration — forge does the rest:

```proto
string database_url = 3 [(forge.v1.config) = {
  env_var: "DATABASE_URL",
  required: true,
  sensitive: true,
  description: "PostgreSQL connection string."
}];
```

What that annotation buys, in every environment:

- `deploy/kcl/config_gen.k` types the field as a `ConfigSecretRef`
  (a Secret **name + key**), defaulted to
  `{name = "<project>-secrets", key = "<env_var lowercased>"}`.
- The same file's `appConfigEnvMap` projects it as `from_secret`, so the
  rendered manifest carries `valueFrom.secretKeyRef` — **never** an
  inline `value:`.
- The per-env `deploy/kcl/<env>/config.k` (git-tracked) carries
  **nothing** for it. There is no slot to accidentally paste a password
  into.
- `pkg/config` registers **no CLI flag** for it (a secret must not land
  in shell history) and never echoes it.

Point it at a different Secret by writing the override in `config.k`:

```kcl
app_config: config_gen.AppConfig = {
    database_url = config_gen.ConfigSecretRef {name = "rds-creds", key = "url"}
}
```

`forge doctor --signal deploy` fails the **Deploy Secrets** check when
any credential-shaped env var renders as a literal — that check going
green is what the annotation is for.

**Which workload carries it** is decided by WHICH config message declares
the field, then by the workload's `config_secrets` naming it. Put a
credential on the config of the process that reads it — a binary's own
message (`(forge.v1.binary_config) = {binary: "admin"}`), or one
subcommand's when one binary runs several processes
(`{binary: "app", command: ["server", "api"]}`) — and no other workload's
manifest can carry it, whatever its `config_secrets` says. For such a
binary, `config_gen.<bin>ProcessEnvMap(cfgs, argv, config_secrets)` picks
the process's configs from the workload's own `command + args` and refuses a
`config_secrets` name that neither of them declares (`binaries` skill).

### The escape hatch: a raw `EnvVar` secret_ref

For a secret that is NOT app config (a sidecar's token, an infra
service's password), declare the reference directly. A secret reference
is a `forge.EnvVar` carrying `secret_ref` (+ optional `secret_key`,
which defaults to `name`). It projects to the same k8s `secretKeyRef`.
For the HOST runtime the store key is `EnvVar.name`.

```kcl
# A service's env_vars — same declaration in every env
forge.EnvVar { name = "STRIPE_SECRET_KEY", secret_ref = "app-secrets" }
forge.EnvVar { name = "JWT_SIGNING_KEY",  secret_ref = "app-secrets", secret_key = "jwt-signing-key" }
```

**Declaring is what makes a value live.** A service receives ONLY the
keys it declares — a value sitting in the store that no service
references reaches nothing. That is deliberate: it means the store
cannot become a side channel for configuration.

```bash
# Store the values (or hand-edit the YAML). --env is REQUIRED — there is no
# default env and no positional form, so a secret can't land in the wrong one.
printf '%s' "$STRIPE_KEY" | forge secret set --env dev STRIPE_SECRET_KEY
forge secret set --env dev JWT_SIGNING_KEY --from-file ./key.pem

forge secret unset  --env dev JWT_SIGNING_KEY
forge secret ensure --env dev   # FileSecrets: create the store + list refs with no value yet
forge secret list   --env dev   # names + presence, never values — works for EVERY provider
```

`set` on a key that already has a value REPLACES it. For a hosted store that
is a new version, and that IS rotation — there is no separate rotate command
(`created v1` / `rotated to v3` in the output).

## Per-env provider

Set `Bundle.secret_provider` per env. It picks where forge gets values.

```kcl
# deploy/kcl/dev/main.k — DEV reads a gitignored directory
_bundle = forge.Bundle {
    secret_provider = forge.FileSecrets { path = "secrets/dev.yaml" }
    services = [ ... ]
}
```

```kcl
# deploy/kcl/prod/main.k — PROD: values live outside forge's view
_bundle = forge.Bundle {
    secret_provider = forge.ExternalSecrets {}
    # Declare WHAT must exist out of band, so a missing Secret BLOCKS the
    # deploy instead of surfacing as CreateContainerConfigError minutes later.
    required_secrets = [forge.ExternalSecret {
        name = "myapp-secrets"
        namespace = "myapp-prod"
        keys = ["database_url"]
        reason = "backs every `sensitive` config field"
    }]
    services = [ ... ]
}
```

- `forge.FileSecrets { path = "secrets/dev.yaml" }` — `type="file"`; a
  gitignored YAML file mapping env-var name to value. DEV / E2E only;
  forge rejects it in any other env. This is what the scaffold emits.
- `forge.ExternalSecrets {}` — `type="external"`; a pure marker, **no
  other fields**. PROD / STAGING.
- `forge.HostedSecrets {}` — `type="hosted"`; values live in the env's
  control plane (the managed secret store). Requires
  `control_plane = forge.ControlPlane {...}` on the same Bundle (load-time
  check). `forge secret set/list/unset --env <env>` write/list through that
  control plane (endpoint + credential from `control_plane`, i.e. `--token` /
  `$FORGE_CONTROL_PLANE_TOKEN` / `forge login` / Reliant's session via
  `$FORGE_CREDENTIAL_HELPER`), scoped to the control-plane
  environment addressed by (this project's forge.yaml `name`, the env's NAME).

  **That one credential also authenticates the platform CONTAINER REGISTRY**,
  which is why a hosted project has no registry secret to manage. `forge
  registry login <env>` takes no `--username` and no `--password-*` for our
  host (it refuses them), and `forge env build --push` / `forge env deploy` log
  in on their own before the first push. So the only image-related secret a
  hosted CI job needs is the one it already has. See the `deploy` and `ci`
  skills.
  Set a hosted workload's secrets with `forge secret set --env <env> <KEY>`
  before `forge env deploy`: deploys that need an unset one fail until it is
  set (not yet a queued gate). What happens to the values depends on the env's KIND, which forge derives
  from the env's own workloads:
  - **persistent** — the env declares ≥1 hosted tier (SimpleBackend /
    StaticSite / ManagedDatabase). The platform runs it and materializes the
    values IN-CLUSTER into the `forge-managed-secrets` Secret every
    `managedSecret` env var reads. The store is write-only: no forge process
    ever reads a value back. A changed value rolls the pods that read it.
  - **local** — the env declares control_plane but NO hosted tier (host /
    compose / frontend workloads run by `forge env up`). The control plane is
    only its secret store. `forge env up` PULLS the values (signed
    in to Reliant, or after `forge login` standalone) and injects them — in memory only, never written to disk —
    into each host service, job, compose/external service and frontend that
    declares them. A failed pull fails `env up` before anything starts.
    `forge env deploy` refuses a local env: there is nothing to publish.

  The kind is immutable on the control plane: flipping a persistent env to
  local (to make its values readable) is refused, not silently applied.
- `forge.DotenvSecrets { path = ... }` — **DEPRECATED**. Still resolved
  so existing projects run; `forge lint` fails on the file and
  `forge secret migrate --env <env>` converts it.

A dev env on the managed store (the shape a real project uses so dev
credentials never live in a file on a laptop):

```kcl
# deploy/kcl/dev/main.k — LOCAL: runs here, secrets pulled from the control plane
_bundle = forge.Bundle {
    control_plane   = forge.ControlPlane { endpoint = "https://api.example.com" }
    secret_provider = forge.HostedSecrets {}
    services = [ ... ]   # host / compose workloads — no hosted tier
}
```

```bash
forge login                                              # once, without Reliant; signed in to Reliant? skip it
printf '%s' "$KEY" | forge secret set --env dev STRIPE_SECRET_KEY
forge env up dev                                         # pulls, injects in memory, starts
```

### Random key material: `generate` (FileSecrets only)

An encryption key or session secret has no source to `set` it from. Declare
it generatable on the provider and forge mints it when absent:

```kcl
secret_provider = forge.FileSecrets {
    generate = {"VAULT_KEY" = forge.GeneratedSecret {prefix = "v1:"}}  # 32 bytes, base64
}
```

`forge secret ensure` and `forge env up` write it (0600, key name printed,
never the value) and never overwrite an existing value. Hosted/external
providers never generate. The value is durable; rotate by hand with `set`.

## Secrets a plain manifest mounts: `Bundle.rendered_secrets`

A provider serves the secrets your **services** declare. Some Secrets have
no service behind them: an in-cluster database, vault or broker you run from
raw manifests (`additional_manifests`) mounts a Secret by `secretKeyRef`,
and nothing about that manifest tells forge which cluster it runs in.

Declare those on the Bundle, beside whatever provider the env uses:

```kcl
# deploy/kcl/dev/main.k — services read FileSecrets; the in-cluster
# store's raw Deployment + Job mount three Secrets no service references.
_bundle = forge.Bundle {
    cluster_target  = _target          # default placement for rendered_secrets
    secret_provider = forge.FileSecrets {path = "secrets/dev.yaml"}
    rendered_secrets = [
        forge.RenderedSecret {
            name = "vault-storage"
            keys = {"connection-url" = forge.RenderedSecretKey {from = "literal", value = "postgres://postgres:postgres@host.k3d.internal:5434/vault"}}
        }
        forge.RenderedSecret {
            name = "vault-unseal"
            # A store key: the value lives in secrets/dev.yaml, never in git.
            keys = {"static-key" = forge.RenderedSecretKey {key = "VAULT_STATIC_KEY"}}
        }
        forge.RenderedSecret {
            name = "vault-unseal"
            cluster = "k3d-workload"         # explicit placement elsewhere
            namespace = "workload"
            keys = {"static-key" = forge.RenderedSecretKey {key = "VAULT_STATIC_KEY"}}
        }
    ]
    additional_manifests = [ ...the raw Deployment and Job... ]
}
```

- **Placed by declaration, never inferred.** Each Secret lands at its own
  `cluster` / `namespace`, defaulting to `cluster_target`'s. No cluster and
  no `cluster_target` is a load error, and so is the same name twice in one
  cluster/namespace.
- **Values never enter git.** `from = "file"` (the default) names a key in
  the env's secret store, which is the FileSecrets file when the env declares
  one (else `secrets/<env>.yaml`), layered over the primary checkout's store
  in a worktree. Only the key is in the render. `from = "literal"` is
  permitted in dev/e2e only, by a KCL check and a Go guard.
- **Same guarantees as a provider Secret.** Applied before the Deployments,
  so the first schedule finds it. Local clusters only (it is plaintext). It
  counts as supply for the deploy preflight's mount check.
  `forge secret ensure --env <env>` lists a store key with no value, and
  `forge secret list` attributes it to its Secret.
- **Prefer it to a hand-written `kind: Secret`.** A raw Secret manifest with
  literals gets none of the above, and a real credential would have to be
  pasted into git to be expressed at all.

`RenderedSecret` also accepts `cluster` / `namespace` inside a
`forge.RenderedSecrets` provider. There, an entry with no `cluster` keeps
the provider's inference (it lands where a service references it by
`secret_ref`), and one that names a cluster is placed explicitly. So an
infra-only Secret no longer needs a dummy "carrier" `secret_ref` on some
service to make it render.

## Per-runtime: what forge does

With **FileSecrets**, forge reads the YAML store (env-var name -> value)
and injects it differently per runtime:

| Runtime | What forge does |
|---|---|
| host / air | Each service gets the keys **it declares** via `secret_ref` — not the whole store. Per-service `HostDeploy.secrets_file` is only a backward-compat fallback when no bundle provider is declared. |
| compose / external | Values are merged **under** the `env_file` overlay — an explicit `env_file` wins. |
| k8s | Forge **renders** Secret objects CLI-side from the declared cluster `secret_ref`s and `kubectl apply`s them **before** the Deployments, so `secretKeyRef` resolves. Each Secret lands in **every cluster and namespace whose workloads declare it**, carrying only the keys those workloads declare — a multi-cluster dev env needs no `forge.ExternalSecret` or mirror script for a store-backed Secret. Guarded by an `isLocalCluster` check — forge **refuses** to render plaintext into a non-local cluster (only k3d / kind / docker-desktop / minikube / rancher-desktop / colima / orbstack). |

**Validation:** `forge env up` / `forge env deploy` **fail-fast** if a declared
`secret_ref` has no value in the store (a file store, or a LOCAL env's pulled
hosted store), listing every miss with the `forge secret set --env <env>` line
that fixes it.

**`forge secret list --env <env>`** answers for every provider: `file` reads
presence from the YAML; `hosted` lists names + current versions; `rendered`
reports whether each declared Secret key resolves from its source; `external`
and `none` list the declarations with presence `unknown` (exit 0 — unknown is
an answer, not a refusal). `--json` carries `presence` (`set` | `missing` |
`unknown`), `version` (hosted), and `verifiable`; it never carries a value.

## Why a YAML store, not a `.env` file

forge projects do not use `.env` files, and `forge lint` fails on one.

A dotenv was handed to every host service **wholesale**, so adding a line
to it made that value live everywhere *without touching KCL*. That made
the untracked file the cheapest place to put anything — and the result
was predictable: service URLs, client IDs and issuer names migrated out
of version-controlled KCL into a file nobody could review, diff, or
reproduce on another machine.

Two properties fix that, and both matter:

1. **Declaration-scoped injection.** A value reaches only the services
   that declare it, so putting config in the store accomplishes nothing —
   this is the property that actually closed the hole.
2. **YAML, not `KEY=value`.** Real quoting and multi-line values, so a PEM
   key or a JSON blob round-trips without escaping games.

If a value is the same on every developer's machine, it is not a secret:
put it in `deploy/kcl/<env>/config.k`, or declare it as a
`RenderedSecretKey { from = "literal" }` (in `Bundle.rendered_secrets` or a
`RenderedSecrets` provider) where it stays in git.

With **ExternalSecrets**, forge **never sees values** and is inert on
its side — it renders nothing and validates nothing. k8s references
pre-existing Secrets (External Secrets Operator / sealed); host &
external runtimes self-fetch (workload identity / ambient env). The
marker just makes the contract explicit.

## Config vs secret — the split

Keep these straight or you'll leak runtime-specific values into the
provider:

- **CONFIG** — values that carry no credential (`LOG_LEVEL`,
  `PORT`, an OTLP endpoint). These project inline, and vary per env via
  `deploy/kcl/<env>/config.k`.
- **SECRET** — anything that carries a credential: a password, a token,
  a key, **or a URL with credentials embedded in it**. These project as
  a Secret reference and get their value from the provider.

A connection string **is a secret** — do not try to split it into a
non-secret template plus a secret password. The value that varies per
env and the value that must stay out of git are the SAME string here, so
splitting buys nothing and costs a fragile interpolation: k8s expands
`$(VAR)` (not `${VAR}`), only from vars defined **earlier in the same
container's env list**, so the composed URL silently renders with a
literal `${DB_PASSWORD}` in it more often than it works. Mark the whole
field `sensitive` and give each env its own Secret value.

## Supersedes

The per-service `HostDeploy.secrets_file` dotenv. It still works as a
**host fallback** when no bundle `secret_provider` is declared, but the
bundle provider is the single-source model now — prefer it.

## Gotchas

- **FileSecrets is local-only.** It is rejected outside dev/e2e (a KCL
  check), and the `isLocalCluster` guard makes forge refuse to render
  plaintext Secrets into anything but a local cluster (k3d / kind /
  docker-desktop / minikube / rancher-desktop / colima / orbstack). Use
  `ExternalSecrets` for staging/prod.
- **Fail-fast on missing refs.** A declared `secret_ref` with no value
  in the store aborts `forge env up` / `forge env deploy` — no more silent
  feature-disable.
- **`optional: true` is the ONE exemption.** Some credentials are
  optional by design — the classic case is a tier's off-switch, where a
  config block is documented as "with either unset the reconciler is not
  registered". Every non-sensitive field in such a block projects as `""`
  and unset is fine; without this annotation the sensitive one would be a
  hard start-up requirement in every environment that deliberately left
  the tier off.

  ```proto
  string admin_password = 99 [(forge.v1.config) = {
    optional: true          // absence is intended; skip the pre-flight
    sensitive: true,
    env_var: "MANAGED_DATABASE_ADMIN_PASSWORD"
  }];
  ```

  It is opt-in, and deliberately so: an unset credential and a
  deliberately-absent one are indistinguishable in the store, so forge
  cannot infer which it is looking at. Reach for it ONLY when the app
  genuinely tolerates the absence — the alternative people invent is a
  placeholder value, which puts a real-looking credential in the store
  and defeats the check for every genuinely-missing secret after it.

  `required: true` and `optional: true` together are refused at generate
  time rather than resolved by precedence.

  All of this is the DEPLOY pre-flight, over the refs a workload declares
  in `config_secrets`. At runtime `sensitive` does not make a field
  required: a field the process cannot start without says `required: true`
  (sensitive or not), and the loader fails on it naming the env var.

  The same exemption exists on the escape hatch. A hand-written
  `forge.EnvVar` takes `secret_optional = True` — the canonical case is an
  integration that is OFF until an operator registers it (a Slack app's
  signing secret, an OAuth client secret): the service reports the
  integration unavailable when the var is empty, so the secret must not
  block every deploy of every env that has not set it up.

  ```kcl
  forge.EnvVar {name = "RELIANT_SLACK_SIGNING_SECRET", secret_ref = "reliant-slack",
                secret_key = "signing_secret", secret_optional = True}
  ```

  It does both halves, which is what makes it safe: the deploy and
  `forge env up` pre-flights skip it, AND it renders as
  `secretKeyRef.optional: true`, so kubelet starts the pod with the var
  unset instead of failing `CreateContainerConfigError` on a Secret that
  does not exist. Exempting only the pre-flight would trade a clear
  deploy-time refusal for a crash-looping pod.
- **The store is keyed by env-var NAME.** The YAML KEY must match
  `EnvVar.name` (not `secret_ref` / `secret_key`), and must be a valid
  env-var name — forge refuses to load a store containing a key that
  could never be injected.
- **Multi-line values round-trip.** A PEM key or JSON blob is written as
  a YAML block scalar; values with colons or leading spaces are quoted.
- **Undeclared values are inert.** A key no service declares is never
  injected. `forge secret list --env <env>` reports these — they are usually a
  typo, or config that belongs in `config.k`.
- **ExternalSecrets is inert.** It renders nothing and validates
  nothing — it only declares that values live outside forge. It has
  **no** provider/auth fields (no aws-secrets-manager / vault keys).
- **env_file wins.** Under compose/external, an explicit `env_file`
  overlay overrides the provider-supplied values.

## Running your own secret store

`Bundle.openbao = forge.OpenBao {}` runs a secret store (OpenBao) the env owns,
and a `forge.ExternalSecret` with `source = forge.StoreSource {...}` reads from
it through the External Secrets Operator. Load `secrets/openbao`.

Other clusters reach the store through `public`: `forge.OpenBaoPublic {host = ...}`
publishes it on a Gateway with a path allowlist, re-encrypted TLS, limits and a
trusted `X-Forwarded-For`. Load `deploy/openbao-public`.
