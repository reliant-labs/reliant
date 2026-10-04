# Connections and the credential vault (integrations phase 1)

**Status:** design only. Nothing here is implemented. This document is the
"vault is its own design" that `INTEGRATIONS.md` §4.2 defers to. Read it with
`INTEGRATIONS.md` §4 and §11, `DELEGATED_CREDENTIAL.md` §7–§9,
`ENGINE_SPLIT_PLAN.md` ("New: `CredentialService`", line 323) and
`TOOL_PLACEMENT.md` §5.

Line numbers cite reliant at `f35fce86` and control-plane at `53604402`.
Anything I could not verify is marked **[unverified]** and collected in §11.

---

## 0. Decision summary

| # | Question | Recommendation |
|---|---|---|
| 1 | Where secrets live | **(b) A reliant-side envelope vault.** Ciphertext goes in reliant's Postgres, each tenant's DEK is wrapped by a KEK, and the KEK sits behind a `KeyWrapper` seam. v1 ships one wrapper, a versioned env keyring. That is the same mechanism control-plane already runs for `git_credentials` (`pkg/crypto/aesgcm.go`). A Cloud KMS or OpenBao Transit wrapper can be added later behind the same seam. **Do not** put integration secrets in control-plane's OpenBao KV store. |
| 2 | Worker call-time path | The activity input carries only `connection_id` (or a binding name). The worker resolves the run's owner **from the DB** (run → owner), checks `connection.user_id == owner`, opens the secret in process and builds the HTTP request. The plaintext lives inside one `vault.Secret` value that cannot be formatted, logged or serialized. No `rlat_` is involved, because the reliant worker is not calling control-plane. |
| 3 | Schema and RPCs | Metadata (`connections`) is split from ciphertext (`connection_secrets`). Wrapped DEKs live in `vault_keys`, single-use OAuth state in `oauth_flows`, and an append-only audit trail in `connection_events`. `ConnectionService` has no RPC that returns a value, and a reflection test pins that, copied from control-plane's secret store. |
| 4 | GitHub App owner | **Reliant.** Self-hosted reliant has no control-plane and still needs GitHub, so reliant must be able to own an App anyway. Control-plane's `git_credentials` and the clone flow converge onto reliant connections in a later phase (§6). |
| 5 | Org-shared connections | User-owned only in v1. The schema reserves `owner_kind` / `org_id`, and the DEK is keyed by *tenant* rather than by user, so org ownership later is a policy change and not a re-encryption. |
| 6 | Plaintext today | `api_keys.api_key` is plaintext (`internal/db/postgres/settings_store.go:142-153`). Fix it roll-forward through the same vault: expand, backfill, switch readers, contract. **Correction to the brief:** control-plane's `git_credentials.access_token` is **already sealed** with AES-GCM (`internal/db/postgres.go:5779`). Only a legacy plaintext pass-through remains (`postgres.go:5741-5757`). |

The rest of this document justifies those choices, argues against each one, and
turns them into a phased plan.

---

## 1. What exists today (verified)

### 1.1 control-plane: the OpenBao secret store

- **What it is for.** `internal/secretstore/contract.go:1-39` describes a store
  for **deploy-environment secrets**. The path is
  `tenants/<org_id>/<deploy_environment_id>/<name>`. The org segment is
  resolved server-side from the `deploy_environments` row (`contract.go:8-16`,
  `:81-94`). Scope is an *environment*, not a user and not a connection.
- **Its properties:**
  - It coerces values to strings so that the audit log HMACs them
    (`contract.go:18-22`, test `secretstore_test.go:206`).
  - It returns no value (`contract.go:28-32`).
  - It is nil without `BAO_ADDR` (`internal/app/providers.go:314-318`,
    `:1853-1869`).
- **Bao principals** (`deploy/openbao/bootstrap.sh`):
  - The API server's `control-plane-service` policy has `create, update` on
    `secret/data/*` and **no `read`** (`bootstrap.sh:314-330`).
  - Values are read only by:
    - the `deployer` principal (Kubernetes auth, used by the Workload
      controller), and
    - `local-secret-reader`, confined to `secret/data/local/*`
      (`bootstrap.sh:419-425`).
- **The existing reveal precedent is `internal/localsecret`.** It is a
  *separate package* with a *separate Bao principal* and a *separate KV root*
  (`localsecret/contract.go:1-28`). That is the pattern the brief means by
  "a reveal path must be separately granted".
- **Who reads a value, and when.** The value is resolved at **deploy** time
  and becomes a k8s Secret; a running pod never talks to Bao
  (`docs/openbao-secret-store.md` §7). The store was designed around rare,
  batch reads, not a read per HTTP call.
- **Operational state.** The design note marks storage, audit shipping and DR
  as open (`openbao-secret-store.md` §5 "an unresolved gap", §6, §9). In prod,
  Cloud Logging is off, so `file_path=stdout` audit "is destroyed on pod
  restart" (`:602-622`).

### 1.2 control-plane: `pkg/crypto` envelope and `git_credentials`

- `pkg/crypto/aesgcm.go` is AES-256-GCM under a **versioned keyring**:
  - The blob is self-describing: `CPK1 ‖ idLen ‖ keyID ‖ nonce ‖ ct`
    (`aesgcm.go:24-60`).
  - `EncryptWithAAD` / `DecryptWithAAD` bind ciphertext to a context
    (`:137`, `:177`).
  - New writes always use the primary key (`:130-136`).
  - The keyring is env `LLM_KEY_ENCRYPTION_KEY` (`pkg/crypto/keyring.go:27`).
- `git_credentials.access_token` and `refresh_token` are **sealed at the
  persistence boundary** (`internal/db/postgres.go:5776-5795`;
  migration `00109_git_credential_refresh_tokens.up.sql:12-13`).
  - `decodeGitCredentialToken` (`postgres.go:5741-5757`) still accepts
    non-base64 values as legacy plaintext, so **legacy rows may still be
    plaintext**. Whether any exist in prod is **[unverified]**.
  - `ENGINE_SPLIT_PLAN.md:330` ("stored plaintext") and `INTEGRATIONS.md:313`
    are therefore stale on this point.
