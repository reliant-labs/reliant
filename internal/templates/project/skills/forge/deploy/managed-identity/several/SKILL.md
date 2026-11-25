---
name: several
description: Several ManagedIdentity instances in one env (Bundle.identities) — brokers, field collisions, share_with, social clients, the host IdP, the control-plane gate.
---

# Several identities in one env: `Bundle.identities`

An env can declare more than one identity, each its **own instance** (own issuer, signing keys, users and Secrets), keyed by `ManagedIdentity.name`: an operator identity for staff beside the app's end users, say. A token from one is never valid at the other.

```python
identities = [
    forge.ManagedIdentity {                                  # name = "identity": the operators
        roles = [forge.IdentityRole {key = "operator"}]
        applications = [forge.IdentityApplication {name = "console", workload = "admin-api"}]
        consumers = [forge.IdentityConsumer {workload = "admin-api"}, forge.IdentityConsumer {workload = "gateway"}]
    }
    forge.ManagedIdentity {
        name = "users"                                       # the app's end users
        project_name = "acme-app"
        applications = [forge.IdentityApplication {name = "app", workload = "api"}]
        consumers = [
            forge.IdentityConsumer {workload = "api"}
            forge.IdentityConsumer {workload = "gateway", omit = ["roles_claim"],
                fields = forge.IdentityFields {issuer = "users_jwt_issuer", audience = "users_jwt_audience", scopes = "users_oidc_scopes", jwks = "users_jwt_jwks_snapshot"}}
        ]
    }
]
```

- `identity = X` is sugar for `identities = [X]`: the two render **byte-identically**, and one identity is recorded as `shape.identity` either way. Set one or the other, never both. Names are unique and match `^[a-z][a-z0-9-]{0,23}$`. The name is the instance's key: renaming one is deleting it and creating another.
- **Declare `applications` and `consumers` on each.** Left to derive them, both identities would broker the same `api` workload, which is refused: an application belongs to one identity, so a workload is the `native_broker` of at most one.
- **One config field, one source.** A workload that reads two identities maps one's outputs onto other config fields (`IdentityConsumer.fields`) or `omit`s them; two identities filling `jwt_issuer` on one workload is refused, naming both and the fix. Its file outputs are mounted per identity: the default identity's at `/var/run/secrets/forge/identity[-jwks]`, another's at `...identity[-jwks]--<name>` (only when the workload reads several; one identity mounts as it always did, whatever its name).
- Secrets and managed secrets are already per name: `forge-identity-<name>` (+ `-jwks`), `RELIANT_IDENTITY_<NAME>_<KEY>`.
- `forge.IdentityRef {output = "issuer", identity = "users"}` names the identity it reads. Empty is accepted only while the env declares one; with several it is refused as ambiguous, listing the names.
- `share_with` is per name: a sharer's identity reuses the owner env's identity **of the same name** (`forge lint`'s `identity-share-with` judges it across envs).
- **Social clients are per identity**: each issuer has its own callback, so two identities of one env declaring the same `client_id` are refused; register one OAuth app per identity.
- **At most one identity per env on `forge.IdentityOnHost`**: a host-run Zitadel serves one instance per env (its default instance), so two would share an issuer. `forge env up` converges that one; the others are hosted.
- The issuer slug of a named instance is `release.IdentitySlugFor`: `<project>-<env>-<name>-<hash>`, and exactly `IdentitySlug`'s for the name `identity`, so the instance every env already has keeps its issuer.
- Several identities are recorded as `shape.identities` (no `shape.identity`). A control plane that does not advertise the `converges-several-identities` capability would converge none of them, so `forge env deploy` **refuses** to declare or record such a shape there; a shape with one identity needs nothing new of it.

Every `forge cloud identity` verb takes `--identity <name>` to act on one of them (`forge cloud identity invite prod --identity users --email person@example.com --role member`). It defaults to the env's only identity; with several, every verb but `status` refuses to guess and lists them, and `status` shows each. A request names its identity only where that matters (several, or one not named `identity`) and only to a control plane that addresses identities by name; an older one is never sent a name it would ignore: a verb that needs one is refused there, and `status` shows the one it serves and says so.
