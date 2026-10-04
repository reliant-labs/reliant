# Connections and the credential vault (integrations phase 1)

**Status:** design only. Nothing here is implemented. This document is the
"vault is its own design" that `INTEGRATIONS.md` §4.2 defers to. Read it with
`INTEGRATIONS.md` §4 and §11, `DELEGATED_CREDENTIAL.md` §7–§9,
`ENGINE_SPLIT_PLAN.md` ("New: `CredentialService`", line 323) and
`TOOL_PLACEMENT.md` §5.

Line numbers cite reliant at `f35fce86` and control-plane at `53604402`.
Anything I could not verify is marked **[unverified]** and collected in §11.

---

## Terms

- **Connection.** A user's saved, authorized login to an external service:
  their GitHub account via OAuth, or a pasted Linear API key. Workflows and
  agents use it to call that service *as the user*.
- **Secret.** The token or key inside a connection: an OAuth access or refresh
  token, an API key, a password. Stored only encrypted.
- **Encryption key (KEK, "key-encryption key").** The one master key, held in a
  Kubernetes Secret and given to the server as an env var. It never encrypts
  secrets directly. It encrypts DEKs. This document does **not** mean an OS
  keyring. It may hold more than one key *version* during a rotation (§5);
  each ciphertext names the version that sealed it.
- **DEK (data-encryption key).** A random per-tenant key, stored in the
  database encrypted by the encryption key. DEKs encrypt the secrets. With two
  levels, rotating the encryption key re-wraps a few small rows, and deleting
  a tenant's DEK makes all of its secrets unrecoverable.
- **api-server vs worker.** Both are reliant server processes. The
  **api-server** answers the UI and API and receives OAuth callbacks and
  webhooks; it is internet-facing. The **worker** runs workflow steps
  (Temporal activities), including the actual calls to GitHub, Slack and so on.
  Neither can see the user's filesystem.
- **Daemon-placed action.** A workflow step or tool that runs on the user's
  machine (or their cloud workspace) through the reliant daemon, rather than
  on the server.

---

## 0. Decision summary

Decisions taken by the user on 2026-10-04 are marked **Decided**.

| # | Question | Answer |
|---|---|---|
| 1 | Where secrets live | **Decided.** Per-connection ciphertext goes in reliant Postgres. One encryption key sits in a k8s Secret, declared through forge as a `forge.EnvVar {secret_ref, secret_key}` exactly like control-plane's `LLM_KEY_ENCRYPTION_KEY` (`control-plane/deploy/kcl/dev/main.k:1184`). It wraps per-tenant DEKs. The envelope is versioned (key id in the ciphertext header), so rotation means adding a new key id to the same Secret. **No OpenBao and no KMS in v1.** |
| 2 | Worker call-time path | The activity input carries only `connection_id`. The worker resolves the run owner **from the DB**, checks ownership and decrypts in process into a `vault.Secret` that cannot be logged or serialized. No `rlat_` is involved (§3). |
| 3 | Who writes and who reads | **Decided: no asymmetric sealing in v1.** The api-server and the worker mount the same key. The api-server writes; the worker writes on refresh and is the only reader (§3.3). |
| 4 | Schema and RPCs | Metadata (`connections`) is split from ciphertext (`connection_secrets`). There are also `vault_keys`, `oauth_flows` and `connection_events`. `ConnectionService` cannot return a value, and a reflection test pins that (§4). |
| 5 | GitHub App owner | **Decided: reliant** (§6). |
| 6 | Daemon-placed actions with connections | **Decided: forbidden in v1.** A step that runs on the user's machine cannot use a connection, so tokens never leave the server (§8.2). |
| 7 | Org-shared connections | **Decided: per-user only in v1**, with `owner_kind`/`org_id` reserved (§7). |
| 8 | Key missing at boot | **Decided: a hosted server refuses to start.** Self-hosted generates a key on first boot, persists it and warns (§2.4). |
| 9 | Plaintext today | `api_keys.api_key` is plaintext (`internal/db/postgres/settings_store.go:142-153`). It is migrated roll-forward through the vault (§2.5). Control-plane's `git_credentials` is already sealed (`internal/db/postgres.go:5779`); only a legacy plaintext pass-through remains (`:5741-5757`). |

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

