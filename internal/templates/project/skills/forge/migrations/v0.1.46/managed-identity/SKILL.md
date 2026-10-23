---
name: migrations/v0.1.46/managed-identity
description: Replace the dev idp-provision job with forge.ManagedIdentity: `forge env up` converges the identity on the dev IdP with the same library the control plane runs, `deploy/kcl/identity.k` declares the users once for every env, and the `auth idp-provision` command, the `idp-provision` workload and `idp_identity_gen.k` are gone. Use when `deploy/kcl/workloads.k` or `deploy/kcl/dev/main.k` declares `idp-provision`, `deploy/kcl/dev/idp_identity_gen.k` exists, a `config.k` reads `idp.idp_identity[`, or `cmd/<bin>/cmd/auth.go` registers `idp-provision`.
---

# Managed identity replaces the idp-provision job

Use when `deploy/kcl/workloads.k` or `deploy/kcl/dev/main.k` declares `idp-provision`, `deploy/kcl/dev/idp_identity_gen.k` exists, a `config.k` reads `idp.idp_identity[`, or `cmd/<bin>/cmd/auth.go` registers `idp-provision`.

`deploy/kcl/workloads.k`, `deploy/kcl/<env>/main.k`, `config.k` and `cmd/<bin>/cmd/auth.go` are yours, so forge edits none of them for you.

### What was wrong

Dev sign-in was wired by a one-shot job (`<bin> auth idp-provision`) that registered the app against the dev IdP and published the generated client id and audience into a committed KCL file, `idp_identity_gen.k`, which `config.k` imported value by value. Hosted envs had no counterpart: staging and prod copied the same values by hand. Two mechanisms, two sets of hand-copied values, and a library (`pkg/devidp`) that only dev used.

### What changed

- **`forge.ManagedIdentity`** (`Bundle.identity`) declares the env's users once: roles, applications, machine users, seed users. Scaffolds write it to `deploy/kcl/identity.k`, and every env imports it. See `deploy/managed-identity`.
- **`forge env up`** converges it on the dev IdP (`forge.IdentityOnHost {infra = "idp"}`) with the same library (`pkg/identity`) the control plane runs for a hosted env, using the bootstrap PAT. It runs after the IdP is ready and before host services start, and a second run writes nothing. Non-secret outputs go to `.forge/identity/<name>.json`. The broker token, client secrets and machine keys go to the env's secret store, under the env var each `forge.IdentityRef` resolves to. The API gets `jwt_issuer`, `jwt_audience`, `oidc_client_id`, `oidc_redirect_uri`, `idp_base`, `idp_login_uri` and `idp_broker_token` from the identity, so no `config.k` line names them.
- **Removed:** the `auth idp-provision` command, the `idp-provision` workload and its env binding, `idp_identity_gen.k` and its `import .idp_identity_gen as idp`, and `pkg/devidp`'s provisioning half (`Reconcile`, `Spec`, the publishers, `ProvisionLoginBroker`, `SecretFile`, `RedirectGlob`). `pkg/devidp` keeps the sign-in broker's client.
- **Behavior change:** a workload that sets `JWT_ISSUER`, `JWT_AUDIENCE`, `OIDC_CLIENT_ID`, `OIDC_REDIRECT_URI` or `IDP_BASE` by hand beside `Bundle.identity` is refused at render, naming both. Pick one source per field.

### Steps (dev)

1. Delete the job: remove the `idp_provision = fw.Workload {...}` stanza and its name from `ALL` in `deploy/kcl/workloads.k`; remove its binding line (`_on_host_job(wl.idp_provision)`) and the `_idp_job` env block, if present, from `deploy/kcl/dev/main.k`. Delete `cmd/<bin>/cmd/auth.go` (nothing else in it, unless you added commands). Delete `deploy/kcl/dev/idp_identity_gen.k`.
2. Remove the identity wiring from `deploy/kcl/dev/config.k`: the `import .idp_identity_gen as idp` line and every `idp.idp_identity["..."]` assignment (backend `jwt_issuer`, `jwt_jwks_url`, `jwt_audience`, `oidc_client_id`, `idp_base`; frontend `oidc_*`). In dev main.k's `_env`, keep `CORS_ALLOW_CREDENTIALS = "true"` for services if sign-in uses a session cookie across ports. Remove `IDP_BROKER_TOKEN` from the API workload's `config_secrets`.
3. Declare the identity. Write `deploy/kcl/identity.k` (or copy the scaffold's, from `forge skill load deploy/managed-identity`):

   ```python
   import forge

   IDENTITY = forge.ManagedIdentity {
       roles = [forge.IdentityRole {key = "admin"}, forge.IdentityRole {key = "member"}]
   }
   ```

   In `deploy/kcl/dev/main.k` add `import ..identity as idn` and, on the Bundle, `identity = idn.IDENTITY | {runtime = forge.IdentityOnHost {infra = "idp"}}`. The `idp` infra entry (`engine = "zitadel"`) stays.
4. Seed users (a dev login of your own): declare `seed_users = [forge.IdentitySeedUser {email = "...", roles = [...], password_secret = forge.ManagedSecret {name = "DEV_PASSWORD"}}]` and `forge secret set --env dev DEV_PASSWORD`.
5. Run `forge env up dev`. It prints each change it made and the store keys it wrote.

### Steps (staging and prod)

Nothing changes unless you want managed identity there. To adopt it, remove any hand-set `jwt_issuer`, `jwt_audience`, `oidc_client_id`, `oidc_redirect_uri` and `idp_base` from that env's `config.k` and workloads, import `identity.k`, and add `identity = idn.IDENTITY` to the Bundle (it needs `control_plane = forge.ControlPlane {}`). To keep bringing your own issuer (Auth0, Okta, a Zitadel you run), declare no `identity` there: that stays supported.
