# 15 — reliant on the full forge deployment model: audit and plan

Status: **plan, for owner review** (2026-10-24). Mandate (owner, 2026-10-24 01:15 and 01:18):
move reliant onto forge's full deployment model, covering (a) forge secret management,
(b) *maybe* moving reliant's database to CNPG, (c) confirming the web frontend and the
landing page are on forge static-site hosting, **with the CDN on**, for our sites and by
default for every forge user, and (d) confirming reliant is on forge's internal
observability, off Sentry.

Docs only. No repo code, no cluster and no prod resource was changed.

**Code state read.** reliant `3d9fa4c1`, control-plane `0f532109`, forge `227a3849b` (each
origin/main, 2026-10-24). reliant and control-plane both pin forge `df09b730`
(`reliant/forge.yaml:34`, `control-plane/forge.yaml:60`, `control-plane/go.mod:36`).
control-plane pins reliant `95276453` (`go.mod:37`, `deploy/kcl/lib/reliant_web.k`).
landing-page `29a9dd1`.

**Evidence legend.** **[V]** measured: `repo/path:line`, or the output of a command in §8.
**[D]** read from a merged doc, runbook or `research/clickstack-review/rollout-log.md` row.
**[I]** inferred. **[U]** unverified, with the check that would settle it.

**What I could not verify.** No sanctioned read-only cluster or database context exists on
this host. The only kube contexts are the owner's full-privilege prod ones
(`gke_…_us-central1_prod`, `gke_…_prod-daemon-v2`), and I did not use them. So the live
Secret inventory, the database sizes and the names of the three values the owner just
stored are **[D]** or **[U]**. Public DNS and HTTPS GETs against our own sites were the
only live reads (§8).

---

## 0. Summary

| Area | Today | Gap to forge's model | Recommendation |
|---|---|---|---|
| **Model** ("reliant declares, cp binds", D4) | reliant declares its 4 workloads and its typed config (`reliant/deploy/kcl/workloads.k`, `config_gen.k`), but **only reliant's host-only dev env uses them**. Prod, dev-k8s and e2e render reliant from control-plane's **second copy** (`control-plane/deploy/kcl/workloads.k:711,731,762`) and **hand-written env lists** (`lib/env.k:200-395`, `prod/main.k:438-540`), held in step by a render test **[V]**. | **F6 (cross-project KCL import) blocks D4**, and nobody owns it: no forge issue or PR exists **[V]**. It blocks only D4. Secrets, CDN and observability do not wait on it. | Build F6 in forge (lane M1). Until then, keep the guard test. |
| **Secrets** | Prod reliant reads **9 k8s Secrets** (§2.1), filled out of band by `cloud-secrets.sh` from GSM, under `forge.ExternalSecrets {}` (`prod/main.k:1885`) **[V]**. The forge half (SF1–SF14) is merged and pinned. On control-plane, SC0–SC5, SC1, SC2, SC22 and SR1 are merged; SC4 covers dev-k8s only; SC7 is rehearsed but **not run in prod**; SC6 (ESO in prod) is not run **[D]**. | Prod has no ESO and no forge-adopted OpenBao, so nothing can read the store yet. The owner's 3 new values sit at `tenants/55949942…/13b10699…/<NAME>`, which is exactly where a `forge.StoreSource {store = forge.HostedStore {…}}` reads. | Make **reliant-slack, reliant-gmail-oauth and reliant-github-webhook the W0a pilot** (§2.4), right after SC6 and SC7. Keep Secret names and keys identical so no pod spec changes. Drop `control-plane-sentry` from the waves: delete it, don't migrate it. |
| **CNPG** (owner: "maybe") | Prod `reliant_cp` is on Cloud SQL `reliant-pg`, shared with every prod DB. Since 2026-10-23 it is REGIONAL (HA), with 7-day PITR and a maintenance window **[D SQLHA]**. Forge's CNPG tier (`forge.ManagedDatabase`) runs **with backups OFF in hosted prod**, "NOT RECOVERABLE" (`control-plane/deploy/kcl/lib/hosted_deploy.k:405-420`) **[V]**. | The forge-native gap is that Cloud SQL is *declared by a script* (`provision.sh` reads `lib/cloudsql_instance.k`), not by a forge declaration. | **Keep Cloud SQL (option A, §3).** Make it a forge declaration (`ManagedDatabase` bound to a Cloud SQL runtime), and make CNPG prod-grade in forge first. Revisit when the exit criteria in §3.5 hold. |
| **Static sites** | Both sites are forge `OnHosted` static sites **[V]**. reliant-web is env `prod-web` (`lib/reliant_web.k`, `prod-web/main.k`); landing is a pure forge site (`landing-page/deploy/kcl/prod/main.k`). | — | Done. |
| **CDN** | **On only for `reliantlabs.io` and `www`**, which LP-edge moved to the Cloud CDN edge by hand **[V: `via: 1.1 google`, `age: 1` on a repeat asset GET]**. `app.reliantlabs.io`, every `*.sites.reliantlabs.dev` host and every customer custom domain go to the regional Envoy LB, with **no CDN** **[V dig + headers]**. forge's docs say the platform owns "the bucket, the CDN" (`deploy/static-site` SKILL.md:16,78). | Two gaps. CDN is not the hosted default (CDN.md steps 4–5 are unbuilt). `OnBucket` sends **no Cache-Control by default** (`forge/internal/deploytarget/staticsite.go:415-417`), so GCS's 1-hour default applies to `index.html` and `config.js` **[V code; GCS default per its docs]**. | Move app.reliantlabs.io to the edge now (tenant zero is not billed). Then build edge metering, then move every site. In forge, give `OnBucket` the same default cache policy Firebase and hosted already have. `config.js` stays `no-cache` everywhere (verified at the edge, §4.2). |
| **Observability** | Go servers send traces, logs, metrics and profiles to our ClickStack **and** errors to Sentry, through 7 Sentry-wired files. Prod web and the Electron renderer are **Sentry-only**: the ClickStack sink is coded but never enabled in prod. Electron main runs `@sentry/electron`, and it **already uploads no minidumps**. `report_bug` files agent defect reports **into Sentry** **[V]**. | The forge pieces exist: server fingerprints sfp1, `forge obs issues`, Issues and Sessions panels, source maps, keyed ingest. What is missing is enablement, the parallel run, a new sink for `report_bug`, and a Node (Electron-main) telemetry entry in forge-web-runtime. | Go servers off Sentry now (lanes O2–O3). Web and desktop: enable ClickStack beside Sentry for one release, then delete Sentry (O1, O4, O5). Owner decisions OD2–OD4. |

The critical paths are independent. **Secrets:** SC6 → SC7 → W0a (§2.6). **CDN:** W1 runs
now; W2 → W3. **Observability:** O2 → O3 run now; O1 → soak → O4/O5. **Model:** M1 → M2 → M3.

---

## 1. What forge's deployment model expects, and where reliant stands

### 1.1 The model (from `forge skill load deploy`, `secrets`, `deploy/hosting`, `deploy/static-site`, `observability/destination`)

1. **Declare once.** Every process is a `fw.Workload` in `deploy/kcl/workloads.k`. Each env's
   `main.k` *binds* each workload to a runtime (`OnHost`, `OnCluster`, `OnHosted`,
   `OnCompose`) and states only its own values.
2. **Config is typed.** A composition-graph root's workload takes
   `config_gen.workloadEnvMap(cfg, w)`: exactly the config its root reaches. A
   `sensitive: true` field projects as a `forge.SecretRef`, never a literal.