- The GitHub flow already does several things this design needs:
  - signed state with a 16-byte nonce and an expiry (`gitcredential/oauth.go:84-143`)
  - refresh (`oauth.go:351`)
  - `MarkGitCredentialNeedsReconnect` on a rejected refresh
    (`gitcredential/contract.go:27-30`)
  - App-awareness: an empty scope means "installation-governed"
    (`oauth.go:310-320`)

  It does **not** do PKCE (no verifier or `S256` in `oauth.go`), and its state
  is signed and stateless, so a state token is replayable until it expires
  **[inferred from the struct; single-use not checked]**.

### 1.3 reliant

- `api_keys` (`internal/db/postgres/schema.sql:104-111`) stores `api_key text`
  in plaintext (`settings_store.go:142-153`). It holds:
  - BYO provider keys
  - the minted LLM key (`SyncReliantProvider`, `internal/grpc/services/settings.go:1625`)
  - the per-daemon `daemon:resume` `rlat_` tokens under
    `reliant-automation:<daemonID>` (`internal/db/core/settings.go:82-87`;
    `internal/automationcred/automationcred.go:22-62`)
- reliant has no at-rest encryption at all. `rg 'cipher.NewGCM|aes.NewCipher'`
  finds no hits under `internal/`, `pkg/` or `cmd/`.
- Temporal history is not encrypted. `internal/temporal/data_converter.go:63-79`
  installs only a claim-check codec. `UserJWT` already rides in activity
  inputs (`internal/workflow/runtime/context.go:67-71`). That is the
  pre-existing exposure `DELEGATED_CREDENTIAL.md` §7 flags.
- Self-hosted mode: `tokenauthority.New` returns `ModeLocal` when
  `ControlPlaneURL == ""` (`internal/tokenauthority/authority.go:50-66`).
- `proto/reliant/v1/connector.proto` already exists and means the reverse
  direction (`INTEGRATIONS.md:279-282`). The new service is
  `ConnectionService` in `connection.proto`.

---

## 2. Where secrets live

### 2.1 The forces

1. **Read frequency.** Integration secrets are read on *every* action call,
   and refresh writes happen roughly hourly per OAuth connection. The Bao store
   was built for reads at deploy time.
2. **Self-hosted parity.** Self-hosted reliant (`ModeLocal`) has no
   control-plane and no Bao, yet GitHub, Slack and HTTP connections must still
   work there. Whatever we pick needs a reliant-local implementation.
3. **Tenant model mismatch.** Bao paths are keyed by control-plane
   `org_id`/`deploy_environment_id` (`secretstore/contract.go:81-94`).
   Connections are keyed by reliant `user_id` (later `org_id`). The user-id
   mapping between the two systems is itself an open question
   (`DELEGATED_CREDENTIAL.md` §13.5).
4. **Blast radius of the reader.** Whichever process can turn a
   `connection_id` into plaintext is the crown jewel. Today that process is
   the reliant worker under every option, because it makes the outbound call.
   The question is only where the *decrypt authority* sits.
5. **Audit and DR maturity.** Bao's audit is not durable in prod today
   (§1.1). A Postgres audit table is durable now.

### 2.2 Options

**(a) Reuse control-plane's OpenBao.** Add a `connections/<user>/<conn_id>`
KV root, plus a new `connection-revealer` principal and RPC for the reliant
worker, modelled on `localsecret`.

- For:
  - reviewed crypto
  - Bao's audit device records every reveal
  - one secret store for the company
  - the "separately granted reveal" pattern already exists (`localsecret`)
- Against:
  - **Every action call becomes a cross-service RPC** (reliant worker →
    control-plane → Bao), so control-plane availability gates every
    integration call. Today it gates only daemon wake and LLM.
  - Self-hosted needs a **second implementation anyway**, so (a) never removes
    the reliant-side vault. It adds one.
  - The reveal RPC must authenticate "the reliant worker acting for user U".
    That means either:
    - a service secret plus a body `user_id`, which `DELEGATED_CREDENTIAL.md`
      §12 rejected, or
    - a per-user `rlat_` minted with a new `connection:reveal` scope, which
      turns a long-lived bearer into a skeleton key for every connection the
      user has.
  - Bao's ops gaps (audit shipping, Raft DR rehearsal) become blockers for
    integrations.
  - Refresh writes go through the `writer` path, and the `writer` path cannot
    read back. The refresh flow therefore needs both read and write, which
    breaks the "no single principal reads and writes" split the store was
    built around.

**(b) A reliant-side envelope vault.**

- Layout:
  - a `vault_keys` table holding one DEK per tenant, wrapped by a KEK
  - a `connection_secrets` table holding AES-256-GCM ciphertext, with AAD
    bound to `(connection_id, tenant_id, field)`
- The KEK sits behind an interface:
  `KeyWrapper{ Wrap(ctx, dek) ; Unwrap(ctx, wrapped, keyID) }`. Planned
  implementations:
  - `envkeyring`: v1, all modes. A versioned keyring in an env var with the
    same grammar as control-plane's `LLM_KEY_ENCRYPTION_KEY`.
  - `gcpkms`: later, prod-only, if the KEK should leave the namespace.
  - `baotransit`: later, if the company standardizes on Bao.
- For:
  - one code path for hosted and self-hosted
  - no new runtime dependency on each call
  - audit in the same transaction as the access
  - AAD stops row transplant
  - the per-tenant DEK lets a tenant be crypto-shredded by deleting one row
- Against:
  - We own the crypto. `ENGINE_SPLIT_PLAN.md:328` warns against n8n's
    "instance-wide key with no tenant isolation", and
    `openbao-secret-store.md` §8 states the same trade.
  - In v1 the KEK is an env var in the same k8s namespace as the DB
    credentials, so anyone who reads Secrets in the namespace can decrypt.
    `openbao-secret-store.md` §2 accepted exactly this for Bao's static seal
    with the same reasoning: the namespace is already the trust boundary.
    That makes it consistent, not strong.

**(c) Hybrid.** Store ciphertext in reliant's DB (b), and do KEK wrap and
unwrap through Bao **Transit** in control-plane (`transit/encrypt`,
`transit/decrypt`), with a local keyring in self-hosted mode.

- For: the KEK never exists in reliant's memory or env, and every DEK unwrap
  is audited by Bao.
- Against:
  - It needs a Transit mount and policy that do not exist yet (**[unverified]**
    that Transit is enabled; `bootstrap.sh` converges only KV-v2 policies).
  - It inherits Bao's availability on cache miss.
  - It adds a cross-repo deploy dependency for little gain over (b), because
    the DEK must still be cached in worker memory for performance. A
    compromised worker therefore gets DEKs either way.

