---
name: migration-v0.1.46-api-keys
description: Upgrade v0.1.46 auth wiring to context-aware validation and API keys, including owned middleware, SetupAuth and serve.go edits.
relevance: migration
---

# v0.1.46 — API keys and context-aware validation

Use when `grep -n 'authDeps.Validate = authValidate' cmd/*/cmd/serve.go` matches, or `internal/app/auth.go` declares `func SetupAuth(cfg *config.Config) (func(token string) (*auth.Claims, error), error)`.

`serve.go`, `internal/app/auth.go` and `pkg/middleware/middleware.go` are yours, so forge makes none of these edits for you. One exception: `forge project upgrade` refreshes `pkg/middleware/middleware.go` and `middleware_test.go` if you never edited them. The refreshed file's `AuthDeps.Validate` takes a context, so the old `serve.go` line `authDeps.Validate = authValidate` stops compiling until step 3 below is done.

### What was wrong

`X-API-Key` reached no validator, and the skill described wiring `SetupAuth` did not offer. A DB-backed validator could not use the shared pool because `SetupAuth` ran before `OpenInfra`.

### What changed

- `forge/pkg/auth`: `Authenticator` (`NewAuthenticator`, `UseKeyValidator`, `Authenticate`) routes a credential by shape. Anything starting with `apikey.KeyPrefix` (`fk_`) goes to the installed `KeyValidator`, with the request context. Everything else goes to the JWT validator. `KeyValidatorFunc` adapts a closure. See `auth/api-keys` for every way it fails closed.
- `forge/pkg/authn`: `Policy.ValidateContext` is `Validate` with the request context; set exactly one of the two.
- **Behavior change in `pkg/authn`:** a validator that returns `(nil, nil)` is now rejected with `CodeUnauthenticated`. Before, the request reached the handler with no claims, and an `auth_required` rpc whose handler never called `GetUser` (every CRUD shim) served it. If a validator of yours returns `(nil, nil)` on purpose, return an error instead.

### Steps

If you never edited the files, delete them and let forge write today's versions:

```bash
rm cmd/<bin>/cmd/serve.go internal/app/auth.go pkg/middleware/middleware.go pkg/middleware/middleware_test.go
forge project rescaffold cmd/<bin>/cmd/serve.go internal/app/auth.go pkg/middleware/middleware.go pkg/middleware/middleware_test.go
```

Otherwise port these edits by hand:

1. **`pkg/middleware/middleware.go`**

   - Declare `const APIKeyHeader = "X-API-Key"`.
   - Select headers by presence: if `len(h.Values("Authorization")) != 0`, require `Bearer ` or return `""`; never fall through on a malformed header.
   - Between that branch and the cookie, add:

     ```go
     if len(h.Values(APIKeyHeader)) != 0 {
         return strings.TrimSpace(h.Get(APIKeyHeader))
     }
     ```

   - Change `AuthDeps.Validate` to `func(ctx context.Context, credential string) (*Claims, error)`.
   - In `NewAuthInterceptor`, replace `Validate: deps.Validate,` with `ValidateContext: deps.Validate,`.

2. **`internal/app/auth.go`**

   - Change `SetupAuth`'s result to `(*auth.Authenticator, error)`.
   - Change its last line to `return auth.NewAuthenticator(validator.Validate), nil`.
   - Add the key seam:

     ```go
     // APIKeyValidator returns the validator API keys are checked with; nil leaves them disabled.
     func APIKeyValidator(_ *Infra) auth.KeyValidator {
         return nil
     }
     ```

3. **`cmd/<bin>/cmd/serve.go`**

   - Replace `authValidate, aerr := app.SetupAuth(cfg)` with `authenticator, aerr := app.SetupAuth(cfg)`.
   - Replace `authDeps.Validate = authValidate` with `authDeps.Validate = authenticator.Authenticate`.
   - Directly after the `app.OpenInfra` error check, add:

     ```go
     authenticator.UseKeyValidator(app.APIKeyValidator(infra))
     ```

4. **`pkg/middleware/middleware_test.go`**: in `TestNewAuthInterceptor_ValidatorConfigured`, give the stub validator the new signature, `func(context.Context, string) (*Claims, error)`, and call it as `validate(context.Background(), "anything")`.

To accept keys, fill in `APIKeyValidator` over your own table: `forge skill load auth/api-keys`.

### Verify

```bash
go build ./... && go test ./pkg/middleware/
curl -si -X POST localhost:<port>/<pkg>.<Service>/<Method> -H 'content-type: application/json' -d '{}' \
  -H 'X-API-Key: fk_0000000000000000000000000000000' | head -1
```

Before you fill in `APIKeyValidator`, expect `401` with `API keys are not enabled on this server`. That answer proves the header is read. A bearer token works exactly as before.