3. **Secrets are references.** Each env names a provider: `FileSecrets` (dev/e2e only),
   `HostedSecrets`, or `ExternalSecrets`. A `forge.ExternalSecret` with
   `source = forge.StoreSource {…}` is synced by ESO from a store (`HostedStore`,
   `OpenBaoStore` or `GCPSecretManagerStore`), with version pins, shadow verification and
   ordered rollout (SF5, SF9–SF11).
4. **Frontends bind a runtime too.** `OnHosted` means "the platform owns the bucket, the CDN
   and the hostname". `runtime_config` becomes `config.js`.
5. **Observability is declared** (`forge.Observability`, `Frontend.telemetry`), and errors
   group as issues in our ClickStack (sfp1 for servers, the browser pipeline for
   frontends).

### 1.2 reliant today, measured

| Fact | Evidence |
|---|---|
| reliant declares `migrate`, `api_server`, `temporal_worker` and `daemon_gateway` as roots | `reliant/deploy/kcl/workloads.k:35,45,62,71` [V] |
| …used only by reliant's own host-only dev env (`OnHost`, `HostInfra` postgres/NATS/Temporal, `FileSecrets`) | `reliant/deploy/kcl/dev/main.k:72-185` [V] |
| control-plane re-declares the same three processes, with extra facts reliant's copy lacks (`release_dependencies`, image, probes) | `control-plane/deploy/kcl/workloads.k:711-790` [V] |
| reliant's own header admits "the two are kept in step by hand" until P8b | `reliant/deploy/kcl/workloads.k` header [V] |
| control-plane builds reliant's env from hand lists (`reliant_base_env`, `reliant_api_env`, `daemon_gateway_env`), plus 7 names taken from cp's AppConfig (`RELIANT_APP_CONFIG_ENV`). It does **not** use reliant's `config_gen.workloadEnvMap`. | `control-plane/deploy/kcl/lib/env.k:200-395`, `lib/stack.k:69-77`, `prod/main.k:438-540` [V] |
| A cp render test checks that env against reliant's schema at the pin | `control-plane/internal/deploy/reliant_env_schema_render_test.go` (cited in `lib/stack.k:65`) [V] |
| `control-plane/deploy/kcl/kcl.mod` has an empty `[dependencies]`, so nothing in cp's KCL can import reliant's | [V] |
| P8b (the D4 ownership move) is blocked on F6 | `12-p8-devtooling-plan.md:38-52,317,331` [V] |
| **F6 has no forge issue and no PR** (searched `cross-project KCL`, `kcl import`, `F6`) | `gh issue/pr list --repo reliant-labs/forge` [V] |

**Verdict.** reliant is *on* the forge model in prod, in the sense that forge renders and
applies every reliant object through `fw.Workload`, `forge.render` and `applier = "forge"`.
But it runs on **control-plane's declarations of reliant, not reliant's own**. D4
("reliant declares, cp binds") holds for dev only.

**Does F6 block the rest of this plan?** No. Secret *bindings* are env facts that cp owns
under D4 anyway (which store, which version pin), so they live in cp before and after F6.
reliant's part is to declare which fields are sensitive, and it already does that
(`reliant/deploy/kcl/config_gen.k:952-984`). The CDN and observability lanes touch neither
declaration.

### 1.3 F6 — what to build (lane M1, forge)

A forge project imports another forge project's `deploy/kcl` as a KCL package:

```
# control-plane/deploy/kcl/kcl.mod
[dependencies]
reliant = { forge_project = "github.com/reliant-labs/reliant", subdir = "deploy/kcl" }
```

then `import reliant.workloads as rwl` and `import reliant.config_gen as rcfg`.

- **The source resolves from `go.mod`.** It uses the module's `replace` path when there is
  one (sibling checkout, dev), otherwise the module cache at the **required version**. So
  the KCL can never import a different reliant than the binary pin: one fact, no fourth pin
  for `check-forge-coherence.sh` to police.
- **Name collisions are refused.** The imported project's `forge` import resolves to the
  importer's forge (same pin, enforced by coherence), so there are never two schema copies.
- **Cache-sound.** forge render tests that walk imports must stat the imported files (this
  repo's rule about child-process inputs).
- Then **M2** (reliant): `workloads.k` gains the facts cp's copy holds (`release_dependencies`,
  probes, the image). **M3** (cp) imports it, binds with `rcfg.workloadEnvMap` plus a
  `config.k` override per sensitive field, and deletes `wl.reliant_*`, the `reliant_*_env`
  lambdas and `RELIANT_APP_CONFIG_ENV`. The schema render test stays as the guard.

---

## 2. Secrets

### 2.1 Inventory: what prod reliant reads

There are three workloads: api-server, temporal-worker and daemon-gateway. The table gives
names and keys only.

| Env var | k8s Secret / key | Read by | Declared at | Source today | Class / wave (secrets-design §1, §7.2) | Target store name(s) |
|---|---|---|---|---|---|---|
| `DATABASE_URL` | `reliant-db-role` / `proxy-database-url` (via PgBouncer) | api, worker, gateway | `prod/main.k:337` (`pgb.client_env`), `lib/env.k:200-207` | G: the dbrole Job mints it; copy in k8s only | S, **W3** | `RELIANT_DB_ROLE_PASSWORD`, plus a `templates` DSN |
| (rollback DSN) | `reliant-db-credentials` / `database-url` | none live; the documented rollback | `lib/env.k:202`, `prod/main.k` `required_secrets` | E | **W3**, or orphan if P10 confirms | — |
| `NATS_USER`, `NATS_PASSWORD` | `control-plane-nats` / `user`, `password` | api, worker, gateway | `lib/env.k:221-222,365-366` | G | S, **W2** (shared with NATS and cp) | `NATS_USER`, `NATS_PASSWORD` |
| `INTERNAL_SERVICE_SECRET` | `control-plane-internal` / `secret` | api, worker, gateway | cp AppConfig projection (`lib/stack.k:69`, `prod/config.k:272`) | G | S, **W2**; dual-key check P11 | `INTERNAL_SERVICE_SECRET` |
| `RELIANT_VAULT_KEY` | `reliant-vault` / `vault_key` | api, worker | `lib/env.k:269` | G; **never rotate in place** | S, **W4**; `immutable`; keep the GSM escrow `reliant-vault-key-prod` | `RELIANT_VAULT_KEY` |
| `RELIANT_GITHUB_WEBHOOK_SECRET` | `reliant-github-webhook` / `webhook_secret` | api, worker (optional) | `prod/main.k:480` | E (GSM `reliant-github-webhook-secret` → copy) | S, **W0a** | `RELIANT_GITHUB_WEBHOOK_SECRET` |
| `RELIANT_OAUTH_SLACK_CLIENT_ID`, `…_CLIENT_SECRET`, `RELIANT_SLACK_SIGNING_SECRET` | `reliant-slack` / `client_id`, `client_secret`, `signing_secret` | api, worker (optional) | `prod/main.k:481-483` | E (GSM `reliant-slack-*` → copy) | S, W1 → **proposed W0a** | the three env names |
| `RELIANT_OAUTH_GMAIL_CLIENT_ID`, `…_CLIENT_SECRET` | `reliant-gmail-oauth` / `client_id`, `client_secret` | api, worker (optional) | `prod/main.k:484-485` | E (GSM `reliant-gmail-oauth-*` → copy) | S, W1 → **proposed W0a** | the two env names |
| `SENTRY_DSN` | `control-plane-sentry` / `go_dsn` | api, worker (+ 5 cp services) | `prod/main.k:439`, `lib/platform.k:233` | E; its cited GSM source **does not exist** [D secrets-design §0] | **Delete**, don't migrate (§5) | — |
| `OTEL_EXPORTER_OTLP_HEADERS_FILE` | `reliant-otel-auth` (volume) | api, worker, gateway | `lib/obs_prod.k:65-115` | P: `platformtoken` renews it | **P**, SC19 | — (platform-issued) |
| Cloud SQL proxy identity | none: Workload Identity KSA `reliant-cloudsql` | all three | `lib/stack.k:553-563` | — | none | — |

