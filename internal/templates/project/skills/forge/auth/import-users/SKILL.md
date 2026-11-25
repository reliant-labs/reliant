---
name: import-users
description: Moving users from another identity provider (Supabase, Auth0, Firebase, Cognito, your own table) into a managed identity without a password reset — the import file (JSONL or CSV) with each user's id, password hash, verified address, social links, metadata and roles; the order (declare identity and social, converge, dry-run, import, cut over); idempotent re-runs; the row outcomes and exit codes; rehearsing on the dev IdP.
---

# Import users from another identity provider

An app moving onto `forge.ManagedIdentity` from another provider keeps its users. `forge cloud identity import` creates each one in the env's identity with:

- **the same user id**, which becomes the token `sub`, so every row your app keys by user id stays valid and no id is remapped;
- **the password hash**, so nobody resets a password: they sign in once more with the password they already had;
- **the verified flag** of the address;
- **social links** ("Sign in with GitHub" lands on the same account), metadata and roles.

There is no token exchange: a session from the old provider is not carried over, and each person signs in once more.

## The file

JSONL, one user per line, version 1:

```json
{"v":1,"id":"4f1c2a9e-…","email":"ada@acme.com","email_verified":true,
 "given_name":"Ada","family_name":"Lovelace","display_name":"Ada",
 "password_hash":"$2a$10$…",
 "links":[{"provider":"github","provider_user_id":"583231","provider_user_name":"ada"}],
 "metadata":{"legacy_plan":"pro"},"roles":["member"]}
```

| Field | Rule |
|---|---|
| `id` | Required. 1-200 of `[A-Za-z0-9._@-]` (a UUID fits). Your old provider's user id. |
| `email` | Required. A bare address; it is also the login name. |
| `email_verified` | The old provider's flag. An unverified account signs in only once its owner proves the address; no mail is sent at import. |
| `given_name`, `family_name` | Derived from the address when empty. `display_name` is optional. |
| `password_hash` | Optional. bcrypt (`$2a$`, `$2b$`, `$2y$`, cost 10-16 on a default instance) always; `$argon2id$`/`$argon2i$`, `$scrypt$`, `$pbkdf2…` only where the instance verifies them. Anything else is refused per row. **There is no plaintext field**, and a `password` field is an error. |
| `links` | `provider` is a social provider the identity declares (`github`, `google`); `provider_user_id` is the provider's STABLE id: GitHub's numeric user id, Google's `sub`. Never a login or an address. |
| `metadata` | Keys under `forge.` are forge's and refused. |
| `roles` | Console-managed roles of the identity. A declared role (`members_from = "declared"`) is refused: its members are the declaration's, and a grant here would be revoked by the next converge. |

Unknown fields are refused, so a misspelt field is an error rather than silently dropped.

CSV works too: a header row, then one user per row. Columns `id, email, email_verified, given_name, family_name, display_name, password_hash, roles` (separated by `;` or spaces), one `link.<provider>` column per provider (`583231` or `583231:ada`), and one `meta.<key>` column per metadata key.

The hash file is a credential for every account in it. Produce it on a machine you trust, never commit it, and delete it after the cut-over. forge never prints, logs or writes a hash back out.

### From Supabase

`auth.users.encrypted_password` is bcrypt and imports as it is. `auth.identities` holds the social links: `provider` plus `provider_id` (for GitHub, the numeric id). `email_confirmed_at IS NOT NULL` is `email_verified`.

## The order

1. **Declare** the identity and its social providers in `deploy/kcl/identity.k` (`social = [forge.IdentityGitHub {…}]`), with the roles the file names.
2. **Converge**, then check every provider the file links is applied: `forge cloud identity status <env>` shows `Social: github APPLIED`. A row linking a provider that is not applied is refused (`provider_not_applied`).
3. **Rehearse** on the dev IdP: `forge env up dev`, then `forge cloud identity import dev --file users.jsonl`. A host-run identity is imported into directly, with the same library the control plane runs.
4. **Dry run** against the target env and fix what it reports:
   ```bash
   forge cloud identity import prod --file users.jsonl --dry-run --report dry.jsonl
   ```
   A dry run cannot see a social link another account already holds (`link_taken`); only the write reports that.
5. **Import**:
   ```bash
   forge cloud identity import prod --file users.jsonl --report report.jsonl
   ```
6. **Cut over**: point the app at the managed identity (drop the `config.k` issuer override, `auth/external-provider`), deploy, and sign in as an imported user.

New sign-ups during the window still land on the old provider. Export again just before the cut-over and import the new file: it is idempotent, so the rows already imported come back `unchanged`.

## Re-runs, outcomes and exit codes

The import is **idempotent by id**. Re-running a file converges and never duplicates, so a re-run is also how an import that stopped resumes. An existing account is left alone unless `--update-existing` (profile, verified flag, links, metadata and roles are added or corrected; nothing is removed). Its password hash is overwritten only with `--update-passwords`, a separate opt-in because overwriting a live account's password is the one change a person notices.

| Outcome | Meaning |
|---|---|
| `created` | The account was created. |
| `updated` | It existed and a difference was applied. The reason names what (`roles,links`). |
| `unchanged` | It existed and nothing was written. `differs:<fields>` names what `--update-existing` would change. |
| `conflict` | Another account holds the address (`email_taken_by_other_id`), the username or the social link (`link_taken`), or the id holds another address. Never merged and never moved. |
| `invalid` | The row is wrong: `invalid_id`, `invalid_email`, `unsupported_hash`, `invalid_hash` (the instance's cost bounds), `unknown_provider`, `invalid_provider_user_id`, `unknown_role`, `declared_role`, `reserved_metadata_key`, or an unreadable line. |
| `failed` | The instance failed the write. Re-run the file. |

Exit 0 when every row is `created`, `updated` or `unchanged`; 3 when any row is a conflict or invalid; 1 when a row failed. `--report` writes `{line, id, outcome, reason}` per row; `--json` prints the totals and every row.

A hosted import goes through the env's control plane, at most 500 rows per call (`--batch`), and needs `identity:manage` held by a person, as `forge cloud identity export` does. A control plane that does not serve the import yet is reported as such, and nothing is written.

## In code

The same import is a library call, `pkg/identity`'s `ImportUsers` on a `Reconciler`, with `ParseImportFile` for the file. `forge project libraries identity` prints the signatures.