### 2.3 Recommendation: (b), with (c) as a pluggable upgrade, not a v1 dependency

Option (b) is the only one that serves both modes with one implementation. Its
weakness, a KEK co-located with the data, is a property of the key wrapper,
and the `KeyWrapper` seam turns that into a configuration choice instead of a
redesign.

- **Reuse, don't reinvent.**
  - Port control-plane's `pkg/crypto` envelope format (versioned key id in the
    blob, AAD binding, primary-key-only sealing). Prefer importing it if
    control-plane's module is importable from reliant **[unverified:
    reliant's `go.mod` has no control-plane require; a copy or an upstream to
    `forge/pkg` is likely needed]**.
  - **Best: upstream it into `forge/pkg/crypto`**, so that control-plane and
    reliant share one reviewed AEAD envelope. That is what
    `forge project libraries` exists for, and it follows the
    "fix it in forge" rule.
- **Two-level keys.** The KEK wraps a per-tenant DEK, and the DEK seals
  values. Rotating the KEK re-wraps N small rows and never re-encrypts
  secrets. Rotating a DEK is a per-tenant backfill.
- **Argument against my own choice.** The company already decided to host
  OpenBao precisely so that it would *not* own crypto
  (`openbao-secret-store.md` §8: "I would make the same call"). Option (b)
  partly reverses that for a second class of secret. My answer:
  - That decision was about a KV store with versions, ACLs and read-back
    semantics. Here we need only seal and open, plus one AEAD and one wrap.
    Control-plane already ships exactly that (`pkg/crypto`) for
    `git_credentials` and the LLM key. We would be reusing crypto the company
    already owns, not adding new crypto.
  - If the user prefers that *all* KEK material live in Bao, choose (c) and
    keep everything else in this document unchanged. That is the purpose of
    the seam. **This is open question Q1.**

### 2.4 Self-hosted mode

- The vault is identical. `KeyWrapper` = `envkeyring`, reading
  `RELIANT_VAULT_KEYRING` (format `v1:<base64-32B>[,v2:...]`, primary first).
- **No key configured** (the decided behaviour):
  - Connections refuse to be created, with a clear
    `FailedPrecondition("vault key not configured")`.
  - Hosted boot **fails** when `RELIANT_CONTROL_PLANE_URL` is set and no
    keyring exists.
  - Self-hosted boot only warns, because connections are optional there.
  - Never fall back to plaintext.
- **First-run convenience:** `reliant` can generate a keyring into its data
  dir with mode `0600` when none exists. This is self-hosted only, and it
  carries a loud warning that losing the file loses every connection.
  Forge-declared secrets (`forge.ExternalSecret`, generatable) cover the
  hosted case **[confirm reliant's KCL declares secrets the same way;
  unverified]**.

### 2.5 Migrating today's plaintext (roll-forward only)

**`api_keys` (reliant).** Expand, backfill, switch, contract. There is no down
file.

1. Migration A (expand): add `api_key_sealed bytea NULL` and
   `vault_key_id text NULL`. Writers dual-write the sealed column, and readers
   prefer sealed, falling back to plaintext.
2. Backfill: a one-shot, idempotent backfill run at boot, batched with
   `WHERE api_key_sealed IS NULL`. It is safe to re-run.
3. Release N+1: readers use sealed only. A plaintext-only row becomes an
   explicit error, and "re-enter your key" surfaces in settings.
4. Migration B (contract): `UPDATE api_keys SET api_key = ''`, then later
   drop the column.

AAD is `("api_keys", user_id, provider)`. The automation `rlat_` rows are
covered by the same backfill. **The api_keys path must not depend on the
`connections` feature shipping.** It is a standalone hardening that lands in
phase 1a.

**`git_credentials` (control-plane).**

- These are already sealed. The remaining work is to retire the legacy
  plaintext pass-through:
  - a backfill that re-seals any row `decodeGitCredentialToken` reads as
    plaintext (`postgres.go:5741-5746`), then
  - deletion of that branch, which its own comment authorises (`:5733-5734`).
- Their long-term home is §6.

---

## 3. Worker call-time resolution

### 3.1 The path

```
action activity input:  { run_id, node_id, uses, with, connection: "conn_abc" | "$gh" }
        │   (no secret, no token, no owner claim the activity trusts)
        ▼
connections.Resolver.ForCall(ctx, runID, ref)
   1. owner   := runs.OwnerOf(runID)                 ← DB, not activity input
   2. connID  := resolve ref via INTEGRATIONS §4.3 order
   3. conn    := connections.Get(connID)
      require conn.status == active
      require conn.owner_kind == user && conn.user_id == owner   (v1)
      require conn.integration_id == manifest.integration
   4. src     := tokensource.For(conn)               ← single-flight refresh (§4.4)
   5. sec     := src.Token(ctx)  → vault.Secret      ← plaintext lives only here
   6. authenticator(manifest.connection.kind).Apply(req, sec)
   7. connection_events.append(used, conn, run, node)   (async, batched)
```

### 3.2 Rules that make "never in history, inputs or logs" structural

- **Type, not discipline.** `vault.Secret` is a struct with an unexported
  `[]byte`.
  - `String()`, `GoString()`, `Format()`, `MarshalJSON()`, `MarshalText()` and
    `LogValue()` (slog) all return `"[redacted]"`.
  - The only way out is `secret.Use(func([]byte) error)`, which zeroes a copy
    afterwards.
  - Authenticators take a `vault.Secret` and write directly into an
    `*http.Request` header.
  - A reflection test asserts that no activity input or output type, and no
    proto message under `proto/reliant/v1`, has a field of type
    `vault.Secret` or `[]byte` named `*secret*|*token*` (allow-list based,
    copied from control-plane's no-value reflection test referenced in
    `secretstore/contract.go:28-32`).
- **The owner comes from the DB, not from the input.** If the activity input
  carried `user_id`, a crafted workflow or a history edit could name another
  user. `runs.OwnerOf(runID)` binds authority to the run record, which only
  launch writes.
- **The resolved `connection_id` is recorded on the node or tool output**
  (`INTEGRATIONS.md:341`). Ids are not secrets.
- **Response scrubbing.** Before returning output into history, the
  authenticator's applied header values are removed from:
  - any echoed request (error bodies sometimes echo the request), and
  - `httpaction` error messages.

  This is done by registering the plaintext with a per-call
  `redact.Set` that string-replaces it in output and error text. It is a
  belt-and-braces measure, tested separately.
