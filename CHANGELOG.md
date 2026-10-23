# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **An identity line in `forge env status` and `forge env deploy` (FI7).** A
  hosted env that declares `forge.ManagedIdentity` shows `identity: ready /
  provisioning / degraded / failed: <reason>`, read from its control plane's
  `IdentityService/GetIdentityStatus` (status and `--json`; it never changes
  `env status`'s exit code). The deploy readiness wait (and `env status --wait`)
  waits on it: a release whose rollout succeeded is `waiting_on_identity` until
  the identity is ready. Still provisioning at the deadline is the timed-out
  exit; failing or degraded at the deadline, or a refused declaration at once,
  is exit 1. An identity forge cannot read is reported and never holds a deploy.
- **`forge cloud identity export <env>`** exports the instance on demand
  (`RequestExport`) and prints a short-lived signed download URL, a credential.
  `users` gains each user's last sign-in and the total count; `status` names
  why there is no instance and the owner/sharer environments by name, and each
  delivery's last write.

- **`forge.ControlPlaneSecret` can be delivered through the store (SF14).**
  `delivery = "store"` (with `store = forge.HostedStore {org, env_id}`) makes the
  control plane write the value into the env's secret store and the External
  Secrets Operator deliver it, as a **floating** ExternalSecret: no version pin,
  because a platform-issued token is renewed in place and its consumers re-read
  the file the kubelet refreshes. One store name, `RELIANT_OTEL_HEADERS`
  (`authorization=Bearer <jwt>`), carries the value; the Secret's `token` key is
  derived from it by an ESO template, so the two keys cannot disagree.
  `refresh` (default `15m`, at least `5m`) sets the re-read interval. The render
  record gains `delivery` and `source`; the shape's
  `control_plane_secrets[].delivery` (bundle feature `shape-store-secrets`) and
  its `store_secrets` binding tell the control plane to write the store and
  derive the exact-path read policy. `release.ControlPlaneSecretStoreName` is the
  Go spelling of the store name, pinned to the render. The default,
  `delivery = "push"`, is unchanged. Not a deploy prerequisite either way.

- **`forge lint --secrets`: the platform-secrets lane (SF12).** For each env that
  uses the model (a store-backed `forge.ExternalSecret`, `forge.OpenBao` or
  `forge.ExternalSecretsOperator`; a project that declares none is not rendered),
  it judges what the schema cannot see across a whole env, with file:line in the KCL
  and the escape in every message. Errors gate: `secrets-cluster-kind` (no
  ClusterSecretStore, ClusterExternalSecret, PushSecret or generator, nor their
  CRDs); `secrets-es-policy` (creationPolicy Orphan or Merge, deletionPolicy Retain,
  a namespaced store, explicit keys, a refresh of at least 5m, a target named for
  itself); `secrets-key-coverage` (every Secret a workload reads is produced by an
  ExternalSecret, ControlPlaneSecret, rendered Secret, Certificate or CloudNativePG
  cluster, and lists the key read: ESO rebuilds an Orphan Secret from the declared
  keys only, so an undeclared key vanishes on the next sync); `secrets-immutable-pin`
  (an immutable store name is read at exactly version 1 by every source);
  `secrets-out-of-band-reason` (the reason must explain, not restate "manual");
  `openbao-wildcard-read` (the rendered access document may not grant a glob read of
  a value without a real reason); `secrets-single-owner` (two declarations producing
  one Secret). `secrets-escrow-in-cluster` warns when OpenBao's seal key and
  recovery output are escrowed in the cluster they protect. `secrets-unreferenced-key`
  (info) lists declared Secrets and keys no workload reads: the orphan finder. The
  render now also records each `required_secrets` entry's `out_of_band_reason`,
  which the lane reads. Waive one finding with `# forge:lint-disable-next-line <rule>: <why>` or
  dial it in `lint.rules`.
- **Alerts fire locally: `forge env up` closes the alerting loop.**
  - Beside the local ClickStack, a scaffolded dev env now runs two new pieces:
    - an `alerts` host workload, `forge observe alerts run`, which evaluates the env's `forge.Alerts` continuously with `for` applied;
    - a compose `alertmanager` (the v0.28.1 digest the control plane runs).
  - The Alertmanager's configuration is rendered by `forge/pkg/alerting/amconfig`, the control plane's own renderer, as org `local` with the env name as its env id. So a route reaches the same receiver locally as in production.
  - The always-present `reliant-inapp` receiver delivers to the evaluator, which writes one line per alert to `.forge/logs/<env>/alerts.log`.
  - New KCL:
    - `forge.Alerts.receivers` (`forge.AlertReceiver`: webhook, slack, pagerduty, autopilot), the receivers of an env forge delivers itself. Their secrets are secret-store keys, written as files.
    - `forge.Observability.alertmanager_port` / `alerts_port`.
  - A host workload's `command` that starts with `forge` runs the forge that launched the env. Migration: `migrations/v0.1.47/local-alerting`.

### Fixed

- **A mock role may embed a role another file of its package declares.** The
  mock generator read only the file it generates from (`contract.go`, or a
  marked `deps.go`), so `type Repo interface { Reader; ... }` with `Reader` in
  `roles.go` produced a `MockRepo` without `Reader`'s methods, and generate's
  build validation failed with "does not implement". The embed is now resolved
  from the type-checked package, the way #1109 resolves another package's,
  with the package's own types unqualified and the sibling file's imports
  carried over.
- **`forge fix connect-errors` sees an As-style helper as a `*connect.Error` reader.**
  - It recognised `errors.As` itself, but not testify's `require.ErrorAs(t, err, &ce)` / `assert.ErrorAs`, a suite's `ErrorAs` method, or a project's own helper: each takes the target as `interface{}`. A handler package whose only reader was one of those got bare kinds, and its tests failed (reliant's `internal/handlers/approval`: four tests).
  - Any call handed a `**connect.Error` is now a reader, so that package keeps `svcerr.ToConnect(…)` and the report names the call.
- **`forge observe alerts check` and `forge observe profile` reach the ClickStack `forge env up` started.**
  - env up runs each env's compose services in their own project (`forge-<id>-<env>`). Both commands exec'd into the default project, so they reported a running ClickStack as not running.
- **`docker compose up <service>` interpolates the whole compose file with every workload's declared env.**

  - Bringing up `otel-profiles` (which depends on `clickstack`) with only its own variables recreated `clickstack` on the file's default ports. On a machine running a second stack, the bind failed.

- **`forge/pkg/alerting/amconfig`: the Alertmanager configuration renderer.**
  Moved verbatim from the control plane, which renders every org's routing
  with it, so a local stack can render with the SAME code: one route per org
  matched strictly on `reliant_org_id` with a blackhole default, per-org
  receiver names, secrets as `*_file` references only, autopilot receivers as
  continue:true observers. `TestNoRouteDeliversAcrossOrgs` runs over
  Alertmanager's real routing tree. New: `Settings.AllowHTTPWebhooks` (false
  by default) lets a local stack, whose receivers are the developer's own
  machine, use plain-http webhooks; a renderer serving several orgs never sets it.

- **`forge.OpenBao` renders (SF6).** `Bundle.openbao = forge.OpenBao {}` now
  lowers to a `forge.dev/v1alpha1 OpenBao` record that `pkg/deploy/openbao`
  expands into the secret store and what converges it. Defaults run: Raft x3 on
  PVCs with `retry_join` auto-join, TLS from a namespaced self-signed
  cert-manager CA, a static seal, audit to stdout and a file, a PDB and zone
  spread. `OpenBaoPostgres` renders a Deployment with the cloud-sql-proxy
  sidecar over Workload Identity. Two content-addressed Jobs run one embedded
  `converge.sh`: a pre-rollout **widen** (the union of the applied and new
  access document) and a post-rollout **converge** (initialise, write the
  recovery Secret before converging anything, apply the document from
  `pkg/secretaccess`, narrow what it no longer contains, revoke root).
  `consumers` become exact-grant roles; `delegated` gets the access-admin policy
  and no KV capability. A plaintext listener needs a `transitional_reason`, a
  glob `read` needs a `wildcard_read_reason`, and both are listed in the
  render's `openbao.warnings`. The release shape now records the store-backed
  secrets, generated names, pins and both components (`forge env shape`).
  Left for later: the bootstrap source and recovery escrow (hook seams are in
  the Job), the `public` endpoint, the Raft snapshot CronJob (refused if
  declared), and the OIDC writer.

- **`forge secret` writes and lists store-sourced secrets, and `forge env secrets
verify|adopt` migrate a Secret onto ESO (SF10).** A name a store-backed
  `forge.ExternalSecret` (`forge.StoreSource`) reads or generates now lives in that
  ExternalSecret's store, whatever the env's `secret_provider` is:

  - `forge secret set` / `list` reach a `HostedStore` through the control plane's
    write-only API, a `GCPSecretManagerStore` with Application Default
    Credentials, and a self-run `OpenBaoStore` by signing in as the
    `forge-secret-writer` service account with a ten-minute token minted from
    your own kube context (design N2): no credential is stored, and cluster
    access is all it needs. `--store` picks one when two stores declare a name.
  - `forge secret import` copies existing values in, create-only (check-and-set
    0; for Secret Manager, "no usable version yet"), so a re-run can never
    overwrite a value that was rotated since. `--from <ns>/<secret> --map
key=NAME` reads a cluster Secret with your kube context; a NAME with a value
    on stdin or `--from-file` imports one. `--to-bootstrap [--project]` writes a
    `BootstrapSecret`'s Secret Manager container. `--dry-run` reads and reports.
  - `forge env secrets verify <env> [<secret>]` compares a live Secret with the
    store's copy per key, and prints `match` / `no-match` and which side is
    missing: by default against `<name>-eso-shadow` (a source with `shadow =
True`), or `--against gsm:<project>` for the Secret Manager parity check.
  - `forge env secrets adopt <env> <ns>/<secret>` strips the stale
    `kubectl.kubernetes.io/last-applied-configuration` annotation (a full copy of
    the applied values) and asserts nothing owns the Secret by ownerReference.

  No command prints a secret value or any digest of one, and none takes a value
  as an argument: values come from stdin, `--from-file`, or a store or cluster
  Secret you already hold. Both are pinned by tests that sweep every command's
  output and error for a canary in raw, encoded and hashed form, on success and
  on failure paths where the remote echoes the request.

- **A config message's `buf.validate` rules are checked as it loads.** Field
  rules (a port's range, a positive duration) and message-level CEL
  (`BILLING_PROVIDER=stripe` needs `STRIPE_SECRET_KEY`) on a `proto/config`
  message now fail `config.Load*`, so a root's command refuses a bad value in
  its `RunE`, before `runRoot` binds a port or applies a migration; and
  `config.Root.Validate` reports every violation over a rendered environment.
  Each names the field's path, its variable and flag, and its value unless the
  field is sensitive. `config.Validate` runs them too. A config that declares
  no rules loads exactly as before.

- **A store-backed `forge.ExternalSecret` now renders the External Secrets Operator
  objects that sync it (SF5).** For each (cluster, namespace) holding one:
  a `reliant-secrets` ServiceAccount, a Role + RoleBinding letting ESO mint that
  service account's token and no other (`resourceNames`, matching ESO's
  `rbac.serviceAccountTokenCreate: false`), one namespaced `SecretStore` per store
  (the `vault` provider with JWT auth over a projected token for `HostedStore` and
  `OpenBaoStore`; `gcpsm` over ESO's own Workload Identity for `GCPSecretManagerStore`),
  and the `ExternalSecret`: one `remoteRef` per store name, `source.pin` as the KV-v2
  `version` (rotation is a reviewed pin bump), `source.templates` as an ESO v2
  template, `creationPolicy` Orphan or Merge with `deletionPolicy: Retain` so a store
  outage or a Flux prune never removes a synced Secret. `shadow = True` syncs to
  `<name>-eso-shadow` for the migration compare. The login role is the one
  `pkg/secretaccess` derives, pinned by a test. `StoreSource` gains `cluster` (the
  context it lands on; default the primary cluster); `HostedStore` gains `org` and
  `env_id`, the control plane's ids that address its values. Refused at render, each
  naming its escape: a HostedStore without ids, an OpenBao no other cluster can reach,
  a GSM store with no ESO Google identity, a template calling a function ESO lacks or
  that changes every sync, and one namespace configuring one store kind two ways.
  Nothing renders for a Bundle with no `source`.

- **A drained Connect stream parked in `Receive` actually ends, and handlers can
  stop their own readers: `serverkit.AbortReceive(ctx)`.** A server-side
  `Receive` reads the request body and no context wakes it; Connect closes the
  body only after the handler returns. So the drain, which ended streams by
  cancelling their contexts, never ended a bidi handler blocked in `Receive` (it
  held the shutdown to the end of the budget), and a handler that read on a
  goroutine of its own could not stop it before returning — leaving it in
  `Receive` after the handler returned, which Connect forbids and which races
  otelconnect's span bookkeeping. `Streams.Install` (every `NewHTTPServer` /
  `NewListener`) now serves each request with an abortable body; a stream the
  drain ends has its reads aborted right after its context is cancelled (the
  read fails with `svcerr.ErrServerDraining`, so the client contract is
  unchanged), and `serverkit.AbortReceive(ctx)` / `CanAbortReceive(ctx)` let a
  handler end its own reads (`ErrReceiveAborted`). HTTP/2 closes the body;
  HTTP/1.1 sets a read deadline, because its `Close` waits for a pending read.
  The response is untouched, and unary routes are unaffected. Skill:
  `services/shutdown`.

- **`svcerr.WrapOr(err, fallback)`: keep a verdict if there is one, otherwise
  say what failed.** A classified `err` (`svcerr.IsClassified`: an svcerr kind,
  a context cancellation or deadline, or a `*connect.Error`) is `Wrap(err)`;
  anything else is `Wrap(WithCause(fallback, err))` — the client reads the
  fallback's code, message, reason and class, and `err` is the logged cause.
  It is the one spelling for a collaborator's error, replacing
  `connect.NewError(connect.CodeInternal, err)` (publishes `err.Error()`,
  flattens a NotFound) and `svcerr.Internal("…")` with `err` dropped (a deadline
  becomes a server fault, the cause never logs). `pkg/crud` uses it in place of
  its private copy, with no change to what it returns. A remote Connect
  client's error is a `*connect.Error` and would pass through, so never
  `WrapOr` one: wrap it in your own verdict with `WithCause`.

- **Boot `/readyz` names the graph node boot is waiting on.** A composition
  root's boot probes used to answer `503 {"status":"not_ready"}` with no checks
  for the whole of graph construction, so a constructor that blocked — a
  provider waiting on another role's migrations, a dial that retries — left a
  stuck pod with no signal. `graphkit.Builder` now reports each node's
  construction to a `graphkit.Progress` (`ReportTo`; one method,
  `Constructing(node) (done func(error))`), which `*serverkit.BootProbes`
  satisfies, and the generated graph's `build` reports to the boot probes on
  `Graph.Ctx`. During boot `/readyz` lists every node already built
  (`{"name":"pool","ok":true}`, completion order), then each under
  construction (`{"name":"db_repo","ok":false,"detail":"building"}`, outermost
  first), then the dependencies serverkit waits on (`schema`, the database).
  Check names are graph node names. A node constructing for more than 10s is
  logged (`boot: still constructing a graph node`, naming the innermost node
  and the path that reached it) every 10s, and again when it finishes.
  Post-build readiness is unchanged. Existing projects: `forge generate`.

- **`forgeconv-handler-connect-error`: every `connect.NewError` in a handler
  package is a warning.** A `*connect.Error` is never redacted — svcerr's
  interceptor passes it through as built — so `connect.NewError(code, err)`
  publishes `err.Error()` and overrides any code `err` already carried. Each
  finding says which kind it is: a fixed message (`forge fix connect-errors`
  rewrites it with the same code and message), an error value (use
  `svcerr.WrapOr`), or `nil`. Generated and test files are skipped. It warns
  by default so no project goes red on the bump; set
  `lint.rules: {forgeconv-handler-connect-error: error}` once migrated.
  **Changed with it:** `forgeconv-no-handler-error-mapping` no longer skips a
  file that imports svcerr. One `svcerr.Wrap` in a file — or a codemod adding
  the import — used to silence every mapper beside it without fixing one; a
  project whose mappers were hidden that way sees them as warnings now.

- **The release shape records store-backed secrets (`pkg/release`, SF3).** A shape
  gains four optional fields a control plane reads to serve a store-backed
  `forge.ExternalSecret`: `store_secrets` (per cluster, namespace and store, the
  names the namespace reads, their version pins and `immutable` marks),
  `generated_secrets` (the values to mint when the store has none, carrying
  `pkg/secretgen.Spec`'s parameters; `ShapeGeneratedSecret.Spec()` converts),
  `openbao` and `external_secrets_operators` (which clusters declare the
  components). `Shape.StoreBindings(store)` returns the `secretaccess.Binding`s of
  one store, so `secretaccess.Derive` gets exact-path policies from a shape alone.
  `release.ProjectStoreSecrets` projects the render's records (`required_secrets[]`
  with a `source`, `openbao`, `external_secrets_operators[]`) into them,
  deterministically and refusing ambiguity (a Secret that reaches no cluster, two
  sources that disagree on a pin or a generation); `bundle.ShapeInput.Store` carries
  the result into the one shape projection. A shape never carries a value: a
  generated secret is a recipe and a pin is a version. The fields are a bundle-document
  feature (`shape-store-secrets`, `BundleFeatureStoreSecrets`), withheld from a control
  plane that does not name it, and omitted when empty, so every shape recorded before
  them encodes and hashes exactly as it did.

- **`forge fix connect-errors`: hand-built Connect errors onto svcerr, without
  changing a code or a message.** It rewrites every `connect.NewError` whose
  client-visible code and message an svcerr spelling reproduces exactly —
  `errors.New("chat not found")` under `CodeNotFound` → `svcerr.NotFound("chat")`,
  other NotFound prose → `svcerr.WithMessage(svcerr.ErrNotFound, …)`,
  `fmt.Errorf(f, args…)` without `%w` or an error argument →
  `svcerr.<Kind>(fmt.Sprintf(f, args…))` — and, in the tests of a package that
  now returns bare kinds and of its importers, `connect.CodeOf` →
  `svcerr.Code`. A bare kind is written only in a handler package, where the
  context is `error` and nothing reads a `*connect.Error` in process in the
  package, its tests or an importer's code; everywhere else (interceptors, `*connect.Error`
  results, a package such a reader sees) it is `svcerr.ToConnect(…)`, and a
  re-run drops the wrapper once the reader reads `svcerr.Code`. Every site it
  cannot rewrite exactly — an error value (`svcerr.WrapOr` is the reviewed
  fix), nil, a computed message or code — is listed with its reason. It is
  type-checked (`go/packages`), idempotent, `--dry-run` prints each rewrite as
  `file:line: old → new`, and its proof is a corpus whose real-client wire
  output is identical before and after. Over reliant: 902 sites rewritten (334
  to bare kinds, 568 kept a `*connect.Error`), 71 test assertions, 341 sites
  listed, and its build, vet and handler tests pass unchanged. On request
  only (`forge project upgrade` does not run it); the playbook is
  `forge skill load migrations/v0.1.47/connect-errors`.

- **The schema for platform secrets: `forge.OpenBao`, `forge.ExternalSecretsOperator`
  and store-backed `forge.ExternalSecret`s (declarations only, nothing renders yet).**
  Two opt-in components on the Bundle, like `observability` and `identity`:
  `openbao = forge.OpenBao {}` (Raft x3 by default, TLS listeners, a generated
  static seal key escrowed through `bootstrap_source`, audit on, a service-account
  writer) and `external_secrets_operators = [forge.ExternalSecretsOperator {cluster}]`
  (one hardened, cluster-wide ESO per cluster; a second for one cluster is refused).
  A `forge.ExternalSecret` gains `source = forge.StoreSource {store, data, templates,
generate, pin, immutable, refresh, creation_policy, shadow}` over a `HostedStore`,
  `OpenBaoStore` or `GCPSecretManagerStore`, and `out_of_band_reason` for the ones
  that stay out of band once any is store-backed. `forge.BootstrapSecret` and
  `bootstrap_source = forge.GCPSecretManager {project} | forge.OutOfBand {}` name the
  values the store needs before it can serve a read. `forge.GeneratedSecret` gains
  kinds `password`, `ec_p256_pkcs8`, `rsa_2048` and `ec_p256_selfsigned` (the closed
  set `pkg/secretgen` implements); `FileSecrets.generate` stays `random` only.
  Every refusal names its escape: a plaintext listener needs `transitional_reason`, a
  glob `read` grant needs `wildcard_read_reason`, a pin above 1 on an `immutable`
  name is refused. A Bundle that declares none of this renders byte-for-byte as before.

- **What the owned `runRoot` reads is every root's config reach.** `generate`
  reads `cmd/<bin>/cmd/serve.go` the way it reads compose.go: `cfg` from
  `config.LoadReach`, `root.Configs.<Msg>`, and each config value handed to a
  function of the command tree, by the same selector rules. A setting the
  process policy reads under its own name (an `observability` block's
  `OTEL_*`, `PROMETHEUS_ENABLED`, a `health_port`) is now in `ROOTS`, in
  `pkg/config` `Roots()` (so `Validate` no longer calls it unreached) and in
  what `LoadReach` demands; before, only forge's own serve-field names counted.
  A database field still counts only for a root that opens the database, and
  a tree with no runRoot keeps the scaffolded serve fields.
- **`//forge:group <path>` declares a group command's help.** A comment of its
  own in `internal/app`, read by the root doc's rule (first paragraph the
  Short, the rest the Long), replaces roots_gen.go's generic "The server roots"
  on the group command above `server api` / `server gateway`. A path no root
  runs beneath, or a root's own command, is a generate error.
- **`forge cloud identity invite|reset --return-code`** hands the one-time
  password-set code back (with the env's `/auth/reset` link filled in) for you
  to deliver, the only way to invite or reset where the environment declares no
  `smtp`: the control plane refuses both there rather than send nothing.
  `status` prints `Mail: none|smtp` (`mailDelivery` in `--json`). `reset` now
  says it emailed a link, and `invite` no longer claims an unsent email
  succeeded. The `deploy/managed-identity` skill describes every verb, the
  `identity:read` / `identity:manage` scopes (machine credentials carrying them
  are accepted; a Reliant session is elevated on demand) and the code flow; it
  had said machine credentials were refused and that an invitation's code could
  not be redeemed by `/auth/reset`.
- **`pkg/secretaccess` and `pkg/secretgen`: the pure halves of store-backed
  secrets.** `secretaccess.Derive` maps `{scope, clusters, per-namespace secret
names}` to OpenBao JWT auth mounts (`jwt-<cluster>`), roles (one per cluster
  and namespace, bound to the namespace's `reliant-secrets` service account and
  the cluster's audience, issuing 10 minute batch tokens) and ACL policies that
  grant `read` on exact paths only: no globs, no list, no metadata, unless a
  `WildcardRead` carries a reason. Output is deterministic, so forge's converge
  Job and a hosted control plane derive identical documents; `Widen` and
  `Removed` implement widen-then-narrow around a promotion. `secretgen`
  generates the declared kinds (`random`, `password` with one character of every
  class, `ec_p256_pkcs8`, `rsa_2048`, `ec_p256_selfsigned`) and `Mint` writes
  them create-only, refusing to complete a half-written key pair. A docker smoke
  against OpenBao 2.6.2 (skipped under `-short`) proves JWT roles accept
  `token_type=batch` and that the derived policy reads exactly the declared
  names.

- **`forge cloud identity` administers users, not just reads them.** New verbs,
  each one rpc of `controlplane.v1.IdentityService`: `set-roles <env> --user
<id|email> --role a --role b` (or `--none`), `reset`, `lock`, `unlock`,
  `delete` (asks first; `--yes` skips the question; a closed stdin refuses) and
  `revoke-sessions`. `--user` takes a user id, or an email matched exactly
  (case-insensitively) through `users`; no match, or two, is a refusal. The
  scopes are `identity:read` for reads and `identity:manage` for every verb that
  changes a user. forge presents neither up front: a Reliant session or `forge
login` credential is traded for the scope the control plane's 403 names, for
  an hour, exactly as `deploy:read` and `secret:write` are. An explicit
  each one rpc of `controlplane.v1.IdentityService`:
  `set-roles <env> --user <id|email> --role a --role b` (or `--none`), `reset`,
  `lock`, `unlock`, `delete` (asks first; `--yes` skips the question; a closed
  stdin refuses) and `revoke-sessions`. `--user` takes a user id, or an email
  matched exactly (case-insensitively) through `users`; no match, or two, is a
  refusal. The scopes are `identity:read` for reads and `identity:manage` for
  every verb that changes a user. forge presents neither up front: a Reliant
  session or `forge login` credential is traded for the scope the control
  plane's 403 names, for an hour, exactly as `deploy:read` and `secret:write`
  are. An explicit
  `--token` is used as given. `invite` is unchanged.
- **`forge project new --verify`** keeps the compile check at the end of the
  bootstrap generate (see Changed).
- **A CHECK that mirrors a Go set declares it, and `forge lint` keeps the two
  equal.** In a migration, write
  `COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS 'forge:closed-set accesstoken.AllScopes'`.
  The new closed-sets lane, which runs by default (`--closed-sets` runs it
  alone), finds the CHECK the migrations last defined. It reads
  `col <@ ARRAY[…]`, `col = ANY (ARRAY[…])` or `col IN (…)`, with or without
  casts, and compares the values with the named set. A mismatch fails the
  lane, naming the missing and extra values and printing the forward
  migration. `forge db migration new --closed-set <constraint>` writes that
  migration. The lane reads migration text, so it
  needs no database and runs on the `-short` tier. That is the tier where a
  forge bump adding `identity:read`/`identity:manage` went unnoticed until it
  turned a project's main red.
  - forge exports `accesstoken.AllScopes` and `accesstoken.AllResourceKinds`.
    A project names its own set as `<import path>.<Var>`; forge type-checks
    that package and executes none of it.
  - postgres drops a constraint's comment along with the constraint, so the
    lane also reports a CHECK that was replaced without re-issuing its
    declaration.
  - `forge:closed-set` is part of the catalog-marker vocabulary, so
    `forge generate` and `--column-markers` reject a misspelled marker or one
    on the wrong constraint kind. `schemadef.CheckConstraint` gains `Comment`.
- **A project's own transaction openers are judged by
  `forgeconv-tx-lock-under-serializable`.** A wrapper that takes an
  `orm.TxPolicy` and runs the transaction itself (reliant's `Repo.RunTx`,
  which keeps its own retry ladder) declares itself with
  `//forge:tx-opener level-arg=<n> [body-arg=<n>]` on its func declaration;
  the lint then finds a lock under SERIALIZABLE in its calls exactly as in
  `RunTx`'s, matched by name and arity across the project, detached when it
  takes no body. A marker that does not parse, or sits anywhere but on a func
  declaration, is an error. The marker is in the `--go-markers` registry.
  `orm.ResolveTxPolicy` / `orm.ResolveDetachedTxPolicy` validate such an
  opener's policy as `RunTx` / `BeginDetached` do and return the full
  options (level, access mode, attempt budget), the error naming the line
  that called the opener.
- **The scaffolded sign-up says what a password needs.** Sign-up used to answer
  "That password does not meet the requirements." and never name one. The
  login broker now serves the identity provider's own complexity policy at the
  unauthenticated `GET /auth/password-policy`
  (`{min_length, has_uppercase, has_lowercase, has_number, has_symbol}`, read
  from Zitadel's `GET /v2/settings/password/complexity` with the broker's
  token, cached for a minute), and a rejected password comes back
  `400 {error, code: "password_policy", unmet:[...]}` naming **every** rule it
  breaks (the provider reports only the first). The sign-up and reset forms show the
  checklist up front and tick it as the person types; the server stays the
  authority. New in `pkg/devidp`: `Client.PasswordPolicy`,
  `PasswordPolicy.Unmet/Describe`, and `PasswordPolicyError.Rule`; `Register` now returns a
  `*PasswordPolicyError` for a weak password. Existing projects keep their
  owned `internal/app/login_broker.go` and screens; copy the new handlers from
  a fresh scaffold.
- **Self-serve password reset in the scaffold.** The broker mounts
  `POST /auth/forgot` and `POST /auth/reset`, and the Next.js and Vite frontends get
  static-export-safe `/auth/forgot` and `/auth/reset?userID=&code=&orgID=`
  pages, so the reset links the control plane produces have somewhere to land.
  `/auth/forgot` never reveals whether an address has an account and is limited
  per address and per client IP. The forgot screen says plainly that the email
  is sent only if the app's identity declares `smtp`. `pkg/devidp` gains
  `SendPasswordResetLink`, `MintPasswordResetCode` and `FindUserByID`. There is
  deliberately no scaffolded invite flow.
- **ERROR log records carry the inputs an issue is grouped by.** Every record at
  ERROR or above that goes through `observe.NewLogHandler` (the stdout line and
  the OTLP logs bridge alike) now has `code.function.name`, `code.file.path` and
  `code.line.number` from the record's PC, and, when it carries an error,
  `exception.type` (`%T`) and `exception.message` (1 KiB bound) of the
  _innermost_ error: svcerr's withheld cause first, then `Unwrap` to the end,
  the first branch of a multi-error. Below ERROR nothing is added and nothing is
  allocated. The PC is the logger's, so a call into `Handler().Handle` with a
  caller-built record (Temporal's slog adapter, Sentry ELECTRON-CM) names that
  caller instead of a frame inside the handler chain.
- **`observe.LogPanic`** logs a recovered panic with `exception.stacktrace`,
  `exception.type`/`message` and the PANICKING frame as its call site, not the
  recover site. `RecoveryInterceptor` (unary and stream), `RecoverMiddleware`
  and `middleware.HTTPStack`'s recovery use it; the old `stack` attribute is
  now `exception.stacktrace`.
- **`observe.ErrorGroup(ctx, name)`** returns the opt-in `error.group`
  attribute, the escape hatch for pinning a record to a named issue.

### Changed

- **`forge project new` applies the feature graph itself.** The scaffold now
  resolves its feature set with the loader's own rule
  (`FeaturesConfig.FollowingRequirements`, the `followPinnedRequirements` the
  loader runs on every load): a `--disable`d feature takes every unpinned
  feature that requires it with it, so nothing is written that the project
  would derive away. This replaces the hand-copied codegen → orm/migrations
  cascade; `--disable codegen` now also turns off frontend codegen, and
  `--disable build` deploy, exactly as their first load always did. A named
  frontend's app is scaffolded whether or not frontend codegen is on (the
  frontend-only shape), and `HasFrontend` (the dev IdP in docker-compose) and
  rescaffold ask the same question as the upgrade lane: is a frontend declared?
- **`forge project new` exits non-zero when the project's own first generate
  fails.** It printed "initial code generation failed", then "✅ Project created
  successfully!" and exited 0, so a script, a CI step or an agent carried on
  over a project that did not generate (the `--disable codegen` shape stayed
  broken that way). The rest of the scaffold still finishes (git init,
  `go mod tidy`) and the files stay; the error says the project is incomplete,
  what is missing, and the `forge generate` that completes it.
- **`tdd.AssertConnectError` (and `TableRPC`'s `WantErr`) read `svcerr.Code`**,
  the code `svcerr.Interceptor` sends, instead of `connect.CodeOf`. A handler
  test calls the `*Service` method directly, so a handler returning a bare
  `svcerr.NotFound("x")` — the documented shape — read as `unknown`. For a
  `*connect.Error` the two agree; the one difference is an unclassified error,
  which now asserts as `internal` (what the client reads) rather than `unknown`.
- **`testkit.NewConnectClient` serves behind `svcerr.Interceptor`**, innermost,
  as production's chain builders do — so the generated `NewTest<Svc>Server`
  answers a handler's bare `svcerr.InvalidArgument("…")` with
  `invalid_argument`, unary and streaming, where it answered `unknown`; and an
  unrecognised error reads `internal server error` there as it does in
  production, instead of its raw text. A test asserting either old answer was
  asserting what no client in production sees.
- **Generated proto mocks return
  `svcerr.Wrap(svcerr.Unimplemented("handler for X not yet implemented"))`**
  for an unprogrammed rpc, instead of a hand-built `connect.NewError`. The code and message are unchanged, the value
  is still a `*connect.Error`, and `errors.Is(err, svcerr.ErrUnimplemented)` now
  holds. Existing projects: `forge generate`.
- **svcerr's `*connect.Error` policy is documented and pinned**: a hand-built
  `*connect.Error` reaches the client exactly as built, wrapped text included —
  redaction applies to what svcerr classifies. The interceptor cannot tell a
  DSN from text meant for the user (a daemon's git rejection), and rewriting
  it would change every project's wire on a forge bump; the fix for
  `connect.NewError(code, err)` is at the call site (`svcerr.WrapOr`). The
  `api` skill says so.

- **`forge lint` fails when a gating lane could NOT run.** A frontend with no
  `node_modules` (or whose install failed), a typecheck with no compiler, a
  `buf.yaml` with no `buf` on PATH, or no golangci-lint that can analyze the
  module now exits non-zero by default. The last line names the lane, the
  reason and the command that fixes it (`npm ci` in the frontend's install
  root, `forge tools install buf`, ...). Before, such a run printed `⚠️ N gating
linter(s) passed; M could NOT run (...)` and exited 0 unless you passed
  `--strict`. That flag also turned every advisory finding into an error, so a
  gate that only wanted "fail when a lane did not run" had to grep the verdict
  text. The same applies to `--quiet`, `--scope`, `--json` (`"ok": false`) and
  `--gate-json`.
  - **Accept a skip by name, with a reason.** Use
    `forge lint --allow-skip 'frontend-typecheck=typechecked in the web job'`
    (repeatable; `lane,lane=reason` names several) or, in forge.yaml:
    ```yaml
    lint:
      allow_skip:
        - lane: frontend-typecheck
          reason: typechecked in the web job
    ```
    A lane is named as the verdict prints it (`frontend typecheck`) or with
    dashes (`frontend-typecheck`). An accepted lane that could not run is listed
    as `SKIPPED (allowed): <lane> — <reason>` and does not fail the run. An
    unknown lane or an empty reason is refused, and a lane that ran and found
    problems still fails. `--skip-frontends` still drops the whole frontend
    lane for a backend-only run.
  - **`--strict` has one meaning: advisory findings become errors.** It no
    longer has any effect on a gating lane that could not run. The one
    could-not-run case it still escalates is the advisory typed-config
    guardrail's, because that report is itself an advisory finding. The same
    holds for a frontend typecheck dialled to `lint.frontend.typecheck: warn`:
    it is advisory, so when it cannot run it warns instead of failing.
  - A lane that `--scope` does not select (frontend lint, graph, and the other
    whole-project lanes) is still reported as `not scoped, so not run` and does
    not fail. The same goes for a frontend outside the scope that could not be
    checked.
  - New `-unavailable` rule ids in `--json`: `buf-lint-unavailable` (it used to
    be `buf`) and `golangci-lint-unavailable` (it used to be `external`).
  - **Migration:** a CI job that passed only because a lane silently did not
    run now fails. You can fix the cause (install the tool, run `npm ci`). Or,
    if the job is meant not to run that lane, opt it out with `--allow-skip
'<lane>=<why>'` or `lint.allow_skip`. A gate that grepped the verdict for
    `could NOT run` to get this failure can drop the grep and trust the exit
    code.
- **A handler's `svcerr` error reaches the client with its Connect code,
  wrapped or not.** `return nil, svcerr.PermissionDenied("…")` used to arrive as
  `code = unknown`, because the constructors build sentinels and connect-go maps
  anything that is not a `*connect.Error` to unknown; only
  `svcerr.Wrap(svcerr.PermissionDenied(…))` worked. `serverkit.NewChain` now
  ends every chain with `svcerr.Interceptor()` (new, in `pkg/svcerr`), which
  applies `svcerr.Wrap` to whatever the handler returned: a typed error gets its
  code, an error already wrapped or a `*connect.Error` passes through unchanged,
  and an error nothing recognises becomes `internal` with
  `svcerr.InternalMessage`. It sits innermost, so audit, logging and metrics see
  the wire error. Writing `svcerr.Wrap` in a handler stays correct. A project
  that assembles its own interceptor list instead of `serverkit.NewChain` adds
  `svcerr.Interceptor()` itself.
- **`observe.Chain` and `observe.DefaultMiddlewares` install the svcerr
  interceptor too.** A project that built its chain with `observe.Chain` (not
  `serverkit.NewChain`) silently kept the old `code = unknown` for
  `return nil, svcerr.PermissionDenied(…)`; nothing failed, and only this file
  mentioned the fix. Both builders now end the chain with `svcerr.Interceptor()`,
  innermost, and `serverkit.NewChain` gets it through `observe.Chain`. Installing
  it yourself as well is harmless: an `svcerr.Interceptor()` passed in `Extras` is
  moved to the innermost position, never run twice (`svcerr.IsInterceptor`,
  `svcerr.WithInterceptor`). The chain is one longer than before, so code that
  indexes into it by position past the extras should account for the new tail.
- **`forge project new` no longer compiles the scaffold before returning.** The
  bootstrap generate skips its final `go build` and deploy-KCL render unless you
  pass `--verify`. That tail was the one CPU-saturating step (40s of the
  pipeline's 63s on a cold build cache), and it re-proved output that forge's
  own e2e corpus compiles on every change; your first `go build` or `forge
generate` runs the same check. `project new` never took a heavy slot and still
  does not: pinned by `TestRunNew_TakesNoHeavySlot`.
  own e2e corpus compiles on every change; your first `go build` or
  `forge generate` runs the same check. `project new` never took a heavy slot
  and still does not: pinned by `TestRunNew_TakesNoHeavySlot`.
- **Transaction isolation has no default; every writable transaction names
  its level.** pkg/orm had two opposite defaults: `RunTx`,
  `RunTxWithOptions` and `BeginDetached` with an unset level began
  SERIALIZABLE (RunTx retrying 40001/40P01 up to seven times), while
  `Client.BeginTx` and `RunTransaction` with nil options began at the
  database's READ COMMITTED, once. Two reliant ports changed isolation
  silently through that gap — `MergeWorktreeCleanupMetadata` relied on a
  `FOR UPDATE` lock, moved from READ COMMITTED to SERIALIZABLE and dropped
  updates under contention. Now `RunTx(ctx, opts, fn)`,
  `RunTransaction(ctx, opts, fn)` and `BeginDetached(ctx, opts)` take an
  `orm.TxPolicy` naming its isolation, which carries its attempt budget —
  the constants `orm.Serializable` (7 attempts), `orm.ReadCommitted` (1
  attempt) and `orm.RepeatableRead`, refined with `WithMaxAttempts` /
  `WithReadOnly` into an `orm.TxOptions` — and an unset level (or budget) is
  rejected with `orm.ErrInvalidTxOptions` naming the call site. One primitive
  rather than `RunTxSerializable` / `RunTxReadCommitted`: the level is a
  value, so it composes with every opener (RunTx, RunTransaction,
  BeginDetached) and passes through a wrapper unchanged instead of
  multiplying method names, and the retry budget is visible beside the level
  it belongs to. A required argument is as close to a compile error as Go
  allows; the zero value is the runtime error. `RunTxWithOptions`,
  `RunTransactionWithOptions`, `Client.BeginTx` and `orm.IsolationDefault`
  are removed, and `orm.IsolationSerializable` / `IsolationReadCommitted` /
  `IsolationRepeatableRead` are renamed `orm.Serializable` / `ReadCommitted`
  / `RepeatableRead`; `RunTxReadOnly(ctx, fn)` keeps its
  shape (SERIALIZABLE READ ONLY DEFERRABLE — a transaction that cannot write
  has one right level). `RunTransaction` honours the attempt budget like
  `RunTx` and reports an exhausted 40001/40P01 as `svcerr.Aborted`;
  `BeginDetached` cannot retry and requires `MaxAttempts == 1`. A `*Tx`
  someone else began is joined with `tx.Join(ctx, fn)`, which takes no
  level: one spelled at a join would claim an isolation the transaction may
  not have (`Tx.RunTx` remains, for `orm.Context`).
- **Rendered pods set `enableServiceLinks: false`. Behavior change.**
  Kubernetes injects `<SERVICE>_PORT=tcp://ip:port`, `<SERVICE>_SERVICE_HOST`
  and `<SERVICE>_SERVICE_PORT` into every pod for every Service in its
  namespace, and those names collide with typed config: a Service `temporal`
  made `TEMPORAL_PORT` unparseable as an int and CrashLooped a prod gateway
  (`ZITADEL_PORT` was the first outage of the class). Every pod template forge
  renders (Deployment, Job, CronJob, the observability node agent's DaemonSet,
  `forge.TestJWKS`'s Deployment) now opts out. `fw.Workload` gains
  `enableServiceLinks` (default off) to opt one workload back in, Full profile
  only (refused on hosted, the host runtime and on a job with `before`). The
  next deploy rolls every forge-rendered pod once, and a standalone Job gets a
  new spec-hash name. A workload that reads `*_SERVICE_HOST` / `*_SERVICE_PORT`
  must move to DNS or explicit env, or opt in: `migrations/v0.1.47`, section 4.

### Added

- **`forge fix tx-isolation`** (run by `forge project upgrade` on crossing
  v0.1.47): rewrites every pkg/orm transaction call to the explicit form that
  keeps its behaviour exactly — `RunTx(ctx, fn)` → `orm.Serializable`,
  `RunTransaction(ctx, fn)` and `BeginTx(ctx, nil)` → `orm.ReadCommitted`,
  each `orm.TxOptions` literal spelled out — and prints every site. It never
  chooses a different level. Type-aware, so a project's own `RunTx` and
  database/sql's `BeginTx` are untouched, and it works after the forge pin
  has moved (when the old calls no longer compile). A scratch module holding
  every old call shape begins each transaction at the same level, with the
  same number of attempts, before and after (`internal/codemod`).
- **`forgeconv-tx-lock-under-serializable`** (`forge lint --conventions`):
  a SERIALIZABLE or REPEATABLE READ transaction whose body takes a lock —
  `SELECT … FOR UPDATE`, bun's `.For("UPDATE")`, `pg_advisory_xact_lock`,
  `LOCK TABLE` — in a literal or a named constant, in the closure, the
  package function passed as it, or the rest of the function that opened a
  `BeginDetached`. The snapshot predates the lock wait, so the waiter wakes
  into stale data and fails 40001; the fix is `orm.ReadCommitted` (keep the
  lock) or dropping the lock. A warning; an error under `--strict`.

### Fixed

- **A bridged project no longer rebuilds forge for a file no build reads.**
  forge's bridge-skew check counted every dirty path in the linked forge
  checkout as "uncommitted changes the binary was not built from" and handed
  each command to a forge rebuilt from the checkout, keyed on the newest
  edit's mtime. A file that cannot reach a build — the e2e shard's test2json
  log, growing in the checkout's root for the whole run — so rebuilt forge on
  every command, and a re-run of `forge generate` ran a different binary and
  never recorded its fixed point (`TestE2EFixtureCorpusCRUDLifecycle`,
  `CPForgeShaped`, `ZeroService` red on main). Now only a source edit counts:
  a Go build file, or any file beneath a directory holding a non-test Go
  package (where `//go:embed` reaches: templates, KCL, skills). A cached
  bridge build is also keyed on its Go settings (`GOFLAGS`, `CGO_ENABLED`,
  `GOOS`/`GOARCH`, …) and is never replaced once published, so a process can
  no longer have the binary it runs swapped for a differently-built one. The
  e2e shard writes its files under `$RUNNER_TEMP`, outside the checkout.
- **A project without codegen works straight after `project new`.**
  `forge project new --disable codegen` (the frontend-only shape) printed "initial
  code generation failed", and `forge generate`, `go build` and `forge lint` all
  failed on it. Its go.mod required and replaced a `<mod>/gen` module that only
  codegen writes; it got no go.work, so `--link-forge` had nothing to bridge;
  and `cmd/<bin>/cmd/root.go` called `rootCommands`, which only the codegen
  lane defines. Now gen's require, replace, `gen/go.mod` and go.work's
  `use gen` follow one predicate, every kind that imports forge gets a go.work,
  and the scaffold (then `forge generate`) writes a `roots_gen.go` whose
  `rootCommands` returns no roots, plus the `metrics schema` command every
  other service has, so root.go is the same file in every shape. The other
  `--disable` values were already green; `TestE2EProjectNewDisableShapes` now
  scaffolds, generates, builds and lints every one. `--service` with
  `--disable codegen`, and `--frontend` with `--disable frontend`, are refused
  up front: they used to drop the component without a word (the latter left an
  empty `frontends/<name>/`).
- **Every `forge scaffold <noun>` leaves the project lint-green, with no
  `forge generate` after it.** `scaffold frontend` wrote
  `proto/config/v1/<fe>_config.proto` and stopped, so `gen/forge_descriptor.json`
  was stale and `forge lint`'s convention and graph lanes refused it until the
  user ran `forge generate`; `scaffold adapter` did the same to `config.proto`
  (and `config.<Name>Config` did not exist yet), and `scaffold package` left
  `observe_chain.go` unreferenced until generate wrote `middleware_gen.go`.
  All three, and `forge package new`, now end with a full generate run, as
  `scaffold service` and `scaffold rpc` already did; `--no-generate` skips it
  for a round that stages several scaffolds. A failed run keeps what the
  scaffold wrote and exits non-zero. Three templates were red on their own:
  the operator's `doc.go` and the binary's `Run` doc carried space-indented
  doc-comment code blocks gofmt rewrites, and every CRD declared
  `addKnownTypes_<Type>` (revive `var-naming`, now `addKnown<Type>Types`),
  left a doubled blank line in its config and composite types (gofmt), and
  tested the deprecated `ctrl.Result.Requeue` (staticcheck SA1019). New
  guards: `TestE2EScaffoldNounsLeaveLintGreen` scaffolds each noun into a fresh
  project and runs `forge lint` straight after it, and the `-short`
  `TestComponentScaffolds_GoIsFormattedAndNamedForLint` checks what the
  operator, crd, binary, worker and webhook scaffolds write.
- **A fresh vite-spa frontend has zero eslint warnings.** It opened with nine
  `react-refresh/only-export-components` warnings: files that exported a hook,
  a context or a helper beside their component, which defeats Fast Refresh.
  The non-components moved to modules of their own — `useAuth` to
  `src/lib/auth/use-auth.ts`, `useEvent`/`useEventBus` to `src/lib/use-event.ts`,
  `usePasswordPolicy` to `src/lib/auth/use-password-policy.ts`,
  `brandThemeToCssVars` to `src/lib/theme/brand-theme.ts`, `FormFieldContext`
  to the new `form_field_context` library component, and the provider stack
  out of `main.tsx` into `src/providers.tsx`; `isExternalHref` and
  `monoLength` are no longer exported. The rule is unchanged. The frontend
  lint e2e tests now fail on any eslint warning, not only on errors.
- **A fresh `forge project new` passes its own `forge lint` again.** The login
  broker (`internal/app/login_broker.go`) took `ctx` second in
  `writePasswordRejection`, which revive's `context-as-argument` rejects; the
  sign-in page and reset form imported out of `import/order`; the password
  test missed an import-group blank line and, on vite-spa, declared two unused
  parameters; vite-spa's eslint linted the generated `public/config.js`
  (`no-undef` on `window`); and the cli kind's `cmd/<name>/version.go` carried
  a doc-comment code block gofmt rewrites. A new `-short` tripwire
  (`TestFreshScaffold_GoIsFormattedAndTakesContextFirst`) checks every Go file
  the service, cli and library kinds write for both Go rules, and
  `TestE2EFreshScaffoldLintExitsZeroForEveryKind` runs the real `forge lint`
  on fresh cli and library projects.
- **The E2E Suite runs on main again.** Since #1056 its shards skipped on every
  push and nightly call: a job with no `if:` inherits the skip of the
  pull-request-only `changes` job two hops up, though `prepare` succeeded. The
  shards now say when they run, and `internal/ciguard`'s
  `TestNoJobInheritsAnUpstreamSkip` rejects that shape in every workflow.
- **`forge tools install` replaces a GOBIN binary on Windows while something
  has it open.** The atomic install (#1062) renamed the staged binary over the
  old one with a bare `os.Rename`, which Windows refuses while any handle, such
  as a virus scanner or a parallel forge, has the target open. It now retries
  for a moment, as every `atomicfile` write already does (new
  `atomicfile.Replace`).
- **The main-red override judges main's latest run that has a verdict.**
  `scripts/ci-main-red.sh` compared against main's newest completed run, which
  is often one a later push cancelled, and refused #1071 because that run
  (25283f30) had no failed job. It now skips cancelled and unfinished runs. It
  also drops the CRs a native `jq.exe` writes, which made `TestMainRedOverride`
  fail on Windows since #1057.
- **A scaffolded Next.js frontend builds again against `@opentelemetry/api`
  1.10.** That release ships only `.d.mts`/`.d.cts` declarations, the tsconfig
  peer pin was aimed at `dist/index.d.mts`, and Next's webpack read it as
  JavaScript (`Module parse failed: Unexpected token`). Only a `.d.ts` is pinned
  now; a package with none is pinned by its directory.
- **Docs no longer promise mail that is never sent.** The `deploy/managed-
identity` skill and the `IdentitySMTP` schema said that omitting `smtp` routes
- **Docs no longer promise mail that is never sent.** The
  `deploy/managed-identity` skill and the `IdentitySMTP` schema said that omitting `smtp` routes
  mail through a platform relay and that an invitation emails a link. Neither is
  true: with no `smtp`, an instance sends no mail at all. The skill now says so,
  sketches building invites by hand (`InviteUser` + `MintPasswordResetCode` +
  `/auth/reset`), and explains that public `/auth/register` is on by default and
  how to turn it off for an invite-only app.
- **`forge tools install` replaces a GOBIN binary atomically.** It ran
  `go install` straight into GOBIN. When cmd/go cannot rename its build into
  place (a setgid GOBIN, or a `$WORK` on another filesystem), it removes
  the file and rewrites it in place. A parallel generate starting `buf` or
  `protoc-gen-go` at that moment could find nothing, or run a half-written
  binary. The install now builds into a temp directory inside GOBIN and
  renames the result into place.
- **Tracing remediation says `--fix`.** Lint became report-only by default,
  but every tracing finding's remediation, the auto-fix hint, the generated
  `NewInstrumented` comment and the observability skills still said
  `forge lint --tracing` rewrites the code. Run as told, it reported the same
  findings and rewrote nothing (control-plane's 46 contract constructions).
  They now say `forge lint --tracing --fix`.
- **A primary key is server-allocated only when the schema says so.** The ORM
  tagged every integer key `,autoincrement`, so reliant's
  `legacy_credential_copy` (`id integer PRIMARY KEY`, no default, no sequence)
  had its caller-set id left out of the generated `Create` and the insert
  failed NOT NULL; each integer member of a composite key got the same guess.
  The tag now follows the applied schema — `serial`/`bigserial`, an identity
  column (schemadef reads `is_identity`; the catalog reports no default for
  one), or any `DEFAULT` — so a `uuid DEFAULT gen_random_uuid()` key is the
  database's too, where `Create` used to ULID-fill it and fail 22P02. `Create`
  now leaves only a ZERO server-allocated key to the database; a key the
  caller set is inserted as set.
- **No lint rewrite path touches `testdata`.** The Go format pass and its
  report-only diff skip any path with a `testdata` segment (not only a pruned
  directory), the frontend prettier walk now skips it too, and the structured
  driver (`--quiet`, `--scope`) no longer hands golangci-lint `--fix` under
  `forge lint --fix` — the rewrite #1041 removed from the text driver only.
  The reported rewrite of `internal/linter/slowtest/testdata/src/a/a_test.go`
  came from the forge embedded in reliant, pinned before #1018.
- **pgtest's shared server survives a slow moment and a long run.** An
  attaching process probed the cross-process shared embedded server with a
  2s connect and 3s ping and, on a miss, `pg_ctl stop -m fast`ed it — under
  every other process using it, which failed mid-test with 57P01
  "terminating connection due to administrator command" and 57P03 "the
  database system is shutting down". A loaded or just-woken machine misses
  that probe routinely (control-plane's integration lane failed that way in
  four runs in a row). A live postmaster is now retried for 30s on the
  monotonic clock; if it still does not answer and other processes use it,
  the newcomer runs on a standalone server of its own and the shared one is
  left alone. `reapStaleInstances` no longer SIGKILLs (and deletes the data
  directory of) the pool's server while it has a live user, or a standalone
  server while its owner lives, however old its pid file looks: a fan-out
  outlives the 30-minute stale age, and a sleep ages every file by the wall
  clock.
- **`forge lint` skips `node_modules` Go packages in a project reached
  through a symlink.** `go list` reports package directories under the
  caller's `$PWD` spelling (macOS's `/var`, a symlinked home or worktree),
  and the filter compared them with the resolved project path. So nothing
  matched, and golangci-lint and the contract linter linted third-party Go
  under `node_modules` again. A project directory that was itself a symlink
  also skipped the search for `node_modules`. Both sides are now resolved.

### Changed

- **A Go cache budget no trim can reach is reported, not passed over.** The
  trim never deletes an entry used in the last 2h (a running build may need
  it), so a cache that takes in more than its budget in that window cannot be
  held to it — and every pass used to report itself finished while a 20 GiB
  budget held a 152 GiB cache. The pass now prints a `no trim can reach it`
  warning with the size written inside the floor and the fill rate, and
  records it in the GC record's new `over_budget` list (not a failure);
  `forge storage status` shows the same split.
- **Child go commands that set their own GOFLAGS keep `-trimpath`.** Tool
  installs (`forge ci` tools, golangci-lint) and the test suite's builds of
  scaffolded projects replaced GOFLAGS (`GOFLAGS=-mod=mod`, `GOFLAGS=`), which
  dropped the `-trimpath` default and gave every `t.TempDir()` project fresh
  cache keys each run. They now merge through `goexec.WithTrimpath`, and
  `TestGOFLAGSOverridesKeepTrimpath` fails on any new site that does not.

- **Checks build with `-trimpath`, so worktrees and scratch copies share one
  build and test cache.** Without it Go hashes every main-module package's
  absolute directory into its cache key: a fresh worktree of the same commit
  compiled the whole module again (267 packages, 0 of 73 test packages cached,
  in control-plane), and `forge ci verify-generated` — which regenerates in a
  fresh scratch directory every run — recompiled everything in its validate
  step every time (10.8s against 2.6s with the flag). `forge generate`'s
  compile check, `forge lint`'s golangci-lint and every `forge ci gate` check
  now merge `-trimpath` into the effective GOFLAGS (`goexec.CheckEnv` /
  `WithTrimpath`), and the scaffolded Taskfiles do the same for every task in
  a top-level `env:` block. A GOFLAGS that already says `-trimpath` or
  `-trimpath=false` is left alone; debug builds keep absolute paths. Measured
  with control-plane's own gate: four concurrent `task ci` runs in fresh
  worktrees went from 182–258s wall (802s summed slot wait) to 103–139s (304s).
- **Heavy slots default to min(CPUs / 2, memory GiB / 8), not CPUs / 4.**
  Memory is what protects the machine (the reclaim collapse the old constant
  was measured against), and it still decides on a 16 vCPU / 32 GiB VM. CPU
  sharing degrades gracefully instead — four CPU-bound test lanes at once took
  the same total time as running them in turn — and a heavy job is rarely
  CPU-bound throughout: four concurrent merge gates on a 16-CPU / 128 GiB Mac
  left the CPU ~33% idle at 4 slots while 304s of slot wait piled up, and at 8
  slots the worst gate's wall fell 139s → 116s (164s → 122s on a rerun).

### Added

- **`ShellBuild.built_images`: one image built FROM another.**
  `built_images = {BASE = "runtime"}` declares that this ShellBuild needs the
  image the `runtime` workload's build pushes. `forge env build` builds
  `runtime` first and runs the dependent with `BASE` set to the exact
  `<repository>@sha256:<digest>` it pushed — the digest its build state and
  the release record — so a parent image is no longer a hand-edited digest
  constant that drifts from what was built. `--target <dependent>` builds its
  parents too; a composition carries an unselected parent from its base
  instead. A parent that fails, is skipped, or pushes nothing whose digest
  forge can resolve fails its dependents rather than handing them a tag.
  Edges to an undeclared, non-ShellBuild or image-less workload are refused at
  render, and cycles before any build runs; `--plan` shows each edge.
  Independent builds still run in parallel.
- **`NewInstrumented`: a contract's constructor, already decorated, so every
  construction site is one rename from traced.** `middleware_gen.go` now
  declares `NewInstrumented` beside the decorator, with `New`'s exact
  parameters and results, returning the decorated value. `forge generate`
  keeps `compose.go` wrapped, but most real projects construct components in
  `providers.go` and `*_wire.go` files too: in the control plane, after every
  contract package gained a decorator, none of its 46 hand-wired constructions
  used it, so its traces were unchanged. `forge-tracing-contract-uninstrumented`
  now autofixes every such call by renaming `New` to `NewInstrumented`, which
  is valid wherever a call can appear (including `x, err := pkg.New(…)` and
  `return pkg.New(…)`) and changes no type, and renames a `NewInstrumented`
  back to `New` when its package opts out. `forge lint --tracing` applies it;
  the full sweep applies it where the rule is at error.

- **Each root's config reach as importable Go: `pkg/config/roots_gen.go`.**
  A graph-mode project's generated `pkg/config` now has `Roots()`,
  `RootNamed(name)` and `ValidateRootEnv(name, env)`. These are the same reach
  `deploy/kcl/config_gen.k`'s `ROOTS` projects, generated from the same data
  in the same step. For each root: its name and command path, every env var it
  reaches (config, path, type, required, sensitive, aliases), the env families
  it reaches, and the configs its process loads with the fields its command
  demands. `forgeconfig.Root.Validate(env)` runs the boot load over a rendered
  env instead of the process's, with no flags, and returns every problem rather
  than the first. It catches a missing required var, a value the process would
  refuse (a bare-number duration, a value outside allowed_values, a misspelt
  env-family entry), and, as `*UnreachedVarError`, every var the root never
  reads. The repo that keeps a deployment's values can then check them against
  the binary's own schema, per root, in a Go test at its pinned version.
- **`Plan<Root>()`: each root's start and stop order as data.** `graph_gen.go`
  emits a static `graphkit.Plan` per root — every node `Build<Root>`
  constructs, in order, with the lifecycle methods its type has, the probe
  listener first for a root that serves no probes. `Plan.Start` / `Plan.Stop`
  are the order `Run<Root>` supervises, so a project pins a root's stop
  order in a unit test without a database or a broker (roots plan F7).
- **Each root's process settings come from the configs its command loads,
  and a root declares whether it migrates.** In a `binary_config` project
  (`(forge.v1.binary_config)` `command`/`subtrees`), `rootSpec.Configs` hands
  `runRoot` the configs a root's command loaded _before_ the policy runs, and
  the scaffolded `serverkitConfig(cfg, root.Configs)` reads the port, drain
  budget, logging, TLS and database settings from whichever of them declares
  each (most specific last), so `server api` and `server worker` listen and
  drain on their own command config's values.
  `//forge:root <name> migrate=apply|wait|none` says what the process does
  about the schema;
  `wait` blocks — `/readyz` naming `schema` — until another process has
  applied the migrations (`migratekit.AwaitSchema`,
  `serverkit.AwaitMigrationsContext`). The default is unchanged: apply when
  the root reaches the database. The scaffolded `serve.go` and `providers.go`
  now read only the root config's own fields off `*config.Config`, so a
  binary whose root config holds only blocks compiles (it read the union of
  every config message's field names). A project whose binary loads one
  config renders as before.
- **Probe paths are configurable, and the deploy follows the process
  (`forge/pkg/serverkit`).** `serverkit.Config.Probes`
  (`ProbePaths{Live, Ready []string}`; empty is `/healthz`, `/readyz`, so
  defaults are unchanged) names the paths the process answers its liveness and
  readiness probes on. Several paths per probe are allowed: the first is
  canonical and the rest are aliases. The boot probes, `serverkit.Run`'s
  listener and a `serverkit.Probes` surface (`Probes.Paths`) all serve them, and
  `ProbePaths.Validate` rejects a path no listener can serve. The scaffold
  declares `liveness_paths` / `readiness_paths` (`LIVENESS_PATHS` /
  `READINESS_PATHS`, reached by every root) and projects them in `serve.go`.
  The KCL lowering's default probe for a forge-built workload reads the same
  env values, and `PORT`, so a pod is probed where its process answers. A
  declared probe is kept. `Probes.Handle(path, h)` adds a probe-surface route,
  such as a `/flow-health` on the probe port. A composition-graph node that
  only calls `g.Probes.Handle` / `AddReadyCheck` no longer counts as serving
  the probes, so its root keeps its probe listener. Existing projects:
  `migrations/v0.1.47` section 3.
- **Roots can be declared one at a time: resolution follows reach.** A
  root's process builds only the closure its fields reach, but
  `forge generate` demanded a provider for every Deps field of every discovered
  component, so a project with 33 handlers could not declare a gateway root
  without first wiring all of them. Now a Deps field forge cannot fill fails
  generate only on a node some root reaches (as before, with the same
  message). A node no root reaches with such a field, and every node that
  takes it, is left out of `graph_gen.go` — no accessor, no injected
  constructor; an owned one is kept — and `forge lint --graph` reports it as
  `forge-graph-unreachable`, always an error. A project whose Deps all resolve,
  every fresh scaffold, generates byte for byte what it did.
- **`forge lint --graph` answers to `lint.rules`.** Its findings went straight
  to the verdict, so `lint.rules: {forge-graph-unreachable: warn}` was accepted
  and ignored. They now pass through the same `off` / `warn` / `error` dial as
  every forge rule, which is what holds the unreached components at warn while
  the roots are declared.
- **A failed request names the trace it landed in, in the browser.** Every
  forge server now answers each Connect call, failed ones included, with the
  W3C `traceresponse` header naming its server span
  (`observe.TraceResponseInterceptor`, inside the trace slot of
  `observe.Chain`), and `middleware.DefaultCORSExposeHeaders` exposes it.
  `@reliantlabs/forge-web-runtime` reads it (next release, 0.6.0): `ConnectClientError.traceId`
  is the server's trace (or, without the header, the trace of the traceparent
  the runtime sent), a dev log line that logs such an error ends in
  `trace_id=<id>`, and `reportException` adds it as `server.trace_id`. So one
  id joins a screenshot of an error, the browser's record, and every server
  span and log line of the request.

- **Every contract package is instrumented by default.** A package whose
  constructor returns its `Service` interface now gets `middleware_gen.go`
  (one span, the call metrics and a failure log per method), and compose.go
  constructs it wrapped, with no `// forge:constructor` marker and no
  `observe_chain.go` seam. The gate on those two had left every package that
  predated the scaffold silently untraced: 26 of the control plane's 46,
  including its hottest business paths, so most of its traces were a single
  span. Without a seam the decorator builds the new
  `observe.DefaultComponentChain(scope, name, level)`, which is what the
  scaffolded seam assembles. Nothing is scaffolded into the package, and the
  seam is now an optional override. `forge generate` also brings an existing
  compose.go construction in line when the decision changes, in both the
  `NewComponents` and the composition-graph (`new<X>` method) shapes: it wraps
  a component that is now instrumented, and unwraps one that gained
  `// forge:no-observe`. `// forge:no-observe` on the package or on one method
  remains the opt-out. Constructions written by hand (`providers.go`,
  `*_wire.go`) are yours to wrap; `forge lint --tracing`'s
  `forge-tracing-contract-uninstrumented` finds them, with the same decision.

- **`orm.Client.DBTX`, `SQLConn(ctx)` and `InTx(ctx)`: sqlc and hand-written
  database/sql code join the ORM's transaction.** A project porting onto the
  ORM keeps sqlc on the same pool for a while, and until now a sqlc call
  inside `RunTx` ran on the pool: outside the transaction, and blocked
  forever on a row the transaction had locked. `client.DBTX()` is a sqlc
  `DBTX` that resolves the transaction `ctx` carries on every call
  (`pgdb.New(client.DBTX())`); `SQLConn(ctx)` is the executor for one
  statement; `InTx(ctx)` asks about this pool, where `orm.InTx` asks about
  the innermost transaction on any. A layer with its own transaction loop
  opens it with `BeginTx` and runs its work under `tx.RunTx`, so both layers
  join it from that side too (`service-layer/transactions`).
- **`forge lint` is report-only under CI, and `--check` says so anywhere.** The
  auto-fix default (#843) is right on a developer's machine and wrong on a CI
  runner, where the checkout is thrown away: a fixer that quietly repairs it
  turns "this commit is unformatted" into a green build. When the `CI`
  environment variable is set (GitHub Actions, GitLab and the rest set it;
  `GITHUB_ACTIONS` also counts), `forge lint` now writes nothing, prints the
  unified diff each fix would have applied (gofmt/goimports and prettier), and
  fails with a `format` lane in the verdict, `--gate-json` evidence and the
  exit code, so CI fails on unformatted code with the exact change to make.
  `--check` is the clear name for the same thing anywhere, and `--no-fix`
  remains as its spelling. `--fix`, a no-op until now, forces fixing even under
  CI. A local run with no flags still auto-fixes. The report and the fixer share
  one walk, so they cannot disagree about which files are pending, and the
  scaffolded release workflow and the `ci` and `deploy` skills now rely on it
  instead of passing a flag. `--json` and the targeted lanes were already
  detect-only.
- **`forge fix composition-graph`: the upgrade path off the legacy
  composition** (composition graph PR 7). It converts what forge scaffolded —
  Infra / OpenInfra, NewComponents, lifecycle.go, ServeSpec and the
  per-service / per-worker / per-operator subcommands — onto providers,
  compose.go constructor methods (every Deps value you set is kept,
  translated: `infra.DB` → `g.DB().DB`, `c.Tasks` → `g.Tasks()`) and the
  default `server` root, which runs what `<bin> server` ran. It is all or
  nothing: an Infra field of your own, a ServeSpec knob, custom supervision, a
  command calling `Serve` or code naming a retired symbol is listed with its
  file:line and the edit that clears it, and nothing is written. It runs only
  on a committed tree (`--allow-dirty`), and a project with no internal/app is
  told how to adopt the graph by hand instead. `migrations/v0.1.47` section 2
  is the manual path, with control-plane's `OperatorListener`,
  `NoKubernetesAPI` and `WorkerLeases` knobs mapped to roots and reach, and the
  three hand edits a graph project moving to `binary_config` needs.

- **`forge lint --tracing`: the places a request's trace is cut.** Four
  rules, all at error in the full sweep, over owned non-test Go:
  `forge-tracing-log-without-ctx` (a `slog.Info`, a `*slog.Logger` level
  method, or a helper named in `lint.tracing.log_wrappers`, called where a
  `context.Context` or `*http.Request` parameter is in scope: the line
  carries no trace_id), `forge-tracing-raw-http-client` (an `*http.Client`
  with no, nil, default or bare transport; `http.DefaultClient`; `http.Get`),
  `forge-tracing-raw-sql-open` (`sql.Open`, sqlx, pgx stdlib, and a pgx pool
  with no tracer, instead of `dbtrace`), and
  `forge-tracing-contract-uninstrumented` (an instrumented contract's `New`
  used outside `compose.go` without its `…WithForgeMiddleware` decorator).
  The log rule autofixes to the `…Context` form, dropping an import the
  rewrite leaves unused. `forge lint --tracing` applies it at any severity
  but off; the full sweep only where the rule is at error, so a repo that
  sets a rule to warn in `lint.rules` while it migrates is not rewritten by a
  forge pin. Worker scaffolds log with their context.
- **A trace per unit of background work: `observe.RunRoot`,
  `lifecyclekit.Every`, and a root span per reconcile in
  `forge/pkg/controller`.** A loop iteration or a reconcile has no Connect
  edge to start its trace, so the decorated methods, queries and calls it made
  were each a one-span trace, or nothing (`dbtrace` opens spans only under a
  parent). `observe.RunRoot(ctx, name, fn)` runs `fn` under a new root span,
  never the caller's trace, and records `fn`'s error or panic (the panic is
  re-raised). `lifecyclekit.Every(name, EveryOptions{Interval}, fn)` is the
  hand-written ticker worker with that built in: one `worker.<name>.run` root
  per run, a failed or panicking run logged and the cadence kept.
  `controller.Reconciler.Run` opens `reconcile <Kind>` around every reconcile,
  with `k8s.namespace.name` / `k8s.object.name` and the error recorded.
- **Trace context through Temporal and NATS: `forge/pkg/observe/temporalotel`
  and `forge/pkg/observe/natsotel`.** `temporalotel.NewInterceptor` is
  Temporal's `contrib/opentelemetry` interceptor with forge's defaults: the
  global W3C propagator (the same context Connect, HTTP and NATS carry), the
  global tracer provider read at span time, and `AllowInvalidParentSpans`, so
  workflows already in flight keep running. Installed once on `client.Dial`,
  it also applies to the client's workers, so the request that starts a
  workflow, the workflow and its activities are one trace; before this,
  Reliant's hand-rolled interceptor made every activity a root. `natsotel` is
  Reliant's NATS propagation, lifted: `NewMsg`/`Inject` on publish,
  `StartConsumerSpan(ctx, subject, header, name)` on consume (core NATS or
  JetStream), `StartProducerSpan` where the publish deserves a span, with
  `messaging.*` attributes.

- **Root reach for binary-config projects.** A composition-graph project whose
  primary binary binds config per command (`(forge.v1.binary_config)`
  `command` / `subtrees`) used to get no per-root reach: no `LoadReach`
  narrowing, and no `ROOTS` in `config_gen.k`, so the scaffolded envs'
  `workloadEnvMap` call did not render. Now each of the binary's configs
  beside the root config is an `app.Graph` input, `g.<Message>`. A Deps field
  typed `*config.<Message>` resolves to it. A root's process loads the root
  config, the subtree configs over its command and its exact command config,
  and its reach is still per field: the leaves its closure reads of each.
  `roots_gen.go` loads each config with the new `config.Load<Msg>Reach`
  (only reached `required` fields are demanded), sets it on the graph, and
  registers its flags (command configs on their command, subtree configs on
  their path's command). `Build<Root>` refuses a nil input its closure reads.
  `ROOTS` carries the union of every loaded config's reached env and projects
  it through `<bin>ProcessEnvMap`, so `workloadEnvMap(appcfg.<bin>_process, w)`
  gives each root exactly its env. Generate refuses a closure that reads a
  config its root's process never loads, and an env var that two loaded
  configs both declare. Single-AppConfig and legacy projects generate
  byte-identical output.

- **Release codemods, and `forge fix operatorkit`.** A codemod
  (`internal/codemod`) is keyed to the release that broke the shape it
  rewrites, exactly as a migration skill is, and decides from the tree, so it
  is a no-op on a project without the old shape. `forge project upgrade` runs
  every rename-class codemod whose release the baseline predates — under
  `--check` it lists what it would rewrite — and each is also
  `forge fix <name> [--dry-run]`. The first is `operatorkit` (v0.1.46): every
  `lifecyclekit.Run` / `Controller` / `Options` reference becomes
  `operatorkit.*`, the operatorkit import is added and an unused lifecyclekit
  import dropped. `forge generate` no longer holds the forge pin back over a
  codemod that has nothing to rewrite in the project. The previous registry
  was keyed by minor hop, which a patch-release breaking change could never
  use, and was empty.

- **A forge that is not the project's pinned forge now hands the command to the
  pinned forge instead of refusing.** The forge on PATH (a Reliant workspace
  bundles one) is routinely older than a project's `forge_version`, and
  `generate`, `scaffold`, `component install` and friends refused with a
  recipe, or a render failed with KCL schema errors (`release_dependencies`,
  `ShellBuild.unit`) that blame the project; agents had to run `task tools` or
  `go run …@<pin>` by hand. Now the command is exec'd under the right forge
  with the same arguments, environment, stdio and exit status, and one line on
  stderr says so. The pinned forge is the project's `.tools/bin/forge` when it
  reports that version, else a one-time `go install` of the pinned module
  version into a cache under `<user cache dir>/forge/pinned/` (a project linked
  to a forge checkout by go.work `use` or a directory `replace` gets a build of
  that checkout instead). The KCL-reading commands (`env render`, `env shape`, `env config`,
  `env diff`, `env up`, `kcl`, `doctor`) are handed over too. Read-only runs stay local;
  `--allow-version-skew` / `FORGE_ALLOW_VERSION_SKEW=1` runs this forge as
  before; if the pinned forge cannot be had (offline, an unpublished version)
  the old refusal returns, now carrying the reason. See `docs/versioning.md`.

- **`observe.TrustRemoteFrom(trusted, opts...)`: continue a caller's trace
  only when the caller is an authenticated internal service.** otelconnect
  answers "adopt the inbound traceparent?" once for every caller:
  `WithTrustRemote` adopts it for everyone, the default for no one. A server
  reachable from the internet must take the default, so its internal callers'
  requests became new traces linked to theirs, never one trace (every
  workspace-proxy → admin-server call in the Reliant control plane). The
  returned interceptor decides per request: when `trusted(ctx, peer, header)`
  verifies the caller (an internal-service token signature, a constant-time
  shared-secret compare, an mTLS identity, never a spoofable header or IP),
  the server span is the caller's child; otherwise it is a new trace linked to
  the caller's, exactly as before. It runs outermost, ahead of auth, so the
  predicate does its own verification. Authorization is unchanged. The threat
  model (trace grafting, forced sampling, baggage injection) is in
  `pkg/observe/trust_remote.go`.

- **`observe.NewHTTPClient`: the traced client for plain outbound HTTP.**
  A call to a third-party REST API goes through a plain `*http.Client`, which
  no Connect interceptor sees; built by hand it had no client span, no
  traceparent and no `X-Request-Id`, so the caller's trace ended at the
  process boundary. `NewHTTPClient(HTTPClientOptions{Timeout, Base})` returns
  a fresh client over one shared traced transport, with spans named
  `<METHOD> <host>` instead of `HTTP POST`. `NewHTTPTransport(base)` wraps a
  transport you do not construct yourself. The scaffolded `DefaultClient()`
  transport, the composition graph's `NewHTTPClient` provider, and the
  adapter and client scaffolds' nil-`HTTPClient` defaults all use it, so a
  minimal wiring is no longer the one untraced outbound path. Connect clients
  keep `NewClientStack`, whose otelconnect interceptor already opens the span.

- **forge's control-plane client names itself in `Forge-Via`.** Every call
  sends `cli`, or `ci` under a CI runner, so a server's activity stream
  (`forge/pkg/activity`) records how a change arrived, as in "sean promoted
  v1.7.44 via CLI". `middleware.DefaultCORSAllowHeaders` admits `Forge-Via`,
  so a browser console can send it cross-origin.
- **Activity scopes are paths, so one feed can span an org and its projects**
  (`forge/pkg/activity`). An event scoped `org_1/billing` belongs to project
  billing of org org_1. `Filter.IncludeAncestors` adds the ancestors' events to
  a scope's feed, so a project's feed shows its org's events.
  `Filter.IncludeDescendants` adds every descendant's events, so an org's feed
  shows every project's. `Filter.Envs` matches any of several environments.
  Wildcards in a scope name match literally. `activity.Skip(ctx)` lets a
  handler whose call changed nothing (an idempotent ensure, a dry run) suppress
  that call's event.
- **Every SQL statement is a span: `forge/pkg/observe/dbtrace`, used by
  `serverkit.OpenDB`, `cmdkit.OpenDB` and `orm.NewClient`.** The pool is
  opened through `otelsql`, so each statement run under a span gets a CLIENT
  child span named `<OP> <table>` (`SELECT chats`), plus `BEGIN`, `COMMIT`,
  `ROLLBACK` and `db.connect`. `db.query.text` is the statement with every
  literal replaced by `?` (`dbtrace.SanitizeSQL`); bind arguments are never
  recorded. Ping, session-reset, prepare and per-row spans are off, and a
  statement with no span in its context opens none, so an un-rooted poller
  does not produce one-span traces. Before this, the only query spans came
  from bun's hook, which hand-written `database/sql` never reaches: prod had
  none in 24 hours. `orm.NewClientWithDB` keeps bunotel's query histogram but
  drops its spans when `dbtrace.IsTraced(db)`, so ORM queries are not spanned
  twice. `serverkit.PgxConn` reaches the `*pgx.Conn` under a `sql.Conn.Raw`
  driver connection through the tracing and pooler-drain wrappers.

- **`observe.NewTraceHandler` records WARN and ERROR lines on the span, and
  `forge/pkg/observe/observetest.AssertLogCorrelation` pins log↔trace joins.**
  A record at WARN or above, logged with a recording span in its context, is
  added to that span as a `log.warn` / `log.error` event (`observe.LogEventWarn`,
  `observe.LogEventError`) with `log.message`, `log.severity` and the error's
  `exception.message`, each capped at 512 bytes. The tail sampler can then keep
  a trace whose logs complained instead of keeping its log lines and dropping
  the trace (in prod only 10% of logged trace ids resolved to a stored trace).
  The handler sits inside `NewErrorClassHandler` in serverkit's logger, so a
  user error lowered to INFO adds no event.
  `AssertLogCorrelation(t, logger, output)` logs through an application's real
  logger under a span and fails unless the lines carry its
  `trace_id`/`span_id` and the WARN became an event: the regression test for
  an app that builds its own logger.

- **One command per root** (composition graph, PR 4). In a graph-mode
  project every root — each `//forge:root` struct in `internal/app`, or the
  generated default `Server` — gets one cobra command in the forge-owned
  `cmd/<bin>/cmd/roots_gen.go`, named after the root or after its new
  `command=` option (`//forge:root api command=server,api` runs as
  `<bin> server api`). Each command runs its root through `runRoot`, the
  owned process policy `serve.go` now holds — config, logger, boot probes,
  migrate, telemetry, the readiness gate and stop budget, for every root at
  once — handing it two facts read off the root's reach at generate time: a
  root that reaches the database migrates first (a worker root that never
  opens it never runs a migration), and a root whose closure serves no
  probes gets a probe-only listener on the server address
  (`serverkit.NewProbeListener`), so every process answers `/healthz` and
  `/readyz`. A config without `pre_stop_delay` / `shutdown_timeout` reads the
  drain budget through `drainbudget.FromEnv`. `graphkit.Builder.Supervise`
  takes nodes to start before the graph and stop after it. A root whose
  command collides with `version`, `db` or `metrics`, or two roots with one
  command, are generate errors. Declaring roots no longer breaks the build:
  the scaffolded `server.go` is gone (its command is generated), and a
  `serve.go` from before this change — no `runRoot` — keeps its own commands
  while generate says what to add.

- **`forge lint --graph`: the composition graph, judged whole** (composition
  graph, PR 5). It assembles the graph exactly as `forge generate` does and
  fails on an error: a service, package, worker, operator or provider no
  root reaches (never constructed, so a service there is served by no
  process — what the boot-time `RequireComplete` gate caught before the
  graph), and a listener serving a service whose `(forge.v1.service).audience`
  differs from its chain's authenticator's — the authenticator's provider
  func declares it with the new `//forge:audience <name>` (the scaffolded
  `NewUserAuth` is `//forge:audience user`). A provider kept for components
  to come carries `//forge:standby` (the scaffolded `NewORM` and
  `NewHTTPClient` do). The lane runs in the full sweep and is whole-project
  under `--scope`; a legacy-composition project skips it.
- **Least-knowledge rules in `forge lint --conventions`** (composition graph
  §2): `forgeconv-deps-surface` (a Deps interface field declaring methods
  the package never calls through it, read with go/types),
  `forgeconv-no-container-deps` (an `internal/app` type, `cmd.Deps`, a
  `map[string]any` locator or `any` in Deps) and
  `forgeconv-deps-no-downcast` (a type assertion from a Deps field to a
  concrete type). They land as warnings; under `--strict`, deps-surface is
  an error for a foreign interface of more than ten methods with unused
  ones, and the other two are errors.
- **`forge fix narrow-deps <pkg>`** declares, in the package (`contract.go`,
  or a new `deps.go`), the interface holding exactly the methods the package
  calls through each wide foreign Deps field, retypes the field, and writes
  its mock; `forge generate` keeps a `deps.go`'s `mock_gen.go` in step.
  `compose.go` needs no change. A field whose value the package passes on,
  type-asserts, or never calls is left alone, with the reason.
- **Each root's config is its reach** (composition graph, PR 6). Generate
  reads, per root, the config fields its closure reads in `compose.go`, plus
  the process fields `runRoot` reads and, for a root reaching the database,
  the database fields. The roots table carries that set and `runRoot` loads
  with the new `config.LoadReach(cmd, reach)` (`pkg/config`'s
  `RequiredWithin(paths...)` load option), so a worker root starts without
  `DATABASE_URL`. `deploy/kcl/config_gen.k` projects the same set as
  `ROOTS`, with `root_of(name)` (binds a workload to a root and its command)
  and `workloadEnvMap(cfg, w)` (exactly the env that root reaches; a
  `config_secrets` entry it does not reach, or an unknown root, fails the
  render). `fw.Workload` gains `root?: str` (needs `build`, excludes
  `command`). New projects deploy `_api` with `root = "server"`. Only a root
  `AppConfig` is projected; per-binary configs (`binary_config`) get no
  `ROOTS`.

### Fixed

- **Parallel generates no longer fail pinning a codegen tool.** When the
  protoc plugin on PATH does not match go.mod, forge installs the pinned one
  into its cache. Two forge processes doing so at once (every fresh project's
  first generate pins) could read a binary the other was still writing, and
  failed with "installed binary reports unknown". The install now writes into
  a private directory and renames the finished binary into place.
- **`forge generate` composes correctly when go.mod is behind the tree.** The
  composition graph types every node with one package load, and that load
  fails outright on a go.mod that needs updating: a fresh clone, a
  just-added `replace`, a dependency bump. The graph then fell back to names
  alone. The server root held no nodes, `graph_gen.go` lost every node's
  lifecycle, and `config_gen.k` refused the scaffold's own
  `config_secrets = ["DATABASE_URL"]`. The tidy that ran after composition
  repaired go.mod, so only the next run was right. A
  `go mod tidy (pre-composition)` step now runs first on any project generate
  has composed before.
- **`forge lint --tracing` says what each rewrite did.** Its fix report
  called every rewrite a log call, so a run that only renamed contract
  constructions to `NewInstrumented` printed "34 log call(s) rewritten to pass
  the request context" across 23 files. Each file now reports in the words of
  the rule that rewrote it (`describeTracingFix`), the summary counts
  rewrites, and the "can be rewritten automatically" hint counts
  contract-uninstrumented warnings as well as log ones.
- **A fresh project's first `forge lint` under CI passes.** `forge lint`'s
  format lane skips a scaffold file nobody has touched, judged by the birth
  hash in `.forge/scaffolded.json`. Of a frontend's scaffold, only `nav.tsx`
  and `dashboard.tsx` were recorded. Every other file read as edited from
  birth, so under CI, where lint is report-only, a new project failed on 31
  template files prettier would re-wrap. Every scaffold-once file a frontend
  scaffold writes, the core UI components and the seeded mock scenario
  included, is now born with its hash.
- **A generated store reports a missing row one way.** `Get<E>ByID`,
  `Get<E>ByPK` and a view's `Get<E>ByID` returned `svcerr.NotFound`, while
  `Update<E>`, `Update<E>Masked`, `Delete<E>` and `Delete<E>ByPK` returned
  `orm.ErrNoRows`, so a caller touching both checked both. `crud.Repo` now
  answers every by-key miss (and `crud.ScopedGet`'s) with
  `svcerr.NotFound("<entity>")` carrying `orm.ErrNoRows` as its cause:
  `svcerr.IsNotFound(err)` and `errors.Is(err, orm.ErrNoRows)` (=
  `sql.ErrNoRows`) are both true. `svcerr.NotFound` is the answer because it
  is the domain category `svcerr.Wrap` maps to CodeNotFound and a service can
  return as-is; the sentinel stays reachable so a Persist closure that
  swallows it (absent-is-success) and every existing check keep working. The
  generated delegates now only forward, and no longer import `errors` or
  `pkg/svcerr`. **Migration note:** nothing that checked either error breaks;
  `ignoreNotFound`-style helpers that test both can test one. Two checks
  that never fired now do: `errors.Is(err, sql.ErrNoRows)` after a `Get…`,
  and `svcerr.IsNotFound(err)` after an Update or Delete. A caller that
  compared the error with `==` (never correct: it was wrapped) or matched its
  text sees `"<entity> not found"` in place of `"sql: no rows in result set"`.
- **`Create<E>` and `Upsert<E>` keep a caller-set `updated_at`.** Managed
  timestamps follow explicit-beats-default, the rule a `NOT NULL DEFAULT`
  column follows: `created_at` and `updated_at` get now only when unset (zero
  time, `nil`, `""`), and a value the caller set is kept — Create overwrote
  `updated_at` unconditionally, so rows that carry their own times (reliant's
  drafts and scenarios) had to bypass it. `Upsert` writes the same
  `updated_at` on either branch. `Update`/`UpdateMasked` still always
  re-stamp `updated_at`. **Migration note:** a Create of a struct that still
  holds a stale non-zero `updated_at` (copied from another row) now stores
  that value; zero it, or set it to now, to stamp.
- **`List<E>` with no limit returns every row instead of the first 10,000.**
  A limit-less `crud.Repo.List` was capped at 10,000 rows with no error and
  no flag, so a table that outgrew it silently returned a wrong answer. The
  cap is gone: no `orm.WithLimit` means all matching rows, as `ListAll`
  already did. Bound a read with `orm.WithLimit`, or walk a growing table a
  page at a time (`WhereGt` on the key + `WithOrderBy` + `WithLimit`). The
  paged List rpcs are unchanged: they clamp `page_size` to `MaxPageSize` and
  always pass their own limit. **Migration note:** a direct caller that
  relied on the cap to bound memory must now pass `orm.WithLimit`.

- **Every ORM statement is sent with its values as bind parameters, not just
  the by-key ones.** bun formats values into the SQL text, so under pgx's
  default statement cache each new id, timestamp or body was a new statement
  to Parse, Describe and plan, and churned the connection's 512-entry cache;
  F12.8 bound only `Get`/`Update`/`UpdateMasked`/`Delete`. Measured in
  reliant: a message save through `NewRaw` was 35–80% slower than the same
  statement with `$N`, and an append through the builder cost 16x the bytes
  and 3x the allocations. Now the connection an `orm.Client` hands bun (pool,
  `RunTx` ctx, or `Tx.Bun()`) re-renders each statement with a binding
  dialect and sends one text per statement shape: generated
  `Create`/`Upsert`/`List`/`ListAll`/`Count`, every bun builder query and
  `NewRaw`. Bound: every model column value (numbers included), every
  string, time, `[]byte` and JSON `?` argument, the values of
  `orm.WhereEq`/`WhereIn`/… , and anything wrapped in the new `orm.Arg(v)` /
  `orm.ArgList(slice)` — the way to bind an integer `?` argument, which bun
  formats without consulting the dialect. A value stays a literal where a
  parameter would change its meaning (positional `ORDER BY`, typed literals,
  bare arguments of `"any"` functions such as `jsonb_build_object`,
  `? IS NULL`, `ON CONFLICT … DO` targets), and a statement forge cannot prove
  equal to bun's text, or that is not DML, runs as bun rendered it.
  `orm.Literal(v)` keeps one value in the text (a constant a partial index's
  predicate is proved from); `orm.InlineValues(ctx)` turns binding off for a
  ctx. `BenchmarkBoundStatements` (pkg/crud): statements prepared per call
  1.0 → 0.0005 for Create, List, Count and `NewRaw`.

  **Migration note.** A `?` value the planner needs at plan time — the
  constant of a partial-index predicate — must now be written into the SQL
  or passed as `orm.Literal(v)`: a generic plan cannot prove a predicate from
  a parameter. Text written into the SQL itself is never bound. `Tx.Bun()`
  now returns a wrapper over `bun.Tx`, not the `bun.Tx` itself, and the
  Client's bun handle runs on a pgdialect wrapper (`db.Dialect()` is no
  longer a `*pgdialect.Dialect`).

- **`api.rest` runs on vanguard v0.5, and no longer hides the mux's other
  routes.** vanguard v0.5 serves REST from a connect-go v2 `*connect.Server`
  (`vanguard.Mount`) and removed `Service` / `NewService` / `NewTranscoder`.
  `serverkit.RESTHandler` keeps its signature: each mounted service is
  registered as `vanguard.ForwardService` over a Connect client whose
  transport serves the call with the mux in process, so a REST request is
  answered by the same handler and interceptor chain as its Connect URL, and
  that handler sees the REST caller's address, TLS state and Host. A request
  the mux has a route for now always goes to the mux: under v0.4's transcoder
  every non-Connect route on it (the login broker's `/auth/*`) answered 404
  once REST was on. Two v0.5 behaviors to know about. A streaming rpc maps to
  REST only when its streamed side is a `google.api.HttpBody` (v0.4 sent
  JSON lines). Annotating any other streaming rpc fails the REST routes at
  startup, and the server then serves Connect only.
- **The tracing rules flag only code a trace can reach.** Three constructions
  were reported that cut no trace, and are no longer: in a project whose
  observability is off (derived off with no server: a CLI or a library), the
  top-level `internal/` and `cmd/` trees and every `package main`, which run
  only in its own untraced binaries — its importable packages are still
  linted, since they run inside the traced servers that import them; a pool
  opened only to look its driver up (every use `.Driver()` or `.Close()`,
  how `dbtrace.Open` builds a traced pool from a driver name); and an
  `*http.Client` in a file that exports telemetry over OTLP, which is
  untraced on purpose (a traced exporter traces its own exports). forge's
  own `forge lint` is green again: its library code now logs with the
  request context, and opens its HTTP clients and pools traced
  (`pkg/oauth2`, `pkg/identity`, `pkg/orm`, `pkg/migratekit`, `pkg/pgtest`,
  `pkg/testkit`, …). `pkg/pgtest` handles are pgx under dbtrace, as
  production's pool is.
- **`forge lint`'s format pre-pass never rewrites a `testdata` fixture.** The
  go tool and the gate never read one, and an analyzer fixture's want lines
  depend on its exact layout.
- **A freshly scaffolded operator passes `forge lint --contract`.** An
  `internal/operators/<name>` package declaring `SetupWithManager` is exempt
  from `requirecontract` structurally, as a worker is.
- **`serverkit.Run` serves pprof through the `Pprof` component** instead of
  its own copy.
- **A legacy-composition `internal/app/lifecycle.go` with no worker compiles.**
  The template imported `forge/pkg/lifecyclekit` unconditionally, but since
  its operator re-exports moved to `operatorkit` (#918) only a `Worker<X>()`
  accessor names it, so a project without workers failed its first generate
  with `"…/pkg/lifecyclekit" imported and not used` (every
  `forge project new` from #918 until the composition graph, #923, made new
  projects skip lifecycle.go). The import is rendered with the first worker,
  and the reconciler adds it when it injects one into an existing file.
- **`gen/forge_descriptor.json` stamps one entry per proto, and a merge keeps
  it current.** The `source_hash` digest of every `.proto` is replaced by
  `sources` (path → sha256 per file). A single digest of the tree matched no
  merge: two branches editing different protos each stamped a different
  value, so every merge that brought in a proto change staled every open
  branch's descriptor, and main went stale when two landed without a
  regenerate. Per-file entries change only where their proto changes, and the
  `forge-generated` merge driver merges them key by key, so the merged stamp
  is the stamp of the merged tree. forge's vendored annotation protos
  (`forge/v1/forge.proto`, `buf/validate/validate.proto`) are not stamped:
  they are grammar, not descriptor input, and only `forge generate` changes
  them. A stale descriptor is now refused with the protos that drifted
  (`proto/x.proto (edited)`). The next `forge generate` rewrites the field.
- **A new service project's `docs/runbook.md` points at what forge ships.** It
  said "Check for traffic spike (Grafana dashboard)" and "Grafana: TODO
  (link)", and asked for Loki, Jaeger and PagerDuty links. It now names the
  Observability tab under Apps in Reliant, the local HyperDX UI, the generated
  dashboards in `deploy/observability/dashboards/`, the generated alert
  defaults (`alert-defaults_gen.json`) and how `forge.Alerts.defaults` tunes
  them, and explains each built-in alert.
- **`forge generate --help` describes the pipeline it runs.** It described
  entity protos in `proto/db/` and a `pkg/app/bootstrap.go`, both retired. The
  no-`forge.yaml` path no longer looks for `proto/api/` and `proto/db/`, and
  the seed step is gated on services rather than on a `proto/db/` directory.

### Removed

- **New service projects no longer get `deploy/observability/prometheus-rules.yaml`
  or `docs/slo.md`.** Nothing in forge loaded the rules (alerts are
  `forge.AlertRule` / `forge.AlertDefault` in KCL, evaluated as ClickHouse
  SQL) and nothing measured the SLO doc's targets. An existing project can
  delete both files.

- **Graph-mode projects no longer scaffold `cmd/<bin>/cmd/server.go`**, and
  `cmd/<bin>/main.go` / `root.go` take no command-group constructors
  (`Execute()`, `newRootCmd(deps)`). The per-service, per-worker and
  per-operator subcommands and `Serve` / `ServeSpec` / `MountFunc` /
  `MountNone` stay only in projects on the legacy `Infra` / `NewComponents`
  composition, which generate exactly as before.

### Added

- **`fw.Workload.profiling` (`forge.Profiling`): a typed per-workload
  profiling override.** `wl.worker | {profiling = fw.Profiling {alloc = True, heap_interval = "1m"}}`
  turns a type on or the heap cadence up for one workload; the schema defaults
  to the runtime's set, and `heap_interval` is whole minutes or hours from
  `1m` to `24h`. It renders `FORGE_PROFILES` and `FORGE_PROFILES_HEAP_INTERVAL`
  on every runtime that runs a process; a value the workload's own `env` sets
  wins, and it never turns profiling on where the env has it off.
- **Fast/slow test tiers: `testkit.Slow`, `forge lint --slow-tests`,
  `forge ci test-budget`.** A test is fast (it runs in the blocking
  `go test -short` tier) unless it declares otherwise:
  `testkit.Slow(t, "<what makes it slow>", testkit.Inputs("deploy/**"))`
  skips under `-short` and in the integration lane, runs in the full tier,
  and says so in its skip message. It sits on Go's own `-short`, so plain
  `go test`, IDEs and the test cache are unchanged; a plain
  `if testing.Short() { t.Skip(...) }` still counts. `Inputs` names the
  files a slow test depends on, read from source (string literals) by the
  new `internal/linter/slowtest` analyzer, for a later affected-selection
  tool. Two guards enforce the marker by measurement:
  `forge lint --slow-tests` (opt-in for now) flags a test whose own code — its body,
  `_test.go` helpers, helper files that import `testing`, across packages
  via analysis facts — execs `go`/`forge`/`kcl`/`node`/`npm`/`git`/`docker`/…,
  drives containers or dials an external host with no `testing.Short` check
  on the way; `go test -short -json ./... | forge ci test-budget` fails on
  any unmarked top-level test over `--budget` (default 2s), with file:line.
- **`forge lint --slow-tests`: git against a repo in a temp dir is not slow
  work.** An exec of `git` whose `Dir` (or, with none, an argument such as
  `-C <dir>` / `init <dir>`) comes from `t.TempDir()` or `os.MkdirTemp` is
  no longer reported: such a fixture repo reads nothing the test cache cannot
  see and is usually milliseconds; `forge ci test-budget` still reports one
  that is slow. The directory is followed through locals, struct fields set
  in the same function, functions that return a temp dir, and helper
  parameters across packages (the slow fact carries the parameter). git over
  the checkout is still reported. Findings: forge 146 → 48, reliant
  205 → 111, control-plane 3 → 2.
- **The interceptor chain has a `Deadline` slot, ahead of `Auth`
  (`forge/pkg/serverkit`).** `ChainDeps.Deadline` takes a `serverkit.Deadline`
  — the slot's configuration and its interceptor in one value — and runs right
  after metrics and right before auth, so the RPC's deadline bounds
  authentication: a stalled token-store query or introspection call ends as
  `DeadlineExceeded` instead of holding the request. The default (a nil slot)
  bounds every unary RPC by `DefaultUnaryDeadline` (30s), leaves streams
  unbounded, and logs a unary RPC still running after `DefaultSlowAfter` (10s)
  at WARN, then every `DefaultSlowRepeat` (30s), then once more when it
  completes. `Deadline.Procedures` overrides the budget per procedure (zero or
  negative is none; an entry bounds a stream too), `Unary` moves the default,
  `SlowAfter`/`SlowRepeat` the watchdog, and `OnSlow` replaces its log (to
  count slow calls as well). The server's budget is a ceiling: a client's
  shorter deadline still applies, a longer one does not lift it.
  `serverkit.NoDeadline` drops the slot; any other interceptor in it replaces
  forge's and runs in the same pre-auth position. `observe.Deps.Deadline` is
  the same slot in `observe.Chain`, with the same rule — nil is
  `serverkit.Deadline{}` (logging through the chain's `Logger`),
  `serverkit.NoDeadline` opts out — because `NewChain` builds on it; the type
  is `observe.Deadline`, which `serverkit.Deadline` aliases. NewChain rejects a
  `Procedures` key that is not a procedure (`/pkg.v1.Service/Method`).
  Behaviour change: an existing chain — `NewChain` or `observe.Chain` — gets
  the 30s unary default on upgrade with no edit; the `migrations/v0.1.47`
  playbook says how to declare longer budgets.
- **`(forge.v1.method).timeout` is enforced.** It was documented as the rpc's
  server-side timeout and read by nothing. `forge generate` now projects it
  into `pkg/middleware/procedures_gen.go` as `ProcedureDeadlines`, and the
  scaffolded chain passes
  `serverkit.Deadline{Procedures: middleware.ProcedureDeadlines}`. An existing
  project adopts it by adding
  that one `Deadline:` line to its owned `serve.go` / `providers.go` chain.
- **A typed activity stream: an rpc declares the activity event it records,
  and one interceptor writes it** (`forge/pkg/activity`).
  - **Declaring.** An rpc declares its event with
    `(forge.v1.method).activity`, giving
    `verb`, `target {kind, id, label}`, `env`, `scope` and `detail`. Field
    paths resolve against the request, or against the response when they start
    with `response.`.
  - **Opting out.** `activity: { none: "<why>" }` records that a mutating rpc
    changes nothing worth an event.
  - **CRUD needs no option.** A `Create<E>`, `Update<E>` or `Delete<E>` rpc with
    forge's CRUD shape implies `create`, `update` or `delete` on `snake(E)`.
  - **One resolver.** `activity.Resolve` is the only code that interprets a
    declaration. `activity.IsMutating` decides which rpcs must declare one:
    every rpc except those named with a read verb (`Get`, `List`, `Watch`,
    `Search`, `Query`, `Count`, `Describe`, `BatchGet`) and those marked
    `idempotency_level = NO_SIDE_EFFECTS`.
  - **The interceptor.** `activity.Interceptor` fills the new
    `serverkit.ChainDeps.Activity` slot, after auth and next to the handler. It
    reads the declaration from the method descriptor. When the handler
    succeeds, it writes the event best-effort, taking the actor from the
    caller's claims and `via` from the `Forge-Via` header.
  - **Failures and defaults.** A failed write is logged and counted
    (`forge_activity_write_failures_total`) and never fails the rpc. The default
    slot logs events without storing them. `serverkit.NoActivity` drops the
    slot.
  - **Transactional writes.** `writer: ACTIVITY_WRITER_HANDLER` hands the write
    to the handler: it calls `activity.Record` inside its `RunTx`, so the event
    commits or rolls back with the mutation. `activity.Record` is also the
    explicit write for actors that are not rpcs: a converger, an alert
    transition, CI.
  - **Storage.** `activity.NewStore(db)` writes and lists the `activity_events`
    table, whose DDL is `activity.SchemaSQL`. `List` filters by actor, verb,
    target, env, via and time, and pages by keyset. The store writes through
    `*orm.Client`, so a write inside `RunTx` joins the transaction.
  - **Docs.** The new `activity` skill covers all of the above.
  - **The lint.** `forgeconv-activity-declared` (`forge lint --conventions`,
    error) fails a mutating rpc with no declaration and any malformed
    declaration, such as a path that does not resolve. `forge generate` records
    each rpc's verdict from `activity.IsMutating` and `activity.Resolve` in
    `gen/forge_descriptor.json` (`Activity`), and the lint reads it there, so
    the lint and the interceptor apply the same rules. **Upgrading:** every
    custom mutating rpc in an existing project needs an `activity` declaration
    or `activity: { none: "<why>" }`.
- **Dashboard spec: `bar`, `heatmap` and `sql` panels, `Panel.Formula` and
  `Panel.Limit` (`forge/pkg/dashspec`).** `bar` is a top-N chart (one group
  per `groupBy` value, the `limit` largest by the first series, default 10);
  `heatmap` buckets one numeric field's values over time (one `count` series
  naming the field); `sql` carries one read-only SELECT in `sql`, checked by
  the new `CheckSQL`, which is the alert-rule guard (`alerting.ValidateExpr`),
  and may bind the dashboard's scope through the `SQLParam*` ClickHouse
  parameters (`{from:DateTime64(9)}`, `{to:…}`, `{step:UInt32}`,
  `{env_ids|envs|services:Array(String)}`). `formula` makes a line or number
  panel plot `A / B * 100` over its series (by position, A to J), parsed and
  evaluated by `ParseFormula` / `Formula.Eval` so every reader computes the
  same value (`x / 0` is 0, as in `ratio`). `limit` (1 to 200) keeps the top
  rows of a table or bar. The validator locates every misuse, and a `sql`
  panel's `signal` is optional (`Panel.Signal` is now `omitempty`; every other
  kind still requires it, so existing output is unchanged). HyperDX has no
  equivalent for any of them, so `RenderHyperDX` leaves those panels out, as
  it does the console kinds. `CheckField` now also requires an attribute
  field's key to be 1 to 128 characters (`span.attr.` alone was accepted),
  the rule the Reliant control plane's copy enforced.
- **`forge/pkg/serverkit/drainbudget`: the one reader of `PRE_STOP_DELAY`
  and `SHUTDOWN_TIMEOUT`.** `Parse` (a Go duration or bare seconds; empty is
  unset), `Effective` (zero is the default, negative is none), the env names
  and defaults, and `FromEnv(getenv)`, which returns a `Budget` whose fields
  map onto `serverkit.Config` and errors naming a malformed variable. The rule
  lived unexported in `pkg/deploy`, so a hand-written main copied it, and the
  copy drifted (reliant read `-1s` as malformed). serverkit's `Drainer` and
  the rendered `terminationGracePeriodSeconds` both use it now, and a test
  pins that the grace period equals the budget `FromEnv` reads. Standard
  library only: `pkg/deploy` imports it without linking serverkit.
- **`crud.Repo`'s by-PK statements bind their values, so their text is
  stable.** bun formats every value into the SQL text, and under pgx's
  default statement cache (what `serverkit.OpenDB` opens) each distinct text
  is a new prepared statement: a Get of a new id or an update carrying a new
  value paid an extra Parse/Describe round trip and churned the connection's
  512-entry cache. `Get`, `Update`, `UpdateMasked` and `Delete` now build the
  same bun statement with `$N` markers and run it through `orm.Context`'s raw
  passthrough with the values as bind parameters (each rendered by the
  column's bun appender, so arrays, JSON, bytea, times and enums keep bun's
  semantics): one prepared statement per entity and mask.
  `BenchmarkRepoByPK` (pgx, one session): statements prepared per call
  1.0 → 0.003 for `Update`/`UpdateMasked` and 0.21 → 0.003 for `Get` over 64
  ids; 8.2–9.0 KB → 4.6–5.6 KB per call; allocations 66–79 → 68–86. A model
  implementing a bun model hook stays on bun's executor; bun query hooks
  (bunotel) no longer see these statements, and the repo's own
  `orm.<Op><Entity>` span carries `db.statement` (without values). The `db`
  skill documents the pool mode: keep pgx's default statement cache.
- **`orm.InTx(ctx)` and `orm.Context.BeginDetached`: probe for, and hold, a
  transaction outside `RunTx`'s closure.** `InTx` reports whether ctx carries
  an active transaction (replacing the `AfterCommit(ctx, noop) == nil` probe).
  `BeginDetached(ctx, orm.TxOptions)` — on `*Client`, `*Tx` and the injected
  `orm.Context` interface — opens a new, caller-owned transaction at the given
  isolation that outlives the call that opened it, which a transaction-scoped
  advisory lock released later needs. It never joins the ctx's transaction,
  never retries, and is rolled back when its ctx is cancelled. A hand-written
  `orm.Context` fake must add the method. Documented in the
  `service-layer/transactions` skill.
- **`pkg/pgtest` hands out pgx handles, the driver production runs.** Every
  `*sql.DB` from `pgtest.New`, `NewClone`, `NewFromTemplate`, `NewAtURL` and
  the template build callback is now opened with `pgtest.DriverName`
  (`"pgx"`, jackc/pgx's stdlib adapter) instead of lib/pq, so a DB test runs
  the same array/time scanning, statement cache and error types
  (`*pgconn.PgError`, not `*pq.Error`) as `serverkit.OpenDB`. `orm.NewClient`
  opens pgx too. `forge lint --fixture-drift`, which classified a rejected
  fixture only from a `*pq.Error`, reads `*pgconn.PgError` as well. A test that
  asserted `*pq.Error` from a pgtest database should match `*pgconn.PgError`
  or the driver-neutral `SQLState()` method.
- **`forge lint` catches SQL written for the wrong placeholder dialect.** The
  raw passthroughs (`db.Exec` / `Query` / `QueryRow`) take postgres `$N`;
  everything bun formats (`db.Bun().NewRaw`, the builders' `Where` / `Set` /
  `ColumnExpr` / …, `bun.SafeQuery`, `db.Bun().ExecContext`) takes `?` / `?N`
  and treats every unescaped `?` as a placeholder. Two new convention rules,
  both errors, read string literals and named constants passed to those calls:
  `forgeconv-bun-dollar-placeholder` (a `$1` bun never binds) and
  `forgeconv-bun-literal-question-mark` (a `?` in a SQL comment or string
  literal, or the jsonb `?` / `?|` / `?&` operators, that would consume an
  argument and shift the rest). The `db/crud-overrides` skill documents both
  dialects and the `\?` escape.
- **`testkit.NewPostgresDB`, `NewMigratedPostgresDB` and `LoadFixture` take
  `testing.TB`**, so a benchmark gets the same migrated, isolated database a
  test does. Callers passing `*testing.T` are unchanged.
- **`pgtest.NewFromTemplate` reaps stale templates.** A template used to live
  as long as its server, which on an external `FORGE_TEST_POSTGRES_URL` server
  (shared, never torn down) meant one leaked database per migration edit,
  forever. Each process that uses a template now stamps it
  (`COMMENT ON DATABASE`), and a process's first `NewFromTemplate` on a server
  drops every template unused for longer than `pgtest.DefaultTemplateTTL` (24h;
  `FORGE_TEST_TEMPLATE_TTL` overrides, `0` disables) and every `*_build`
  database whose builder is gone, each under its key's advisory lock. An
  unstamped template from an older forge is adopted, then reaped a TTL later;
  a template reaped under a process that had cached it is rebuilt.
- **`crud.NoReadBack()`: an exec-only `Update` / `UpdateMasked`.** Every
  update reads back, via `RETURNING`, each column it did not write plus every
  GENERATED column, so the entity holds the stored row. For a write whose
  caller discards the entity that is most of the cost: stamping one column of
  a 12-column row went 93 → 39 allocations and 9.5 → 6.5 KB with the option
  (`BenchmarkRepoUpdateMasked_OneColumnStamp`). `Repo.UpdateMasked` now takes
  `...UpdateOption` (Preserve is rejected there: a mask already names what is
  written). The default is unchanged.
- **The composition graph: a new project's `internal/app` is typed, generated
  Go in which every part of the process is a node** (composition graph, PR 3).
  A provider is any `func New<X>(<X>Deps) (T, error)` in the owned
  `providers.go` — the scaffold ships the database pool and ORM, the outbound
  HTTP client, the user audience's `UserAuth` / `UserChain` / `APIListener`,
  and pprof — and it is a node exactly like a handler, package, worker or
  operator. Every Deps field resolves **by type** at generate time (a graph
  input, a config block, a framework seam, or the one node whose value is
  assignable; identity is the Go type, never a name). The owned `compose.go`
  is one constructor method per node, `func (g *Graph) newX() (T, error)`,
  whose value expressions are the graph's edges: forge adds a missing method
  and a missing Deps key and never touches a value. The generated
  `graph_gen.go` holds a memoized accessor per node (`g.X()`), and per root a
  `Build<Root>` that constructs exactly the closure the root's fields reach
  and then seals the graph, and a `Run<Root>` that supervises it under
  `lifecyclekit.Supervise` — each node's Run/Drain/Close/Ready bound in a
  `lifecyclekit.Node` literal read off its type by go/types at generate time.
  A root is a `//forge:root <name>` struct in `internal/app`; with none
  declared, forge generates `Server` — every runnable node nothing depends on
  (the API listener, the workers, the operators) — which is what
  `<bin> server` runs. A missing provider, an ambiguous match (until
  `compose.go` names one), a dependency cycle (named edge by edge; a
  `func() T` field is a lazy edge that breaks it) and a root field nothing
  produces are generate errors. The generated `routes_gen.go` replaces
  `mounts_services_gen.go`'s `MountAll`: one `<Audience>Routes` route set per
  audience (`UserRoutes`, `DaemonRoutes`, …), each a node whose Deps are its
  services, with `MountOptions` per-service options set in `compose.go`.
  New runtime packages: `forge/pkg/graphkit` (`Get`, `Memo`, `Builder.Build`,
  `Builder.Nodes`, `Builder.Supervise`, `Catch`) and, in `serverkit`,
  `Probes` (`/healthz`, `/readyz` over a Drainer and checks that may be
  added after the handler is built, `/metrics`), `Edge` (the CORS /
  security-headers / request-id layers, fail-closed), `Pprof` and
  `SetupTelemetry`.
- **A project scaffolded before the graph keeps generating exactly as
  before.** forge treats an `internal/app` that declares `type Infra struct`
  or `func NewComponents` as the legacy composition and runs the legacy
  generators unchanged (proven byte-identical on control-plane). To migrate by
  hand: move each `OpenInfra` field into a provider func, delete
  `compose.go`, `lifecycle.go` and the `Infra` struct, and re-run
  `forge generate`; the scripted migration is composition graph PR 7.
- **A new project has no per-service, per-worker or per-operator
  subcommands** (design §9.5): `cmd/<bin>/main.go` is a bare `cmd.Execute()`,
  `server` runs the graph's server root, and `serve.go` is the process policy
  around it (config, logger, boot probes, migrate, telemetry, the readiness
  gate and stop budget). Splitting a process is a new root, not a new
  subcommand; one cobra command per root is composition graph PR 4.
- **`serverkit.Listener` and `serverkit.Chain`: the listener and the
  interceptor chain are components, and a process can run several of each**
  (composition graph, PR 1). `serverkit.NewChain(ChainDeps{Auth, …})` is the
  chain the scaffolded `serve.go` used to spell inline — trace → recovery →
  request-id → logging → metrics → auth → audit → rate-limit → extras →
  stream-drain → protovalidate, plus the 4 MiB payload caps. Every slot has a
  working default except `Auth`, which is required (nil is a boot error); a
  default is dropped only by its `Omit` constant (`serverkit.NoAudit`,
  `NoTrace`, `NoStreamDrain`, `NoValidate`). `Auth` is a
  `serverkit.Authenticator` — the interceptor plus the audience it admits
  (`serverkit.AuthFor(serverkit.AudienceUser, interceptor)`).
  `serverkit.NewListener(ListenerDeps{Config, Chain, Routes, Edge, Front, REST})`
  is one address with its own `ListenerConfig` (TLS, timeouts, HTTP/2 stream
  cap, `DrainReserve`, `RedialOnDrain`), its own `Streams`, and the lifecycle
  `Run` / `Drain` / `Close` / `Ready`. `serverkit.Run` is now one `Listener`
  plus the process policy around it, with no behaviour change: an existing
  `serve.go` keeps working untouched, and `Server.Chain` is the new optional
  field a new one sets.
- **`option (forge.v1.service) = {audience: "daemon"}` declares which kind of
  caller a service serves** (default `"user"`), and a listener refuses to
  start when it mounts a service whose audience differs from its chain's
  `Auth.Audience()` — `serverkit.Run` checks the same over `Server.Chain`. A
  daemon credential can no longer reach a user RPC, even through a
  hand-written route set. The audience is in `gen/forge_descriptor.json`
  (`Audience`), in `forge project annotations`, and readable at runtime with
  `serverkit.ServiceAudience`.
- **`lifecyclekit.Supervise(ctx, policy, nodes...)` runs a process's
  components in order and stops them in reverse** (composition graph, PR 2).
  A component implements whichever of the four single-method interfaces it
  has — `Runner` (`Run(ctx)`), `Drainer` (`Drain(ctx)`), `Closer`
  (`Close(ctx)`), `Readier` (`Ready(ctx)`); `lifecyclekit.Component(name, c)`
  makes it a `Node`, and `lifecyclekit.WorkerNode(w)` adapts today's
  `serverkit.Worker` (Run is `RunContext` or `Start`, Close is `Stop`). Start
  runs in list order and waits for each node that reports `Ready` before the
  next, then marks the process ready. The stop is the root's `Policy`:
  readiness flips and the pre-stop pause runs (`serverkit.Drainer`), then in
  reverse order each node drains, has its `Run` context ended and awaited, and
  closes, inside one shutdown budget; `Policy.Flush` (the telemetry flush) runs
  last. Each node writes a `drain: node stopped` line and a `phase=node` sample
  of `forge.server.drain.duration`. A hand-written main can run two
  `serverkit.Listener`s — a user API and a daemon gateway — under it today.
  `serverkit.Drainer.Phase` is exported for the same phase lines.
- `forge doctor`'s payload-cap check reads a `serverkit.NewChain` call as
  capped (it applies the caps itself) and fails a negative cap, which is
  `NewChain`'s spelling of "no cap".
- **`pkg/identity` sign-in codes converge instead of churning.** With
  `Options.SignInCodes`, every pass used to mint a fresh password-reset code for
  each credential-less member, which invalidated the code the caller had just
  delivered and made no pass quiet. `Options.HeldSignInCode` now tells the
  library which members still hold a live code, and those keep it.
  `SignInCode.ExpiresAt` (minting time plus `SignInCodeLifetime`, Zitadel's
  default one hour) says when to re-issue, and `Report.Credentialed` names the
  members confirmed to hold a credential, so a caller can drop their codes.
- **`forge project new --link-forge` finds the forge checkout without a build-time
  path, and takes `--forge-root`.** The bridge needed the checkout this binary
  was built from, recovered from an ldflag or the source path the compiler
  bakes in. Both are erased by `-trimpath`, which the Reliant workspace image
  sets as the Go default (`GOFLAGS=-trimpath`), so `--link-forge` refused for
  every forge built there, and `TestRunNewInPlace_ExistingRepo` and tierguard
  failed unless run with `GOFLAGS=-buildvcs=auto`. The checkout is now
  resolved, first match winning, from `--forge-root <path>` (or
  `FORGE_ROOT`), the ldflag, the compiled source path, the
  `replace github.com/reliant-labs/forge => ../forge` that a host binary such
  as `reliant` was built with (resolved against the directories the binary and
  the process live under), the checkout the executable is inside, and the
  checkout forge was started in. A stated path that is not a forge checkout
  (no forge `go.mod` and `pkg/`, or a module-cache copy) is an error, and the
  refusal when nothing is found names `--forge-root`.
- **A StatefulSet's declared volume size can grow, and the deploy converges it.**
  `volumeClaimTemplates` are immutable, so raising a declared size (a
  `clickhouse_disk = "100Gi"` that was `"50Gi"`) used to fail every apply with
  `updates to statefulset spec for fields other than ... are forbidden`, and the
  only way out was a hand-patched PVC with the KCL left at the old value. Direct
  apply now recovers that rejection the production way: it grows each existing
  claim of the grown template (grow-only, after checking the claim's StorageClass
  has `allowVolumeExpansion`; claims already at or above the size are left
  alone, including ones expanded by hand ahead of the declaration), deletes the
  StatefulSet with `--cascade=orphan` so its pods and claims keep running, and
  re-applies, which recreates the object and adopts the pods — no restart when
  the pod template is unchanged. A shrink, a changed storage class, access mode
  or label, an added or removed template, or a change to another immutable field
  (judged by a server-side dry run with the live templates swapped in) is
  refused with the reason and writes nothing. The logic is the new public
  `pkg/stsresize` (`Decide` is pure; `Converge` runs it over a small `Cluster`
  interface), so a reconciler's converger can run the same procedure before it
  hands a revision to Flux: kustomize-controller dry-runs every object of a
  Kustomization before applying any, so one such StatefulSet otherwise stops the
  whole unit converging, and its per-object force would delete the pods.
- **A raised StatefulSet volume now converges under Flux too: forge renders a
  pre-rollout resize Job.** Direct apply could repair the rejection after the
  fact; a reconciler cannot (Flux dry-runs every object of a Kustomization before
  applying any, so one raised `volumeClaimTemplates` size wedged the whole tree,
  and forge was not in the loop). The render now carries the repair: for each
  (cluster, namespace, delivery unit) that holds a StatefulSet with
  `volumeClaimTemplates` it emits a `sts-volume-resize-<unit>-<hash>` Job, with a
  ServiceAccount, Role, RoleBinding and NetworkPolicy, in the unit's `pre` phase.
  The bundle's `pre` tree is what the `rollout` tree waits on and direct apply
  gates workloads on the same Job, so it runs before the StatefulSet is applied
  by either. It grows each claim smaller than the declared size, then deletes the
  StatefulSet with `--cascade=orphan` so the rollout recreates it with the new
  templates and adopts the running pods; it is a no-op when the sizes match or
  the StatefulSet is absent, and idempotent (the Job name hashes the declared
  volumes). A shrink, or any change to a template other than a larger storage
  request, exits 2: the Job's `podFailurePolicy` fails it at once and the deploy
  stops with the reason. The Job's RBAC is namespace-scoped (`get`/`delete` on
  the named StatefulSets, `get`/`list`/`patch` on PVCs, nothing cluster-scoped);
  whether a claim may grow is learned from a server-side dry-run patch before the
  first write. The script is embedded in `pkg/stsresize` (`Script`, `Build`) and
  tested against `Decide` over one table so the two cannot drift. The image is a
  digest-pinned `alpine/k8s`; `Bundle.statefulset_resize = forge.StatefulSetResize
{image = ..., disabled = True}` replaces or removes it. It is not rendered for
  `lifecycle = "local"`/`"ephemeral"` envs. `forge env deploy`'s preflight now also
  compares each rendered StatefulSet's volumes with the live ones and blocks a
  shrink (or any unconvergeable template change) by name before anything is
  applied (`volume_resize_refused` in `--json`). See `deploy/statefulset-volumes`.
  An env with a StatefulSet gains a `pre` tree and its objects on its next bundle.
- `forge cloud identity status|users|invite <env>`: inspect hosted managed identity,
  page/search users, and email an invitation with explicit `--email` and repeatable
  `--role`. Uses the authenticated org and exact project/environment lookup;
  invitations require a human org admin. No passwords or credentials are printed.
- **KCL: `forge.Frontend.telemetry = forge.BrowserTelemetry {funnels = [forge.Funnel {...}]}`
  declares product funnels.** A `forge.Funnel` is
  `{id, title?, steps, window = "1h", by = "session"}`: 2 to 10 distinct event names (the names the app passes to
  `track()`), a `window` of a number and a unit of s/m/h/d, counted per browser
  `session` or `user`. `forge generate` reads every env's frontends and draws each
  declared funnel as a `funnel-<id>` panel on the project's frontend dashboard
  (`deploy/observability/dashboard-spec_gen.json`, and the release bundle's
  dashboards). A funnel id declared by several envs is one funnel if the definitions
  match, and an error if they differ. Not allowed on a `rn` frontend.
- **KCL: browser telemetry knobs (frontend-observability F4a).**
  `forge.Frontend.telemetry = forge.BrowserTelemetry {...}` gains
  `console_capture` (default `True`; `False` renders `CONSOLE_CAPTURE = "false"`
  into the runtime config), `replay_sample_rate` (default `1.0`, 0..1; below 1
  renders `REPLAY_SAMPLE_RATE`), `sourcemaps` (`"publish"` | `"public"` | `"off"`,
  default `"publish"`: the frontend's source map handling, previously
  unreachable from KCL) and `errors = forge.FrontendErrors {ignore = [...]}`
  (exact exception type, message substring, or `re:` RE2 over the message; an
  issue that matches is born ignored). `forge.ObservabilityRetention` gains
  `replay_days` (14, max 30), `errors_days` (30) and `product_events_days` (90).
  The generated `otel_gen.ts` of both web scaffolds sends the frontend's name
  (`forge.frontend`) and reads the two runtime knobs from `config.js`, so one
  build promotes across envs.
- **A `forge.OnHosted` frontend ships browser telemetry by default.** Its
  runtime config gets `OTEL_ENDPOINT = "/_otel"` (the platform answers it on the
  site's own origin) unless the author named an endpoint (`runtime_config`,
  `config.otel_endpoint`, or the build variable) or switched `signals.browser`
  off. Before, a deployed build sent nothing until the endpoint was declared by
  hand. Other runtimes are unchanged.
- **The release shape records the telemetry declaration.**
  `Shape.FrontendTelemetry` (per frontend: source map handling, error filters,
  funnels) and `ShapeObservabilityPolicy.Retention` are written only when they
  differ from the defaults, so no existing shape or config digest moves. A
  control plane strict-decodes the shape, so both are one negotiated bundle
  feature (`shape-frontend-telemetry`, `BundleFeatureFrontendTelemetry`), and
  `release.BundleReader.LimitShape` leaves them out for a control plane that
  does not name it: on the bundle document, and on the environment declaration
  `forge env build|deploy` send, which asks `GetCapabilities` only when there
  is something to withhold.
- **`forge/pkg/dashspec`: the dashboard spec and its validator are public.**
  The `forge.dashboards/v1` types (`Dashboard`, `Panel`, `Series`, the kinds,
  aggregates, operators and units), `ParseDashboard` (every problem located by
  line, column and path), `ProblemsOf`, `CheckField` and `IsConsoleKind` moved
  out of `internal/obsdash`, which aliases them and is otherwise unchanged. A
  module that reads forge's dashboards validates with forge's own rules
  instead of a hand-kept copy: the control plane's copy rejected every
  dashboard forge#880 generated until it was patched by hand.
  `internal/obsdash`'s HyperDX renderer refuses a field through `CheckField`,
  and a test holds the two to the same vocabulary.

### Removed

- **The `enforce-component-observe` lint and `config.enforce_component_observe`.**
  It failed a package that had made no observability decision. Being
  instrumented is now the default decision, so there is nothing left for it
  to enforce. Delete the key if your forge.yaml sets it.

### Fixed

- **forge rides out a k3s server that crashes while a k3d cluster boots.**
  k3d waits for a server's "k3s is up and running" log line and gives up the
  moment the container restarts (`stopped returning log lines: … status=restarting`),
  although Docker's restart policy brings k3s back. forge now keeps a new
  cluster's nodes on failure (`--no-rollback`, only for a name that did not
  exist), and when a server restarted it deletes the half-made cluster and
  creates it once more, printing the server's exit and last log lines. Any
  other failure is rolled back and returned as before; a second crash fails
  with both reasons.
- **`forge lint ./internal/x/...` no longer reformats packages you did not name.**
  The auto-fix pre-pass (`gofmt`/`goimports`, then `golangci-lint --fix` and the
  frontend formatter) honoured `--scope`, but ignored the positional package
  paths of `forge lint [paths...]`: naming one package still rewrote every Go
  file in the project, including ones another agent was editing in the same
  checkout. The Go format pre-pass now touches only files under the named
  paths (a whole-module pattern such as `./...` is still the whole tree), and
  the frontend formatter, which cannot be confined to Go paths, does not run
  when paths are named. A bare `forge lint` still auto-fixes the whole project,
  `--scope` still confines it to the scope, and `--no-fix` and `--json` still
  write nothing.
- The `wireRelease.Commits` comment in `internal/cli/hosted_ledger.go` named
  `DeployRelease.commits` as tag 13; the control plane's proto declares it as tag 14.
- **`lint.rules` in `forge.yaml` now re-grades every `forge lint --conventions`
  finding, not only `forgeconv-frontend-process-env`.** The dial was applied to
  that one rule's findings and ignored for every other, so
  `lint.rules: {forgeconv-activity-declared: warn}` was accepted by the config
  loader and then did nothing: the rule stayed an error, and there was no way
  to adopt it incrementally. `off`, `warn`, `error` and the `*` wildcard now
  behave the same for every proto and Go-tree convention rule, after
  `forge:lint-disable` suppression.

- **Parallel `forge cluster up`s no longer lose each other's kubectl
  context.** `k3d cluster create`, `k3d kubeconfig merge`,
  `k3d cluster delete` and `kubectl config use-context` each rewrite the whole default
  kubeconfig with no lock, so two forge processes creating clusters at once
  dropped one context, and the loser failed on `context "k3d-<name>" does not
exist` for a running cluster. forge now creates with
  `--kubeconfig-update-default=false --kubeconfig-switch-context=false` and
  makes every kubeconfig write under one cross-process flock beside the file
  (`<kubeconfig>.forge.lock`). The declarative cluster path also stops
  silently switching your current context to the cluster it created.
- **A graph-composed project's scaffolded e2e harness starts `<bin> server`.**
  It ran `<bin> <svc>`, a per-service command that only legacy Components
  projects have, so every new project's e2e suite failed with `unknown
command`.
- **`forge generate` drops a removed Deps key from a compose.go construction
  of a package that is no longer a graph node** (marked
  `//forge:external-component` or `//forge:not-a-component`). The graph
  reconcile only measured node constructions, so the dead key failed every
  generate's validate build (#747, again in graph mode); the construction is
  kept and reported with the graph's remedy.
- **`forge ci verify-generated` and `forge generate --check` never write the
  checkout.** Both regenerated the caller's own tree and then read
  `git status`, so a concurrent gate held every reader of the tree back for
  30-50s, and a run that found drift, crashed or was killed left the
  regenerated files behind. They now regenerate a scratch copy made beside
  the repository (`.<repo>.forge-verify-<pid>-*`: its own git repository
  borrowing the checkout's objects, holding the checkout's index and working
  tree byte for byte, plus the gitignored files generate reads) and diff the
  copy against itself; output and exit codes are unchanged. They judge the
  working tree as `git add -A` would commit it — HEAD on a clean checkout — so
  a dirty tree now gets a verdict instead of a refusal, and every change
  reported is generate's. The generate cache is shared both ways: the
  checkout's record is copied in, and a fixed point the scratch run verifies
  is copied back to `.forge/generate-cache.json`. The cache key also stops
  including `FORGE_SLOT_HELD`, a fresh token per slot holder, which made every
  run inside the gate a miss. `forge ci gate` no longer runs verify-generated
  alone, skips it on a dirty tree, or resets the tree after it fails.
- **`forge slot run` and read-only commands no longer edit `.git/config`.**
  Every forge command pointed the generated-file merge driver at itself
  (`.git/config`, `info/attributes`) and activated forge's hooks
  (`core.hooksPath`) as a side effect, so `forge slot run`, `env status`,
  `env render` and `env deploy --dry-run` rewrote the config every linked
  worktree shares. Only commands that write generated or state files do it
  now (`generate` without `--check`/`--plan`, `scaffold`, `project disown`,
  `project delete service`, `package new`). And two copies of the pinned forge
  at different paths (each worktree's tool-cache install, a `go install`ed one)
  no longer take the driver from each other on every run: the copy already
  registered keeps it while it exists.
- **`forge env deploy <env> <version> --frontends-only` no longer renders or
  compares the env's bundle.** It applies no bundle object, yet it rendered
  the whole env and refused when a newer checkout's workloads differed from
  the release's bundle of record — so a release's frontend could not ship
  after its backend had (control-plane prod, 2026-10-19). It now records no
  bundle and computes no k8s plan; its promotion is still confirmed and
  scoped, and the frontend still builds from the release's recorded sources.
  A `--target` naming only frontends shipped to Firebase or a bucket is
  treated the same; any target that renders cluster objects keeps the check.
- **A release's bundle reproduces from its own commit and forge.** A
  hand-composed `forge.image_ref_for(<repo>, image_tag)` rendered
  `<repo>:<release>@<digest>` at the cut, whose build state held a digest
  under `<repo>:<release>`, and `<repo>@<digest>` on any later redeploy,
  whose build state did not — so the versioned deploy refused its own
  release's bundle. A named release now pins its images under the release
  tag from the release alone (`releaseVersionTag`, the one rule the build's
  push tag, the render's `image_tag` and the pins share).
- **A Firebase or bucket frontend that fails before its upload reports
  nothing shipped.** A failed `npm run build` (or assemble, source-map
  filing, asset retention, config write) was reported as
  `PARTLY … failed part-way; some of it may be live`, although the host was
  never written. Those failures now carry `cluster.ErrNothingApplied`, which
  the deploy's ship log reads, unless an earlier frontend of the same group
  already shipped.
- **`lifecyclekit.Supervise` no longer reports a node as stuck because an
  earlier node spent the shutdown budget.** Nothing waited for a node's `Run`
  after its cancel once the budget was gone, so every node behind a slow one
  logged `node did not stop within the shutdown budget` at ERROR (and
  `run_still_running`) even when it returned instantly. Each `Run` now gets
  250ms to return after its cancel; only one that really ignores its context
  is reported. The walk overruns the budget by at most 250ms per truly stuck
  node.
- **`forge storage` worktree reaping no longer holds a worktree for forge's
  own scratch, a project's pinned tools or its `tmp/`.** The rebuildable
  allow-list gains `.forge/tmp`, `.forge/generate-cache.json`, `/.tools` and
  `/tmp`; a leading `/` anchors an entry to the worktree root (`web/tmp` is
  still unrecognized data), and the never-list still wins (`tmp/data`,
  `tmp/.env`, `*.db`). A git-ignored repository that is a direct child of
  `<worktree>/.tools/pre-commit/` (pre-commit's hook clones, which
  control-plane's gate creates) no longer makes the worktree a
  `nested-repository`; every other nested repository, including one inside
  `tmp/`, still holds. Mirrors reliant's `worktreereclaim` list.

- **`serverkit.Listener` loads its TLS certificate in `Listen`, not `Run`.**
  `ServeTLS` read the pair only once `Run` started serving, so a missing,
  malformed or mismatched certificate failed after `Listen` had bound and
  `Ready` had reported the listener up — `lifecyclekit.Supervise` had already
  started the next node. `Listen` now loads and validates the pair before it
  binds and returns an error naming both paths; `Run` serves the loaded
  certificate. `serverkit.Run` gets the same boot-time failure.
- **`pkg/lease`: a slow reply no longer drops every lease on a healthy session.**
  Every query on the lease session (the heartbeat, each standby poll, the
  connect) ran under a deadline of `HeartbeatInterval`, and a query that misses
  its deadline makes pgx close the connection, so the server drops the session
  and every advisory lock on it. A round trip longer than one heartbeat interval
  (a CPU-starved process, a throttled pod, a busy database) therefore dropped
  every lease the Locker held and forced a failover. It flaked
  `TestManyLeasesOnOneLockerSurviveConcurrentPollingAndHeartbeats` on loaded CI
  runners (`leader holds 0 of 20 leases`), and with control-plane-workers' 5s/5s
  timing a 5s stall did the same to its ~15 leases. The new
  `lease.Options.QueryTimeout` (default `DefaultQueryTimeout`, 10s) bounds each
  query separately from how often the heartbeat runs. Overlap after a partition
  is now bounded by `HeartbeatInterval + QueryTimeout`.
- **A hosted bundle carries only the document fields its control plane
  decodes.** forge#850 added `provenance.commit_subject`/`commit_author` and the
  per-object `objects` summaries to the bundle document, and a control plane
  strict-decodes that document: every control plane built before #850 refused
  every bundle
  (`RecordBundle: bundle document: json: unknown field "commit_subject"`), so no release could be recorded — including the one that
  would ship the control plane that understands them. An optional document
  field is now a FEATURE (`release.BundleDocFeatures`): `forge env deploy` /
  `env build` ask `DeployService/GetCapabilities` (`bundleDocumentFeatures`)
  before building a hosted bundle and leave out every part the server did not
  name; a server without the RPC or the field gets the document its decoder
  knew. A control plane advertises `release.BundleDocFeatures()` from the same
  forge it decodes with. `TestEveryBundleDocAdditionIsAFeature` fails on a new
  document field until it is mapped to a feature.
- **`forge env config` reports missing secrets instead of failing.** forge#859
  routed it through `env exec`'s resolver, and with it the fail-closed secret
  check: reading back an env whose secret store lacked one declared key printed
  nothing and exited 1. It now prints everything that resolves, leaves the
  missing variables out, lists them on stderr and under `missing_secrets` in
  `--json`, and exits 0. `forge env exec` and `env up` still refuse, since
  running without a declared secret is the real error; `env config --strict`
  restores the refusal for a caller that wants it.

- **A dependency outage no longer floods the logs at probe frequency.**
  `Drainer.ReadinessHandler` (and so `serverkit.Run`'s `/readyz`) logged every
  failing check at ERROR on every probe — every few seconds, on every replica,
  for the length of the outage, into the log pipeline and the error tracker.
  A check is now logged once at WARN when it starts failing and once at INFO
  when it recovers (with `failed_for`); probes in between log at DEBUG. Never
  ERROR: readiness failing is the probe doing its job.
- **The dev Zitadel moves to v4.19.5** (was v4.19.4) in host-infra, the compose
  `idp` service and its per-platform checksums (taken from the release's
  `checksums.txt` and re-computed from the downloaded archives). 4.0.0–4.19.4
  carry three high advisories: GHSA-mchc-246r-6m76, GHSA-84fg-x5h4-w8xv and
  GHSA-qj3q-235g-qr6w (Login V2 link-host poisoning, a forgeable Login V2
  verification proof, and device-code approval without consent). The dev IdP
  serves no Login V2 and registers no device-code app, but a dev pin that lags
  the deployed one is drift. No config or steps changes are needed. Upgrade
  notes worth knowing: list endpoints now enforce `DefaultQueryLimit` (100,
  v2) and `MaxQueryLimit` (1000, v1 and v2), and a malformed
  `x-zitadel-public-host` is now rejected instead of ignored.

- **Adapter scaffolds use typed configuration from birth (#748).**
  `forge scaffold adapter <name>` (and `scaffold package --type adapter`)
  appends a `<Name>Config` message with a `<NAME>_BASE_URL` leaf to the config
  proto and composes it on `AppConfig` using a free field number. The adapter
  takes the generated config block; compose.go fills it from the loaded config
  by type instead of emitting `BaseURL: ""` with a TODO. Its httptest fixture
  sets the block's base URL to the stub. Re-running is a no-op; reserved tags
  and existing fields are preserved. Missing `AppConfig` is an actionable
  refusal before any adapter files are written, not a scalar fallback that
  violates `forge-config-deps`.

- **Go initialisms are replaced only as whole PascalCase words (#795).**
  `ToPascalCase` matched initialisms by prefix, so `forge scaffold service
identity` produced `IDentityService` and an `id-entity` file that `generate`
  then rejected; `Slack`, `Uint`, `Ramp`, `Idle` and `Ion` were mangled the
  same way. An initialism now applies only when followed by an upper-case
  letter, a digit or the end of the name (`identity` -> `Identity`, `user_id` ->
  `UserID`, `http_url` -> `HTTPURL`). Plurals are not initialisms: `ids` ->
  `Ids`, as in staticcheck ST1003 and protoc-gen-go. A proto service that
  spells an initialism as a word (`ApiKeyService`) now matches its directory
  (`api_key`): the check compares logical names, the Connect selectors in an
  existing `service.go` follow the proto spelling, and the fix hint never names
  the current state. Generated identifiers that contained a mangled prefix or
  an `...IDs`/`...URLs` plural change on the next regenerate.

- **Frontend-only scaffolds need no database or cluster (#401).** Disabled
  migrations no longer declare a migration job. Database-free frontend projects
  get host dev servers and hosted static staging/prod, with no database, IdP or
  unused cluster. `project new --frontend` accepts `--output` and `--routes`;
  frontends added later use the same named declaration and rebindable line.
  `env new --bind` validates the entire copy before writing, including under
  `--force`. Offline static admission uses explicit preflight addresses without
  relaxing live deployment's requirement for a pullable release artifact.

- **`project upgrade` synchronizes Forge requirements in every project module
  (#762).** Root, `gen/` and other discovered Go modules upgrade before template
  changes; `--check` reports pin drift without running Go. Failed `go get` or
  `tidy` commands return an error, restore module files and print exact recovery
  commands instead of advancing `forge.yaml`. Workspace files and replacements
  are preserved.

- **A fresh project with a Next.js frontend passes its own npm vulnerability
  gate (#754).** Scaffolds require Vitest `^5.0.4`; Vitest 3 brought critical
  tinypool advisories and Vitest 4 crashes npm 10's installer. Next.js, Vite SPA,
  the dev web-runtime workspace root and web-runtime's own install now override
  only HyperDX recorder's exact `fflate` 0.7.4 pin to patched `^0.7.5`; npm does
  not apply overrides from a published dependency, so no web-runtime release is
  needed for scaffolds. `ci.vuln_scan.exemptions` now accepts npm GHSA ids with
  the same required reason and expiry, scope, stale and expired-entry rules as
  Go ids. The npm gate still fails on high/critical findings, reads npm's JSON
  report rather than its exit code, and fails closed on malformed output. A new
  Next.js frontend records a visible 90-day reviewed acceptance for the dev-only
  braces advisory, which has no fixed release.

- **Shipped skills fit Reliant's rendered delivery budget (#808).** The guard
  measures the production renderer with the longest shipped `reliant forge`
  prefix and reserves space for collapsed navigation and the completion footer
  inside the unchanged 24,000-byte limit. Start-here, deploy, DB, proto, CI and
  two migration guides now link to discoverable subskills for detailed teaching,
  leaving real room for growth without dropping their rules.

- **Cross-repo build sources no longer require sibling checkouts (#107).**
  `ShellBuild.source` and named Docker `build_contexts` accept `GitSource`
  pins through the frontend source cache and explicit local overrides. Shell
  source/cwd ambiguity and malformed pins fail rather than running in the
  project root; `forge doctor --verbose` distinguishes pins from filesystem
  references. The release-source check includes shell pins, and cache metadata
  no longer makes fetched checkouts appear dirty.

### Changed

- **Least-knowledge lint: the design's severities by default, and the two K3
  checks.** `forgeconv-deps-surface` (a foreign interface of more than ten
  methods with unused ones), `forgeconv-no-container-deps` and
  `forgeconv-deps-no-downcast` are now errors without `--strict`, as
  composition graph §7 planned one release after they landed as warnings; a
  fresh scaffold has none. New: `forgeconv-no-getenv` (a component package
  reading `os.Getenv` / `LookupEnv` / `Environ` — configuration its root's
  config reach cannot see) is a warning, an error under `--strict`; and
  `forgeconv-deps-root-config` (a Deps field holding the root
  `*config.Config`) is reported only under `--strict`, as a warning, because
  the scaffold still hands every component the root config. `--strict` makes
  every other least-knowledge finding an error.

- **The entity struct's types are idiomatic and lossless.** Field names spell
  Go initialisms in capitals (`ID`, `OrgID`, `APIKey`; the `<E>Field*` and
  `<E>Constraint*` constants follow); an integer column is as wide as it is
  declared (`INTEGER` → `int32`, `SMALLINT` → `int16`, `BIGINT` → `int64` —
  it was always `int64`); a `jsonb`/`json` column is `json.RawMessage` (nil
  for NULL), not `string`.
  `COMMENT ON COLUMN t.c IS 'forge:go-type=<type>'` declares the Go type of
  one column — a named enum
  over an integer, `map[string]any` over json, `<path>[;<pkg>].<Type>` —
  with no generated wire conversion (a CRUD rpc carrying it fails generate
  by name). The CRUD conversions follow: the wire's int32 enum number is
  stored at the column's width, json pairs through `[]byte`/`string`.
  **Upgrading:** regenerate; then rename references to the projected fields in
  hand-written code — `.Id` → `.ID`, `.UserId` → `.UserID`,
  `TaskFieldId` → `TaskFieldID` (`gopls rename`, or `gofmt -r` per name) — and
  adjust code that read an `integer` column as `int64` or a json column as
  `string` (`string(row.Payload)`, `json.RawMessage(s)`).
- **A `NOT NULL` column's `DEFAULT` applies when the field is unset — and an
  explicit zero is still stored.** The generated store wrote the Go zero for
  every defaulted column a caller left unset, so the DEFAULT never fired:
  `threads.status INTEGER NOT NULL DEFAULT 2` was stored as 0 by
  `CreateThread`, and `jsonb NOT NULL DEFAULT '{}'` failed the insert with
  `""` (reliant P5b). Bun's `default:` tag was never the answer — it reads
  every zero as unset and would turn an explicit `false` into a
  `DEFAULT true`. The entity type now carries the distinction
  (`codegen.ColumnInsertDefault`): a scalar whose DEFAULT is not its Go zero
  is a **pointer** field (`nil` = unset), and a column whose zero it can never
  hold keeps its type with `,nullzero` (a json document; an array with a
  non-empty DEFAULT; a scalar a CHECK keeps from its zero, which is every born
  enum column). `pkg/crud` gives unset one meaning on every write: `Create`
  omits the column, a full-replace `Update` and `Upsert`'s conflict branch
  keep the stored value; a `,nullzero` slice is no longer normalized to `{}`.
  The CRUD create op stops pasting the DEFAULT's literal into Go
  (`schemaDefaultAssigns` is gone): it leaves the field unset and the
  database applies its own DEFAULT, whatever expression it is. **Upgrading:**
  regenerate; a NOT NULL column whose DEFAULT is a non-zero scalar becomes a
  pointer field, so hand-written code that reads or writes it changes from
  `row.Status` / `row.Status = 2` to `*row.Status` / `row.Status = ptr(2)`;
  born enum (TEXT + CHECK) and json columns keep their types.
- **`database.orm.package` places the generated data layer.** The ORM always
  projected into `internal/db` (`package db`) and refused any name the
  project already declared there — so a project with its own data layer in
  `internal/db` (reliant: 30 of 55 names, `type Chat = core.Chat`, `Repo`,
  `Daemon`) could not have the store at all without renaming thousands of
  references first. `database: {orm: {package: internal/store}}` in
  forge.yaml puts every `<entity>_orm_gen.go`, `store_gen.go` and
  `_repo_ext.go` seam in that directory, declaring its package; the
  collision refusal and `//forge:no-orm` read that package. Every consumer
  follows it: CRUD wiring and test factories import it (as `db`), compose.go
  provides `<pkg>.Store` / `<pkg>.<Entity>Store` Deps fields,
  `forge project shapes --kind store`, and the drift guard and its hints.
  Moving the key reclaims forge's previous projection from the old directory (the
  `_repo_ext.go` seams are yours and stay). Default unchanged: `internal/db`.
  The collision refusal now offers the key. `generator.GeneratePlanORM` takes
  a `config.ORMConfig` in place of its two unused module/service arguments.
- **A view can be a read-only entity.** forge projected BASE TABLEs only, so
  a view's row type was hand-written (reliant: `chats_with_activity`, the one
  definition of a chat's activity). `COMMENT ON VIEW v IS 'forge:entity'`
  (regular or materialized; `key=<column>` names the key, else `id`) projects
  it: the row struct, `List<E>` / `Count<E>`, `Get<E>ByID` when it has a key,
  and the matching read-only store — no write is generated. Non-key columns
  are pointers (the catalog cannot say a view column is NOT NULL). Views
  without the marker are not projected. `schemadef.IntrospectViews` and
  `shadowdb.Views` read them from the same shadow apply as the tables.
- **The merge driver is set up per worktree, and forge says what it changed.**
  Any forge command run in a project (`forge db migration rebase` was the one
  noticed) pointed the repository's `merge.forge-generated.driver` at a forge
  binary and routed forge's generated and state files to it in
  `info/attributes`, silently, in the repository's common git directory that
  every linked worktree reads. The command names one forge binary, and
  worktrees are on different branches with different pins, so the last forge to
  run decided how every other agent's rebase merged. The command now goes in
  the worktree's own `config.worktree` (forge enables `extensions.worktreeConfig`
  once, which git has honoured since 2.20 and go-git accepts; it declines for a
  bare repository or an explicit `core.worktree`, and
  `FORGE_MERGE_DRIVER_SHARED=1` keeps the old shared placement), so a worktree
  merges with the forge its own commands run. The routes are no longer copied
  into the shared `info/attributes` when the project's committed
  `.gitattributes` already declares them, as every scaffolded project's does.
  What is still written is reported: each setting with its scope and old value,
  the git directory, how many worktrees share it, and the undo. A run that
  changes nothing stays silent, and `FORGE_NO_MERGE_DRIVER=1` still opts out.
- **A composite-primary-key table gets a store.** It used to get no entity at
  all, only an info line, so counters and join tables (reliant:
  `message_order_counters`, `project_daemons`, `connection_secrets`, …) were
  hand SQL. The entity struct and store now project, addressed by the WHOLE
  key: `Get<E>ByPK(ctx, db, k1, k2)` and `Delete<E>ByPK` take one parameter
  per key column, `Upsert<E>` resolves a conflict on every key column, and
  Create/List/Count/Update/UpdateMasked address the row by the struct's key
  fields. `pkg/crud.Repo` gains `GetByPK` / `DeleteByPK` (a key entity), and
  its Update, UpdateMasked, Upsert and zero-row classification address a
  composite key by every column (Bun's `WherePK`); PK columns stay out of the
  SET list. CRUD wiring still needs a single-column key, and there is no ULID
  fill or cursor pagination for a composite key.
- **Entities are named from their table, and a CRUD rpc that matches no table
  is refused.** A CRUD rpc's entity found its table only as
  `Pluralize(snake(<E>))`, so a singular-named table (`daemon_attachment`,
  `provider_backoff`) matched nothing and its `Get<E>` silently became a
  pb-through stub. The join now matches a table by its own entity name (its
  singular, camel-cased), then the plural and the singular of `<E>`; a table
  whose name derives to the wrong thing declares what it holds with
  `COMMENT ON TABLE t IS 'forge:entity-name=<Name>'`, which also names its
  store. A unary, unmarked CRUD-named rpc whose entity has a wire message but
  matches no table, and that no handler implements by hand (its only method
  is forge's unwired stub, or none), now fails `forge generate` (under the
  CRUD step's gate), naming the rpc and the tables it looked for. The seed scope and the
  ownership audit look for the singular too. **Upgrading:** a project with
  such an rpc gets the refusal on its next generate: write the migration,
  declare `forge:entity-name`, or mark the rpc `// forge:custom` (what it
  already was in effect).
- **Continuous profiling captures CPU and the live heap by default; allocation,
  goroutine, mutex and block profiles are opt-in (`forge/pkg/observe`).** The
  profiler still takes a 10s CPU profile every minute, but now snapshots the
  heap every 15 minutes and exports only `inuse_space` / `inuse_objects`. Go's
  `alloc_space` / `alloc_objects` are cumulative since process start, so each
  snapshot repeated every allocation site the process ever had and grew with
  uptime: in a production deployment they were 97.7% of stored profile rows (99.2%
  of bytes), and one service's grew from 11k to 39k rows per snapshot over 8
  hours. The new defaults keep about 0.3% of that deployment's rows (PROFILES.md
  has the measurements). Choose the set with `observe.Config.Profiles`
  (`ProfilesConfig{Types, HeapInterval}`), or per deploy with `FORGE_PROFILES`
  (`cpu,heap,alloc,goroutine,mutex,block`, or `none`) and
  `FORGE_PROFILES_HEAP_INTERVAL`, which win over the config; invalid values
  are logged and ignored. Mutex and block profiles set the runtime's sampling
  rates only while selected and restore them on shutdown. Opted-in allocation
  counters stay cumulative, at the heap cadence. A live-heap snapshot also no
  longer carries the locations and functions of allocation-only stacks.
- **The ORM projects every applied table, whatever its rpcs.** The store
  (`<entity>_orm_gen.go`, `<Entity>Store`, delegates, the `_repo_ext.go` seam)
  used to exist only for a table some managed service declared a CRUD rpc
  for, so a project whose CRUD-named rpcs are all `// forge:custom` — or whose
  tables have no rpc at all — got no data layer from `forge generate` (reliant:
  zero of 55 tables). The store now follows the schema: every BASE TABLE with a
  single-column primary key is projected, a table no rpc names under the
  singular of its name (`message_content_blocks` → `MessageContentBlock`).
  CRUD wiring, pages and seed roots still key on CRUD rpcs.
  `COMMENT ON TABLE t IS 'forge:no-entity'` keeps one table off the store (and
  off CRUD wiring);
  `//forge:no-orm` still opts out the whole data layer. The ORM step no longer
  needs a service, only an entrypoint, and a name collision's advice now names
  the table opt-out (`// forge:custom` no longer takes a table off the store).
  **Upgrading:** the next `forge generate` writes a store for each table that
  had none. If one of them redeclares a name your `internal/db` already
  declares, generate refuses and names it: rename yours, or add
  `COMMENT ON TABLE <t> IS 'forge:no-entity'` in a migration.
- **`lifecyclekit` no longer re-exports `operatorkit.Run`, `Controller` and
  `Options`, so importing it never links controller-runtime.** Every binary
  that imported lifecyclekit for `Supervise`, `WrapWorker` or `Singleton`
  linked `sigs.k8s.io/controller-runtime` and its Kubernetes tree (~1.8 MB),
  operators or not. The three names live only in
  `forge/pkg/appkit/operatorkit`; the scaffolded `lifecycle.go` names it
  directly, and a `go list -deps` guard pins that lifecyclekit reaches no
  `k8s.io` package. An owned `lifecycle.go` that names the removed symbols:
  `forge skill load migrations/v0.1.46/operatorkit-bridge`.
- **`PRE_STOP_DELAY` and `SHUTDOWN_TIMEOUT` follow one rule: zero is the
  default, negative is none** — the rule every `serverkit.Config` duration
  already used. `ShutdownTimeout <= 0` always became the 30s default, so "no
  shutdown window" could not be said, while "no pre-stop pause" had to be
  written negative. A negative `ShutdownTimeout` (config, env, or
  `serverkit.Drainer`) is now a window over as it opens; "none" is never
  "unbounded", since the kubelet ends the shutdown at the grace period anyway.
  The rendered `terminationGracePeriodSeconds` reads both the same way: `0s`
  used to add 0 while the server spent its default (a SIGKILL mid-drain), a
  negative adds nothing, and any Go duration (`1m30s`, `500ms`, rounded up)
  is read instead of falling back to the default.
- **`forge generate` refreshes a stale vendored `proto/forge/v1/forge.proto`
  (and `proto/buf/validate/validate.proto`).** It vendored them only when
  missing, so a project kept the forge.proto of the forge it was scaffolded
  on: reliant could not write `(forge.v1.service).audience` until it
  hand-synced the file. A copy that differs from the running forge's (after
  the go_package/CRLF normalization `forge lint --vendored-protos` uses — now
  one shared `assets.NormalizeVendoredProto`) is re-copied and reported as
  `🔄 Refreshed <path>`. A disowned copy (`forge project disown <path>`) is
  kept, and so is forge's own source copy in forge's repository.
- **`forge env up` converges a host-run managed identity with `pkg/identity`
  (FI4).** An env whose `identity` runtime is `forge.IdentityOnHost {infra}` is
  converged against its host Zitadel after the infra is up and before any host
  service starts, with the same library the control plane runs for hosted
  envs, authenticated by the bootstrap PAT (`idp_pat_path`), in the server's
  default instance. Non-secret outputs go to `.forge/identity/<name>.json`;
  the broker token, client secrets and machine keys go to the env's secret
  store under the env var each `forge.IdentityRef` resolves to, so a host-run
  API gets `jwt_issuer`, `jwt_audience`, `oidc_*`, `idp_base` and
  `idp_broker_token` with nothing copied. The launch pre-flight no longer asks
  for those store keys beforehand.
- **Removed: the `auth idp-provision` command, the `idp-provision` workload and
  its env binding, `deploy/kcl/<env>/idp_identity_gen.k`, and the
  `pkg/devidp` provisioning half** (`Reconcile`, `Spec`, `Identity`, the
  publishers, `EnsureProject`/`EnsureSPAApplication`, `ProvisionLoginBroker`,
  `SecretFile`, `RedirectGlob`/`LogoutGlob`). `pkg/devidp` keeps the sign-in
  broker's client (flow, login, password reset, token file). A project that
  has the old job migrates by declaring `identity = forge.ManagedIdentity
{runtime = forge.IdentityOnHost {infra = "idp"}}` on its dev Bundle.

### Added

- **`forge env exec <env> --workload <name> -- <argv...>` (#758)** runs a
  local command with the selected workload's resolved configuration, inherited
  stdio/cwd, literal argv, signal forwarding and the child's exit status. The
  default is exactly `api` (the scaffold's `_api.name`); no first-service guess.
  Only OnHost/OnCompose are accepted, before accessing secret providers. Config
  readback and exec share the same local resolution and launched-coordinate
  overlay; host config layers also drive `env up`. Compose shares its host-side
  launcher resolver (secrets < env_file < runtime env), not container env:
  declare host-reachable published addresses; no container DNS/DSN rewriting.

- **Release provenance a console can read without a repository (console v3
  D4/D5).** A cut records its commit's subject and author on the provenance,
  and the commits the release adds over the newest earlier release whose commit
  it descends from (`Release.Commits`: newest 100, `truncated`, and a `total`
  that is unknown in a shallow clone). Every bundle carries a value-free summary
  line and per-section digests for each object (`BundleDoc.Objects`).
  `release.CompareCommits`, `CompareArtifacts` and `CompareObjects` diff two
  releases from those records; ranges compose through their bases, and fall back
  to the releases cut in between on the same branch when a shallow cut broke the
  chain. A scoped deploy (`--target`, `--frontends-only`) records the components
  it moved as `Promotion.Targets`; `release.Decide` treats a promotion of the
  bound release as a move when it moves something new. A scoped deploy of a new
  release is refused where the env's control plane converges promotions (a hub
  or hosted tiers), since it would move more than the scope. A control plane
  must accept the new bundle fields (`objects`, `provenance.commit_subject`,
  `provenance.commit_author`) before a forge that writes them records to it.

- **`forge.ManagedIdentity`: an env's users, declared in KCL, with no app code
  change.** `Bundle.identity` names the roles, the applications that sign users
  in (derived from the env's `api` workload) and the consumers (every service
  workload that can reach the instance); forge records the declaration in the
  release shape (`shape.identity`, names only, omitted when absent so no existing
  shape moves, sealed into the bundle config) and fills each consumer's typed
  config (`jwt_issuer`, `jwt_audience`, `oidc_scopes`, `oidc_client_id`,
  `oidc_redirect_uri`, `idp_login_uri`, `idp_base`, `idp_broker_token`,
  `jwt_roles_claim`, `jwt_jwks_snapshot`) through the new `forge.IdentityRef` env
  source: a reserved managed secret on a hosted workload, a key of the delivered
  Secret `forge-identity-<name>` on a cluster workload (the broker token and the
  JWKS snapshot as re-read files), the secret store for a host-run identity. A
  field the identity fills AND a workload sets by hand is refused. Login is
  `native` (default; `hosted` is accepted and recorded, phase 2), instances are
  private unless asked, `share_with` reuses a sibling env's users within one
  project (no chains), SMTP and seed-user passwords are secret names, social
  providers are reserved. The issuer is `https://<slug>.reliantidentity.com`,
  the slug assigned once by the control plane (`release.IdentitySlug`).
  `forge.ControlPlaneSecret` is generalised beyond `telemetry_token`: the derived
  kinds `identity` (required: pods wait for the first delivery, and
  `forge env shape` says so), `identity_machine_key` and `identity_jwks`, plus
  `keys` and `required`. See `deploy/managed-identity`.

- **One `forge generate` applies the migrations to a shadow database once.**
  Every step that reads the applied schema — the entity join behind the
  frontend pages, nav, service stubs, ORM, CRUD wiring and seed step, the test
  factories, the frontend mock dataset — used to replay every migration into a
  fresh shadow database of its own: about eight replays of one unchanged
  directory per run, close to half of a full run on control-plane (168
  migrations). A run now applies each migration set once, to a template
  database (`schemadef.Template`): readers of the model get a deep copy of the
  introspected tables, and readers that execute SQL get a private copy of the
  database (`CREATE DATABASE … TEMPLATE`), so nothing one reader writes reaches
  another. The template is filed under the digest of the exact migration bytes
  it applied plus the shadow server, so a migration written mid-run (a scaffold
  birth) is applied afresh, never served stale; the server is resolved once per
  run, and the templates are dropped when the run ends. `--timings` reports the
  count:
  `shadow schema: migrations applied 1× (…), reused by N more reader(s)`.
  New library surface: `schemadef.ReadMigrationSet`,
  `MigrationSet.ApplyTemplateAt`, `Template.{Tables,Open,Close,Digest}`, and
  `pgtest.NewClone` / `pgtest.NewCloneAtURL` (copy a database the caller built).

- **An env moves onto the Flux path one delivery unit at a time.**
  `Bundle.hub_converges = [<unit>, …]` lists the delivery units the control plane's
  hub converges from the recorded bundle; `forge env deploy` applies every other
  unit directly. This replaces the env-level "the hub converges this env" boolean,
  which could only say yes or no for the whole env. Direct apply leaves a
  hub-converged unit's objects alone and **prunes against the full render**, so a
  skipped Deployment is never read as an orphan; the rollout wait skips it, and a
  deploy whose every unit is the hub's applies nothing to a cluster and asks for
  no kubectl context. The shape records `units: [{name, applier}]`, the env's
  declaration tells the control plane `hubConvergedUnits` (the old `hubConverged`
  flag is true only once every unit has moved, so a control plane that predates
  units can never converge one forge still applies), and each apply gate records
  `units_forge` / `units_hub`. The `flux-safe` refusal now follows the object's
  unit: an error in a unit the hub converges, a warning in one forge applies.
  Unset, forge keeps its old inference. Declaring a non-empty `hub_converges`
  beside `applier = "forge"` or a `lifecycle` is refused at render.
- **Bound vs live, per tree.** `forge env status` and the deploy plan list each
  tree of the bound bundle (cluster × unit × phase) with who applies it, the
  revision the promotion binds it to, the revision the hub's reconciler reports it
  applied, and the verdict (`converged`, `behind`, `failed`, `unobserved`,
  `forge-applied`). Promotion is intent and convergence is observed: the binding
  is never moved late to make them agree. A control plane that reports one reading
  per cluster is shown as such; a tree it reports nothing for reads `unobserved`.
- **A forge app on a shared, operated identity provider (Zitadel) gets its roles,
  survives an issuer outage at restart, and can rotate the login broker's token
  without one.** Three typed config fields set the behaviour; all are inert until
  set, and Clerk, Firebase and generic issuers are unchanged. `jwt_roles_claim`
  (default `auto`) maps Zitadel's `urn:zitadel:iam:org:project:<id>:roles`
  role-to-org map into `Claims.Roles` (and `Role`, when there is exactly one),
  scoped to `jwt_audience`'s project; the flat `roles` array still wins when a token
  has one. `jwt_jwks_snapshot_file` / `jwt_jwks_snapshot` (+ `_max_age`, default
  7d) deliver the issuer's last-known public keys with a `delivered_at`, so a server
  restarted while the issuer is down starts on them, retries the issuer in the
  background, and switches to live keys on the first success. Live always wins for
  a `kid` it holds; the snapshot adds a key only in a rotation overlap (delivered
  after the last live fetch); a stale, absent or malformed snapshot still refuses
  to start. `idp_broker_token_file` is re-read on every use, with
  `idp_broker_token` as the fallback. Library:
  `auth.JWTConfig.{RolesClaim, JWKSSnapshot, JWKSDiscover}`,
  `auth.JWKSKeyfuncWithSnapshot`, `devidp.Client.TokenFile`. New fields are in
  the scaffold's `config.proto`, so an
  existing project adds them by hand (the `auth` skill has the block and the new
  `SetupAuth`; `Validator.Close` now stops the JWKS refresh).
- **The dev Zitadel moves to v4.19.4** (was v4.16.2) in host-infra, the compose
  `idp` service and its per-platform checksums. 4.16.2 carries open critical
  advisories (account takeover, cross-org privilege escalation). No config or steps changes were
  needed, and the host-infra suite now downloads the real archive and checks it
  against the pinned checksum. 4.18.0 is broken upstream and was skipped.
- **serverkit exposes the listener knobs a streaming server needs.** These
  come from reliant's API listener (P3c G5), and each one used to be
  hard-coded:
  - `Config.ReadHeaderTimeout`, `Config.ReadTimeout` and `Config.IdleTimeout`.
    Zero keeps the defaults (10s / 30s / 120s), and a negative value disables
    the timeout. Over HTTP/2, `ReadTimeout` is a deadline on each stream's
    request body, so the 30s default cuts a client-streaming or bidi call at
    30 seconds. A server with long client streams sets it negative.
  - `Config.HTTP2MaxConcurrentStreams` is the number of concurrent streams
    one HTTP/2 connection may open, over TLS or h2c. Zero keeps net/http's
    default of 250. reliant uses 1000.
- **An open stream no longer holds a forge server's shutdown for the whole
  budget.** `http.Server.Shutdown` waits for every active request, and a
  long-lived one (an SSE response, a websocket, a server stream) never
  finishes on its own: every rollout of an app with one open stream waited out
  `SHUTDOWN_TIMEOUT`, logged `server shutdown error: context deadline exceeded`
  at ERROR from every replica, and exited cutting the stream without a status
  (reliant #773). `serverkit.Run`'s listener now owns a drain registry,
  `serverkit.Streams`, and ends its streams inside the budget with cause
  `svcerr.ErrServerDraining`, while unary requests drain as before. Mark a
  plain-HTTP streaming route with `serverkit.LongLived(h)`; it ends as the
  drain begins, after the pre-stop pause, so its client reconnects to a live
  replica. A stream with something to say first asks for
  `serverkit.Draining(ctx)` — a soft signal while its context is still live —
  and runs until it returns or the deadline minus `Config.DrainReserve` (5s by
  default). `svcerr.Draining(detail)` / `svcerr.ErrServerDraining` is the
  drain's identity (wire code Unavailable), distinct from `ErrUnavailable`.
  `/readyz` now answers `{"status":"draining"}` during shutdown.
- **Connect streaming RPCs end cleanly when a server drains.**
  `serverkit.StreamDrainInterceptor()`, now in the scaffolded interceptor
  chain, binds every server-streaming and bidi handler to the listener's drain.
  A drained stream ends **OK** with the trailer `x-forge-drained: true` — a
  browser client treats Unavailable as a dead connection and may abandon every
  read on it — or **Unavailable** for procedures and services listed in
  `serverkit.Config.RedialOnDrain`, for peers that redial on it. Unary and
  client-streaming RPCs are untouched. The scaffolded `serve.go` is yours, so
  an existing project adds the one line itself: `forge project upgrade list`
  offers v0.1.46 §8.
- **A claimed stream can learn exactly when the drain will end it.**
  `serverkit.DrainDeadline(ctx)` returns the moment `Streams.Drain` moves to
  its hard phase, once the drain has begun. A gateway telling its peers when
  to have reconnected used to compute shutdown deadline − `DrainReserve`,
  which is early whenever the budget is under twice the reserve (the reserve
  then shrinks to half the budget). `DrainHTTP` fixes the schedule before
  `Shutdown` begins the soft phase, so every stream it wakes can read it.
- **A shutdown says where its time went.** `serverkit.Run` used to log
  "server stopping" and then nothing until an error, so a rollout that used
  its whole grace period could not be diagnosed from its own log. Each phase
  now writes one `drain: …` INFO line with `phase` and `elapsed` — readiness
  flipped, pre-stop over, listener drained (with `streams_ended`,
  `streams_claimed`, `streams_hard_ended`, `hijacked_conns_closed`,
  `streams_still_running`), workers stopped, operators stopped, teardown done,
  complete — and records the new built-in histogram
  `forge.server.drain.duration` by `phase`. `Streams.Report()` returns the
  listener's counts for a main that drains its own listener.
- **A main that drains without `serverkit.Run` gets the drain's phase lines
  and metric too.** They used to be written only inside `Run`, so a main
  using `Drainer` + `DrainHTTP` had to hand-log `Streams.Report()`. Each piece
  now writes its own phases to the `Logger` and `Meter` fields it is given
  (nil: `slog.Default()` and the global OpenTelemetry meter): `Drainer.Begin`
  readiness and pre_stop, `DrainHTTP` the listener (via `Streams.Logger` /
  `Streams.Meter`, now with `addr`), the new `Drainer.Complete` the total.
  `Run` uses the same path and adds only workers, operators and teardown, so
  nothing is written twice. The readiness and pre_stop lines no longer carry
  `streams_open` (the listener line's counts cover it).
- **A websocket open at shutdown closes with a status and runs its cleanup.**
  `http.Server.Shutdown` neither waits for nor closes a hijacked connection,
  so the process exit dropped every open websocket — the browser saw a dead
  TCP connection, and the handler's deferred cleanup (a terminal's PTY) never
  ran. A connection hijacked under `serverkit.LongLived` is now registered
  with the drain: `serverkit.AfterDrain(ctx, f)` runs f when the drain begins (a
  websocket handler blocked in a read sends 1012 "service restart" and sets a
  read deadline there), the drain waits for the handler, and a handler that
  ignores the drain has its connection closed by the drain at the end of the
  reserve (`Streams.CloseHijacked`), so its cleanup still runs.
- **A rollout's drain no longer logs at ERROR.** `svcerr.ErrServerDraining`
  (and every `svcerr.Draining(…)`) is now `error_class=canceled`, and every
  logging layer — `LoggingInterceptor`, `LogMiddleware`, and so
  `observe.Chain` with no options — treats it as an expected outcome: written
  like a success, with `expected=true`, never at ERROR or to Sentry. A gateway
  that ends each connected client's stream with `Unavailable("draining")` used
  to page once per client per deploy; apps had to know to declare
  `WithExpectedErrors` themselves. The error counter still counts it, under
  `canceled`.
- **The shutdown sequence is usable without `serverkit.Run`.** A main that runs
  its own listener (a worker with a health port, a gateway) uses the same
  pieces: `serverkit.Drainer` (readiness gate, pre-stop pause, shutdown budget,
  `ReadinessHandler`), `serverkit.NewHTTPServer(cfg, handler, streams)` (the
  listener Run builds, with its drain registry installed) and
  `serverkit.DrainHTTP(ctx, srv, streams)` (the listener's drain). See
  `forge skill load services/shutdown`.
- **A direct apply prunes superseded one-shot Jobs, keeping the newest three of
  each.** A standalone Job is named by the hash of its spec, so every release that
  changes one leaves the previous Job behind, and since a reconciler made
  `ttlSecondsAfterFinished` wrong (the Job re-runs every TTL period) nothing
  removed them: control-plane-prod held ~250 completed Jobs and gained about five
  per deploy. After a full, non-dry-run apply, forge deletes the finished Jobs of
  each base name beyond the newest three (the current release's included). It
  deletes a Job only when forge
  made it (`managed-by=forge` and the `forge.dev/job-name` handle), it is in the
  applied namespace and env, the render still defines that base name, and it has
  finished. A running Job, a Job the render being applied names (a rollback's),
  a CronJob's child, a chart's Job and any other namespace or env are never
  touched, and a `--target` apply prunes nothing. A failure is a warning, never a
  failed deploy. The Flux path needs no code: a superseded Job leaves the render
  and Flux's inventory prune deletes it, which `TestBuild_HashNamedJobsStayPrunable`
  pins by requiring that the bundle never stamps a Job prune-exempt. The plan's
  `object_removed` detail for a Job now says the apply prunes it, where it said to
  "delete it by hand".
- **Per-service Connect handler options: `mountkit.WithHandlerOptions`, and
  a generated `app.MountOptions` value with one field per service.** Before
  this, `serverkit.Server.HandlerOpts` was a single chain shared by every
  service, and `mountkit.Option` could only change the HTTP stack. reliant
  adds a machine-wake interceptor to two of its services (P3c G1), and the
  only way to do that on generated mounts was to disown
  `mounts_services_gen.go`.

  - `mountkit.WithHandlerOptions(...connect.HandlerOption)` adds options to
    one mount, after the shared chain. Connect runs interceptors in the order
    they were added, so the shared chain (recovery, logging, auth) wraps the
    per-mount interceptor. Where Connect lets the last option win (read and
    send limits), the per-mount option overrides the shared one. It works
    with `mountkit.RegisterService` and `serverkit.Server.Mount`. Each mount
    gets a fresh slice, so one mount's options never leak into another's.
  - `mountkit.Resolve(shared, defaultStack, opts...)` is the same rule for a
    caller that registers through its own methods.
  - `mounts_services_gen.go` generates `type MountOptions struct` with one
    `[]mountkit.Option` field per service (the zero value is none). Its
    methods are the mounts, and each is a `MountFunc` as a method value, so
    owned serve code passes the options explicitly:
    `Mount: app.MountOptions{Worktree: wake}.MountAll` for the server, or
    `.MountWorktree` for one service's subcommand. Nothing is discovered by
    name. `(*Components).Mount<Svc>` and `MountAll` are the same mounts with
    no options, so the generated `server` and per-service commands compile
    unchanged.

- **`forge/pkg/identity` converges the content of a Zitadel instance to a
  declared spec.** One library for both halves of managed identity:
  `forge env up` will run it against the dev instance with a bootstrap PAT, and the
  control plane against each customer instance with that instance's owner machine
  key. It reconciles the project (with role assertion), roles, OIDC
  applications (web, SPA, native and the native-sign-in broker app with its
  per-app login route), machine users with keys and expiring tokens, the login,
  password-complexity and lockout policies, SMTP, declared role members as a set
  (revoke first, other roles in a grant kept) and, for local and ephemeral
  instances only, seed users. Every step reads first, so a pass against a
  converged instance writes nothing. It is plain JSON over HTTP with no Zitadel
  Go import, so it stays on the permissive side of the AGPL boundary. Generated
  credentials live behind a `KeyStore` seam and the published values (issuer,
  audience, client ids, a JWKS snapshot, the broker token) behind `Outputs`.
  `Rotate` replaces a broker token or machine key with an overlap window. For
  an existing instance, `Options.Adopt` plus `ProjectSpec.AdoptID` and
  `Application.AdoptClientID` converge it in place and keep the ids other
  systems hold. A `-short`-gated smoke test runs all of it against the real,
  unmodified Zitadel v4.19.4 image.
- **`forge ci gate` starts the longest path first.** Host slots used to be
  granted in plan order (forge's static checks, then a project's extras, then
  the test lanes), so a gate's longest checks waited behind every shorter
  heavy check planned before them: on control-plane the unit suite (~15 min)
  and the integration suite (~22 min under load) queued behind ~25
  slot-minutes of lint, build and render rows. The gate now records how long
  each check's command took (`durations.json` beside the verdict cache, shared
  by every worktree of a repository) and orders the checks that read the tree
  by the longest chain still to run: a check's own recorded time plus the
  longest chain of checks that wait on it. The first run has no history and
  keeps plan order; tree-mutating checks still run first and alone. A slot that
  is free when several checks become ready together now waits 20ms for them
  all to ask before it picks one.
- **`forge generate --steps harness` regenerates only the test harness, in
  seconds.** After a `Deps` edit the next thing you need is each
  `helpers_gen_test.go` (`NewTest<Svc>`, `With<Svc>Deps`, the `Stub<Field>`
  doubles), and the only way to get it was a full run, which takes about 2
  minutes on reliant. The new preset runs the harness step and the steps it
  reads from: config, checksums, and the service list read from the descriptor
  the last full run left. It skips the Tier-1 stomp guard, as `--steps mocks`
  does. A proto or migration change still needs the full run. The harness step
  itself is faster too, in every run: it used to type-check each cross-package
  `Deps` interface's package from source, one load per field. On reliant that
  was 72 of the preset's 74 seconds. It now reads export data for every
  service's interfaces in a single load. The preset takes 10 seconds on
  reliant, and its output is byte-identical to the full run's.
- **The test harness stubs `// forge:optional-dep` interface fields too.**
  `helpers_gen_test.go` emitted a `Stub<Field>` for every required interface
  dep and nothing for an optional one. So the one test that exercises an
  optional dep's non-nil path had to hand-roll its double: reliant's
  model-endpoint `Router` and `Credentials`, and eight trigger deps. Now an
  optional field gets its `Stub<Field>`, with the same fail-closed methods, but
  `NewTest<Svc>` still leaves it nil. A test that says nothing about the field
  runs the code's nil path. A test of the other path passes
  `With<Svc>Deps(Deps{Field: &Stub<Field>{…}})`. An optional field forge
  cannot stub gets no TODO, since nothing requires it.

- **`forge cluster connect` grants one binding exactly what its env renders, in a scope
  the owner chooses, as the binding's own identity.** `--env` names the env whose
  binding this is. `--apply-scope namespace:<ns>[,<ns>…]` (the default, over the
  namespaces the render targets) writes a Role and RoleBinding per namespace;
  `--apply-scope cluster` writes a ClusterRole and ClusterRoleBinding and says it
  is cluster-admin equivalent (it holds `bind` and `escalate` on RBAC). The rules
  are derived from the env's render, one per resource it applies, so a release that
  adds a kind is Forbidden by the cluster until connect is re-run. The subject is
  the binding's own identity: a GCP service account the control plane allocates for
  it (gcp), or a ServiceAccount forge mints per binding (token) — never a shared
  one. The grant no longer carries `impersonate` or the namespace, ServiceAccount
  and Role writes the impersonated identity needed; a re-run prunes what the new
  plan no longer names and removes the legacy shared-identity grant. `--dry-run`
  now prints the exact RBAC diff against the cluster (or `--live-from <capture>`)
  and the permissions it would newly grant. `disconnect` deletes every binding's
  grant, token identity and delivery policy for the cluster, found by label. The
  delivery-secret grant is unchanged apart from its per-binding names.

- **A migration can declare that it breaks rollback, and a project can require
  every migration to declare either way.** `-- forge:breaks-rollback <why>` is
  the counterpart of `-- forge:backward-compatible`: the previous release
  deliberately does NOT keep working on the schema the migration leaves. The
  migrator treats it as it treats a migration that declares nothing; what it
  adds is that the break is a reviewed decision. With
  `database.migration_safety.require_compat_declaration: true`, `forge lint
--migration-safety` fails every migration after the baseline (the newest
  `-- forge:adopts` migration, or the first) that declares neither
  (`migration-compat-undeclared`) or gives no reason, or only a `<why>`
  placeholder (`migration-compat-reason`). A migration declaring both is
  refused whether or not the requirement is on (`migration-compat-conflict`),
  and the migrator reads it as not compatible. `forge db migration new
--backward-compatible "<why>"` / `--breaks-rollback "<why>"` writes the line;
  in a project that requires one and got neither, it writes both choices as a
  comment to replace. Either directive may now separate its reason with a
  colon (`-- forge:backward-compatible: additive`), which before read as no
  declaration at all.

- **`forge.DeliveryPolicy`: what an env's releases must prove, declared per env
  (schema and shape only).** `Bundle.delivery_policy` carries nine controls, each
  `off | audit | enforce` with its own parameters: `trusted_builder`, `signing`
  (org KMS key, a key you hold, or keyless Sigstore), `provenance_sbom`,
  `flux_verify`, `admission_verify`, `signed_promotions`, `vuln_gate` (severity
  policy, KEV, fixable-only, time-boxed exemptions), `bundle_kinds` and
  `approvals` (one person by default; a two-person rule needs declared
  approvers). Controls that cannot block a deploy default to `enforce`
  (`signing`, `provenance_sbom`, `flux_verify`); the rest default to `audit`.
  forge refuses combinations no release could pass (an enforced verifier with
  signing off, an enforced gate over an SBOM that is off, a two-person rule with
  no approvers). The resolved policy is recorded in the release shape as
  `delivery_policy`, omitted when every value is the default so existing shapes
  hash as before. **Nothing enforces it yet**: it is recorded for the control
  plane, and `deploy/delivery-policy` says which control is enforced by which task.
- **A Flux-safe bundle.** Flux re-applies a bundle on every interval and prunes
  what it stops carrying, so some objects that are harmless under `kubectl apply`
  are wrong under it. The bundle build now refuses, and `forge lint` (`flux-safe`)
  reports, a Job with `ttlSecondsAfterFinished` (the TTL controller deletes the
  finished Job and the next reconcile re-runs it: control-plane's temporal-schema,
  temporal-namespace and openbao-bootstrap), a Job whose name carries no hash of
  its spec (its immutable spec wedges every reconcile after the first change), and
  the force annotation on a kind that holds data. These are errors for an env a
  reconciler may apply and warnings for one forge applies itself, so an env that
  builds today keeps building.
- **The build states what the cluster would have defaulted.** A Deployment's
  `strategy` (`RollingUpdate`, 25%/25%) and `progressDeadlineSeconds` (600) are
  now rendered explicitly, by forge's renderer and for a Deployment written by
  hand in `forge.Manifests`, and a `StorageClass`, `RuntimeClass`,
  `PriorityClass` or RoleBinding carries the Flux force annotation
  (`kustomize.toolkit.fluxcd.io/force: enabled`), so a reconciler can recreate
  it on an immutable-field change as direct apply already does. Values you wrote are never overridden. These change
  every Deployment's rendered bytes (and so its object hash) once.
- **Each tree records `budget_seconds`**, the whole-rollout allowance the control
  plane enforces in place of Flux's short per-attempt wait: the largest
  `progressDeadlineSeconds` (rollout) or `activeDeadlineSeconds` (Jobs) among the
  objects the tree is judged on, plus 120 s.

### Added

- **Bundle layout v2: one tree per cluster × delivery unit × phase.** A bundle
  used to publish one flat tree per cluster, so a Flux Kustomization per cluster
  applied the whole env in a single batch and could not express what direct apply
  guarantees (a pre-rollout Job completes before any workload changes; the daemon
  cluster before the hub). Every cluster that is not the unclustered (`_`) or
  hosted tree is now laid out as
  `manifests/<cluster>/<unit>/<phase>/<NNN>-<kind>-<name>.yaml` (phase `pre`,
  `rollout` or `post`), and the bundle document carries `trees`: each with its
  `depends_on`, a 90 s per-attempt timeout, a 15 s retry interval and the objects
  its health is judged on. Declare units with `Bundle.delivery_units` (a list of
  `forge.DeliveryUnit {name, after}`) and place a workload or a `forge.Manifests`
  group with `delivery_unit`; an env that declares none puts everything in `base`.
  The phase comes from one classifier (`release.ClassifyPhase`) that the
  pre-rollout gate of `forge env deploy` now also uses, so the two cannot drift.
- **`tree_moved` plan finding.** An object whose tree changes between releases is
  deleted by the Kustomization it leaves and created by the one it joins, with
  nothing ordering the two. A move between trees is a **stop** finding that needs
  `--acknowledge-destructive tree_moved`; the one-time move from the flat layout
  is a single warning per cluster.

### Changed

- **`applier = "forge"` is a transitional state, not a permanent one.** Every real
  env, the platform's own included, moves onto build → OCI bundle → ledger → Flux,
  and direct apply narrows to dev and ephemeral envs. The schema, the skills
  (`deploy/shape`, `deploy/flux`, `deploy/flux-safe`, the v0.1.45 migration) and the
  direct-apply notice now say so, and name `hub_converges` as the way to move.
- **Static and Firebase frontends ship after the promotion is recorded**, from the
  machine that deploys, for an env whose every unit is the hub's too (the
  pre-record pass only checks they can ship).
- **The observability outputs are one file per service, so a rebase no longer
  rewrites them.** Three files listed every service:
  `deploy/observability/dashboard-spec_gen.json`, `alert-defaults_gen.json`
  and `dashboards/saved-searches_gen.ndjson`. Two branches that each added a
  service changed all three, at the same sorted position whenever the
  services were neighbours, and in JSON so repetitive that git aligned even
  distant insertions onto each other. The merge driver kept one side, so on
  reliant every lane's rebase was followed by a generate that rewrote about
  1,500 lines, and then an amend. The spec is now written as
  `deploy/observability/spec/project_gen.json` plus one
  `spec/service-<svc>_gen.json` per service. Each service file carries that
  service's dashboard, its saved searches and its alert entry. The saved
  searches are now one `dashboards/<project>-service-<svc>_gen.ndjson` per
  service. `alert-defaults_gen.json` (now `forge.alert-defaults/v2`) no
  longer lists services. Two such branches touch disjoint files, so git
  merges them, with or without forge's driver, into exactly what a generate
  over both writes. The assembled spec the release bundle and the console
  read is unchanged (`forge.dashboards/v1`). `forge generate` removes the old
  files if nobody edited them. The local stack's `docker-compose.yml` is
  yours, so pick up its new saved-search loader, which reads every
  `*_gen.ndjson`, with `forge project upgrade`. Until then the local HyperDX
  loads no generated saved searches.
- **A bundle now seals the env's `observability_policy`, so its digest changes
  for envs that set one.** The release shape recorded on the ledger carried
  `observability_policy` (what the env opted out of), but `forge env build`
  never passed it to `bundle.Build`, so the config blob sealed inside the
  bundle, and the digest over it, left it out; `delivery_policy` was sealed
  correctly. An env that declares `forge.Observability` with anything switched
  off or opted in gets a new bundle digest on its next build, even with no
  other change (envs that declare none, or opt out of nothing, are unchanged).
  `ProjectShape` and `Build` now build the shape through one constructor, and
  `TestBundleSealsEveryShapeField` fails when a shape field is recorded but not
  sealed. See `forge skill load migrations/v0.1.46`.
- **`forge generate` deletes the stale files it generated.** A forge-certified
  file that no emitter writes any more was only listed, with "run with
  --force-cleanup to delete", and `--force-cleanup` was hidden from `--help`
  — so the files stayed and kept compiling. On reliant, marking the
  model-endpoint rpcs `// forge:custom` left
  `internal/handlers/model_endpoint/factories_gen_test.go` behind. Now a
  stale file whose bytes are still exactly what forge rendered is deleted (the
  delete is journaled, so a run that fails later puts it back). A stale file
  that was hand-edited is kept and listed with the two ways to settle it:
  `forge generate --force-cleanup`, now in `--help`, deletes those too, and
  `forge project disown <path>` keeps one as yours. The sweep is skipped, and
  says so, when a step failed as a warning that run, since that step's outputs
  look stale only because it wrote nothing. A path in the scaffold-once ledger
  is never deleted, even when it still carries a marker from the forge
  version in which it was regenerated every run.
- **`forge db migration new` writes a short header; `--context` writes the
  full one.** Every new migration used to open with the reconstructed schema,
  the whole previous migration and every filename in the directory — 4,658
  lines on reliant, most of them the baseline it had just squashed, above a
  few lines of SQL that agents and reviewers then had to find. The header now
  says how many migrations make up the schema, which is newest, and where to
  read it (`forge project shapes --kind table`, `forge db introspect`).
  `--context` (implied by `--dsn`) writes the old block.

- **BEHAVIOR CHANGE: `forge.flux_chart()` is hardened and sized by default (F8).**
  The chart's own RBAC let both controllers read every Secret and ConfigMap
  cluster-wide and impersonate ANY ServiceAccount, and its defaults (4 workers,
  100m/64Mi, no priority) let one slow rollout block every other author's
  deploy. The default now installs: per-namespace controller RBAC
  (`rbac = "namespaced"`: Flux CRs cluster-wide, leader election in `flux-system`,
  impersonation pinned by `resourceNames`, Secret reads only where a RoleBinding
  names `flux-tenant-reconciler` / `flux-tenant-source`, written with
  `forge.flux_namespace_bindings(ns)`); multitenancy on with no cluster-admin
  binding; `--concurrent=16 --concurrent-ssa=8 --requeue-dependency=10s`
  (source-controller `--concurrent=8`); `CancelHealthCheckOnNewRevision` and
  `DisableConfigWatchers`; requests and limits; PriorityClass `forge-delivery`;
  NetworkPolicies reduced to `allow-egress` (ingress to every `flux-system` pod
  only from `flux-system` pods), `allow-scraping` only for the namespaces in
  `scrape_namespaces`, no `allow-webhooks`; Pod Security `restricted` on
  `flux-system`. Every piece is overridable (`rbac = "pinned" | "chart"`,
  `network_policies = "chart"`, `pod_security`, `feature_gates`, `values`, ...).
  A namespace holding Flux objects needs `flux_namespace_bindings` or its source and
  apply fail `Forbidden`. See `migrations/v0.1.46` and
  `docs/runbooks/flux-patch-and-upgrade.md`.
- **`forgeconv-deps-are-interfaces` explains a struct-VALUE dep.** A Deps
  field typed as a struct value (`Policy netguard.Policy`) is still reported
  — deliberately, though its exported func fields look like the func seams the
  rule accepts: such a type's methods fall back to the real thing when a func
  field is nil (the system resolver) or build live handles (a dialing
  `*http.Transport`), and a value cannot be left unset, so the zero value
  `Deps{}` and the generated harness pass reaches the network. The finding now
  leads with the fix that fits: when the value is derived from configuration,
  take the config block and derive it in `New`.

- **Each object's tree (`<unit>/<phase>`) is part of the bundle's config digest**,
  so a release that re-homes an object has a different digest even when its bytes
  are unchanged. Bundles built before this release have a different layout and so
  a different digest; this is the one-time migration above.

- **`forge.HelmChart.namespace_labels` refuses `app.kubernetes.io/name` and
  `app.kubernetes.io/managed-by`.** forge stamps both on the Namespace it
  creates for a chart, but it only DEFAULTS them, so that a chart's own name
  label survives for its selectors. On the Namespace, a declared value therefore
  replaced forge's in silence. Declaring one now fails KCL evaluation, as
  `forge.dev/*` and `kubernetes.io/metadata.name` already did. `RenderHelmChart`
  refuses a spec carrying any of these keys before it runs helm, so a spec that
  reaches it some other way is refused too. Drop the key: forge's values are the
  chart's `name` and `forge`.

- **Chart pruning uses recorded UIDs and a non-expiring apply/prune lock.**
  Label-only enumeration could delete a copied object or a concurrent applier's
  resources. Owned charts now atomically lock before any chart write and record
  successful SSA-response UIDs in `kube-system/forge-chart-inventory-<hash>`.
  Only prior inventory UIDs are prune candidates; UID/resourceVersion-conditional
  DELETE protects replacements and intervening ownership/prune-policy changes.
  The first run deletes nothing; failed applies leave inventory unchanged and
  prune nothing. CRDs, Namespaces, PV/PVC, StatefulSet and Secret remain protected,
  as do Flux prune-disabled annotations and controller-owned objects. Plans use
  the same inventory/UID guards. Inventory and prune errors fail the deploy.
  **Operational change:** the applier needs ConfigMap access in kube-system.
  The lock never expires or admits automatic takeover. Failed/cancelled/crashed
  commands retain it; terminate/fence the old writer and children, resolve
  outstanding writes, then use `forge cluster recover-chart-lock` with explicit
  context, owner/chart, inspected UID/resourceVersion and holder confirmation.
  See `deploy/flux` for the safety procedure. Previously untracked stale objects
  still require one-time manual deletion. `prune = False` disables deletion,
  not serialization or inventory tracking.

- **BEHAVIOR CHANGE: a re-apply of a `forge.HelmChart` now deletes the objects
  the chart stopped rendering.** forge applies a chart with `kubectl apply`,
  which never deletes, so an object a chart version dropped (the Flux chart's
  ClusterRoles and NetworkPolicies are the case that surfaced it) stayed live forever,
  including security objects: an `allow-webhooks` NetworkPolicy and a
  `cluster-reconciler-impersonator` ClusterRole and binding had to be deleted by
  hand. forge now stamps every object a chart renders with
  `forge.dev/chart-owner=<project>.<env>` and, after a chart's apply fully
  succeeds, deletes the objects carrying that stamp (and that chart's
  `forge.dev/workload`) that the new render no longer contains. It never
  deletes a CRD, a Namespace, or a PersistentVolume / PersistentVolumeClaim /
  StatefulSet / Secret (kept, and reported); an object annotated
  `kustomize.toolkit.fluxcd.io/prune: disabled` (the same escape hatch a Flux
  bundle honours); an object with `ownerReferences`; or anything from a chart
  whose render is empty or unreadable. `prune = False` on the `forge.HelmChart`
  turns it off for that chart. Objects a chart dropped BEFORE this version carry
  no stamp and are never deleted: remove those by hand once. A `--target` apply
  of something else renders no chart and prunes nothing. See
  `migrations/v0.1.45/helm-chart-prune`.

### Added

- **`flux_chart(placement = flux_placement_delivery_v1())`** places the
  controllers on a dedicated tainted node pool, with `runtime_class_name =
FLUX_RUNTIME_CLASS_GVISOR` for a target that has a sandbox. Never defaulted on.
- **`HelmChart.patches`** merge-patches the objects a chart renders, keyed
  `Kind/name` (the `Bundle.overrides` engine), for fields the chart's values do not
  template, such as a pod's `runtimeClassName`. A key that matches nothing fails
  the render.
- **`forge ci gate` runs checks concurrently, caches verdicts by tree, and
  tests only what a branch changed.** The gate ran its checks one after another
  (one project measured a p50 of 35 min, up to 3 h, on a shared machine). Now a
  check starts as soon as its `after` dependencies have finished; a check that
  rewrites the tree (`verify-generated`, an extra with `mutates_tree`) runs alone
  before the checks that read it; heavy checks each hold a host heavy slot and
  light ones run under `--jobs`. Every check still runs after a failure, and the
  table keeps plan order, with the wall time and a `head:` line. Each check
  logs to `tmp/ci/<lane>/<check>.log`. On a committed tree a check that already
  passed on that tree (any worktree, same forge and pins) is not run again:
  `PASS (cached <sha>)` (`CI_NO_CACHE=1` / `--no-cache` to bypass; a dirty tree
  turns it off). On a branch, `unit-tests` runs only the Go packages whose test
  binaries contain a changed package, and falls back to the whole suite when
  anything but Go source changed, on the default branch, or with `CI_FULL=1` /
  `--full`. New: `CI_ONLY` (checks or groups), `CI_REPORT`, `CI_LOG_DIR`, and
  `ci.gate.env` / `path` / `affected_ignore`; extra checks gained `lane`,
  `group`, `after`, `heavy`, `mutates_tree`, `cache`, `probe`, `skip_exit_code`,
  `env` and `replaces`, enough to express a hand-written ~26-step gate script.
  See `ci/gate` and `migrations/v0.1.46`.

- **Host-wide heavy slots: `pkg/hostslot` and `forge slot`.** On a shared
  machine every heavy job slows the others down — on a 16 vCPU / 32 GiB VM
  `go build ./...` went from 36–50 s beside 0–3 other heavy jobs to 101–155 s
  beside 8 or more, from memory-cgroup reclaim — so heavy work now queues for
  one of N host slots instead of piling up. A slot is an OS file lock (a
  crashed holder releases it); N defaults to min(CPUs / 4, memory GiB / 8),
  read from the cgroup v2 limit when there is one, and `FORGE_HEAVY_SLOTS`
  overrides it (`off` disables). `forge slot run --class heavy -- <cmd>` runs a
  command in a slot and `forge slot status` names the holders and the queue.
  `forge generate` and `forge lint` take a slot on their own, and the
  scaffolded Taskfile runs `test` and `test:integration` under a `SLOT` prefix.
  Anything already inside a slot (`FORGE_SLOT_HELD`, or an ancestor holding
  one) never waits again, so nested heavy commands cannot deadlock. See
  `migrations/v0.1.46`.
- **A no-op `forge generate` skips its steps.** A full run that changes
  nothing records the tree as a verified fixed point in
  `.forge/generate-cache.json` (gitignored, per checkout). The next run that
  finds every input byte-identical prints one "nothing to do" line and
  stops: on control-plane, about half a second instead of about three minutes
  at moderate load (2–5 s instead of 8+ minutes on a heavily loaded box). The
  key covers the work tree as git sees it, the gitignored files generate
  reads or writes (go.work, installed
  node_modules, dotenvs, secrets, `.forge/` state, every other ignored file
  outside a wholly ignored directory), the outputs the verifying run wrote,
  local `replace`/`go.work` source outside the project, the forge executable
  by content, every tool it runs (go, buf and the plugins the buf templates
  name, goimports, node, generate hooks), the environment that steers them,
  and which postgres hosts the shadow schema. Any difference runs every step
  and names what changed. A run that WROTE files never records — the run
  after a change is the one that proves the tree converged — and neither does
  a narrowed or weakened run (`--steps`, `--force`, `--skip-validate`, …).
  `--no-cache` (or `FORGE_GENERATE_NO_CACHE=1`) runs every step;
  `forge ci verify-generated --no-cache` passes it through. `--check` and
  verify-generated consult the record (a recorded fixed point cannot drift)
  but never write one. The check runs before `forge generate` queues for a
  heavy slot, so a no-op never waits behind other agents' builds.
- **`forge generate --timings`** (or `FORGE_GENERATE_TIMINGS=1`) prints each
  step's wall time, slowest first, when the run ends — including runs that
  failed, and runs nobody typed (verify-generated's).

- **`//forge:resolves-caller` declares a project's own caller resolver.** The
  `unscoped_auth` audit counted a handler as resolving the caller only when it
  reached forge's scaffolded seam, `middleware.GetUser` / `ClaimsFromContext`.
  A project whose auth package predates forge (reliant's
  `auth.GetUserIDFromContext`) had no way to say so, and every authenticated
  rpc it moved onto forge's handler model was reported as never resolving the
  caller (P3b friction F18). Put the marker in a package-level function's doc
  comment, beside the code it describes. The audit then counts a handler that
  calls it as resolving the caller, whether through the import that binds the
  declaring package in that file (aliased, plain or dot) or through a helper
  in the handler's own package, and its hint names the declared resolvers.
  The match goes by import path, so a same-named function in an undeclared
  package still does not count. A marker on a method is reported as ignored,
  because a call site does not name a method's receiver type. The
  declarations come from `codegen.DeclaredCallerResolvers`, so other tools
  can read the same declaration. `forge project audit --json` lists them under
  `caller_resolvers`, and `forge project annotations --kind go` documents the
  marker.
- **`forge.HelmChart.namespace_labels` / `namespace_annotations`.** Labels and
  annotations on the Namespace forge creates for a chart, so Pod Security
  Admission (`pod-security.kubernetes.io/enforce=restricted`) can be declared on
  `flux-system`. They converge under server-side apply: a changed value is
  updated, a key you stop declaring is removed, and a key another manager set
  is left alone. `forge.dev/*` and `kubernetes.io/metadata.name` are refused.
  Not available on a `delivery = "bundle"` chart, which has no forge-created
  Namespace.
- **`forge.KubeconfigPolicyRule.resource_names`.** Limits a minted rule to named
  objects (`resourceNames`): `bind` on exactly two ClusterRoles, `get` on
  one Secret. It replaces a hand-written ClusterRole beside the mint.

- **Built-in alerts for workloads and the platform (A6).** Five new
  `forge.Alerts.defaults` ids over the node agent's customer metadata:
  `crash-loop` (restarts grew by 3 in 10m, or a BackOff event), `oom-killed`
  (a restart whose last exit was `OOMKilled`, or an `OOMKilling` event),
  `memory-near-limit` (memory limit utilization above 0.9 for 10m),
  `image-pull-failed` and `evicted` (Kubernetes events). Each is tuned or
  disabled by id like the rest. Their SQL scopes by `reliant.env.id`, which the
  node agent stamps and forge cannot know, so it carries the placeholder
  `{reliant_env_id:String}` for the evaluator to bind (the local check binds
  the env name). An env with `signals.metadata = False` records none of the
  five: there is nothing for them to read. Two more ids,
  `deploy-failed` and `observability-quota`, are raised by the control plane
  from its own events; forge records them as marker rules so your tuning and
  `enabled = False` travel with the release. Tested against ClickHouse 26.8
  with the exporter's real tables.
- **Dashboards ship in the release.** A release bundle now carries every
  dashboard the project declares at `observability/dashboards/`, beside the
  metrics schema at `observability/metrics-schema.json`, inside the same
  manifests layer (a Flux `layerSelector` still selects exactly one layer, and
  nothing a Kustomization `spec.path` reaches is touched). Generated dashboards
  come from `dashboard-spec_gen.json` plus the Custom metrics dashboard drawn
  from the built binary's schema; a renderer-neutral file you own in
  `deploy/observability/dashboards/` (a `"panels"` array) ships as is, and one
  named like a generated dashboard (`shop-overview.json` for
  `shop-overview_gen.json`) replaces it. A HyperDX-format file (`"tiles"`)
  stays local. Every file is validated when the release is cut: a bad file fails
  `forge env build <env> --release` with `file:line:col: path: message`
  errors before a release is left behind without its bundle; a deploy that
  builds nothing only warns. `forge observe dashboards check` runs the same
  validation and lists what would ship.
- **Two best-practice dashboards.** _Workloads_ draws the customer metadata the
  node agent delivers (CPU and memory against requests and limits, restarts and
  the last exit reason, CPU and memory by image tag, Kubernetes events and
  container terminations). _Releases_ splits requests, error rate and latency
  by `service.version` and adds a per-version comparison table. Neither has a
  heatmap. They draw nothing on a local `forge env up` (no node agent, no
  releases).
- **`annotations` on a dashboard spec.** `{"source": "releases"}` declares that
  a dashboard's time-series panels carry deploy markers, drawn by the console
  from the release ledger. Every generated dashboard with a line panel declares
  it. HyperDX has no such concept and the HyperDX renderer ignores it. A `"s"`
  (seconds) unit joins the display units.

### Fixed

- **forge refuses to run npm in a frontend on a Node below its
  `engines.node`, and says which Node it needs.** The scaffolded frontends
  require Node `>=22.12` (vite 8, vitest 5). On Node 20, `npm install` there
  does not fail cleanly. npm 10.8 crashes resolving the tree with
  `npm error Cannot read properties of null (reading 'edgesOut')`, a message
  that names neither Node nor the floor. That is how
  `forge project new --frontend` broke in forge's own scaffold smoke test,
  which still ran Node 20. `forge project new`, `forge scaffold frontend` and
  the TypeScript step of `forge generate` now check the node on PATH against
  the frontend's `engines.node` first. They name both versions and the fix,
  and run no npm. They refuse only a range they can read (`>=`, `^`, a bare
  version, joined by `||`), and only when `node` is on PATH.
- **A web frontend's test toolchain resolves one vite: vitest 5, vite 8,
  `@vitejs/plugin-react` 6.** The Next.js scaffold left `vite` to vitest's
  dependency, so `vitest.config.ts` took `defineConfig` from vitest's vite and
  `react()` from whatever vite `@vitejs/plugin-react` found. In a dev build's
  bridged workspace those differed: web-runtime's vitest ^4.1 put vite 8 at
  the root, the plugin (4.x) resolved it there, and vitest 3 kept a vite 7 of
  its own, so `tsc --noEmit` and `next build` failed with TS2769 "Property
  'rolldownVersion' is missing" (red on main in
  `TestE2EFixtureCorpusFrontendBasePath`). The Next.js and Vite SPA scaffolds
  now declare `vite ^8.0.0`, `vitest ^5.0.0` and `@vitejs/plugin-react ^6.0.0`
  — the same three in both kinds, so a project with both hoists one vite — and
  `engines.node >=22.12`, the floor vite 8 and vitest 5 declare. vitest 4.1 is
  skipped on purpose: npm 10.x, which ships with Node 22, crashes installing
  it ("Cannot read properties of null (reading 'edgesOut')"). vitest 5 also
  drops the `tinypool` that a fresh project's `npm audit` reported as
  critical (#754): the scaffold's audit goes from 2 critical to none.
  **Migration:** `package.json` is yours once scaffolded, so a project
  adopts this by hand: in each Next.js or Vite SPA frontend set `vite`,
  `vitest` and `@vitejs/plugin-react` to the ranges above (add `vite` to a
  Next.js frontend's `devDependencies`), raise `engines.node` to `>=22.12`,
  run `npm install` and check that `npm ls vite` lists one version. See
  `migrations/v0.1.46` section 6.

- **A pooler rollout no longer fails statements or the pool's close.**
  On SIGTERM PgBouncer refuses every client that needs a server connection
  with `FATAL: server shutting down` (08P01) — before forwarding the
  statement — so every pooled client failed its next statement on each idle
  connection to the draining pod, although the other replica was serving.
  `serverkit.OpenDB`'s `pgx` pool now reports that refusal as
  `driver.ErrBadConn`, and database/sql runs the statement again on another
  connection; the error keeps PgBouncer's message if every replica refuses.
  Closing a connection the pooler already dropped
  (`tls: failed to send closeNotify alert (but connection was closed anyway): … broken pipe`)
  is no longer reported, so `OnShutdown`'s `DB.Close` stops logging an ERROR
  on every deploy. Other drivers open unchanged.
- **The migrator's directive scan no longer dominates integration tests.**
  `migratekit` scans every embedded migration for `forge:backward-compatible`,
  `forge:breaks-rollback` and `forge:adopts` on every start, and ran three
  regexes over every line of SQL to do it. On control-plane's 170 migrations
  (12k lines) that was 240ms a scan and 5.3s under `-race`, paid by every
  integration test that migrates a fresh database: whole packages
  (`deploystore`, `reconcilestore`) went past their 30-minute budget under
  load. Only `--` comment lines that mention `forge:` reach the regexes now:
  24ms a scan, 72ms under `-race`. What is read, and every line number, is
  unchanged.
- **A multi-word component's workload is named as a DNS label, so its env
  renders.** `forge scaffold worker engine_shadow` declared
  `name = "engine_shadow"` in `deploy/kcl/workloads.k`, and `fw.Workload`
  rejects that name: it becomes every object's name, so it must be an RFC-1123
  label. After that, no env rendered. `forge generate` reported this only as a
  failed k3d-ports step, and still exited 0. The same happened to a
  multi-word service or operator. Scaffold now declares
  `engine_shadow = fw.Workload { name = "engine-shadow" … }`: the KCL binding
  keeps the component's identifier, and `args` still selects the
  `engine_shadow` subcommand. Everything that matches a declared workload to a
  component now matches it in either spelling (`codegen.SameWorkload`). That
  covers scaffold's already-declared check, `forge scaffold crd --operator`,
  the component-drift lint, the dev env's API port key and `FindWorkload`
  (`forge doctor parity`). To fix a project scaffolded before this, rename
  the `name` field to its dashed form.
- **`forge lint --optional-deps-guard` no longer flags reading a map-typed
  optional dep.** Once deps-are-interfaces accepted a table of collaborators,
  reliant's trigger handler declared `// forge:optional-dep` fields
  `Access map[string]IntegrationAccess` and
  `Senders map[string]SenderDirectory`, and the guard flagged every
  `s.deps.Access[k]` as a nil dereference. Reading a nil map is safe in Go,
  so the guard now resolves each optional field's declared type: a map
  literal type, or a named map type declared in the package. Index reads,
  comma-ok reads, `len`, `range`, `delete` and methods on a named map type
  are not flagged. Two shapes still are, because they panic while the dep is
  nil. A write (`s.deps.M[k] = v`, `+=`, `++`) panics on a nil map. A deref of
  a looked-up element (`s.deps.M[k].Do()`, or `a := s.deps.M[k]; a.Do()`)
  panics on every key, because a nil map yields the zero element; this
  applies only when the element's zero value is nil. Such a deref is guarded
  by a nil check on the map, a nil check on the element, or a comma-ok read.
  A key missing from a wired map is not an optional-dep concern, and the fix
  hint points to the comma-ok read for it. Slices and arrays keep the plain
  rule: indexing one is a deref. Findings now carry a `Use`
  (`deref` / `map-write` / `map-element`), and each kind has its own message
  and fix hint.
- **`forge lint`'s format pre-pass formats to the project's own gate, so it
  never rewrites a file that gate accepts.** In reliant,
  `forge lint --quiet --scope internal/handlers/trigger --scope internal/grpc`
  regrouped the imports of six files under `internal/grpc` that the author
  never touched. reliant's `.golangci.yml` enables goimports with no
  `local-prefixes`, and its pre-commit `go fmt` accepted those files. The
  pre-pass applied goimports with the go.mod module as the local prefix
  regardless, which is the scaffolded config's setting and not reliant's. So
  the hook and lint disagreed on every later edit. The pre-pass now reads the
  golangci-lint config (v1 or v2, YAML, JSON or TOML) a run loads:

  - gofmt is always applied. It is Go's own formatting, and no gate rejects
    it.
  - Import grouping is applied only when the config enables goimports, and it
    uses exactly the `local-prefixes` the config declares (none declared,
    none applied).
  - When gci is enabled, gci owns grouping (golangci-lint `--fix` applies
    it), and the pre-pass leaves grouping alone.
  - With no goimports, no config, or a config that does not parse, imports
    are not regrouped.

  A scaffolded project is unchanged: its gate is goimports with the module
  as the prefix. Scoped auto-fix already touched only files under `--scope`.
  The files in the report were under `--scope internal/grpc`, so they were in
  scope; the pre-pass's standard was the bug. A test now pins the scope
  boundary too.

- **`forge lint --help` says that lint rewrites files.** The synopsis now
  reads "auto-fixes formatting in place first; --no-fix to only check". The
  long help says files are rewritten in place, only inside `--scope`, and
  never when the project's own formatter gate accepts them. `--quiet` and
  `--scope` each say that auto-fix still applies under them and that
  `--no-fix` stops it.
- **A pin run as `go run .../cmd/forge@<pin>` under `FORGE_NO_HOOKS=1`
  repairs the merge driver.** reliant's `scripts/pin-forge.sh` runs exactly
  that. After a pin, the clone's `merge.forge-generated.driver` still named
  the previous pin's binary until someone ran the new forge from a real path.
  Two things stopped the repair (#785). First, `FORGE_NO_GIT_HOOKS` /
  `FORGE_NO_HOOKS` skipped the driver as well as `core.hooksPath`. Second, a
  `go run` build is ephemeral, so it was never written as the driver.
  - The hooks opt-out is now about hooks only. The driver is local git
    config, not a hook, and `FORGE_NO_MERGE_DRIVER=1` is its one opt-out.
    **Breaking:** a test that runs forge against a developer's checkout and
    set only `FORGE_NO_GIT_HOOKS` to keep forge out of its git config must
    also set `FORGE_NO_MERGE_DRIVER=1`.
  - A `go run` build of the pinned forge installs a durable copy of itself at
    `<user cache>/forge/merge-driver/<identity>/<name>` and names that. It is
    a hard link where the filesystem allows one, otherwise a copy. It is
    written once per identity and reused after that. The copies of the three
    most recently used identities are kept. A driver that named an evicted
    copy is unusable, so the next forge command repairs it. A forge that is
    not the pin never copies itself. Test binaries and builds under the temp
    directory are still never written.
  - `go run` builds are recognized by their shape: `<tmp>/go-build*/…`, or
    the build cache's `<2 hex>/<64 hex>-d/<name>` executable store. The
    location of `GOCACHE` is not used, because `go env -w` can move it
    without the environment showing it.
- **`forge generate` reports a mock only when its bytes changed.** Every run
  printed `✅ Updated internal/handlers/mocks/<svc>_mock_gen.go` for every
  service, and `✅ Generated mock + middleware for internal/<pkg>/` for every
  contract package. A re-run that changed nothing still printed them, and on
  forge itself one changed `mock_gen.go` came with eight such lines. The
  closing summary was already right. Now each line appears only when the file
  changed. An unchanged file is a `--verbose` line (`… unchanged`). The
  contract-package count reads `(N changed)`. The write chokepoint gained
  `checksums.WriteGeneratedFileOutcome`, which reports whether a write changed
  the file's bytes. `codegen.GenerateMock` returns a `MockResult` (wrote,
  changed, or why it was skipped) instead of a bool.
- **serverkit drains h2c requests on shutdown.** serverkit served h2c through
  x/net's `h2c.NewHandler`, and that handler hijacks each connection.
  `http.Server.Shutdown` does not track a hijacked connection, so Run's
  graceful drain returned in about 20ms while every h2c request was still
  running. A rolling deploy behind an h2c load balancer therefore cut every
  in-flight Connect and gRPC call. HTTP/1.1 drained correctly. The listener
  now uses net/http's own h2c (`http.Server.Protocols` with
  `UnencryptedHTTP2`; x/net deprecated `h2c.NewHandler` for this reason).
  Shutdown now sends GOAWAY to an h2c connection and waits for its streams,
  as it does over TLS. Native h2c accepts prior knowledge, which is what
  Connect, gRPC and load balancers send. An HTTP/1.1 `Upgrade: h2c` request
  is now served as HTTP/1.1.

- **Flag synopses name a value, not a quoted example.** pflag treats the first
  back-quoted span of a flag's usage as the name of its value. So
  `forge db migration new --help` printed
  `--backward-compatible -- forge:backward-compatible — <why>`. Twelve other
  flags had the same problem, among them `--option`, `--release`, `--tag`,
  `--timeout`, `--harness`, `--routes`, `--token` and `--from`. Boolean flags
  showed a value they do not take: `--push`, `--no-wait`, `--no-build`,
  `--background`, `--no-generate`, `--no-install`, `--no-seed` and
  `--guarded-fields`. Those usages now quote with `'…'`. The two migration
  flags name their value `why`. A test walks every command and fails on any
  value name with a space, and on any value name for a flag that takes no
  value.
- **`forge lint --migration-safety` closes with advice for the findings it
  printed.** After a `migration-compat-undeclared`, `-reason` or `-conflict`
  finding, the closing line said to rewrite "the destructive migration" or add
  `-- forge:allow-destructive`, and that settles none of them. Each
  compatibility rule now has its own fix. A batch that spans rules names the
  hatch of each rule present, and of no rule that is absent.
- **`WithPlaceholderDeps` says when its stand-in is not inert.** It was
  documented as a "never-initialised stand-in", but for a generated config
  message `new(T)` is a valid, empty config that the service runs on. In
  reliant's model endpoints, that means self-hosted, with the permissive
  network policy. No value of a config message can be told apart from real
  configuration. So the harness doc, `testkit.Placeholder` and the api skill
  now say this, and tell you to supply the config a row means through
  `With<Svc>Deps`.
- **A failed `forge generate` step makes the run exit non-zero.** A step that
  fails without stopping the run (the ORM refusing a name `internal/db`
  already declares, a frontend page or hooks step, goimports) let the
  remaining steps run and then exited 0 under "1 step(s) FAILED". Agents,
  scripts and CI read exit 0 as success, so `forge ci verify-generated`,
  `generate --check` and every scripted `forge generate` certified a tree
  that was missing the failed step's output. The run still goes on past the
  failure, keeps what the other steps wrote, says which dependents it skipped
  and names the failed steps under the completion line, and now exits 1.
  `--strict` still stops at the first failed step. `--watch` keeps watching
  after such a run. `forge env up` and `forge build` refuse to build on it, as
  they do when generate aborts (`--no-generate` skips the refresh).
  `forge scaffold service` keeps the services it wrote instead of rolling them
  back, because the run's writes were generated from them, and then fails.
  `forge project rescaffold` re-creates every path before it fails.
- **`forgeconv-deps-are-interfaces` accepts a map or slice of interfaces, and
  says where the seam goes when a dep is only handed on.** Reliant's trigger
  handler has two Deps fields, `map[string]IntegrationAccess` and
  `map[string]SenderDirectory`, which key one interface per integration. Both
  were flagged, so the lane had to invent a one-method lookup interface and an
  adapter type per map. A slice, array or map whose elements are interfaces or
  funcs is a table of collaborators. Each element is already a seam, and the
  zero value is inert: a lookup in a nil map answers `ok=false`. Such a field
  now passes, while a table of concrete values (`map[string]*store.Repo`)
  still fails. When the rule fires on a concrete dep and the package never
  calls a method on it, the finding now names the seam with `file:line`. That
  covers two cases. The package only hands the dep to another call: reliant's
  catalog passed `*tools.ToolsFactory` to `toolDef.Factory(…)` and, through a
  local variable, to `def.Factory(…)`. Or it only calls a func-typed field of
  the dep. In both cases the generic "name the methods you call" remedy had
  nothing to name.
- **`forge generate` reports what it wrote, and a failed step's dependents
  say they were skipped.** On reliant main a run printed "⚠️ Warning: ORM
  generation failed", then "✅ Generated internal/db/ (2 entity ORM files)",
  "✅ Generated handlers/message CRUD wiring" and "✅ Generated
  …/handlers_crud_test.go" — none of which existed. The ORM line printed after
  the swallowed failure; the CRUD lines printed after calls that returned early
  having written nothing (every CRUD rpc hand-written, or the scaffold-once test
  already born). Now: a failed ORM step prints no success line; the CRUD step
  after it reports itself skipped, with the reason, and writes nothing; each
  CRUD line reports what its call did
  (`⏭️ handlers/<svc>: no CRUD wiring written — every CRUD rpc (GetMessage) is implemented by hand`);
  and the completion line is followed by the steps that failed as warnings. (The ORM
  failure itself is correct there: reliant's `internal/db` declares `Message`
  and `ModelEndpoint`, which forge's ORM would redeclare.)
- **`forge project shapes` diffs declared rpcs against handlers.** A handler
  line carried only the method name (`SendMessage`), so `--grep ChatService`
  dropped every handler and `--grep Chat` pulled in unrelated methods; and
  `--kind handler` listed every method on a handler package's `*Service` —
  helpers, `Name`, `Register` — as a handler (39 for reliant's settings
  service, which has 33 rpcs). A handler line is now named like the rpc it
  serves (`SettingsService.GetSettings`), resolved the way `generate` resolves
  the package's service, and only methods that ARE an rpc of that service are
  listed; a package no declared service resolves to keeps its exported methods,
  marked `no declared service`. New: `--service <Name>` keeps one service's
  rpcs and handlers, and `--unimplemented` lists each declared rpc of a
  forge-managed service with no handler method or only forge's unwired stub.
  Streaming rpcs (`returns (stream X)`) were missing from the rpc list
  altogether, and are listed.
- **The generated-file merge driver merges only as the forge the project
  pins.** The driver command in `.git/config` named whichever forge binary
  first installed it, and a forge that found a usable command left it alone,
  so a clone kept merging with a forge several pins old: reliant's named
  `~/.cache/fm-forge-bin/v0.1.45-…6f9811cb/forge`, and a cherry-pick warned
  that it was not the pinned forge, then merged with it anyway. Now, at merge
  time, a driver that is not the forge `forge.yaml` pins hands the merge to the
  pinned forge when one has run in this clone (every forge command run by the
  pinned forge records its driver command under
  `forge-merge-driver.<identity>.command`), and otherwise merges as git does
  with no driver — a plain three-way merge, a normal conflict where the sides
  overlap — and says why. Any command run by the pinned forge
  (`forge generate` after a pin bump) points the driver at itself; a forge that
  is not the pin never replaces a working driver.
- **The ORM name-collision refusal names the rpcs you declared.** Advising how
  to keep a table off forge's ORM, it listed the CRUD quintet the entity's
  name implies —
  `CreateMessage, GetMessage, UpdateMessage, DeleteMessage, ListMessages` on
  reliant — whatever the project declared. It now lists the declared, unmarked
  rpcs that make the table an entity, each with its `file:line`
  (`MessageService.GetMessage (proto/reliant/v1/message.proto:14)`,
  `ChatService.ListMessages (proto/reliant/v1/chat.proto:221)`), by the same
  rule that decides what is an entity. The refusal is now a typed
  `*generator.ORMCollisionError`.

- **`forgeconv-no-handler-error-mapping` fires only on sentinels mapped to
  connect errors.** The body-shape signal counted every `errors.Is` /
  `errors.As` in a function and fired when the function also built a
  `connect.NewError` anywhere, so a handler that switches on sentinels to
  choose a RESPONSE status — reliant's `PollCopilotDeviceAuth` maps Copilot
  device-flow errors to PENDING / EXPIRED / DENIED and builds connect errors
  only for a missing argument and a failed save — was reported as a
  hand-rolled mapper. A sentinel now counts only when the `case` (or `if`)
  arm it selects produces a connect error: it returns a `connect.NewError` as
  its error, returns a `connect.Code…`, or assigns one in a function that
  builds the error from it. Those last two spellings (a code mapper; a switch
  that picks the code for one `NewError` after it) are now caught as well.
- **Replicas waiting for the migration run lock no longer deadlock a
  `CREATE INDEX CONCURRENTLY`.** migratekit's waiters blocked inside `SELECT
pg_advisory_lock(…)`, a statement that holds a snapshot until the lock is
  free. A concurrent index build waits for every older snapshot to end, so the
  build waited on the waiters while they waited on the run lock its session
  held, and postgres killed one side: a waiter (`take the migration run lock:
… deadlock detected (40P01)`, failing every replica's boot but one), or the
  build, leaving the schema dirty and the index INVALID. Reliant measured it
  with four replicas booting together, every trial. Every session-level lock
  migratekit takes (the run lock, and the record locks held across a
  golang-migrate write) now polls `pg_try_advisory_lock`, 10ms backing off to
  1s, holding nothing between attempts, through every entry point (`Apply`,
  `AutoMigrate`, `Open`, `Up`, `Force`) and on the run's one session, so a
  session-mode PgBouncer sees the same thing. The wait ends with the caller's
  context (`Apply`; `Open`/`Up`/`Force` take none and wait for the running
  migration as before). A `CREATE INDEX CONCURRENTLY` still has to be its
  migration file's only statement.

- **An interface that embeds another package's interface keeps its embedded
  methods in every generated double.** The handler test harness, the contract
  mock (`mock_gen.go`) and the observe decorator (`middleware_gen.go`) read
  interfaces from syntax and skipped every `pkg.Iface` embed, so a stub or
  mock had none of its methods and did not compile. reliant's
  `inbox.Repository` embeds `worktreesweep.StorageStore` (which embeds
  `worktreesweep.Store`), and `forge generate` failed its validate with
  `*StubRepo does not implement Repository (missing method
AdoptWorktreeDaemon)`; a contract `Service` embedding `io.Closer` produced a
  mock failing its own `var _ Service` check. A cross-package embed is now
  read from the type-checked package, so its embeds at any depth come with it;
  the predeclared `error` contributes `Error() string`, and a method reached
  through two embeds is declared once. The harness imports the packages the
  embedded signatures name under the embedding file's own aliases. When an
  embed cannot be read (its package does not type-check, or it has an
  unexported method), the harness no longer emits a stub that cannot compile:
  the Deps field is left to the test, with a TODO naming the embed.
- **A package directive means the same thing whichever file declares it.**
  `forge lint` read a package's `//forge:exclude-contract` (and
  `//forge:not-a-component`) from the first reasoned marker by file name and
  honoured only the `forge:lint-disable` allowances above that one marker, so
  the package's answer depended on file order: when reliant moved
  `approval.go`, which held the outbound-io allowance, out of
  `internal/grpc/services`, `connection.go`'s marker became the declaration
  and the package was refused although nothing about it had changed. A
  directive now applies to the whole package from any file, and a package
  that repeats one must repeat it identically (reason and allowances). Two
  declarations that disagree — different reasons, a bare marker beside a
  reasoned one, an allowance on one and not the other — are refused by the
  new `forge-exclude-contract-conflict` / `forge-not-a-component-conflict`,
  naming both `file:line`s. **Upgrading:** a package whose markers disagree
  now fails `forge lint`; keep one declaration, in the package's `doc.go`,
  which the docs and remediation text now recommend.

- **A web frontend's dev loop works from any origin, not just localhost.**
  `forge env up` handed the dev server the API's URL,
  `http://localhost:<api port>`, and the browser called it directly. Opened
  through a Reliant workspace preview URL
  (`https://<port>-<id>.workspaces.reliantapi.com`), a tunnel or a LAN IP,
  that is the viewer's own machine, so sign-in and every RPC failed (and
  Private Network Access blocks it regardless). Under the dev server the
  browser now calls `<origin><basePath>/_api/*`, and `next.config.ts`
  rewrites it to the API (`vite.config.ts` proxies it), the same pattern as
  `/_otel`. The upstream is the `NEXT_PUBLIC_API_URL` / `VITE_API_URL`
  `forge env up` already injects, so an explicit value from your shell or
  KCL still wins. The session cookie becomes first-party to the page's host,
  and CORS is not involved in dev. A new scaffold-once `src/lib/api-base.ts`
  makes the choice for both `src/lib/connect.ts` and
  `src/lib/auth/native-login.ts`: the dev proxy under the dev server, the
  runtime `config.js` `API_URL` in every build, exactly as before. Opt out
  with `FORGE_DEV_API_PROXY=off` on the dev server. `next dev` also refused
  its HMR socket and dev resources to any non-localhost page, which then
  rendered but never hydrated; `next.config.ts` now sets `allowedDevOrigins`
  to the Reliant preview host (`RELIANT_PREVIEW_URL_TEMPLATE` + `PORT`), the
  machine's own IPv4 addresses and `FORGE_DEV_ALLOWED_ORIGINS`
  (`vite.config.ts`: `server.allowedHosts`). `forge lint`'s
  `forgeconv-frontend-process-env` treats `FORGE_DEV_API_PROXY` like
  `NODE_ENV`. Expo is unchanged. **Migration:** the four files are
  scaffold-once, so existing projects merge by hand:
  `forge project rescaffold` the new `src/lib/api-base.ts` (and its test),
  then add the rewrite and `allowedDevOrigins` to `next.config.ts` and route
  `connect.ts` and `native-login.ts` through `apiBase()`. No generated file
  changes. See `migrations/v0.1.46/dev-api-proxy`.
- **`forge generate` no longer fails after a composed package is marked
  `//forge:external-component` (#747).** compose.go's reconciler drops a
  `<pkg>.Deps{…}` key whose field the Deps struct no longer declares, but it
  only checked packages still in the discovered component set. Marking a
  composed package external (or `//forge:not-a-component`, or excluding it)
  took it out of that set while compose.go, which is yours and only ever
  added to, kept constructing it, so removing one of its Deps fields left a
  key naming nothing:
  `unknown field BaseURL in struct literal of type alpaca.Deps`, then a rollback, on every generate. The drop now covers every
  project package compose.go constructs, with the same guards (keys only, a
  proven Deps struct, the alias resolved through the file's own import, a
  gofmt check). Generate also warns when compose.go still constructs a
  package whose marker now takes it out of the server's composition, and says
  what to delete; it never deletes that code itself.
- **Generated test helpers build the config production loads (#749).**
  `defaultTestConfig` in every `helpers_gen_test.go` returned
  `&config.Config{}`, so a nested config block was nil and every proto
  `default_value` behind it read as zero: handler tests ran against a config
  no process ever has (a project's hard-rule limits all read 0). pkg/config
  now generates `config.Defaults()`, the config `Load` yields when no file,
  environment variable or flag is set: proto defaults applied, nested blocks
  allocated, sensitive fields empty. It reads nothing from the process, and
  the harness defaults to it. The library side is the new
  `forgeconfig.DefaultsInto` / `DefaultsTyped`, the same defaults layer
  `LoadInto` starts from rather than a second copy of it. A component whose
  config package declares no `Defaults()` (a hand-written one, a CLI's stub)
  keeps the zero value.

- **A release commit's E2E Scaffold waits out the proxy's negative cache.**
  `release-forge.sh` checks that the version was never published by asking
  proxy.golang.org for it moments before tagging, and the proxy caches that
  404 for up to 30 minutes. `wait-for-go-proxy.sh` gave up after 10, so both
  the release's own wait and E2E Scaffold on the release commit timed out
  (v0.1.45: gate 02:06Z, push 02:07:45Z, served 02:37Z). The default window
  is now 35 minutes. Non-release commits are unaffected: the pinned version
  is already served and the probe returns at once.

- **API keys work in a scaffolded project (#746).** The README called them a
  first-class credential, but a scaffold had no working path for one. The
  generated `credentialFrom` read only `Authorization: Bearer` and the session
  cookie. The `auth/api-keys` skill said to pass `Provider: "both"` to a
  `SetupAuth` that returned a bare `func(token string)`. And `serve.go` ran
  `SetupAuth` before `app.OpenInfra`, so a key validator had no database
  handle (the project that found this opened a second pool). Now:

  - `forge/pkg/auth.Authenticator` (`NewAuthenticator`, `UseKeyValidator`,
    `Authenticate`) routes a credential by its shape. `fk_…`
    (`apikey.KeyPrefix`) goes to a key validator installed after
    construction, with the request context, and anything else goes to the JWT
    validator. It fails closed: keys disabled, a malformed key (rejected
    before the store is read), a rejected key (`ErrInvalidAPIKey`; the
    validator's own error stays server-side as `svcerr.Cause`), or no claims.
    `auth.KeyValidatorFunc` adapts a closure.
  - `forge/pkg/authn.Policy.ValidateContext` hands the validator the request
    context. Set exactly one of `Validate` and `ValidateContext`.
  - **Behavior change:** `pkg/authn` now rejects a validator that returns
    neither claims nor an error. Such a request used to reach the handler with
    no claims, so an `auth_required` rpc whose handler never calls `GetUser`
    (every CRUD shim) served it.
  - The scaffold. `pkg/middleware/middleware.go` declares
    `APIKeyHeader = "X-API-Key"`, and `credentialFrom` reads a Bearer header,
    else `X-API-Key`, else the cookie: one credential per request, with no
    fallback. `AuthDeps.Validate` takes the request context.
    `internal/app/auth.go`'s `SetupAuth` returns `*auth.Authenticator`, and a
    new `APIKeyValidator(*Infra)` seam returns the key validator (`nil`
    leaves keys disabled). `serve.go` installs it right after
    `app.OpenInfra`, on the pool the handlers use.

  Existing projects own `serve.go` and `internal/app/auth.go`.
  `forge project upgrade` refreshes `pkg/middleware/middleware.go` only while
  it is unedited, and the refreshed file does not compile against an old
  `serve.go`. See `migrations/v0.1.46`.

- **A rotated `OTEL_EXPORTER_OTLP_HEADERS_FILE` is noticed by its content.**
  The headers file was re-read only when its identity, size or mtime moved. On
  Windows `os.SameFile` resolves a stat'ed path when it is called, so it
  compared the current file with itself, and on any OS a same-size rewrite
  inside one filesystem clock tick kept its mtime: a renewed token of the same
  length could be missed until the next change. Each recheck now reads the
  file (a few hundred bytes, once per 5s per process) and re-parses it when
  the bytes differ. `TestHeadersFileIsReReadOnlyWhenItChanges` failed on the
  Windows runner for this reason.

- **Debug builds keep absolute source paths when `-trimpath` is the toolchain
  default.** The Reliant workspace image sets `GOFLAGS=-trimpath` in
  `$GOROOT/go.env` so worktrees share the build cache. A trimmed binary
  records module-relative source paths, and delve sets breakpoints by absolute
  path, so `forge debug break` reported "could not find file" and IDE
  breakpoints never bound. `forge debug start`, `forge build --debug` (the
  `delve` host runner's binary), the scaffolded `.air-debug.toml` and the
  scaffolded VS Code launch configs now pass `-trimpath=false`, which a command
  line flag wins over GOFLAGS. Existing projects' `.air-debug.toml` and
  `.vscode/launch.json` are theirs: add `-trimpath=false` to the debug build
  there by hand.
- **forge's own tests pass under `-trimpath`.** Ten test files found the
  repository root or testdata from `runtime.Caller`, which under `-trimpath`
  names a module-relative path that is not on disk; they now resolve against
  the package directory `go test` runs in. `internal/trimpathguard` fails on
  any new `runtime.Caller` whose file result is kept.

- **`forge db migration new` quotes the previous migration's SQL, not its
  header.** The PREVIOUS MIGRATION section commented out the whole previous
  file, including the context header forge had generated into it, which
  quoted the migration before that, and so on: every header nested one level
  deeper than the last. The section now quotes only what follows the previous
  file's `-- Write your migration SQL below:` line (#745). A migration with no
  such line (hand-written or imported) is still quoted in full, and one with
  nothing below the line reads `(no SQL)`.

- **A fresh checkout no longer regenerates a committed, current `gen/` before
  every build.** The build's staleness check compared modification times.
  git writes `gen/` before `proto/` (index order), so on every clone the
  protos were the newer files, and every CI job and new worktree ran the
  whole generate pipeline first. That pipeline needs the codegen toolchain,
  refuses an unpinned forge, and downloads embedded postgres; control-plane's
  k3d smoke test died on a Maven Central 429 during it. When
  `gen/forge_descriptor.json` carries a `source_hash`, staleness is now
  decided by content: the same digest `loadDescriptor` already uses to refuse
  a stale descriptor. Only an unstamped tree still falls back to mtimes.

- **`forgeconv-deps-are-interfaces` judges a generic dep by its
  declaration.** A Deps field typed with an instantiated generic interface
  (`streaming.UpdateHub[db.UserUpdate]`) was refused as "concrete type
  `*ast.IndexExpr`" — wrong about the type, and naming a Go AST node instead
  of it. An instantiation (`pkg.T[A]`, `pkg.T[A, B]`, same-package `T[A]`)
  is now classified as the generic type it instantiates, so a generic
  interface is silent and a generic struct still fires; a pointer to an
  instantiation still fires as any pointer does. Findings render the type as
  source (`*hub.Broker[Event]`), never as an AST node name.

- **Schema-qualified and quoted table names read as the table, everywhere
  forge reads migration DDL as text.** pg_dump writes every table qualified
  (`CREATE TABLE public.tasks`), and `forge project shapes --kind table`
  named each one `public`. The same short pattern was spelled separately in
  `forge map`, the migration-context reader behind entity births, the
  read-only-fields and column-markers lints, and the unscoped_auth audit
  (its `forge:owner` reader and its handler-SQL table scan); some read the
  schema as the table, others missed `"public"."tasks"` and `public."Tasks"`
  outright, so a `forge:owner` or `forge:fill` declared on a quoted name went
  unseen. They now share one identifier grammar (`internal/sqlident`): one to
  three dot-separated parts, each unquoted (folded to lower case, as postgres
  folds it) or double-quoted (kept verbatim, `""` for a quote). `shapes`
  reports the schema as `schema=<name>` detail.
- **`forge generate`'s "N changed" counts every write.** Agents read the
  completion line as the fixed-point check, and it counted only writes made
  through forge's journaled writers. It printed "0 changed" while the run
  rewrote `.forge/scaffolded.json` (P3b friction F17), it never counted
  `.forge/hashes.json` or `.forge/disowned.json` because they were saved after
  the line printed, and a retired generated file counted as nothing. The line
  is now checked against the pre-run fingerprint of the tree that generate
  already takes (now also under `--no-revert`), and that fingerprint keeps
  the committed `.forge/` state files instead of skipping `.forge/` whole.
  Any file whose bytes changed is counted, whatever wrote it. The state files
  are saved before the line prints, and removals show as `N removed`. A
  changed file outside `.forge/` that no forge writer recorded is named on
  its own line, because it was written by a step directly or by another
  process.
- **A structural change no longer switches features on silently, and
  forge.yaml can switch them off.** Features derive from the tree. The first
  `internal/handlers/` a CLI-shaped project grows (reliant adopting forge's
  handler model one service at a time) derives the project to service-kind
  and turns `observability` and `hot_reload` on. That happened with no line in
  the output, and the next generate wrote `deploy/observability/**`. The
  `features:` block was refused, so nothing could turn them back off (P3b
  friction F15).

  - `features:` in forge.yaml now pins any feature on or off, whatever the
    tree derives, for example `features: {observability: false}`. A pin is
    never normalized away on write, so no later change to the tree overrides
    it.
  - A derived feature whose requirement is pinned off follows it off (ingress
    with deploy, orm with codegen). A pinned feature whose requirement is off
    fails the load, naming both.
  - `orm` has no key: `//forge:no-orm` in internal/db stays its one switch,
    and `features.orm` is refused with that pointer.
  - The observability dashboards gate on the feature alone, no longer also on
    the kind, so a pin reaches them.
  - The first time a derived observability writes `deploy/observability/`,
    generate says what turned it on and how to pin it off.
  - The prerequisites audit is n/a for a project with no deploy tree, rather
    than a warning about dev KCL it never had.
  - `forge project features` says when a feature is pinned.
  - `forge project new --disable <feature>` pins the feature off in the
    forge.yaml it writes, so the switch outlives the scaffold.
  - The deploy skill documents pinning. Its pod-priority section moved to a
    new `deploy/pod-priority` sub-skill so the skill stays under the
    24,000-byte load cap.

  Kind itself stays derived from the tree. A declared `kind:` would be a mode
  whose consequences pins already reach one by one, and it would contradict
  the tree. A `kind: cli` project with handlers would have handler codegen
  running while `forge scaffold` refused it.

- **Two branches that each add a service merge or rebase without a person
  resolving forge's files.** Every such branch changes the same files: the
  birth ledger, `.forge/hashes.json`, the descriptor, the aggregated
  dashboard spec, alert defaults and saved searches, and the generated
  service mounts. In the P3b pilot's 33 parallel lanes each one conflicted on
  every rebase (F16). The `forge-generated` merge driver now settles them:

  - `.forge/scaffolded.json`, `hashes.json` and `disowned.json` merge key by
    key (`checksums.MergeStateFile`) and are rendered exactly as forge writes
    them.
  - A generated file whose line merge still conflicts, when both sides are
    forge's verifiable renders, keeps this side's render. The next
    `forge generate` renders both sides' inputs over it. A comment-incapable
    output (JSON) keeps this side's render whenever both sides changed it,
    because its certifying hash is this side's. A hand-edited side keeps the
    conflict.
  - Every forge command now wires the driver into the clone: the driver
    command goes in `.git/config` and the attributes routing forge's files
    in `info/attributes`, scoped to the project's directory. A project
    adopted in place, which never got forge's `.gitattributes` (reliant), is
    covered without editing a file it owns. The command names the binary by
    absolute path, because a bare `forge` resolved on PATH at merge time to
    nothing or to another version. It never names an ephemeral binary
    (`go run`, a test build, anything under the temp directory), and it
    leaves an existing `%P` command alone while its binary exists, so
    worktrees on different forge builds do not keep rewriting the shared
    config. `FORGE_NO_MERGE_DRIVER=1` skips the driver alone.
    `FORGE_NO_GIT_HOOKS=1` (or `FORGE_NO_HOOKS`) still keeps forge out of the
    clone's git configuration altogether, driver included.
  - The scaffolded `.gitattributes` names the new routes too.

  One file can still conflict: a forge project's own `internal/app/compose.go`,
  where `forge generate` injects each component. Take either side and run
  `forge generate`, which re-injects the other side's component.

- **A service adopted into a project without typed config compiles, and a
  failed generate names what is missing.** Before, the handler scaffold
  imported `<module>/pkg/config` and required `Deps.Config` in every project,
  even one with no config protos and no `pkg/config` (reliant, adopting
  services in place). `go mod tidy` then failed, generate rolled back, and
  the error said "fix go.mod or go.sum" (P3b friction F13).

  - `service.go` imports and wires `Config` only when the project has typed
    config: config protos under `proto/config`, or a `pkg/config` that
    declares `Config`. The harness already followed `Deps.Config` (#702). A
    project without typed config gets no `pkg/config` placeholder, because
    an empty `Config` would be a value every caller had to construct and pass
    while it configured nothing.
  - The config loader runs only when `proto/config` holds a `.proto`. A CLI
    scaffold leaves an empty `proto/config/v1`, and the loader used to
    replace the CLI's `pkg/config` stub with one importing `gen/config/v1`,
    which nothing generates. That broke the first generate after a CLI
    project gained a service.
  - A failed `go mod tidy` reads go's own report and names each unresolved
    import. An import inside the project's module is named as a package
    that does not exist, which no go.mod or go.sum change can fix, with a
    hint for `pkg/config` and `gen/`. An external one is named as a
    dependency that cannot be resolved. The go.mod advice stays only for a
    failure that names no import, and the error no longer repeats its own
    prefix.
  - When the generated test harness fails to compile because the project's
    own `pkg/middleware` has no `ContextWithClaims` (P3b F14), the error
    names that seam and the one function to add. It no longer calls the
    failure a forge codegen bug.

- **The test harness's auto-stubs refuse an unprogrammed call instead of
  answering `(nil, nil)`, and a test can program them.** The harness stubs
  every required interface-typed Deps field so `NewTest<Svc>(t)` constructs.
  Each stubbed method returning `(*T, error)` returned `(nil, nil)`, which a
  handler reads as "found" and dereferences, so a test could not reach a
  not-found or failure branch through the stub; reliant's P3b lanes hand-wrote
  fakes instead. A stub now has the shape of a contract mock: it is exported as
  `Stub<Field>` (`StubRepo` for `Deps.Repo`; it was `stub<Pkg><Field>`), has a
  `<Method>Func` field per method and embeds `contractkit.Recorder`. Every
  unprogrammed method whose last result is `error` returns
  `testkit.StubNotConfigured`, which applied only to error-only methods before;
  its message now leads with `<Stub>.<Method>Func not set`, the same
  fingerprint as `contractkit.MockNotSet`. A method with no error result still
  returns its zero values. Program one with
  `WithXDeps(Deps{Repo: &StubRepo{GetChatFunc: ...}})`. A test that called
  through an unprogrammed stub and relied on the zero answer now gets the
  error, which names the method to program.
- **A handler whose Deps require something the test harness cannot build
  gets scaffolded tests that pass as written.** The harness fills Logger,
  Config, an `orm.Context` DB, Clock and IDGen, and stubs interfaces; a
  required `*sql.DB` (reliant's account handler), any other pointer, map,
  slice or channel, or a func with no framework default stayed nil, so
  `NewTest<Svc>(t)` died in `validateDeps` with "Deps.SQL is required". That
  included every test forge scaffolded beside the handler, which could not
  pass without an edit. Now:
  - the harness has a `WithPlaceholderDeps()` option that fills each such
    field the test left nil with a never-initialised stand-in
    (`testkit.Placeholder`): `new(T)` for a pointer, an empty map or slice,
    an unbuffered channel, a func that panics naming the field. It is for
    rows that never reach the dependency;
  - the scaffolded per-RPC tests and CRUD lifecycle test of such a service
    pass it, with a comment naming the fields, so they pass as written;
  - a construction failure appends which of those fields are still nil, and
    both remedies: `With<Svc>Deps(Deps{...})`, or `WithPlaceholderDeps()`
    (`testkit.UnfilledDepsHint`). Optional deps are never touched.
- **Forge's own outputs under a stale record no longer block the next
  `forge generate`.** A generate killed during "Validating generated code"
  had already rewritten three observability dashboards, but saves
  `.forge/hashes.json` only at the end of a run, so the next generate
  refused all three as hand-edited Tier-1 files. A merge that takes one
  side's generated file and the other side's ledger (or merges a stamped
  file's body under one side's marker) did the same. The stomp guard now
  quarantines the drift it finds instead of failing: each drifted file's
  write goes to a side render, the emitters run, and a new step, "Tier-1
  file-stomp guard: compare drift with fresh renders", runs after them and
  before validation. A file whose bytes are this run's render (modulo the
  marker, and canonical formatting for Go) is re-recorded silently — a
  stamped file restamped, a JSON output's `hashes.json` entry rewritten.
  Anything else is a hand-edit, reported as before (mid-merge wording
  included), and the run's writes roll back. A run with a genuine hand-edit
  now renders before it reports. A path an emitter targeted this run is no
  longer listed by the stale-artifact sweep. Every `--steps` preset that
  keeps the guard keeps the new step, and the driver appends it to a plan
  that does not.
- **Scaffolded names and streaming tests.** Three things every reliant P3b
  lane hit:
  - An auto-stub no longer carries the package name at all: it is
    `Stub<Field>` (`StubRepo`), so a handler package `tool_call` no longer
    produces `stubTool_callRepo`.
  - A server- or client-streaming rpc's scaffolded test is a real test. It
    drives the stub through `NewTest<Svc>Server`'s typed client (a stream
    cannot be called in process) and asserts `tdd.AssertScaffoldStub`, so it
    passes as written and goes red when the rpc is implemented. It used to be
    a TODO under the unary test's `tdd.RunRPCCases` header. A bidi rpc's file
    has its own header saying why forge writes no case: the harness serves
    HTTP/1.1, and connect-go carries a bidi stream only over HTTP/2.
  - `forge generate`'s progress line names
    `internal/handlers/mocks/<svc>_mock_gen.go`, the file it writes, not
    `<svc>_mock.go`. Both now read `codegen.MockRelPath`.
- **Scaffolded packages, workers and webhook tests name a config only when
  there is one.** `service.go` takes `Config` only in a project with typed
  config (config protos, or a `pkg/config` declaring `Config`); the package
  (default, client, eventbus), worker (poll and cron) and webhook-test
  templates still imported `<module>/pkg/config` unconditionally, so in a
  project adopted in place, reliant's, each failed `go mod tidy` and rolled
  generate back. Packages and workers now follow the same rule
  (`codegen.ProjectHasTypedConfig`). A webhook test builds an existing
  service's Deps, so it follows that Deps instead: it passes a `Config` only
  when the service takes one, imported from the package the service names.
- **A failed `forge generate` that changed no Go file no longer builds the
  project to report on its rollback.** After reverting a failed run, generate
  ran `go build ./...` over the restored tree to say whether it compiles. It
  did that on every rollback, so a run that failed a pre-flight check after
  rewriting only `forge.yaml` spent about 9 minutes on reliant building Go
  sources that were byte-for-byte the ones the user started with. The check
  now runs only when the run changed a Go build input: a `.go` file, cgo or
  assembly source, `go.mod`/`go.sum`/`go.work`, or anything under `vendor/`.
  A rewrite with identical bytes is not a change; a Go file changed outside
  the rollback journal still counts, and so does any run whose pre-run
  fingerprint could not be taken. When it skips, the report says the run
  changed no Go file, so there was nothing to recompile, instead of claiming
  the tree compiles.
- **`forge project migrate import --from goose` splits a NO TRANSACTION file
  into one migration per statement.** It used to write the file whole under an
  `-- x-no-tx-wrap: true` header, which nothing reads: golang-migrate has no
  no-transaction mode and sends a file as one multi-statement query, which
  postgres runs as one implicit transaction. So reliant's goose files holding
  several `CREATE INDEX CONCURRENTLY` statements failed `migrate up` with
  SQLSTATE 25001 and left the schema dirty, as would an `ALTER TYPE … ADD
VALUE` followed by a row using the value (55P04). Each statement now becomes
  its own migration (`<name>_part1`, `_part2`, …), cut where goose cuts them —
  a line ending in `;`, or a `StatementBegin`/`StatementEnd` block — so each
  runs exactly as goose ran it, and a failure part-way leaves only that
  statement to repair. A one-statement NO TRANSACTION file stays one file. The
  header is gone. A re-import meets a file an older forge imported whole under
  its old name: refused without `--force`, replaced with it. On re-pin,
  re-import such files with `--force` on a database you can rebuild (or split
  them by hand, one statement per file).
- **`forge lint --migration-safety` refuses a migration that cannot run as one
  transaction.** forge's migrator sends a migration file to postgres as one
  query, and several statements in one query run as one implicit transaction.
  A file with two `CREATE INDEX CONCURRENTLY` statements, or one beside any
  other statement, passed the lint and then failed `migrate up` with SQLSTATE
  25001, leaving the schema dirty (reliant's goose cutover hit seven). The new
  `non-transactional-statement` rule reports each such statement at its
  file:line, with the fix: a migration file of its own, holding that one
  statement (`forge db migration new <name>`). It covers every statement
  postgres refuses inside a transaction block that a migration could hold:
  `CREATE`/`DROP INDEX CONCURRENTLY`, `REINDEX … CONCURRENTLY` and `REINDEX
SCHEMA|DATABASE|SYSTEM`, `DETACH PARTITION … CONCURRENTLY`, `VACUUM`, a
  table-less `CLUSTER`, `CREATE`/`DROP DATABASE`/`TABLESPACE`, `ALTER DATABASE
… SET TABLESPACE`, `ALTER SYSTEM`, `DISCARD ALL`, and the subscription
  statements that touch a replication slot. `ALTER TYPE … ADD VALUE` is not
  among them: postgres has allowed it in a transaction since 12, and nothing
  forge provisions or tests is older than 16. Using the added value in the
  same file does fail (SQLSTATE 55P04), so `enum-value-used-before-commit`
  reports that use, unless the same file created the type. Both rules are
  errors. They read statements the way postgres does (strings, quoted
  identifiers, dollar quotes, nested comments, BEGIN ATOMIC bodies), so a `;`
  or a `CONCURRENTLY` in a string or a comment is not mistaken for a
  statement, and the tests check every verdict against a real postgres. They
  cannot see the database, so a file postgres does run as one transaction (a
  subscription with no replication slot, a string that only shares an added
  enum value's spelling) says so with `-- forge:allow-in-transaction: <why>`,
  which `forge lint --show-suppressed` lists. On re-pin, a migration the rules
  refuse either never applied anywhere (split it) or is one they misread (mark
  it).
- **`forge db squash` no longer tells you to force a baseline onto a database,
  and writes the same bytes for the same history.** Its last "next step" was
  `forge db migrate force <id>` on every existing database: force records a
  version as applied without checking anything, so it would have marked the
  baseline applied on a database whose schema it never verified, skipping the
  check adoption exists to make. The steps now say what is true (since the
  squashed forge history is declared and adopted — see "A squashed forge
  history is adopted" under Added — that is: a database the squashed
  migrations built adopts the baseline), preview each database with `forge db
migrate status` and a `migrate up` against a copy, and never force. The
  tests check each of those claims against the migrator. The header also dropped the wall-clock
  time and the absolute `--from-dir`: it names the squashed migrations' count
  and version range, so a re-squash with the same `--image` is byte-for-byte
  identical (reliant stripped the old header with `tail -n +6`; drop that on
  re-pin).
- **A proto enum over an INTEGER column projects instead of refusing the
  generate.** forge births enum columns as TEXT holding value names, and the
  CRUD projection mapped only that, so a column the author declared integer,
  storing the wire number (reliant's `tasks.status INTEGER NOT NULL DEFAULT
1`), failed generate with `no conversion between wire kind enum and column
type INT4`. SQL is the schema: an integer column of any width (or
  `INTEGER[]` for a repeated enum) now stores the number. The write path
  routes an unset enum exactly as the TEXT path does (the column's literal
  DEFAULT, NULL for a nullable column), the read path casts the stored number
  back (proto3 enums are open, so an undeclared number passes through as it
  would on the wire; one outside int32 is refused), and a list filter binds
  `int32(req.Status)` rather than the name Postgres would compare as `integer
= text`. Birth still writes TEXT + CHECK.

- **The `observability` skill loads whole again.** It had grown to 24,390
  bytes, over the 24,000-byte load cap, so a loader truncated its tail (the
  sub-skill pointers and the rules). The DB-persisted audit-trail recipe on
  `forge/pkg/audit` (the table, the DB-backed interceptor, the scoped
  `ListAuditEvents` read RPC) moved to its own sub-skill,
  `observability/audit-log`. The parent keeps a one-paragraph pointer that
  says when to load it, and is now 21,946 bytes.
- **The ORM refuses a name your `internal/db` already declares, before it
  writes anything.** forge projects each entity into the one `internal/db`
  package (`Task`, `CreateTask`, `TaskStore`, the aggregate `Store` /
  `NewStore`), and a project adopting forge usually has a data layer there
  already. reliant's `type Task = core.Task` met task_orm_gen.go in the
  validate build as `Task redeclared in this block`, and generate reverted
  everything with neither the declaration nor a way out named. The
  projection now renders first and refuses a collision with the project's
  declaration (`file:line`), the entity and table behind it, and the three
  resolutions: keep that table off forge's ORM by marking its CRUD-named rpcs
  `// forge:custom` (the per-entity path off a hand-written or sqlc data
  layer), hand the name to forge (rename yours; sqlc: `rename:`), or
  `//forge:no-orm`. The ORM's package and names stay fixed rather than
  configurable: every generated consumer and owned file relies on them, and
  a project's coexistence need is per entity, not per package.
- **The ORM no longer deletes a hand-written `internal/db/types.go`.** The
  projection removed `internal/db/types.go` unconditionally (it was once
  forge's own proto-era alias file), so a project whose data layer had a
  `types.go`, the most ordinary name there is, lost it on the first generate
  that projected an entity. Only a forge-written one is retired now.

- **A `migratekit.Migrator` is one Postgres session, so concurrent
  `db migrate up` runs work behind a session-mode PgBouncer.** A Migrator held
  its run-lock connection and a second session that golang-migrate's pgx driver
  opened from the DSN, and a run waiting for the run lock sat on both. Behind a
  session pooler with two server connections (reliant's prod pool per
  PgBouncer replica), two concurrent runs deadlocked until `query_wait_timeout`
  killed one: `open migrator: failed to open database: driver: bad
connection`. `Open` now checks one connection out of its own pool (capped at
  one) and binds golang-migrate to it, as `AutoMigrate` does since the
  one-connection fix; `Up`, `Force` and `State` all run on it, and a waiter
  holds that session and nothing else. `forge db migrate *` and every
  scaffolded `db migrate` command go through `Open`. golang-migrate's own
  `x-` DSN options (`x-migrations-table`, `x-statement-timeout`,
  `x-multi-statement`, …) are still honoured, and a failed migration still
  reports `migration failed: <message> (column N) in line L`, where the
  driver now underneath used to print `in line 0`. `Migrator.Up` returns what
  a failed run did (`Before`, a committed adoption) alongside the error.
  Tested behind a real session-mode PgBouncer (`-tags integration`) and a
  per-role connection cap.
- **Under `//forge:no-orm`, a CRUD-named rpc gets a stub instead of
  vanishing.** The stub step skipped every rpc that matched an entity, and the
  CRUD step, which is off with the ORM, never implemented it: the rpc had
  neither a stub nor a shim and served Unimplemented with nothing in the tree,
  `shapes` or the audit saying so. The stub step now skips exactly what the
  CRUD step will implement, under the CRUD step's own gate, so with no ORM
  every rpc is stubbed. The skip set is also per service: one service's CRUD
  `CreateTask` no longer suppresses another service's custom `CreateTask`
  stub.

- **The test harness imports a config only for a component that takes one.**
  `helpers_gen_test.go` imported `<module>/pkg/config` for every service and
  every wired contract package, and declared `WithConfig` and a
  `&config.Config{}` default around it. A project without typed config (no
  `proto/config`, so no `pkg/config`; reliant, when it adopted contracts) got
  a harness that did not compile beside each of them. The harness now reads
  the component's `Deps.Config` field: when it is `*<pkg>.Config` the harness
  imports that package (the one the component's own Deps names, aliased
  `config` when its path does not end in `config`) and keeps `WithConfig`, the
  default and the Deps fill. A component without the field gets none of them.
  A service whose Deps has no `Config` no longer gets `deps.Config = cfg.cfg`,
  which did not compile either.
- **An interface your Deps consumes no longer binds the package's own type
  that satisfies it.** The contract analyzer let a type implementing any
  `contract.go` interface export only that interface's methods. An interface
  `contract.go` names as a `Deps` field's type is a requirement, the narrow
  seam `forgeconv-deps-are-interfaces` asks for, and the rule treated it as
  the contract. So a package could not take its own registry through a narrow
  seam: reliant's connauth wanted `Deps.Broker` to be a `Get`-only interface
  over its `*Brokers`, which also exports `Register` for the composition root,
  and turned the lookup into a func field instead. A type that satisfies only
  Deps seams (a field's type, or a pointer, slice, array, map or channel of
  one) is no longer bound. `Service` and a `//forge:service` /
  `//forge:contract` interface still bind every implementer, even when a Deps
  field also names them (a decorator's `Next Service`). Any other `contract.go`
  interface still binds. A bound type may still export the methods of every
  contract interface it satisfies, seams included.

- **`forge env deploy` renders the env once; a cluster group's namespace is
  where it applies and waits, never an input to what the env declares.**
  Deploy re-rendered the whole env once per cluster group with that group's
  namespace bound to KCL's `namespace` option. For control-plane prod's new
  `observability` group that made `observability` the env namespace, so its
  prune override `Namespace/${_namespace}@<hub>` became
  `Namespace/observability@<hub>` and matched two objects (the env's own plus
  the hub's declared `Namespace/observability`): the apply failed with
  `override … matches 2 objects` while `forge env render prod` — which renders
  with the env namespace only — passed. The env is now rendered once, with the
  env namespace `forge env render` uses (`cluster.ApplyOpts.Render`), and each
  group applies its cluster's partition of that one stream
  (`ScopeManifestsToGroup`). `RenderManifests` and `forge env render` share one
  render function. Regression tests drive the deploy's own dispatch against a
  fixture whose second cluster group's namespace equals a namespace the env
  declares on the first.

- **A release that does not render is refused before its promotion is
  recorded, with or without `--skip-preflight`.** The pre-record run of
  `forge env deploy <env> <version>` now also renders every cluster group
  through the apply's own path as a dry run (render, scope to the cluster,
  partition into passes, template charts) and writes nothing. Before, the first
  render of each group happened in the apply, after the compare-and-set write,
  so a group the render or router refused left the ledger claiming a release
  that was never live (control-plane prod, `20261013.044832-36da25fefcf9`).
  `--skip-preflight` still waives the live-target checks (Secrets, images,
  served kinds); it no longer waives the question "does this release render".

- **A freshly scaffolded service lints clean again.** The `metrics` command
  scaffolded into every service binary (`cmd/<bin>/cmd/metrics.go`, #681)
  blank-imported `internal/metrics` with no comment, and the revive
  `blank-imports` rule every scaffolded project runs refuses that outside a
  main package, so `forge lint` failed on a project nobody had touched — and
  on every existing project the next `forge generate` wrote the file into.
  The import now carries the comment saying why it is linked. A template-level
  guard (`TestGoTemplatesJustifyBlankImports`) checks every Go template for
  the same, under `-short`. An existing project's `metrics.go` is yours once
  written: add the comment above the import by hand.
- **`license-check` checks your dependencies, not your own packages.**
  `forge ci gate` ran `go-licenses check ./...` without excluding the project,
  so a project with no LICENSE file logged `Failed to find license` for every
  one of its own packages and then passed, and a project under a restricted
  license would have failed its own gate. The check now passes
  `--ignore <module>` for the module in `go.mod` and for every module a root
  `go.work` uses from inside the project tree (a `--link-forge` checkout
  outside it stays checked). go-licenses matches `--ignore` as a plain string
  prefix, so when a dependency's path extends the module's (`example.com/app`
  and `example.com/app-client`) the ignore is narrowed to the module's subtree,
  `example.com/app/`, and the dependency is still checked (#755).
- **A contract parameter may be named anything; the generated mock,
  decorator and test stubs still compile.** An interface method has no body,
  so `Run(ctx context.Context, m Manifest)` is legal Go. The mock that
  implements it does have one, and its receiver is `m`: the parameter
  redeclared it and `mock_gen.go` did not build (reliant renamed httpaction's
  parameters to get past it). The same shape broke `middleware_gen.go` for a
  parameter named `o` (its receiver), `context`, `r0`/`r1` or `err` (its
  capture locals), or after the package a sibling parameter's type comes from
  (`user user.User`); a parameter `e error` compiled and forwarded nil instead
  of the caller's error. The test harness's auto-stubs broke on `new`, a local
  type's name, or a result type's package. Mocks and decorators now rename
  such a parameter to the positional `pN` an unnamed one already gets, and
  drop result names, which no generated body assigns; auto-stubs, whose
  bodies never read a parameter, declare types only. Parameter names are not
  part of a method's type, so callers see no change.

- **The deploy preflight checks a referenced Secret or ConfigMap where its pod
  reads it.** The manifest-driven check recorded references by name only and
  looked every one up in the env's deploy namespace on the primary cluster, so
  a workload rendered into another namespace, or onto another cluster, was
  refused for a Secret that existed exactly where it is used. control-plane's
  prod release was blocked on `control-plane-prod/obs-credentials` and
  `control-plane-prod/otel-agent-token`, which live in `observability-data` and
  `observability` (on both clusters). A reference is now located by the
  referencing object's `metadata.namespace` (the deploy namespace when unset)
  and, in a multi-cluster env, by the deploy's own router — the same
  attribution `forge env render` prints in its `# cluster:` headers. The same
  location drives the rest of the gate: a rendered Secret satisfies only
  references in its own namespace, a Secret the deploy provisions is exempt
  only in the namespace it is provisioned into, a declared
  `forge.ExternalSecret` folds in only the keys referenced from its namespace
  on the cluster being checked, and `imagePullSecrets` are resolved from the
  pod's namespace and cluster. On a run that spans clusters every finding,
  declared prerequisites included, is reported as `<context>:<namespace>/<name>`
  (previously `<context>/<namespace>/<name>` for declared prerequisites only).
  An absent ConfigMap is reported as `(the ConfigMap does not exist)` rather
  than `(the Secret does not exist)`.

- **`migratekit.AutoMigrate` works on a pool capped at one connection.** It
  held one pool connection (the run lock and golang-migrate's driver) and then
  asked the same pool for another for every record it keeps — applied,
  compatibility, goose adoption — so at `MaxOpenConns=1`, which a desktop or
  monolith build on embedded postgres may set, boot waited forever for the
  connection it was holding. A run now does all of its own work on the one
  connection that holds the run lock, through `AutoMigrate` and `Migrator.Up`
  / `Force` alike; the record helpers take a `*sql.Conn`, so a pool cannot be
  handed to them. Adoption and `Force` hold `appliedLockKey` at session level
  and commit the record before golang-migrate writes the version, since at
  boot golang-migrate shares the session and its transaction would otherwise
  commit migratekit's early. That also makes `Force` safe to re-run after a
  crash between the two writes; the old order left the forced version with no
  record, refused as missing.
- **The typed-config guardrail checks env reads, or says it cannot.** The
  advisory lane runs forbidigo with whatever the project's golangci-lint config
  gives it. A `.golangci.yml` with no forbidigo `forbid` list (a hand-owned
  one, as in reliant) left forbidigo on its built-in `fmt.Print*` patterns, so
  the lane filed every `fmt.Println` as a typed-config finding and never looked
  at an `os.Getenv`. The lane now reads the config golangci-lint loads
  (`.golangci.{yml,yaml,json}` in the project or its nearest ancestor, v1 or v2
  layout). Unless a forbid pattern covers `os.Getenv`, it reports itself as
  could-not-run (`typed-config-guardrail-unavailable`, an error under
  `--strict`) with the patterns to add, and does not invoke golangci-lint. A
  clean run also stops reporting `typed-config guardrail — 1 warning` whose only
  detail was `0 issues.`: golangci-lint's summary and source snippets are not
  findings, only file-anchored issues are (`--quiet`, `--scope`, `--json`).
- **A dirty schema's refusal names a force command the reader has.**
  `migratekit.AutoMigrate` and the scaffolded `<binary> db migrate up` told the
  operator to run `migrate force N`, golang-migrate's CLI, which forge no
  longer uses and a production image does not carry. The scaffolded binary
  now has its own `db migrate force <version>` (migratekit's `Force`, so the
  applied record stays in step), and `up` names it by the binary's real
  command path: the image is the one thing every environment that reads the
  refusal has, and a hosted database is reachable from nowhere else.
  `AutoMigrate`, which cannot know its binary's commands, names
  `forge db migrate force N` from a checkout of the release (new
  `migratekit.ForceCommand`, tested to resolve) and the binary's own `db
migrate force N` where it has one. The `db` and `db/deploy-migrations`
  skills, `docs/concepts.md` and the `forge db squash` doc stop naming the
  golang-migrate CLI or crediting its lock. `db.go` is scaffolded once, so an
  existing project gains `force` by copying it from a fresh scaffold.

- **`forge project new` without a git identity says what happened, in one
  line.** With no `user.name` / `user.email` (and none git could guess), the
  initial commit failed and forge printed git's whole "Please tell me who you
  are" fatal error as `failed to initialize git repository`, then reported
  success. The repository was in fact initialized, hooks activated and every
  file staged; only the commit was missing. It now prints one warning saying
  so, with the command to finish it:
  `git commit --no-verify -m "Initial commit from forge"` (`--no-verify`
  because the scaffolded commit-msg hook runs commitlint). git's own output
  is shown under the new `project new --verbose` (`-v`). Other git failures
  still show git's output (#750).

### Changed

- **`pkg/alerting` keys its partitions by `Owner`.** The package's opaque
  partition key now uses the word forge already uses for whose a row is
  (`forge:owner`), instead of a vocabulary forge removed: `RuleKey.Owner`,
  `EventQuery.Owner`, `Limits.MaxRulesPerOwner`, `FileRuleSource.Owner`,
  `RuleSource.Owners`, `StateStore.Owners` and `StateStore.LoadOwner`. In the
  `pgstore` schema, `alert_instances` and `alert_events` are keyed on an
  `owner` column and their indexes are named for it. A consumer that copied
  `pgstore.SchemaSQL` into its own migrations renames that column in both
  tables in a new migration.

- **`forge generate` validates without linking, and loads the tree once.**
  The validate step ran `go build -o /dev/null ./...`, which links every main
  package and throws the binary away, then loaded the whole tree again through
  go/packages to typecheck `_test.go` files, type-checking every dependency
  from source. On reliant (19 main packages, cgo, so each link is an external
  gcc/ld run) that was 217s of a 258s run. It is now one
  `go list -e -export -test -deps ./...`: go list runs the real compiler on
  every package and every test variant (it reports their export data) through
  the shared build cache, and never links. A main package's compile error
  still fails validation; the one linker-only error a generator can cause, a
  main package without `func main`, is checked directly. Errors are read from
  go list's per-package JSON, so a broken library is reported once, not once
  per importer. Ownership rules are unchanged: a non-test compile error or a
  forge-owned `_test.go` fails the run, a user-owned `_test.go` warns. The
  test variants it compiles are the ones `go test` builds next, which then
  finds them cached. The post-rollback "does the restored tree compile?"
  check uses the same no-link compile. If go list itself cannot run, both
  fall back to `go build`. Measured on reliant (load average 60-110 on 16
  cores): a `forge generate` that changes nothing went from 274s to 51s
  (validate 206s to 14s); one that rewrote two files from 236s to 142s
  (validate 218s to 97s, most of it compiling their dependents' test
  variants, which `go test` then reuses); and from an empty build cache the
  validate commands went from 872s (`go build` 814s, typecheck 58s) to 617s.
  On re-pin, the first generate in a checkout compiles every package's test
  variant once (about 200s on reliant when the non-test build is already
  cached); every later run, and the next `go test`, reuses them.
- **`FORGE_NO_GIT_HOOKS` is the name for opting out of forge's git hooks, and
  `forge generate` says which hooks it ran.** `FORGE_NO_HOOKS=1` stops forge
  setting `core.hooksPath=.githooks` and its merge driver in a clone. Agents
  kept setting it expecting `forge generate` to skip forge.yaml's
  `generate.hooks` (reliant's `make sqlc`), and it never did. It is now
  spelled `FORGE_NO_GIT_HOOKS`; `FORGE_NO_HOOKS` is still honoured as the same
  git-only opt-out, so a clone opted out under it stays opted out. Generate
  hooks still always run: a skipped hook leaves output that still compiles but
  is stale, and `make sqlc` measured 3s of reliant's generate, so there is no
  skip flag. The generate output names every hook before they run and confirms
  each one (`✓ generate hook "sqlc" ran (3.2s)`); with either git-hooks
  variable set, the step says that it does not skip these. A run narrowed with
  `--steps` or `--templates-only` that leaves the hooks out now prints
  `⏩ generate hooks NOT run (...)` naming them and the paths they write.
  `forge generate --help` has a Hooks section saying all of this, and so do
  the forge-managed `.githooks/pre-commit` comment and the `architecture`
  skill. On re-pin, `forge generate` rewrites `.githooks/pre-commit` (the
  comment only); rename `FORGE_NO_HOOKS` to `FORGE_NO_GIT_HOOKS` where you set
  it.
- **A config env var set to `""` is unset, for every field type.**
  `pkg/config`'s env layer read an empty variable as a value for strings, bytes
  and lists, and as unset for numbers, bools and durations. So `TEMPORAL_HOST=`
  (what compose's `X: ${X}`, a template rendered from a missing key, or
  `export X=` produce) replaced a `localhost` default or a config file's value
  with `""`, and `ALLOWED_REGISTRIES=` cleared a defaulted list. Every kind now
  reads it as unset and the file or default value stands. An explicitly empty
  value is a flag's to set: `--pprof-addr=` turns pprof off,
  `--allowed-registries=` clears a list. A required field whose variable is set
  but empty fails with an error that says so. The generated KCL config schema
  gains a `check:` refusing `field = ""` for a non-sensitive string field with a
  non-empty default: it would render an env value the process ignores, so the
  render fails and names the flag instead. On re-pin, `PPROF_ADDR=` no longer
  disables pprof (pass `--pprof-addr=`), and `forge generate` rewrites
  `deploy/kcl/config_gen.k` with the checks.

### Added

- **A squashed forge history is adopted, not refused: `-- forge:adopts forge
<versions>`.** After `forge db squash` replaced a forge history with one
  baseline, every database that history built was refused, and misleadingly:
  "schema at 20260101000002, AHEAD of the newest migration this binary embeds
  (20260201000000)" for a baseline numbered above the history, and "MISSING …
  give it a NEWER version" for one below it. A baseline can now declare the
  forge versions it replaces, the shape of goose's adoption (#649), and
  `forge db squash` writes the list (plus `-- forge:backward-compatible`: the
  baseline builds exactly the schema the release before the squash runs on).
  `migratekit` adopts a database whose schema version is one the baseline
  lists when every listed version has a `schema_migrations_applied` row (a
  database older than that record is judged by its `schema_migrations`
  version, which golang-migrate never applies below): the baseline gets its
  applied row and becomes the schema's version, none of its SQL runs, and the
  migrations after it apply on the same run. One behind the squash (a listed
  version not applied) or past it (a schema version the list lacks) is a
  `*migratekit.AdoptionError` with forge's remedies, and nothing changes. The
  history's rows stay, so the release before the squash, rolled back to,
  still finds its migrations recorded and reads the baseline as a compatible
  schema ahead of it. Adoption runs under the run lock and `appliedLockKey`
  on the run's one session (#706), and finishes an adoption a crash
  interrupted. The migrator refuses, at `Open` and at boot, a forge baseline
  listing a version it still carries (its own number, or a squashed file left
  beside it); `forge lint`'s `invalid-adopts-directive` reports the same, and
  `forge db squash` refuses a `--to` inside the history. Without the
  declaration, a database built by a history the binary does not carry is now
  a `*migratekit.ForeignHistoryError` that says so and names the fix, instead
  of AHEAD or MISSING, and `forge db migrate status` previews both cases
  rather than calling them AHEAD. `Adopted.String()` is the adoption line
  every command prints; the scaffolded `db.go` uses it. Skills:
  db/deploy-migrations ("Squashing a forge history"), db/versioning. On
  re-pin, nothing changes for a project that has not squashed a forge
  history; one that has can re-run `forge db squash` (or add the
  `-- forge:adopts forge` lines by hand) and drop any rebuild-every-database
  plan.
- **`forge db squash --from-dsn`: squash a database another tool migrated.**
  `forge db squash` replays only forge's `*.up.sql`, so a goose history could
  not be squashed: reliant pg_dumped a goose-built database by hand, stripped
  pg_dump's prelude by hand, and typed the `-- forge:adopts goose …` list.
  `--from-dsn <postgres URL>` dumps an existing database instead, whatever
  tool built it, through the same `squashBaseline` lexing that drops pg_dump's
  session prelude and psql meta-commands (#678). pg_dump runs in a
  `postgres:<server major>-alpine` container (`--image` overrides), and the
  password reaches it through the environment, never the command line. When
  the database has `goose_db_version`, the baseline's header declares every
  version goose applied there (`-- forge:adopts goose …`, eight per line), read
  as migratekit's adoption reads the table, and the command checks the file
  parses back to that list. Point it at a throwaway database the complete
  history built (`goose up` on an empty database): its rows become INSERTs in
  every fresh database, so the command lists them per table, and it names any
  table that looks like another tool's history (`--exclude-table` leaves one
  out; `goose_db_version`, an older goose's `goose_db_version_id_seq`, and
  forge's bookkeeping always are). The header names only the history (count
  and version range), so a re-cut writes the same bytes. Rehearsed on reliant's 152 goose migrations: the goose-built database
  is ADOPTED (152 versions verified), a fresh database applies the baseline,
  and their `pg_dump --schema-only` are identical. The db/deploy-migrations
  skill's hand recipe is replaced by the command. On re-pin, reliant can
  re-cut its P5a baseline with it and drop the hand-made dump.
- **`migratekit.Apply(ctx, db, fsys, dir) (Result, error)`: the boot path's run,
  returning what it did.** `AutoMigrate` is now `Apply` plus a log line per
  fact. A command that wants the boot path's policy (a binary embedding no
  migrations is `Result.NothingEmbedded`, not an error; a dirty schema is
  refused before anything runs) can report adoption, a schema ahead, and the
  versions applied without switching to a `Migrator`. It runs on one
  connection borrowed from `db`. `AutoMigrate`'s boot log no longer has the
  separate `migration status` line; `no pending migrations` now carries the
  version.
- **`// forge:custom`: declare an rpc custom whatever its name.** An rpc was
  CRUD whenever it was unary, named `Create<E>`/`Get<E>`/`Update<E>`/
  `Delete<E>`/`List<Es>` and `<E>` had a table and a wire message, and the
  framework guide forbids the only way out (renaming it). reliant has 52 such
  rpcs whose semantics are its own. Put `// forge:custom` on its own line
  above the rpc and it is custom on both sides of the wire: a pb-through stub
  and a scaffold test row, no CRUD shim or op, no entity page, a hook that
  invalidates at service scope, no list-filter lint. It makes no entity, so
  marking every CRUD-named rpc of a table is the per-entity opt-out: the table
  stays plain schema, with no ORM struct in `internal/db` (no `Task
redeclared` against a project's own `Task`), page or seed root. A comment
  marker, like `forge:unmanaged`, because who implements a handler is not a
  fact about the API contract and must not cost the `forge.proto` import.
  Generate refuses a near-miss spelling, the marker on a service or message,
  a trailing marker (protobuf hands a comment under one rpc to that rpc even
  when it was written for the next), a marker the source shows but the
  compiled descriptor lost, and a newly custom rpc whose shim or lifecycle
  test still uses its `crud<Rpc>Op` or create-request factory, naming the
  file and line. `forge project audit` marks each in
  `shape.services[].rpcs[].custom`.
- **`forge project audit` reports rpcs with no handler method
  (`unhandled_rpcs`).** A Service embeds `Unimplemented<Svc>Handler`, so an
  rpc of a managed service with no `*Service` method compiles and serves
  `CodeUnimplemented` with no stub saying so, invisible to `orphan_stubs` and
  `shapes`. The category names each one (warn), and lists a service whose
  struct embeds another type, which may promote the method, as unresolved
  rather than accusing it.

- **`forge lint --show-suppressed`: every suppression the project declares, in
  one list.** The `contracts/package-markers` skill told users their
  allowances were "reported by `--show-suppressed`", and `forge lint` answered
  `unknown flag`. It now lists, with file:line, what each covers and its stated
  reason: `forge.yaml` `lint.rules` entries set to off or warn and
  `contracts.exclude` paths, `//forge:exclude-contract` markers,
  `forge:lint-disable-file` / `-next-line` / block and `//nolint:` directives,
  `-- forge:allow-destructive` / `-- forge:allow-unsafe-not-null` migration
  pragmas, and `// forge:optional-checked`. Each source is read by the code
  that honours it (`suppress.Declarations`, `migrationlint.Allowances`,
  `codegen.FindExcludeContractMarker`, the optional-deps guard's own matcher),
  so the list cannot disagree with the linters. Hand-written files only;
  generated files, `testdata`, `node_modules` and `vendor` are skipped.
  Read-only: it runs no linter and exits 0. `--json` and `--scope` combine
  with it; a lane flag, `--quiet`, `--gate-json` or package paths are refused.
- **`alerting.RuleSpec.KeepFiringFor` (`keep_firing_for`)**: holds a firing alert open that long after its series stops returning, like Prometheus's rule field of the same name. The shape's `alerts[]` (#684) declares it, so the evaluator now honours it instead of silently ignoring it. The one-evaluation flap damping every rule already had is the floor.
- **`(forge.v1.config).env_var_aliases`: a config field read from fallback env
  var names.** A field reads `env_var`, then each alias in order, and the first
  one set wins. This replaces hand-written chains such as
  `RELIANT_CONTROL_PLANE_URL`, else `CONTROL_PLANE_API_URL`, else
  `CONTROL_PLANE_BASE_URL`. Each alias is
  `{name: "...", deprecated: bool}`. A permanent alias (`NODE_ENV`) is read
  silently. A deprecated one prints a stderr line at load whenever it is set,
  naming `env_var`, whether it supplied the value or was shadowed. Aliases sit
  in the env layer (below the flag) and follow its rules: `""` is unset, and a
  malformed value in the winning name is an error, never a fall-through.
  forge's own setters keep to `env_var`: the KCL env map and the secret store
  key name it alone. Flag help now lists the env vars that set a flag,
  permanent aliases included (`--environment string   … [env: RELIANT_ENV,
NODE_ENV]`). A required-field error names every alias. `forge generate`
  refuses an alias that repeats any env var or alias another field of the same
  process reads (a command config counts its binary's root config), repeats
  the field's own `env_var`, has no `env_var` beside it, is not a variable
  name, or sits on a frontend config field. New proto message
  `forge.v1.EnvVarAlias`; `ConfigField.EnvVarAliases` joins
  `gen/forge_descriptor.json` (omitted when empty). The typed-config guardrail
  is name-agnostic, so a raw `os.Getenv` of an alias is flagged like any other.
- **`alerting.QueryOptions.Limits` and `chquery.Scan`**: the per-query limits without `readonly`, and the row reader behind `chquery.Run`, for a `Querier` that runs as an impersonated ClickHouse user (`EXECUTE AS`). Such a Querier cannot send `readonly` (a readonly session refuses the `EXECUTE AS` statement itself); the target user's profile contains the query instead. `Settings()` is now `Limits()` plus `readonly = 2`.

- **Alert rules, declared per env: `forge.AlertRule`, `forge.AlertRoute`,
  `forge.AlertDefault`.** On `forge.Observability.alerts = forge.Alerts {rules,
defaults, routes}`. A rule is the Prometheus shape with the condition inside
  `expr`, a single native ClickHouse SELECT over the OpenTelemetry tables whose
  every row is a firing instance (non-`value` columns are labels, `value` the
  sample); `severity` is a label (critical | warning | info, default warning).
  Render refuses: missing fields, a non-duration `for`/`interval`, an interval
  under 1m, an `expr` that is not one SELECT, reserved labels (`alertname`,
  `reliant_*`), duplicate names, a route receiver that is a URL or a token, and
  routes with no default (empty-`match`) route. Routes name receivers; secrets
  live in the control plane.
  `forge generate` writes `deploy/observability/alert-defaults_gen.json` and
  `forge env shape` resolves four **generated defaults** per env from it (error
  rate and p95 latency from the unsampled `rpc.server.*` metrics, service silent
  only for services with steady traffic over the previous 24h, frontend JS error
  spike against the 1h baseline), each tunable or disabled with
  `Alerts.defaults`. They route to in-app + Slack, never a pager. The release
  shape gains optional `alerts` (rules from the user and the enabled defaults,
  `source: user|default`, `pkg/alerting`'s RuleSpec JSON) and `alert_routes`, for
  the control plane to evaluate; a shape that declares none encodes and hashes as
  before. `output.observability.alerts` is a new optional render-contract key.
  See `observability/alerts` and `migrations/v0.1.45` section 9.
- **Observability opt-out: `enabled = False`, or per category.** Hosted
  observability is on by default; `forge.ObservabilitySignals` gains `metadata`
  (customer metadata: pod CPU/memory against limits, restarts and last exit
  reason, events, image and version, collected by the platform's node agent) and
  `browser` (the frontend SDK) beside `traces`, `metrics`, `logs` and `profiles`.
  A disabled signal renders `OTEL_<SIGNAL>_EXPORTER=none`; `enabled = False`
  renders `OTEL_SDK_DISABLED=true`; `browser` (or everything) off renders
  `OTEL_ENDPOINT=off` into every frontend's runtime config and build variable.
  The resolved policy is recorded in the release shape as `observability_policy`
  (`{enabled, disabled[]}`, omitted when nothing is opted out), so the platform
  enforces it at the source. Disabled data is never collected, stored or billed.
  **`signals.replay` is the one opt-IN category** (default `False`): the platform
  drops browser session-replay events unless the env opts in (`opt_in` in the
  policy).

- **`pkg/alerting`: a SQL alert-rule evaluator in the Prometheus shape.** A
  rule's `expr` is one ClickHouse SELECT; every row it returns is a firing
  instance, its non-value columns the labels and its `value` column the sample.
  The package owns the server-side guard (single SELECT, no table functions
  that read outside the owner's database, `readonly=2`, `max_execution_time`,
  `max_result_rows`), label protection (`reliant_*` is stripped from rules and
  columns; the `RuleSource` supplies identity labels, applied last), the
  inactive → pending → firing → resolved state machine with one-evaluation flap
  damping, a jittered scheduler sharded over `pkg/lease`, and an Alertmanager v2
  client. It knows nothing of orgs, billing or any particular database: the
  owner key (whose data a rule queries) is an opaque string, and `Querier`,
  `RuleSource`, `Notifier` and
  `StateStore` are interfaces declared in the package. Ships `RuleSpec` (the
  rule model), `FileRuleSource` (hot-reloaded rules file, keeps the last good
  rules), `chquery` (a ClickHouse `Querier` for one owner), `pgstore` (Postgres
  `StateStore` and its schema) and `MemoryStore`.

- **`Bundle.applier = "forge"`: a declared self-applied platform env.** A real
  environment forge applies on purpose and permanently, for an env whose own
  control plane must not apply it (control-plane's `prod`: a hub applying the
  env that runs the hub is circular). It is exempt from the planned
  F-REFUSE-DIRECT, is **never recorded hub-converged** however fully its
  clusters are bound to connected clusters, and is recorded in the shape
  (`shape.applier`). Setting it beside `lifecycle` is refused: they answer one
  question opposite ways. `output.applier` is a new render-contract key (null
  when undeclared); every `render_contract` golden gains `"applier": null`.
  See `deploy/shape` and `migrations/v0.1.45` section 8.

### Removed

- **The pending alert definitions in `dashboard-spec_gen.json`** (#644): the
  `alerts` array (`status: pending`) and `obsdash.Alert`. The three definitions
  became the `error-rate`, `p95-latency` and `service-silent` defaults.

- **The dead in-cluster Flux pointer path.** #461 made `reconcilesThroughFlux()`
  unconditionally false and asked for the follow-up that deletes the rest. Gone:
  forge's own Flux install in `forge cluster up`, the OCIRepository/Kustomization
  pointer `forge env deploy` wrote, its convergence wait, the Flux-secret sync,
  the Flux plan-live reader, `forge env status`'s in-cluster convergence, and
  the `internal/flux` pointer/observe code (net about 4,000 lines removed). What
  stays is `forge.flux_chart()` and the declared bootstrap-chart install. The
  direct-apply notice no longer claims the env "will be reconciled from its
  bundle".

- **Project-mutating commands refuse to run under a forge other than the
  project's `forge_version` pin.** `forge generate`, `forge scaffold` (and every
  noun under it), `forge package new`, `forge component install`,
  `forge env new`, `forge project rescaffold|delete|migrate`, and the
  auto-generate that `forge build` / `forge env up` run when generated code is
  stale, now stop with a runbook: running vs pinned version, a `go run
github.com/reliant-labs/forge/cmd/forge@<pin> <same args>` that needs no
  install, and the `GOPRIVATE=... go install ...@<pin>` form. Previously only
  `build` / `env build` / `env deploy` refused; an agent running `forge generate`
  with a stale forge on PATH silently rewrote unrelated generated files (otel,
  frontend, descriptor). Read-only runs are never blocked (`--dry-run`,
  `--check`, `lint`, `audit`, `shapes`, `skill`, `env status`, `--help`).
  Escape hatch: `--allow-version-skew` or `FORGE_ALLOW_VERSION_SKEW=1`; builds
  without a version identity (`(devel)` with no commit), forge's own repo, and a
  project bridged by go.work to the checkout the binary was built from are
  exempt. Under `reliant forge` the comparison uses the embedded forge's
  module version, not reliant's.
- **`forge.ControlPlaneSecret`: a Secret the env's declared control plane
  supplies and renews.** `Bundle.control_plane_secrets` declares one (`name`,
  `cluster`, `namespace`, `kind = "telemetry_token"`). It is recorded in the
  env shape (`shape.control_plane_secrets`: name, namespace, cluster,
  connected cluster, kind, keys; never a value), **never blocks a first deploy**
  (the preflight neither requires it nor reads its absence as a missing
  dependency, and it counts as supply for the undeclared-mount gate), and is
  **refused at render without a connected-cluster binding** or without
  `control_plane`. There is no k3d exemption: delivery is the control plane
  dialing the cluster. A cluster workload in the same cluster and namespace
  gets an optional whole-directory Secret volume `forge-otlp-auth` at
  `/var/run/secrets/forge/otlp` and `OTEL_EXPORTER_OTLP_HEADERS_FILE`, so a
  renewal needs no restart. `ObservabilityDestination.token_from_control_plane
= True` derives the node agent's Secret (`forge-otlp-token`, key `token`) on
  every cluster running the agent. `v1alpha1.SecretVolumeSource` gained
  `optional`. `output.control_plane_secrets` is a new render-contract key
  (every `render_contract` golden gains `"control_plane_secrets": []`). See
  `deploy/control-plane-secret` and `migrations/v0.1.45` section 7.

- **`forge env deploy <env> --offline-ledger --to <release|previous>`: roll back
  with the control plane unreachable.** The realistic outage is a bad release
  that takes the control plane down, where redeploying the current release
  does not help. The offline copy of a hosted env's ledger now keeps its last N
  promotions (default 5, `FORGE_OFFLINE_LEDGER_WINDOW`, max 50) with each
  release's recorded digests, refreshed on every successful ledger read or
  write. `--to previous` (or a cached release version or promotion id) deploys
  one of them by digest behind the usual confirmation gate; a release not in the
  copy is refused with the list of what is. The rollback is queued and replayed
  on the next ledger contact with the existing moved-env refusal; several
  offline deploys in one outage chain and replay in order. `forge env status`
  lists the releases the copy can deploy (`offline_cache` in `--json`), so the
  break-glass options are visible before the outage; each operator machine needs
  one successful `forge env status` to warm its copy.
- **`forge env deploy <env> --offline-ledger`: a break-glass for a hosted ledger
  whose control plane is down.** With the control plane unreachable, a
  `forge.ControlPlane` env could not resolve its current release, and
  `FORGE_LEDGER=machine` hit the unimported-ledger refusal without the live
  release; the only fallback was an unrehearsed fresh cut that rebuilds every
  image. forge now keeps a read-through copy of each hosted env's current
  promotion and release (per project and env, under `$FORGE_LEDGER_HOME`),
  refreshed by every successful ledger read or write. `--offline-ledger`
  deploys that release by its recorded digests with no build and no contact
  with the control plane, prints the copy's age and what it will replay, and
  queues the promotion (with the apply's verdict). The next successful ledger
  read replays the queue idempotently, as a compare-and-set against the
  promotion the copy named: if the env moved meanwhile the record is refused
  and stays queued rather than overwriting it. It redeploys the cached release
  only, refuses envs whose hosted tiers or hub-converged clusters the control
  plane applies, and is the only bypass of the unimported-ledger refusal.

### Added

- **Telemetry credentials rotate with no restart.** `forge/pkg/observe`
  honours `OTEL_EXPORTER_OTLP_HEADERS_FILE` (and
  `OTEL_EXPORTER_OTLP_<SIGNAL>_HEADERS_FILE`): a file in the
  `OTEL_EXPORTER_OTLP_HEADERS` format, attached per request on all four signals
  (traces, metrics, logs, profiles; HTTP via a RoundTripper, gRPC via per-RPC
  credentials) and re-read when its mtime changes (stat at most every 5s, no
  goroutine). Headers in `OTEL_EXPORTER_OTLP_HEADERS` win over the file. A
  missing or unreadable file exports without its headers and logs one WARN per
  minute; it never fails or blocks the app.
  On a cluster, `forge.ObservabilityDestination.token_secret` mounts the
  node agent's bearer token as a file (Secret volume, no `subPath`) read by
  `bearertokenauth{filename}`, which re-reads it on change; it is exclusive with
  `headers_secret`, which is still read once at start. Verified against
  opentelemetry-collector-contrib 0.162 with a kubelet-style symlink swap
  (`RUN_HEADERS_FILE_SMOKE=1 go test -tags integration -run TestHeadersFileSmoke ./pkg/observe/`).
  Rendering the file mount for a workload's own volume waits on
  `forge.ControlPlaneSecret`.

- **`forge cluster connect --delivery-secret <ns>/<name>` grants the control
  plane least-privilege write access to declared Secrets.** Per namespace, a
  Role with name-pinned `get/update/patch/delete` plus `create`, and a
  fail-closed ValidatingAdmissionPolicy that admits only the declared
  namespace/name pairs, only as `Opaque`, for the delivery identities only.
  `--delivery-subject` binds an optional dedicated identity. Off unless named;
  re-running is an idempotent upsert; `--dry-run` prints every object;
  `disconnect` removes the grant.

- **`pkg/lease` and `lifecyclekit.Singleton`: run a background worker on any
  number of replicas and have exactly one do the work.** A Postgres
  advisory-lock lease (`lease.New(db, opts).Run(ctx, name, fn)`): one replica
  leads, the others stand by and take over within `RetryInterval` of the holder
  exiting, or at once when its database session dies, with a heartbeat that
  cancels a leader whose session was swapped or dropped (overlap after a
  partition is bounded by `HeartbeatInterval`). `lifecyclekit.Singleton(l, w)`
  wraps a `serverkit.Worker` in a named lease. It needs no schema, no
  Kubernetes RBAC and no new infrastructure, and behaves the same under
  `forge env up`, compose and a cluster. The `workers` skill says which workers
  need one (and which must not take one). Tested against real Postgres,
  including backend termination.

### Fixed

- **`forge generate` no longer composes a package it cannot construct, and no
  longer calls `compose.go` hand-written.** The pre-codegen contract check
  stays silent on a `contract.go` with no interface and on an interface
  catalogue with no `Deps` and no constructor, but composition composed both
  anyway: it wrote `pkg.New(pkg.Deps{})` calls that could not compile, and the
  validate build then told the author that `internal/app/compose.go`, which
  forge had just written, was "hand-written, not generated". Composition now
  refuses before writing `compose.go`, naming each package and exactly what is
  wrong (no `type Deps struct`, no constructor, `` `New` is `func(x int) *Impl` ``
  …) and the markers that make the tree truthful (`//forge:external-component`,
  `//forge:not-a-component`, `//forge:exclude-contract`). It is not silently
  left out: membership stays declared, never inferred. A compile error in a
  reconciled file (`compose.go`, `lifecycle.go`) now says forge may have
  injected the failing line instead of blaming the author.
- **`forge project new --in-place` no longer ignores files the repository
  tracks.** The scaffold's `.gitignore` ignores a root `/package.json`,
  `/package-lock.json` and `go.work.sum`, which a forge project never commits
  but an existing repository may (reliant commits all three), and the in-place
  merge appended them anyway. Every forge pattern is now checked with git's own
  matcher (`git ls-files --cached --ignored --exclude=<pattern>`, anchored to
  the target directory when it is a subdirectory of the repository); one that
  matches a tracked file is left out, with a comment in its place when forge
  writes a fresh `.gitignore`, and the run lists each skipped pattern and the
  tracked files it would have hidden.
- **`forge project new --in-place` starts an adopted project's typed-config
  guardrail advisory.** It wrote `config: {enforce_typed_access: error}` into
  every new `forge.yaml`, so a project adopting forge failed its first
  `forge lint` on every `os.Getenv` it already had. An in-place target that
  already holds a project now gets an explicit `warn` (the strictness an absent
  key already resolves to), in `forge.yaml` and in the scaffolded
  `.golangci.yml` alike, and the run says so. Greenfield projects, including an
  in-place scaffold into an empty directory, keep `error`. The `migration`
  skill and `docs/concepts.md` now document both defaults and when to flip.
- **`forge lint` runs every lane flag it is given.** `forge lint --conventions
--contract` ran whichever lane came first in the dispatch chain, skipped the
  other without a word, and printed ✅ — a green verdict over a lane that never
  executed (text mode, `--quiet`/`--scope`, `--json` and `--gate-json` alike).
  Every named lane now runs, each exactly as it runs alone, and one verdict
  covers all of them: `N gating linter(s) failed: <lanes>`, or a success line
  counting every lane. One table (`lintLaneFlags`) now drives lane selection
  for all four drivers, and a lane a driver has no runner for is an error, not
  a silent fall-through to the full sweep. `--exported-vars` beside
  `--contract` runs the shared check once.
- **`forge generate` vendors `forge/v1/forge.proto` on demand, and refuses a Go
  buf template that would generate it.** Only `forge project new` wrote
  `proto/forge/v1/forge.proto`, so a project that adopted forge in place (or
  whose only forge-annotated protos are config protos) failed `forge generate`
  on the import until the file was copied in by hand. Generate now vendors it
  the way it vendors `buf/validate/validate.proto`: the first run whose protos
  import it and that finds no copy writes forge's own (with the `forgepb`
  `go_package`); an existing copy is never rewritten, so `forge lint
--vendored-protos` stays the drift guard. Because a project's own Go template
  may target all of `proto/`, generate now refuses one whose `protoc-gen-go`
  would compile a vendored proto — a second Go registration of a descriptor
  `forgepb` (or protovalidate) already registers panics every binary at init —
  naming the file and the `inputs: exclude_paths` lines to add (a module that
  owns the file's `go_package`, i.e. forge itself, is exempt). The
  `--buf-plugins=remote` rewrite of `buf.gen.yaml` dropped that exclusion and
  did exactly this; it now keeps it, and the three Go templates forge writes
  share one `inputs:` block.
- **`forge db squash` writes a migration, not a psql script.** It kept
  pg_dump's session prelude, so the baseline ran
  `SELECT pg_catalog.set_config('search_path', '', false)` on the migrator's
  session: the next migration in the same run failed with `relation "x" does
not exist`, and `AutoMigrate` handed the connection back to the app's pool
  with an empty `search_path`. Its line filter for psql meta-commands also
  deleted any line beginning with `\` inside a function body or an inserted
  value. squash now reads the dump the way postgres lexes it (quotes, `E''`
  strings, quoted identifiers, dollar-quoted bodies, nested comments) and drops
  only top-level `SET` / `set_config` statements and every psql meta-command
  (`\restrict`, `\unrestrict`, …); every other statement is kept byte for byte.
  A baseline that defines functions or procedures opens with
  `set_config('check_function_bodies', 'false', true)`: pg_dump writes
  functions before the tables a `LANGUAGE sql` body names, and the setting is
  transaction-local, so it ends with the baseline. The `db/deploy-migrations`
  skill's hand-cut (goose) recipe gains the same line.
- **BREAKING: a config file's path variable is the binary's own, not
  `FORGE_CONFIG`.** Every forge-built binary read `FORGE_CONFIG`, so a shell that
  set it for one forge app made every other forge binary started from that
  shell load that app's file and die at startup (`FORGE_CONFIG=/x.yaml reliant
server worker` → `config file "/x.yaml" does not exist`), including a daemon
  a desktop app launches with the login environment. The variable is now
  `<BINARY>_CONFIG`, derived from `(forge.v1.binary_config).binary`
  (`RELIANT_CONFIG`, `IDP_PROVISION_ONESHOT_CONFIG`) and shared by a binary's
  root and command configs; the project-global `AppConfig`, bound to no binary,
  reads `<PROJECT>_CONFIG` from forge.yaml's `name` (the generated
  `config.ConfigPathEnv`). Set to `""`, it is unset, like every variable the
  loader reads. `FORGE_CONFIG` is read by nothing, and
  `config.ConfigPathEnv` is removed from `forge/pkg/config`. The variable is
  recorded on the `--config` flag, so `--help` names it, and a missing file now
  says where its path came from (`(from env RELIANT_CONFIG) ... point
RELIANT_CONFIG at a readable file, or unset it`). New in `pkg/config`:
  `ConfigPathEnvFor(binary)`, and `RegisterConfigFlag(cmd, env)`, which
  registers `--config` with a chosen variable — called on a binary's root
  before the generated `Register*Flags`, it is the override. `forge generate`
  refuses a config field whose `env_var` or alias is its process's path
  variable. A
  project that set `FORGE_CONFIG` sets the binary's variable instead, and
  regenerates `pkg/config/config_gen.go`.
- **A root config and a command config share one `--config` file.** A process
  that loads both (`reliant server api` reads `ReliantConfig` and
  `ApiServerConfig`) decoded the file into each, each rejected the other's keys
  as unknown, and so no non-empty file could load: every one failed one of the
  two. The file is now flat across the configs the command loads, like its
  flags and env vars, and each takes its own top-level keys. Those configs are
  the ones registered on the command path through the cobra-level
  `Register*` functions (recorded on the command), so a key none of them
  declares is still a loud error, now naming them all (`unknown key
"jwks_ulr": no config this command loads has such a field (ApiServerConfig,
ReliantConfig)`), and nested keys stay strict. One file serves one process:
  `server worker` refuses a key only `server api` reads. A key two loaded
  configs both declare is refused at load, and `forge generate` refuses a root
  and a command config of one binary that share a top-level field name, as it
  already refused a shared env var or flag.
- **A config file's list replaces the default; it no longer appends to it.**
  The file layer `proto.Merge`d the file onto the defaults, and Merge appends a
  repeated field and unions a map, so `allowed_registries: [quay.io]` over the
  default `docker.io,ghcr.io` loaded all three — against the rule that every
  layer replaces a whole list, and silently widening a list a deployment meant
  to narrow. Merge also copied only populated fields and recursed into every
  message, so a value equal to proto3's zero was dropped (`port: 0`,
  `enabled: false`, `[]` kept the default) and `timeout: 0.5s` over a 30s
  default loaded 30.5s. The file now sets exactly the fields it names: a list
  or map replaces the default (`[]` clears it, as `--allowed-registries=` does;
  an empty env var is unset and clears nothing), a named
  zero value overrides, a Duration is replaced whole, a nested config block
  takes only the leaves it names, and `null` sets nothing. Fields the file does
  not name keep their defaults, as before.

- **`forge env deploy <env> --dry-run` now predicts the real run's release
  decision, and a nested git checkout is no longer "dirt".** A bare
  `forge env deploy prod` cut and built a whole new release because an
  untracked nested worktree made the checkout dirty, and the preceding
  `--dry-run` said nothing about releases. `--dry-run` now prints the line the
  real run prints (`would reuse release <v>` / `would cut new release <v>
because …`, with the dirty paths named) from the same function, and `--json`
  carries it as `release_decision`. An untracked directory with its own `.git`
  is excluded from the dirty verdict and from the provenance tree hash
  (`release.InspectCheckout`); the decision line lists the ones it ignored.
  A dirty cut on a shared env (hosted, or a non-local cluster) now asks before
  it builds, and refuses with exit 5 and no build when there is no terminal;
  `--yes` accepts it.
- **Traces, logs and profiles now line up.** Found in a live end-to-end run:
  - The scaffolded `serve.go` put the `otelconnect` interceptor in
    `observe.Deps.Extras`, which `observe.Chain` appends AFTER auth, so a call
    auth rejected had no span, its `rpc failed` line and audit event carried no
    `trace_id`, and profile samples had no span to link to. `observe.Deps` has a
    new `Trace` field that `Chain` places outermost, followed by the pprof
    labels naming its span; the scaffold uses it. `serve.go` is scaffold-once:
    see `migrations/v0.1.45` section 5 for the one-line edit.
  - `@reliantlabs/forge-web-runtime`: the first RPCs a page makes, which fire
    before the lazy HyperDX SDK has patched `fetch`, carried a synthesized
    `traceparent` no browser span matched, so the API span's parent id joined to
    nothing. The trace interceptor now waits (at most 2s) for telemetry startup.
  - `forge env up` on a project with no commits yet no longer fails with `git
tag resolution: exit status 128`; it tags images `uncommitted`.
  - `forge env up` no longer needs `air` on PATH: it installs its pinned air
    (v1.67.4) into forge's tool cache, and runs the API under `go-run` with a
    one-line notice if it cannot.
- **`forge db migrate` runs forge's migrator, not the `migrate` CLI.** `up`,
  `status`, `version` and `force` shelled out to golang-migrate's CLI, which
  skipped everything `pkg/migratekit` adds: the run lock shared with
  `AutoMigrate`, the applied record and its mismatch/missing refusals,
  `-- forge:adopts` goose adoption (the CLI ran the baseline's SQL over goose's
  schema and left the database dirty), and the schema-ahead verdict. A version
  the CLI applied also got no record row, so a server's next `AutoMigrate`
  refused it as missing. All four now open `migratekit` over the project's
  migrations directory (same `--dsn` / `DATABASE_URL` / `--dir`), and so do
  `forge db reset` and `forge db squash`; nothing needs `migrate` on PATH any
  more. `up` prints what the scaffolded `<binary> db migrate up` prints
  (`ADOPTED …`, `no pending migrations`, `migrations applied: A -> B`) and an
  empty directory is "nothing to apply"; `status` reports version, latest and
  pending. `migratekit` keeps its record in step with the dirty-schema
  recovery: a run that fails records the versions it applied before the
  failure, `Migrator.Force` records the versions it asserts (and drops rows
  above them), and `Up` on a dirty schema returns golang-migrate's dirty error
  instead of reporting the half-applied version missing. Before, repair →
  `force` → `up` was refused with every version of the failed run "missing".
  A database the old CLI migrated after `AutoMigrate` created the record still
  refuses those versions; the error prints the `INSERT` that records them.
- **`forge lint --contract` no longer judges test files as a package's API.**
  Every driver hands `_test.go` files to the contract analyzers (`go vet` and
  multichecker analyze test variants; the in-process lane loads with
  `Tests: true`), and the analyzers read them as API. In reliant,
  `internal/mcpserver/http_test.go`'s `memStore` fake satisfied the package's
  `BindingStore` contract, and the single-seam rule flagged every other
  exported method on it: 14 findings, none of them about the API. Methods and
  vars declared in test files are now skipped by the single-seam,
  require-contract and exported-vars rules, so a package whose only exported
  methods are a test fake needs no `contract.go`, and a fake `Name`/`Start` in
  a test no longer buys an ordinary `internal/workers/` package the worker
  exemption. The skip is per file: the package's own source stays strict. No
  rule read tests on purpose; the exclusion gate already skipped them.

### Added

- **Lean scaffolded CI over a local gate: `task ci`, `task ci:checks`,
  `forge ci gate`.** The scaffolded `ci.yml` was nine jobs (lint, test, build,
  verify-generated, deployability, vuln-scan, license-check, docker-build, e2e),
  each repeating checkout, setup-go, cache restore and the forge install, and
  GitHub rounds every job up to the whole minute: the one project generated from
  it cost about $850/month at its October pace. It is now `changes`, `checks`
  (one setup, then `task ci:checks`), `test` (`task test`) and `integration-test`
  (`task test:integration`, PRs only, only when the repo has `//go:build
  integration` files), plus `docker-build` and `e2e` where the project has them;
  `changes` skips the heavy jobs for a docs-only change, every job (in every
  scaffolded workflow) has `timeout-minutes`, and the workflow is a thin wrapper:
  run `task ci` locally for the same verdict. `forge ci gate [checks|test|
  integration|all]` is the runner, and forge owns the check list and the tool
  pins (golangci-lint, buf, govulncheck, go-licenses, built into forge's tool
  cache; a copy on PATH at another version, or a golangci-lint built with an
  older Go than `go.mod`, is not used). Every check runs even after one fails,
  a summary table is printed and appended to `$GITHUB_STEP_SUMMARY`, and the
exit status is non-zero if anything failed. A check that could not run is
`SKIPPED`, not a pass, and fails the gate unless named in `CI_SKIP`. Add your
own checks under `ci.gate.extra_checks`in forge.yaml.`ci.yml`is scaffold-once,
so existing projects adopt this through the new section in`migrations/v0.1.45` (`forge project upgrade list`).
- **A database that is not up yet no longer crash-loops the server.** The
  startup connect (`serverkit.AutoMigrateContext`, the scaffolded
  `openDatabase` via `serverkit.OpenDB`) retries with capped exponential
  backoff and jitter for `DB_CONNECT_TIMEOUT` (new config field, default 5m)
  instead of failing on the first refused dial, logging one WARN per 15s
  bucket with the error class and INFO with elapsed time on success. The
  scaffolded `Serve` binds the listener first (`serverkit.StartBootProbes`):
  `/healthz` passes and `/readyz` returns 503 naming `database` until `Run`
  takes the address over. Only a database still unreachable at the deadline
  exits non-zero (`*serverkit.ConnectTimeoutError`). The rendered default
  startup probe is now `/readyz` with a 360s window, covering the 5m wait;
  a prod rollout restarted three services over a PgBouncer that was seconds
  late. `serverkit.AutoMigrate` keeps its signature (no boot probes).
  Existing projects own `serve.go`/`providers.go`: see the PR for the edits.
- **A typed observability destination: `forge.Observability` on the Bundle.**
  An environment declares where its telemetry goes (an OTLP endpoint, the
  protocol, and a `headers_secret` reference, never an inline secret), which
  signals ship, trace sampling (errors always kept) and retention hints. Forge
  renders the plumbing per runtime; apps keep exporting OTLP to a local endpoint
  and never learn the backend. Unset means telemetry off with no dangling
  endpoint. Host and compose: the local ClickStack stores by default, and a
  destination swaps it for an `otel-forwarder` service rendered from the
  declaration. Clusters (k3d or any `ClusterTarget`): a node-agent DaemonSet
  (OTLP receiver, filelog of container stdout, kubeletstats, k8sattributes for
  enrichment only, the pinned contrib 0.162 image) with a node-local Service
  (`internalTrafficPolicy: Local`); client-supplied `k8s.*`/`reliant.*` resource
  attributes are deleted before enrichment. `forge.OnHosted`:
  forge renders no agent and emits only the `OTEL_*` contract. A headers secret
  nothing supplies, a workload that also exports OTLP logs, and a host process
  with no local port are refused at render. `forge.OTEL_ENV` no longer points at
  `otel-collector.observability:4317`, which nothing deployed. See
  `observability/destination` and `migrations/v0.1.45`.
- **Changed:** the OTLP endpoint/protocol pair `forge env up` gave host
  processes is now derived by that declaration (`ObservabilityEntity`) instead
  of the removed `clickstackHostOTLPEnv`.
- **`forge.yaml` `generate.hooks`: run your own commands as part of `forge generate`.**
  The escape hatch for a generator forge does not own, such as a query
  generator kept while porting onto the ORM, a project codegen tool, or a docs
  dump. Each hook is an argv (`command: [sqlc, generate]`; no shell, so
  quoting is the same on every platform), an optional project-relative `dir:`,
  and the `writes:` paths it produces. Hooks run in declaration order after
  forge's codegen and before `go mod tidy` and the validation build, so their
  output is tidied and compiled in the same run. A non-zero exit fails the
  generate. Declared `writes:` join the rollback journal, so a failed generate
  restores them like forge's own output. See `architecture`, "The Generate
  Pipeline".
- **migratekit adopts a database goose already migrated.** A project leaving
  goose squashes goose's history into one first-migration baseline and lists
  every goose version in the baseline's header:
  `-- forge:adopts goose <version> …` (the line repeats to wrap).
- **Continuous profiles go to ClickHouse, pushed by the app (owner decision: push by default, no Pyroscope).**
  `pkg/observe` now profiles every process that has an OTLP endpoint: a 10s CPU
  profile plus heap and goroutine snapshots about once a minute (jittered),
  converted to OTLP profiles and POSTed to `OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT`
  (a full URL; unset, the base endpoint gains `/v1development/profiles`). On by
  default whenever an endpoint is set; off with `OTEL_PROFILES_EXPORTER=none` or
  `observe.Config{ProfilesDisabled: true}`. The loop never blocks or crashes the
  app: own goroutine, panics recovered, a TCP probe before each cycle, exponential
  backoff to 15 min, and the CPU profile is skipped when another is running.
  Measured: ~90 ms CPU and 1.5 MB per conversion (~0.15% of a core), ~8.5 MB/day
  of ClickHouse per instance. The redundant `samples` sample type is dropped, a
  profile ID is set, and the `trace_id`/`span_id` pprof labels that
  `TracingInterceptor` (and the new `observe.ProfileLabelsMiddleware`) put on
  request goroutines become OTLP links. **Pre-release pin:** the profiles format
  is `v1development`, so contrib `pkg/translator/pprof` and collector
  `pdata/pprofile` are pinned together at **v0.162.0**, the same version as the
  local `otel-profiles` collector; bump all three in one change.
- **`otel-profiles` in the scaffolded `docker-compose.yml`** fills the seam left
  for it: stock contrib collector 0.162.0 (digest-pinned) with
  `--feature-gates=+service.profilesSupport`, OTLP in, the `clickhouseexporter`
  out to the ClickStack ClickHouse with its existing credentials. Compose
  services get `OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT`; for host processes the
  typed declaration derives it (`forge.Observability.profiles_port`, the port
  `dev/main.k` publishes) unless you chose an OTLP endpoint, protocol or
  profiles endpoint yourself. **Profiles are on by default with the local
  ClickStack and governed by `signals.profiles` elsewhere:** `signals.profiles`
  is now unset by default, meaning on when there is no destination and a
  `profiles_port`, off otherwise (rendered as `OTEL_PROFILES_EXPORTER=none`);
  an explicit value wins. `True` with the local ClickStack but no
  `profiles_port` is refused at render. The compose `otel-forwarder` gained the
  `service.profilesSupport` gate so `signals.profiles = True` with a
  destination starts. See `observability/profiles` and `migrations/v0.1.45`.
  `deploy/observability/profiles-schema.sql` (yours) creates the raw
  `otel_profiles` table (72h TTL) and two materialized views, deduplicated
  stacks and per-minute totals (30d TTL), idempotently, on every up.
- **`forge observe profile`** exports an aggregated profile (a service over a
  time range, or one trace) as pprof (`go tool pprof -http`), speedscope JSON, or
  collapsed stacks; `--sql` prints the query. The query and the writers are in
  the new `pkg/profilequery` for reuse by other tools.

  On the normal `db migrate up` and `AutoMigrate` paths, a database with a
  `goose_db_version` table and nothing yet in `schema_migrations` is checked
  against that list:

  - **Exactly the listed set:** the baseline is recorded as applied without
    running its SQL, and later migrations apply on the same run.
    `Result.Adopted` is set and `AutoMigrate` logs `ADOPTED`.
  - **A listed version missing, or a version applied that the list lacks:**
    the run refuses with `*migratekit.AdoptionError`. It names the versions,
    says how to fix each case, and changes nothing.

  The check is set membership, never "the newest version matches", because
  goose applies out of order. A version counts as applied when its latest row
  says so, which is how goose reads its own table. goose's table is never
  modified, so the last goose release remains a safe rollback target. After
  adoption, goose's table is never read again.

  A malformed declaration, or one on any migration but the first, is refused
  at `Open` and at boot on every database. `forge lint` reports the same at
  review time as the new `invalid-adopts-directive` rule. The scaffolded
  `db migrate up` prints the adoption. `forge project migrate import --from
goose` now warns that renumbering re-runs history on a database goose
  already migrated. See the `db/deploy-migrations` skill, "Adopting a database
  another tool migrated".

- **Adopt forge one service at a time: `// forge:unmanaged`.** A full-line
  comment above `service X {` takes that service out of forge's service model.
  buf still generates its code and it stays in the descriptor, but `forge
generate` scaffolds nothing service-scoped for it: no `internal/handlers/<svc>/`
  or rpc stubs, no mocks, CRUD wiring or entities, no mount, compose entry or
  subcommand, no frontend hooks or pages. `forge scaffold` skips it and refuses
  `scaffold service|rpc|entity --from-proto` on it, `forge lint`'s
  list-filter-optional rule and `forge project shapes` treat its rpcs as not
  forge's, and `forge project audit` lists every one under
  `unmanaged_services`. Its rpcs' `auth_required` still projects into
  `pkg/middleware/procedures_gen.go`, because whether an rpc is public is part
  of the contract whoever serves it. Before this, the first `generate` on an
  existing server stubbed every rpc of every service at once — 307 stubs on
  reliant. Delete the line to adopt the service. A near-miss spelling, or the
  marker on an rpc or message, fails generate. `codegen.ParseServicesFromProtos`
  now returns managed services only; `ParseDeclaredServices` returns all.
  See the `migration` skill, "Adopting in place".
- **`//forge:not-a-component: <why>` takes a contract package out of the server
  composition and keeps its contract.** Every `internal/<pkg>/contract.go`
  package used to be treated as a server component, so a package only the CLI
  or a daemon uses (a browser login flow whose `Deps` are a credentials
  directory, an `OpenURL func(string) error` and an HTTP doer) failed
  `forge generate` with `Cliauth.Deps.Dirs (credentials.Dirs) has no provider`
  until its collaborators were wired onto the server's `Infra`. A marked
  package keeps `contract.go`, the contract lint, `mock_gen.go` and (with
  `// forge:constructor`) its decorator; `compose.go` never constructs it,
  never resolves its `Deps` and gives it no `Infra` field, and it gets no
  `NewTest<Pkg>` factory. A server component that requires it by type fails
  generate naming the marker, and `forge lint` refuses a bare marker
  (`forge-not-a-component-reason`) or one where composition does not read it
  (`forge-not-a-component-scope`: a handler, worker or operator, beside
  `//forge:external-component`, or a package with no `contract.go`). When an
  unmarked package's `Deps` have no provider and no service, worker or
  operator reaches it, the missing-provider error names the marker as a
  second remedy. `forge project audit --json` reports `uncomposed_packages`
  and `forge project graph` a per-package `composition`. See the `contracts`
  skill, "Packages the server does not have".
- **Config scoped to one subcommand of a binary:
  `(forge.v1.binary_config).command`.** A binary whose subcommands run as
  separate processes (`reliant server api`, `reliant server worker`) can now
  give each process its own config instead of one surface every role links:
  `{binary: "reliant", command: ["server", "api"]}`. The binding is keyed on
  `(binary, command)`. The binary's root config (no `command`) stays the
  binary-wide surface every process loads; a command config's leaves register
  as LOCAL flags on that subcommand (`Register<Msg>Flags(cmd)`), and
  `Load<Msg>(cmd)` refuses to load from any other command
  (`config.CheckCommandPath`), which is where a mistyped path surfaces: a
  cobra tree is assembled at runtime, so generate checks the path's shape
  only. Generate refuses two messages for one `(binary, command)`, a
  malformed path, and a root and command config of one binary declaring the
  same env var or flag. In KCL each config keeps its own `<msg>EnvMap`, and a
  binary with command configs also gets a `<Bin>Process` bundle and
  `config_gen.<bin>ProcessEnvMap(cfgs, argv, config_secrets)`, which projects
  one workload's env from its own `command + args` (root config plus the
  longest matching command config) and routes each `config_secrets` name to
  the config that declares it. New projects' `config.k` scaffolds the bundle as
  `<bin>_process`. See the `binaries` skill, "One binary, several processes".
- **Generated HyperDX dashboards.** `forge generate` now derives dashboards
  from the project's services and writes them to
  `deploy/observability/dashboards/*_gen.json`, where the local ClickStack
  already loads them: an overview (request rate, error rate, p50/p95/p99 per
  service, top errors, error logs), one dashboard per service (a
  requests/error-rate/latency row for every rpc, DB calls, Go runtime, logs), a
  frontend dashboard (JS errors, sessions, web vitals, page load, browser API
  calls) and a saved error-log search per service. Everything filters on
  `service.name` and `deployment.environment.name`. A hand-written
  `<name>.json` beside a `<name>_gen.json` takes it over; forge never writes a
  file there that does not end in `_gen.json`. The model is a renderer-neutral
  `Spec` (`internal/obsdash`, also written to
  `deploy/observability/dashboard-spec_gen.json`) that HyperDX JSON is rendered
  from, so another renderer can draw the same dashboards. Tiles name their
  source (`Logs`/`Traces`/`Metrics`); the compose entrypoint binds the names to
  this database's source ids. Alert definitions (error rate, p99, no data) are recorded in the spec's
  `alerts` as `pending`, because HyperDX 2.40 never evaluates an
  alert declared in a provisioned file (hyperdx PR #3233). See skill
  `observability/dashboards`.
- **Frontend builds are told what they were built from.** Every frontend build
  forge runs gets `FORGE_SOURCE_REPO`, `FORGE_SOURCE_REF`, `FORGE_SOURCE_COMMIT`
  (the GitSource pin and the sha it resolved to) and `FORGE_RELEASE` (the
  deploy's bound release), always present and empty when unknown. A GitSource
  is fetched shallow with no tags, so a tag-pinned build could not name its own
  version and the release name never reached a build at all; reliant-web
  shipped "version unknown" to prod. A local source override reports no source.
  See `docs/cross-repo-sources.md`, "What the build is told about itself".
- **`forge storage gc` reclaims dangling images and anonymous Docker volumes.**
  Dangling images (untagged, used by no container, built over an hour ago) are
  removed by every pass, the automatic ones included. Anonymous volumes (the
  daemon's `com.docker.volume.anonymous` label AND a 64-hex name, plain `local`
  driver, mounted by no container, seen unused for `anonymous_volume_unused`,
  default 14 days) are removed only by an explicit `forge storage gc --apply`;
  automatic passes only record when each was first seen unused. Named volumes
  are never removed. Every removal goes through the pass's delete governor and
  the new `max_docker_removals_per_pass` cap (default 100), and a daemon error
  reporting EMFILE/EIO/ENOTCONN backs the pass off. `storage.Runner.ReapWorktrees`
  is renamed `Explicit`. See `docs/runbooks/storage-lifecycle.md`.
- **`pkg/observe` merges the standard `OTEL_RESOURCE_ATTRIBUTES` and
  `OTEL_SERVICE_NAME` into the OpenTelemetry resource** (`resource.WithFromEnv`).
  The deployment environment is the forge env name, delivered by the platform as
  `OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=<env>`; it is not derived
  from the CORS/HSTS `environment` mode field, which cannot tell staging from prod.

- **BREAKING: `pkg/observe` honours the standard OTLP environment.**
  `OTEL_EXPORTER_OTLP_{ENDPOINT,PROTOCOL,HEADERS}` and the per-signal variants
  now configure the exporters: `http/protobuf` is the default, `grpc` is
  supported, and headers reach the wire (previously gRPC only, no headers).
  The default protocol flips from gRPC to `http/protobuf`: an existing
  `:4317` endpoint with no `OTEL_EXPORTER_OTLP_PROTOCOL` must now set
  `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`.
  `OTEL_SERVICE_NAME` now overrides the compiled-in service name, and
  `OTEL_SDK_DISABLED=true` turns export off. `Config.OTLPEndpoint` still
  overrides the endpoint; with no endpoint nothing is exported.
- **The W3C TraceContext+Baggage propagator is always installed**, not only when
  an OTLP endpoint is set, so `traceparent` is honoured with export off.
- **Server logs carry `trace_id` and `span_id`** when the logging context holds a
  span (`observe.NewLogHandler`, used by `serverkit`).
- **Opt-in OTLP logs exporter** (`OTEL_LOGS_EXPORTER=otlp` / `Config.OTLPLogs`),
  off by default and mutually exclusive with stdout/file collection.
- **Go runtime metrics** are recorded when metrics export is on.

### Changed

- **`serverkit.RequireComplete` checks exactly the inventory's services.** It
  used to register each inventory service's whole proto FILE and demand every
  service in it, so a file declaring a forge-managed service beside an
  unmanaged one refused to boot a complete server. `serverkit.ProjectServiceFiles`,
  which built that file-scoped registry, is removed; `RequireMounted` with a
  descriptor source you build is unchanged.
- **BREAKING: a binary's root config registers every leaf as a persistent
  flag, and `Register<Msg>SharedFlags` is gone.** The generated
  `Register<Msg>Flags(cmd)` for a `(forge.v1.binary_config)` message with no
  `command` now registers all of its leaves, composed blocks included, as
  PERSISTENT flags on the command it is given (the binary's root), via the new
  `config.RegisterPersistentFlags`; it used to register only the message's own
  leaves, as LOCAL flags, beside a separate `Register<Msg>SharedFlags` for the
  composed blocks. That split modelled binaries as subcommands of one root,
  which `command` now expresses directly, and it left a root config's own
  flags invisible to the binary's subcommands. Replace
  `Register<Msg>SharedFlags(root)` + `Register<Msg>Flags(root)` with one
  `Register<Msg>Flags(root)`. `config.RegisterSharedFlags` /
  `RegisterOwnFlags` remain as library primitives.
- **ClickStack is the local observability stack, and it is ON by default.**
  `forge env up` now runs ONE container, HyperDX's no-auth _local mode_ image
  (ClickHouse, the HyperDX UI and an OTLP collector), in place of the opt-in
  Grafana LGTM stack. Host processes are handed
  `OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:<port>` and
  `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, as a pair and only when the shell,
  `config.k` and the workload's `env` set neither (a user's gRPC endpoint is
  never paired with our protocol), so traces and metrics reach HyperDX with
  nothing to register and no key to copy. Frontend dev servers get this
  project's collector base URL (the port compose actually published, so their
  `/_otel` proxy cannot reach another project's ClickStack) and the env name
  (`NEXT_PUBLIC_ENVIRONMENT` / `VITE_ENVIRONMENT`), which the browser reports as
  `deployment.environment.name`. The frontends' `frontend_<name>.log` console
  mirror is not tailed, because the HyperDX browser SDK already ships the same
  console and the two would store every line twice. The collector tails the per-service
  files `forge env up` already writes under `.forge/logs/<env>/`, parsing the
  JSON lines the runtime emits (`trace_id`/`span_id` become the log's trace
  link), and that is the one local log path. Every port is published on
  `127.0.0.1`; ClickHouse is not published, and its passwordless `default` user
  is replaced by a dev-only credential. State persists in named volumes. The
  HyperDX sources (Logs, Traces, Metrics) are pre-created, and HyperDX dashboard
  JSON dropped into `deploy/observability/dashboards/` is loaded within seconds
  by HyperDX's file provisioner. Turn it all off with `_observability = False` in
  `deploy/kcl/dev/main.k`. `forge env status` now reads traces, metrics (gauge,
  sum and histogram) and logs back from ClickHouse, by service name.

  Gone with it: the `lgtm` and `alloy` compose services,
  `deploy/alloy-config.alloy`, the generated Grafana dashboards under
  `deploy/observability/grafana/`, and the Grafana MCP example. Profiles are not
  collected locally yet. An existing project keeps its own `docker-compose.yml`
  and `deploy/kcl/dev/main.k` (both are yours); load `migrations/v0.1.44` to move
  it over.

- **Frontends ship the HyperDX browser SDK, initialised, with a same-origin
  `/_otel` route.** Next.js and Vite scaffolds now install
  `@hyperdx/browser` (exact-pinned `0.26.0`) and start it from the generated
  `src/lib/otel_gen.ts`. It captures uncaught errors, unhandled rejections,
  `console.*`, fetch/XHR spans, document load, long tasks and web vitals, and
  sends them as OTLP to `<basePath>/_otel/v1/{traces,logs}` on the page's own
  origin, so the bundle holds no collector address and no secret (the SDK's
  required `apiKey` is a public placeholder; a collector that enforces ingestion
  auth has its proxy strip or replace it). `next dev` rewrites and `vite`
  `server.proxy` forward `/_otel` to `OTEL_EXPORTER_OTLP_ENDPOINT` (default
  `http://127.0.0.1:4318`). A static export has no server, so a deployed
  environment routes `/_otel` at its ingress; the production default is
  therefore off until the frontend's `otel_endpoint` is set. `forge lint
--static-export` stays green because the rewrite is gated to development.
  Session replay is shipped but **off**, and when enabled
  (`NEXT_PUBLIC_OTEL_REPLAY` / `VITE_OTEL_REPLAY`) forces all text and inputs
  masked. Network bodies are not captured. `traceparent` on every RPC is
  unchanged and still always on. No Sentry SDK is scaffolded.

  **Breaking, web-runtime 0.4.0.** The `@reliantlabs/forge-web-runtime/otel`
  subpath and its eight `@opentelemetry/*` SDK peers are gone: HyperDX registers
  the page's one global tracer provider, and a second registration silently
  loses. `initClientTelemetry` is gone too; the new `/telemetry` subpath's
  `initBrowserTelemetry` replaces both. `reportException()` (barrel) records an
  error a React/Next boundary caught, and `RuntimeErrorBoundary`, `error.tsx`
  and `global-error.tsx` call it.

  **Browser and API spans share one trace.** The serve scaffold builds
  `otelconnect.NewInterceptor(otelconnect.WithTrustRemote())`. By default
  otelconnect only _links_ to an inbound `traceparent`, so a page and the API
  call it made were two traces even though the header arrived intact. Found by
  running the stack: ClickHouse held them under different trace ids. `serve.go`
  is scaffold-once, so an existing project adds the option by hand.

  **The browser reports `deployment.environment.name`**, the key the backend
  uses, so a browser span and a server span of one trace carry the same
  environment. The forge env name is rendered into the runtime config document
  (`config.js`) as `ENVIRONMENT` for every env forge writes it for, and
  `forge env up` also hands it to the dev server (`NEXT_PUBLIC_ENVIRONMENT` /
  `VITE_ENVIRONMENT`). A declared `config.environment` wins. A hosted frontend's
  `config.js` is written by the control plane from its `runtime_config`, which
  this change leaves as published; declare `ENVIRONMENT` there when you turn
  hosted browser telemetry on.

  **Existing projects** (load `migrations/v0.1.44`): `forge generate` rewrites
  `src/lib/otel_gen.ts`, raises the web-runtime range to `^0.4.0` and adds
  `@hyperdx/browser` at the runtime's pin to each web frontend's
  `package.json`. A scaffold-once `providers.tsx` (Next.js) or `main.tsx`
  (Vite) that still calls `initClientTelemetry()` must drop that call and its
  import, and `next.config.ts` / `vite.config.ts` need the `/_otel` proxy block.
  **Requires `@reliantlabs/forge-web-runtime@0.4.0` on npm** before a released
  forge scaffolds or regenerates a frontend.

### Fixed

- **`db migrate up` and `AutoMigrate` no longer race each other.** Both rely
  on golang-migrate's advisory lock, but golang-migrate names the database
  differently for each: the URL path (`/app`) for the DSN that `Open` uses,
  and `current_database()` (`app`) for the pool connection `AutoMigrate`
  wraps. That gives one database two lock keys, so a deploy initContainer or
  the dev loop's migrate job never excluded a server booting with
  `AUTO_MIGRATE`. Six replicas racing one database, half through each entry
  point, failed every time:

  - two replicas ran the same migration, and the second failed on the
    table the first had just created;
  - replicas met `Dirty database version` while a peer was mid-apply;
  - a replica refused a migration a peer had just recorded;
  - a replica lost the race to create `schema_migrations`
    (`duplicate key … pg_type_typname_nsp_index`).

  migratekit now takes its own advisory lock, under one key both entry points
  share. It is held from before golang-migrate's driver is built (building it
  creates `schema_migrations`) through the last bookkeeping write. `Open`
  holds it only while building the driver. `Up` and `AutoMigrate` hold it for
  the whole run. `AutoMigrate` takes it on the connection it already borrows,
  so a boot needs no extra pool slot. If the unlock fails, that connection is
  discarded rather than returned to the pool still holding the lock. Covered
  by `TestReplicasRacingThroughBothEntryPointsTakeTurns`.

- **`forge project new --in-place` adopts the forge pin the module's go.mod
  already has.** Scaffolding reliant in place (its go.mod requires forge
  `v0.1.44-0.20261007193810-e75c74afb46e`) wrote the RUNNING binary's
  `v0.1.44-0.20261010080729-…` into `forge.yaml` `forge_version`, so the two
  version facts disagreed from birth. The scaffold now records exactly what
  generate's reconcile step follows (go.mod's forge require), and a `--force`
  that replaces go.mod requires the same version again. It never moves the
  pin: when this forge cannot generate into the forge the module resolves
  (the same verdicts as generate's compatibility check: an unreleased build
  without `--link-forge`, a pin older than a released binary, or a binary
  older than the pin), `project new` refuses before writing anything and
  names the pinned forge to run (`go run …/cmd/forge@<pin> …`). The
  unreleased-build guard no longer refuses an in-place module that already
  resolves forge to the very build running. `generate`'s reconcile step also
  stopped following the require of a REPLACED forge: `goModForgeRequire`
  parsed go.mod in lax mode, which drops `replace`, so that branch never fired.
- **`forge generate` resolves every `local:` buf plugin before buf runs.**
  reliant's kept `buf.gen.yaml` runs
  `local: [node, tools/protoc-gen-es/node_modules/protoc-gen-es-v2.15.0/bin/protoc-gen-es]`;
  with `npm ci` never run there, generate died with a Node `MODULE_NOT_FOUND`
  stack trace from inside buf. Right before each `buf generate` (the root
  `buf.gen.yaml`, each frontend template, the workspace template), with the
  PATH buf will get, forge now checks each v2 `local:` plugin (string or argv
  list): argv[0] on PATH or as a project-relative executable, `go tool <name>`
  against go.mod's (or the go.work's) tool directives, and `node <script>`'s
  script. A miss fails with the template, the plugin, what is missing and the
  fix (``run `npm ci` in tools/protoc-gen-es``, `go get -tool …`,
  `forge tools install`, `chmod +x …`). Forms decided only at run time
  (`go run pkg@v`, npx, interpreter flags) have just their argv[0] checked.
- **`forge generate` refuses a `(forge.v1.binary_config)` binding to a binary
  that does not exist.** `binary` accepted any string: `{binary: "x server
api"}` (a subcommand line where a binary name belongs) and
  `{binary: "nonexistent"}` both generated a loader, a KCL schema and an env
  projection that no process ever loaded, contradicting forge.proto's own
  contract. A binding must now name a `cmd/<name>` directory; the refusal names
  every offender as `proto/<file>:<line>`, lists the project's binaries, and
  suggests the `cmd/` spelling when the name differs only by `-`/`_`. Two
  messages bound to the same binary (and `command`) are refused too: the KCL
  projection looked the binary up rather than the message, so both got the
  first message's fields.
- **`forge lint` no longer reports a golangci-lint that cannot read the
  toolchain's export data as a typecheck failure.** On control-plane the
  gating lane failed with one `typecheck` error, `could not import time (…
export data version 5 is greater than maximum supported version 4)`, in a
  file nobody had touched. The golangci-lint on PATH (v2.13.1, built with
  go1.27.0) passed the language check, but the module builds with go1.27.2,
  whose compiler writes export data version 5. golangci-lint decodes export
  data with the golang.org/x/tools it was built against, not with the Go it
  was built with: v2.13.1 (x/tools v0.49.0) reads up to 4 even when built with
  go1.27.2, and v2.14.0 (x/tools v0.50.0) reads 5 even when built with
  go1.27.0. Comparing build Go versions therefore cannot decide it, so forge
  reads golangci-lint's own report. Every lane that runs golangci-lint now
  captures the output. When the output says the binary cannot decode the
  export data, forge discards that run, prints one line naming the binary,
  its build, the toolchain and both format versions, and runs again with its
  pinned golangci-lint built with the module's toolchain. That build is the
  same cached one per (pin, toolchain) as the language-version fallback, and
  the rest of the lint run uses it directly. If forge cannot build it, or the
  pin cannot read the export data either, the lane fails with one runbook
  finding naming both binaries and the toolchain, plus the install command.
  It never reports the bogus typecheck error. Plain `forge lint` now prints
  golangci-lint's output when the run finishes, not as it streams.

- **Boot-time migration gives its database connection back.**
  `migratekit.AutoMigrate` built golang-migrate's driver with `WithInstance`.
  That driver checks out a dedicated `*sql.Conn` and releases it only in
  `Close`, and its `Close` also closes the caller's pool, so the migrator was
  never closed. Every server that booted with auto-migrate kept one
  `MaxOpenConns` slot checked out and one idle server session open for its
  whole life. The migrated database could not be used as a
  `CREATE DATABASE ... TEMPLATE` even after the pool closed, which is how
  control-plane's integration harness found it. The migrator now runs on a
  connection `AutoMigrate` owns (`WithConnection`) and closes it on return.

- **`forge generate` no longer refuses over a nested git checkout.** The
  Tier-1 stomp guard walked into any worktree or repository parked inside the
  project directory (control-plane's `control-plane-hosted-identity/`) and
  reported that checkout's files as hand-edited Tier-1 files of this project,
  with remedies (`--force`, `disown`) that would have rewritten or frozen the
  other checkout. The marker scan now skips every subdirectory carrying a
  `.git` file or directory.

- **Generated frontend mocks no longer read the clock.** control-plane's
  "Verify Generated Code" check went red every day at 00:00Z with no source
  change. `forge generate` built the `src/mocks/*_gen.ts` fixtures from the
  runtime seed config, which `forge db seed` anchors at the current UTC day so
  a seeded dev database reads as current. Every seeded timestamp in the
  committed fixtures therefore moved with the calendar. The fixtures now anchor
  at seedplan's fixed reference instant (the four weeks before 2024-01-29), the
  same anchor entity factories and CRUD test fixtures already used, so they are
  a pure function of the migrations, `vocab.yaml`, salt and row counts. Ids,
  strings, enums and foreign keys still agree with the seeded database; only
  timestamps differ. The guarantee is enforced twice. In the CLI,
  `seedConfigFromProject` is clock-free and only `liveSeedConfig` (used by
  `forge db seed apply|status|reset`, `forge db reset` and `forge env up`'s
  auto-seed) reads the day. In codegen, the mock seed projection ignores
  `Config.Now` whatever its caller passes. A project's next `forge generate`
  rewrites the fixtures' timestamps once, to 2024-01 dates, and then leaves
  them alone. An audit of every other `time.Now()` reachable from generate
  turned up nothing else that lands in committed output: the remaining calls
  are generated code calling `time.Now()` at runtime, or one-time event records
  (disown, scaffold and migration-applied times) written when the event
  happens.

- **A contract mock imports every package its foreign interfaces use.**
  `forge generate` rewrote control-plane's `internal/svcdaemon/mock_gen.go`
  without `github.com/nats-io/nats.go`, so the package stopped building on every
  run. The mock generator named an unaliased import by guessing its path's last
  element (`nats.go`), so the `*nats.Msg` a foreign Deps interface returned never
  matched it. It also read foreign interfaces from syntax alone, which skipped
  embedded interfaces, and matched an alias by substring (`v1` inside
  `controlplanev1.Plan`), which left an unused import. A new
  `contract.Resolver` asks the toolchain instead: unaliased imports get their
  declared package name, and each foreign interface is rendered from `go/types`,
  embedded methods included, with every package its signatures reference
  imported under its real path. Declared methods keep their source order, so
  existing mocks are not reshuffled. One resolver serves the whole run (two
  `go/packages` loads, not two per contract). A package the toolchain cannot
  load falls back to the syntax path, so mock generation never blocks codegen.

- **`FORGE_LEDGER=machine` keeps a run off a declared control plane.**
  control-plane's hermetic `scripts/test-kata-prepull.sh` set
  `FORGE_LEDGER_HOME` to a temp dir and ran `forge ledger import --apply`.
  prod declares `forge.ControlPlane`, so the import planned against, and tried
  to write to, the production ledger. Only a plan conflict stopped it.
  `FORGE_LEDGER_HOME` is a location, not a selector, and it stays one: users
  set it to move the ledger off a network filesystem, and making it select
  would send their hosted envs to an empty machine ledger. The new
  `FORGE_LEDGER=machine` sends every env to this machine's ledger. No
  endpoint, credential or client for the declared control plane is resolved.
  Every forge process that inherits the variable is covered, so a script's
  render, import and status calls are all covered. An unrecognised value is
  refused, never read as the default. The override prints one stderr line per
  env, and `forge ledger where <env> --json` reports
  `"override": "FORGE_LEDGER=machine"` so a script can assert it.
  `forge ledger import` now labels each target HOSTED or machine. Before a
  hosted write it prints `Writing to the HOSTED ledger at <url> …`.

- **A failed `forge generate` restores external-tool output too, and claims a
  clean tree only after checking it.** A refused generate in control-plane
  printed "your tree is back to its pre-run state" over 32 files buf had left
  modified: the rollback journal recorded only forge's own writers, on the
  theory that tool output is deterministic from the inputs. It is not. A local
  plugin rendered a different version header, and buf regenerated stubs from
  another agent's uncommitted proto. `GenStep.Writes` now declares what each
  external tool may write (buf's `out:` dirs, the descriptor, `openapi/`,
  frontend TS stubs, sqlc's outputs, `gen/go.mod`, every `go mod tidy`, the KCL
  image-registry migration). Those paths join the journal before the tool runs.
  Directories are captured recursively, and a file or directory the tool
  creates is removed on restore. Before the first step the pipeline also
  fingerprints the work tree (`git ls-files`, or a walk outside git; size,
  mtime, mode and sha256). After the revert the report makes one of three
  claims: back to its pre-run state (checked), NOT fully back (followed by
  every file that still differs), or could not be checked (run
  `git status`). Unjournaled changes are reported, never reverted: in a shared
  checkout they may be another agent's. The check found forge writers that
  wrote raw and are journaled now: the scaffold-once `contract_test.go` (a
  refused run left it behind and kept its birth record, so it was never
  scaffolded again), the `observe_chain.go` seam, and the frontend import
  repoint, tsconfig/vite edits, nav and hooks writes.

- **`forge env render` no longer asks the control plane for an organization
  that nothing uses.** The off-base check for hosted images composed the push
  base first, which costs an organization lookup with the stored credential,
  and only then looked for hosted images. An env that declares a control plane but
  hosts nothing (control-plane's prod) called its control plane on every
  render. The base is now resolved only when there is a hosted image to judge.

- **`forge lint`'s contract lane honours `GOWORK`.** It decided workspace mode
  by looking for a `go.work` up the tree, so a workspace named by
  `GOWORK=/tmp/x/go.work` (how a pinned project is bridged to an unreleased
  forge checkout without writing into it) looked like none. The lane then
  added `GOFLAGS=-mod=mod` and every package load failed with "-mod may only
  be set to readonly or vendor when in workspace mode". `GOWORK` is consulted
  first, as cmd/go does: a file means workspace mode, `off` means none, unset
  falls back to the search.

- **`forge lint` runs a golangci-lint that can analyze the module.**
  golangci-lint refuses a module whose `go` directive is newer than the Go it
  was built with ("the Go language version (go1.26) used to build
  golangci-lint is lower than the targeted Go version (1.27)", exit 3). The
  workspace image shipped a go1.26-built release binary, so the gating lane
  could not run on control-plane, reliant or forge itself, all `go 1.27`. The
  binary is now chosen per module. The one on PATH is used when its build Go
  (read with `debug/buildinfo`, no subprocess) is at least the module's `go`
  directive. Otherwise forge builds its pinned golangci-lint with the toolchain
  `go` selects for the module, once per (pin, toolchain), into
  `<UserCacheDir>/forge/tools/golangci-lint/` (`FORGE_TOOLS_CACHE` overrides
  it), and prints a one-line notice first. If that build fails, the lane fails
  with a message naming the binary, both Go versions and the exact
  `GOTOOLCHAIN=… go install …` that fixes it. The gating lane and the advisory
  typed-config guardrail share one resolution per run. No golangci-lint on
  PATH is still a skip.

- **A no-version `forge env deploy` reuses the release already cut for the
  checkout, and builds nothing.** On 2026-10-09
  `forge env build prod --release 20261009.205925-d29da50b` cut prod's release
  from a clean checkout. A
  `forge env deploy prod` in the same unchanged checkout then rebuilt every
  image (the reliant image came out with a different digest) and named a new
  release. The control plane had stored the cut's tree hash, but the hosted
  ledger dropped `provenance` when it read releases back, so no release ever
  matched. Reads now decode it. A release is reused when it records this
  checkout's clean tree and this forge version, covers every artifact the env
  declares (each source-pinned frontend at today's resolved commit included),
  and every image it pins still resolves in its registry. Its name does not
  matter. A reused release is deployed as `forge env deploy <env> <version>`
  would deploy it, with no build. The deploy first prints
  `reusing release <v> (cut at …)` or `cutting new release <v> because …`. A
  dirty checkout, another forge version, a `-D` option, a missing artifact, or
  an expired image each cut a new release. The cut and the deploy capture
  provenance through one function.

- **Status writes survive the informer cache trailing them.** New
  `controller.UpdateStatus` retries a `Status().Update` conflict with the
  object's current resourceVersion, re-sending the status the reconciler
  computed. A reconciler requeued shortly after its own status write is handed
  the object from before that write, and a plain update was refused with "the
  object has been modified". control-plane's ManagedDatabase controller lost
  the Failed status it was recording that way, and logged the refusal at
  ERROR. A status subresource has one author, so taking the newer
  resourceVersion carries nothing stale. Any error other than a conflict is
  returned at once, and a conflict that outlasts the retries is returned for
  `IsTransient` to requeue. The CRD scaffold's state-machine shape uses it.

- **Storage maintenance unlinks are capped, paced, and back off on filesystem
  distress.** On 2026-10-09 one `forge storage gc --apply` unlinked 175,324 Go
  build cache entries in ~30s on a virtiofs-backed workspace volume while
  builds ran; the host's virtiofsd ran out of descriptors, the guest saw
  EMFILE, and the volume was detached seconds later. The trim skipped each
  failed unlink and tried the next. Every unlink a pass makes (Go build cache,
  golangci-lint, goimports, orphaned private caches, temp sweep, source cache,
  rotated logs) now goes through one governor: at most `max_deletes_per_pass`
  (default 50,000) per pass, paced to `delete_rate_per_sec` (default 1,000),
  and the first EMFILE/ENFILE/ENOTCONN/EIO stops the pass, retaining the rest.
  A backed-off pass prints `BACKED OFF`, records `backed_off_layers`, and exits
  0 so nothing retries it at once. A source clone is renamed out of its cache
  key before deletion, so a stopped eviction never leaves a half-deleted pin.

- **`forge build` stamps the scaffold's version package.** The server
  scaffold keeps `version`/`commit`/`date` in `<module>/cmd/<name>/cmd`, but
  `forge build` stamped only `main.*`, so every such binary reported version
  `dev`, and its error reports were filed under `<app>@dev` (control-plane's
  prod Sentry events arrived as `control-plane@dev`). Both packages are stamped
  now. The linker ignores a `-X` for a package the binary does not link, so a
  project that keeps its vars in `main` is unaffected, and a KCL
  `GoBuild.ldflags` entry still wins on the same key.

- **No automatic storage pass removes a worktree.** The background auto-gc
  (`forge env up`, the hourly job) ran the worktree layer, so a policy with
  `worktree_reap` let it remove any worktree that looked idle, clean and
  pushed, which is what a worktree an agent is between commands in looks like.
  Only an explicit `forge storage gc --apply` removes worktrees now; the daily
  job runs `gc --apply --scheduled` and previews. Explicit removal also refuses
  the main checkout, anything containing it or the home directory, a path whose
  `.git` is not a gitdir file of the repository, worktrees under Reliant's
  `~/.reliant/worktrees` (unless `worktree_reap_reliant_managed`), and any tree
  with an entry modified in the last 24h; it prints each canonical target
  before removing it, and `git worktree prune` only expires registrations idle
  for the same 24h.

- **A Firebase deploy no longer breaks every tab opened before it.** Sentry
  ELECTRON-CC: a tab on the previous build of app.reliantlabs.io lazy-loaded
  a code-split chunk the deploy had removed; Firebase answered the missing
  `/assets/<name>-<hash>.js` with index.html and HTTP 200 (the site's
  `** -> /index.html` rewrite), and the dynamic import died on a MIME/parse
  error. Three changes on `forge.OnFirebase`:

  - `spa_fallback = "/index.html"` replaces the catch-all rewrite, which is
    now refused. forge renders it as an RE2 `regex` rewrite matching only
    route-shaped paths — inside the document's directory, last segment not
    ending in a static-file extension — so a missing chunk is a real 404.
  - Each deploy carries forward the hashed assets of the previous releases
    (`keep_asset_releases`, default 10, floor 2) and records what the site
    serves in `<base_path>/forge-assets.json`; downloads are verified
    against the recorded sha256. Firebase stores content by hash, so carried
    files cost no upload. A site whose live release predates this is read
    as "no manifest", and an unreachable site degrades to carrying nothing;
    neither blocks the deploy.
  - forge renders `headers`: the build's asset dir (new `Frontend.asset_dir`,
    default `assets` for vite and `_next/static` for nextjs) is
    `public, max-age=31536000, immutable`, everything else `no-cache` (it
    was Firebase's default `max-age=3600` for index.html too). A frontend's
    `cache_control` rules, previously refused on Firebase, now replace that
    default.

- **`internal/pkgguard` catches every reference to a forbidden environment
  read, not only calls.** It inspected call expressions alone, so
  `getenv: os.Getenv` — a function value stored for a later call — passed it
  while forbidigo failed `Lint (forge/pkg)` on the same line. It now resolves
  each file's imports and reports any `os.Getenv` / `os.LookupEnv` /
  `os.Environ` reference, including through an aliased or dot import.

- **New lint rule `forgeconv-config-service-name`.** It flags a config-proto
  field bound to `OTEL_SERVICE_NAME`. A config field projects one value into
  every workload's env, overriding the per-workload name `forge.render`
  derives, and the scaffold shipped exactly such a field (`service_name`,
  default `"unknown"`) until 0d94e103, so every project scaffolded before then
  still carries it. Delete the field and reserve its tag and name. A workload
  that wants another name sets `OTEL_SERVICE_NAME` in its own env. ERROR,
  suppressible with `forge:lint-disable-next-line`.

- **Every workload gets its own `OTEL_SERVICE_NAME`.** A project-wide value
  filed every workload's spans, metrics and errors under one service:
  control-plane's config proto still carried the scaffold's old
  `service_name` field (default `"unknown"`), and the config projection
  stamped it onto reliant-api-server, its worker, daemon-gateway and every Job
  alike. `forge.render` now sets `OTEL_SERVICE_NAME` to the workload's name on
  every runtime that runs a process (host, cluster, hosted; not a tool),
  unless the workload's env sets one. An empty literal counts as unset.
  Derived keys follow the declared ones, so `$(VAR)` expansion order is
  unchanged. The scaffolded `dev` and `cloud` envs drop the per-env lambda
  that did this.

- **Every workload gets `OTEL_RESOURCE_ATTRIBUTES`: its environment, project
  and release.** `pkg/observe` takes the deployment environment from
  `OTEL_RESOURCE_ATTRIBUTES`, but nothing rendered it, so every service lost
  its environment attribute. `forge.render` now sets
  `deployment.environment.name=<env>,service.namespace=<project>,service.version=<release>`
  on the same runtimes as `OTEL_SERVICE_NAME`, omitting a key whose source is
  empty and percent-encoding each value. An author's own value is kept whole
  (an empty literal counts as unset). The release comes from the new
  `Bundle.release`, which defaults to the reserved `-D release_version=` option
  that `forge env deploy` and `forge env build` bind to the release they name.

- **`forge.OTEL_ENV` pairs its `:4317` endpoint with
  `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`.** The runtime's default protocol is
  `http/protobuf`, so a gRPC-port endpoint with no protocol exported nothing.

- **Go workloads get `GOMEMLIMIT` from their container memory limit.** The Go
  runtime does not read its cgroup limit, so a heap growing toward GOGC's
  target was OOM-killed before the collector felt any pressure. On a cluster
  or hosted runtime, `forge.render` now sets `GOMEMLIMIT` to
  `go_memory_limit_percent` (a new `Workload` field) of the memory limit (the
  declared limit, else the request), in whole MiB. The default is 90 for a
  `forge.GoBuild` workload and unset for any other: forge cannot see that a
  sibling repo's or a third-party image is Go, so declare it there. `0` opts
  out, and values above 95 are refused. It is never set when the workload's
  env sets `GOMEMLIMIT` or `GOGC`, or on host and compose, which have no
  container limit.

- **A release refuses a sibling checkout that is not at the commit the
  project pins.** A `ShellBuild` with `cwd = "../reliant"` built whatever that
  checkout had on disk, so a release could record a days-old sibling image
  while `go.mod` pinned a newer commit (prod release 20261007.165315). Every
  `ShellBuild` now records the checkout it ran in — HEAD, dirty flag, origin,
  module — in its build state, and a release carries it per artifact as
  `built_from`. `forge env build <env> --release` (also `--plan`, `--no-build`
  and a deploy that re-uses a release) refuses when an external checkout is
  dirty or not at the commit its `go.mod` require or `forge.GitSource` ref
  pins, before anything is built, and prints the fix:
  `git -C <dir> checkout --detach <commit>`.

- **Request factories seed parents in non-`public` schemas, and say why when they
  cannot.** `forge generate` warned `relation "users" does not exist` for every
  create-request factory in a project whose tables live in a named schema
  (`controlplane.users`): the baked parent `INSERT`s named only the bare table,
  so the shadow connection never found it, and the factory became a `t.Fatalf`.
  `schemadef.Table` now carries its `Schema`, and the seed planner renders
  `"controlplane"."users"` for any table outside `public`. A one-member
  vocabulary (`CHECK (provider = 'byo')`, which postgres stores as a plain
  equality rather than an `= ANY` list) is also read as a pool, so such a column
  no longer gets a value its CHECK rejects. When a parent row still cannot be
  built, the warning now names the parent table's multi-column constraints forge
  could not place and the two remedies that work (restate the constraint, or
  `forge project disown` the factory) instead of echoing a bare SQLSTATE.

- **A narrow stored credential elevates on demand instead of dead-ending.**
  `forge cluster connect` and `forge domain ls` answered a signed-in org owner
  with 403 `does not carry the cluster:manage / domain:read scope`, and the
  hint ("re-run `forge login`") minted the same narrow token again. When a
  call made with a `forge login` credential or a credential-helper token is
  refused for a missing scope, forge now calls the control plane's
  `AccessTokenService/ExchangeToken` for exactly that scope and retries once.
  The control plane re-reads the user's role at that moment, so the stored
  credential never has to carry `cluster:manage`. The elevated token lives in
  memory for the process and is never written to the credentials file; `--token`
  and the token env var are never elevated. A refusal is shown with the
  original error plus why elevation did not help (an older control plane, or a
  role without the permission).

- **Dependabot no longer bumps code generators it cannot regenerate for.**
  protoc-gen-es writes its version into every `*_pb.ts`, and protoc-gen-go
  (installed by `forge tools install` at go.mod's `google.golang.org/protobuf`)
  into every `*.pb.go`. Dependabot edits the lockfile or go.mod but never runs
  `forge generate`, so each such bump failed Verify Generated Code by
  construction; control-plane #645 was one, merged red. The scaffolded
  `.github/dependabot.yml` now ignores `@bufbuild/protoc-gen-es` and its exact
  peer `@bufbuild/protobuf` in the npm entry, and `google.golang.org/protobuf`
  in the gomod entry when the project runs protoc-gen-go. The file is yours
  once scaffolded, so existing projects get it from the v0.1.44 migration
  (`forge project upgrade list`). A migration detection script that ended in a
  quoted argument (`"$f"`) also had its closing quote stripped by the
  frontmatter parser and silently matched nothing; only a matched pair of
  quotes is stripped now.

- **`forge generate` no longer rewrites go.sum under a caller's
  `GOFLAGS=-mod=mod`.** Its `go` subprocesses inherited the caller's GOFLAGS,
  and the goimports pass over generated code runs `go list -m -e -json ...`,
  which under `-mod=mod` records a checksum for every module in the graph: 356
  go.sum lines on control-plane that a CI regenerate never produces. Every `go`
  subprocess forge runs on its own behalf — and goimports — now gets its
  environment from `goexec.Env`, which drops `-mod=mod` from the inherited
  GOFLAGS and keeps everything else (`-tags`, `-trimpath`, `-mod=vendor`, …).
  Env that forge or a project's declared build config sets explicitly is still
  honoured. A test pins that no new call site inherits the environment
  unscrubbed.

- **An unknown key in the per-machine `storage.json` no longer fails unrelated
  commands.** `storage.Load` decoded with `DisallowUnknownFields`, so a file
  written by another forge version (e.g. `go_cache_unused`, a live key added in
  #537 and unknown to older binaries) made `forge build` and friends abort with
  `storage policy: json: unknown field`. Unknown keys are now ignored with a
  one-line warning naming them; strictness stays with forge.yaml.

- **A fresh scaffold signs in.** The README's first sixty seconds ended at a
  sign-in page nobody could pass, for two reasons.

  - `auth idp-provision` printed the login broker's token to the terminal
    ("shown ONCE") instead of storing it, minted a new one on every
    `forge env up`, and the dev `_api` named only `DATABASE_URL` in
    `config_secrets` — so even a token stored by hand never reached the server,
    and `/auth/login` was a 404. The job now keeps the token in the env's secret
    store (`secrets/dev.yaml`, as `IDP_BROKER_TOKEN`; the dev env hands it the
    path as `IDP_BROKER_TOKEN_STORE`), reuses it while the issuer still accepts
    it (`devidp.ProvisionLoginBroker`, `devidp.SecretFile`), and never prints
    it. The dev `_api` names it, and `forge env up` re-reads the store after its
    jobs, so the first run's API already has it.
  - After sign-in every RPC answered 401 "missing Authorization header". The
    session is an HttpOnly cookie, but the Next.js transport did not send
    credentials cross-origin, and the scaffolded auth interceptor read only the
    `Authorization` header. The transport is built on `fetchWithSession`, as the
    Vite one already was, and `pkg/middleware/middleware.go`'s `credentialFrom`
    reads a Bearer header, else the session cookie. `SessionCookieName` is
    declared there and `internal/app/login_broker.go` sets the cookie under it.

  Existing projects own every file involved, so apply it by hand: add
  `"IDP_BROKER_TOKEN"` to `_api`'s `config_secrets` and
  `IDP_BROKER_TOKEN_STORE = "secrets/dev.yaml"` to the idp-provision env in
  `deploy/kcl/dev/main.k`; re-scaffold `cmd/<bin>/cmd/auth.go` and
  `pkg/middleware/middleware.go` (delete, then `forge project rescaffold <path>`)
  or port the changes; and give each real transport in
  `frontends/<name>/src/lib/connect.ts` a fetch that sets
  `credentials: "include"`.

- **The observability opt-in delivers data.** Following the comment in
  `deploy/kcl/dev/main.k` ran Grafana with nothing reaching it: the host-run API
  had no OTLP endpoint and compose published no OTLP port. The forge dashboards
  never loaded either (`grafana/otel-lgtm` reads providers from
  `/otel-lgtm/grafana/conf/provisioning`, not `/etc/grafana/provisioning`), and
  their panels queried `rpc_server_duration_milliseconds`, which otelconnect
  v0.10 no longer emits. Now one switch, `_observability = True`, runs `lgtm`,
  publishes OTLP on a loopback port the env declares, and points every host
  process at it. The overview dashboard queries
  `rpc_server_call_duration_seconds` (by `rpc_method` and
  `rpc_response_status_code`) and the database pool, and its test derives the
  series names from otelconnect itself. `forge env up` and `forge env status`
  list what compose services publish, Grafana included.
- **`forge env status` no longer passes on what it cannot see.** Prometheus
  reported "✓ 1 targets up" when the only target was the lgtm image scraping
  itself; it now passes only on series carrying the app's `job`. pprof read
  whichever process held the machine-wide 127.0.0.1:6060 and reported its
  profiles as the app's; each dev host process now gets its own `PPROF_ADDR`,
  and the check warns when `/debug/pprof/cmdline` names another process.
- **`forge env up` output.** initdb's and pg_ctl's transcript (absolute paths,
  a `pg_ctl … start` hint) goes to `.forge/hostinfra/postgres/postgres.log`.
  Host jobs write the `.forge/logs/<env>/<job>.log` the summary lists. A compose
  service is converged once per run, not three times. Entity birth no longer
  announces a "(+down)" migration it does not write, and prints the migration
  project-relative. The compose check's hint is `forge env up <env>`, not
  `docker compose up -d`.
- **One unusable currency no longer takes a generated page down.** The list and
  detail pages format money in each row's own `currency`, and
  `Intl.NumberFormat` throws a `RangeError` for anything that is not an ISO 4217
  code — an empty string, a typo, or the dev seed's `sample_currency_<n>` — so
  the whole Orders page fell to "Something went wrong" on the first
  `forge env up`. `formatMoneyCents`, `formatMoneyWhole` and
  `formatMoneyInterval` now render such a value as the plain amount beside the
  raw code. Existing projects: copy `formatMinorUnits` and the three helpers
  from a fresh scaffold's `src/lib/format-utils.ts`.
- **Migration-safety findings point at the statement.** Each statement's line
  was counted from the previous semicolon, so the first statement under the
  header `forge db migration new` writes was reported at `:1`, and every later
  one at the line before it. The verdict also put the finding count beside the
  number of files scanned ("2 findings across 2 migration files" for two
  findings in one file); it now says "2 findings in 1 migration file (2
  migration files checked)".

### Changed

- **Forge is open source: Apache License 2.0, replacing the Business Source
  License 1.1.** The license covers the whole module (the CLI, every
  `forge/pkg` library, the KCL modules) and `@reliantlabs/forge-web-runtime`,
  which is published as 0.4.1 under `Apache-2.0`. Every version published
  before this change is also licensed under Apache-2.0, alongside BSL. Code
  forge generates, scaffolds or copies into a project belongs to that project
  and carries no attribution obligation (README, "License"). `NOTICE` is new.
  The npm package ships `LICENSE` and `NOTICE`; `TestWebRuntimeShipsTheRepoLicense`
  keeps its copies identical to the root, and the release workflow refuses a
  tarball without them. Contributions are signed off under the Developer
  Certificate of Origin rather than a CLA (CONTRIBUTING, "License of
  contributions").

- **Every successful call is logged again by default; sampling is a
  per-environment setting in typed config.** `observe.LoggingInterceptor`
  (`rpc completed`) and `observe.LogMiddleware` (component calls) sampled
  successes out of the box — one per procedure per minute. They now write
  every success unless their caller passes `observe.WithSuccessSampling(d)`.
  Failures and successes over 1s are always logged. The scaffolded
  `proto/config/v1/config.proto` declares `log_success_sample_window`
  (default `0s`; env `LOG_SUCCESS_SAMPLE_WINDOW`, flag
  `--log-success-sample-window`), so a deployment sets it per env in
  `deploy/kcl/<env>/config.k`, and the scaffolded `serve.go` passes the
  loaded value to `observe.Chain` as `LogOptions`. `forge/pkg/observe` reads
  no environment: an existing project adds the field to its AppConfig and
  `LogOptions: []observe.LogOption{observe.WithSuccessSampling(cfg.LogSuccessSampleWindow.AsDuration())}`
  to the `observe.Deps` in its owned `serve.go`.
  `observe.DefaultSuccessSampleWindow` is removed (there is no default
  window).
- **One prettier release formats a scaffolded project, everywhere.** Frontends
  now pin `prettier` exactly (`3.5.3`, was the range `^3.5.0`), and the
  scaffolded `.pre-commit-config.yaml` runs that same release as a local hook
  instead of `pre-commit/mirrors-prettier` (archived at `v3.1.0`). The two
  disagreed about the component library forge installs, so a fresh project's
  hook rewrote files its own `npm run format` had just written, and the range
  drifted to whatever `npm install` resolved. Existing projects: replace the
  mirrors-prettier repo in `.pre-commit-config.yaml` with a `repo: local`
  hook (`language: node`, `entry: prettier --write --ignore-unknown`,
  `additional_dependencies: ["prettier@3.5.3"]`), and pin
  `"prettier": "3.5.3"` in each frontend's `package.json`.
- **Skills and project memory now lead with shipping.** The start-here `forge`
  skill gains a "Ship it" section (hosting is the default, `forge env deploy`,
  the free static tier, queued-on-billing exit 7, no `forge login` under
  Reliant); `deploy/hosting` holds the gates table and the provides/refuses
  table; `frontend/serving`, `deploy/static-site`, `db/deploy-migrations` and
  `secrets` state what hosting accepts and gates on. The project memory template
  gets a one-line Shipping note.

### Removed

- **BREAKING: forge no longer runs or scaffolds sqlc.** The data-access path is
  the ORM projected from the applied migrations: the generated `<Entity>Store`,
  plus the owned `internal/db/<entity>_repo_ext.go` seam for anything it cannot
  express (bun, CTEs, `FOR UPDATE SKIP LOCKED`, raw SQL), tested against a real
  migrated database. The `sqlc generate` step is gone from `forge generate`, so
  a root `sqlc.yaml` is no longer run implicitly: keep it running while
  you port off it by declaring it as a `generate.hooks` entry (see Added). A root
  `sqlc.yaml` with no hook running it gets a forge.yaml load warning that
  spells out the hook. `forge project new` no longer writes `db/sqlc.yaml` or
  `db/queries/`. That stub was dead on arrival anyway: generate only looked for
  a ROOT `sqlc.yaml`, its paths were root-relative inside a file sqlc resolves
  relative to `db/`, and it targeted the `internal/db` package the ORM generates
  into. `db/README.md` now documents migrations, seeds and the query seam. The
  `db`, `contracts`, `testing/integration` and `migration-service` skills say to
  port sqlc queries onto the store, not to keep writing them.
- **The host-application credential DEPOSIT is gone (`pkg/cloudcred.Save`,
  `Delete`, `HostClientID`, `Location`, `Credential`).** A host used to copy its
  own session token into `credentials.json` under client `host-app`, and forge
  presented it to the control plane's deploy API. That token was a permanent,
  multi-purpose session credential (Reliant's daemon or CLI login) which mostly
  did NOT hold deploy authority, so deploys 403'd and users ran `forge login`
  anyway — and where it did work, the deploy API, the registry login and every
  subprocess saw a credential that could also connect as the user's daemon.
  forge no longer reads `host-app` entries at all; `cloudcred.RemoveLegacyHostDeposits`
  lets a host purge what it wrote. Replaced by the credential helper below.
- **forge no longer requires cgo.** `kcl_plugin.forge` is bridged through a
  purego callback, so `go install` works with any `CGO_ENABLED` setting,
  including Windows without a C toolchain. `env diff`'s `unsupported` status is
  gone, as are the `CGO_ENABLED=1` prefixes and "CGO is required" notes in the
  Taskfile, Makefile, scaffolded CI install step, version-skew install hints
  and skills.

### Changed

- **The dev loop runs the API as ONE process — `_api`, the binary's
  `server`, under air (hot reload) — so the frontend reaches every service.**
  Dev ran each service as its own `go run ./cmd/<p> <service>` host process on
  its own port, and the frontend's dev config (`api_url`, the
  `<project>-dev-api` port key) named the first: a 3-service project's
  frontend could call one service in dev — the defect #527 fixed for hosted
  envs, and it strands the sign-in cookie the same way. `deploy/kcl/dev/main.k`
  now declares the same `_api` the hosted envs do, `_port_of` gives it the
  `-api` key, and it binds `_on_host(_api)` instead of the services and
  workers (`server` mounts every service and supervises every worker). This
  is what the docs already said — "one binary serves every service on one
  mux", "Go services (hot reload)" — and what `.air.toml` (`entrypoint =
["./tmp/<p>", "server"]`) and `task dev` already ran; `forge env up dev`
  did neither. `_on_host` now runs `server` under air (anything else still
  `go run`), so a .go edit rebuilds and restarts the API; air must be
  installed (`go install github.com/air-verse/air@latest`, which `forge
doctor` already required), and `forge env up` refuses to start without it,
  naming the workload and the `runner = "go-run"` way out, instead of failing
  in the host phase on `exec: "air": executable file not found`. It no longer
  prints "the workload's args [server] are not passed" when the air config's
  entrypoint passes exactly those args. `forge scaffold service|worker` adds
  no dev line (`_on_host(_api)` is bound with the first service);
  `forge env up dev --target api` restarts the API. **Existing projects:**
  copy `_api` and the `_on_host` binder from a fresh `forge project new`'s
  `deploy/kcl/dev/main.k`, change `_port_of`'s `-api` owner to `"api"`, and
  replace the service/worker lines with `_on_host(_api)`.
- **New projects deploy to Reliant hosting by default.** `forge project new`
  scaffolds `staging` and `prod` hosted on the forge control plane: the API
  (every service and worker, as one workload — below) and the migrate job
  bound `_hosted` (its image's registry host dropped,
  since the platform pulls only from its own registry), a
  `forge.ManagedDatabase` each workload reads through `forge.DatabaseRef`,
  `forge.HostedSecrets`, the frontend on platform static hosting with
  `API_URL` and `CORS_ORIGINS` wired by reference, and `control_plane =
forge.ControlPlane {}`. A fresh scaffold renders every env with no
  placeholder. Before, staging and prod bound everything to a cluster the
  author had to name (`forge env deploy prod --explain`: `REFUSE (declared
context not in kubeconfig)`) and the frontend to `REPLACE_ME_BUCKET`. The
  scaffolded CI is the hosted pipeline (`release.yml`, no registry login)
  instead of `deploy.yml`. `dev` is unchanged. Hosted workloads and the
  managed database need billing; a static site alone is free.
  - **The hosted API is ONE workload, `_api`: the binary's `server`.** A
    browser reaches the API at one origin — the frontend's Connect transport
    has one base URL (`/<package>.<Service>/<Method>`, so one origin serves
    every service), and sign-in answers with an HttpOnly session cookie the
    browser returns to that origin only. The platform gives each workload its
    own hostname and routes no paths between them, so hosting each service as
    its own workload (as first shipped) left a 3-service project's frontend
    able to call one service: `API_URL` named the first. Per-service URLs in
    the browser cannot fix that — the session cookie would still be stranded
    on one host — so each hosted env declares `_api` (`args = ["server"]`:
    every service on one Connect mux, every worker supervised beside it) and
    binds it instead of its services and workers; the frontend's `API_URL`
    names it. One workload is also one bill, not one per service. It is bound
    once there is something for it to run (a project born with no service
    pays for no idle workload; `forge scaffold service` binds it with the
    first). `forge scaffold service|worker` then adds no hosted line — `server`
    already runs it (dev too, below); a job,
    an operator (which `server` skips without a Kubernetes API) and a tool
    bind as before. `forge env new X --from prod --bind api=cluster` rebinds
    the API; `--bind <service>=…` is refused, naming `_api`. Splitting a
    service or worker out is its own `_hosted(wl.<name>)` line plus taking it
    out of `server` (cmd/<bin>/cmd/server.go). The binary mode
    (`--binary per-service|shared`) never reached the deploy files and does
    not now: both modes ship one binary with a subcommand per component and
    the all-in-one `server`.
  - **Hosting elsewhere stays a one-line rebind.** Each env declares
    `_on_cluster` and `_on_bucket` beside the hosted binders, unused, with
    the `forge.ClusterTarget` / `forge.OnBucket` to fill shown in a comment
    (`_cluster = None`, `_bucket = None`). A binding to either before it is
    declared fails the render, naming the workload and the fix.
  - **Kinds hosting refuses.** `forge scaffold operator` binds the operator
    `_on_cluster` in every deployed env — the one binding that can run it —
    and warns that those envs refuse to render until `_cluster` is declared
    (or the line is dropped); a hand-declared `kind = "cron"` binds the same
    way. forge's cron component (`forge scaffold worker --kind cron`) is a
    worker with its own scheduler and is hosted like any worker. Every other
    new workload binds where the env's `migrate` job runs, so an env
    scaffolded on a cluster keeps binding there.
    `forge scaffold frontend` binds a new frontend through a hosted env's
    `_hosted_frontend`, and still to a bucket in an env without one.
  - **Existing projects are not rewritten** (env files are scaffolded once).
    To adopt hosting, add `control_plane`, `secret_provider =
forge.HostedSecrets {}` and a hosted `forge.ManagedDatabase` to the env's
    Bundle, give `_hosted` the bare image and `DATABASE_URL =
forge.DatabaseRef {...}`, and rebind each line `_hosted(...)` — or try it
    beside prod with `forge env new cloud --from prod --bind <name>=hosted`
    first. The `deploy` skill has the exact edits. A project scaffolded
    between the hosting default and the one-workload API binds each service
    `_hosted(wl.<name>)` and its frontend calls only the first: replace those
    service and worker lines with `_api` (copy the declaration and the
    `_api_config` / `_hosted_frontend` lines from a fresh `forge project new`'s
    `deploy/kcl/prod/main.k`; the `deploy/hosting` skill shows them).
- **Next.js frontends are scaffolded as static exports, and the generated CRUD
  pages are static routes.** Before, the default was `output: standalone`, a
  Node server that hosted static hosting (`forge.OnHosted {}`) cannot run. The
  generated detail and edit pages were dynamic `src/app/<slug>/[id]/` routes,
  so switching to `output: static` failed `npm run build` on the first entity
  with `Page "/<slug>/[id]" is missing "generateStaticParams()"`. Now:
  - `forge project new --frontend` and `forge scaffold frontend` write
    `output: static` into `forge.yaml`. `npm run build` exports into `out/`,
    and the Dockerfile serves `out/` from nginx. `--output standalone` is the
    explicit opt-in for a Node server. `next dev` is unchanged. A `forge.yaml`
    entry with no `output:` still means standalone, because every frontend
    scaffolded before this change has a standalone `next.config.ts`.
  - Detail and edit are `src/app/<slug>/view/page.tsx` and
    `src/app/<slug>/edit/page.tsx`, served at `/<slug>/view?id=…` and
    `/<slug>/edit?id=…`. Each reads the id with `useSearchParams` under a
    Suspense boundary, and renders a "which row?" state when `?id=` is
    missing. List row clicks, create-then-redirect, the Edit action, edit's
    Cancel, breadcrumb and save redirect all build the new URLs through
    `src/lib/entity-routes.ts` (`entityViewHref`, `entityEditHref`,
    `useEntityIdParam`). That file is a new scaffold-once helper with its
    own vitest suite. `forge generate` backfills it into an older frontend
    the first time it writes a page that imports it.
  - The id is in the query string, not the path, because an export can only
    hold pages it enumerates at build time. `trailingSlash` stays off, so
    the export writes `out/<slug>/view.html`. Hosted static hosting and the
    scaffolded nginx image both resolve `/<slug>/view` to that file by
    trying `.html`. No CDN rewrite is involved.
  - The static `next.config.ts` sets `images: { unoptimized: true }`, since
    next/image's optimizer is a server route. The dev-only browser-log
    route (`app/%5F_forge/log`) answers POST only, so the export leaves it
    out.
  - The sign-in guard keeps the query string in `returnTo`, so a visitor
    bounced off `/<slug>/view?id=…` comes back to the same row.
  - **Existing projects are not rewritten.** Their pages are scaffold-once.
    While `src/app/<slug>/[id]/` exists, `forge generate` keeps it as that
    entity's detail/edit route and writes no view/edit pair beside it. If
    the frontend is `output: static`, generate warns that `next build` will
    refuse those routes. `forge project upgrade list` offers the new
    `migrations/v0.1.44` playbook to any project with a `[id]` route. The
    playbook covers rescaffolding untouched pages, moving edited ones, and
    switching `output:` with `forge project upgrade --force next.config.ts
Dockerfile`.
  - `forge lint --guarded-fields` reads the new `<slug>/edit/page.tsx` as
    well as the old `[id]/edit` path. The stale-route report recognizes both
    page shapes.
- **The render refuses a Next.js server build bound to a static runtime.**
  `forge.OnHosted` for a frontend, `forge.OnBucket` and `forge.OnFirebase`
  serve files and run nothing. A frontend whose forge.yaml `output` is
  `standalone` or `server` used to render, deploy and build green, then fail
  at publish on an `out/` nothing wrote. The roofers scaffold bound one this
  way. `forge env render` / `deploy` / `up` and `forge build` now refuse it
  at render. The refusal names the frontend, the env and both fixes: set
  `output: static` and migrate (`forge lint --static-export` lists what), or
  bind it elsewhere, since a Next.js server ships as a workload with a
  `forge.DockerBuild`. forge.yaml stays the one declaration of the build
  shape. forge binds it into every render it drives as the reserved
  `-D frontend_outputs` option, and the KCL never restates it. An env with no
  forge.yaml frontend of that name, or a plain `kcl run`, is not judged.
  **Adoption:** a project whose forge.yaml frontend has no `output:` reads as
  `standalone` (what its scaffold-once next.config says). If such a frontend
  is bound to a static runtime, set `output: static` and follow
  `forge skill load migrations/v0.1.44`.
- **Concurrent `forge generate` / `forge scaffold` runs in one project now
  queue instead of failing.** Every run takes the project's lock
  (`.forge/forge.lock`). A second run prints one line,
  `⏳ forge: waiting for pid N (<command>) …`, and starts when the first
  finishes. Previously the lock was an O_EXCL marker file: every concurrent
  run failed at once with "another forge process is running … remove it with:
  rm .forge/forge.lock". Agents that followed that advice ran two pipelines
  in one tree, and their revert-on-failure rollbacks restored each other's
  writes. The lock is now an OS file lock (flock; LockFileEx on Windows) that
  dies with its process, so a killed run cannot leave a stale lock and there
  is no 10-minute reclaim window. `forge scaffold` and all its nouns hold it
  for the whole command, not only for the generate pass at the end, so another
  run never sees a half-scaffolded tree: protos with the CRUD quintet injected
  and a migration written, but no ORM or handlers. Build's auto-generate,
  `generate --watch`, `--check`, `project rescaffold` and `project new` take
  the same lock. The file stays on disk between runs and is already
  gitignored (`.forge/*`); do not delete it.
- **`orm.Context` gains `RunTx`, `RunTxReadOnly` and `RunTxWithOptions`.**
  They are on the interface so `s.deps.DB.RunTx(…)` compiles against the
  injected `DB orm.Context`. A hand-written `orm.Context` implementation, such
  as a test fake, must add the three methods. `orm.Client.Bun()` now returns a
  thin wrapper around the `*bun.DB`, so `ExecContext`/`QueryContext`/
  `QueryRowContext` can join a ctx transaction. Use `BunDB()` when you need the
  concrete type. `RunTransaction` keeps its semantics: database-default
  isolation, one attempt, handle-passing. It now joins a transaction already
  carried in ctx, and `Tx.Commit` runs after-commit callbacks registered
  through `tx.RunTx`.
- **Scaffolded CRUD lifecycle tests carry no fixtures; their rows come from
  regenerated factories.** `handlers_crud_test.go` (scaffold-once) used to
  embed a literal `INSERT` seed block and literal create-request values,
  frozen against the birth schema. Editing the birth migration afterwards —
  the db skill's own advice: a `GENERATED` column, a one-way status `CHECK` —
  broke it (`cannot insert a non-DEFAULT value into column …`,
  `violates check constraint …`). It now calls
  `<svc>.NewCreate<Entity>Request(t, db, 0|1)`,
  rendered into the forge-owned `factories_gen_test.go` from the applied schema
  on every `forge generate`: it seeds the FK parents and returns a request the
  current constraints accept (enums the column DEFAULT supplies, nullable
  timestamps and GENERATED columns are left unset). A factory forge cannot
  derive a valid request or row for is still emitted, with a body that fails
  the calling test naming postgres's verdict, and `forge generate` prints it
  as a warning — it no longer fails the generate.
- **`New<Entity>` factories insert minimal, lifecycle-consistent rows.** Every
  NOT NULL column without a DEFAULT and every FK parent is filled; every other
  column is left to its DEFAULT or NULL; GENERATED columns are skipped; CHECKs
  are satisfied, including length/range/vocabulary checks, a DEFAULT the
  column's own CHECK rejects, and one-way status implications (the
  union/guard branch closest to the DEFAULTs). A jobs factory that used to
  insert a LEAD job with a crew, a schedule, `completed_at` and `lost_reason`
  now inserts a bare LEAD. Each factory is executed against the shadow schema
  at generate time; if the minimal row is rejected the full row is tried.
- **`forge lint --fixture-drift` executes literal fixtures instead of
  pattern-matching them.** Every fixture statement in a scaffolded
  `handlers_crud_test.go` runs against the shadow schema in a rolled-back
  transaction (grouped per test function, one savepoint per statement), and
  postgres's own error is reported per statement — GENERATED columns, UNIQUE
  collisions, dangling foreign keys and the CHECK violations the old matchers
  missed. A project with no literal fixtures opens no database; an unreachable
  shadow is reported as `forge-fixture-unverified`, never as clean. The rule id
  is now `forge-fixture-rejected` (was `forge-fixture-generated-column` /
  `forge-fixture-duplicate-unique`). `--crud-fixtures` (the dangling-FK
  matcher, rule `forge-crud-fixtures`) is folded in and kept as a hidden,
  deprecated alias.
  **Existing projects:** run `forge generate` (factories gain the
  `New<CreateRequest>` functions), then `forge lint --fixture-drift`. Your
  `handlers_crud_test.go` is yours and is not rewritten; to adopt the new shape,
  delete its seed block and replace each literal `&pb.Create<Entity>Request{…}`
  with `<svc>.NewCreate<Entity>Request(t, db, 0)` / `(t, db, 1)`. Tests that
  relied on `New<Entity>` populating optional columns now get DEFAULT/NULL
  there — set them with an override.
- **`forge doctor`'s `forge: kcl-plugin` check is now a real probe.** It
  evaluates a one-line KCL program through `kcl_plugin.forge` instead of
  reading a build flag, so it fails for what can actually break now: KCL's
  native library failing to extract or load (antivirus, read-only temp dir,
  bad `KCL_LIB_HOME`). Same name and pass/fail/skip meaning as before.
- **A KCL runtime that cannot load is an error, not a crash.** kcl-lang.io/lib
  panics when it cannot extract libkcl; forge used to crash with a stack trace
  (`forge doctor` exited 2). It now reports `load the KCL runtime: …` from
  every render.
- **Scaffolded `.air.toml` sets no env inline.** `ENVIRONMENT=development` is
  set by `forge env up` on the air process (and by `task dev`), so the config
  uses `entrypoint` and works under air's PowerShell on Windows.
- **`task db:restore` shows pg_restore's warnings.** The `grep -v` filter that
  hid them is gone, because `grep` does not exist on Windows.
- **An unwritten `forge:computed` field fails `forge lint`, like an unwritten
  `forge:read-only` one.** It used to only warn while read-only failed —
  backwards, since computed is the stronger promise ("my app derives this").
  `computed-fields lint` now gates; `--computed-fields --json` reports
  `ok: false`.
- **Pending-stub mode for both unwritten-column rules.** Right after
  `forge scaffold`, the rpc that will write a read-only/computed column is
  still forge's own `// forge:gen unwired-stub` placeholder, and failing the
  gate on forge's fresh output taught agents to ignore the rule. While the
  declaring service's handler package still holds those stubs, the finding is
  a warning naming them — `pending: implement ChangeJobStatus, ScheduleJob` —
  and once none remain it is an error again. Deterministic: the service comes
  from the entity's proto directory and the handler package from the same
  resolver `forge generate` uses; forge does not guess which rpc writes which
  column.
- **A failing `forge lint` names the failed linters on its last line**
  (`forge lint: 2 gating linter(s) failed: computed-fields lint, read-only-fields lint`),
  instead of "one or more linters reported errors".
  The verdict is the last line in every mode.
- **`frontend lint` no longer "passes" on a project with no frontend.** With no
  declared frontend and no `frontends/` directory it is a silent no-op, as the
  frontend typecheck lane already was, instead of counting as a gating linter
  that ran.
- **Bridging a project to a local forge checkout is opt-in: `forge project new
--link-forge`.** It used to happen on its own for every dev build of forge —
  including a host binary that embeds forge through a workspace (`reliant
forge project new`), which bridged every project it created to whatever
  checkout that host compiled from. On a shared machine that is the main
  checkout everyone pulls into, so the library under those projects moved with
  each merge while the binary generating their code did not. Now nothing is
  written unless asked for; a service scaffold from an unreleased build is
  refused with the flag named, and `--link-forge` on a binary that cannot
  locate its checkout is refused before anything is written.
  `FORGE_LINK_FORGE=1` is the same opt-in for harnesses (forge's e2e corpus
  sets it). The web-runtime twin (`.forge-link/` + root `package.json`) now
  follows the project's go.work bridge and links that same checkout, instead
  of being laid down by any dev build for its own checkout. **Adopting:**
  existing bridged projects are untouched; a contributor scaffolding with a
  dev forge adds `--link-forge`.
- **The scaffolded `handlers_crud.go` says each thing once.** Every delegating
  method used to carry the same eight-to-ten-line comment (what pkg/crud does,
  where the wiring lives, and the full AUTHENTICATED / PUBLIC / SCOPED
  explanation). Now each method has a summary line and one `Auth:` line that
  still names `middleware.GetUser` (an author editing `DeleteOrder` reads
  `DeleteOrder`'s comment, not the header). The file header explains each tag
  once, and only the tags the file contains. Update methods keep a line on mask
  handling, which now mentions that `op.ReadOnly` columns are refused and kept.
  The file is yours, so existing projects keep their comments; methods appended
  later get the short form.

### Added

- **`forgeconv-list-filter-optional`: `forge lint --conventions` now rejects a
  List-request filter field without `optional`.** The rule ("List request
  filter fields must be optional") was documented in every project's
  CLAUDE.md and the proto skill and checked by nothing. A scalar or enum field
  on a `List<X>Request` (or the request of a `List<X>` rpc) with no presence
  is an error: unset and the zero value are the same on the wire, so the
  generated List op either can never filter on the zero value or — for a
  bool/enum — always applies it and returns only the `false`/UNSPECIFIED rows.
  Pagination/ordering controls and message-typed fields are exempt, enums
  resolve across the proto tree, and a required parameter can say so with
  `[(buf.validate.field).required = true]` instead.

- **Signed in to Reliant means signed in to the control plane — no `forge
login`.** forge has a credential-helper protocol (`pkg/cloudcred`, the model
  of kubectl exec plugins and git credential helpers): when
  `$FORGE_CREDENTIAL_HELPER` names a command, forge writes
  `{"version":1,"endpoint":…,"scopes":[…]}` to its stdin and reads a token (or a
  structured refusal: `no_session`, `denied`, `unavailable`) from its stdout.
  The value is a JSON argv array, or a single executable path. Reliant sets it
  for `reliant forge …` and for every shell its agents run, and answers with a
  short-lived token minted from the user's Reliant session for exactly the
  endpoint the env declares. Hosted commands (`env deploy`, `secret`,
  `domain`, `cloud`, `release`, `env promote|verify|start|stop`, `registry
login`, …) resolve a credential in this order: `--token`, the env's declared
  token variable (CI), the stored `forge login`, then the helper — and an
  EXPIRED `forge login` falls through to the helper instead of failing. A
  helper's token is reused within one process until a minute before it
  expires; caching across processes is the helper's job. When the helper
  refuses, forge prints the host's own advice (Reliant: "sign in to Reliant
  (`reliant auth login` / the app)") and the CI variable — never `forge login`
  — and a 401/403 on a helper-minted token says to sign in again there. Helper
  stdout never reaches an error message or a log. `forge cloud status <env>`
  shows the source and expiry; `forge login` notes when a helper is already
  set. `forge login` and the token variable are unchanged for standalone forge
  and CI.

- **`forge lint --static-export`, and a `static-export lint` lane in every
  `forge lint`.** It judges each Next.js frontend that must build to a static
  export: forge.yaml declares `output: static` for it, or any env binds it to
  `forge.OnHosted`, `forge.OnBucket` or `forge.OnFirebase` (learned from each
  env's real render, not a text scan). It reports with file:line and a fix:

  - **errors**, which `next build` refuses for an export: a dynamic segment
    (`[x]`, `[...x]`, `[[...x]]`) with no `generateStaticParams`, a GET route
    handler not declared `force-static`, `'use server'`, `next/headers`,
    `dynamic = "force-dynamic"` / `revalidate = 0`, and next/image without
    `images.unoptimized` or a custom loader. Also a frontend whose build is not
    an export at all, and forge.yaml and next.config disagreeing about it.
  - **warnings**, for what the export accepts and then drops: `middleware.ts` /
    `proxy.ts`, non-GET route handlers that are not dev-only, next.config
    `rewrites` / `redirects` / `headers` not gated to development,
    `revalidate = N`, and an export bound to `forge.OnBucket` without
    `trailingSlash: true` (a bucket resolves no `.html`, so every route but
    `/` 404s; the hosted origin tries `<path>.html` itself).

  CI's `NODE_ENV=production npm run build`, `forge build` and
  `forge env deploy` run the real export, which stays authoritative. This lane
  reports the same failures earlier and offline. A `[id]` page born from
  forge's CRUD templates names the `migrations/v0.1.44` route migration. It is
  a text scan with no TypeScript AST, so it stays silent rather than guess: a
  re-exported `generateStaticParams` or a next.config built by a function is
  not followed. Honours `--quiet`, `--scope` (by file) and `--json`, and the
  shared `// forge:lint-disable-next-line <rule>: <reason>` directive.

- **Queued hosted deploys: exit 7, the block, and `--wait`.** A control plane
  that ACCEPTS a deploy but holds it on a person — today, billing for the
  hosted workloads or managed database it runs, or for static sites past the
  free tier — no longer makes `forge env deploy` fail or sit out a
  fifteen-minute wait on a promotion nothing is moving. The release, bundle
  and promotion are recorded as usual; forge reads the hold off the Promote
  response (rollout phase `HELD` as the fallback) and prints ONE block — what
  it waits on, why, what to do, and the action URL — then exits **7
  (queued)**, a code nothing else uses.
  The deploy goes live by itself once billing is set up; nothing is re-run, and
  a newer deploy replaces a queued one. `--json` carries the same facts as
  `.queued` (`waiting_on`, `holds[].action_url`). `forge env deploy --wait`
  blocks through the queue until the release is live (or `--timeout`; still
  queued then is 7), and is mutually exclusive with `--no-wait`.
  `forge env status <env>` on a queued env exits 7 with the block instead of
  reporting drift, and `forge env status <env> --wait` waits through it. The
  capacity pre-flight now continues past a "would be queued" answer
  (`CheckDeployCapacity` holds) instead of refusing before the build; an older
  control plane that sends no holds is refused exactly as before. See the
  `deploy/hosted-capacity` skill.
- **Money and basis-point helpers in `@reliantlabs/forge-web-runtime`.** The
  barrel now exports `formatMinorUnits`, `parseMinorUnits`,
  `minorUnitsToInput`, `currencyMinorDigits`, `formatBasisPoints`,
  `parseBasisPoints` and `basisPointsToInput`. They are the web counterpart of
  `pkg/money`: an amount is an integer count of minor units, as a `bigint` or
  an integer `number`, plus an ISO 4217 code. Currency defaults to USD and
  locale to en-US. Each currency's minor digits come from Intl (JPY 0, KWD 3),
  and formatting stays exact past 2^53 because Intl receives a decimal string,
  never a float. Parsing returns `null` instead of rounding or guessing. That
  covers too many decimals, a negative without `allowNegative`, a misplaced
  group separator (`"12,50"` in en-US), another currency's symbol, and values
  outside int64. The input helpers round-trip through the parsers. Rate parsing
  caps at 100% unless you pass `max`. Plain library functions: codegen does not
  infer money from field names. A forge app (roofers) hand-wrote these, along
  with a per-page `bpsToPercentInput`.
- **`// forge:generated <expr>` births a GENERATED column.** The db skill says a
  value derived from the same row should be `GENERATED ALWAYS AS (…) STORED`,
  but birth could only emit `BIGINT NOT NULL DEFAULT 0`, so the owned
  migration was hand-edited straight after every birth (`line_total_cents`,
  `tax_cents`, `balance_cents` in the roofers run). Write the expression on the
  field instead: `int64 line_total_cents = 9; // forge:generated
round(quantity * unit_price_cents)::BIGINT`. The rest of the comment line is
  copied verbatim into `<col> <type> NOT NULL GENERATED ALWAYS AS (<expr>)
STORED`. NOT NULL follows the presence rule every born column follows:
  dropped for an `optional` field and for a Timestamp, so an expression that
  yields NULL for a plain field fails the write rather than storing a value
  the wire cannot carry. There is no DEFAULT; an enum keeps its CHECK and
  protovalidate rules still project. The marker implies `forge:read-only`: the
  field leaves the born Create request, and the generated Update refuses an
  `update_mask` naming it. Before writing anything, birth applies the rendered
  migration to the shadow database inside a rolled-back transaction. An
  expression postgres rejects refuses the birth, naming the marker's file and
  line with postgres's message, and leaves the proto untouched: an unknown
  column, a non-`IMMUTABLE` function such as `now()`, or one generated column
  reading another (both are named). So does a marker with no expression, two
  markers on one field, or a repeated/map/oneof/JSONB field. Supported fields
  are single-valued scalars, enums and Timestamps. The descriptor carries the
  expression too (`SchemaFieldDef.Generated`), so `forge scaffold entity
--from-proto <svc>.<Message>` births the same column as bare `forge
scaffold`. `forge project annotations` lists the marker and the mapping row.
  `pkg/schemadef` gains `OpenShadowAt` and `(*Shadow).TryApply` for asking
  postgres whether SQL would apply without changing the shadow.
- **Version-skew warning for a bridged forge checkout.** When a project's
  go.work (or a directory `replace`) bridges a local forge checkout,
  `forge generate` and `forge lint` print one line — and `forge doctor`
  reports a `Forge Bridge` warning — whenever the running binary was not built
  from that checkout as it is now: a different commit, uncommitted changes on
  either side, or (forge embedded in a host through a workspace, which records
  no forge commit) a checkout that moved after the binary was written. The
  line names both sides and the fix:
  `⚠️  forge skew: reliant (built 2026-10-01 01:31) records no forge commit, and go.work bridges /src/forge, which changed after it was built (now 750833054b78) — generated code and library differ. Fix: rebuild reliant from that checkout, or unbridge: go work edit -dropuse=/src/forge`.
- **A decision table for column write markers** in `db/write-policy`. It picks
  `forge:read-only`, `forge:computed`, `forge:generated`, `forge:guards` or
  `forge:immutable` by who writes the value. It reflects the Update enforcement
  below: guards adds no protection to a read-only column, only the disabled edit
  row naming the rpc that owns it. `forge`, `proto` and the project memory point
  to it.
- **The rule for CRUD vs custom rpcs, stated once and the same everywhere.** An
  rpc is CRUD only when it is unary and named exactly
  `Create|Get|Update|Delete<E>` or `List<Es>`, where `<E>` has a table and a
  same-name proto message. `UpdateJobStatus`, `CreateInvoiceFromEstimate` and
  `ListJobsByCrew` are custom, so there is no need to rename them defensively
  (an agent did in the roofers run). This is in the project memory and in the
  `forge`, `proto` and `api` skills.
- **A retired-claims registry over all shipped guidance**
  (`internal/templates/retired_claims_test.go`). It covers every project and
  service template (skills, `reliant.md.tmpl` and `reliant-reliant.md.tmpl`,
  which render as reliant.md / CLAUDE.md / AGENTS.md / .cursorrules /
  copilot-instructions.md), plus the README and `docs/`. Each row is a claim
  forge retired, with the truth that replaced it, the skill that carries that
  truth (checked to exist), and a bad and a good sample the pattern must catch
  and spare. Against the previous main it reports 16 stale lines.
- **`db/seeds/vocab.yaml` describes timestamps, booleans and NULL.** A time
  column takes instants relative to the day the seed runs:
  `{from: -90d, to: +30d}` (optional `step: 1d`), or a list of `now` / `-3d` /
  `+2w` offsets and absolute ISO dates. A boolean column takes
  `[true, true, false]`, and `null` is a list value for a nullable column
  (`[null, "x"]`). Repeating an entry weights the draw for every kind. A `null`
  on a NOT NULL column refuses the seed and names the column. Before this,
  YAML dropped a null from the list (`[null]` failed with "has no values"), and
  boolean and time columns were ignored with a warning. `seedplan.Config.Now`
  is the anchor: the seed CLI passes the start of the current UTC day. The zero
  value is a fixed instant, so generated mocks, factories and fixtures stay
  byte-stable.
- **`forge db seed apply|reset --dsn` accepts any loopback database**
  (localhost, 127.0.0.0/8, ::1, a unix socket; any port, any name), so a
  throwaway postgres can be seeded. A non-loopback `--dsn` must still be the
  env's declared database unless `--allow-remote-dsn` is passed. The env must
  be dev either way. An ambient `$DATABASE_URL` gets no relaxation, and
  `forge db reset` (DROP DATABASE) keeps the strict check.
- **Context-carried transactions in `pkg/orm`: `RunTx`, `RunTxReadOnly`,
  `RunTxWithOptions` and `AfterCommit`.**
  `s.deps.DB.RunTx(ctx, func(ctx context.Context) error)` runs fn in a
  transaction carried by the ctx it receives. Every query made with that ctx
  against the same database joins it:
  generated delegates (`db.GetJobByID(ctx, s.deps.DB, id)`), stores,
  `pkg/crud.Repo`, `db.Bun()` builders and raw `Exec`/`Query`/`QueryRow`, with
  no handle threaded through signatures. It is SERIALIZABLE by default and
  retries serialization failures and deadlocks (SQLSTATE 40001/40P01) with
  jittered backoff, up to `orm.DefaultTxMaxAttempts` (7). Still conflicting
  after that, it returns `svcerr.Aborted`. A ctx that already carries a
  transaction on the same database is joined, and the inner call's options are
  ignored. `orm.AfterCommit(ctx, fn)` runs fn once, after the outermost commit;
  rolled-back and retried attempts discard it. fn may run more than once, so it
  must have no side effects outside the database. Read-check-write state
  transitions therefore need no `SELECT … FOR UPDATE` lock helpers. The new
  `service-layer/transactions` skill covers RunTx, AfterCommit, a lock-free
  state-transition recipe and migrating lock helpers, and `service-layer`,
  `db`, `db/crud-overrides` and `interactor` point to it. Executor resolution is
  guarded by identity: a transaction carried for one `*sql.DB` never runs a
  statement sent to another.
- **`crud.Preserve(columns...)`, a per-call write policy for a full-replace
  Update.** The generated `db.Update<Entity>` delegate takes
  `opts ...crud.UpdateOption`; existing calls compile unchanged. `Preserve`
  keeps the named columns out of that one write and reads their stored values
  back. Use it when the caller, not the column, decides what may be written.
  `forge:immutable` (`,skipupdate`) remains the answer when no full replace
  should ever rewrite the column. Naming a column that does not exist is an
  error, not a no-op, so a typo cannot silently disable the protection.
- **`crud.UpdateOp.ReadOnly`**: the columns `HandleUpdate` refuses in a client
  `update_mask`. Generated from the entity's `forge:read-only` /
  `forge:computed` fields.
- **`forge lint --quiet` (`-q`).** Prints only what failed — a one-line summary
  per failing linter, its error findings with fix hints, and the verdict as
  the last line; a clean run is one line. Auto-fix still applies, so the
  verdict matches the default run. Combines with any targeted flag
  (`forge lint --read-only-fields --quiet`), with `--scope` and with
  `--gate-json`. Agents were piping full lint through `head` and losing the
  result.
- **`forge lint --scope <path>`** (repeatable or comma-separated). Reports only
  findings in files under the given project-relative paths, so an agent
  sharing a checkout can lint its slice. golangci-lint, the typed-config
  guardrail and the contract linter run on the scope's Go packages only;
  file-anchored linters run project-wide and report what is under the scope
  (a finding with no file is never hidden); the whole-project linters —
  frontend lint, component-drift and `--config-reach` — cannot be scoped, are
  skipped, and are named in the verdict ("run an unscoped `forge lint` before
  merging"). Auto-fix touches only files under the scope. Works with `--json`
  and `--quiet`; refused with positional package paths and with `--gate-json`
  (a gate records whole-project evidence). Every pipeline lane must declare
  its scope mode, pinned by a test.
- **`seedplan.Config.Minimal`** — plan the smallest row the schema accepts
  instead of the fullest: only keys, NOT NULL columns without a usable
  DEFAULT, NOT NULL references, UNIQUE/ordered NOT NULL columns and columns a
  child references are written; discriminated-union and status-guard CHECKs
  take the branch closest to the column DEFAULTs, the same branch on every
  row. `Plan.Writes(table, column)`, `Plan.NaturalValue(...)` and
  `MinimalUnionPlacement` expose the decision; `UnionCell.Present` reports a
  branch's IS NOT NULL requirement. `forge db seed` is unchanged (full plans).
- **Windows support.** Native windows/amd64 and windows/arm64, no C toolchain:
  - process lifecycle (tree kill, liveness, parent/port/start-time lookup,
    reading another process's argv and environment for stale-stack reclaim);
  - an in-process POSIX shell for ShellBuild and migration detection;
  - a delve JSON-RPC client that builds on windows/arm64;
  - `[build.windows]` sections in the scaffolded air configs, `.exe` host
    binaries from `forge build` and the `binary`/`delve` runners;
  - zitadel's Windows binary for the host IdP;
  - in-use file detection via the Restart Manager, so reclaimers work;
  - a directory-junction fallback when symlinks need privileges;
  - portable scaffolded Taskfiles (guarded by a test), and a `.gitattributes`
    keeping generated files LF;
  - `forge doctor` floors for Windows: air >= 1.65.3, Task >= 3.45.5;
  - `forge storage schedule` registers a daily Task Scheduler task;
  - a `windows-latest` CI job that builds, tests and renders a scaffold.
- **`forge doctor` checks air** (any OS, >= 1.63.2) for projects with an
  `.air.toml`: the scaffolded config names its binary with `entrypoint`, which
  older air does not read, so it would have nothing to run.
- **`cli.RenderSkill` — a skill exactly as `skill load` prints it, for
  harnesses.** `cli.LoadSkill` returns the raw body, so a harness injecting a
  skill into an agent's context handed it `forge generate` while
  `reliant forge skill load` (and the project memory) said
  `reliant forge generate` — a command the agent may not have, or a different
  forge build than the one that generated its project. `RenderSkill` applies
  the same resolution, version-skew advisory, audience filter and command-name
  rewrite as the CLI; `skill load` and `forge start` now call the same
  renderer, so the two cannot drift. `RenderSkillOptions.CLIName` defaults to
  the name this process reports, the same one `cli.RenderProjectMemory`
  renders.

### Fixed

- **`schemadef.OpenShadowAt` returns no shadow when it fails.** It used to
  return the live scratch database beside a migration-replay error, so the
  only correct call shape was `defer shadow.Close()` BEFORE checking `err` —
  and a caller writing the ordinary err-check-first form leaked the database.
  It now drops what it opened and returns `nil, err`: check `err`, then defer
  `Close`. (`Close` stays nil-safe, so existing callers keep working.)
- **A pre-rollout Job wait no longer leaves a `kubectl wait` running after it
  returns.** The wait races a `condition=complete` watcher against a
  `condition=failed` one and answers with the first; the loser was only
  signalled, so it could outlive the deploy step that started it. It is now
  killed and reaped before the verdict is returned.
- **A service, entity, field or RPC whose name has a digit in it now builds —
  forge spells every identifier it shares with buf's generators by THEIR
  casing rules.** protoc-gen-go camel-cases a proto name word by word and
  treats a digit as a word, so the letter after it is capitalised; forge
  title-cased only after `_`. A service named `alphav1connect` made forge emit
  `UnimplementedAlphav1connectServiceHandler` where connect-go declares
  `UnimplementedAlphav1ConnectServiceHandler`; an entity `Base64Item`
  (field `base64item`) made the Update op read `req.Base64item` where the
  field is `Base64Item`; and an entity scaffolded as `Oauth2token` broke every
  pb type, RPC method and response field (`pb.CreateOauth2tokenRequest` vs
  `CreateOauth2TokenRequest`). `internal/naming` now carries ports of the
  generators' own rules — `GoCamelCase` (protoc-gen-go's `strs.GoCamelCase`),
  `GoFieldNames` (its per-message rename of a field that collides with a
  generated method or getter: `descriptor` → `Descriptor_`), the connect-go
  names (whose `<Service>Name` constant, alone, is spelled from the RAW proto
  name), and protobuf-es's `protoCamelCase`, method `localName` and `$`
  escapes — and every emitter that names a generated symbol goes through them:
  the CRUD ops, shims and born tests, handler stubs and the stub/shim
  dedupe scans, the mocks, `service.go`, the test clients, the public
  procedure list, `forge scaffold rpc`, the read-only/computed-field lints,
  the `unscoped_auth` audit (which looked a handler up by the proto rpc name,
  so a digit-named authenticated RPC was never inspected), and on the TS side the hooks' `client.<method>` (an RPC `LLMChat` is
  `client.lLMChat`, not `llmChat`) and the pages' and mock transport's entity
  fields (`data?.base64item`, not `base64Item`). The pb message and forge's
  `db.<Entity>` row name a column differently (`sha256sum`: pb `Sha256Sum`,
  db `Sha256sum`; `address_line_2`: pb `AddressLine_2`, db `AddressLine2`), so
  the conversions now spell each side with its own rule; the ORM's rule is
  unchanged (`naming.ColumnGoName`, formerly `ToProtoPascalCase`, which claimed
  to be protoc-gen-go's and was not), so no existing struct field is renamed.
  The same derivation fixes entities with a leading acronym, whose response
  field forge spelled from the entity name (`LLMKey` → field `llm_key` →
  `LlmKey`, not `LLMKey`). `TestGeneratedNames_MatchRealPlugins` builds
  protoc-gen-go and protoc-gen-connect-go at the go.mod versions, runs them on
  a fixture of such names and checks every derived identifier is declared;
  `TestE2EDigitNamedServicesAndEntitiesBuild` scaffolds them end to end
  (build, vet, idempotent regenerate, the born CRUD tests on postgres, `tsc`).
  Not covered: a message named after a TS-reserved identifier (`Object`,
  `Partial`), which protoc-gen-es exports as `Object$`.

- **Two services may declare the same message name without breaking the
  frontend's `tsc`.** Proto keeps message names per package, so alpha and beta
  each declaring `PingRequest` is ordinary — but the generated TS files that
  import from several proto modules into one scope imported each name bare, and
  a name from two modules is TS2300 `Duplicate identifier 'PingRequest'`. The
  project-wide scenario handler map (`src/mocks/scenario-rpcs_gen.ts`) hit this
  as soon as two services shared an RPC shape; `mock-transport_gen.ts` did for
  two entities whose services name a response alike (`GetResponse`); and a
  service's hooks file did when an RPC took a request type from another proto
  file that shared a name with one of its own. A name imported from more than
  one module is now imported `as <module>_<Name>`
  (`services_alpha_v1_alpha_pb_PingRequest`) from each, and the file refers to
  that; a name only one module supplies keeps its own, so files without a
  clash are byte-identical. `mock-transport_gen.ts` also imported an entity's
  fixture module once per service that lists the entity (`import * as
thingsMocks` three times for a `ListThings` in three services); it is now
  imported once and every service's dispatch row is seeded from it.

- **A component named like an identifier `compose.go` itself uses no longer
  breaks the first build.** A service, worker or operator was imported into
  `internal/app` under its package name, beside the names the generated files
  already use — `NewComponents`' locals (`c`, `infra`, `err`), its imports
  (`fmt`, `slog`, `time`, `db`, `ctrl`, …) and package `app`'s own
  declarations — so `forge project new x --service c` failed its first
  generate with `c.New undefined (type *Components has no field or method
New)`, and `fmt`, `slog`, `err`, `infra` failed the same way (`fmt
redeclared in this block`). Such a component is now imported as
  `<role><Name>` (`svcC`, `wkrFmt`), the form two components that share a
  package name already get; every other keeps its package name, so no
  `compose.go` that compiled before changes. The set of names in use is not a
  list forge maintains: it is read off the `compose.go`/`lifecycle.go`
  templates themselves and the project's own `internal/app` files, so a local
  added to a template later is covered the day it is written. A
  `TestGenerateCompose_EveryTemplateIdentifierAsAComponentName` sweep builds a
  project with one service — and one worker — per such name. `main` is
  refused up front by `forge project new --service`, `forge scaffold
service|worker|operator|library`: Go cannot import package main, so the
  project could never build.

- **`forge project rescaffold` re-creates the hosted CI pipeline again.**
  Since new projects are born hosted, the scaffold writes `release.yml` and
  `.github/actions/forge-deploy/action.yml` for the staging and prod it just
  wrote — but rescaffold decides which CI files a project has from a render of
  every env, and those envs import config modules (`config_gen.k`, each env's
  `config.k`) only `forge generate` writes. On a project generate had not
  completed on (a fresh scaffold whose bootstrap generate failed), the envs
  did not render, rescaffold took them for envs that are not hosted, and
  refused both files ("this project declares none"). It also memoized that
  answer and handed it to the generate pipeline it ran next, so the
  pipeline's CI step — after writing the very modules that made the envs
  render — still wrote a `deploy.yml` beside `release.yml` and a
  `build-images.yml` for cluster envs. Rescaffold now writes the env config
  modules first when an env does not render (the new `forge generate --steps
env-config` preset), never hands its answer to the pipeline, and names an
  env that still fails to render (`forge env render <env>`) instead of
  calling it unhosted. `TestRescaffold_EveryScaffoldedFileIsReemittable` had
  been failing on main since #518.

- **`forge project new` no longer warns `frontend config: could not read
deploy/kcl/dev/config.k` — and no longer writes the dev `config.js` from
  proto defaults.** It scaffolds into `<--path>/<name>`, a relative path, and
  generates from there; the frontend-config probe at
  `shop/deploy/kcl/dev/zz_forge_frontend_config_probe_<pid>.k` was handed to
  kpm, which resolves a relative source against the work dir (`shop`) and
  looked for `shop/shop/deploy/...`. The fallback's `API_URL` was the proto
  default, `http://localhost:8080` — the dev IdP's port. `kclrender` now
  resolves the work dir and the source against the process cwd before kpm
  sees either, so every render means one thing by a relative path — the
  backend config probe, `env new --check` and `RunInWorkDir` (which entered a
  relative work dir and resolved it again from inside) had the same defect;
  #520 anchored the frontend probe alone. The two doctor renders that passed
  a work-dir-relative source pass an absolute one.
- **A freshly scaffolded operator renders.** `forge scaffold operator`
  declares the workload before its first CRD (`crds = []`; `forge scaffold
crd` is the next step), and the workload schema required at least one CRD
  of every operator — so every env, dev included, failed to render with a
  schema error naming neither the operator nor the fix. An operator may now
  list none: one that owns no CRD yet, or reconciles built-in kinds only
  (granted by `clusterRBAC`); `crds` is what its derived ClusterRole covers,
  and an empty list derives nothing. Dev renders it on k3d; a hosted env,
  where it is bound `_on_cluster`, refuses only for the undeclared `_cluster`,
  naming the operator. `forge scaffold crd <Kind>` now adds the kind to the
  operator's `crds` in `deploy/kcl/workloads.k` (the derived RBAC reads only
  that list; before, the user had to know to), and prints the edit when the
  list cannot be found unambiguously. Relaxed in `pkg/deploy/v1alpha1`'s
  `WorkloadSpec.Validate` too, so the control plane's admission agrees.
- **A born list page no longer links its rows to a detail page that does not
  exist.** Every list row called `router.push('/<slug>/<id>')`, but the detail
  page is only generated when the service has a Get RPC — so a List-only
  entity's rows were all 404s. Rows link only when the detail page is
  generated (`PageTemplateData.EmitsDetailPage`, which also gates the detail
  route itself and the create page's landing); otherwise they are inert and
  the list keeps the id/created/updated columns it would have left to the
  detail page.

- **`next dev` no longer rewrites the scaffolded `tsconfig.json`.** Next 16
  checks the file on every `next dev` and `next build`. When it finds a value
  missing it rewrites the whole committed file, reformatted. The Next.js
  scaffold shipped `"jsx": "preserve"`, which Next 16 forces to `react-jsx`, and
  lacked the route-type globs Next appends to `include`. So every dev run left
  a diff. The template now ships `"jsx": "react-jsx"` and includes
  `.next/types/**/*.ts`, `.next/dev/types/**/*.ts` and their `.next-prod`
  twins, which `next build` asks for under forge's production `distDir`. A
  dev or build run now leaves the file byte-identical. `tsc` reads the same
  files as before, because `exclude` still drops `.next` and `.next-prod`. The
  Vite SPA template already used `react-jsx`, and Vite never writes its
  tsconfig. Expo only adds `extends`, which the React Native template already
  has. Existing projects can commit the tsconfig Next already rewrote; after
  their next `npm run build`, they also commit the two `.next-prod` globs.
- **`<Resource>` and `DefaultErrorFallback` show `userMessage(error)`.** Both
  printed the raw `error.message` in monospace. A `ConnectError` reached the
  user with its transport framing intact (`[not_found] no such job`). This
  broke the runtime's own rule that errors are displayed through
  `userMessage`. Both now render the framing-free message as body text.
  `<Resource>` falls back to generic copy when the error has no message.
- **Mock mode signs the UI in.** With `MOCK_API` set to `"true"` or
  `"hybrid"`, the scaffolded web auth context (`src/lib/auth/context.tsx`)
  now takes its identity from the fixture session in `session-provider.ts`
  (new export `isMockMode()`) and never sends `GET /auth/session`, so the
  route guard lets every page through. Previously the context asked the
  server in every mode: in pure mock the request failed, the app read
  signed-out, and every page bounced to `/auth/sign-in`. In mock mode, logout
  ends the fixture session locally, and a reload restores it. Hybrid renders
  the same fixture user, but the fixture is presentation only. Its token is
  still handed out in pure mock alone, so a forwarded hybrid RPC carries only
  the real session cookie the browser already holds, and `/auth/sign-in`
  still posts to the real server. With mock mode off, nothing changes. New
  frontends ship `src/lib/auth/context.test.tsx` (Vitest), which pins all
  three modes. `context.tsx` is scaffold-once, so existing projects see the
  change through `forge project upgrade`'s advisory lane.
- **Fixture Lists answer like the backend's generated List.**
  `@reliantlabs/forge-web-runtime/mock-transport` now:

  - filters by equality on every `optional` List request field that is set
    and names an entity field (enums compare by value; implicit-presence
    fields never filter, matching the generated `Filters` closure);
  - applies `search` as a case-insensitive substring match over string fields
    and enum value names;
  - sorts by `order_by` / `descending`, rejecting an unknown column with
    `InvalidArgument`;
  - applies `page_size` (default 50, max 100) with an offset `page_token`;
  - returns `total_count` as the filtered count before paging.

  Previously every List returned every fixture row with `total_count` 0, so
  filtered lists showed everything and dashboard counts read 0. Like the
  backend, an ordered list mints no `next_page_token`. The request schema
  comes from the Connect method descriptor, so `mock-transport_gen.ts` is
  unchanged. Custom (non-CRUD) RPCs are still answered only by scenario
  handlers.

- **Born List requests filter by `optional` fields too.** Quintet completion
  gave a List facet to every bool, enum and `<stem>_id` reference except an
  `optional` one, so the nullable reference was the one FK a born list could
  not filter by (`optional string crew_id` on the roofers `Job`, unassigned
  until dispatch, while `customer_id` and `property_id` got facets). The
  `forge:read-only` marker on it played no part. The facet gate predated FK and
  enum facets and skipped every field with presence, though a facet is
  `optional` on the request either way. An optional reference, enum or bool now
  gets its facet, and a nullable text column counts toward `search`. The
  generated filter is unchanged: `col = $1`, which rows holding NULL never
  match. Matching the NULL rows ("jobs with no crew") is not a facet; add a
  request field and filter in the op. Born once: an existing List request
  gains nothing, so add the field by hand.
- **Generated create/edit forms and list filters follow the field's declared
  rules.** Four defects from the roofers run, in both the Next.js and Vite
  page templates:

  - A field is required only when a rule says so: protovalidate
    `required = true`, `string.min_len >= 1`, or a non-optional foreign key
    (born `NOT NULL REFERENCES` with no DEFAULT). Every other `NOT NULL`
    column used to be born `.min(1, "Required")`, so a `string notes` column
    (`NOT NULL DEFAULT ''`) was mandatory. `max_len`, `email`, `pattern` and
    numeric bounds are still projected as before.
  - An empty input for a proto3 `optional` field submits as unset on both
    forms. Its zod schema maps `""` to `undefined` before validating, so
    `optional int32 year` stores NULL instead of 0, and an empty optional
    email is not rejected by `.email()`. The edit prefill no longer seeds
    the zero, and the update mask still names the field, so clearing a
    stored value writes NULL.
  - The enum zero value (`*_UNSPECIFIED`, found by wire number 0) is never
    offered in a form select or a list filter. The born CHECK rejects it.
    Both forms refuse it in zod. A `NOT NULL` enum's create select starts on
    the first real member, which is the column DEFAULT and what Create stores
    when the field is omitted. An `optional` enum gets a None option.
  - Create navigates to the new record's detail page (`/<entity>/<id>`),
    read from the Create response's entity field. It falls back to the list
    when the response does not carry the entity.

  Scaffolded pages are yours, so existing projects keep their pages. Delete
  a page and run `forge project rescaffold <path>` to pick up the new form.

- **A CRUD op no longer disappears when its shim moves out of
  `handlers_crud.go`.** Generate decided which `crud<Rpc>Op`s to emit by
  file: a method declared anywhere except `handlers_crud.go` counted as
  "implemented by hand" and lost its op. Moving a shim verbatim into a
  sibling file (roofers: `CreatePayment` into `invoice_ops.go`) therefore
  made the next generate drop `crudCreatePaymentOp`, fail its own build
  validation and revert 70 files. Emission now follows what the package's
  code calls, in any file: an op is emitted when no method declares the rpc
  (forge appends a shim) or when anything in the package calls
  `s.crud<Rpc>Op`; a method that calls no op is hand-written wherever it
  lives, so it gets no op and the op's own checks do not apply. Shims are
  appended only for rpcs no file of the package declares, so a moved shim is
  never declared twice. `<entity>ToProto` / `<entity>FromProto` stay emitted
  while any code in the package calls them, even when every CRUD rpc of the
  entity is hand-written. Behavior change: a real implementation written
  inside `handlers_crud.go` (one that no longer calls its op) is now treated
  like one in any other file — no op, no op validation.
- **A failed validate build names the error on its ROOT CAUSE line.** The
  line read `go build failed: exit status 1. Fix: ensure all referenced types
are imported` whatever the error was. It now quotes the first compiler
  error (`file:line:col: message`, plus a count of the rest) and says whether
  that file is hand-written (fix it) or generated (fix its inputs, or report
  a forge bug). A failing generated `_test.go` typecheck quotes its first
  error the same way. The revert is unchanged.
- **`forge env down` never stops the process running it.** forge's ownership
  markers are environment variables, inherited by everything a forge-started
  process runs — an agent server's shells included — so `forge env down <env>`
  or `--all` typed inside one selected the very server hosting it and ended
  the session along with every sibling session. Every stop path (`env down`,
  `env down --all`, the `env up` pre-flight, host-infra shutdown, `debug stop`)
  now walks the command's own parent chain (darwin, linux, the BSDs, Windows)
  and never signals an ancestor or the tree under it, reporting
  `skipped pid 1234 (reliant serve …): it is an ancestor of this command —
stopping it would end the session running you`. Everything else is stopped;
  the per-env form leaves that env's host infrastructure up, records stay so
  `forge env ps` still lists the stack, and `forge env up` refuses (stopping
  nothing) when the stack it would replace hosts it. The tree-kill primitive
  refuses an ancestor too, and skips a group-wide signal to a process group
  the command belongs to.
- **A custom rpc whose name starts with a CRUD verb is custom on the frontend
  too.** The server has always required a CRUD rpc's entity to have a table and
  a wire message. The hook and page generators split the name on the verb alone.
  So `useUpdateJobStatus` invalidated a `jobStatus` query scope that no query is
  keyed under, and the Job list and detail it had just changed stayed cached
  until a hard refresh. `CreateInvoiceFromEstimate` left the invoice list stale
  in the same way, and `ListJobsByCrew` produced a `jobs-by-crews` CRUD page,
  nav entry and mock fixture. Both generators now apply the server's message
  check (`crudEntity`). A custom mutation invalidates every query on its service,
  and a custom List births no page. A descriptor written before the deep message
  inventory existed keeps the old name-only answer. Regenerate to pick it up.
- **`forge:guards` on a `forge:read-only` column renders its disabled row.** The
  edit page dropped read-only fields before the guard pass looked, so with both
  markers the column vanished from the page and nothing named the rpc that owns
  it. Now read-only alone still hides the field, and read-only plus guards
  renders the disabled "changed through `<Rpc>`" row. That combination is right
  for a lifecycle column: the API refuses the write, and the page says who can
  make it.
- **The project memory no longer contradicts the code.** `reliant.md.tmpl` is
  loaded into every session, and it described thin handlers in front of an
  `internal/<svc>/contract.go` business layer, with an `errors.go`
  `connect.Code` switch, that `forge scaffold service` has never written and
  `forgeconv-no-handler-error-mapping` flags. It also said `auth_required`
  "gates nothing at runtime", although the interceptor enforces it fail-closed.
  It now shows what scaffold writes: logic on the `*Service` rpc methods in
  `internal/handlers/<svc>/`, with `forge scaffold package` for logic that
  needs isolation, `svcerr.Wrap` and `RunTx`. The same corrections apply to the
  `api`, `db`, `services`, `proto-split`, `migration`, `migration-service`,
  `service-layer`, `architecture` and `api/role-interface` skills (stale
  `contract.go`-beside-handlers and `<svc>_mock.go` paths), to
  `docs/getting-started.md`, and to the scaffolded `service.go` comment, which
  said Deps were resolved by name (they are resolved by type).
  `internal/app/compose.go` ownership was already consistent on main (yours,
  reconciled). A harness with an older embedded forge can still show "forge-owned
  and regenerated", and the retired-claims registry keeps that phrasing out.
- **`forge db` commands read the `-C` project, not the CWD.** `--dir` defaulted
  to a relative `db/migrations` computed before `-C` was parsed, so
  `forge db migrate up -C <proj>` from `/tmp` failed with
  `open /tmp/db/migrations/.`. `forge db seed apply -C <proj>` read migrations,
  `vocab.yaml` and `db/seeds/custom/` from the CWD, then printed
  `Seeded 0 row(s) across 0 table(s)` and exited 0. Every db subcommand
  (migrate, migration new/rebase, seed, reset, squash) now resolves the
  migrations directory, a relative `--dir` and the seed overlays against the
  project root. A missing migrations directory is an error naming the path.
- **Seeding nothing is an error when there was something to seed.** `seed
apply`/`seed reset`/`db reset` fail when the database has tables the
  migrations read do not define (the wrong `-C`/`--dir`), when
  `database.seed.tables` names no real table, or when the plan inserted nothing
  into tables that are still empty. `database.seed.tables: []`, an
  already-seeded database and a schema with no tables still succeed.
- **Undescribed seeded timestamps sit in the four weeks before today**, not
  January 2024.
- **The mock-fixture freshness test names both causes.** Editing only
  `db/seeds/vocab.yaml` fails `fixture-freshness_gen.test.ts`, which is
  correct, but the message said the schema had changed. It now lists the schema
  (`db/migrations`) and the seed vocabulary (`db/seeds/vocab.yaml`), and says to
  run `forge generate` (`reliant forge generate` inside reliant) from the
  project root.
- **ORM writes return the values the database computed.** `pkg/crud.Repo`'s
  Create, Upsert, Update and UpdateMasked now `RETURNING` every
  `GENERATED ALWAYS AS (…) STORED` column into the entity. Create and Upsert
  also return each column the insert left to its DB `DEFAULT` (a nil pointer,
  or a zero `,nullzero` / `,default:` field), along with a server-allocated
  PK. The generated delegates and stores inherit this. Previously a generated
  column came back as the Go zero after Create and stale after an update, and
  on a serial-PK table the explicit `RETURNING id` also suppressed Bun's own
  read-back of defaulted columns. So every caller re-read the row
  (`line_total_cents`, `total_cents`, `balance_cents` in the roofers run).
  The RETURNING list names only the struct's own columns, never `*`, so
  inserts keep working while a migration that adds a column has been applied
  ahead of the code. A write that matches no row is still `orm.ErrNoRows`.
- **A client Update no longer writes `forge:read-only` / `forge:computed`
  columns.** Both markers promise "readable, not client-writable", and the born
  Create request keeps that promise. The AIP-134 `Update<Entity>Request` wraps
  the whole entity, though, and the generated CRUD Update wrote it all back. Any
  authenticated caller could set a server-owned column directly (an invoice
  straight to PAID, past the RPC that owns the state machine), and a maskless
  Update from a client that simply didn't echo the field reset it: an unset enum
  became the column DEFAULT, a lifecycle timestamp became NULL, a computed total
  became 0. Nothing errored and nothing was logged. An `update_mask` naming the
  column was accepted as well. The generated Update op now carries the entity's
  read-only columns as `op.ReadOnly`. `crud.HandleUpdate` refuses a mask path
  naming one with `InvalidArgument`, reason `unknown_field`, and the full
  replace passes `crud.Preserve(...)`, so the stored value survives whatever the
  request carried. Your own code is unaffected: `db.Update<Entity>Masked`
  naming the column (what a custom RPC calls), `Create`, and a plain
  `db.Update<Entity>` all still write it. The policy is read from the proto on
  every `forge generate`, the same reading that already shapes the Create
  request and the edit form, so existing projects are fixed by regenerating, with
  no migration. Hand-written overrides that reloaded the stored row and copied
  the editable fields onto it, or filtered lifecycle paths out of the mask, can
  go. AIP-203 asks servers to ignore output-only mask paths; forge refuses them
  instead, because a 200 that changed nothing is a silent non-write (an override
  wanting AIP behaviour sets `op.ReadOnly = nil`). A value derived in an Update
  `op.Entity` hook is no longer persisted by the generated Update. Derive it
  where its inputs change, with `db.Update<Entity>Masked` naming it, or as a
  GENERATED column.
- **Update responses report the stored row, not the request.** Beyond the
  generated columns above, `Repo.Update` and `Repo.UpdateMasked` now read back
  (`RETURNING`) every column the write did not set, in the same round trip. The
  entity a caller holds afterwards, and so the CRUD response, carries the stored
  value of a held-back (`forge:immutable`, read-only, secret) column and of every
  column a mask did not name, rather than whatever the request carried. Before,
  a full replace that correctly left `status` alone still answered with the
  reset `status` the client sent.
- **A freshly scaffolded project no longer warns `gen-missing-source` on
  forge's own files.** Three emitters hand-wrote a header that stopped at the
  forge-owned banner: the ORM projection (`internal/db/<entity>_orm_gen.go`),
  the service mocks (`internal/handlers/mocks/<svc>_mock_gen.go`) and
  `db/embed_gen.go`. Each now stamps a `// Source:` line naming what it is
  derived from. Nothing held emitters to the rule `forge lint` holds every
  `_gen` file to, so `internal/tierguard` now runs that exact rule
  (`scaffolds.LintGeneratedHeader`) over every Tier-1 file in its rendered
  fixtures. Existing projects pick the lines up on the next `forge generate`;
  the finding on a forge-generated file now says that, instead of telling the
  user to edit a file marked DO NOT EDIT.
- **`forge lint --read-only-fields --json` and `--guarded-fields --json` run
  their one lane.** Both were missing from the targeted JSON table and fell
  through to the whole suite.
- **`forge lint --conventions` reports proto paths from the project root**
  (`proto/services/x/v1/x.proto`, not `services/x/v1/x.proto`), so they are
  openable and `--scope` can match them.
- **forge only kills processes it can prove are its own.** On every OS, a pid
  read from a pidfile (embedded postgres, the host zitadel, a recorded dlv)
  is killed only if its executable matches and it started no later than the
  pidfile was written — a recycled pid is left alone. On Windows, where pids
  recycle fast and a parent pid is never updated, process-tree ownership and
  teardown also require the parent to be alive and older than the child, and
  a graceful stop (CTRL_BREAK) is sent only to a verified process-group
  leader, never broadcast to the whole console.
- **Background `forge env up` stacks on Windows survive closing the
  terminal** (DETACHED_PROCESS), as they do on Unix.
- **`deploy/static-site` skill: forge is for static sites too.** Agents
  reaching for forge only when there is a backend routed every landing page,
  marketing site and docs site around it, even though forge already ships one
  end to end: a static export bound to `forge.OnHosted {}` is published to the
  control plane's static hosting (bucket, CDN and hostname owned by the
  platform), with envs, build-once releases and promotion. The skill covers when
  forge is the right call for a site, the static frontend scaffold, a complete
  frontends-only hosted `prod`, `runtime_config`, and the release flow, all
  verified against a real project. The root `forge` skill, `deploy`,
  `frontend` and the README now route to it. The scaffold gaps it works around
  are tracked in #401.

### Changed

- **`forge project new` with frontends and no services leads with the
  frontend.** The next-steps block told a frontend-only project to
  `forge scaffold service item`, which is how a static site ends up with a CRUD
  backend nobody asked for. It now points at the frontend and
  `deploy/static-site`, and keeps `scaffold service` as the line for a project
  that does need an API. It does not print a bare `forge env up dev`, because a
  frontend-only scaffold's dev env still fails that way until #401 lands.
- **The README stopped advertising `forge.External`.** It was removed with the
  one workload model (#284), but the README still offered it for Fly.io, Cloud
  Run, ECS, Vercel and Railway, and described deploy as `forge.Service` plus a
  `host` block. Both now describe the per-workload runtimes, and a
  platform-CLI runtime is tracked in #400.

- **`forge env status` is the ONE read view of an environment.** Reading an env
  used to take six verbs — `env status`, `env verify`, `env wait`,
  `env rollout`, `env topology`, `env history` — and they were views of a
  single question, split by which half of the answer each happened to own. A
  reader had to know, before they could ask, that the bound release lived in
  `verify`, the rollout phase in `rollout`, the runtime ports in `status`, and
  the promotion that caused all of it in `history`. Two were literal
  duplicates: `env rollout` WAS `env wait --timeout 0`.

  They are modes of one command now:

  ```bash
  forge env status                  # every environment, and how far behind each is
  forge env status prod             # prod right now: runtime AND release
  forge env status prod --wait      # block until the rollout settles
  forge env status prod --history   # prod's promotion ledger, newest first
  ```

  One env's report carries the bound release, verify (running digests vs the
  binding, in all five states, through the control plane's observer for a
  hosted env), the rollout phase per workload, runtime health, gates, and
  ledger freshness. The two halves are deliberately asymmetric about failure:
  runtime health REPORTS (a down stack is a state this command must be able to
  print), while the release half makes a claim and so owns the exit code.

  **Every exit code is unchanged**, because every mode reaches the same code
  the old verb called: 0/1/2 for the read, plus 5 (timed out, still
  progressing) and 6 (superseded) under `--wait`. `--timeout` serves two modes
  with different budgets — 60s for a cluster read, 15m for a wait — so its
  declared default is an unset sentinel resolved per mode; `--wait --timeout 0`
  still means one read, never blocking.

  `--json` emits one document carrying the standard envelope. Its release
  fields stay FLAT (`bound`, `images`, `release`) because `forge gate record
--from` recognises the document by them.

### Removed

- **BREAKING: `forge env verify|wait|rollout|topology|history` are deleted**,
  with no alias and no hidden name — they are modes of `forge env status` (see
  Changed, above). `forge release where` and `forge release verify` are
  untouched: both answer a question about a RELEASE rather than an
  environment. A removalguard entry pins all five spellings on every surface.

- **BREAKING: `forge env promote` is gone — `forge env deploy <env> [vX | --from
<src-env>]` does the whole job.** No alias and no hidden name; the old
  spelling dies on "unknown command".

  ```bash
  forge env deploy prod v1.4.0          # was: forge env promote v1.4.0 --to prod && forge env deploy prod
  forge env deploy prod --from staging  # exactly what staging runs
  forge env deploy prod                 # unchanged: re-apply prod's current binding
  ```

  **The verb now means record + apply + wait, and the wait is not a flag.**
  Recording a binding ships nothing, so a pipeline step that only promoted
  reported success before any byte had moved and the release's real failure
  surfaced minutes later with nothing connecting the two. Every pipeline
  therefore spelled it `promote --deploy --wait`, and the spellings that
  omitted either half were bugs waiting for an incident. So the health gate is
  ON by default; `--no-wait` opts out and names
  `forge env status <env> --wait` as the way to gate later.

  **Who applies it is read off the environment's ledger, not a flag** — the
  same declarative fact as where the promotion is recorded, so the two cannot
  disagree. An env whose KCL declares `forge.ControlPlane` is converged by
  that control plane and forge waits on the rollout it computes, pinned to the
  promotion just written. Every other env is self-managed: the same command
  renders the new binding and applies it from this machine, and that apply's
  per-resource rollout wait IS the health gate.

  `promote --wait` and `promote --deploy` are removed with the verb rather than
  renamed: both are now what the command does.

  Everything else is unchanged and moved with the code — the compare-and-set
  (`--expect-current` / `--expect-unbound` / `--supersede`), `--gate`,
  `--from` / `--from-promotion`, `--plan`, the run flags, every exit code and
  the JSON envelope. A deploy that names no version keeps today's spec-change
  behaviour, and a release-only flag passed without a version is now refused
  rather than silently ignored.

- **BREAKING: an environment no longer has an image registry — a WORKLOAD has
  one, as part of its image.** `forge.ClusterTarget.registry`,
  `forge.ControlPlane.registry` and `forge.DockerBuild.registry` are all gone.
  The author writes the full reference on the workload and forge contributes
  only what it alone knows:

  ```kcl
  # deploy/kcl/workloads.k
  api = fw.Workload {
      name = "api"
      image = "ghcr.io/acme/api"        # the registry IS the image
      build = forge.GoBuild {cmd = "./cmd/acme", output_name = "acme"}
  }
  ```

  forge appends the resolved tag and, after a push, the digest. It never
  invents, prefixes or rewrites the host. So the reference you read in the KCL
  is the reference the build pushes and the cluster pulls — they cannot
  disagree, which is the whole point: a build aimed at one registry while the
  spec named another was an `ErrImagePull` that surfaced only at rollout.

  Two workloads in one env may now name two different registries, which an
  env-wide field could not express at all.

  **Migration is automatic.** `forge generate` removes `registry = …` from every
  ClusterTarget / ControlPlane and prefixes the removed value onto a workload's
  bare `image`, judging ambiguity ONLY over the envs where that workload is
  bound to a runtime that pulls an image (cluster or hosted). A registry on an
  env where the workload runs `OnHost`, or is not bound at all, is simply
  dropped — it was never that workload's. If the same workload genuinely pulls
  from two different registries, forge REFUSES and prints the exact per-env KCL
  rather than picking one. Your binder expressions are never edited.

  **A bare image is refused at render**, naming the workload: it would render as
  `api:<tag>`, which the kubelet resolves against Docker Hub and fails to pull.
  A third-party image must name its host too (`docker.io/library/nats:2.10`).

  A hosted env is no different, and gets its own runbook: a hosted env may one
  day inherit a default from the control plane's advertised image push base, but
  that base is not pushable yet (control-plane ADR 0003 / #334), so forge does
  not invent one. Hosted FRONTENDS follow the same rule with the same field
  name (`forge.Frontend.image`), and forge appends the platform's own
  `static.v1` layout segment itself, so that segment never appears in your KCL.

  A local env states the image IT runs, because a k3d node can only pull from a
  host-local registry. That is a plain KCL string on the workload — the
  scaffolded dev env declares one `_LOCAL_IMAGE` constant and its `_on_k3d`
  binder sets it. forge ships no helper for composing an image reference and
  prescribes no pattern: a project that wants one source of truth writes its
  own constant, which is what KCL is for.

  **`forge registry login <env>` logs in to every distinct host the env's images
  name** — no host argument and no flag that takes one. The credential is the
  only input: `--username` with `--password-stdin`, or `--password-env` naming
  the variable that holds it (the `forge.ControlPlane.token_env` convention, so
  CI states a NAME in git and never the value). `forge registry ref <env>` now
  prints one `<image>@<digest>` per built image, labelled by the workload that
  declared it.

  **RELEASES MUST BE RE-CUT.** The release ledger now keys an OCI artifact by
  its declared repository, host included, so the key IS the address and
  `release.Artifact.URI` is no longer recorded for images. Ledgers cut before
  this change are keyed by bare name, which no longer resolves to an address:
  `forge release verify` reports such an entry as UNVERIFIABLE rather than
  guessing Docker Hub. Re-cut and re-promote any release you still rely on.
  Keying by bare name could not survive per-workload registries — two envs
  building the same app to two registries collapsed onto one entry, so a prod
  promotion could read a digest that only ever existed in the dev registry.

  Also removed: `BuildState.Registry` and `buildtarget.State.Registry` (Image
  now holds the full repository), `forge env new`'s `REPLACE_ME_REGISTRY` knob
  (an env has no registry to get wrong), and the `registry` field the render
  projected onto each cluster runtime.

- **BREAKING: a `forge.ShellBuild` `cmd` is plain KCL, run verbatim — the
  `${TOKEN}` substitution is gone.** Forge used to rewrite a fixed vocabulary
  into the command before running it — `${IMAGE}`, `${TAG}`, `${CODE_VERSION}`,
  `${SERVICE}`, `${TARGETARCH}`, `${REGISTRY}`, `${PROJECT_DIR}`, `${ENV}` and
  `${BUILD_CWD}`. It no
  longer substitutes anything and exports no variables of its own: the rendered
  string is handed to `sh -c` byte-for-byte, from the declared `cwd`, with the
  declared `env` merged onto the process environment. Every `$VAR` in a command
  is now the shell's, so `$PWD` and `$HOME` work.

  Write the values in KCL, where the command is composed and the value already
  lives: `forge.target_arch()` (new, and the reason `target_arch` is now a
  reserved render input), `forge.image_tag("<env>")`, `forge.env()`,
  `file.workdir()`, the image reference itself, or a literal. To keep a
  `${NAME}` spelling, declare `NAME` in that build's `env` map and the shell
  resolves it.

  `forge lint` (`shellbuild-tokens`) and `forge generate` both REFUSE a leftover
  token. That is a gate rather than a warning because one of them fails
  silently: `GOARCH=${TARGETARCH}` reaching the shell unset becomes `GOARCH=`,
  which Go reads as "unset" and builds for the host — so an amd64 cluster gets
  an arm64 binary whose only symptom is an `exec format error` crash-loop. The
  rest (`docker push /:`) fail loudly.

  Forge now resolves the build's tag BEFORE the render and binds it as the
  existing `image_tag` input, so a rendered `cmd` already names the tag the build
  records and pushes (`--release` > `--tag` > the image's own pin > the env's
  `image_tag` > `git describe`, unchanged). Two casualties of the old pass are
  also gone: `forge project audit`'s `conflict_tokens` / `conflict_count` (with no
  built-in tokens there is nothing for a declared env key to collide with) and
  `forge doctor`'s substituted-command preview, which now prints the exact string
  forge will run instead of one built from `<registry>`/`<tag>` stand-ins.

  Escaping, worth knowing before you write one: braced-dollar is KCL's own
  interpolation, so backslash-escape a shell `${HOME}` or use a raw `r"..."`
  string. A DOUBLED dollar is not an escape — KCL reads it as an undefined name
  and refuses to compile.

- **BREAKING: every rollback surface is gone — recovery is roll forward.**
  `forge env deploy --rollback`, `forge env promote --rollback`, the KCL
  `forge.External.rollback_cmd` field, and every provider's rollback path
  (`kubectl rollout undo`, compose override pinning, the static-site
  re-sync, the hosted `DeployService/Rollback` call) are removed, along with
  the deploy's skip-the-migration-on-a-rollback machinery. A rollback claims to
  undo a release it cannot undo: the release already ran its migrations and
  wrote data. Fix forward instead — cut a release and promote it. Binding an
  env to an OLDER release is still an ordinary `forge env promote`; the plan
  labels it `direction BEHIND` and warns that it undoes nothing. Existing
  ledgers keep working: a `"kind":"rollback"` entry reads as the promote of
  its release. Delete `rollback_cmd` from any `forge.External` block.

### Added

- **Scaffolded CI for hosted environments** (ADR `docs/adr/env-verbs.md`,
  task V6). A project with at least one hosted env (an env whose KCL declares
  `forge.ControlPlane` and something the platform runs) now gets two new
  files. The first is `.github/workflows/release.yml`. On a `v*` tag it runs
  checks once (`forge lint --gate-json`, plus `task test -- -json` fed to
  `forge ci verify-test-run --gate-json`). It then builds, pushes and records
  the release in ONE command (`forge env build <env> --release "$VERSION"`;
  `--release` implies `--push`). Finally it deploys the release through the
  hosted envs in promotion order, one job per env, each under its GitHub
  Environment. Each stage after the first deploys `--from` the stage before
  it, pinned with `--from-promotion`. It asserts `--expect-current`, which the
  previous job captured before this stage's approval wait. A hotfix that lands
  while an approval is pending therefore turns the run red (exit 3) instead of
  being overwritten. The second file is
  `.github/actions/forge-deploy/action.yml`, a composite action vendored into
  the repo so that it versions with the project's forge pin. It is one
  `forge env deploy` — which records, applies and waits for health itself — and
  then records that outcome as rollout evidence on the promotion the deploy
  wrote and summarizes it into the job summary. Because the verb decides who
  applies from what the env DECLARES, the action carries no promote step, no
  "does this env converge its own promotions" probe, no separate wait and no
  `deploy:` input.
  Existing projects get both via
  `forge project rescaffold .github/workflows/release.yml`.
  Neither file uses curl or environment ids, and no token appears in a URL.
  The only secret is `FORGE_CONTROL_PLANE_TOKEN`.
- **`forge cloud token create|list|revoke`** mints and manages the org
  automation token that CI deploys with. An org token has no acting user, so
  it outlives any one person. Scopes are validated before the round trip.
  `--json` carries the one-time secret, so
  `… --json | jq -r .secret | gh secret set FORGE_CONTROL_PLANE_TOKEN` never
  echoes it to a terminal.
- **`forge ci summarize <doc.json>...`** renders forge `--json` documents as
  Markdown for `$GITHUB_STEP_SUMMARY`. It renders the verdict from the
  envelope, a field table and row tables. It decides nothing: it exits 0
  whatever the documents say, and 1 only when an input cannot be read.
- **`forge env deploy` applies a MIXED env's cluster half.** An env that keeps
  its ledger on a control plane AND declares workloads that control plane does
  not run — a cluster Deployment, a compose service, host infra, a shipped
  frontend — now gets both halves of the deploy: the client-side apply for what
  forge owns, then the wait on the rollout the control plane computes. Before,
  the follow-through branched on "is this env hosted" alone, so a mixed env
  recorded its promotion and waited on the hosted rollout while its cluster
  workloads silently stayed on the previous release — a green deploy that
  shipped half a release. The apply runs first and a failure short-circuits the
  wait. Nothing is configured: the shape comes from the env's own render.
- **Status verbs for hosted deploys** (hosted-deploy-primitives §3.5):
  - `forge env rollout <env>`: where the current (or `--promotion`)
    rollout has got to, read once. It is `forge env wait --timeout 0`, with
    the same exit codes (0 succeeded, 1 degraded, 2 undetermined, 5 still
    rolling out, 6 superseded).
  - `forge env history <env> [--limit] [--before] [--release]`: the
    promotion ledger, newest first, with from-env, actor, note, evidence
    counts and the CI run. It pages with a keyset cursor (`next_before`;
    empty means the last page), and both ledger backends page identically.
  - `forge release where <version>`: which environments are currently bound
    to a release. Exit 1 when none are.
  - **`forge env verify` works for hosted envs.** forge cannot read a hosted
    env's cluster, so it reads the control plane's observer through
    GetRollout and reports the same five states with
    `source: control-plane observer`. A stale observation is UNREACHABLE
    (exit 2), never MATCH. Cluster envs are unchanged.
  - `forge env topology --json` gains `promotion_id`, `from_env`,
    `gates_summary` and, for hosted envs, `rollout_phase`.
- **`forge ci run <run-id> [--env <env>] [--json]`**: one CI run's timeline
  from the hosted control plane. It shows the release cut, the recorded
  checks, every promotion and each promotion's rollout, with a verdict.
  Exit codes: 0 every stage passed, 1 a stage failed or errored, 5 still
  running, 2 could not read the control plane. A run id nothing has written
  under exits 5, never 0. `--env` picks which control plane to ask;
  without it, the project's single declared control plane is used. The
  design spec named this `forge run show`; that would have collided with the
  dev-server `forge run`, which takes its own positional arguments.
- **`forge env verify` refuses to judge against a stale file ledger.** A
  self-managed env's promotions live in `.forge/promotions/<env>.jsonl`,
  committed to git, so a checkout that has not pulled the latest release
  record compares the cluster against an OLDER promotion. A fine deploy then
  reads as DRIFT, and a deploy that never happened can read as MATCH. verify
  now compares the env's log with origin's default branch (as of the last
  fetch; it never fetches). When this copy is `behind` or `diverged`, every
  image is still reported, but the command exits **2** (could not determine)
  with the fix. `ahead` (recorded here, not yet merged) is shown, not failed.
  `--json` carries `ledger: {state, ref, local_entries, upstream_entries,
detail}`. A control-plane ledger has no copy to be behind and is unaffected.
- **`forge env promote` always compare-and-sets.** The write asserts that the
  env is still on the promotion the plan read, and is refused if someone else
  moved it since. A hotfix that lands while a pipeline waits for approval now
  turns that pipeline red (exit 3, `promotion_conflict`) instead of being
  overwritten. No flag is needed. `--expect-current <id>` replaces the planned
  value with one captured earlier; `--expect-current unbound` /
  `--expect-unbound` asserts the env was never promoted. Re-promoting the
  release an env already runs is still a no-op whatever the expectation says,
  so a retried success is never a conflict. `--supersede` admits a promote
  while the current rollout is in flight and is recorded on the new entry.
  Exit 4 is `rollout_in_flight` / `environment_pinned`. `--plan --json` now
  shows `current.promotion_id` (the value to capture) and `expected`; a
  refused `--json` document carries `applied: false` and
  `refusal: {reason, expected…, actual_current, actual_phase}`, and its
  `ok`/`exit_code` match the process status. The file ledger applies the same
  check against the history it reads. Hosted CAS needs a control plane with
  the P0 contract — an older one ignores the fields.
- `forge env promote --run-id / --run-url / --no-run`: the promotion records
  the CI run, defaulted from `GITHUB_*` / `CI_PIPELINE_*`. `--wait`,
  `--deploy`, `--timeout`, `--fail-fast`, `--gate`, `--from` and
  `--from-promotion` are declared and refuse with "not supported by this
  forge build" until they are wired.
- `service_account_annotations: {str:str}` on `forge.K8sOverrides`,
  `forge.RenderedWorkload` and `forge.workloads.Workload`: annotations stamped
  on the ServiceAccount forge GENERATES for the workload, and on no other
  object. It is how a workload gets cloud workload identity (GKE
  `iam.gke.io/gcp-service-account`, EKS `eks.amazonaws.com/role-arn`) without
  renaming its identity — the SA keeps the workload's name, so Roles, bindings
  and policy that name it keep working. Keys are validated as Kubernetes
  annotation keys at render time, and combining it with `service_account`
  (where forge generates no SA) is refused rather than silently dropped.
- `forge env render` now includes every declared `forge.HelmChart`'s objects.
  They used to be missing entirely (flux, cert-manager and envoy-gateway each
  rendered 0 objects while `env deploy` applied them), so render was not a
  preview of deploy. Charts are templated through the deploy's own chart
  render (`helm template` with the declared values, the pinned CRD bundle,
  the chart's own CRDs, the post-hook drop), each document is marked
  `# source: helm chart <name>` and attributed to the cluster deploy applies it
  to, and `--target` selects charts the same way deploy does. Templating needs
  `helm` and registry access; `--no-charts` skips it, and the summary names the
  charts it left out.
- `cluster:manage` access-token scope (`accesstoken.ScopeClusterManage`):
  registers, rotates and removes an org's BYO clusters. Its own authority —
  `deploy:write` neither implies it nor is implied by it.

### Changed

- **Scaffolded CI: the build-once release pipeline owns hosted envs, and the
  broken pieces are gone.**
  - `deploy.yml` no longer rebuilds per env for a hosted env. If it did,
    staging and prod would run different digests, and `release.yml` builds
    once instead. A mixed env, one with both hosted and cluster parts, also
    moves wholly to `release.yml`. Its ledger is the control plane, so once
    it has been promoted, `forge env deploy` pins both halves to the
    release. A `deploy.yml` rebuild of its cluster half would then deploy
    the old digests and still report success. A mixed stage writes the
    kubeconfig, because `forge env deploy` applies that half from CI itself.
    When every env is hosted, `deploy.yml` is not emitted at all.
  - `build-images.yml`'s opt-in `cut-release` job is deleted. It cut and
    promoted by raw curl, read `DEPLOY_TOKEN` instead of
    `FORGE_CONTROL_PLANE_TOKEN`, and was broken against the server because
    it sent no artifact kind or mode.
  - `reconcile.yml` runs `forge env status <env> --json`. It used to call
    `forge reconcile`, which was never a verb, and its matrix expressions
    were missing their `$`.
  - Every rendered workflow is now checked by `actionlint`, when it is on
    PATH.
- **BREAKING (one require line): `github.com/reliant-labs/forge/pkg` is no
  longer a module.** forge is now a single module,
  `github.com/reliant-labs/forge`, carrying both the CLI and the `pkg/*`
  runtime libraries. **Import paths did not change** — a module at
  `github.com/reliant-labs/forge` with a `pkg/testkit` directory serves
  `github.com/reliant-labs/forge/pkg/testkit`, byte-identical to what the
  submodule served. Consumers change one require line and no source:

  ```sh
  go get github.com/reliant-labs/forge@vX.Y.Z
  go mod edit -droprequire=github.com/reliant-labs/forge/pkg
  go mod tidy
  ```

  Dropping the retired requirement is not optional, and it does not degrade
  gracefully: both modules can serve `forge/pkg/*` import paths, so a graph
  holding both answers every such import with `ambiguous import: found package
... in multiple modules`. `forge generate` now detects this before codegen
  and distinguishes a requirement you own from one a dependency drags in.
  A dependency's must be fixed in that dependency — a `replace` only hides it.

  The two modules were always released in lockstep, at the same commit, with
  the same version. Keeping that true took three hand-maintained syncs, and
  two failed in production: the root module's `require forge/pkg` went stale
  and made `go install .../cmd/forge@main` uninstallable, and a hand-listed
  registry of "forge/pkg symbols the generator emits" went stale when
  `testkit.StubNotConfigured` was added, so `forge generate` rewrote a project
  tree and then failed its own validate. One module makes both
  unrepresentable.

- **A dev build no longer pins a version it is not.** The scaffold's forge
  requirement comes from the binary's own build info: a release tag, or the
  pseudo-version `go install ...@main` records. A local or dirty build — whose
  bytes no module proxy can serve — now pins **nothing** and requires a
  `go.work` source bridge, which `forge generate` names in its refusal. It
  used to fall back to a hand-maintained "last published tag" constant,
  telling projects to require a release that could not satisfy the code being
  generated.

- **`forge generate`'s compatibility gate is a version comparison.** The
  project's forge must be `>=` the generating binary's. This replaces a probe
  that compiled a throwaway program against a hand-listed symbol set; the
  inequality covers every symbol ever added, cannot rot, and costs one
  `go list` instead of a `go build`. A project resolving a NEWER forge still
  passes — that is the ordinary upgrade order.

- **`task release:forge` is one tag, one commit.** The two-tag atomic push, the
  `go.sum` bare-clone resolution dance, and the `defaultPublishedForgePkgVersion`
  bump all existed only because the root module required an unpushed submodule
  version. `scripts/release-pkg.sh` and `task release:pkg` are removed.

### Fixed

- **The host disk reserve no longer refuses builds on a CI runner.** v0.1.43's
  admission check refused every build lane below a fixed 20 GiB of free disk,
  and a stock GitHub runner starts with ~14 GiB — so `forge env build` failed
  on a runner before compiling anything, with a remedy (`forge storage gc`)
  that reclaims caches a fresh runner does not have:

  ```
  host disk /tmp/... has 2.3 GiB free, below the 20 GiB reserve; run
  'forge storage status' and 'forge storage gc --apply' before building
  ```

  On a CI runner (`CI` / `GITHUB_ACTIONS`) a measured shortfall is now a
  warning and the build proceeds; an unreadable disk or unloadable policy
  still refuses. `FORGE_STORAGE_ENFORCE_RESERVE=1` keeps the refusal for a
  persistent self-hosted runner. Developer machines are unchanged.

- **A store-backed Secret now reaches EVERY cluster that consumes it.** A
  `FileSecrets` (or pulled local hosted) provider projects the Secrets its
  cluster workloads declare by `secret_ref` — but forge applied that
  projection once, into the env's primary kubectl context. A multi-cluster
  dev env whose second cluster also reads the Secret got nothing there, so the
  pod stalled on CreateContainerConfigError. control-plane worked around it by
  declaring the Secret a `forge.ExternalSecret` (which then blocked every
  fresh worktree's deploy preflight) and creating + mirroring it with a shell
  script, whose randomly generated HMACs then disagreed with the store values
  forge projected into the first cluster. The projection is now placed per
  consuming (cluster, namespace), with exactly the keys that cluster's
  workloads declare — the same trust boundary `RenderedSecrets` already keeps.
  A single-cluster env renders and applies exactly what it did before.

- **The dev databases exist before the first cluster workload is applied.**
  `ensureDevDatabase` already creates every declared dev DSN — including one
  an in-cluster workload reaches through `host.k3d.internal` — but it ran in
  `forge env up`'s host phase, which starts only after the cluster rollout
  converges. A cluster workload dialing a per-worktree database therefore
  crash-looped on `database "…" does not exist`, the rollout never converged,
  and the step that would have created the database never ran. The deploy
  dispatch now ensures the dev databases at the infra→cluster boundary — after
  compose / host-infra have brought the server up, before any cluster group is
  applied — so `forge env up dev` and an applying `forge env deploy dev` both
  start a fresh stack with no manual `CREATE DATABASE`. Dry runs create
  nothing; non-dev envs are untouched (the same fail-closed dev classifier).

- **`forge env up` from a linked git worktree no longer takes over and
  recreates compose infrastructure another checkout is running.** Compose names
  a project after its directory's basename and resolves relative bind mounts
  against that directory. Two worktrees laid out as `<container>/<repo>` share
  one basename, so they selected the SAME project with DIFFERENT mount sources:
  compose saw a changed config and recreated every shared container, which
  dropped every other stack's connections and left the containers mounting
  files inside that worktree.

  - New `forge.OnCompose {shared = True}`: the stack is machine infrastructure
    every worktree uses, so forge drives it from the repo's PRIMARY checkout
    (`--project-directory`, with `file` and `env_file` resolved there) from
    whichever worktree runs. An `up` from any checkout is then a no-op once the
    stack is running. A stack a worktree had already taken over is converged
    back to the primary on the next run, with one recreate.
  - New `fp.write_file(path, content, shared=True)`: writes into the primary
    checkout, so every stack's render updates the ONE copy of a file the
    shared stack mounts, instead of N diverging per-checkout copies.
  - Every compose deploy now checks container ownership first. A non-shared
    deploy whose containers carry another LIVE checkout's
    `com.docker.compose.project.working_dir` is refused before pull/up, with
    both fixes named. Containers from a checkout that no longer exists are
    adopted.

- **A cert-manager `Certificate` in the bundle now SUPPLIES the Secret it
  materialises, so the deploy preflight stops false-failing on it.** The
  render-time secret back-propagation gate recognised only `kind: Secret`
  documents as in-stream supply, so a workload mounting a Secret that a
  `Certificate` in the SAME bundle creates was reported as an undeclared
  mount. `forge env deploy e2e` refused to run on the vendored
  cloudnative-pg barman-cloud plugin, whose Certificates and the Deployment
  mounting `barman-cloud-{client,server}-tls` render in one stream, and the
  only way past it was `--skip-preflight` — which disables the image, CRD and
  live-Secret checks too.

  cert-manager writes the Secret when it reconciles the Certificate, in the
  same apply pass that creates the pods mounting it, exactly as
  `kubectl apply` orders a rendered `kind: Secret` ahead of its consumers.
  Demanding it exist beforehand made a first deploy of any cert-manager-backed workload
  unsatisfiable by construction: the thing that provisions the Secret was the
  very deploy being blocked. The advice the gate printed — declare a
  `KubeconfigSecret` or an `ExternalSecret` — was actively wrong for a
  cert-manager Secret, whose bytes cert-manager owns and would overwrite.

  Matching is on the `cert-manager.io` API group (any version), so a bundle
  on `v1alpha2` or a future `v2` is recognised without a forge change, and an
  unrelated CRD that happens to be named `Certificate` is not. A Certificate
  supplies only the one name in its `spec.secretName`; every other undeclared
  mount in the same bundle still fails.

- **The frontend bundle and image tests run in the e2e lane, on a minimal
  fixture.** The `next build` peer-pin test scaffolded a whole frontend
  (OpenTelemetry web instrumentation included) inside the unit lane's
  `go test -race ./...`, where the 7GB runner OOM-killed it (`signal: killed`
  after up to 1029s) and failed the Test job on unrelated PRs. It and the
  static-image `docker build` test now live in `internal/cli` under
  `-tags e2e`, on the e2e shards' own runners, where a missing tool is a
  failure rather than a skip. The pin test installs only what its claim
  needs (33 packages; ~35s locally for both of its builds) and still proves
  both halves: directory pins fail with `No QueryClient set`, and forge's
  post-install reconcile makes the build prerender. Each node process is
  capped at `--max-old-space-size=2048`. The e2e shard now verifies a docker
  daemon answers before the suite runs.
- **`forge tools install` never mutates a frontend's `package.json` or
  lockfile.** It used to run `npm install --save-dev @bufbuild/protoc-gen-es`
  in every frontend under `--force` — which the scaffolded verify-generated
  job passes — and in any frontend without `node_modules` yet. On a Linux
  runner npm re-saved a macOS-written `package-lock.json` without its `libc`
  entries, and `forge ci verify-generated` reported that user-owned file as
  generated-code drift; committing either OS's lockfile moved the failure to
  the other OS. The plugin is now installed only by the frontend's own
  `npm ci`, from its declared devDependency. `forge tools install` checks
  that every frontend whose buf.gen.yaml runs the local plugin declares it,
  and fails with the edit to make when one does not. `--force` reinstalls the
  Go tools only.
- **`forge ci verify-generated` no longer blames `forge generate` for a file
  an earlier step changed.** It refuses to regenerate over an
  already-modified tree and lists those paths as changed before
  `forge generate` ran, instead of reporting them as out-of-date generated
  code.
- **A Next.js frontend's tsconfig peer pins no longer split packages in the
  bundle.** The pins named package DIRECTORIES, and Next's webpack resolver
  applies tsconfig `paths` to app code: wherever a pin resolved (an ordinary
  `npm ci` in the frontend, or a Docker build at `/app`), app code bundled
  `@tanstack/react-query`'s `build/legacy` while
  `@reliantlabs/forge-web-runtime` bundled `build/modern`. Two module instances
  are two React contexts, and prerender failed with `No QueryClient set, use
QueryClientProvider to set one`. Every pin now names the package's
  declaration file (e.g. `…/@tanstack/react-query/build/modern/index.d.ts`,
  `react` → `@types/react/index.d.ts`), derived from the INSTALLED manifest,
  never hardcoded, because the entry moves inside the declared ranges
  (`@opentelemetry/sdk-trace-base` 2.0 → 2.11). Next's resolver skips a `.d.ts`
  target and Vite ignores `paths`, so bundles resolve through `exports`, while
  tsc still binds one copy of the types — which the pins remain necessary for.
  `forge project new` and `forge scaffold frontend` write them after their
  install; `forge generate` heals an existing project's directory pins
  wherever they resolve, once, and never rewrites a declaration pin.
- **The Next.js frontend Dockerfile follows `frontends[].output`.** It always
  copied `.next-prod/standalone`, which only `output: standalone` produces, so
  a `static` frontend's image (and a `server` one's) failed with
  `COPY .next-prod/standalone: not found` — and CI builds an image for every
  frontend. `static` now serves `out/` from `nginxinc/nginx-unprivileged`
  (uid 101, port 8080, mounted under `base_path`), `server` runs `next start`,
  and `standalone` keeps the node server. The build runs at the frontend's
  repository path (`/src/frontends/<name>`) instead of `/app`, installs with
  `npm ci`, and the node runners drop the bundled npm/corepack trees, which
  carried every HIGH CVE in `node:22-alpine`. The scaffold ships a
  frontend-level `.dockerignore`, since the build context is the frontend
  directory. The Dockerfile is scaffold-once: `forge project upgrade --check`
  reports the new one to existing projects.

### Removed

- `database.migration_safety.down_files_allowed_until` and the
  `forge project upgrade` step that stamped it. Every down migration is now a
  `no-down-migration` lint error, with no grandfathered history: forge never
  runs one, so deleting it is always safe. A forge.yaml still carrying the key
  loads with a warning naming the fix.
- `go.work` from forge's own repo. It existed to stitch `pkg` to the root
  module, and it was also what hid the stale-require bug: every in-repo build
  resolved `./pkg` locally and stayed green while `@main` was uninstallable.
- `buildinfo.PkgVersion`/`SetPkgVersion`, `PkgModuleVersion` (already dead),
  `Build.PkgPath` and the `forge/pkg` line in `forge version` — a second
  version axis with nothing left to describe.
- `docs/pkg-versioning.md`, replaced by `docs/versioning.md`.

## [0.1.11] - 2026-09-01

Patch release. The root CLI (`v0.1.11`) and the runtime library
(`pkg/v0.1.11`) are tagged as always — on adjacent commits, `pkg` on the
release commit and the root tag one commit later on the require bump.

This release bounds the parallel-dev-stack port allocator, teaches the
block registry what kind of key it is holding, and repairs two k3d
failures that presented as something other than what they were.

### Added

- **`dev_stack:` in `forge.yaml`** — `max_stacks` (default 8: the primary
  checkout plus seven worktrees) and `block_size` (default 100, the
  historical hardcoded quantum). The parallel-stack ceiling was
  previously stated twice and tied together nowhere: the allocator was
  unbounded while a project pre-maps host ports at cluster-CREATE time,
  so a worktree past the last mapped block allocated cleanly and then
  failed later as "gateway unreachable". `AllocateBlock` now refuses a
  NEW block past the ceiling with the remedy in the error. An EXISTING
  entry always resolves regardless — an issued block is baked into k3d
  mappings and the dev IdP's `iss` claim and redirect URIs, so lowering
  the ceiling must never move a port already handed out.
- **`forge env devstack prune`** reclaims blocks whose git worktree no
  longer exists, which is what keeps the now-finite block range from
  filling with leaked entries. Dry-run by default. It never touches a
  plain port-block key or the default key, and if enumerating live
  worktrees fails for any reason it reclaims nothing.
- **`fp.dev_stacks()` KCL builtin** — the roster of registered dev-stack
  keys, so a module generating one config block per running stack never
  parses `.forge/blocks.json` itself. It returns EMPTY on a read-only
  render, which keeps `forge generate` a pure function of committed
  inputs; otherwise a generated tracked file would capture whichever
  worktrees happened to exist on that machine.
- **`forgeconv-allocate-port-spacing`** — two `allocate_port` base
  literals congruent modulo `block_size` collide at the block equal to
  their separation. The rule fires only when that block is reachable at
  the project's configured `max_stacks`, which keeps it actionable
  rather than flagging every latent pair.

### Fixed

- **Rendering or deploying a cloud env no longer claims a local port
  block.** `forge env render` / `forge env deploy` armed the persistent
  `fp.allocate_port` block registry for every env, so rendering prod from a
  linked worktree registered a new `prod-<worktree>` block in the primary
  checkout's `.forge/blocks.json`. That leaked one permanent block per
  throwaway worktree, went unseen by `--fail-on-write` because the registry
  sits outside the worktree, and failed the render outright once the registry
  reached `dev_stack.max_stacks`. These commands now claim a block only when
  the env's declaration runs on this machine: a k3d cluster, a host, compose
  or host-infra service, or a workload on a local context. Otherwise
  `allocate_port` returns its base port and nothing is written. `forge env up`
  is unchanged, including `forge env up prod --target <frontend>`.
- **A stale k3d serverlb upstream no longer reads as a flaky cluster.**
  k3d's serverlb is nginx configured with the server node's NAME and no
  `resolver` directive, so it resolves each upstream once at startup and
  caches it for the process lifetime. When the node later gets a
  different Docker IP, nginx keeps proxying to the cached address; with
  `max_fails=1 fail_timeout=10s` the symptom is not a clean outage but
  ~10-second "connection refused" windows that recover on their own.
  Forge now compares container start times and refreshes the load
  balancer before the API probe and after `k3d cluster start`.
- **A declared `api_port` is no longer dropped** for clusters that also
  name a k3d config file. `k3d cluster create --config` accepts no
  per-flag settings, so the value parsed, validated, and then did
  nothing while k3d picked a random host port. Where the config file
  already sets `kubeAPI.hostPort`, the file wins if they agree and forge
  refuses if they disagree.
- **The cluster health probe reports why it failed.** It previously
  collapsed every distinguishable failure into a bare false, after which
  forge printed "the API is still starting" — a diagnosis it had never
  established.
- **A node sampled moments after restart is no longer misreported as
  dead.** k3d's entrypoint runs its own setup before it execs k3s, so
  "no k3s process" must now persist for a grace period before counting
  as exited.
- **The block registry distinguishes a dev-stack key from a plain
  port-block key.** A bare `{key: block}` map could not, so a generator
  reading it raw emitted a dev NATS account for a prod web port into a
  tracked config file. The legacy bare-int form is still accepted.
- **A frontend `npm install` is retried once during
  `forge project new`.** A single transient registry failure previously
  degraded into a scaffold with no `node_modules`, discovered much later
  as some other tool reporting a package it could not find.

## [0.1.10] - 2026-08-28

Patch release. The root CLI (`v0.1.10`) and the runtime library
(`pkg/v0.1.10`) are tagged as always — on adjacent commits, `pkg` on the
release commit and the root tag one commit later on the require bump.

v0.1.9 made a deploy fail when a workload never became ready. This
release fixes the two ways that new strictness, and `forge generate`,
could report a failure that had not happened.

### Fixed

- **A successful deploy is no longer failed by waiting on one-shot Jobs
  that were never applied.** forge names a one-shot Job by its spec hash,
  but the rollout wait unioned the manifest-derived (hashed) names with a
  caller-supplied list that derived the UNHASHED entity names. Those names
  matched no object in the namespace, `kubectl wait` errored on each, and
  v0.1.9's policy — correctly, for a Job that genuinely did not run —
  turned that into a failed deploy. A real prod deploy applied 201
  objects, rolled all 13 Deployments, completed both one-shot Jobs, and
  was still reported as failed. The wait set is now every `kind: Job` in
  the stream the apply just sent and nothing else: authoritative by
  construction, already filtered by `--target` and scoped to the right
  cluster. A Job that was applied and then FAILED still fails the deploy,
  and a Job that failed to APPLY is still caught by the kind-agnostic
  apply-completeness check.

- **`forge generate` no longer leaks machine-local state into tracked
  files, and `forge lint` no longer fights the generator over `gen/`.**
  Evaluating a KCL module fires every `file.write` in it, bypassing
  forge's write chokepoint, so a generate on a clean checkout could emit
  bytes derived from a gitignored registry — two developers on the same
  commit produced different output. Generate-path renders now revert any
  tracked file that went clean → dirty across them.

## [0.1.9] - 2026-08-28

Patch release. The root CLI (`v0.1.9`) and the runtime library
(`pkg/v0.1.9`) are tagged as always — on adjacent commits, `pkg` on the
release commit and the root tag one commit later on the require bump.

The theme is deploys that report the truth. `forge env deploy` could
previously exit 0 over an environment that never came up, in two
independent ways: a workload that never became ready was a warning, and
a kubectl apply that confirmed fewer objects than were rendered went
unnoticed entirely.

### Added

- **`RolloutPolicy`: a deploy now fails when a workload never becomes
  ready.** `forge env deploy` printed a warning and exited 0 when a
  Deployment or a one-shot Job failed to roll out, so a prod deploy
  reported success over a broken environment — the failure surfaced
  later, somewhere else, looking like a different bug. Readiness is now
  part of what "deployed" means.

  Four flags govern it: `--rollout wait|warn|skip` (the previous
  exit-0-on-failure behavior is still reachable as `warn`, and `skip`
  opts out), `--rollout-timeout` (default raised from 60s to 5m, since
  60s failed honest-but-slow rollouts), `--rollout-fail-fast`, and
  `--rollout-order`.

  A failed Job now fails the deploy rather than being ignored, and it
  fails as soon as Kubernetes says so: both the `complete` and the
  `failed` conditions are watched, so a Job that has already failed no
  longer burns the full timeout before reporting it.

- **`forge env up` resolves GitSource frontends.** Only build and deploy
  did, so a frontend declared with a cross-repo `source:` dev-served by
  running npm in the project root — against the wrong tree, or against
  no frontend at all. `up` now resolves the source like its siblings.

### Fixed

- **A deploy fails when kubectl confirms fewer objects than were
  rendered.** The apply step trusted a zero exit code and never compared
  what came back against what went in. In the v1.5.6 incident a prod
  deploy applied 103 of 105 rendered objects and reported success; the
  two missing objects were found by hand, much later. The count is now
  checked, and a short apply is a failed deploy.

### Changed

- The toolchain moves to go 1.27, and the govulncheck pin rises to
  v1.7.0. These are one change, not two: v1.1.4 vendors an `x/tools`
  that panics under 1.27, so the old pin cannot scan the new toolchain.
- CI caches each Go module separately, rather than treating the
  multi-module repo as one cache key.
- Scaffold guidance now tells the reader to FIX forge when forge is what
  stands in the way, instead of working around it. A workaround plus a
  TODO is the outcome that rule exists to prevent — it buries the signal
  that tells us what to fix.

## [0.1.8] - 2026-08-27

Patch release. The root CLI (`v0.1.8`) and the runtime library
(`pkg/v0.1.8`) are tagged together, as always.

Everything under [Unreleased] below ships in this release: `forge env
render`, the cluster-health and object-collision doctor checks, the
`forge.dev/env` ownership stamp on every rendered object, seedplan tuple
and union support, and the testreport package.

## [Unreleased]

### Added

- **`forge env render <env>` — read what an environment renders, without
  deploying it.** There was no supported way to ask "what objects does this
  environment own". `kcl run` fails on a real project (it needs forge's
  `kcl_plugin.forge` harness), `forge doctor` renders every environment and
  reports a count, and `forge env deploy --dry-run` is the deploy command:
  it resolves a kubectl context, runs the declared-cluster guard, and refuses
  outright when the recorded build is behind HEAD. So the answer got
  reconstructed by hand. One audit read 1,886 lines of KCL and inferred each
  workload's target lexically — HOST from the presence of a `host =` block,
  cluster from a `k8s = K8sOverrides{cluster = ...}` override a thousand
  lines further down. One misread there deletes the wrong object out of the
  wrong cluster.

  The command prints the manifests a deploy would apply as a `---`-separated
  YAML stream, each document preceded by a `# cluster:` comment naming where
  it lands — a comment, so the stream still pipes into `kubectl diff -f -`.
  Attribution matters because an environment renders ONE stream and may
  deploy it to several clusters (control-plane's dev: most workloads on
  k3d-control-plane, workspace-proxy on k3d-cp-daemon), and it is not
  modelled a second time here: each document is handed to
  `internal/cluster.ScopeManifestsToGroup`, the function that performs the
  routing at deploy time, once per cluster. A document replicated to every
  cluster (an unattributed Namespace) is printed once with both named, so
  the object count equals the render's own — `forge doctor`'s 341 across
  control-plane's five environments is 30 + 71 + 58 + 76 + 106. `--cluster`
  narrows to exactly one cluster's stream; `--list` gives the inventory view;
  `--kind` / `--name` / `--target` narrow further; a non-zero exit carries
  the KCL error, so it works as a CI gate.

  **It does not claim to be pure, because it cannot be.** KCL evaluates
  `file.write` during rendering, so a project whose deploy KCL generates a
  file writes it every time anything renders — control-plane's dev main.k
  calls `nats.write_conf`, and `forge doctor` has been rewriting
  `deploy/nats/nats.conf` on every run all along. forge has no hook to
  suppress a project's own writes: the write happens inside the embedded KCL
  runtime, which has no read-only mode, and imposing one from outside
  (rendering from a copy of the tree, sandboxing the process) is neither
  portable nor cheap. So the command reports instead of promising: it stats
  the project tree before and after and names every file that changed, and
  `--fail-on-write` turns that into a non-zero exit for callers that need
  the guarantee. What forge CAN suppress it does — its own resolve_port
  store write is reverted, content and mtime, so it never shows up in the
  report as forge blaming the render for forge.

- **`forge ci verify-test-run` — a green suite that ran almost nothing is now
  visible.** A Go suite that skips its way through exits 0 and reads as green.
  Measured on one real package, one environment variable apart: 9 pass / 124
  skip without `DATABASE_URL`, 103 pass / 1 skip with it — same exit code, 7%
  of the tests, and nothing in the pipeline had a word to say about it.

  The new command reads the run's own `go test -json` output — from a pipe or
  `--from a-file` — and reports the packages whose pass therefore proves
  nothing. It runs no tests: `forge test` was removed precisely because a
  second spelling of the project's suite reports on a different suite than the
  one the project runs, so this reads the record of the run that already
  happened. On a 176-package, 482 MB stream it takes 2.5 seconds.

  Two rules, because skips are legitimate and a gate that fires on every skip
  gets switched off: `zero-evidence` (every test in the package skipped — no
  sample-size floor, "none of them ran" is unambiguous at any size) and
  `mass-skip` (more than `--max-skip-ratio`, default 0.5, of at least
  `--min-tests`, default 5). Healthy packages are never listed. Across the 112
  tested packages of the reference project in its broken environment the pair
  reports five, and the ratios above the line are 1.00 / 0.95 / 0.80 / 0.60 /
  0.56 against a next-highest of 0.20 — the threshold sits in an empty band,
  not through the middle of a crowd.

  Genuinely-expected heavy skipping is declared once in forge.yaml under
  `ci.test_skips.allow`, with a REQUIRED `reason` — the same contract as
  `forge project disown <path> --reason`. A declaration that stops suppressing
  anything is reported as no longer needed rather than left to rot; one that
  matches no package in the run is silent, so a scoped `go test ./internal/x/`
  does not drown in irrelevant notices.

  Three states, not two: input carrying no `go test -json` events, or a stream
  that ends mid-run, is UNDETERMINED and exits non-zero — the command never
  reports a clean run it could not read, and `--warn-only` does not downgrade
  that. Failures in the stream also fail it, because
  `go test -json ./... | forge ci verify-test-run` in a shell without
  `set -o pipefail` reports only the last command's status.

### Changed

- **Scaffolded services run pprof by default.** `pprof_addr` shipped with
  no default, so a scaffolded binary declared `PPROF_ADDR` on the deploy
  side and started no listener — the env var was projected onto every
  workload and read by nobody, and `forge env status` reported
  `? pprof — could not resolve a pprof address` about it. A gateway then
  sat at ~1 GB of anonymous memory being OOMKilled with no way to ask the
  process WHAT it was holding: a cgroup's `memory.stat` says how much and
  never what, and the answer only exists while the process is still alive.

  The default is now `127.0.0.1:6060`, and loopback is what makes always-on
  safe: the listener exists in every environment and is routable from none
  of them. It stays on its own serverkit listener (never the public port,
  whose endpoints sit behind a k8s Service), and the scaffolded manifests
  give it no Service, route, or container port — reach it with
  `kubectl port-forward <pod> 6060:6060` and then
  `go tool pprof http://localhost:6060/debug/pprof/heap`. The scaffolded
  `docker-compose.yml` overrides it to `0.0.0.0:6060`, the one place a
  profile has to cross a container boundary (Alloy scrapes it for
  Pyroscope). `PPROF_ADDR` / `--pprof-addr` moves it; `""` switches it off.

- **A pprof bind failure can no longer take a service down.** `serverkit.Run`
  routed it through `FailurePolicy`, so with the default `FailProcess` a busy
  debug port terminated the process. That was defensible while pprof was
  opt-in; with it on by default the ordinary cause is banal — a second copy
  of the binary on the same dev box — and a debug surface that can kill the
  service is worse than no debug surface. Run now logs it and serves on,
  under either policy. `FailurePolicy` continues to govern supervised
  components (workers, operators) unchanged.

## [0.1.0] - 2026-08-17

First minor release. The root CLI (`v0.1.0`) and the runtime library
(`pkg/v0.1.0`) are tagged together, as always.

### Added

- **The generated scaffold test row is falsifiable.** Every scaffolded
  handler ships a self-destructing test row that asserts the RPC is not
  implemented yet, so it goes red the moment you write the handler.
  Keyed on a bare `connect.CodeUnimplemented`, that row could never
  fail: a FINISHED handler can answer the same code for its own reasons
  — a forwarder, a feature-flagged path, or most commonly a nil-guard on
  an optional dep the test harness leaves unset — so the row passed
  forever against implemented RPCs. Observed in one project: 78 of 78
  integration rows green, none of them asserting anything.

  `svcerr.ErrScaffoldStub` / `svcerr.ScaffoldStub(rpc)` is a sentinel
  only forge's own untouched stub can produce, and `tdd.Case` gains
  `WantScaffoldStub` (backed by the exported `tdd.AssertScaffoldStub`)
  to match it. Replace the stub and the row fails, whatever the
  replacement returns. It is deliberately NOT matched by
  `svcerr.Unimplemented`, which remains the right answer for an RPC that
  is unimplemented on purpose.

  Identification is dual because the tiers differ: in process the error
  chain is intact and `errors.Is` matches, while through a real Connect
  client the error is marshalled and rebuilt, so only the
  `svcerr.ReasonScaffoldStub` metadata survives. Both are accepted, or
  unit rows and integration rows would mean different things under one
  field name.

  **Existing projects need no migration.** Stub excision still
  recognises the older `Unimplemented` and `CodeUnimplemented`
  spellings, so stubs already on disk stay excisable. Generated stubs no
  longer import `fmt` (the message is composed from the RPC name), and
  the AST import-fixer no longer adds it.

- **npm license gate for the published web-runtime.**
  `scripts/check-npm-licenses.sh` is the npm twin of `check-licenses.sh`
  — same allowlist-not-blocklist bar, same "an unrecognized license
  fails rather than passing quietly", same per-package exceptions that
  must state a reason. `web-runtime` is published to npm, so a
  non-permissive dependency would propagate into every project that
  installs it. The strong-copyleft family, LGPL included, is not
  exemptable through the allowlist. web-runtime passes with zero
  production dependencies.
- **Entity protos are dead: SQL is the schema language.** `forge scaffold
entity bookmark url:string title:string tags:[]string done:bool`
  emits the create-table migration (`db/migrations/NNNNN_create_*.sql`
  — `TEXT PRIMARY KEY CHECK (id <> '')`, NOT NULL + defaults, native
  arrays, `created_at`/`updated_at TIMESTAMPTZ DEFAULT (now())`;
  `--soft-delete`, `--no-timestamps`, `--no-rpcs`) and scaffolds the
  CRUD wire contract into the service proto once. `forge generate`
  shadow-applies `db/migrations` to a real ephemeral Postgres, introspects, and
  projects entity structs + ORM (`internal/db/<entity>_orm.go`, plain
  Go types: `time.Time`, pointers for nullable, native slices), CRUD
  wiring with generated `<entity>ToProto`/`<entity>FromProto`
  conversions, and frontend pages/nav/mocks — all from the APPLIED
  schema. Behavior is read off real columns: `deleted_at` ⇒ soft
  delete, `created_at`+`updated_at` ⇒ managed timestamps, text columns
  ⇒ search filter span.

### Removed

- **`deploy/kcl/components_gen.json`. KCL is the sole source of truth for
  deploy.** The generated component inventory is gone — the file, the
  `codegen.GenerateComponentsJSON` / `ComponentsToJSON` /
  `ComponentsJSONRelPath` Go API that wrote it, and the
  `fc.load_components` / `fc.load_migrate` / `fc.COMPONENTS_GEN` KCL
  loaders that read it. No alias, no shim, no fallback.

  What replaces it is `deploy/kcl/components.k`: one typed literal per
  component (`fc.Server {name = "billing", build = {...}}`), **scaffolded
  once and owned by the project from then on.** `forge scaffold
service|worker|binary|operator` APPENDS an entry; nothing forge does
  ever rewrites what is already there.

  Why: a file forge regenerated every run could not hold the per-env
  customization KCL exists for — bringing in components forge never
  generated (NATS, a cache, a sidecar), running a containerized database
  in dev against a hosted one in prod, changing a port per environment.
  It was also gitignored, so a FRESH CLONE rendered **zero manifests,
  silently**, until someone ran `forge generate`. Now every deploy file
  is tracked and hand-editable, and `kcl run deploy/kcl/dev` works on a
  clean checkout with nothing generated first.

  Two axes, each owned by one place: `components.k` says WHAT the system
  is made of (env-neutral — no ports, no replicas); `deploy/kcl/<env>/main.k`
  says HOW it runs in that environment (ports, replicas, resources,
  registry, secrets, env-only components).

  The deploy-time migration step moved with it: `migrate` is now stated
  literally per env (`migrate = ["/app/<project>", "db", "migrate", "up"]`)
  instead of being derived, so an environment that migrates out of band
  sets `migrate = []` and one that migrates differently says so.

  Forge still NOTICES drift — a component in the code with no entry in
  `components.k` — but it REPORTS it (`forge lint`, with the exact stanza
  to paste) rather than rewriting the file. Both directions have
  legitimate deliberate cases, so neither fails a build.

  **Migration:** add a `deploy/kcl/components.k` declaring your
  components, replace `for c in fc.load_components(fc.COMPONENTS_GEN)`
  with `for c in comps.COMPONENTS` (plus `import ..components as comps`)
  in each env's `main.k`, replace `fc.load_migrate(fc.COMPONENTS_GEN)`
  with the literal argv, and delete the gitignore entry. `forge generate`
  scaffolds `components.k` for you if it is absent.

- **The per-component port.** `config.ComponentConfig.Ports`,
  `config.PortSpec`, `config.HTTPPortName`, `ComponentConfig.PrimaryPort()`
  and the `ports` key in `deploy/kcl/components_gen.json` are gone — no
  alias, no shim. A component never had a port to carry: every service in
  a binary mounts onto the SAME Connect mux and the process listens once,
  on `AppConfig.port` (env `PORT`, default 8080 — now
  `config.DefaultServePort`). Any other port is a DEPLOY fact, declared
  per environment on `forge.components.Component.ports` in
  `deploy/kcl/<env>/main.k`, which is unchanged and still the place to
  state one. The rendered manifests and the `forge build` / `forge env`
  JSON contract are byte-identical across the change: nothing ever
  populated the field, so `components_gen.json` shipped `"ports": []` for
  every component and every reader hit its fallback.
- The `Port` column in the generated `docs/generated/architecture.md`
  component table. It printed `0` for every component in every project.
- The `(forge.v1.entity)` / `(forge.v1.field)` annotation handling,
  `protoc-gen-forge mode=orm` (`*.pb.orm.go`), `proto/db` scaffolding,
  proto-derived migrations (`--from-proto`, the boilerplate entity
  migration), `forge db proto`/`forge db codegen`, the
  `proto_migration_alignment` audit category, the
  `proto-orm-out-of-sync` lint, and the `internal/db/types.go` proto
  alias file. The annotation definitions remain in
  `forge/v1/forge.proto` as deprecated tombstones (ignored, with a
  generate-time notice) so existing projects keep compiling; their
  migrations were already the de-facto schema truth — see the
  `proto-entities-to-schema-truth` migration skill.
- Initial project scaffold generated by `forge project new`.
- `forge/pkg/serverkit`: extracted the uniform `cmd/server.go` lifecycle
  (HTTP listener, observability chain, healthz/readyz, worker supervisor,
  operator manager, graceful shutdown) into a reusable library. The
  generated `cmd/server.go` is now a ~50-line shim that projects the
  project's typed `*config.Config` onto `serverkit.Config` and wires
  per-project `Hooks` (Bootstrap, PostBootstrap, AutoMigrate, SetupOTel,
  ProjectInterceptors, CORS/SecurityHeaders/RequestID middleware factories).
  Cuts ~470 lines from every project's generated output.

### Changed

- **One custom RPC, one handler file.** The generate pipeline now
  scaffolds each unimplemented custom RPC into its own
  `internal/handlers/<svc>/rpc_<snake_name>.go` — package clause,
  imports, one `*Service` method, its `// forge:gen unwired-stub
symbol=<pkg>.<Method>` marker — instead of piling every RPC into one
  shared `handlers.go`. This is the same filename `forge scaffold rpc`
  has always written for an RPC that is not in the proto yet, so the two
  paths agree; previously the layout depended on whether you scaffolded
  the RPC before or after declaring it in the proto. A handler package's
  file layout is invisible to Go and to every forge reader (all of them
  walk the directory), so it is chosen for the one thing it does decide:
  who has to merge with whom. Two authors implementing two RPCs of the
  same service now have nothing to split. This matches the scaffold
  TESTS, which have been one file per RPC
  (`handlers_scaffold_<rpc>_test.go`) since the same reasoning was
  applied to them. When CRUD gen excises an RPC's stub because the RPC
  became entity-backed, the emptied `rpc_<name>.go` is removed with it
  rather than left as a bare `package x` husk. **Existing projects need
  no migration:** forge only ever writes a stub for an RPC it finds
  unimplemented, so a `handlers.go` already holding your handlers is
  never read, rewritten, or re-stubbed — it keeps working exactly as it
  is, and only NEW RPCs land in per-RPC files. The `forgeconv-handler-
file-size` lint now names `rpc_<name>.go` in its remediation instead of
  a third spelling.
- **Scaffolding is one verb: `forge scaffold`.** Arity picks the
  granularity — bare `forge scaffold` births every `// forge:entity`-marked
  message the protos imply and then projects them; `forge scaffold <noun>
…` scaffolds exactly one thing (`entity`, `service`, `worker`,
  `operator`, `crd`, `binary`, `frontend`, `scenario`, `webhook`,
  `package`, `adapter`, `library`, `handler-file`, `rpc`). This replaces
  `forge project add <noun>` and `forge project scaffold`, which were the
  same operation spelled as unrelated commands. There is no alias and no
  deprecation shim: the old spellings exit non-zero with `unknown
command`. `forge project` keeps the commands that act on the project as
  a whole — `new`, `delete`, `disown`, `migrate`, `upgrade`, `map`,
  `graph`, `introspect`, `features`, `annotations`, `audit`.
- `pkg/app/app_gen.go`: `RESTHandler` is now a method (`func (a *App)
RESTHandler() http.Handler`) backed by an unexported `restHandler`
  field. Required by the `serverkit.Application` interface contract;
  the renamed accessor preserves the public read path
  (`app.RESTHandler()` instead of `app.RESTHandler`).
- `pkg/app/bootstrap.go`: `WorkerList()` returns `[]serverkit.Worker`
  (was `[]*WorkerInstance`); `OperatorList()` returns
  `[]serverkit.Operator` (was `[]*OperatorInstance`). The wrapper types
  unchanged — they satisfy the interfaces directly.
- `pkg/app/bootstrap.go`: `RunOperators` signature gains
  `healthProbeAddr string` so projects that bind a controller-runtime
  probe listener can forward `serverkit.Config.OperatorHealthProbeAddr`.

### Migration

- See the new `v0.x-to-serverkit` migration skill for the upgrade path,
  including the manual edits required for projects whose
  `pkg/app/bootstrap.go` is forge-forked (e.g. cp-forge).

### Deprecated

### Removed

### Fixed

- `forge env up --target <name>` did cluster work for targets that have
  no cluster deployment edge. Target selection filtered the manifest
  stream, which is too late: cluster creation and cross-cluster
  kubeconfig minting run before render/apply, so targeting a host
  service still created a k3d cluster and the deploy pipeline reached
  its empty-manifest fallback. Phase requirements are now derived from
  the rendered placement graph — host, build-only and dev-served
  frontend targets skip both phases; compose and external targets deploy
  without a cluster; cluster services, operators, platform charts and
  cluster frontends require both. Infra pre-warm remains independent, so
  host processes can still depend on compose services when nothing
  selected runs in Kubernetes. The build-plan summary now reflects the
  filtered set rather than announcing work that will not happen.
- `forge scaffold frontend` baked `DEV_API_URL = "http://localhost:0"` into
  the new frontend's `src/lib/apiurl_gen.ts` whenever the project already
  had a server component — it read the always-zero per-component port and
  overwrote its own 8080 default with it. Non-mock dev then pointed
  `connect.ts` at port 0 until the next `forge generate` happened to
  rewrite the file.
- `forge project audit`'s ingress category could never emit its
  "declared but no route" finding: it gated on a port that was always 0,
  so it reported `0 service(s) without route` for every project. It now
  reports every SERVER with no route (workers/crons/operators/binaries get
  no k8s Service, so nothing can route to them).

### Security

[Unreleased]: https://github.com/reliant-labs/forge/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/reliant-labs/forge/compare/v0.0.8...v0.1.0
