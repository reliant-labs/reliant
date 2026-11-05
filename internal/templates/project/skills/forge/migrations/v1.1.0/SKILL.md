---
name: v1.1.0
description: Upgrade a project to forge v1.1.0. A `--kind cron` worker now runs its schedule on forge/pkg/cronkit, which claims each tick in the app's Postgres (table forge_cron_ticks, created on first use) so it fires on ONE replica instead of on every replica; `const PerReplica = true` opts a worker back into running everywhere. Workers scaffolded earlier keep firing on every replica until converted. Use when internal/workers/*/worker.go imports github.com/robfig/cron/v3. Also: forge/pkg/releasehealth.ServiceHealth gained an Endpoints slice, so it is no longer comparable with ==; use when Go code imports forge/pkg/releasehealth. Also: a scaffolded UpdateX over a table owned by its own primary key (COMMENT ON COLUMN <t>.id IS 'forge:owner') stamped the key, moving an update aimed at another row onto the caller's; use when a migration declares forge:owner on an id column. Also: create-request factories over a table whose row or FK parents declare forge:owner take FactoryOwner(owner), seeding the parents under the owner a scoped handler stamps; use when a migration declares forge:owner. Also: the scaffolded login broker no longer serves public sign-up (POST /auth/register) outside development unless the config sets auth_self_signup, because a self-registered address is unverified and an app that binds invitations by email is open to account takeover; a broker scaffolded earlier mounts it everywhere and must be edited by hand (forge lint warns: forgeconv-open-signup). Use when internal/app/login_broker.go mounts RegisterPath unconditionally. Also: a client update_mask naming a forge:immutable column, or a column with no wire field, is now InvalidArgument instead of writing it (an OAuth subject rewritten, a sealed secret reset to NULL); use when a migration declares forge:immutable or a CRUD Update overrides op.ReadOnly. Also: forge/pkg/schemadef.ForeignKey gained KeyColumns / RefKeyColumns slices (a composite foreign key's whole key), so it is no longer comparable with ==; use when Go code imports forge/pkg/schemadef. Also: a List request field can declare a range or prefix filter with (forge.v1.filter) = {column, op}, which the generated List op applies, so a List hand-written only to filter by a time window or a key prefix can go back to the generated op; use when a handler file builds its own crud.ListOp.
version: v1.1.0
detection: grep -rqs --include='*.go' --exclude='*_test.go' 'github.com/robfig/cron/v3' internal/workers && exit 0; grep -rqs --include='*.go' --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules 'forge/pkg/releasehealth"' . && exit 0; grep -rqs --include='*.up.sql' 'forge:owner' db/migrations && exit 0; grep -qs 'mux.Handle(RegisterPath' internal/app/login_broker.go && ! grep -qs 'if broker.selfSignup' internal/app/login_broker.go && exit 0; grep -rqs --include='*.up.sql' 'forge:immutable' db/migrations && exit 0; grep -rqs --include='*.go' --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules 'forge/pkg/schemadef"' . && exit 0; exit 1
---

# Upgrading to v1.1.0

## 1. Cron workers fire once per tick, not once per replica

**Use when** a worker under `internal/workers/` imports `github.com/robfig/cron/v3` (it was scaffolded with `--kind cron` before v1.1.0).

### What changed

A cron worker runs inside a long-running workload, so with two replicas every
replica ran every tick: an hourly poller polled twice an hour. A newly
scaffolded cron worker runs on `forge/pkg/cronkit` instead. Before each tick
it inserts (worker name, scheduled tick time) into `forge_cron_ticks` in the
app's Postgres with `ON CONFLICT DO NOTHING`; the replica whose insert lands
runs the tick and the others skip it (a DEBUG log and
`forge_cron_ticks_total{outcome="skipped"}`). The table is bookkeeping that
cronkit creates on first use (`CREATE TABLE IF NOT EXISTS`) as the app's own
role, the one that already runs the migrations at boot. No migration to write.

Nothing changes on upgrade by itself: `worker.go` is yours, and forge never
rewrites it. An unconverted worker keeps firing on every replica.

### Decide per worker

- The job must run once per tick (a poller, a digest, a sweep that calls a
  third party): convert it, below, with `PerReplica = false`.
- The job is meant to run on every pod (an in-process cache refresh, a local
  temp-dir sweep): convert it with `PerReplica = true`, or leave it as it is.

### Convert

1. In `Deps`, add the ORM handle the ticks are claimed on (skip this when the
   project has no `db/migrations`; the worker then fires on every replica and
   says so at boot):

   ```go
   // DB is where ticks are claimed, so a tick fires once across replicas.
   // forge:optional-dep
   DB orm.Context
   ```

2. Beside `const Schedule`, declare the replica policy:

   ```go
   const PerReplica = false
   ```

3. Replace the robfig/cron scheduler: drop the `cron`, `baseCtx` and
   `baseCancel` fields, and make `Start` block in cronkit:

   ```go
   func (w *Worker) Start(ctx context.Context) error {
       return cronkit.Run(ctx, cronkit.Options{
           Name:       w.Name(),
           Schedule:   Schedule,
           PerReplica: PerReplica,
           Claims:     cronkit.Postgres(w.deps.DB),
           Logger:     w.deps.Logger,
       }, w.Run)
   }

   func (w *Worker) Stop(ctx context.Context) error { return nil }
   ```

4. Make `Run` return an error, and drop its own span and `recover`: cronkit
   runs each tick under a root span (`cron.<name>.tick`), logs the returned
   error and recovers a panic. `cronkit.TickTime(ctx)` is the scheduled time,
   the same on every replica, for keying idempotent writes.

   ```go
   func (w *Worker) Run(ctx context.Context) error { … }
   ```

   Update `worker_test.go` where it calls `w.Run(ctx)` (`_ = w.Run(ctx)`).

5. `forge generate`. The new `DB` field resolves to the ORM by type
   (`DB: g.ORM(),` in compose.go's `new<Worker>`); in a legacy `NewComponents`
   project, set it there yourself (`DB: infra.ORM` or what your Infra names
   it). Then `go mod tidy` if robfig/cron is now only indirect.

`Name()` is the claim key: keep it stable, since a renamed worker starts a
fresh claim history (harmless: the next tick is claimed under the new name).

### Behaviour to know

- At most once per tick: a replica that crashes mid-tick loses that tick, and
  a claim that fails (database down) skips the tick there, logged at ERROR
  (`outcome="claim_error"`).
- Ticks on one replica never overlap; a tick that outlives its period skips
  the ticks it covered. robfig/cron used to start an overlapping run instead.
- `@every <d>` is aligned to multiples of d, so it no longer counts from
  process start.

The `workers` skill (Cron-Scheduled Worker) is the full contract.

## 2. `releasehealth.ServiceHealth` is no longer comparable

**Use when** Go code imports `github.com/reliant-labs/forge/pkg/releasehealth`.

### What changed

`ServiceHealth` gained `Endpoints []EndpointHealth`: release health now judges
p95 latency per endpoint (`ServiceHealth.LatencyRatio`), so a window that
merely holds a different mix of endpoints does not read as the service getting
slower. A struct with a slice field cannot be compared with `==` or used as a
map key.

### What to do

Nothing, unless your code compares two `ServiceHealth` values with `==`/`!=`
or keys a map by one; the compiler names each such site. Compare the fields
you mean instead (`a.Service == b.Service`), or `reflect.DeepEqual` in a test.
Leaving `Endpoints` nil keeps the old service-level latency verdict.

## 3. A table owned by its own key: UpdateX stamped the key

**Use when** a migration declares the primary key as the owner column, e.g.
`COMMENT ON COLUMN tenants.id IS 'forge:owner'` (a table whose rows are owned by
themselves).

### What changed

The owner-scoping wrapper forge scaffolds into `handlers_crud.go` stamps the
owner column onto an Update's entity, `e.<Owner> = owner`. When the owner column
is the key, that line is `e.ID = owner`: an update aimed at another row was
silently moved onto the caller's own, and came back as that row's constraint
error (`already_exists … tenants_slug_key`) rather than NotFound. A newly
scaffolded Update over such a table predicates on the key and never stamps it.

`handlers_crud.go` is yours, so forge does not rewrite an existing one.

### What to do

In each `Update<Entity>` of such a table, delete the `op.Entity` override that
assigns `e.ID = owner`, together with its `build := op.Entity` line. Keep the
`op.Persist` (and `op.PersistMasked`) override: its `orm.WhereEq("id", owner)`
is what scopes the write. `forge project audit` prints the corrected wrapper
for an unscoped one.

### Also new: declaring the owner at birth

`// forge:owner` on a proto field (`// forge:read-only forge:owner` above
`string tenant_id = 2;`) writes the `COMMENT ON COLUMN … IS 'forge:owner'` into
the entity's birth migration, so `forge scaffold` births `handlers_crud.go`
already scoped. A `forge:read-only` NOT NULL owner column no longer needs
`forge:fill=handler` while its Create is authenticated (the wrapper stamps it);
a `forge:fill=handler` added only to get past "can never be populated" can stay
or go.

## 4. Create-request factories seed parents under the caller's owner

**Use when** a migration declares `forge:owner` on a column.

### What changed

A scoped Create stamps its row's owner from the caller, while
`New<Create>Request` seeded the row's FK parents under owners the seed planner
invented. With a tenants table, the created row referenced a tenant that did not
exist; with a same-owner guard (a composite `(tenant_id, id)` key, a trigger),
it crossed owners. A factory whose table or parents declare `forge:owner` now
takes `FactoryOwner(owner)`: every parent's owner column, and every reference to
one, is seeded under that owner, and the request's own owner field is set to it.
Without the option it seeds exactly as before, and existing calls compile
unchanged.

### What to do

`handlers_crud_test.go` is yours, so forge does not edit an existing one. Where
a lifecycle test creates a row through a scoped handler, pass the owner the
handler derives from the test's claims:

```go
req := orders.NewCreateOrderRequest(t, db, 0, orders.FactoryOwner("test-user"))
```

"test-user" is what the scaffolded wrapper's `claims.UserID` placeholder yields
for `crudTestCtx()`. If you disabled a same-owner guard in the lifecycle test to
get past this, you can turn it back on.

## 5. Public sign-up is off outside development

**Use when** `internal/app/login_broker.go` mounts `RegisterPath` with no `if`
around it (`forge lint` reports `forgeconv-open-signup` at the line).

### What changed

The scaffolded broker used to mount `POST /auth/register` in every
environment, and outside development it signs the new account straight in
with an address nobody verified (`EmailVerified: false`). An app that binds
an invitation, a tenant membership or a pre-created record to a user by email
is then open to account takeover: register the victim's address, sign in, and
claim what was waiting for them.

A broker scaffolded since v1.1.0 serves sign-up in development only, unless
the deployed environment's config sets `auth_self_signup: true`, and serves
`GET /auth/options` so the sign-in screen offers "Create an account" only when
sign-up is served. `devidp.User` gained `EmailVerified`, read from the
issuer by `FindUserByID` / `FindUserByEmail`. `login_broker.go` and the
sign-in screen are yours, so forge does not rewrite them.

### Edit

1. Declare the opt-in on the config the broker reads (`proto/config/.../config.proto`,
   on the message `login_broker.go` takes; use its next free field number),
   then run `forge generate`:

   ```proto
   bool auth_self_signup = <next free tag> [(forge.v1.config) = {
     env_var: "AUTH_SELF_SIGNUP"
     flag: "auth-self-signup"
     default_value: "false"
     description: "Serve public sign-up (POST /auth/register) in a deployed environment; development always serves it. A self-registered address is UNVERIFIED: make every bind-by-email require devidp.User.EmailVerified before setting this."
   }];
   ```

2. In `internal/app/login_broker.go`, add the constant, the struct field, and
   its value in `RegisterLoginBroker`:

   ```go
   OptionsPath        = "GET /auth/options" // in the const block beside RegisterPath
   ```

   ```go
   type loginBroker struct {
       // ...
       // selfSignup is whether RegisterPath is served at all.
       selfSignup bool
   }
   ```

   ```go
   broker := &loginBroker{
       // ...
       devMode:    cfg.Environment == "development",
       selfSignup: cfg.Environment == "development" || cfg.AuthSelfSignup,
       // ...
   }
   ```

3. Replace the unconditional mount:

   ```go
   mux.Handle(RegisterPath, http.HandlerFunc(broker.handleRegister))
   ```

   with:

   ```go
   if broker.selfSignup {
       mux.Handle(RegisterPath, http.HandlerFunc(broker.handleRegister))
   }
   mux.Handle(OptionsPath, http.HandlerFunc(broker.handleOptions))
   ```

   and add the handler:

   ```go
   // authOptions is what the sign-in screen asks before it renders.
   type authOptions struct {
       SelfSignup bool `json:"selfSignup"`
   }

   // handleOptions reports whether sign-up is served. Public: anyone could
   // learn it by trying.
   func (b *loginBroker) handleOptions(w http.ResponseWriter, _ *http.Request) {
       writeAuthJSON(w, http.StatusOK, authOptions{SelfSignup: b.selfSignup})
   }
   ```

4. In the sign-in screen (`src/app/auth/sign-in/page.tsx` in Next.js, the
   sign-in route in `src/routes.tsx` in Vite), offer sign-up only when the
   server serves it. Add to `src/lib/auth/native-login.ts`:

   ```ts
   export interface AuthOptions {
     selfSignup: boolean;
   }

   // Fails closed: an older server, an outage or a malformed body hides sign-up.
   export async function fetchAuthOptions(): Promise<AuthOptions> {
     try {
       const response = await fetch(`${apiOrigin()}/auth/options`, {
         credentials: "include",
         headers: { Accept: "application/json" },
       });
       if (!response.ok) return { selfSignup: false };
       const body = (await response.json()) as Partial<AuthOptions> | null;
       return { selfSignup: body?.selfSignup === true };
     } catch {
       return { selfSignup: false };
     }
   }
   ```

   then, in the screen, load it once (a `useEffect` like `usePasswordPolicy`'s;
   new projects get `src/lib/auth/use-auth-options.ts`), change
   `const signingUp = mode === "signUp";` to
   `const signingUp = selfSignup && mode === "signUp";`, and wrap the
   "New here? / Create an account" `<p>` in `{selfSignup ? (…) : null}`.

5. **Before setting `auth_self_signup: true` in any deployed env,** make every
   place the app binds something to a user by email require the address
   verified: `idp.FindUserByID(ctx, claims.Subject)` and refuse unless
   `user.EmailVerified`. `forge skill load auth/authorization` ("Binding by
   email requires a verified address") has the code.

### Deciding

- Invite-only product: steps 2-4 are enough; leave `auth_self_signup` unset.
  Deleting the `RegisterPath` mount and the toggle outright also clears the
  lint.
- Anyone may join: do all five steps, then set `auth_self_signup = True` on
  the env's config in `deploy/kcl/<env>/config.k`.

## 6. A client update_mask can no longer write an immutable or wire-less column

**Use when** a migration declares `forge:immutable`, or a CRUD Update override
appends to `op.ReadOnly`.

### What changed

A full-replace `Update<Entity>` keeps a `forge:immutable` column as stored, but
a client `update_mask` naming it was accepted and wrote it — `NULL` or `""` when
the request did not carry it. The same held for a column with no wire field (a
SQL-spelled `forge:secret`): the client could not send its value, so naming it
reset the stored one. The generated Update op now lists both in a new
`op.Unmaskable`, and `crud.HandleUpdate` refuses a mask path naming one with
`InvalidArgument` (reason `unknown_field`) before anything is written.
`db.Update<Entity>Masked` from app code still writes them. The scaffolded edit
form leaves immutable fields off, and the born lifecycle test no longer picks
one as its mutation target.

### What to do

1. Run `forge generate`: `handlers_crud_ops_gen.go` gains the `Unmaskable` line.
2. Remove the hand-written `op.ReadOnly = append(op.ReadOnly, "<col>")` lines
   that worked around this from `handlers_crud.go`; they are now redundant.
3. An edit page scaffolded earlier is yours and still renders an immutable
   field and names it in its `update_mask`, so saving it now fails with
   `InvalidArgument`. Delete that field from the edit form's schema, default
   values and mask paths.
4. If a client was meant to set an immutable column through Update (an admin
   reassigning an owner), remove it from `op.Unmaskable` in that op's override
   (`db/crud-overrides`).

## 7. `schemadef.ForeignKey` is no longer comparable

**Use when** Go code imports `github.com/reliant-labs/forge/pkg/schemadef`.

### What changed

`ForeignKey` gained `KeyColumns []string` and `RefKeyColumns []string`: a
composite constraint (`FOREIGN KEY (tenant_id, strategy_id) REFERENCES
strategies (tenant_id, id)`) is still reported as one entry per column, and
each entry now carries the whole key, so a reader can tell that all its columns
name the same parent row. Both are nil for a single-column reference.
Introspection also pairs the key's columns by position: a composite used to
come back as the cross product of its columns (strategy_id → strategies.tenant_id
as well as → strategies.id), and now yields exactly one entry per column. A
struct with a slice field cannot be compared with `==` or used as a map key.

### What to do

Nothing, unless your code compares two `ForeignKey` values with `==`/`!=` or
keys a map by one; the compiler names each such site. Compare the fields you
mean instead (`a.Column == b.Column && a.RefTable == b.RefTable`), or
`reflect.DeepEqual` in a test. Code that deduplicated the old cross-product
rows of a composite can drop that workaround.

## 8. A List can filter by a range or a prefix without being hand-written

**Use when** a handler file builds its own `crud.ListOp` (a copy of the
generated List op) only to filter by a time window, a "from" bound or a key
prefix, because a generated List filter could only be `col = $1`.

### What changed

A List request field can declare `(forge.v1.filter) = {column, op}`. `column`
names the compared column, and `op` is one of `LIST_FILTER_OP_GT`, `GTE`,
`LT`, `LTE` or `PREFIX`. The generated List op applies it with
`orm.WhereGte` and the rest, or with `orm.WherePrefix`, which matches `%`, `_`
and `\` literally. Fields without the annotation work exactly as before.

### What to do

Nothing is required. To drop a hand-written List: annotate its request fields
(`forge skill load proto/list-filters`), delete the hand-written method, and
run `forge generate`, which wires the RPC to its generated op again.