- **The HTTP client never logs headers.** `httpaction` uses a transport that
  strips `Authorization`, `Cookie`, `X-Api-Key` and every header the manifest
  marks `secret: true` from OTel span attributes and debug logs.

### 3.3 Who authenticates the worker's reveal?

There is **no reveal RPC** under the recommendation. The worker already has
DB access to the run, and it holds the vault keyring, so the authorisation
check is the in-process step 3 above.

Credentials the worker presents:

| To | Credential | Why |
|---|---|---|
| reliant Postgres (`connection_secrets`, `vault_keys`) | the worker's existing DB role | Same as today. Separating it into a dedicated role is phase 4 hardening (§9). |
| KEK (`envkeyring`) | env var, mounted only on **worker** and **api-server** pods | The api-server needs to *seal* (create and OAuth callback). The worker needs to *open*. See the split below. |
| a third-party API | the connection's token | Never the user's JWT or `rlat_` (`INTEGRATIONS.md:319-321`). |
| control-plane | none for connections | — |

**Seal/open split (recommended hardening, phase 1b).** The api-server only
needs to *encrypt*. Make sealing asymmetric with a per-tenant X25519 "sealing
key":

- `vault_keys` stores the public half in the clear, and the private half
  sealed by the KEK.
- The api-server seals new values with the public key (HPKE base mode) and
  **never holds the KEK**.
- Only the worker can open.
- A compromised api-server, which is the internet-facing process, can then
  write connections but cannot read any existing secret.

Cost: one HPKE dependency (`github.com/cloudflare/circl/hpke`, or Go 1.24+
`crypto/hpke` **[unverified which Go version reliant pins]**). Argument
against: OAuth **refresh** happens in the worker, and the worker must re-seal,
so the worker holds both halves regardless. The gain is only "api-server
compromise ≠ read". I think that is worth it, because the api-server is the
public ingress and runs the webhook receivers. **This is open question Q3.**

### 3.4 Unattended runs: "may this run act as user U?"

- **What decides it.** The run's owner is fixed at launch:
  - for a trigger fire, `trigger.user_id`
  - for a chat run, the chat's user

  The resolver uses that owner. No bearer is needed, because nothing leaves
  the worker toward control-plane on this path.
- **Where the `rlat_` still matters.** The `rlat_` from
  `DELEGATED_CREDENTIAL.md` still governs *waking the daemon*, and that is
  unchanged. It is **not** a key to connections. `INTEGRATIONS.md:320` says
  "the `rlat_` only answers may this unattended run act as U (fetching U's
  connection)". I am narrowing that: with an in-reliant vault, possession of
  the run record already proves it, and making connections depend on a
  bearer token the worker could forge in-process adds nothing.
- **Revocation hook.** A trigger run must not use connections after the
  owner's account is disabled or deleted. The resolver checks
  `users.disabled_at IS NULL` **[confirm the column exists; unverified]**.
  Connection rows cascade on user delete.
- **If option (a) or (c) is chosen instead,** the reveal or unwrap call to
  control-plane must authenticate as "reliant worker acting for user U" with
  a **per-user, `connection:use`-scoped, daemon-unbound `rlat_`**. That means
  minting a second automation token next to `daemon:resume`, with the same
  mint, store and `BearerFor` pattern as `automationcred.Resolver`
  (`automationcred.go:46-62`). It is extra machinery that (b) avoids, and is
  one more reason for (b).

---

## 4. Schema, service and OAuth broker

### 4.1 Tables (reliant Postgres, one goose migration via `make migration`)

```sql
-- One wrapped DEK per tenant per key version.
CREATE TABLE vault_keys (
  id            text PRIMARY KEY,               -- "vk_..."
  tenant_kind   text NOT NULL CHECK (tenant_kind IN ('user','org')),
  tenant_id     text NOT NULL,
  version       int  NOT NULL,
  kek_id        text NOT NULL,                  -- which KEK wrapped it ("env:v1", "kms:...")
  wrapped_dek   bytea NOT NULL,
  seal_pubkey   bytea NULL,                     -- §3.3 asymmetric sealing (phase 1b)
  state         text NOT NULL CHECK (state IN ('primary','decrypt_only','destroyed')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_kind, tenant_id, version)
);
CREATE UNIQUE INDEX vault_keys_one_primary ON vault_keys (tenant_kind, tenant_id) WHERE state = 'primary';

CREATE TABLE connections (
  id              text PRIMARY KEY,             -- "conn_..."
  owner_kind      text NOT NULL DEFAULT 'user' CHECK (owner_kind IN ('user','org')),
  user_id         text NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- creator; owner when owner_kind='user'
  org_id          text NULL,                    -- reserved (§7); CHECK below
  integration_id  text NOT NULL,                -- "github"
  auth_kind       text NOT NULL CHECK (auth_kind IN ('oauth2','github_app_user','api_key','basic','none')),
  name            text NOT NULL,
  account_label   text NULL,                    -- "octocat"
  external_account_id text NULL,                -- provider's stable id (GitHub user id, Slack team id)
  scopes          text[] NOT NULL DEFAULT '{}',
  oauth_client    text NULL,                    -- which registered client minted it (rotation, §4.5)
  status          text NOT NULL CHECK (status IN ('active','needs_reauth','revoked')),
  status_reason   text NULL,                    -- "invalid_grant", "user_revoked" (never provider body)
  is_default      boolean NOT NULL DEFAULT false,
  access_expires_at timestamptz NULL,
  last_used_at    timestamptz NULL,
  created_at, updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at      timestamptz NULL,
  CHECK ((owner_kind = 'user' AND org_id IS NULL) OR (owner_kind = 'org' AND org_id IS NOT NULL))
);
CREATE UNIQUE INDEX connections_one_default ON connections (user_id, integration_id)
  WHERE is_default AND deleted_at IS NULL AND owner_kind = 'user';
CREATE UNIQUE INDEX connections_name ON connections (user_id, integration_id, name) WHERE deleted_at IS NULL;

-- Ciphertext only. Separate table so no metadata query can SELECT it by accident.
CREATE TABLE connection_secrets (
  connection_id text NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
  field         text NOT NULL CHECK (field IN ('access_token','refresh_token','api_key','password','client_secret')),
  vault_key_id  text NOT NULL REFERENCES vault_keys(id),
  ciphertext    bytea NOT NULL,                 -- envelope; AAD = conn_id‖field‖tenant
  generation    bigint NOT NULL DEFAULT 1,      -- CAS for refresh (§4.4)
  updated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (connection_id, field)
);

-- Single-use OAuth flow state (server-side; replaces stateless signed state).
CREATE TABLE oauth_flows (
  state_hash     bytea PRIMARY KEY,             -- sha256(state); raw state never stored
  user_id        text NOT NULL,
  session_id_hash bytea NOT NULL,               -- binds to the browser session (§4.3)
  integration_id text NOT NULL,
  pkce_verifier_sealed bytea NOT NULL,          -- sealed with the user's DEK
  redirect_after text NULL,                     -- validated relative path
  reconnect_connection_id text NULL,
  expires_at     timestamptz NOT NULL,          -- 10 min
  consumed_at    timestamptz NULL
);

-- Append-only audit (insert-only grant for the app role; §9).
CREATE TABLE connection_events (
  id bigserial PRIMARY KEY, connection_id text NOT NULL, user_id text NOT NULL,
  kind text NOT NULL,      -- created, used, refreshed, refresh_failed, needs_reauth, revoked, deleted, default_changed
  run_id text NULL, node_id text NULL, tool_call_id text NULL,
  actor text NOT NULL,     -- "user:<id>", "worker", "trigger:<id>"
  at timestamptz NOT NULL DEFAULT now()
);
```

