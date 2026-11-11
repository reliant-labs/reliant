# Supabase auth events: sign-ups and sign-ins from the auth database

Reliant's users sign in with Supabase, so forge's managed identity reports
nothing for us. We emit forge's standard auth events (`auth_signed_up`,
`auth_signed_in`, `auth_signed_out`, provider `supabase`) the three ways forge
offers for another auth provider:

| Emitter | What it reports | Where |
| --- | --- | --- |
| Browser adapter | `identifyUser` on every signed-in session (the id on spans and replays, and the daily `auth_signed_in {kind: resumed}`), `auth_signed_out` on sign-out | `frontends/web/src/lib/telemetry.ts` `reportAuthChange`, via `@reliantlabs/forge-web-runtime/auth/supabase` |
| API-server heartbeat | `auth_signed_in {kind: resumed}`, once per user per UTC day per process | `internal/grpc/interceptors/auth.go` `trackSession` → `analytics.Tracker.TrackAuthSignedIn` |
| Supabase triggers → hook | `auth_signed_up` (with method and `verified`) and `auth_signed_in {kind: credential}`, from the auth database itself | `deploy/supabase/forge_auth_events.sql` → `POST /_forge/auth/hooks/supabase` on the API server (`grpcserver.NewSupabaseAuthHook`) |

Every path skips **anonymous** Supabase sessions (`signInAnonymously`): an
anonymous visitor is not a signed-in user. An upgrade (`is_anonymous` flips to
false) is the sign-up. Every path also honours the user's
`privacy.analytics_enabled` opt-out (the browser through the analytics switch,
the server through `analytics.ForUser`). Nothing carries an email, IP address
or user agent.

The first two need nothing but a release. The third needs the steps below,
once per Supabase project.

## Setting up the trigger hook

The triggers queue an HTTP request through `pg_net`, which sends it after the
transaction commits, and every function swallows its own errors: a down API
server, a missing secret or a bug never blocks or fails a sign-in. `pg_net`
does not retry, so a few events can be lost while the endpoint is down.

### 1. The API server takes the secret

Generate one secret per environment (`openssl rand -base64 32`), keep it in that
environment's secret store, and give it to `reliant-api-server` as
`SUPABASE_AUTH_HOOK_SECRET`. The api-server's env is rendered by
control-plane's KCL, so this step is a control-plane deploy change. Without the
variable the route does not exist (404); with it, the API logs nothing new at
boot, and `POST /_forge/auth/hooks/supabase` answers 401 to anything not
carrying the bearer.

Check it from outside, after the deploy:

```sh
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://<api origin>/_forge/auth/hooks/supabase   # 401, not 404
```

### 2. The Supabase project sends events

In the project's SQL editor, as `postgres`. The URL and the secret go in Vault,
never in git:

```sql
select vault.create_secret('https://<api origin>/_forge/auth/hooks/supabase', 'forge_auth_hook_url');
select vault.create_secret('<SUPABASE_AUTH_HOOK_SECRET>', 'forge_auth_hook_secret');
```

Then run `deploy/supabase/forge_auth_events.sql`. It is idempotent: re-running
it upgrades in place.

### 3. Verify

1. Sign up a new test account with email and password, then sign in.
2. In the SQL editor: `select status_code, error_msg, created from net._http_response order by created desc limit 5;`
   should show `204`s. A `401` means the Vault secret and the API's
   `SUPABASE_AUTH_HOOK_SECRET` differ. A `404` means the API has no secret. An
   `error_msg` means the URL is unreachable from Supabase.
3. In ClickStack (the env's logs), or with `reliant forge obs users <env>`: a
   `product.auth_signed_up` and a `product.auth_signed_in` with
   `product.provider = supabase` for the test user's id.

### Rollback

Either stops every event at once and is safe to run any time:

```sql
drop trigger if exists forge_auth_user on auth.users;
drop trigger if exists forge_auth_session on auth.sessions;
drop schema if exists forge_auth cascade;
```

Or unset `SUPABASE_AUTH_HOOK_SECRET` on the API server: the route goes away, and
the triggers' requests fail harmlessly (visible in `net._http_response`).

### Rotating the secret

Set the new value on the API server, then
`select vault.update_secret(id, '<new>') from vault.secrets where name = 'forge_auth_hook_secret';`.
Deliveries between the two steps are refused (401) and lost.

### After a forge bump

`deploy/supabase/forge_auth_events.sql` is forge's file verbatim
(`TestSupabaseAuthEventsSQLIsForgesVerbatim`). When a forge bump changes it,
regenerate it with `reliant forge auth hooks sql supabase >
deploy/supabase/forge_auth_events.sql` and re-run it on each project.

## Projects

| Env | Supabase project | Status |
| --- | --- | --- |
| nonprod | `umoxnokojetwkrmttfqj` | not applied: apply after the release carrying this is deployed, then verify |
| prod | `ounfncqxmfhzorhxwndh` (`dash.reliantlabs.io`) | **not applied: needs the owner's go-ahead**, as with the `email_verified` access-token hook |
