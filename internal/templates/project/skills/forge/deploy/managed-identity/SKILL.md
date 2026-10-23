---
name: deploy/managed-identity
description: forge.ManagedIdentity, an env's users declared in KCL — roles, the applications that sign users in, the workloads that read tokens, share_with, SMTP, seed users — administering a hosted env's users with `forge cloud identity` (verbs, the identity:read/manage scopes, password-set codes), how forge fills each consumer's typed config (jwt_issuer, oidc_client_id, ...) with no app code change, what it delivers per runtime, what is refused, and what is not built yet (hosted login, social).
---

# Managed identity

`Bundle.identity = forge.ManagedIdentity {...}` declares an env's users. The control plane converges an identity-provider instance to it; forge records the **declaration** in the release shape (`shape.identity`, names only) and fills every consumer's typed config from the instance's outputs. You write no `config.k` line and no app code for it, and copy nothing between files.

```python
# deploy/kcl/identity.k, imported by every env
IDENTITY = forge.ManagedIdentity {
    roles = [
        forge.IdentityRole {key = "admin", display_name = "Administrators"}
        forge.IdentityRole {key = "member"}
    ]
}

# deploy/kcl/prod/main.k
identity = idn.IDENTITY                                   # runs on the control plane (hosted)

# deploy/kcl/dev/main.k: the same declaration on the env's host-run Zitadel
identity = idn.IDENTITY | {runtime = forge.IdentityOnHost {infra = "idp"}}

# a preview env reusing its project's staging users
identity = forge.ManagedIdentity {share_with = "staging"}
```

## What you declare

| field | meaning |
|---|---|
| `runtime` | `forge.OnHosted {}` (default; needs `control_plane`) or `forge.IdentityOnHost {infra}` (a `HostInfra` with `engine = "zitadel"`) |
| `login.mode` | `native` (default): the app's own form plus the server-side login broker. `hosted` is **phase 2**: accepted and recorded, nothing converges it yet |
| `roles` | `key`, `display_name`, `group`; `members_from = "console"` (default) or `"declared"` with `members = [emails]`, a set converged from KCL and read-only in the console |
| `applications` | derived when empty: one `native_broker` named `<env>:api` for the workload `api`, else one per service workload. `redirect_uris`, `logout_uris`, `login_uri` are literal overrides |
| `consumers` | derived when empty: every service workload on a runtime that can reach the instance. `fields = forge.IdentityFields {...}` remaps an output to a non-standard config field (an app validating two issuers); `deliver_only = True` is the general "deliver, I will read the Secret myself" escape hatch: the control plane still writes `forge-identity-<name>` (+ `-jwks`) to the workload's cluster, but forge projects nothing into the workload (no env, no volumes) and does not refuse hand-set values. Use it for a workload that reads the Secret itself, or while adopting an identity whose workload still reads legacy config; flip it off to let forge fill the typed config |
| `machine_users` | `name`, `roles`, `key_secret` + `cluster`: where its key is delivered (hosted instance); a host-run one keeps the key in the env's secret store |
| `smtp` | `host`, `port`, `user`, `sender`, `password = forge.ManagedSecret {name = ...}`: a **name**, never the password. **Omit it and the instance sends no mail at all**: there is no platform relay |
| `seed_users` | only in an env that declares `lifecycle = "local"` or `"ephemeral"` |
| `domain` | your own issuer domain; needs `public = True` (or hosted login) |
| `public` | ask for a public route. **Private by default** |
| `project_name` | the identity project's name (default: `Bundle.project`); settable so an adopted instance keeps its legacy name |
| `share_with` | a bare env name **of the same project**. The owner must declare an identity and not itself be a sharer: no chains. A sharer declares only applications and consumers |
| `social` | **reserved**; refused at render |

## Administer a hosted env's users

After deploying the environment, use the public control-plane management API (`controlplane.v1.IdentityService`) through Forge:

