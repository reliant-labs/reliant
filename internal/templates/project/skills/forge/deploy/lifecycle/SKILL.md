---
name: deploy/lifecycle
description: Stopping, starting and deleting a HOSTED environment from the CLI — `forge env stop|start|delete`, --target and --wait, billing refusals, data retention and `forge env retained list|purge`, and why `forge env down` is not the same thing; the whole app with `forge app stop|teardown|delete`; plus the domain commands and scope errors.
---

# Hosted env lifecycle

Everything the Reliant UI does to a hosted env is available from the CLI. Signed
in to Reliant (running `reliant forge …`, or in a Reliant agent's shell) you are
already authenticated — do NOT run `forge login`; forge asks Reliant's
credential helper (`$FORGE_CREDENTIAL_HELPER`) for a short-lived token. Standalone
forge uses `forge login`; CI sets the env's token variable.

```
forge env stop prod [--target api] [--wait]    # suspend; never refused
forge env start prod [--target api] [--wait]   # resume; refused with the billing reason if not entitled
forge env delete preview [--yes] [--keep-data] # tear the env down, data included
```

- `--target` takes a workload name (resolved to its deployment id); without it
  the whole environment changes. After the call forge prints each deployment's
  DECLARED state; the platform converges asynchronously.
- `--wait` polls until the platform OBSERVES the new state (`--timeout`,
  default 5m) and exits non-zero on timeout.
- A refused `start` prints the server's reason and the fix; nothing is started.
- `delete` prompts you to type the env name. With no TTY or `CI=1` it fails
  fast unless you pass `--yes`. It removes EVERYTHING the env runs: its
  deployments, workloads, URLs, Flux objects, custom resources, namespaces and
  its managed databases' data (irreversible). `--keep-data` retains each
  managed database instead: it keeps running and billing as storage and is
  listed as a retained database until purged; a re-deploy creates a NEW env and
  does not reattach it. The project's registry images are kept either way.
- `delete` also removes a hosted env whose `deploy/kcl/<env>/` is gone: it is
  looked up by name on the control plane the project is bound to
  (`.forge/hosted-projects.json`), or on the one `--endpoint <url>` names when
  the project is bound to none or several. `--endpoint` must be one of those
  bindings or Reliant cloud; any other host is refused before a credential is
  sent. `--id <environment-id>` names such an env by the control plane's id
  instead (refused if the env belongs to another project, or is still
  declared). `forge env list --orphaned` lists the hosted envs this project no
  longer declares.
- Before confirming, `delete` shows the env's id, project, control plane,
  deployment count and bound custom domains. The control plane refuses to
  delete an env while a custom domain is bound to it: `forge domain unbind`
  each first. If the bound domains cannot be read, `delete` stops before
  confirming and deletes nothing.
- All three refuse an env that is not hosted. `forge env down` only stops local
  host processes and never touches a control plane.

## Retained databases

A database kept by `--keep-data` is a RETAINED database: still running, still
billing as storage, never reattached by a later deploy.

```
forge env retained list [--all]          # this app's (--all: the organization's), with size and dates
forge env retained purge <id> [--all] [--yes]  # destroy one: type its name to confirm. IRREVERSIBLE
```

`purge` takes the id `list` prints, or a database name when exactly one of
this app's retained databases has it. Either resolves among THIS app's: another
app's is refused and named; `--all` resolves among the organization's, and the
confirmation names the owning app. A control plane that purges retained
databases by itself shows the date in a `PURGE AFTER` column; an older one
does not, and its databases stay until purged. Purging needs an org owner or
admin.

## The whole app: stop, tear down, delete

The app is every hosted env of this project (forge.yaml `name`) on the control
plane the project is bound to (`.forge/hosted-projects.json`, or
`--endpoint`). Least to most destructive:

```
forge app stop [--yes]                   # suspend every env; storage keeps billing
forge app teardown [--yes] [--keep-data] # delete every env, keep the app; nothing bills
forge app delete [--yes]                 # schedule the app's deletion (7-day grace window)
forge app delete --status | --cancel     # show it / cancel it until the purge starts
```

- Each asks you to type the app name (`--yes` skips it; with no TTY or `CI=1`
  it fails fast without `--yes`), and `stop` and `teardown` print each env's
  outcome. An env the control plane refuses (`stop` needs `deploy:write` on
  it; `teardown` an org owner or admin) is reported and left as it was, the
  rest still change, and the command exits non-zero.
- `stop` keeps URLs, data, secrets and domains; `forge env start <env>`
  resumes an env. Storage keeps billing: `teardown` is the zero-cost state.
- `teardown` is `forge env delete` for every env: data destroyed unless
  `--keep-data` (then listed by `forge env retained list`). It keeps the app's
  name, release history and release images (not billed) and its custom domains
  (unbound), so `forge env deploy <env> <release>` recreates an env. Each env's
  secrets are not given back to a recreated env: set them again. It deletes
  nothing while a custom domain is bound to one of the envs (`forge domain
  unbind` it first), when the bound domains cannot be read (the domain service
  failing, or a control plane without one), or while an env this project
  declares also runs parts on a cluster you operate.
- `delete` stops every env at once and refuses new deploys, promotions and
  envs (reason `app_deletion_scheduled`). After the grace window the control
  plane purges every env and its data, retained databases, release history and
  images, domains and certificates, and secrets, and frees the name; activity,
  audit and spend history stay. `--cancel` starts again what the deletion
  stopped. `--status` also says when the purge waits on a paused secret
  destroy. It needs an org owner or admin signed in as a person (tokens are
  refused) with a live step-up on every protected env: on a `step_up_required`
  refusal, step up in the Reliant web app and re-run. A control plane that
  predates app deletion answers Unimplemented; forge says so and changes
  nothing (`teardown` still works there).

## Preview environments

Per-PR and per-branch copies of a template env, which expire:
`forge env preview create|delete`, `forge env gc`. Load `deploy/preview`.

## Domains

`forge domain add|ls|show|verify|bind|unbind|rm --env <env>` — see
`deploy/domains`.

## Missing-scope rejections

A 403 naming a scope (e.g. `domain:read`): the credential lacks it. Under
Reliant, the Reliant session it was minted from lacks it — grant it in Reliant
(Settings → Access Tokens) or sign in again with `reliant auth login`; standalone,
re-run `forge login`. If a fresh token still lacks it, your role has no grant —
ask an org admin, who can mint a token with the scope in the web app (Settings →
API tokens, https://forge.reliantlabs.io/settings/tokens); export it as the env's
declared token variable.

### Scopes for reading what a deployed app is doing

These scopes belong to the feedback loop (observe, then query). Each is its own
authority: none implies another product's, and a token is minted with them like
any other scope.

| Scope | Allows | Notes |
|---|---|---|
| `obs:read` | every telemetry read: traces, logs, metrics, issues, release health | the one an org automation token carries to gate CI on a release's health; needs no acting user |
| `obs:triage` | changing an issue's triage state (resolve, ignore, unresolve) | implies `obs:read` at the point of use; a read-only token cannot silence an issue |
| `obs:write` | authoring the org's observability workspace (dashboards: save, restore, archive, promote) | implies `obs:read` at the point of use; implies nothing else (not `obs:triage`); cannot be bound to one environment |
| `db:query` | one read-only query against an env's database | implies nothing and is implied by nothing: app data is not telemetry. A real env may restrict it further to people |
| `storage:read` | reading an env's object storage: list a bucket, read an object's metadata, preview it, mint a short-lived download URL | implies nothing and is implied by nothing, `db:query` included: uploads are not rows. May be bound to one environment; a real env may restrict it further to people (`data_access.storage`) |
| `deploy:config:read` | a release's config VALUES: a workload's plain env var values and its redacted rendered manifest (`deploy:read` sees names only) | implies nothing and is implied by nothing, `deploy:write` included: shipping a release is not reading what it carries. May be bound to one environment. Secret objects and secret refs never show a value, and a secret-looking plain value shows only as `redacted` (`release.RedactObject`), with no value-derived digest |
| `vuln:read` | the vulnerability findings of the org's releases and environments: the per-env summary, the findings list, one advisory across every artifact it touches, a release's findings | implies nothing and is implied by nothing, `deploy:read` and `deploy:write` included: a findings list is an attack map, so the CI token that records releases does not read it unless it was minted with it. May be bound to one environment |
| `tests:read` | the org's test record: runs and their failures, coverage per package, file and line, flake states, a release's tests | implies nothing and is implied by nothing, `deploy:read`, `deploy:write` and `tests:write` included: failure output can quote config, request bodies and log lines. A control plane may grant it to its `deploy:read` holders. May be bound to one environment (the runs recorded against it) |
| `tests:write` | uploading test runs to the control plane, and nothing else | a narrow upload scope like `sourcemaps:write`, so a pull request's CI job records its tests without `deploy:write`; it implies nothing, `tests:read` included. Not registry-only, and bound to nothing. A control plane may also accept `deploy:write` for the upload, so a deploying CI needs no second token |

### A registry-only credential for filing source maps

`sourcemaps:write` files a frontend's source maps (`forge frontend sourcemaps
push`) and does nothing else. The registry grants it push and pull on
`<org>/<project>/<frontend>-sourcemaps` repositories only, and the control plane
refuses it on every RPC. It is a whole credential: a token carrying it carries
no other scope, acts as no person, is bound to nothing, and no wildcard resolves
to it. Because it cannot ask the control plane for its org, the push names the
org itself. A person mints the token in the web app (Settings → API tokens,
https://forge.reliantlabs.io/settings/tokens); `forge cloud token create` does
it from the CLI only on a control plane that admits a CLI login for token
administration (Reliant cloud refuses one: exit 9, `person_session_required`):

    forge cloud token create --env prod --name desktop-sourcemaps --scopes sourcemaps:write --expires-in 2160h
    forge frontend sourcemaps push dist --frontend desktop --build-id "$VERSION" --env <env> --org <org id>

`accesstoken.AllScopes` is the closed set; a store's `access_tokens` CHECK must
list every member (`db/closed-sets`).

## Where the credential comes from

`--token`, then the env's declared token variable (CI), then what `forge login`
stored, then the credential helper `$FORGE_CREDENTIAL_HELPER` (Reliant's
session). `forge cloud status <env>` prints which one a command will use —
never the token.
