---
name: api-keys
description: API keys as a credential — what every scaffold already wires (`X-API-Key` or `Bearer fk_…`, routed by shape to a key validator installed after `OpenInfra`), the `APIKeyValidator` seam you fill in over your own table on the handlers' pool, and `forge/pkg/apikey`'s Generate/Hash/LookupPrefix/Verify.
---

# API keys

Every scaffold already accepts API keys **once you supply a key validator**. Until then they are disabled, which is not the same as unchecked: a key is rejected. Forge ships the wiring and the security primitives (`forge/pkg/apikey`); you own the table, the lookup, and issuing.

## What the scaffold wires

| File (yours) | What it does |
|---|---|
| `pkg/middleware/middleware.go` | `const APIKeyHeader = "X-API-Key"`. `credentialFrom` reads ONE credential: `Authorization: Bearer …`, else the `APIKeyHeader`, else the session cookie. |
| `internal/app/auth.go` | `SetupAuth(cfg *config.Config)` returns `*auth.Authenticator`, built as `auth.NewAuthenticator(validator.Validate)` over the JWT validator. `APIKeyValidator(_ *Infra) auth.KeyValidator` is the key seam. It returns `nil` (keys disabled) until you fill it in. |
| `cmd/<bin>/cmd/serve.go` | `authDeps.Validate = authenticator.Authenticate`, before anything is opened (a server with no authenticator refuses to start). Right after `app.OpenInfra`: `authenticator.UseKeyValidator(app.APIKeyValidator(infra))`. |

The key validator is installed after `OpenInfra` because checking a key reads the database. It runs on the pool the handlers use (`infra.DB` / `infra.ORM`), never a second one. It receives the request's context, so the lookup is cancelled with the request and traced under it. No RPC is mounted until after it is installed.

**The credential's shape picks the validator, not its header.** `auth.Authenticator` sends anything starting with `apikey.KeyPrefix` (`fk_`, the form `apikey.Generate` issues) to the key validator, and everything else to the JWT validator. So `X-API-Key: fk_…` and `Authorization: Bearer fk_…` are the same key. Neither validator ever sees the other's credential.

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
       prefix     TEXT PRIMARY KEY,          -- apikey.LookupPrefix(key): the indexed handle
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
           prefix, err := apikey.LookupPrefix(key)
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

   Return errors freely. The caller only reads `invalid API key`, and your error, including a store failure's text, reaches the log as the cause. To let handlers tell a key-authenticated call apart (for example, to refuse key management made with a key), set `Claims.Raw["auth_method"] = "api_key"` here. `enrichClaims` in `pkg/middleware/middleware.go` still runs after this, as it does for a JWT.

3. **Issuing.** Use a custom rpc. Do not name it `CreateApiKey`, which forge would read as CRUD. Name it `IssueApiKey`, generate the key server-side, store its prefix and hash, and return the plaintext **once**:

   ```go
   user, err := middleware.GetUser(ctx)        // the key belongs to the caller
   if err != nil {
       return nil, err
   }
   key, err := apikey.Generate()               // "fk_" + 32 base62 chars, ~190 bits
   if err != nil {
       return nil, svcerr.Wrap(err)
   }
   prefix, _ := apikey.LookupPrefix(key)
   // s.deps.DB: a `DB *sql.DB` Deps field, filled from infra.DB in compose.go.
   // A prefix collision violates the PRIMARY KEY: generate again.
   if _, err := s.deps.DB.ExecContext(ctx,
       `INSERT INTO api_keys (prefix, key_hash, user_id) VALUES ($1, $2, $3)`,
       prefix, apikey.Hash(key), user.UserID); err != nil {
       return nil, svcerr.Wrap(err)
   }
   return connect.NewResponse(&pb.IssueApiKeyResponse{Key: key}), nil
   ```

   **Revoking** sets `revoked_at`. The validator's `WHERE` clause is where revocation and expiry are enforced. `pkg/apikey` knows nothing about your rows.

4. **Check it.** `middleware_test.go`'s `TestAuthWiring_APIKeyAndBearer` covers the credential channels and precedence. Exercise your own table end to end with `curl -H 'X-API-Key: fk_…'` against an auth-required rpc.

## Variations

- **A key-only service**: in `SetupAuth`, return `auth.NewAuthenticator(nil), nil`. Every bearer token is then rejected (`auth.ErrBearerTokensDisabled`), and the service needs no issuer, JWKS or `jwt_secret`. `auth/dev-loop` covers when the dev IdP is scaffolded at all.
- **A different header**: change `APIKeyHeader`. A browser client sending it cross-origin also needs it in `cors_allow_headers`. An API key rarely belongs in a browser.
- **A key format without `fk_`**: the Authenticator routes by `apikey.KeyPrefix`. For another format, assign your own `func(ctx context.Context, credential string) (*middleware.Claims, error)` to `authDeps.Validate` in `serve.go`.
- **Turning keys off again**: return `nil` from `APIKeyValidator`.

## Rules

- Store the SHA-256 **hash** plus an indexed **prefix**. Never store the plaintext, and never a reversible encryption of it. A fast hash is correct here: bcrypt and argon2 defend low-entropy passwords, not a ~190-bit random key.
- Return the plaintext exactly once, at issue. A key you can re-display is a key you are storing.
- Compare with `apikey.Verify` (constant time), never `==`. A byte-by-byte compare leaks the hash through timing.
- Check keys on the pool `OpenInfra` opened, inside `APIKeyValidator`. Never open a second `sql.Open` pool in `SetupAuth`.
- Revocation and expiry belong in your lookup's `WHERE` clause.