```bash
forge cloud identity status prod                  # state, issuer, apps, deliveries, Mail: none|smtp
forge cloud identity users prod --query person@example.com --page-size 20 --json
forge cloud identity invite prod --email person@example.com --role member [--role operator] [--return-code]
forge cloud identity set-roles prod --user person@example.com --role member   # --none removes every console role
forge cloud identity reset prod --user person@example.com [--return-code]
forge cloud identity lock|unlock prod --user person@example.com
forge cloud identity revoke-sessions prod --user person@example.com
forge cloud identity delete prod --user person@example.com                     # asks; --yes skips the question
forge cloud identity export prod                  # export every user now; prints a short-lived download URL
# Continue a listing: forge cloud identity users prod --page-token <nextPageToken> --json
```

`users` shows each user's last sign-in (the newest successful password, passkey or IdP check Zitadel recorded; `-` for none) and how many users match across every page. `status` says why there is no instance (not declared, refused, or created on the reconciler's next pass) and names the owner and sharer environments.

`export` exports the instance on demand (every user, password hashes included, in Zitadel's import format; one export per 15 minutes) and prints a signed URL valid for minutes: the URL is a credential, so never paste it anywhere shared. It needs `identity:manage` and, for a person, the `identity:manage` grant as `--return-code` does; it is refused where the control plane has export switched off.

### The identity line of `env status` and `env deploy`

A hosted env that declares `forge.ManagedIdentity` gets one line in `forge env status <env>`, read from its control plane: `identity: ready`, `identity: provisioning (creating)`, `identity: degraded: <delivery>` or `identity: failed: <reason>`. It never changes `env status`'s exit code. `forge env deploy` (and `env status --wait`) **waits on it**: a release whose rollout succeeded reports `rollout succeeded; waiting on identity: …` until the identity is ready. Still provisioning at the deadline is the timed-out exit (retry the wait); failing or degraded at the deadline, or a refused declaration at once, is exit 1. A control plane that cannot be asked (no identity API, a credential without `identity:read`) is reported as `identity: unknown: …` and never holds a deploy.

`--user` takes a user id or an exact email (resolved through `users`; an ambiguous one is refused). Every verb accepts `--json`. Choose the recipient explicitly; never infer the owner's email from the agent's session. There is no password flag, bootstrap password or production seed user, and no response carries a password or token, except the one-time code `--return-code` asks for.

`set-roles` replaces the user's **console-managed** roles with exactly the `--role` values. Roles with `members_from = "declared"` are converged from KCL: naming one is refused, the ones a user holds are kept, and `delete` refuses a user who holds one (the next reconcile would recreate them). Change those in `deploy/kcl/identity.k`. `lock` is reversible with `unlock`; `delete` is not. `revoke-sessions` ends sign-in sessions; access tokens already issued stay valid until they expire.

### Who may call it

The endpoint comes from that env's `ControlPlane` KCL. Credentials use the usual precedence: `--token`, the declared token env var, the stored `forge login`, then the host session helper (under Reliant, no extra login is needed). The org comes from the authenticated control plane and the env id is looked up by exact project/name; there is no org override.

| what | needs |
|---|---|
| `status`, `users` | the `identity:read` scope |
| every verb that changes a user (`invite`, `set-roles`, `reset`, `lock`, `unlock`, `delete`, `revoke-sessions`), `export`, and any `--return-code` | `identity:manage` (implies read), held by an **org admin** |

A person's session is checked against their org role and their identity grants (owners and admins hold both scopes). A **machine credential** (an org access token, a Reliant agent's session token) is accepted when it is scoped to the org and carries the scope; a `--return-code` from one also needs its acting user to still hold the `identity:manage` grant. A Reliant session or a `forge login` credential does not carry the identity scopes itself: when the control plane answers that one is missing, forge exchanges the credential for exactly that scope (`AccessTokenService/ExchangeToken`, for an hour, in memory only, and only while the person behind it holds the grant) and retries. A `--token` or the token env var is used exactly as given and must already carry the scope.