Orphan: `reliant-statsig` is in secrets-design's inventory, but nothing in today's prod KCL
references it (`rg -i statsig control-plane/deploy/kcl/prod` → 0 hits **[V]**). Delete it
with the SC0-style runbook. Not read in prod: `JWT_PUBLIC_KEY`, because prod sets a JWKS URL
(`lib/env.k:328-331`), and `LITELLM_MASTER_KEY`, which "reliant reads nowhere"
(`lib/env.k:332`) **[V]**.

**A forge gap this exposes (lane S5).** reliant's OAuth clients are **env families**
(`map<string,string> oauth_client_secrets` with `env_prefix: "RELIANT_OAUTH_"`, `sensitive:
true`; `reliant/proto/reliant/config/v1/integrations_config.proto:82-98`). forge's
`config_gen.k` projects **no** env family at all: no `RELIANT_OAUTH_*` appears in
`reliant/deploy/kcl/config_gen.k` **[V]**. That is why cp spells the five OAuth vars by hand.
forge should project a sensitive family as a `{str: forge.SecretRef}` an env fills per key,
or F6 cannot carry these.

### 2.2 What already exists (do not rebuild)

- **forge, pinned at `df09b730` in both repos:** SF1 schema, SF2 `pkg/secretaccess` and
  `secretgen`, SF3 release shape, SF4 the hardened ESO, SF5 the store-backed
  `ExternalSecret` rendering SecretStore, Role and ES, SF6–SF8 `forge.OpenBao` (bootstrap,
  escrow, public edge), SF9 preflight, SF10 `forge secret set/list/import` and
  `env secrets verify/adopt`, SF11 apply-before-consumers and pin-bump rollout ordering,
  SF12 `forge lint --secrets`, SF13 issuer registration, SF14 `ControlPlaneSecret` through
  the store **[V: forge PR list]**.
