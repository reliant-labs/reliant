---
name: deploy/managed-identity
description: forge.ManagedIdentity, an env's users declared in KCL — roles, the applications that sign users in, the workloads that read tokens, share_with, SMTP, seed users — administering a hosted env's users with `forge cloud identity` (verbs, the identity:read/manage scopes, password-set codes), how forge fills each consumer's typed config (jwt_issuer, oidc_client_id, ...) with no app code change, what it delivers per runtime, what is refused, sign-in (owners, signup modes, passkeys, magic links, Google/GitHub with your own OAuth client, mail addressing, the optional login_policy output), and what is not built yet (hosted login).
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
| `smtp` | `host`, `port`, `user`, `sender`, `password = forge.ManagedSecret {name = ...}`: a **name**, never the password. Omitted, a hosted instance uses platform delivery (`email`), a dev one its mailpit |
| `seed_users` | only in an env that declares `lifecycle = "local"` or `"ephemeral"` |
| `domain` | your own issuer domain; needs `public = True` (or hosted login) |
| `public` | ask for a public route. **Private by default** |
| `project_name` | the identity project's name (default: `Bundle.project`); settable so an adopted instance keeps its legacy name |
| `share_with` | a bare env name **of the same project**. The owner must declare an identity and not itself be a sharer: no chains. A sharer declares only applications and consumers |
| `owners`, `owner_role` | people who must be able to get in (see Sign-in below) |
| `signup`, `signup_domains`, `signup_role` | who may create an account: `invite` (default), `open`, `domain` |
| `methods`, `passkey_rp_id` | first factors offered: `password` (default), `passkey`, `magic_link` |
| `social` | `forge.IdentityGoogle` / `forge.IdentityGitHub` with your own OAuth client |
| `email` | `forge.IdentityEmail {delivery, from_name, from_local, reply_to}`: how the instance's mail is addressed |

## Sign-in: owners, signup, methods, social, mail

```python
IDENTITY = forge.ManagedIdentity {
    roles = [forge.IdentityRole {key = "owner"}, forge.IdentityRole {key = "member"}]
    owners = ["founder@acme.com"]                 # seeded every pass; additive and sticky
    signup = "domain"                             # invite (default) | open | domain
    signup_domains = ["acme.com"]                 # a subdomain matches only if listed
    signup_role = "member"
    methods = ["password", "passkey", "magic_link"]
    social = [forge.IdentityGoogle {
        client_id = "1234.apps.googleusercontent.com"                       # public: a literal is fine
        client_secret = forge.ManagedSecret {name = "GOOGLE_CLIENT_SECRET"} # a NAME in the env's store
    }]
    email = forge.IdentityEmail {from_name = "Acme", reply_to = "help@acme.com"}
}
```