`status` reports the current state even while provisioning or refused. Every verb fails closed on an identity that is not ready, an unknown role, a declared/read-only role or a missing scope: fix the reported cause; do not bypass it with an internal reconciler credential.

### Getting a credential to a person

`invite` creates the account (or grants roles to an existing one, `created=false`). An account with no credential yet is issued a one-time **password-set code**, which the scaffolded `/auth/reset` page redeems (it sets the password and signs the person in). `reset` issues one for any account; their current password keeps working until it is redeemed. Codes are single use and expire within the hour.

**No mail is sent unless the environment declares `smtp`**; there is no platform relay, `GetIdentityStatus` reports `mail_delivery: IDENTITY_MAIL_DELIVERY_NONE`, and `status` shows `Mail: none`. Then the control plane **refuses** `invite` (for an account that needs a code) and `reset` with FailedPrecondition naming `smtp`, rather than succeed and send nothing. An `identity:manage` caller passes `--return-code` (`return_code` on the rpc): the code comes back, with the env's reset link already filled in (`<app>/auth/reset?userID=…&code=…&orgID=…`, built from the application's `login_uri`), and you deliver it over a channel you control (your own mailer, a message, an admin screen). Declare `smtp` and the code is emailed instead.

Where the link points: an explicit `login_uri` on the application always wins. Without one, the deployed control plane builds it from the first bound domain, and an env that has neither gets the bare code with no link. **Once control-plane #1115 is deployed** (merged, not yet in a prod release), an identity's `login_uri` and its reset links default to the env's single hosted frontend, at its oldest live custom domain or else its platform hostname. An env with several hosted frontends must still declare `login_uri`.

The code is a credential: never print it into a shared log or return it from an endpoint anyone can call. **forge scaffolds no invite UI**; an admin screen of your own calls `IdentityService.InviteUser` / `SendPasswordReset` with `return_code`, or mints a code from the app's broker with `devidp.MintPasswordResetCode(ctx, userID)`.

### Public sign-up is on by default

The scaffolded broker mounts `POST /auth/register`, so **anyone who can reach the app can create an account** and is signed in. For an invite-only app, delete that route in `internal/app/login_broker.go` and the "Create an account" toggle in the sign-in screen, then invite as above.

## The issuer

`https://<slug>.reliantidentity.com`. The control plane assigns the slug **once**, when the instance is first provisioned, stores it, and never recomputes it, so renaming an env or project cannot move an issuer. The rule it assigns by is `release.IdentitySlug`:

```
<project>-<env>-<hash>      label = lower-case [a-z0-9], other runs folded to "-", at most 24 characters
hash  = first N hex digits of sha256(envID), N = 6 and grown (to 12) only to resolve a collision
```

`envID` is the control plane's immutable id for the env, never its name. The control plane's unique index, not the hash, guarantees uniqueness. A slug is at most 62 characters.

## What reaches a consumer

The consumer's typed config fields are filled; the env var is the upper-cased field name.

| output | field | hosted workload | cluster workload | host process (OnHost identity) |
|---|---|---|---|---|
| issuer | `jwt_issuer` | managed secret `RELIANT_IDENTITY_<NAME>_ISSUER` | key `ISSUER` of Secret `forge-identity-<name>` | secret store key `ISSUER` |
| audience | `jwt_audience` | `..._AUDIENCE` | `AUDIENCE` | `AUDIENCE` |
| scopes | `oidc_scopes` | `..._SCOPES` | `SCOPES` | `SCOPES` |
| idp_base | `idp_base` | `..._ISSUER` | `ISSUER` | `ISSUER` |
| client_id, redirect_uri, login_uri | `oidc_client_id`, `oidc_redirect_uri`, `idp_login_uri` | `..._<APP>_<KEY>` | `<APP>_<KEY>` | `<APP>_<KEY>` |
| broker_token | `idp_broker_token` | managed secret | **file** `IDP_BROKER_TOKEN_FILE` (re-read on change) | store key |
| jwks | `jwt_jwks_snapshot` | optional managed secret | **file** `JWT_JWKS_SNAPSHOT_FILE`, optional Secret | store key |
| roles_claim | `jwt_roles_claim` | literal `auto` | literal `auto` | literal `auto` |

