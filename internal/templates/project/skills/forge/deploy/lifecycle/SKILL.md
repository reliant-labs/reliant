---
name: deploy/lifecycle
description: Stopping, starting and deleting a HOSTED environment from the CLI — `forge env stop|start|delete`, --target and --wait, billing refusals, data retention, and why `forge env down` is not the same thing; plus the domain commands and scope errors.
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
- All three refuse an env that is not hosted. `forge env down` only stops local
  host processes and never touches a control plane.

## Domains

`forge domain add|ls|show|verify|bind|unbind|rm --env <env>` — see
`deploy/domains`.

## Missing-scope rejections

A 403 naming a scope (e.g. `domain:read`): the credential lacks it. Under
Reliant, the Reliant session it was minted from lacks it — grant it in Reliant
(Settings → Access Tokens) or sign in again with `reliant auth login`; standalone,
re-run `forge login`. If a fresh token still lacks it, your role has no grant —
ask an org admin, who can mint
`forge cloud token create --env <env> --name <n> --scopes <scope>`; export it as
the env's declared token variable.

### Scopes for reading what a deployed app is doing

Three scopes belong to the feedback loop (observe, then query). Each is its own
authority: none implies another product's, and `forge cloud token create
--scopes` accepts them like any other.

| Scope | Allows | Notes |
|---|---|---|
| `obs:read` | every telemetry read: traces, logs, metrics, issues, release health | the one an org automation token carries to gate CI on a release's health; needs no acting user |
| `obs:triage` | changing an issue's triage state (resolve, ignore, unresolve) | implies `obs:read` at the point of use; a read-only token cannot silence an issue |
| `obs:write` | authoring the org's observability workspace (dashboards: save, restore, archive, promote) | implies `obs:read` at the point of use; implies nothing else (not `obs:triage`); cannot be bound to one environment |
| `db:query` | one read-only query against an env's database | implies nothing and is implied by nothing: app data is not telemetry. A real env may restrict it further to people |

`accesstoken.AllScopes` is the closed set; a store's `access_tokens` CHECK must
list every member (`db/closed-sets`).

## Where the credential comes from

`--token`, then the env's declared token variable (CI), then what `forge login`
stored, then the credential helper `$FORGE_CREDENTIAL_HELPER` (Reliant's
session). `forge cloud status <env>` prints which one a command will use —
never the token.