- **control-plane:** merged are SC0 hygiene, SC3 (B1–B4 GSM containers, Data Access logs,
  daily export), SC5 (B1, B3 and B4 escrowed; **B2 not**), SC1 per-env JWT roles with the
  deployer narrowed, SC2 step-up and `protected` envs, SC22 JWT clients, and SC4 (dev-k8s
  on `forge.OpenBao` + ESO; dev and e2e deliberately not moved). SC7 was rehearsed end to
  end on throwaway GKE on 2026-10-23: a ~6 s blip, Kubernetes auth dying at adoption, the
  0a/0b split mandatory. The runbook is `docs/runbooks/sc7-openbao-adoption.md` (cp#1211)
  **[D]**.
- **reliant:** SR1, the console's write-only secrets UI with versions and step-up (#1336)
  **[V]**.

### 2.3 Target shape

Use one new file, `control-plane/deploy/kcl/lib/secrets_prod.k`. secrets-design §13 already
assigns it to the waves, so **this plan adds rows to it and does not create a second file**.
Each Secret keeps its name and keys, so every reliant `secretKeyRef` stays byte-identical and
no reliant pod spec changes:

```python
forge.ExternalSecret {
    name = "reliant-slack", namespace = "control-plane-prod"
    keys = ["client_id", "client_secret", "signing_secret"]
    reason = "the Reliant Slack app (A0C731AMMFE): OAuth client + Events API signing secret"
    source = forge.StoreSource {
        store = forge.HostedStore {
            org = "55949942-…", env_id = "13b10699-…"           # tenant zero, platform prod
            server = <in-cluster OpenBao Service>, ca_configmap = <forge.OpenBao's CA>   # hub reads in-cluster
        }
        data = {"client_id" = "RELIANT_OAUTH_SLACK_CLIENT_ID", "client_secret" = "RELIANT_OAUTH_SLACK_CLIENT_SECRET",
                "signing_secret" = "RELIANT_SLACK_SIGNING_SECRET"}
        pin = {…: 1}                                            # rotation = a reviewed pin bump (D3)
        creation_policy = "Orphan"                              # dedicated Secret; "Merge" while cloud-secrets.sh still writes it
    }
}
```

The workloads keep `secret_optional = True` (`prod/main.k:480-485`). An integration whose
value has not synced reports "unavailable"; it does not fail the pod.

### 2.4 The owner's three new values become the first migrated secrets

They are in the hosted store at `secret/tenants/55949942…/13b10699…/<NAME>`. That is the
same layout `HostedStore` reads (secrets-design §2.1 rec. 5; `forge/kcl/schema.k:3880-3910`).
So no import step is needed. **[U]** Their exact names and versions: run
`forge secret list --env prod` (names and versions only), or ask the sibling agent.

Proposal (**OD5**): **move them into W0a**, beside or instead of the GitHub App key, for
three reasons:

- The values already exist only in the store.
- Every consumer is `secret_optional`, so the worst case is "integration unavailable", not
  an outage.
- They are the values the owner needs live.

The sequence:

1. **Interim (the sibling agent; nothing here).** However the sibling unblocks Slack and
   Gmail, the cleanest hand-off is that the live k8s Secret ends up holding **the same
   bytes** as the store's v1.
2. **Prerequisites:** SC6 (ESO on the hub), then SC7 (prod OpenBao adopted: TLS, escrow,
   JWT clients). Use the SC7 runbook as written. Step 0 is two deploys (0a, then 0b), never
   one: as one deploy it measured a 95 s outage.
3. **Shadow.** Declare the ES with `shadow = True`. ESO syncs `reliant-slack-eso-shadow`.
   Then `forge env secrets verify prod reliant-slack` must print `equal` for every key.
   - If the interim did not copy the new values, verify prints `DIFFERENT`. That is an
     intentional rotation, not drift: record owner sign-off in the rollout log, then
     continue.
4. **Adopt.** Set `shadow = False`. The `adopt` helper strips `last-applied`. ESO
   `Update`s the existing Secret in place: no delete and recreate, no ownerRef
   (secrets-design §7.1).
5. **Roll consumers only if bytes changed.** Env-from-Secret is read at container start.
   Restart api-server and then worker, one at a time (SF11 stamps this for later pin bumps).
6. **Verify:** ES `Ready`; Slack OAuth install and Events signature verification work;
   Gmail token refresh and the 5-minute inbox poll show no `unavailable` in
   `reliant-temporal-worker` logs.
7. **Retire.** Remove the three blocks from `cloud-secrets.sh` (with the SC9 writer guard)
   and delete the GSM duplicates after a soak (D5).

### 2.5 Rotation, bootstrap and dev

- **Rotation (D3).** `forge secret set --env prod NAME` writes vN (write-only, audited,
  step-up on a `protected` env). A PR bumps `pin`. SF11 syncs the ES *before* rolling the
  consumers that read it. `RELIANT_VAULT_KEY` is `immutable` (a pin above 1 is refused).
  `INTERNAL_SERVICE_SECRET` and the NATS password need a dual-key window first (P11),
  because api, worker, gateway and cp all present them.
- **Bootstrap.** reliant owns no bootstrap secrets. B1–B4 are the platform store's: B2 is
  still empty and gets minted in SC7's B2 rotation. Escrows kept in GSM: OpenBao B1/B3/B4,
  `zitadel-masterkey-escrow`, `reliant-vault-key-prod`.
- **Dev.** reliant's standalone dev uses `FileSecrets {path = "secrets/dev.yaml"}`
  (gitignored; `reliant/deploy/kcl/dev/main.k:185`, `.gitignore:364-365`), which is
  forge's intended dev/local provider. A later optional step (**OD9**): a `HostedSecrets`
  *local* env for shared dev integration credentials (a Slack dev app), so they never sit
  in a laptop file. cp's `forge env up dev` (k3d) keeps its own provider. SC4 deliberately
  left dev and e2e off `forge.OpenBao`.

### 2.6 Order (no downtime)

`SC6 → SC7 (0a, 0b, adoption, client JWT flips) → SC8 (drop plaintext :8202 after a soak) →
W0a (reliant-slack, reliant-gmail-oauth, reliant-github-webhook, GitHub App key) → W1 (rest)
→ W2 (control-plane-nats, control-plane-internal; Merge→Orphan on control-plane-secrets) →
W3 (reliant-db-role as a template) → W4 (reliant-vault, immutable) → SC24 (delete
cloud-secrets.sh)`.

Why there is no downtime:

- Secret names and keys never change.
- ESO updates in place under `Orphan`/`Merge` with `deletionPolicy: Retain`, so a store or
  ESO outage leaves the last good Secret serving.
- Every reliant integration ref is optional.
- The only measured blip is SC7's ~6 s, on the platform's own store clients, not reliant.

What is missing, by repo:

- **forge:** S5, sensitive env families.
- **control-plane:** SC6, SC7, SC8, the W0a+ rows, and `reliant-statsig` and
  `control-plane-sentry` deletion runbook rows.
- **reliant:** nothing for W0a–W4.

---

## 3. CNPG for reliant's database: a decision

### 3.1 Today

| | Evidence |
|---|---|
| Cloud SQL `reliant-pg`, POSTGRES_16 ENTERPRISE, `db-custom-1-3840` (1 vCPU, 3.75 GB), 15 GB SSD | rollout-log SQLHA, `research/prod-deadline-sentry-2026-10-23.md` §2 [D] |
| **REGIONAL** HA (primary us-central1-a, standby -f), **PITR 7 days**, automated backups (7 retained), maintenance SAT 12:00Z; one restart, ≈7.5 min down, 2026-10-23 | rollout-log SQLHA closing row; `control-plane/deploy/kcl/lib/cloudsql_instance.k` [D/V] |
| Holds **every** prod DB: `reliant_cp`, `control_plane_cp`, `temporal_cp`, `litellm_cp`, `zitadel`, `openbao`, … | `cloudsql_instance.k:1-5` [V] |
| reliant reaches it via PgBouncer (`reliant_cp` txn pool, `reliant_cp_session`) and cloud-sql-proxy over Workload Identity | `prod/main.k:337`, `lib/stack.k:553-563`, `research/prod-ha/README.md:55-56` [V/D] |
| Declared as KCL constants that `deploy/gcp/provision.sh --phase 10` reads and **applies imperatively** | `cloudsql_instance.k` header [V] |
| `reliant_cp` size | **[U]**: needs `SELECT pg_database_size('reliant_cp')` through a sanctioned read-only role (F5c `forge_ro` / `forge db query`). Upper bound: the whole instance disk is 15 GB across 6+ DBs. |

forge's CNPG today:

- `forge.ManagedDatabase` renders a CNPG `Cluster`: ≤3 instances, ≤500 GiB, declarative
  `restore` (`forge/kcl/schema.k:944-988`, `pkg/deploy/v1alpha1/manageddatabase_types.go:58-98`).
- Backups use the Barman Cloud CNPG-I plugin (`control-plane/deploy/kcl/lib/barman_plugin.k`),
  in dev/e2e to in-cluster SeaweedFS.
- **Hosted prod runs it with backups OFF**: "THIS MEANS A PROD MANAGED DATABASE IS NOT
  RECOVERABLE … Revisit before this tier is offered for anything durable"
  (`lib/hosted_deploy.k:405-420`).
- The CNPG operator runs on prod-daemon-v2 only (`lib/cnpg_vendored.k`); the hub has 11
  orphaned CNPG CRDs **[V]**.

### 3.2 Options

| | A. Keep Cloud SQL, make it forge-declared (**recommended**) | B. CNPG on the hub for `reliant_cp` only | C. CNPG for every platform DB | D. Status quo |
|---|---|---|---|---|
| HA / failover | Google-run regional standby, live since 10-23 | CNPG streaming standbys across zones; we tune and test failover | same as B, for 6+ DBs | as A |
| Backups / PITR / DR | Google-run, 7-day PITR, on | **we build** Barman → GCS over Workload Identity, cross-region bucket, restore drills; forge has none of it in prod today | same, bigger | as A |
| Major upgrades | Google in-place, with downtime | CNPG in-place major upgrade (offline `pg_upgrade`, newer operators) or a logical-import cutover; ours to test | ours ×6 | as A |
| Operator burden | none new | operator upgrades, PDBs, storage class, node pool sizing, alerting on replication lag and archive failures | high | none |
| Failure domains | one instance for all DBs (today's risk) | splits reliant from the rest, but **reliant still needs Temporal (`temporal_cp`) on Cloud SQL**, so availability is no better | removes Cloud SQL; adds the hub cluster as the DB failure domain | — |
| Same in every env? | no; dev is `HostInfra` postgres on host anyway | no; dev is still host postgres | no | no |
| forge-native declaration | **yes, once forge can bind `ManagedDatabase` to Cloud SQL (§3.4)** | yes | yes | no (script) |
| Cost | ≈ unchanged | similar node and PD cost, plus engineer time | similar | — |
| Migration risk | none | a data move (§3.3) | 6 data moves, including Temporal | — |

### 3.3 If the owner picks B or C anyway: the data path

- **Logical replication.** Cloud SQL needs `cloudsql.logical_decoding=on`, which is **an
  instance restart** (all prod DBs; the last restart was ≈7.5 min **[D]**). Every table
  needs a replica identity, sequences must be copied at cutover, and DDL is frozen while
  it runs. Then: CNPG `Subscription`, catch up, a write freeze, re-point PgBouncer's
  `reliant_cp` target, unfreeze. **Write downtime ≈ 1–3 min [I].**
- **Dump and restore** (CNPG `bootstrap.initdb.import` through the proxy). Downtime = dump
  + restore with writes stopped. At an estimated ≤ a few GB on a 1-vCPU source, that is
  **≈10–30 min [I]**. Measure size first (§3.1 [U]).
- Rollback: PgBouncer back to Cloud SQL, accepting any writes made after cutover. That is
  why logical replication needs a reverse subscription prepared before the flip.

### 3.4 Recommendation, and the devil's advocate

**Recommendation: A.**

1. **Keep `reliant_cp` on Cloud SQL.**
2. **forge (lane D2):** `ManagedDatabase` gains a Cloud SQL runtime binding,
   `runtime = forge.OnCloudSQL {instance = "project:region:instance"}`. That is the same
   "declare once, bind per env" primitive workloads use: dev binds host postgres, a
   customer's cluster binds `OnCluster` (CNPG), hosted binds `OnHosted`. Its first version
   *verifies* the declared durability settings (availability, PITR, backups, maintenance
   window) in `forge doctor` and the deploy preflight. `provision.sh` keeps applying them
   until forge converges them itself, which removes the "a script must be run" smell
   behind `cloudsql_instance.k`.
3. **forge (lane D3):** make CNPG prod-grade where it already ships, the hosted tier
   customers use. That means Barman → GCS over Workload Identity, a restore drill test,
   archive-lag alerts, and a major-upgrade path. We must do this before the tier holds
   anything durable anyway (`hosted_deploy.k:405-420`).

**Against A:** "We are asking customers to trust `ManagedDatabase`, and not running our own
product on it is the opposite of dogfooding."

That is fair. But the right dogfood is D3, on the tier customers actually get. Our primary
DB would be the most expensive place to discover that the backup path is absent: it is
absent *today* in hosted prod.

**Against my own A:** "`OnCloudSQL` is a mode, and Cloud SQL lock-in." forge already treats
cloud runtimes as bindings (`OnFirebase`, `OnBucket`, `OpenBaoPostgres.cloudsql_instance`),
so this is a primitive, not an enum of blessed configs. The declaration (`ManagedDatabase`)
stays portable.

**Against B:** it buys a forge-native declaration at the price of an operator we would run
alone, and it does not improve availability while Temporal stays on Cloud SQL.

### 3.5 Exit criteria to revisit CNPG for prod

All four must hold:

1. D3 shipped, with a monthly automated restore drill green for 60 days on a hosted
   customer DB.
2. CNPG failover tested under load on the hub.
3. A major-version upgrade rehearsed.
4. A measured reason to leave Cloud SQL: cost, latency, or a capability Cloud SQL lacks.

---

## 4. Static sites and the CDN

### 4.1 Where each site is served from (2026-10-24, public probes)

| Host | DNS | Path | CDN? | Headers observed |
|---|---|---|---|---|
| `reliantlabs.io`, `www.reliantlabs.io` (landing, env `prod`) | `sites-edge.reliantlabs.dev` → **34.95.100.185** | global ALB + **Cloud CDN** (`static-sites` backend, `USE_ORIGIN_HEADERS`) → NEG `static-sites-origin` → workspace-proxy `staticorigin` → GCS | **yes**: `via: 1.1 google`; repeat asset GET has `age: 1` | assets `public, max-age=31536000, immutable`, gzip, `Vary`; `/` and `/config.js` `no-cache` with an ETag, **never served aged** (3 GETs, no `age`) |
| `app.reliantlabs.io` (reliant-web, env `prod-web`) | `ingress.reliantlabs.dev` → **34.63.203.181** | regional Envoy L4 LB (us-central1) → workspace-proxy → GCS | **no** (no `via`, no `age`) | same origin policy: assets immutable + gzip; `/`, `/config.js`, `/version.json` `no-cache`; HSTS |
| `*.sites.reliantlabs.dev` (every hosted site's platform host, e.g. `valiant-caribou`, `elegant-oriole` = landing staging) | → 34.63.203.181 | Envoy | **no** | — |
| customer custom domains | instructions point at ingress; edge IPs "ACCEPTED by verification, never PRINTED" | Envoy | **no** | `control-plane/internal/customdomain/records.go:55-61` [V] |

Both of our sites **are** on forge static hosting:

- reliant-web is `forge.Frontend {runtime = forge.OnHosted {}, asset_dir = "assets"}` in its
  own persistent env `prod-web`, published by `cut-release.sh` (`lib/reliant_web.k`,
  `prod-web/main.k`).
- landing is a forge site project (`landing-page/forge.yaml` `name: landing`; prod
  `_hosted_frontend(_web_frontend)`, `HostedSecrets`, `Observability {replay}`,
  `BrowserTelemetry {funnels}`).

Firebase and Cloudflare Pages are retired: cp#1099, landing-page#84 **[D]**.

### 4.2 What hosted static provisions today (control-plane implementation)

- **Bucket:** platform-owned `reliant-prod-static-sites`, one prefix per org/env/site. The
  operator (`internal/operators/staticsite`) syncs `releases/<digest>/` → `live/`, keeps 10
  releases, and protects live + previous.
- **Serving** (`internal/staticorigin`): host index → `site.json` → prefix. SPA fallback
  for route-shaped paths; a missing asset is a 404. Previous releases' assets stay
  serveable after a deploy. gzip; quoted ETag with 304; HSTS.
- **Cache policy per asset class** (`internal/staticorigin/headers.go:31-42`):
  - files under the declared `assetDir` get `public, max-age=31536000, immutable`;
  - **everything else gets `no-cache`**, explicitly including the entry document,
    `config.js` and `version.json`;
  - a legacy site with no `assetDir` gets `no-cache` on `*.html` and `config.js` only, and
    no directive on the rest. With `USE_ORIGIN_HEADERS` the edge does not cache those
    either: safe, but no CDN benefit.
- **Runtime config:** the platform writes `config.js` after each sync. Its ETag changed
  between two of my probes minutes apart, and every response was fresh.
- **Invalidation on promote: none, by design.** HTML and `config.js` are `no-cache`, so
  the edge revalidates them every time, and hashed assets never change.
  `STATIC_SITE_CDN_URL_MAP` stays unset (`lib/hosted_deploy.k:396-399`; CDN.md "Changed in
  implementation"). The cost is one origin round trip per HTML load. A purge per platform
  hostname would miss the same site under a custom domain.
- **Custom domains / TLS:**
  - Envoy: cert-manager HTTP-01 or DNS-01, with a ListenerSet and HTTPRoute per domain.
  - Edge: Certificate Manager map `static-sites`, one DNS authorization per hostname, listed
    **by hand** in `STATIC_EDGE_CERTS` (`lib/static_edge.k:44-48`): today only the
    `*.sites.reliantlabs.dev` wildcard and `reliantlabs.io`/`www`.
- **Metering:** static egress is metered **at the origin** (`staticorigin` → `staticegress`),
  and the free-tier cap is enforced there. An edge cache hit never reaches the origin.
  **A CDN without edge metering under-bills and cannot enforce the cap** (CDN.md). Edge
  metering (CDN.md step 4) is **not built**: there is no LB-log sink or consumer in cp
  **[V git log]**.

### 4.3 Is the CDN the default?

- **Hosted (`forge.OnHosted`): no.** Only hostnames moved to the edge by hand get it.
  forge's own text says otherwise ("the platform owns the bucket, the CDN and the
  hostname", `forge/internal/templates/project/skills/forge/deploy/static-site/SKILL.md:16,78`;
  `deploy/hosting` SKILL.md:17,68), and so does landing's scaffolded header ("Static
  hosting is managed by Reliant, including the CDN"). That is true for landing's prod
  domains and **false for every other site**.
- **`forge.OnBucket`: by design forge never creates your CDN.** `cdn.url_map` names yours,
  and `invalidate = "entrypoints"` (default) purges HTML and the runtime config document
  (`forge/kcl/schema.k:2778-2828`). **Defect:** with no `cache_control` rules, forge uploads
  objects with **no Cache-Control** (`forge/internal/deploytarget/staticsite.go:415-417`).
  GCS then serves a publicly readable object as `public, max-age=3600` (GCS's documented
  default; not measured here), so `index.html` and **`config.js` can be an hour stale in
  browsers**, after the CDN purge.
- **`OnFirebase`** already renders the right default: `asset_dir` immutable, everything else
  `no-cache` (`schema.k:2944-2948`).

### 4.4 Steps

| Lane | Repo | What | Depends |
|---|---|---|---|
| **W1** | cp | app.reliantlabs.io on the edge. Add `reliant-web=app.reliantlabs.io` to `STATIC_EDGE_CERTS` (`lib/static_edge.k:48`). `provision.sh prod --phase 11` (cert + DNS authorization CNAME). Verify with `curl --resolve app.reliantlabs.io:443:34.95.100.185` (assets get `age`; `/config.js` and `/version.json` never aged). Flip the CNAME to `sites-edge.reliantlabs.dev`. Extend `scripts/check-web-version.sh` to assert `via: 1.1 google` and an un-aged `config.js`. Rollback: CNAME back to `ingress.reliantlabs.dev`; Envoy still serves the host. Tenant zero is not billed, so metering is not a prerequisite (CDN.md step 3). | none (**OD6**) |
| **W2** | cp | Edge metering: LB request logs → Logging sink → Pub/Sub → a control-plane-workers consumer that sums `responseSize` per host into `staticegress`; cap enforcement by host purge. A rollout-log GCP write. | none |
| **W3** | cp | Every site on the edge: wildcard `*.sites.reliantlabs.dev` DNS → edge (cert `static-sites-wildcard` is already ACTIVE). The custom-domain loop issues Certificate Manager certs for static-site bindings, and `records.go` *suggests* the edge for them. | W2 (**OD7**) |
| **W4** | forge | `OnBucket` default cache policy = Firebase's and hosted's (asset_dir immutable, rest `no-cache`) when the frontend declares no `cache_control` (`internal/deploytarget/staticsite.go` `uploadCmds`, schema doc). Add a test that `config.js` is never uploaded cacheable. | none |
| **W5** | forge | Truthful docs until W3: `deploy/static-site`, `deploy/hosting` and the frontend-only `main.k.tmpl` say "CDN for hostnames the platform has moved to its edge". After W3, `forge domain ls` / `forge env status` report the serving path (edge vs ingress) from the control plane. | W3 for the second half |
| **W6** | cp | Extend the pin: `internal/staticorigin/serving_test.go:46-56` already asserts `config.js` and `version.json` are `no-cache` for one site shape. Add a `base_path` site, an asset dir at the root, and a legacy site with no `assetDir`. That keeps P9's runtime config (R2a) from ever being cached long. | none |

Risks:

- **Negative caching.** It is on at the edge. A 404 for a not-yet-synced asset path could
  be cached briefly. The operator syncs before the host index flips and previous releases'
  assets stay serveable, so the window is small **[I]**. W1's verify includes a deploy and
  a reload.
- **Edge cert issuance is a per-hostname list in KCL** until W3 automates it.

---

## 5. Observability: what is still outside forge's stack

### 5.1 Inventory

| Component | On forge's stack (our ClickStack) | Still on Sentry or other external services |
|---|---|---|
| Go servers (api, worker, gateway) | OTLP traces, logs, metrics and profiles through forge `pkg/observe` (`reliant/internal/logging/logger.go`, `observe.Setup`), authenticated by `reliant-otel-auth` (`lib/obs_prod.k:65-115`) **[V]**. ERROR records carry `code.function.name` and `exception.type` for sfp1 grouping (`forge/pkg/obsquery/fingerprint.go:10-55`). cp's issues worker groups and symbolicates (`control-plane/internal/workers/issues/*`). `NewIssue`, `IssueRegressed` and `IssueSpike` alert on them (cp#1132). | **Errors to Sentry:** `internal/telemetry/{sentry,log_event,scrub,bug_report}.go` (1,080 LOC); `internal/logging/sentry_handler.go` (slog ERROR → Sentry); `internal/workflow/runtime/registry.go` (`CaptureExceptionWithContext` ×2); `cmd/reliant/cmd/serve.go` (`Flush`); `internal/app/providers.go:394-396` (`SetReporter`); config `ServerProcessConfig.sentry` (`proto/reliant/config/v1/observability_config.proto:54-59`, `pkg/config/roots_gen.go:95-98,213-216`, goldens `cmd/reliant/cmd/testdata/server_roles/*.surface.golden`); module `getsentry/sentry-go` in `go.mod`. Prod env: `_backend_sentry_env` (`control-plane/deploy/kcl/prod/main.k:438-442`) on reliant api and worker (`:497,539`) and on 5 cp services (`:623,702,757,827`), plus `required_secrets` `platform.sentry_dsn_secret` (`lib/platform.k:233`). |
| `report_bug` (agents filing Reliant/forge defects) | — | **Each report is a Sentry event** (`internal/llm/tools/report_bug.go`, `internal/telemetry/bug_report.go`, `internal/workflow/runtime/activities/handlers/bug_report_note.go`; reliant#657). Removing Sentry without a new sink silently drops every report (**OD3**). |
| Web (app.reliantlabs.io) | forge-web-runtime `^0.9.0` and `@hyperdx/browser 0.26.0` are installed, and `lib/telemetry.ts` is written (reliant#1027) **[V]**. | **Prod is Sentry-only.** `telemetrySinks.ts` defaults to Sentry, and ClickStack starts only when `VITE_OTEL_ENDPOINT` is set. **Nothing sets it in prod** (`control-plane/deploy/kcl/lib/env.k:556-650`: only `VITE_SENTRY_DSN` and `VITE_SENTRY_ENABLED`). `prod-web/main.k` declares no `observability`, and `reliant_web` declares no `telemetry` (landing does both). `@sentry/react ^10.47` (`frontends/web/package.json:71`), `lib/sentry.ts`, `lib/sentryScrub.ts`, the funnel in `lib/analytics.ts`, `api/rpcErrorPolicy.ts` and `api/transport.ts` reporting, CSP `*.ingest.sentry.io` (`frontends/web/vite-plugin-csp.ts:170-174`). Open cp#1203 (P9 R2c) carries `SENTRY_DSN`/`SENTRY_ENABLED` into `runtime_config`. |
| Electron renderer (app://bundle) | same code as web | Sentry-only: electron DSN in `electron/release.config.json:13` and `lib/env.k` `reliant_desktop_release_config`. It has no same-origin `/_otel`, so it needs the **keyed ingest** `ingest.reliantlabs.dev/k/<key>/v1/*`, which is already routed (`control-plane/deploy/kcl/prod/ingress.k:565-590`) **[V]**. |
| Electron main | — | `@sentry/electron ^7.10` (`electron/src/main.js:96-227`). **Native minidumps are already never uploaded** (`electron/src/sentry-scrub.js:73-77,209-220`; pinned by `electron/test/sentry-minidumps.test.js`; reliant#532) **[V]**. So the frontend-observability decision to "keep Sentry for the Electron main process (native crash reports)" (`research/frontend-observability/DECISIONS.md:16`) rests on something the app no longer does. Main sends only JS errors. `release.yml:613-615` (and 2 more jobs) passes `secrets.SENTRY_DSN` and `secrets.STATSIG_CLIENT_KEY` to `generate-build-config`. |
| Statsig | dropped with web Sentry (owner, 2026-10-19) | residue: `release.yml` `STATSIG_CLIENT_KEY`, `proto/reliant/config/v1/analytics_config.proto`, the cp Secret `reliant-statsig` (orphan, §2.1). |
| Landing | fully on forge (BrowserTelemetry, replay, funnels; landing-page#83) | none in code. Its privacy page names Sentry and Statsig (`landing-page/src/pages/Privacy.tsx:580-1264`), which is legal copy to update when they go. |
| Console | Issues and Sessions panels, funnels (forge#880), `forge obs issues` (#1166), source maps as an OCI artifact (#879, #896) | **no session replay player** in reliant's console (`rg rrweb frontends/web/src` → none) **[V]**, though the owner turned replay on (DECISIONS 2026-10-19 #4). |
| Product integration "Sentry connector" (reliant#636, MCP `SENTRY_ACCESS_TOKEN`) | — | a **user-facing** integration, not our telemetry. **Keep.** |

### 5.2 Removing Sentry entirely: the steps

The owner's DSN will not be rotated, so these run as soon as each one's replacement is
verified.

1. **O2 — Go servers off Sentry** (reliant; can start now).
   - Before deleting: run `forge obs issues --env prod --service reliant-api-server` (and
     worker). sfp1 issues must exist for the error classes Sentry shows. Panics recovered
     in `workflow/runtime/registry.go` must log ERROR with `exception.type` and a stack, so
     sfp1 groups them.
   - Then delete: `internal/telemetry` Sentry reporter, scrub and log-event; `internal/logging/sentry_handler.go`;
     `ServerProcessConfig.sentry` (proto, regenerate, goldens); `SetReporter` and `Flush`;
     the `sentry-go` module.
   - No backwards compatibility (reliant rule): remove the config fields, don't deprecate
     them.
2. **O2b — `report_bug` sink** (reliant + cp; **OD3**). Recommended: an ERROR-free
   structured log event, `source=llm_bug_report`, with the same redaction and bounds as
   today, through OTLP. cp's issues worker groups it under its own fingerprint, and
   `forge obs issues --source llm_bug_report` lists it. That keeps the text off public
   GitHub. The autopilot on-call can still file GitHub issues from it.
3. **O3 — cp stops injecting Sentry** (cp, with the pin bump carrying O2). Drop
   `_backend_sentry_env` from the reliant api and worker envs. With **OD4**, drop it from
   cp's five services too and delete `control-plane/pkg/observe/sentry*.go` and `sentry-go`.
   Remove `platform.sentry_dsn_secret` from `required_secrets`. Then add a runbook row to
   delete Secret `control-plane-sentry`. There is no GSM copy (it never existed).
4. **O1 — web and desktop on ClickStack, beside Sentry** (cp; after cp#1203 / reliant#1354
   land). In `runtime_config` set `OTEL_ENDPOINT` (prod-web: same-origin `/_otel`; desktop
   renderer: the keyed ingest URL with its key) and `TELEMETRY_SINKS=sentry,clickstack`.
   Add `observability` to the `prod-web` Bundle and `telemetry = forge.BrowserTelemetry
   {replay sampling per the owner's 2026-10-19 #4}` to `reliant_web`.
5. **Parallel run for ONE release** (**OD8**: one rather than DESIGN's "one or two", given
   the mandate). Compare top errors, error rate, funnel steps and replay availability.
6. **O4 — delete web Sentry** (reliant): `@sentry/react`, `lib/sentry.ts`, `sentryScrub.ts`;
   collapse `telemetrySinks.ts` to the one sink; the funnel → the runtime's `track`; CSP
   entries; `SENTRY_*` keys from the web config proto (and the cp `runtime_config` keys in
   the same coherent pin bump; cp#1203's guard test enforces this).
7. **O5 — Electron** (reliant; **OD2**). Renderer as O4. Main: replace `@sentry/electron`
   with forge's runtime over OTLP to the keyed ingest. forge-web-runtime is browser-only
   today, so **lane O6 (forge)** adds a Node/Electron-main entry: uncaught exception and
   unhandled rejection, `exception.*` attrs, the same scrub policy. Then delete the
   `sentry-dsn` and `statsig-client-key` inputs from `release.yml` and
   `generate-build-config`, `electron/src/sentry-scrub.js` and its tests.
8. **O7 — copy** (landing-page + reliant settings; owner and legal): the privacy page and
   the "crash reporting" setting text.
9. **O8 — Sentry account** (owner): disable ingestion, then delete the projects after a
   soak.

Close forge PLAN item **F7** ("Sentry hook") as won't-do (`PLAN.md`, "Still open in forge").

---

## 6. Lanes, order, verification

Within a parallel group, lanes touch **disjoint files**. A lane that names another chat's
file goes **through that chat**.

| Lane | Repo | Owned files | Depends on | Parallel group | Verification |
|---|---|---|---|---|---|
| **S1 = SC6** | cp | `deploy/kcl/lib/eso_prod.k`, one import in `prod/main.k` | — | secrets chat | SC7 runbook §1 checks (kinds absent, no webhook, no exec, VAPs) |
| **S2 = SC7** | cp | `deploy/kcl/lib/openbao_prod.k`, client env, deletes `lib/openbao_cloud.k` and `deploy/openbao/bootstrap.sh` | S1 | secrets chat | runbook probes; ≤6 s blip; the JWT flips |
| **S3 = W0a (+ OD5)** | cp | `deploy/kcl/lib/secrets_prod.k` (**the SC9 file: one lane, not two**), `deploy/secrets/cloud-secrets.sh` guard | S2 | after S2 | `forge env secrets verify` equal (or a signed-off rotation); ES Ready; Slack/Gmail/webhook smoke; `forge lint --secrets` clean |
| S4 = W1–W4 rows for reliant | cp | same file, a later PR each | S3 + 48 h soak | serial | per-wave verify; P11 dual-key before W2 |
| **S5** | forge | config KCL codegen (sensitive env families → `{str: SecretRef}`) | — | **G-now** | golden for a `map<string,string>` sensitive family; reliant regenerate shows `RELIANT_OAUTH_*` |
| S6 | cp | `docs/runbooks/secrets-cleanup.md` rows: `reliant-statsig`, then `control-plane-sentry` | (O3 for sentry) | G-now | `forge lint --secrets` `secrets-unreferenced-key` confirms each |
| **W1** | cp | `deploy/kcl/lib/static_edge.k`, `scripts/check-web-version.sh` | — | **G-now** | `curl --resolve` probes, then the DNS flip; `check-web-version.sh` asserts the edge and an un-aged `config.js` |
| W2 | cp | new worker consumer + `provision.sh` sink | — | G-now | metered bytes on the edge = the origin's for an uncached host |
| W3 | cp | `internal/customdomain/records.go`, `internal/workers/custom_domain_converge`, DNS | W2 | after W2 | a customer site serves with `age`; the domain flow suggests the edge |
| **W4** | forge | `internal/deploytarget/staticsite.go`, `kcl/schema.k` (OnBucket doc) | — | **G-now** | unit test: `index.html` and `config.js` uploaded `no-cache`, `asset_dir` immutable |
| W5 | forge | skills text; later `forge domain ls` / `env status` | W3 (second half) | G-now (text) | skill text matches §4.1 |
| W6 | cp | `internal/staticorigin/serving_test.go` | — | G-now | `task test:short -- ./internal/staticorigin/...` |
| D1 | — | none: read-only measurement with a **sanctioned** role | owner sanction | — | `pg_database_size` per DB, recorded here |
| **D2** | forge | `kcl/schema.k` (`OnCloudSQL` runtime on `ManagedDatabase`), doctor/preflight check | OD1 = A | G-now | render + doctor against a declared-vs-live fixture |
| D3 | forge + cp | barman → GCS over WI for the hosted tier; restore-drill test; `lib/hosted_deploy.k` | — | G-now | a restore drill on a scratch DB |
| **O2** | reliant | `internal/telemetry/*`, `internal/logging/sentry_handler.go`, `internal/workflow/runtime/registry.go`, `cmd/reliant/cmd/serve.go`, `internal/app/providers.go`, `proto/reliant/config/v1/observability_config.proto` (+ regenerate, goldens), `go.mod` | — | **G-now** | `make test-affected`; `forge generate` clean; sfp1 issues present in prod before merge |
| O2b | reliant (+ cp issues worker if a new fingerprint is needed) | `internal/llm/tools/report_bug.go`, its new telemetry home, `bug_report_note.go` | OD3 | with O2 | a report appears in `forge obs issues --source llm_bug_report` |
| **O3** | cp | `deploy/kcl/prod/main.k` (`_backend_sentry_env` and its uses), `lib/platform.k:233`; (OD4) `pkg/observe/sentry*.go`, `go.mod` | O2 pinned | after O2 | prod render has no `SENTRY_*`; `task ci` |
| O1 | cp | `deploy/kcl/lib/env.k` (runtime_config keys), `lib/reliant_web.k`, `prod-web/main.k` | cp#1203 merged | after #1203 | browser spans and `rum.rr-web` for tenant zero in ClickHouse; the generated frontend dashboard is non-empty |
| O4 | reliant | `frontends/web/src/lib/{sentry,sentryScrub,telemetrySinks,analytics}.ts`, `api/{rpcErrorPolicy,transport}.ts`, `vite-plugin-csp.ts`, `package.json`, web config proto | O1 + one release | after soak | vitest affected; CSP has no sentry; bundle has no `@sentry` |
| O5 | reliant | `electron/src/{main,sentry-scrub}.js`, tests, `package.json`, `release.yml`, `.github/actions/generate-build-config`, `.github/scripts/generate-build-config.mjs` | O6, OD2 | after O6 | electron `npm test`; a packaged smoke shows a main-process error in ClickStack |
| O6 | forge | forge-web-runtime Node/Electron-main entry | — | G-now | runtime unit tests; an OTLP export to a keyed-ingest fixture |
| O7 | landing-page, reliant | `src/pages/Privacy.tsx`, settings copy | O4, O5 | last | owner/legal review |
| **M1 = F6** | forge | KCL dependency resolution from `go.mod` | — | G-now | e2e: cp-shaped fixture imports a sibling project's `workloads.k` at the pin; skew refused |
| M2 | reliant | `deploy/kcl/workloads.k` | M1 | after M1 | `forge lint --graph`; reliant dev unchanged |
| M3 | cp | `deploy/kcl/workloads.k`, `lib/env.k`, `lib/stack.k`, envs | M2 pinned | after M2 | `reliant_env_schema_render_test` green; rendered prod diff is empty except ordering |

**Can start now, in parallel:** S5, S6 (statsig), W1, W2, W4, W5 (text), W6, D2/D3 (after
OD1), O2 (+O2b after OD3), O6, M1.

**Critical paths:**

- secrets: S1 → S2 → S3 (the owner's values live in prod);
- CDN default: W2 → W3;
- Sentry gone: O1 → soak → O4, and O6 → O5;
- D4: M1 → M2 → M3.

---

## 7. Risks, coordination, owner decisions

### 7.1 Risks

| Risk | Mitigation |
|---|---|
| SC7 adoption breaks Kubernetes-auth clients (measured 403s until the JWT flip) | Follow the runbook's flip order. reliant is not a store client; its exposure is only through S3's timing. |
| ESO adoption drops keys a Secret holds but the ES doesn't declare | `Merge` while half-migrated; SF12 key coverage (`forge lint --secrets`); names and keys unchanged. |
| A rotation race (pods roll before ESO syncs) | SF11 ordering. Pin bumps are reviewed PRs. |
| Removing Go Sentry leaves an error class unseen | O2 gate: sfp1 issues present first. `IssueSpike`/`NewIssue` alerts already fire on ClickStack. |
| `report_bug` reports silently vanish | O2b ships in the same PR as the Sentry deletion, never after it. |
| Web or desktop go dark if ClickStack ingest fails | The parallel release (O1) keeps Sentry until O4. The frontend dashboard + `FrontendJSErrorSpike` alert (forge obsdash). |
| CDN serves a stale `config.js` | Origin policy is `no-cache`, never aged at the edge (§4.1); W6 pins it; W1 extends the version check. |
| CDN under-bills customers | W3 strictly after W2 (OD7). |
| F6 adds a fourth pin that drifts | M1 resolves from `go.mod`; no separate ref exists. |
| Cloud SQL single instance for every DB | Unchanged by this plan (already REGIONAL + PITR). CNPG would not remove the Temporal dependency (§3.2). |

### 7.2 Coordinate with

- **Secrets chat (SF/SC):** owns S1, S2 and `lib/secrets_prod.k` (SC9). This plan's S3/OD5
  is a **proposal to them**, not a parallel lane. S6 rows go in their runbook.
- **Sibling agent (Slack/Gmail unblock):** the hand-off in §2.4 step 1. Ask them to record
  which store names hold the three values, and whether the live Secret now has the same
  bytes.
- **Obs (fbl/\*, cp#1212):** O1/O2b touch the issues worker and dashboards they own; O6 is
  forge-web-runtime, which they also work on.
- **landing-v6 (LV65 in flight on staging):** O7's `Privacy.tsx` edit goes after their
  current PR, or through them.
- **P9 lanes in this chat:** cp#1203 (R2c), reliant#1354 (R2a), #1370 (R2b) and #1381 (D7
  traceInterceptor) land **before** O1/O4/O5. O4 removes the `SENTRY_*` web config keys in
  the same coherent reliant→cp pin bump the #1203 guard test demands.
- **cp pin bumps:** O3 and M3 ride a `task pin:reliant` with `check-forge-coherence.sh`.

### 7.3 Owner decisions (recommendations first)

| # | Decision | Recommendation |
|---|---|---|
| **OD1** | Move `reliant_cp` to CNPG? | **No, not now (option A).** Keep Cloud SQL, make it a forge declaration (D2), and make forge's CNPG prod-grade on the customer tier first (D3). Revisit on §3.5's criteria. |
| **OD2** | Electron main off Sentry, reversing the 2026-10-18 "keep for native crashes"? | **Yes.** Minidumps are already not uploaded, so Sentry carries only JS errors. Do it once forge's Node entry (O6) exists. |
| **OD3** | Where `report_bug` goes after Sentry | **ClickStack event, `source=llm_bug_report`**, grouped by cp's issues worker and listed by `forge obs issues`. Not GitHub directly (report text can hold user context). Autopilot may triage into GitHub. |
| **OD4** | Take control-plane's own 5 Go services off Sentry in the same step | **Yes.** It is the same DSN and the same Secret, and cp already has forge issues. Otherwise `control-plane-sentry` cannot be deleted. |
| **OD5** | Make reliant-slack / reliant-gmail-oauth (the 3 values just stored) the W0a pilot, beside the webhook and the GitHub App key | **Yes.** The values are already in the store and the consumers are optional. |
| **OD6** | app.reliantlabs.io onto the CDN edge now, before edge metering | **Yes.** Tenant zero is not billed; rollback is one CNAME. |
| **OD7** | CDN as the default for every hosted site only after edge metering | **Yes.** Otherwise we under-bill and cannot enforce the free-tier cap. |
| **OD8** | Web Sentry parallel run length | **One release**, then delete. |
| **OD9** | (later, minor) A shared hosted-secrets *local* env for reliant dev integration creds | Optional; defer until someone needs a shared dev Slack/Gmail app. |

---

## 8. Commands behind the [V] claims (read-only)

```bash
# public DNS + headers (no credentials)
for h in app.reliantlabs.io reliantlabs.io www.reliantlabs.io valiant-caribou.sites.reliantlabs.dev elegant-oriole.sites.reliantlabs.dev; do dig +short $h; done
#   app → ingress.reliantlabs.dev 34.63.203.181; reliantlabs.io → 34.95.100.185; www → sites-edge.reliantlabs.dev 34.95.100.185; *.sites → 34.63.203.181
curl -sSD- -o/dev/null https://reliantlabs.io/assets/<hashed>.js     # 2nd GET: via: 1.1 google, age: 1
curl -sSD- -o/dev/null https://app.reliantlabs.io/assets/<hashed>.js # no via, no age
curl -sSD- -o/dev/null https://reliantlabs.io/config.js              # cache-control: no-cache, no age (×3)
curl -sSD- -o/dev/null https://app.reliantlabs.io/config.js          # cache-control: no-cache
# code
rg -n -i statsig control-plane/deploy/kcl/prod                       # 0 hits
rg -n 'getsentry/sentry-go' reliant --type go -l                     # internal/telemetry/{sentry,log_event,scrub,bug_report}.go
rg -n 'RELIANT_OAUTH_' reliant/deploy/kcl/config_gen.k               # 0 hits (env families not projected)
gh issue list --repo reliant-labs/forge --search "cross-project KCL OR kcl import OR F6" --state all   # no F6
kubectl config get-contexts -o name                                  # only the two full-privilege prod contexts; not used
```