Refinements over `INTEGRATIONS.md` §4.1, with reasons:

- `secret_ref` becomes the **`connection_secrets` table**. A ref string into
  some external store is unnecessary under (b), and a separate table means a
  `SELECT *` on `connections` cannot return ciphertext.
- `owner_kind` + `org_id` + `CHECK`: §7.
- `external_account_id`: dedupes "connect the same GitHub account twice", and
  routes app-level webhooks (installation and team id) to connections.
- `generation`: the compare-and-swap that makes refresh races safe.
- `oauth_flows`: single-use, session-bound state. Today's signed stateless
  state (`gitcredential/oauth.go:84-143`) cannot be made single-use.
- `auth_kind` gains `github_app_user`, a user-to-server token from a GitHub
  App with expiring and refreshable tokens. `oauth_app_installation` is not a
  user credential: installation tokens are minted on demand from the App
  private key (§6) and are never stored per connection.

### 4.2 `ConnectionService` (`proto/reliant/v1/connection.proto`)

| RPC | Notes |
|---|---|
| `ListConnections({integration_id?})` | Metadata only. |
| `GetConnection(id)` | Metadata only. |
| `CreateApiKeyConnection({integration_id, name, fields: map<string,string>})` | Values are write-only. The request message is the *only* proto carrying a secret, and it is annotated `debug_redact = true` (protobuf field option) so that `prototext` and logging interceptors redact it **[verify the connect logging interceptor honours `debug_redact`; unverified]**. |
| `StartOAuth({integration_id, name?, reconnect_id?})` → `{authorize_url}` | Creates an `oauth_flows` row. |
| `CompleteOAuth({state, code})` | Used for Electron/deeplink. The browser path is the HTTP callback (§4.3). |
| `TestConnection(id)` → `{ok, account_label, error_class}` | Runs the manifest's `test` request via the worker path. |
| `RenameConnection`, `SetDefaultConnection` | |
| `DeleteConnection(id)` | Revokes at the provider where an API exists (best effort), then deletes. |
| `ListConnectionEvents(id, page)` | The audit view. |

**No RPC returns a value.** `TestConnectionServiceResponsesCarryNoSecretFields`
reflects over every response message and fails on any field outside an
allow-list. That is the same structural pin as control-plane's secret store.
Authorization: every handler loads the row and requires
`row.user_id == auth.GetUserID(ctx)`, which yields `NotFound`, never
`PermissionDenied`, so that ids are not an oracle (same rule as
`secretstore/contract.go:103-108`).

### 4.3 OAuth broker (api-server)

HTTP routes: `GET /integrations/oauth/{provider}/start` (also reachable via
`StartOAuth`) and `GET /integrations/oauth/{provider}/callback`.

1. **Start.** Generate a 32-byte `state` and a 64-byte PKCE verifier, with
   `code_challenge = S256(verifier)`. Insert into `oauth_flows`:
   `state_hash`, `session_id_hash = sha256(session id or JWT sid)`, the
   sealed verifier, and `expires_at = now()+10m`. Also set a `__Host-`
   prefixed, `HttpOnly`, `SameSite=Lax` cookie that carries a flow binder for
   web, then redirect.
2. **Callback.** Run `UPDATE oauth_flows SET consumed_at = now() WHERE
   state_hash = $1 AND consumed_at IS NULL AND expires_at > now() RETURNING
   ...`, so a state can be used exactly once. Then require that the
   callback's session or cookie matches `session_id_hash`. This blocks
   login-CSRF, where an attacker's code is attached to the victim's account.
3. **Exchange** server-side, with `code_verifier`, against the manifest's
   **catalog-fixed** `token_url` (§8.4). Request a short timeout and cap the
   response size.
4. **Identify:** `GET` the manifest `test` endpoint, which yields
   `external_account_id` and `account_label`.
5. **Seal and upsert** in one transaction: the `connections` row,
   `connection_secrets`, and a `connection_events(created)` row. On
   `reconnect_id`, the existing row is updated in place and `status` returns
   to `active`.
6. **Redirect** to the validated `redirect_after` (relative paths only, the
   same rule as `safeReturnTo` in `gitcredential/oauth.go`).

Electron: the `StartOAuth`/`CompleteOAuth` RPC pair, with the session binding
done by the authenticated RPC caller instead of a cookie.

### 4.4 Worker `TokenSource`, with single-flight refresh

```
tokensource.For(conn).Token(ctx):
  if cached && !expiringWithin(60s): return cached
  singleflight.Do(conn.id):                              ← in-process dedupe
    tx: SELECT ... FROM connection_secrets WHERE connection_id=$1 FOR UPDATE   ← cross-process dedupe
        if row.generation advanced since our read → another worker refreshed; use it
        resp := POST token_url grant_type=refresh_token
        on invalid_grant (RFC 6749 §5.2) or GitHub "bad_refresh_token":
            UPDATE connections SET status='needs_reauth', status_reason='invalid_grant'
            event(needs_reauth); emit user_update for the UI; return ErrNeedsReauth (permanent)
        on 5xx/timeout: return retryable error, status unchanged
        seal(new access, new refresh if rotated) ; generation++ ; event(refreshed)
```