`<APP>` is the application name upper-cased with other runs folded to `_` (`prod:api` is `PROD_API`). A cluster consumer waits (`CreateContainerConfigError`) for the first delivery of the `identity` Secret rather than crash-looping on an empty issuer, and starts by itself when it lands. The JWKS snapshot Secret is optional: it is a fallback.

**Escape hatches on a consumer** (`forge.IdentityConsumer`):

- `fields = forge.IdentityFields {...}` remaps an output to a non-standard config field.
- `omit = ["scopes", "roles_claim", "login_uri", "jwks"]` declines outputs the app's config has no field for. Without it every standard output is projected under its standard name, as env nothing reads. Only those four may be omitted: issuer, audience and the native-broker outputs define the identity (use `deliver_only` to project none). An output cannot be both mapped in `fields` and omitted, and one omitted twice is refused. The delivered Secrets still carry every key, and the shape records `omit` (absent when empty, so existing shapes hash unchanged).
- `deliver_only = True` delivers the Secrets and projects nothing, for a workload still on its legacy config.

The control plane is the only writer of `forge-identity-*` and `RELIANT_IDENTITY_*`; nobody sets them, and `forge secret` never asks.

## Derived control-plane secrets

`Bundle.identity` derives `forge.ControlPlaneSecret`s you never declare (declaring an identity kind by hand is refused): kind `identity` (required), `identity_jwks` (optional) per cluster namespace that runs a hosted-identity consumer, and `identity_machine_key` per machine user. They are in `shape.control_plane_secrets` and listed in `shape.identity.deliveries`, so the existing delivery path sees them. A cluster consumer needs a connected cluster (`forge cluster connect`), refused at render otherwise.

## What is refused

- a hosted identity with no `control_plane`; `IdentityOnHost` naming something that is not a zitadel `HostInfra`;
- `seed_users` outside a `local`/`ephemeral` env; `domain` on a private instance;
- `share_with` that is not a bare env name (so never another project), is this env, or sits beside roles/smtp/seed users/machine users/domain/project name; sharing a host-run IdP;
- duplicate roles, applications, consumers; undeclared roles on a machine or seed user; a consumer naming an undeclared application or a non-service workload;
- **a hand-set field the identity fills**: a workload that sets `JWT_ISSUER`, `JWT_AUDIENCE`, `OIDC_CLIENT_ID`, `OIDC_REDIRECT_URI` or `IDP_BASE` (literal or secret ref) beside `Bundle.identity` is refused, naming both. An empty value is the unset default and gives way. To bring your own issuer for an env, declare no identity there.

`share_with` ownership (the owner declares an identity, is not a sharer, same org and project) is judged where both envs are visible: the control plane at admission, since one env's render cannot see another's.

## `forge.IdentityRef`

The primitive the projection is made of; write it yourself for an output the projection does not hand out: `env = {SECOND_ISSUER = forge.IdentityRef {output = "issuer"}}`. Application outputs take `application = "<name>"`.

## Dev: `forge.IdentityOnHost`

`forge env up` converges the declaration on the env's host-run Zitadel (the `HostInfra` named by `infra`) with the library the control plane runs, authenticated by the bootstrap PAT (`idp_pat_path`), in the server's default instance. It writes `.forge/identity/<name>.json` (non-secret) and the env's secret store (broker token, client secrets, `<USER>_MACHINE_KEY`), under the env var each reference resolves to. A seed user's password is the store entry its `password_secret` names (`forge secret set --env dev <NAME>`). `auth/dev-loop` has the rest.

## Not built yet

`login.mode = "hosted"` (accepted, recorded, unconverged), `social` (refused).
