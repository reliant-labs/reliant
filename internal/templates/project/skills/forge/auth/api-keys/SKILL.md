---
name: api-keys
description: API keys as a credential — what every scaffold already wires (`X-API-Key` or `Bearer fk_…`, routed by shape to a key validator installed after `OpenInfra`), the `APIKeyValidator` seam you fill in over your own table on the handlers' pool, `forge/pkg/apikey`'s Generate/Hash/LookupPrefix/Verify, your product's own key prefix (`apikey.Format`, `APIKeyFormat`) in place of forge's `fk_`, and `Claims.Credential` (`IsAPIKey`), the unforgeable mark of a key-authenticated call.
---

# API keys

Every scaffold already accepts API keys **once you supply a key validator**. Until then they are disabled, which is not the same as unchecked: a key is rejected. Forge ships the wiring and the security primitives (`forge/pkg/apikey`); you own the table, the lookup, and issuing.

## What the scaffold wires

| File (yours) | What it does |
|---|---|
| `pkg/middleware/middleware.go` | `const APIKeyHeader = "X-API-Key"`. `credentialFrom` reads ONE credential: `Authorization: Bearer …`, else the `APIKeyHeader`, else the session cookie. |
| `internal/app/auth.go` | `SetupAuth(cfg *config.Config)` returns `*auth.Authenticator`, built as `auth.NewAuthenticator(validator.Validate)` over the JWT validator. `APIKeyValidator(_ *Infra) auth.KeyValidator` is the key seam. It returns `nil` (keys disabled) until you fill it in. `APIKeyFormat() apikey.Format` names the prefix keys carry (forge's `fk_` until you change it), and `SetupAuth` installs it with `authenticator.UseKeyFormat(APIKeyFormat())`. |
| `cmd/<bin>/cmd/serve.go` | `authDeps.Validate = authenticator.Authenticate`, before anything is opened (a server with no authenticator refuses to start). Right after `app.OpenInfra`: `authenticator.UseKeyValidator(app.APIKeyValidator(infra))`. |

The key validator is installed after `OpenInfra` because checking a key reads the database. It runs on the pool the handlers use (`infra.DB` / `infra.ORM`), never a second one. It receives the request's context, so the lookup is cancelled with the request and traced under it. No RPC is mounted until after it is installed.

**The credential's shape picks the validator, not its header.** `auth.Authenticator` sends anything starting with the installed `apikey.Format`'s prefix, or with forge's `fk_` (`apikey.KeyPrefix`, which every Format accepts), to the key validator, and everything else to the JWT validator. So `X-API-Key: fk_…` and `Authorization: Bearer fk_…` are the same key. Neither validator ever sees the other's credential. The examples below show the default `fk_`; [Your product's key prefix](#your-products-key-prefix) replaces it.

**One credential per request, no fallback.** Header presence selects the credential, even when empty or malformed. Any `Authorization` header blocks fallback to `X-API-Key`; any `X-API-Key` header blocks fallback to the session cookie. A request with an invalid Bearer token and a valid key is rejected. An explicit header always beats the ambient session cookie.

### How each case fails

On an rpc without `auth_required: false`:

| Presented | Result |
|---|---|
| nothing | `401`: missing credential |
| `fk_…` while `APIKeyValidator` returns `nil` | `401 API keys are not enabled on this server` |
| `fk_` too short to carry a lookup prefix | `401 malformed API key`. Your validator, and your store, are never called. |
| a key your validator rejects, or a store error | `401 invalid API key`. Your error never reaches the caller; it is logged server-side as the failure's `cause`. |
| your validator returns `(nil, nil)` | `401`. No principal is a failure. |
| a bad Bearer token beside a good key | `401`. The Bearer token is the credential. |

## Enabling keys

1. **The table.** Write a migration (`forge db migration new add_api_keys`). Store the **hash** and an indexed **lookup prefix**, never the key:

   ```sql
   CREATE TABLE api_keys (
       prefix     TEXT PRIMARY KEY,          -- APIKeyFormat().LookupPrefix(key): the indexed handle
       key_hash   TEXT NOT NULL,             -- apikey.Hash(key): SHA-256, hex
       user_id    TEXT NOT NULL,             -- whose key it is
       expires_at TIMESTAMPTZ,
       revoked_at TIMESTAMPTZ,
       created_at TIMESTAMPTZ NOT NULL DEFAULT now()
   );
   ```

   Make it a plain table, not a `// forge:entity`. A generated `CreateApiKey` would let a client choose the stored hash, and the plaintext must be generated server-side and shown once.

2. **The validator.** In `internal/app/auth.go`, name the `_ *Infra` parameter and replace the body of `APIKeyValidator`. It does one indexed read by prefix on the handlers' pool, then a constant-time compare:

   ```go
   func APIKeyValidator(infra *Infra) auth.KeyValidator {
       return auth.KeyValidatorFunc(func(ctx context.Context, key string) (*auth.Claims, error) {
           prefix, err := APIKeyFormat().LookupPrefix(key)
           if err != nil {
               return nil, err
           }
           var hash, userID string
           err = infra.DB.QueryRowContext(ctx, `SELECT key_hash, user_id FROM api_keys
               WHERE prefix = $1 AND revoked_at IS NULL
               AND (expires_at IS NULL OR expires_at > now())`, prefix).Scan(&hash, &userID)
           if err != nil {
               return nil, err // sql.ErrNoRows included: an unknown key
           }
           if !apikey.Verify(key, hash) { // constant time
               return nil, errors.New("key does not match")
           }
           return &auth.Claims{UserID: userID}, nil
       })
   }
   ```

   Return errors freely. The caller only reads `invalid API key`, and your error, including a store failure's text, reaches the log as the cause. `enrichClaims` in `pkg/middleware/middleware.go` (or your `ClaimsEnricher`) still runs after this, as it does for a JWT.

   **You don't mark the claims as a key's; forge does.** The Authenticator stamps every claims set your validator returns with `Claims.Credential = auth.APIKeyCredential(<lookup prefix>)`, and every token's with `auth.TokenCredential()`. The marker is unforgeable by a token. Its fields are unexported and the field is `json:"-"`, so no JWT payload, and no `UserResolver` that decodes one, can set it. The token path overwrites whatever its validator returned, and the auth interceptor restores it after Enrich. Never mark a key in `Claims.Raw`: `Raw` also holds a token's decoded claims, so anyone who can get a token issued with that claim can forge it.

   A key with no person behind it (a per-tenant key) can return its tenant and role on the claims, for example `&auth.Claims{UserID: "apikey:" + prefix, OrgID: tenantID, Role: role}`. Your enricher then trusts those fields only for a key:

   ```go
   func (s *service) EnrichClaims(ctx context.Context, claims *auth.Claims) (*auth.Claims, error) {
       if claims.IsAPIKey() {
           return claims, nil // OrgID and Role are the key row's, checked by APIKeyValidator
       }
       // A person: map claims.UserID through your members table. A token's own
       // org_id / role claims are never trusted here.
       ...
   }
   ```

   `claims.Credential.KeyPrefix()` is the key's lookup prefix, its row's primary key. Use it to record `last_used_at` or to name the key in an audit entry.

3. **Issuing.** Use a custom rpc. Do not name it `CreateApiKey`, which forge would read as CRUD. Name it `IssueApiKey`, generate the key server-side, store its prefix and hash, and return the plaintext **once**:

   ```go
   user, err := middleware.GetUser(ctx)        // the key belongs to the caller
   if err != nil {
       return nil, err
   }
   // s.deps.KeyFormat: a `KeyFormat apikey.Format` Deps field, filled with
   // app.APIKeyFormat() in compose.go (a handler cannot import internal/app).
   key, err := s.deps.KeyFormat.Generate()     // its prefix + 32 base62 chars, ~190 bits
   if err != nil {
       return nil, svcerr.Wrap(err)
   }
   prefix, _ := s.deps.KeyFormat.LookupPrefix(key)
   // s.deps.DB: a `DB *sql.DB` Deps field, filled from infra.DB in compose.go.
   // A prefix collision violates the PRIMARY KEY: generate again.
   if _, err := s.deps.DB.ExecContext(ctx,
       `INSERT INTO api_keys (prefix, key_hash, user_id) VALUES ($1, $2, $3)`,
       prefix, apikey.Hash(key), user.UserID); err != nil {
       return nil, svcerr.Wrap(err)
   }
   return connect.NewResponse(&pb.IssueApiKeyResponse{Key: key}), nil
   ```

   **Refuse key management made with a key.** A leaked key must not mint more keys or revoke its owner's. At the top of `IssueApiKey` and `RevokeApiKey`:

   ```go
   if authn.IsAPIKey(ctx) { // github.com/reliant-labs/forge/pkg/authn
       return nil, svcerr.Wrap(svcerr.PermissionDenied("not allowed with an API key; sign in"))
   }
   ```

   In a handler test, build a key caller with `testkit.WithAPIKey("abcd1234")` (`github.com/reliant-labs/forge/pkg/testkit`) beside the key row's role and tenant: `AuthedContext(t, testkit.WithAPIKey("abcd1234"), testkit.WithRoles("dataplane"), testkit.WithOrgID(tenantID))` (the package's generated test helper) in process, or the same options to `WithPrincipal(…)` for the `NewTest<Svc>Server` client. It sets `Credential: auth.APIKeyCredential("abcd1234")` and `UserID: "apikey:abcd1234"`, as the Authenticator would.

   **Revoking** sets `revoked_at`. The validator's `WHERE` clause is where revocation and expiry are enforced. `pkg/apikey` knows nothing about your rows.

4. **Check it.** `middleware_test.go`'s `TestAuthWiring_APIKeyAndBearer` covers the credential channels and precedence. Exercise your own table end to end with `curl -H 'X-API-Key: fk_…'` against an auth-required rpc.

## Your product's key prefix

Your users read the prefix in every key they hold, so a product built on forge names its keys, not forge's `fk_`. The prefix is one value, `APIKeyFormat()` in `internal/app/auth.go`:

```go
func APIKeyFormat() apikey.Format {
    return apikey.MustFormat("acme_") // was apikey.Format{}, forge's "fk_"
}
```

A prefix is lowercase letters and digits ending in `_` (`acme_`, `acme2_`), starts with a letter, and is at most 16 bytes. `apikey.NewFormat` returns an error for anything else, `MustFormat` panics, and both refuse `fs_…`, the session ids' prefix.

Three places read it, and each must use the same Format:

1. **Routing.** `SetupAuth` calls `authenticator.UseKeyFormat(APIKeyFormat())`, so a credential with your prefix goes to the key validator. Without it, an `acme_…` key is treated as a bearer token and every request fails.
2. **Lookup.** Your `APIKeyValidator` calls `APIKeyFormat().LookupPrefix(key)`.
3. **Issuing.** The code that mints keys calls `Generate` and `LookupPrefix` on the same Format, passed to it as a Deps field (step 3 above).

**Existing `fk_` keys keep working.** Every Format also accepts `fk_` keys and returns the same lookup handle for them as `apikey.LookupPrefix` did. The table needs no migration, and nothing has to be reissued. Only the active custom prefix and `fk_` are accepted; changing from one custom prefix to another requires reissuing those custom keys.

**A project scaffolded before `APIKeyFormat` existed** calls `apikey.Generate` and `apikey.LookupPrefix` directly. To switch it, add the `APIKeyFormat` function above to `internal/app/auth.go`. Call `authenticator.UseKeyFormat(APIKeyFormat())` where the authenticator is built (after `auth.NewAuthenticator`). Then replace `apikey.Generate()` and `apikey.LookupPrefix(key)` with the Format's methods in the validator and in the issuing code (a store or handler, given the Format through its Deps). `apikey.Hash` and `apikey.Verify` don't change. A server that uses forge's older `auth.Validator` interceptor in place of the Authenticator calls `validator.UseKeyFormat(APIKeyFormat())`, so its claims record the lookup handle.

There is no `forge.yaml` setting for the prefix. The code that issues and checks keys is yours, so the prefix lives in it, where a test can read it.

## Variations

- **A key-only service**: in `SetupAuth`, pass `nil` to `auth.NewAuthenticator`, call `UseKeyFormat` as above, and return the authenticator. Every bearer token is then rejected (`auth.ErrBearerTokensDisabled`), and the service needs no issuer, JWKS or `jwt_secret`. `auth/dev-loop` covers when the dev IdP is scaffolded at all.
- **A different header**: change `APIKeyHeader`. A browser client sending it cross-origin also needs it in `cors_allow_headers`. An API key rarely belongs in a browser.
- **A key format that is not a prefix and 32 random characters**: the Authenticator routes by its `apikey.Format`. For a wholly different format, assign your own `func(ctx context.Context, credential string) (*middleware.Claims, error)` to `authDeps.Validate` in `serve.go`.
- **Turning keys off again**: return `nil` from `APIKeyValidator`.

## Rules

- Store the SHA-256 **hash** plus an indexed **prefix**. Never store the plaintext, and never a reversible encryption of it. A fast hash is correct here: bcrypt and argon2 defend low-entropy passwords, not a ~190-bit random key.
- Return the plaintext exactly once, at issue. A key you can re-display is a key you are storing.
- Compare with `apikey.Verify` (constant time), never `==`. A byte-by-byte compare leaks the hash through timing.
- Check keys on the pool `OpenInfra` opened, inside `APIKeyValidator`. Never open a second `sql.Open` pool in `SetupAuth`.
- Revocation and expiry belong in your lookup's `WHERE` clause.
- Tell a key from a person with `claims.IsAPIKey()` or `authn.IsAPIKey(ctx)`, never with a value in `Claims.Raw`.