- **Owners** are created email-verified, granted `owner_role` and invited once while they have no credential. Removing one revokes nothing, so `owner_role` (default `owner`) must be a declared role with `members_from = "console"`.
- **Leaving `methods`, `signup` and `social` at their defaults leaves the instance's login policy alone** and delivers no `login_policy`: the broker keeps its legacy behaviour (password, sign-up only in development or with `auth_self_signup`). Owners and `email` alone change neither.
- **Mail is required** for `magic_link` and for `open`/`domain` signup: `smtp`, a hosted instance whose `email.delivery` is not `"none"` (platform delivery is the default; the sending domain is assigned, so `from_local` is only the local part), or a `forge.HostInfra {engine = "mailpit"}` in dev (`IdentityOnHost.mail` names it; empty = the env's only one).
- **Social client secrets never reach a pod**: the shape records the secret's NAME, only the reconciler reads it, and a workload whose env reads that name is refused. Set it with `reliant forge secret set --env <env> GOOGLE_CLIENT_SECRET`; a rotation takes effect on the next reconcile pass. Use a separate dev OAuth client with the dev secret store.
- **`passkey_rp_id`** (needs `passkey` in `methods`) pins the WebAuthn relying party. Default: the host of the app's sign-in origin (`localhost` in dev). Passkeys are bound to it forever.

| | hosted | dev (`IdentityOnHost`) |
|---|---|---|
| mail | platform relay, or BYO `smtp` | Mailpit (`engine = "mailpit"` HostInfra) |
| social callback to register | `https://<slug>.reliantidentity.com/idps/callback` (`forge cloud identity status`) | `http://localhost:<idp port>/idps/callback` (`forge env up`) |
| passkey RP id | the sign-in host | `localhost` |

These fields are recorded in the shape (`shape.identity.owners`, `signup`, `methods`, `passkey_rp_id`, `social`, `email`, `dev_mail`) as the bundle feature `shape-identity-signin`. A control plane that does not advertise it is sent the declaration without them and converges the identity as before, so nothing is enabled that nobody converged.

## Administer a hosted env's users

After deploying the environment, use the public control-plane management API (`controlplane.v1.IdentityService`) through Forge:

```bash
forge cloud identity status prod                  # state, issuer, apps, deliveries, Mail: none|smtp|platform|sandbox, social callback, owners pending
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

**Without mail** (no `smtp`, and the control plane's platform delivery is not on for the env, or `email.delivery = "none"`), `GetIdentityStatus` reports `mail_delivery: IDENTITY_MAIL_DELIVERY_NONE` and `status` shows `Mail: none`; with platform delivery it shows `Mail: platform` (or `sandbox` for an unclaimed org, whose mail is captured, not sent). Then the control plane **refuses** `invite` (for an account that needs a code) and `reset` with FailedPrecondition naming `smtp`, rather than succeed and send nothing. An `identity:manage` caller passes `--return-code` (`return_code` on the rpc): the code comes back, with the env's reset link already filled in (`<app>/auth/reset?userID=…&code=…&orgID=…`, built from the application's `login_uri`), and you deliver it over a channel you control (your own mailer, a message, an admin screen). With mail, the code is emailed instead.

Where the link points: an explicit `login_uri` on the application always wins. Without one, the deployed control plane builds it from the first bound domain, and an env that has neither gets the bare code with no link. **Once control-plane #1115 is deployed** (merged, not yet in a prod release), an identity's `login_uri` and its reset links default to the env's single hosted frontend, at its oldest live custom domain or else its platform hostname. An env with several hosted frontends must still declare `login_uri`.

The code is a credential: never print it into a shared log or return it from an endpoint anyone can call. **forge scaffolds no invite UI**; an admin screen of your own calls `IdentityService.InviteUser` / `SendPasswordReset` with `return_code`, or mints a code from the app's broker with `devidp.MintPasswordResetCode(ctx, userID)`.

### Public sign-up is off outside development

The scaffolded broker serves `POST /auth/register` in development only, so a deployed app is **invite-only** until the env's config sets `auth_self_signup: true`; invite as above. Opting in lets anyone who can reach the app create an account under an address nobody verified and be signed straight in, so first make every bind-by-email check `devidp.User.EmailVerified` (`auth/authorization`). The sign-in screen offers "Create an account" only when the broker serves it (`GET /auth/options`).

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
| login_policy | `idp_login_policy` | optional managed secret `..._LOGIN_POLICY` | key `LOGIN_POLICY`, **optional** | optional store key `LOGIN_POLICY` |

`<APP>` is the application name upper-cased with other runs folded to `_` (`prod:api` is `PROD_API`). A cluster consumer waits (`CreateContainerConfigError`) for the first delivery of the `identity` Secret rather than crash-looping on an empty issuer, and starts by itself when it lands. The JWKS snapshot Secret is optional: it is a fallback. `login_policy` (the versioned `LOGIN_POLICY` document the sign-in broker enforces) goes only to a native broker's consumer, only when the declaration sets `methods`, `signup` or `social`, and is always optional: until the control plane writes it the broker serves its legacy policy.

**Escape hatches on a consumer** (`forge.IdentityConsumer`):

- `fields = forge.IdentityFields {...}` remaps an output to a non-standard config field.
- `omit = ["scopes", "roles_claim", "login_uri", "jwks", "login_policy"]` declines outputs the app's config has no field for. Without it every standard output is projected under its standard name, as env nothing reads. Only those five may be omitted: issuer, audience and the native-broker outputs define the identity (use `deliver_only` to project none). An output cannot be both mapped in `fields` and omitted, and one omitted twice is refused. The delivered Secrets still carry every key, and the shape records `omit` (absent when empty, so existing shapes hash unchanged).
- `deliver_only = True` delivers the Secrets and projects nothing, for a workload still on its legacy config.

The control plane is the only writer of `forge-identity-*` and `RELIANT_IDENTITY_*`; nobody sets them, and `forge secret` never asks.

## Derived control-plane secrets

`Bundle.identity` derives `forge.ControlPlaneSecret`s you never declare (declaring an identity kind by hand is refused): kind `identity` (required), `identity_jwks` (optional) per cluster namespace that runs a hosted-identity consumer, and `identity_machine_key` per machine user. They are in `shape.control_plane_secrets` and listed in `shape.identity.deliveries`, so the existing delivery path sees them. A cluster consumer needs a connected cluster (`forge cluster connect`), refused at render otherwise.

## What is refused

- a hosted identity with no `control_plane`; `IdentityOnHost` naming something that is not a zitadel `HostInfra`;
- `seed_users` outside a `local`/`ephemeral` env; `domain` on a private instance;
- `share_with` that is not a bare env name (so never another project), is this env, or sits beside roles/smtp/seed users/machine users/domain/project name; sharing a host-run IdP;
- duplicate roles, applications, consumers; undeclared roles on a machine or seed user; a consumer naming an undeclared application or a non-service workload;
- sign-in rules: `domain` signup without `signup_domains` (or the reverse), a wildcard or duplicate signup domain, a `signup_role` or `owner_role` that is not a console role, a duplicate method, no method and no social provider, `magic_link` or open/domain signup with no mail, an owner that is not an email (or listed twice in any case), two providers of one type, an empty client id or a literal client secret, `passkey_rp_id` without `passkey`, header characters in `email`, `email.from_local` beside `smtp`, sign-in fields on a `share_with` sharer, `IdentityOnHost.mail` naming something that is not a mailpit;
- **a hand-set field the identity fills**: a workload that sets `JWT_ISSUER`, `JWT_AUDIENCE`, `OIDC_CLIENT_ID`, `OIDC_REDIRECT_URI` or `IDP_BASE` (literal or secret ref) beside `Bundle.identity` is refused, naming both. An empty value is the unset default and gives way. To bring your own issuer for an env, declare no identity there.

`share_with` ownership (the owner declares an identity, is not a sharer, same org and project) is judged where both envs are visible: the control plane at admission, since one env's render cannot see another's.

## `forge.IdentityRef`

The primitive the projection is made of; write it yourself for an output the projection does not hand out: `env = {SECOND_ISSUER = forge.IdentityRef {output = "issuer"}}`. Application outputs take `application = "<name>"`.

## Dev: `forge.IdentityOnHost`

`forge env up` converges the declaration on the env's host-run Zitadel (the `HostInfra` named by `infra`) with the library the control plane runs, authenticated by the bootstrap PAT (`idp_pat_path`), in the server's default instance. It writes `.forge/identity/<name>.json` (non-secret) and the env's secret store (broker token, client secrets, `<USER>_MACHINE_KEY`), under the env var each reference resolves to. A seed user's password is the store entry its `password_secret` names (`forge secret set --env dev <NAME>`). `auth/dev-loop` has the rest.

## Not built yet

`login.mode = "hosted"` (accepted, recorded, unconverged).