- The row lock serializes refreshes across worker replicas. Without it,
  providers that rotate refresh tokens (GitHub Apps, Slack with rotation)
  invalidate the loser's token, and the connection falls spuriously into
  `needs_reauth`.
- The in-memory cache is keyed by `(connection_id, generation)`, has a TTL of
  at most the token lifetime, and drops the entry on `DeleteConnection`
  (via a `pg_notify('connection_changed', id)` listener) **[a LISTEN
  connection in the worker is new; acceptable?]**.
- A 401 from the provider on a *fresh* token triggers one forced refresh,
  then `needs_reauth`.
- Activity error classification: `ErrNeedsReauth` is a non-retryable
  `ApplicationError` type `ConnectionNeedsReauth`, so Temporal does not burn
  retries against it.

### 4.5 OAuth client secrets

Client ids and secrets for Slack, Linear and others are **deployment config**,
not connections: env and forge `secret_ref` per environment, read by the
api-server (exchange) and the worker (refresh). They are not stored in the
vault. In self-hosted mode the operator registers their own apps.
`connections.oauth_client` records which client minted a grant, so that a
rotated client is detectable.

---

## 5. Key lifecycle

| Event | Action |
|---|---|
| KEK rotation | Add `v2` to the keyring as primary, then re-wrap every `vault_keys` row (`kek_id` changes, DEKs do not). Remove `v1` from the keyring once `SELECT count(*) WHERE kek_id='env:v1' = 0`. |
| DEK rotation (per tenant) | Insert a new primary version, demote the old one to `decrypt_only`, and backfill-reseal that tenant's `connection_secrets`. On completion, mark the old one `destroyed` and zero `wrapped_dek`. |
| Tenant deleted | Delete that tenant's `vault_keys` rows. Every ciphertext they sealed, including those in backups, becomes unrecoverable (crypto-shredding). |
| KEK lost | Every connection is lost. Users reconnect. Unlike Bao's root key, nothing else depends on it. The KEK is still **durable per environment** and never regenerated, following the Zitadel masterkey precedent (`openbao-secret-store.md` §3). |

## 6. GitHub App ownership (INTEGRATIONS §11 Q2)

### Today

Control-plane owns a GitHub OAuth/App client:

- `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` (`internal/app/mounts.go:122-138`)
- `GITHUB_APP_SLUG` (`gitcredential.go:293`)

It stores per-user tokens in `git_credentials` and uses them for
`CloneRepo`, which enqueues a `git.clone` command to the daemon
(`git_credential.proto:29`; `gitcredential/contract.go:46-80`). Reliant calls
it through `controlplane.Client.CloneRepoOntoDaemon`
(`reliant/internal/controlplane/client.go:88-96,174`;
`internal/grpc/services/project_from_repo.go:107`).

### Options

1. **Control-plane owns the App.**
   - Reliant fetches tokens from control-plane per call, which is option (a)'s
     cross-service read in disguise.
   - Self-hosted reliant has no GitHub integration at all, or needs a second
     App path.
   - The webhook endpoint must live in control-plane and forward events to
     reliant triggers.
2. **Reliant owns the App (recommended).**
   - One registered App. Its private key and webhook secret are reliant
     deployment secrets.
   - The webhook endpoint `POST /integrations/github/events` is on the
     reliant api-server (`INTEGRATIONS.md` §5.2).
   - User-to-server tokens become `connections` rows (`github_app_user`).
   - Installation tokens are minted on demand from the App key, cached until
     about 5 minutes before their one-hour expiry, and never stored.
   - Self-hosted runs its own App with the same code.
3. **Two Apps** (control-plane for clone, reliant for integrations). The user
   installs and authorizes twice, and repo access drifts between the two. This
   is the status-quo trajectory, and the worst UX.

### Recommendation: reliant owns it

The clone flow converges in phase 5:

- `CloneRepoOntoDaemon` gains a reliant-resolved token. Either reliant
  resolves the user's GitHub connection and passes a **short-lived
  installation token scoped to the one repo**
  (`POST /app/installations/{id}/access_tokens` with `repositories:[name]`,
  `permissions:{contents:read}`), or reliant enqueues the clone to the daemon
  itself. Either way, control-plane stops holding a long-lived user token.