- `pkg/crypto/aesgcm.go` is AES-256-GCM under a **versioned set of encryption keys** (control-plane's code calls it a "keyring"; it is an env var, not an OS keyring):
  - The blob is self-describing: `CPK1 ‖ idLen ‖ keyID ‖ nonce ‖ ct`
    (`aesgcm.go:24-60`).
  - `EncryptWithAAD` / `DecryptWithAAD` bind ciphertext to a context
    (`:137`, `:177`).
  - New writes always use the primary key (`:130-136`).
  - The key set is env `LLM_KEY_ENCRYPTION_KEY` (`pkg/crypto/keyring.go:27`).
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

### 2.1 Decision (2026-10-04): one k8s-Secret encryption key, ciphertext in Postgres

- **The encryption key** is one 32-byte key, versioned as `v1:<base64>`. It is
  delivered to the reliant api-server and worker as env `RELIANT_VAULT_KEY`,
  declared in the deploy KCL exactly the way control-plane declares its own:

  ```kcl
  forge.EnvVar {name = "RELIANT_VAULT_KEY", secret_ref = "reliant-vault", secret_key = "vault_key"}
  ```

  - The precedent is `LLM_KEY_ENCRYPTION_KEY` →
    `secret_ref = "control-plane-secrets"` (`control-plane/deploy/kcl/dev/main.k:1184`).
  - The value is set with `forge secret set --env <env> <KEY>`, checked with
    `forge secret ensure --env <env>` (`main.k:824-826`), and rendered into
    the cluster from the same store (`main.k:829-833`).
  - Reliant's workloads are declared in control-plane's KCL, not in reliant;
    reliant has no `deploy/` KCL of its own. Their shared env lives in
    `control-plane/deploy/kcl/lib/env.k`: `reliant_base_env` at `:224` reaches
    both api-server and worker, and `reliant_api_env` at `:307`. For dev host
    processes it lives in `deploy/kcl/dev/main.k`: `_reliant_host_env` at
    `:1303`, and `_reliant_worker_host_env` at `:1417`, derived from it. The
    new `EnvVar` goes in `reliant_base_env` and `_reliant_host_env` (§9).
- **Ciphertext** stays in reliant Postgres: `connection_secrets` and
  `vault_keys` (§4.1).
- **The envelope** is versioned, as in control-plane's `pkg/crypto`: the key id
  lives in the ciphertext header (`aesgcm.go:24-60`), AAD binds a ciphertext
  to its row, and only the primary key seals. Rotation means adding `v2` to
  the same Secret value (`v2:…,v1:…`, primary first), re-wrapping, then
  dropping `v1` (§5).
- **OpenBao and KMS are out of v1.** The `KeyWrapper` interface around
  wrap/unwrap leaves room to move the encryption key into Bao Transit or Cloud
  KMS later without touching stored secrets.

### 2.2 Why not one k8s Secret per connection

Putting each connection's token directly in a k8s Secret looks simpler, but
it fails on three counts:

1. **Volume.** There would be one Secret per user per integration: thousands,
   all in one namespace, listed and watched by kubelet and controllers.
2. **Churn.** OAuth access tokens expire hourly and refresh-token rotation
   rewrites them. Each refresh would be a k8s API write, which turns the
   apiserver/etcd into a hot OLTP store.
3. **Privilege.** The worker would need RBAC to *create and update Secrets*
   in its namespace. That is a far larger capability than reading one env var:
   it could overwrite the DB credentials or the encryption key itself.

So k8s holds the **one** key, which is static, set by forge and readable only
as an env var, and Postgres holds the many rows of ciphertext, which are
transactional, auditable and cascade with the user.

### 2.3 Alternatives considered (not chosen for v1)

- **Control-plane's OpenBao KV with a per-call reveal.** Rejected:
  - every integration call would become a control-plane round-trip
  - self-hosted would still need this vault
  - the worker's reveal would need a broad per-user `rlat_` or a service
    secret (`DELEGATED_CREDENTIAL.md` §12)
  - Bao's audit shipping and DR are unresolved in prod
    (`openbao-secret-store.md` §5, §9)
- **Encryption key in Bao Transit or Cloud KMS.** Deferred by decision. The
  gain over a k8s Secret is real but small while the namespace is already the
  trust boundary (the same argument `openbao-secret-store.md` §2 accepted for
  Bao's static seal).

**Argument against the decision.** Anyone who can read Secrets in the reliant
namespace, or the worker's environment, can decrypt every connection. That is
true, and it is the same exposure the namespace already has for DB
credentials and the Stripe key. Per-tenant DEKs still mean a database-only
leak (backup, replica, SQL injection) yields nothing.

### 2.4 Missing key at boot (decided 2026-10-04)

- **Hosted** (`RELIANT_CONTROL_PLANE_URL` set, i.e. not `ModeLocal`,
  `internal/tokenauthority/authority.go:50-66`): **the api-server and worker
  refuse to start** without a parseable `RELIANT_VAULT_KEY`. Declaring it as a
  `secret_ref` also makes forge's deploy preflight fail before rollout.
- **Self-hosted** (`ModeLocal`): there is no forge-managed secret.
  - Options: (i) refuse to start, which breaks first-run for everyone, even
    those who never use connections; (ii) disable connections until the
    operator sets a key, which is a scavenger hunt; (iii) **generate and
    persist**.
  - **Chosen: (iii).** If `RELIANT_VAULT_KEY` is unset, on first boot generate
    32 random bytes, write `v1:<base64>` to `<data dir>/vault.key` with mode
    `0600` (create-exclusive, so two processes cannot both generate), and log
    a WARN: "generated vault key at …; back it up — losing it loses every
    saved connection".
  - On later boots, read the file. If the env var is set, it wins, so an
    operator can move to their own secret management.
  - If the DB already holds `vault_keys` rows but no key is found, **refuse to
    start**. Generating a fresh key would silently orphan every connection.
  - This is cleaner than (i) or (ii) because it follows the "working defaults,
    not empty ones" rule and never loses data silently.
  - **[unverified]** That the self-hosted api-server and worker share one data
    dir. If they run on separate hosts, the operator must set the env var, and
    the second process hits the refuse-to-start check above.

### 2.5 Migrating today's plaintext (roll-forward only)

**`api_keys` (reliant).** Expand, backfill, switch, contract. There is no down
file.

1. Migration A (expand): add `api_key_sealed bytea NULL`. Writers dual-write,
   and readers prefer sealed, falling back to plaintext.
2. Backfill: idempotent, run at boot, batched on
   `WHERE api_key_sealed IS NULL`.
3. Release N+1: readers use sealed only. A plaintext-only row becomes an
   explicit error, and "re-enter your key" surfaces in settings.
4. Migration B (contract): blank `api_key`, then drop it later.

AAD is `("api_keys", user_id, provider)`. This covers the
`reliant-automation:<daemon>` `rlat_` rows too. It does not depend on the
`connections` feature shipping (phase 1a).

**`git_credentials` (control-plane).** These are already sealed. The remaining
work is to re-seal any legacy plaintext rows and delete the pass-through
(`postgres.go:5741-5746`), which its own comment authorises (`:5733-5734`).
Their long-term home is §6.

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

### 3.3 Who writes and who reads (decided 2026-10-04: no asymmetric sealing in v1)

Secrets are written to reliant Postgres (`connection_secrets`), always
encrypted. **Writers:**

- the **api-server**, when an OAuth callback completes or a user pastes an
  API key
- the **worker**, when it refreshes an expiring OAuth token

**Reader:** only the **worker**, at the moment it makes a call. No RPC
returns a value, so the api-server never decrypts anything.

Both processes mount the same encryption key (`RELIANT_VAULT_KEY`). Limiting
the api-server to "can encrypt, cannot decrypt" (per-tenant public sealing
keys) is a possible later hardening. It adds a column and changes how *new*
values are sealed; existing ciphertext never needs re-encrypting.

There is **no reveal RPC**. The worker already has DB access to the run, and
it holds the encryption key, so authorisation is the in-process step 3 of
§3.1.

| To | Credential | Why |
|---|---|---|
| reliant Postgres (`connection_secrets`, `vault_keys`) | the worker's existing DB role | Same as today. A dedicated role is phase 4 hardening (§9). |
| encryption key | env `RELIANT_VAULT_KEY`, from a forge `secret_ref`, on **api-server and worker** only | The api-server seals; the worker seals on refresh and opens. |
| a third-party API | the connection's token | Never the user's JWT or `rlat_` (`INTEGRATIONS.md:319-321`). |
| control-plane | none for connections | — |

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
- If the encryption key ever moves behind a control-plane service (Bao
  Transit), that unwrap call would need a per-user, `connection:use`-scoped
  `rlat_`, following the `automationcred.Resolver` pattern
  (`automationcred.go:46-62`). v1 needs none of that.

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
  kek_id        text NOT NULL,                  -- which encryption-key version wrapped it ("v1")
  wrapped_dek   bytea NOT NULL,
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
  some external store is unnecessary when ciphertext lives in reliant Postgres, and a separate table means a
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
| Encryption key rotation | `forge secret set --env <env> RELIANT_VAULT_KEY 'v2:<new>,v1:<old>'` (primary first) and roll the pods. A boot task re-wraps every `vault_keys` row under `v2` (`kek_id` changes; DEKs and secrets do not). Once `SELECT count(*) FROM vault_keys WHERE kek_id='v1'` is 0, set the Secret to `v2:<new>` alone. |
| DEK rotation (per tenant) | Insert a new primary version, demote the old one to `decrypt_only`, and backfill-reseal that tenant's `connection_secrets`. On completion, mark the old one `destroyed` and zero `wrapped_dek`. |
| Tenant deleted | Delete that tenant's `vault_keys` rows. Every ciphertext they sealed, including those in backups, becomes unrecoverable (crypto-shredding). |
| Encryption key lost | Every connection is lost. Users reconnect. Nothing else depends on it. The key is still **durable per environment** and never regenerated: `forge secret set` must never overwrite it outside a rotation, following the Zitadel masterkey precedent (`openbao-secret-store.md` §3). |

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
   - Reliant fetches tokens from control-plane per call, which is a
     per-call cross-service read.
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
     control-plane's `LLM_KEY_ENCRYPTION_KEY` in a job that has both keys; **this is the only
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
| 8.1 | **Stolen worker credential** (worker pod env plus DB creds) | Every connection of every tenant, because the worker holds the encryption key. This is the dominant risk, and it is the same under every option, because the worker must hold plaintext to call the API. | encryption key only on the worker and api-server (a later api-server encrypt-only hardening is possible, §3.3); a dedicated DB role for `connection_secrets` (phase 4); short-lived OAuth access tokens, so stolen ones expire; per-tenant DEKs, so a *DB-only* theft (backup, replica, SQL injection) yields nothing without the encryption key; `connection_events` makes misuse auditable. Moving the key into Bao/KMS later would not change this, because unwrapped DEKs live in worker memory. | Accepted. The only real fix is a separate egress-signing service (an "integration proxy" that holds tokens and makes the calls). Noted as a v2 option, not v1. |
| 8.2 | **Compromised daemon** | Nothing: secrets never leave the worker. | **Decided 2026-10-04: daemon-placed actions cannot use connections.** A step that runs on the user's machine cannot use a connection, so tokens never leave the server. This overrides `INTEGRATIONS.md` §3.3's "passed to the daemon per call". The catalog loader rejects `placement: daemon` together with `connection` (a fail-first test). If that is ever needed, pass a *derived* narrow token (for example a repo-scoped GitHub installation token, 1h), never the refresh token. The clone flow (§6) uses exactly that. | A daemon sees tool *outputs*, which may contain provider data. That is by design. |
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
| **1a: Envelope library + `api_keys` hardening** | The AEAD envelope (port of control-plane `pkg/crypto`), upstreamed to forge so both repos share it; reliant `internal/vault` (`KeyWrapper` with one implementation, `envkey`; per-tenant DEK; `Secret` type; hosted refuse-to-start and self-hosted generate-on-first-boot, §2.4); `api_keys` expand migration, dual-write, boot backfill; forge KCL `secret_ref` for the key | **forge:** `pkg/crypto/envelope.go` (+ tests), released. **reliant:** `internal/vault/{contract.go,vault.go,envkey.go,localkeyfile.go,secret.go,secret_test.go}`; `internal/db/migrations/postgres/<ts>_vault_keys_and_api_keys_sealed.sql`; edit `internal/db/postgres/settings_store.go:132-175` (seal and open); `internal/db/postgres/schema.sql`; `make sqlc`; config wiring for `RELIANT_VAULT_KEY` in api-server/worker setup. **control-plane KCL (where reliant's workloads are declared):** add `forge.EnvVar {name = "RELIANT_VAULT_KEY", secret_ref = "reliant-vault", secret_key = "vault_key"}` to `deploy/kcl/lib/env.k` `reliant_base_env` (`:224`, reaching the cluster api-server and worker) and to `deploy/kcl/dev/main.k` `_reliant_host_env` (`:1303`; `_reliant_worker_host_env` at `:1417` inherits it); per env, `forge secret set --env <env> RELIANT_VAULT_KEY v1:<base64>`. **[unverified]** whether e2e/prod reliant workloads all draw from `reliant_base_env`; e2e declares env inline (`deploy/kcl/e2e/main.k:671`, `:726`) and may need the line added directly. **control-plane:** switch `pkg/crypto` to the forge import (no format change: `CPK1` must remain readable). |
| **1b: Connections core** | Tables (§4.1), `ConnectionService`, OAuth broker, `TokenSource`, resolver, audit events | `proto/reliant/v1/connection.proto`; migration `<ts>_connections.sql`; `internal/db/core/connection.go`; `internal/db/postgres/connection_store.go`; `internal/connections/{contract.go,service.go,oauth.go,tokensource.go,resolver.go,authenticator.go,events.go}`; `internal/grpc/services/connection.go`; HTTP routes in `internal/serverapi` (callback); worker wiring next to `automationcred` in workersetup. |
| **1c: First consumers** | GitHub (`github_app_user`) and generic HTTP (`api_key`/`basic`) connections; `TestConnection`; settings UI list, connect, reconnect and delete | `internal/integrations/catalog/{github,http}/manifest.yaml` (connection block only); `web/src/components/settings/Connections*.tsx`. The action node is INTEGRATIONS phase 2. |
| **1d: control-plane cleanup** | Re-seal legacy plaintext `git_credentials` rows; delete the pass-through branch | control-plane: a backfill (boot task or Job) plus edits to `internal/db/postgres.go:5711-5757`; test that a plaintext row is re-sealed. |
| **4 (hardening)** | Dedicated Postgres role: only the worker can `SELECT connection_secrets`; `connection_events` insert-only; later, optionally, an api-server encrypt-only split or a Bao/KMS `KeyWrapper` | migration (GRANTs), deploy KCL. |
| **5 (convergence)** | GitHub clone via a reliant connection; import and retire `git_credentials`; retire control-plane's GitHub OAuth routes | both repos; §6. |

### Rollout order

1. forge: release the envelope package.
2. control-plane: switch to the forge `pkg/crypto`. This is behaviour-neutral;
   pin it with existing tests plus a golden `CPK1` blob test. Deploy.
3. reliant 1a: run `forge secret set` for `RELIANT_VAULT_KEY` in each env and
   land the control-plane KCL `secret_ref` *first* (the forge preflight
   blocks otherwise, and hosted reliant refuses to start without it). Dual-write, then backfill, then confirm
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
- Encryption key rotation: wrap under v1, set `RELIANT_VAULT_KEY=v2:..,v1:..`,
  rewrap, then set `v2` alone; every secret still opens.
- Boot: hosted mode with the key unset or unparseable → startup error.
  Self-hosted with no key → `vault.key` created `0600` and a WARN logged; a
  second boot reuses it. Self-hosted with `vault_keys` rows but no key →
  startup error (it never generates over existing data).
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
- The catalog loader rejects `placement: daemon` plus `connection`
  (decided 2026-10-04).

**control-plane 1d**

- A legacy plaintext `git_credentials` row is re-sealed by the backfill.
  After the branch is deleted, a plaintext row returns
  `ErrGitCredentialUndecryptable` and is not passed through.

---

## 11. Decided, unverified, and still open

### Decided (2026-10-04)

1. **Key storage.** One encryption key in a k8s Secret, declared as a forge
   `secret_ref` and set with `forge secret set`. Ciphertext lives in reliant
   Postgres. The envelope is versioned for rotation. No OpenBao and no KMS in
   v1 (§2).
2. **GitHub App.** Reliant owns it (§6).
3. **Asymmetric sealing.** Dropped for v1. The api-server and worker share the
   key (§3.3).
4. **Daemon-placed actions with connections.** Forbidden in v1 (§8.2).
5. **Org connections.** Per-user only in v1; columns reserved (§7).
6. **Missing key at boot.** A hosted server refuses to start. Self-hosted
   generates the key on first boot, persists it to the data dir and warns
   (§2.4).

### Unverified (resolve during implementation)

1. Whether any prod `git_credentials` rows are still plaintext (§1.2).
2. Whether control-plane's GitHub client is a GitHub App or an OAuth App (§6).
3. Whether control-plane's `pkg/crypto` can be imported from reliant, or
   needs an upstream to forge (§2.1, §9).
4. Whether reliant's Connect logging honours `debug_redact` (§4.2).
5. Whether a `users.disabled_at`-style column exists in reliant (§3.4).
6. Whether every hosted reliant workload draws env from `reliant_base_env`,
   or whether e2e and prod need the `secret_ref` added inline (§9).
7. Whether the self-hosted api-server and worker share one data dir, which
   the generated key file depends on (§2.4).

### Still open

- **Phase-5 clone convergence shape.** Should reliant pass a repo-scoped
  installation token to control-plane's `CloneRepo`, or enqueue `git.clone`
  to the daemon itself? **Recommend** passing the token in phase 5. It keeps
  control-plane's daemon command path, and drops only its token custody.
  This is not needed for phase 1.
