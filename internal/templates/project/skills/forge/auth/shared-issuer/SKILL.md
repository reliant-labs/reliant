---
name: shared-issuer
description: Running a forge app against a shared, operated identity provider (a managed Zitadel) — jwt_roles_claim and Zitadel's project-roles map, the JWKS snapshot fallback that lets a restart survive an issuer outage (what is trusted and when), and idp_broker_token_file for rotating the login broker's token without a restart.
---

# Shared-issuer runtime: roles mapping, snapshot fallback, rotating broker token

Three runtime behaviours exist for an app whose issuer is a shared, operated one (a managed Zitadel) rather than a single-owner IdP it owns. All three are typed config, read by the scaffolded `SetupAuth` / login broker, and all three are off or inert until configured — a Clerk, Firebase or generic-OIDC app is unchanged.

## 1. Roles: `jwt_roles_claim`

Zitadel does not emit a flat `roles` array. It asserts a project-namespaced MAP, `role → {orgId: orgDomain}`, under `urn:zitadel:iam:org:project:<projectId>:roles` (one project) and `urn:zitadel:iam:org:project:roles` (every project in the token). The validator maps it into `Claims.Roles`.

| `jwt_roles_claim` | reads |
|---|---|
| `auto` (default) | the flat `roles` array if the token has a non-empty one — so every generic issuer is exactly as before — else Zitadel's map: with `jwt_audience` set (Zitadel's audience is the project id) **only that project's** claim, otherwise every project's merged |
| `<claim name>` | exactly that claim and nothing else: a flat array, a space/comma separated string (`scp`-style), or a role map |

- The map's KEYS are the roles; a role granted in several organizations appears once. Roles are sorted.
- When roles came from a map and there is exactly **one**, `Claims.Role` is set to it too (unless the token has its own `role`). With several, `Role` stays empty: the token does not say which one is "the" role, and choosing by sort order would be a guess that grants access.
- **Set `jwt_audience` on a Zitadel app.** Without it `auto` cannot tell your project's roles from another project's that the same token was issued for, and merges them.
- The token must actually carry the claim: Zitadel adds it when the project has "assert roles on authentication" on, or the `urn:zitadel:iam:org:projects:roles` scope was requested. A token with no roles claim yields no roles — the right answer, not an error.
- Clerk and Firebase use their own resolvers and ignore this field. A custom `UserResolver` replaces the mapping entirely.

For a composed validator (`TokenValidators`), `JWKSValidatorConfig.RolesClaim` / `HMACValidatorConfig.RolesClaim` take the same value.

## 2. JWKS snapshot fallback (D7): start through an issuer outage

By default a validator fetches the issuer's JWKS at boot and **refuses to start** when it cannot. Right for an IdP one app owns; a fleet-wide hazard for a shared one, where one IdP outage crash-loops every app that restarts during it.

A **snapshot** is the issuer's last-known PUBLIC keys, delivered out of band by whatever operates the issuer. Configure either (or both; the newer wins, they are never merged):

| field | what |
|---|---|
| `jwt_jwks_snapshot_file` (`JWT_JWKS_SNAPSHOT_FILE`) | path to the document; re-read when it changes, so re-delivery on key rotation needs no restart |
| `jwt_jwks_snapshot` (`JWT_JWKS_SNAPSHOT`, sensitive) | the same document as a config value — the shape the secret store carries (an OpenBao-backed ManagedSecret / ControlPlaneSecret on hosted and cluster runtimes). "Sensitive" only so it rides the secret path; the keys are public |
| `jwt_jwks_snapshot_max_age` | how old `delivered_at` may be (default `168h`, 7 days) |

Document format — a normal JWKS plus two members:

```json
{ "issuer": "https://acme.reliantidentity.com",
  "delivered_at": "2026-10-15T03:00:00Z",
  "keys": [ { "kty": "RSA", "kid": "…", "alg": "RS256", "n": "…", "e": "AQAB" } ] }
```

`delivered_at` is required in an inline value (nothing else dates it); a file without it is dated by its mtime. JWKS keys carry no expiry, which is why age is judged from the delivery time against `max_age`. `issuer`, if present, must equal `jwt_issuer` — another issuer's snapshot is refused, not merged.

**Boot behaviour** — try live first, always:

| issuer | snapshot | result |
|---|---|---|
| up | any | starts on **live** keys; the snapshot is not consulted |
| down | fresh (≤ max_age) | starts on the snapshot, logs a warning, retries the issuer in the background (5s doubling to 5m); **the first live success replaces it** |
| down | stale, absent, malformed, wrong issuer, future-dated | **refuses to start** — "no keys at all" is still fatal, and the error says why |

**What is trusted, precisely.** Tokens are fully verified either way (signature, alg, `iss`, `aud`, `exp`); a snapshot supplies key material, nothing else.
- While running on a snapshot only, it is re-checked against `max_age` on every verification: past it the validator **fails closed** (and keeps retrying live) instead of trusting week-old keys indefinitely.
- **Live wins.** For a `kid` the live set holds, the live key is used, even if the snapshot holds a different key under that kid. A key the issuer revoked or dropped is not resurrected by an older snapshot.
- **Rotation overlap — the one union.** A token signed with a `kid` the live set does not hold is checked against the snapshot only if the snapshot was *delivered after this process last fetched the live set*: the publisher saw a rotation this process has not refreshed for. A one-line warning names the kid. After the next live fetch the snapshot stops counting for that kid.
- A snapshot dated more than 5 minutes in the future is refused, so one cannot be made perpetually fresh.

Composed validators: `auth.JWKSKeyfuncWithSnapshot(url, auth.JWKSSnapshot{...})` returns a keyfunc and a stop func for `NewJWKSValidator`.

`jwt_jwks_snapshot*` without `jwt_jwks_url` or `jwt_issuer` is a configuration error: a snapshot is the fallback for a live source, not a key source of its own (use `jwt_secret` for a static key).

## 3. `idp_broker_token_file`: rotate the login broker's token without a restart

`IDP_BROKER_TOKEN` is read once at boot. `idp_broker_token_file` (`IDP_BROKER_TOKEN_FILE`) names a file holding the token instead, **read on every use** (a few hundred bytes; a login is not a hot path, so there is no cache to go stale). A Secret volume's symlink swap and a rename-into-place write are both seen on the next call. The same pattern as `OTEL_EXPORTER_OTLP_HEADERS_FILE`.

- The file wins while it yields a token; `idp_broker_token` stays supported and is the fallback.
- A missing, unreadable or empty file never takes sign-in down: the last token read keeps being used, and one warning a minute says why. It recovers by itself.
- The path is not a secret; the file's content is. Do not make the field `sensitive`.
- Rotation is the issuer side's job: mint the new token, deliver it, and let the old one live until its expiry (≥ 10 days of overlap for a 30-day token) so a consumer that has not re-read yet still works.