- `git_credentials` is then retired roll-forward:
  1. stop writing it
  2. a one-time import into reliant `connections` (decrypt with
     control-plane's keyring in a job that has both keys; **this is the only
     step that needs both repos' keys at once**)
  3. switch reads
  4. drop it in a later migration
- **Argument against:**
  - Control-plane's flow works today, has refresh and needs-reconnect, and
    already holds the App relationship. Moving it is work with no immediate
    user-visible gain.
  - A per-repo installation token passed to the daemon is still a bearer on
    the daemon, which a compromised daemon could misuse (§8.2). It is narrower
    than today's long-lived user token, though.
- **Mitigation:** do not migrate clone in phase 1. Phase 1 ships GitHub
  connections in reliant using the **same App registration** that
  control-plane uses (same client id, both callbacks registered). The user
  then installs once, and the two token stores coexist until phase 5.
  **[Verify that control-plane's client is a GitHub App and not an OAuth
  App:** `oauth.go:310-320` handles both. If it is an OAuth App, register a
  new App for reliant and migrate clone onto it.**]**

---

## 7. Org-shared connections (INTEGRATIONS §11 Q1)

**v1: user-owned only.** Each connection is usable only by runs whose owner
is `user_id`.

What the schema reserves so that org ownership is additive:

- `owner_kind` / `org_id`, guarded by a `CHECK` (§4.1). The v1 resolver
  rejects `owner_kind='org'` outright, so a row inserted early cannot be used.
- `vault_keys.tenant_kind` / `tenant_id`. An org connection gets the org's
  DEK, so leaving the org or deleting it is crypto-shredding by tenant and
  not by user.
- `connection_events.actor`, so shared use is attributable per user from day
  one.

**The v2 sketch (not built):**

- A connection with `owner_kind='org'` is usable by runs whose owner is a
  member with role ≥ `connection_user`. Managing it needs `connection_admin`.
- Workflows reference org connections only through **bindings** (§4.3 step 2),
  never by hard-coded id, so the share and the grant stay explicit.
- The GitHub App *installation* is naturally org-level. In v2, triggers route
  by `installation_id` to an org connection.

---

## 8. Threat model

| # | Threat | What it gets | Mitigations | Residual |
|---|---|---|---|---|
| 8.1 | **Stolen worker credential** (worker pod env plus DB creds) | Under (b): every connection of every tenant, because the worker holds the KEK. This is the dominant risk, and it is the same under every option, because the worker must hold plaintext to call the API. | KEK only on the worker and api-server pods (and with §3.3 sealing, the api-server cannot open); a dedicated DB role for `connection_secrets` (phase 4); short-lived OAuth access tokens, so stolen ones expire; per-tenant DEKs, so a *DB-only* theft (backup, replica, SQL injection) yields nothing without the KEK; `connection_events` makes misuse auditable. Option (c) moves the KEK into Bao, but cached DEKs still live in worker memory. | Accepted. The only real fix is a separate egress-signing service (an "integration proxy" that holds tokens and makes the calls). Noted as a v2 option, not v1. |
| 8.2 | **Compromised daemon** | Nothing, for server-placed actions: secrets never leave the worker. For `daemon`-placed manifest actions (`INTEGRATIONS.md` §3.3: "credentials … passed to the daemon per call"), the token for that call. | **v1 rule: integration actions that need a connection may not be `daemon`-placed.** The catalog loader rejects `placement: daemon` together with `connection` (a fail-first test). If that is ever needed, pass a *derived* narrow token (for example a repo-scoped GitHub installation token, 1h), never the refresh token. The clone flow (§6) uses exactly that. | A daemon sees tool *outputs*, which may contain provider data. That is by design. |
| 8.3 | **Malicious workflow author** shares a workflow whose nodes say `connection: conn_author` | Nothing. The resolver checks `conn.user_id == runs.OwnerOf(run)`; it does not trust the workflow. A foreign id answers the same as a missing one: `FailedPrecondition("no github connection")`. | Fail-first test: user B runs A's shared workflow with A's connection id hard-coded → error, and zero `used` events on A's connection. The same applies to trigger rows: a trigger's `connection_bindings` are validated at create time *and* at fire time against the trigger owner. | — |
| 8.4 | **SSRF via OAuth endpoints** | If `authorize_url`/`token_url` came from user input, the api-server or worker would POST secrets to arbitrary hosts, including `169.254.169.254` and cluster services. | v1: `token_url`, `authorize_url`, `revoke_url` and `base_url` come **only from embedded catalog manifests**, never from a request or a DB row. The `generic HTTP` connection (`api_key`, `basic`) has no OAuth. Its *request* URL is user-supplied, so `httpaction` uses a dialer that resolves and then rejects private, loopback, link-local and CGNAT addresses on **every connection, including redirects** (checked at dial time, which defeats DNS rebinding), with redirects capped at 3. User-authored manifests (`INTEGRATIONS.md` §11 Q4) are forced to daemon placement *and* cannot reference connections in v1. | Self-hosted operators may want internal hosts; provide `RELIANT_HTTP_ALLOW_PRIVATE_CIDRS`. |
| 8.5 | **Log, trace and history leakage** | Tokens in Temporal history, slog, OTel spans, error strings or provider error echoes. | `vault.Secret` cannot be formatted or serialized (§3.2); the activity-type reflection test; header stripping in the transport; per-call redaction of error and output text; `debug_redact` on `CreateApiKeyConnection.fields`; OAuth `code`, `state` and `code_verifier` never logged (the callback logs only `state_hash[:8]`). A **canary test** creates a connection whose secret is a random sentinel, runs an action that fails, then greps the Temporal history export, the captured slog output and the span exporter for the sentinel. It must find zero hits. | `UserJWT` in history (`runtime/context.go:67`) is a pre-existing, separate exposure; out of scope, flagged. |
| 8.6 | **Login CSRF / OAuth state replay** | An attacker's account is linked into the victim's reliant, so the victim's agent writes to the attacker's repo. | Single-use `oauth_flows`, session-bound (§4.3); PKCE. | — |
| 8.7 | **Row transplant** (attacker with DB write swaps ciphertexts between connections) | Victim's run uses the attacker's token or the reverse. | AAD = `connection_id‖field‖tenant`; a mismatched open fails. Fail-first test. | — |
| 8.8 | **Prompt-injected agent exfiltrates via an integration** | Data leaves through a legitimately connected tool. | Out of scope here. `INTEGRATIONS.md` §6 covers it (capability ceiling, parameter binding). The vault guarantees only that the agent never *sees* a token: tools receive params, never auth. | — |

---

## 9. Phased implementation plan

| Phase | Deliverable | Files (new unless noted) |
|---|---|---|
| **1a: Envelope library + `api_keys` hardening** | The AEAD envelope (port of control-plane `pkg/crypto`), upstreamed to forge so both repos share it; reliant `internal/vault` (`KeyWrapper`, `envkeyring`, per-tenant DEK, `Secret` type); `api_keys` expand migration, dual-write, boot backfill | **forge:** `pkg/crypto/envelope.go` (+ tests), released. **reliant:** `internal/vault/{contract.go,vault.go,envkeyring.go,secret.go,secret_test.go}`; `internal/db/migrations/postgres/<ts>_vault_keys_and_api_keys_sealed.sql`; edit `internal/db/postgres/settings_store.go:132-175` (seal and open); `internal/db/postgres/schema.sql`; `make sqlc`; config wiring for `RELIANT_VAULT_KEYRING` in api-server/worker setup and the control-plane KCL `deploy/kcl/*/main.k` (`forge.ExternalSecret`, generatable, durable). **control-plane:** switch `pkg/crypto` to the forge import (no format change: `CPK1` must remain readable). |
| **1b: Connections core** | Tables (§4.1), `ConnectionService`, OAuth broker, `TokenSource`, resolver, audit events, asymmetric sealing (if Q3 = yes) | `proto/reliant/v1/connection.proto`; migration `<ts>_connections.sql`; `internal/db/core/connection.go`; `internal/db/postgres/connection_store.go`; `internal/connections/{contract.go,service.go,oauth.go,tokensource.go,resolver.go,authenticator.go,events.go}`; `internal/grpc/services/connection.go`; HTTP routes in `internal/serverapi` (callback); worker wiring next to `automationcred` in workersetup. |
| **1c: First consumers** | GitHub (`github_app_user`) and generic HTTP (`api_key`/`basic`) connections; `TestConnection`; settings UI list, connect, reconnect and delete | `internal/integrations/catalog/{github,http}/manifest.yaml` (connection block only); `web/src/components/settings/Connections*.tsx`. The action node is INTEGRATIONS phase 2. |
| **1d: control-plane cleanup** | Re-seal legacy plaintext `git_credentials` rows; delete the pass-through branch | control-plane: a backfill (boot task or Job) plus edits to `internal/db/postgres.go:5711-5757`; test that a plaintext row is re-sealed. |
| **4 (hardening)** | Dedicated Postgres role: only the worker can `SELECT connection_secrets`; `connection_events` insert-only; optional `gcpkms` or `baotransit` `KeyWrapper` | migration (GRANTs), deploy KCL. |
| **5 (convergence)** | GitHub clone via a reliant connection; import and retire `git_credentials`; retire control-plane's GitHub OAuth routes | both repos; §6. |

### Rollout order

1. forge: release the envelope package.
2. control-plane: switch to the forge `pkg/crypto`. This is behaviour-neutral;
   pin it with existing tests plus a golden `CPK1` blob test. Deploy.
3. reliant 1a: deploy with the keyring secret provisioned *first* (preflight
   blocks otherwise). Dual-write, then backfill, then confirm
   `count(api_key_sealed IS NULL) = 0`.
4. reliant 1b and 1c behind a `connections` feature flag in the UI. The
   server side ships dark.
5. reliant 1a contract: readers sealed-only, then blank the plaintext.
6. control-plane 1d (independent; any time).
7. Phases 4 and 5 later.

---

## 10. Test plan (each must be shown to fail before the code exists)

**Vault (`internal/vault`)**

- Seal and open round-trip. Opening with the wrong AAD fails
  (`TestOpenRejectsTransplantedCiphertext`).
- Every formatting path of `Secret` redacts: `fmt %v %+v %#v %s`, `json`,
  `slog`, `errors.Join`.
- An unknown key id in the blob gives a distinct `ErrUnknownKey`, not a
  generic auth failure.
- Sealing always uses the primary key. Opening succeeds under `decrypt_only`
  and fails under `destroyed`.
- KEK rotation: wrap under v1, add v2 primary, rewrap, remove v1; every
  secret still opens.
- The golden `CPK1` blob produced by control-plane opens with the forge
  package (format compatibility).

**`api_keys` migration**

- A pre-migration plaintext row is readable after backfill, and
  `api_key_sealed` is set.
- The backfill is idempotent.
- After the contract step, a plaintext-only row yields `ErrReenterKey`, not
  the plaintext.
- `GetProviderAPIKeys` still excludes `reliant-automation:%`
  (`settings_store.go:163`).

**Connections**

- `TestConnectionServiceResponsesCarryNoSecretFields` (reflection allow-list).
- Cross-user: `Get`, `Delete`, `SetDefault` and `Test` on another user's id
  return `NotFound`.
- Resolver: a run owned by B with A's connection id gives
  `FailedPrecondition` and zero `used` events on A's connection (§8.3). This
  covers the explicit id, the binding and the trigger-row binding.
- Resolver ignores any `user_id` in the activity input (feed a forged one and
  assert it is unused).
- `owner_kind='org'` rows are rejected by the v1 resolver.
- Activity input and output types contain no `vault.Secret` or
  secret-named `[]byte`/`string` (reflection).
- **Canary leak test (§8.5).** An end-to-end action failure with a sentinel
  secret; the sentinel is absent from Temporal history, slog capture and the
  span exporter.

**OAuth**

- Reused `state` → rejected. Expired → rejected. State from session X
  completed in session Y → rejected.
- PKCE: the token request carries the `code_verifier` matching the
  challenge (fake provider asserts it).
- `token_url` comes only from the catalog: `StartOAuth` with a request-level
  URL field fails to compile (the field does not exist), and the manifest
  loader rejects a non-HTTPS `token_url`.

**TokenSource**

- 20 concurrent `Token()` calls on an expiring token cause exactly 1 refresh
  POST (single-flight).
- Two `TokenSource` instances (simulated replicas) on one DB cause exactly 1
  refresh, and the loser reads the winner's generation.
- `invalid_grant` → status `needs_reauth`, a `needs_reauth` event, a
  non-retryable `ConnectionNeedsReauth` error. A 503 → retryable, status
  unchanged.

**SSRF**

- The HTTP action against `127.0.0.1`, `169.254.169.254`, `10/8`, an IPv6
  ULA, and a hostname that resolves to private on its second lookup
  (rebinding), plus a redirect to private: all refused.
- The catalog loader rejects `placement: daemon` plus `connection`.

**control-plane 1d**

- A legacy plaintext `git_credentials` row is re-sealed by the backfill.
  After the branch is deleted, a plaintext row returns
  `ErrGitCredentialUndecryptable` and is not passed through.

---

## 11. Unverified, and open questions for the user

### Unverified (resolve during implementation)

1. Whether any prod `git_credentials` rows are still plaintext (§1.2).
2. Whether control-plane's GitHub client is a GitHub App or an OAuth App (§6).
3. Whether control-plane's `pkg/crypto` can be imported from reliant, or
   needs an upstream to forge (§2.3).
4. Whether reliant's Connect logging honours `debug_redact` (§4.2).
5. Whether a `users.disabled_at`-style column exists in reliant (§3.4).
6. Reliant's Go version, for `crypto/hpke` (§3.3).
7. Whether OpenBao Transit is enabled (only relevant if Q1 = (c)).

### Questions for the user (each with a recommendation)

- **Q1. Vault backend.** (b) reliant envelope vault with an env-keyring KEK,
  or (c) the same vault with the KEK in control-plane's OpenBao Transit?
  **Recommend (b) now**, with the `KeyWrapper` seam so (c) is a later config
  change. (a), Bao KV with a reveal per call, is not recommended.
- **Q2. GitHub App ownership.** **Recommend reliant**, sharing the existing
  App registration in phase 1, with clone converging in phase 5.
- **Q3. Asymmetric sealing**, so the api-server can write but not read
  (§3.3). **Recommend yes, in phase 1b.** It costs one dependency, and it
  removes the read capability from the internet-facing process.
- **Q4. Daemon-placed actions with connections.** **Recommend forbidding
  them in v1** (§8.2).
- **Q5. Org connections.** **Recommend user-owned for v1**, with the columns
  reserved (§7).
- **Q6. Hosted boot without a keyring.** **Recommend failing hard** in hosted
  mode and warning in self-hosted mode (§2.4).
