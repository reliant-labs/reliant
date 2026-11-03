---
name: auth
description: Authentication — managed identity as the default (forge.ManagedIdentity fills the validator's issuer, audience and the login broker's credentials; bring-your-own issuer is the escape hatch), the owned SetupAuth validator (JWT default with OIDC discovery, Clerk/Firebase variants), the fail-closed interceptor, mixed public/authenticated APIs via auth_required, and how to tell from source whether a server authenticates. Sub-skills: authorization, dev-loop, frontend, shared-issuer, api-keys.
---

# Authentication

Authentication is a **middleware, nothing more**: an interceptor validates the request's credential — a bearer token, or an API key — and puts `*Claims` on the context, **fail-closed** — no credential, or an invalid one, is rejected before any handler runs. Forge scaffolds the validator choice in OWNED code (`internal/app/auth.go`) and wires the interceptor.

**Which RPCs are callable without credentials is declared in the proto**, per rpc, with `option (forge.v1.method) = { auth_required: false }` — projected into `pkg/middleware/procedures_gen.go`, which the interceptor reads. No second list to keep in step.

Everything downstream reads the context: `middleware.GetUser(ctx)` answers **who the caller is**. What that caller may DO, and which rows they may see, is application policy no annotation can express.

Sub-skills: **`auth/authorization`** (`Enrich`, provisioning), **`auth/shared-issuer`** (Zitadel roles, the JWKS snapshot, a rotating broker token), **`auth/dev-loop`** (a token locally, the dev IdP `forge env up` converges), **`auth/frontend`** (native sign-in, the login broker), **`auth/api-keys`**.

## The default: managed identity

A project with a frontend declares its users once, in `deploy/kcl/identity.k` (`forge.ManagedIdentity`: roles, machine users, seed users), and every env imports it. Only where it runs differs: the dev IdP in dev (`forge.IdentityOnHost {infra = "idp"}`, converged by `forge env up`), the control plane's identity instance in staging and prod (the default runtime, which needs `control_plane`). From that declaration forge fills the API's `jwt_issuer`, `jwt_audience`, `oidc_scopes`, `oidc_client_id`, `oidc_redirect_uri`, `idp_login_uri`, `idp_base`, `idp_broker_token`, `jwt_jwks_snapshot` and `jwt_roles_claim` (`forge.IdentityRef`). You write no `config.k` line for them, copy no client id, and `SetupAuth` below needs no edit. Roles reach handlers as `Claims.Roles` (`auth/shared-issuer`).

- **A hand-set field is refused.** A workload that sets `JWT_ISSUER`, `JWT_AUDIENCE`, `OIDC_CLIENT_ID`, `OIDC_REDIRECT_URI` or `IDP_BASE` beside `Bundle.identity` fails the render, naming both. One source per field.
- **Dev:** `forge env up` converges the identity on the host IdP and writes the broker token and ids to the env's secret store; `.forge/identity/<name>.json` holds the non-secret outputs. `auth/dev-loop` has the detail and the failures.
- **Hosted:** the control plane delivers the same values; `deploy/managed-identity` has the fields, what each runtime receives, and what is refused.
- **Bring your own issuer** (Auth0, Okta, Keycloak, a Zitadel you run) stays supported and is the escape hatch: declare no `identity` in that env and set `jwt_issuer` / `oidc_client_id` in its `config.k` (see "Swapping the IdP").

## Choosing the validator (owned code, not config)

Authentication is CODE, not a `forge.yaml` provider enum. Each service scaffolds one editable file, `internal/app/auth.go`, whose `SetupAuth` returns the `*auth.Authenticator` `cmd serve.go` mounts — a JWT validator wrapped so API keys can join it later (`auth/api-keys`). Picking a validator is a wiring choice; WHERE this deployment's issuer lives is per-environment DATA on the typed config (declared in `proto/<project>/config/v1/config.proto`, pinned in `deploy/kcl/<env>/config.k`). **`SetupAuth` reads no environment variable** — the app has one configuration channel.

`SetupAuth(cfg *config.Config)` is unaffected by per-binary config (`forge skill load binaries`, "Config"): `config.Config` is always a real type, whether it aliases a single project-global `AppConfig` or — in an all-binary-annotated project — the config message bound to the primary server binary. `SetupAuth` never needs to know which.

```go
// internal/app/auth.go — YOURS (scaffolded once, never regenerated). Abridged.
func SetupAuth(cfg *config.Config) (*auth.Authenticator, error) {
	// Issuer but no explicit JWKS URL: discover where its keys are. Passed as a
	// func, not resolved here, so it re-runs on every live attempt and a
	// snapshot can cover an issuer that is down at boot.
	var discover func(ctx context.Context) (string, error)
	if cfg.JwtJwksUrl == "" && cfg.JwtIssuer != "" && cfg.JwtSecret == "" {
		discover = func(ctx context.Context) (string, error) {
			meta, err := oauth2.Discover(ctx, nil, cfg.JwtIssuer)
			if err != nil { return "", fmt.Errorf("discover OIDC issuer %q: %w", cfg.JwtIssuer, err) }
			return meta.JWKSURI, nil
		}
	}
	validator, err := auth.NewValidator(auth.Config{
		Provider: "jwt",
		JWT: auth.JWTConfig{
			SigningMethod: cfg.JwtSigningMethod,
			Issuer: cfg.JwtIssuer, Audience: cfg.JwtAudience,
			JWKSURL: cfg.JwtJwksUrl, JWKSDiscover: discover, Secret: cfg.JwtSecret,
			RolesClaim: cfg.JwtRolesClaim,
			JWKSSnapshot: auth.JWKSSnapshot{
				File: cfg.JwtJwksSnapshotFile, Inline: cfg.JwtJwksSnapshot,
				MaxAge: cfg.JwtJwksSnapshotMaxAge.AsDuration(),
			},
		},
	})
	if err != nil { return nil, fmt.Errorf("build JWT validator: %w", err) }
	return auth.NewAuthenticator(validator.Validate), nil
}
```

**Nothing in the scaffold names an identity provider** — no vendor branch, no default endpoint. Every issuer arrives as a URL on the typed config.

- **Set `jwt_issuer` alone and the JWKS endpoint is DISCOVERED** from `/.well-known/openid-configuration` — one URL instead of two that must agree. Set `jwt_jwks_url` too and it WINS, which is what a containerized dev IdP needs.
- **Keys are fetched at BOOT** and refreshed in the background, so rotation needs no redeploy — and an unreachable issuer or JWKS endpoint REFUSES TO START, naming the URL, **unless a fresh JWKS snapshot was delivered** (`auth/shared-issuer`). Never soften the refusal into a warning: "boots, accepts nothing, reports healthy" is the worst of both.
- **`jwt_jwks_url` and `jwt_secret` are mutually exclusive** — one validator, one key source; both is rejected at startup, not resolved by precedence. For several issuers at once (an IdP migration), compose `auth.Config.TokenValidators`.
- **`jwt_secret` is `sensitive`** — it projects as a `secretKeyRef`, never an inline manifest value: an HS\* value both verifies AND mints. The OIDC client fields are NOT secrets (see the `auth/frontend` skill).
- **`jwt_signing_method` and the key must agree.** Default `RS256`, so a shared-secret `jwt_secret` needs `HS256` — otherwise a correctly-signed token is rejected on `alg` alone, which reads as a bad secret and is not.
- **Swap the validator freely** — arbitrary code, not a fixed enum. A nil validator makes the server **refuse to start**.

### Swapping the IdP (bring your own issuer)

The escape hatch from managed identity: an env that declares no `identity` takes its issuer from `config.k`.

**Most swaps are a CONFIG change, not a code change.** Any standards-compliant issuer — Auth0, Keycloak, Zitadel, Okta, Supabase, a local dev IdP — needs no constructor: point `jwt_issuer` (plus `jwt_jwks_url` if discovery cannot describe it) at the new issuer and the default validator validates against its keys.

Only a NON-STANDARD claim shape needs code — edit `internal/app/auth.go` and wrap a different validator's `.Validate` in `auth.NewAuthenticator`:

```go
// Clerk — JWKS + Clerk's org/session claim shape (sub / org_id / org_role → Claims).
// Email isn't in a Clerk token by default; sync it via webhook.
validator, err := auth.Clerk(auth.ClerkOpts{JWKSURL: cfg.JwtJwksUrl, Issuer: cfg.JwtIssuer})

// Firebase — Google's JWKS + Firebase shape (sub = uid); token must match a project's
// aud AND issuer. Add a `firebase_project_ids` config field for the list.
validator, err := auth.Firebase(auth.FirebaseOpts{ProjectIDs: strings.Split(cfg.FirebaseProjectIds, ",")})
```

A constructor needing a value the config lacks gets a config field, never an `os.Getenv`. Other non-standard shapes ride `UserResolver`. Point the login broker at the same issuer — both halves must name it, or a session minted by one is rejected by the other.

## Shared-issuer runtime (sub-skill `auth/shared-issuer`)

For an app whose issuer is a shared, operated one (a managed Zitadel), three typed config knobs exist, all inert until set — Clerk, Firebase and generic issuers are unchanged:

- **`jwt_roles_claim`** (default `auto`) maps Zitadel's `urn:zitadel:iam:org:project:<id>:roles` role-to-org map into `Claims.Roles`; the flat `roles` array still wins when present. **Set `jwt_audience` on a Zitadel app** so only your project's roles are read.
- **`jwt_jwks_snapshot_file` / `jwt_jwks_snapshot`** (+ `jwt_jwks_snapshot_max_age`, default 7d): a delivered, dated copy of the issuer's public keys. An issuer that is down at boot no longer prevents starting when a fresh snapshot exists; live keys win as soon as the issuer answers. No usable keys at all still refuses to start.
- **`idp_broker_token_file`**: the login broker's token, re-read on every use, so the issuer's operator can rotate it without a restart.

Load `auth/shared-issuer` for the document format, the exact trust rules and the rotation-overlap case.

## The Claims struct

`Claims` is the canonical auth payload — `UserID` / `Email` / `OrgID` / `Role` / `Roles` — defined in `forge/pkg/auth` and aliased in the owned `pkg/middleware/middleware.go` (`type Claims = auth.Claims`), so library and project code name ONE type.

Read it with `middleware.ClaimsFromContext(ctx)` (`(*Claims, ok)`) on an RPC that may be anonymous, or `middleware.GetUser(ctx)` (`(*Claims, error)` — `CodeUnauthenticated` when none) on one that requires a principal. Additional claim DATA rides the `enrichClaims` hook; additional claim FIELDS go on `forge/pkg/auth.Claims`, never a parallel type.

## The wiring file + interceptor chain

The auth MECHANISM (mode resolution, refusal-to-start, allow-list gate, Bearer parsing, claims plumbing) lives in `forge/pkg/authn`. The project keeps ONE scaffolded-once file, `pkg/middleware/middleware.go`, wiring the things projects customize: the **credential validator** (passed EXPLICITLY into `NewAuthInterceptor(AuthDeps{Validate: fn})`, with the request's context — no package global), the **identity enricher** `enrichClaims`, and the **credential channel** `credentialFrom` (a Bearer header, else `APIKeyHeader`, else the session cookie — one credential per request, never a fallback). It also owns `Claims`, the claims context key, and `GetUser`. The allow-list is not among them — it is generated into `procedures_gen.go`.

**How to tell from SOURCE whether this server authenticates** — two facts, both in the repo, neither of them runtime state:

1. the generated `cmd serve.go` builds `middleware.AuthDeps{AnonymousOK: false}` and threads the OWNED `app.SetupAuth(cfg)` authenticator's `Authenticate` into `AuthDeps.Validate`;
2. the exempt RPCs are `procedures_gen.go`, projected from the protos' `auth_required` declarations.

Mode resolution happens ONCE at construction: a validator → enforce; `ExternalAuth` → passthrough (another interceptor owns identity); neither → **refuse to start**. **No environment variable can run a forge service without authentication**, and no config value can either — which is why the answer is readable in a diff rather than only observable in production.

The chain is a component, `serverkit.NewChain(serverkit.ChainDeps{Auth: …})`, built once in `serve.go`. Order: **trace** (otelconnect, `ChainDeps.Trace`) **→ recovery → request-id → logging → tracing → metrics**, then **deadline → auth → audit → rate-limit → your `Extras` → activity → stream-drain → protovalidate → svcerr**, the last one installed by `NewChain` and `observe.Chain` themselves (`api`). Auth after observability is deliberate — operators see auth failures in the same dashboards as successful traffic, and a rejected call has a span and logs carrying its `trace_id`. The **deadline** (`ChainDeps.Deadline`, a `serverkit.Deadline`) precedes auth, so a stalled credential lookup ends as `DeadlineExceeded`: 30s per unary RPC by default, per rpc from `(forge.v1.method).timeout` (`middleware.ProcedureDeadlines`). What must wrap auth goes there, never in `Extras`. Every slot is defaulted except **`Auth`, which is required: a nil `Auth` is a `NewChain` error, so a server without an authenticator fails at boot**. A slot you set replaces its default; `serverkit.NoAudit`, `NoTrace`, `NoDeadline`, `NoActivity`, `NoStreamDrain` and `NoValidate` drop one, and nothing drops `Auth`. Payload caps ride the chain too (`ReadMaxBytes`/`SendMaxBytes`; zero is 4 MiB).

### Audiences: one listener per kind of caller

`ChainDeps.Auth` is a `serverkit.Authenticator` — the auth interceptor plus the **audience** it admits: `serverkit.AuthFor(serverkit.AudienceUser, authInterceptor)` for your signed-in users. A service declares the audience it serves in its proto, defaulting to `user`:

```proto
service ToolsDaemonService {
  option (forge.v1.service) = { audience: "daemon" };  // not reachable with a user token
  rpc Attach(stream AttachRequest) returns (stream AttachResponse);
}
```

A `serverkit.Listener` refuses to start when it mounts a service whose audience differs from its chain's `Auth.Audience()` — and `serverkit.Run` checks the same over a `Server.Chain`. So a daemon credential can never reach a user RPC, even through a hand-written route set. Serving a second kind of caller is a second chain and a second listener, never a second token type inside one chain:

```go
daemonChain, err := serverkit.NewChain(serverkit.ChainDeps{Auth: serverkit.AuthFor("daemon", daemonAuth)})
daemon, err := serverkit.NewListener(serverkit.ListenerDeps{
    Config: serverkit.ListenerConfig{Addr: ":9090", ReadTimeout: -1}, // long bidi streams
    Chain:  daemonChain,
    Routes: serverkit.RoutesFunc(func(mux *http.ServeMux, opts ...connect.HandlerOption) []string {
        return components.MountToolsDaemon(mux, cfg, logger, opts...)
    }),
})
```

Run the listeners under `lifecyclekit.Supervise` (`services/shutdown`, "Without `serverkit.Run`").

### Publishing one RPC (per-RPC allow-list)

`auth_required: false` publishes ONE rpc to unauthenticated callers. It is
per-rpc, not a service posture, and the default is closed.

```proto
service Status {
  rpc GetVersion(GetVersionRequest) returns (GetVersionResponse) {
    option (forge.v1.method) = { auth_required: false };  // published deliberately
  }
  rpc GetDetail(GetDetailRequest) returns (GetDetailResponse) {}  // no annotation = closed
}
```

Opening an rpc is not the only way to serve a second audience: forge declares
multiple frontends and multiple services in `forge.yaml`, so separate audiences
can have separate applications against separate rpcs.

Each lands in the generated set as connect's own `…Procedure` constant, matched EXACTLY — never by substring, so a `HealthReport` rpc cannot ride along with the health probes. An unannotated rpc defaults to `auth_required: true`: silence never publishes an endpoint. The gRPC probes are always allowed; they run before anything can authenticate.

**A public handler gets NO claims — not even from a caller who presented a valid token.** The allow-list is checked BEFORE the Authorization header is read, so the validator is not consulted at all. Handle the anonymous case explicitly:

```go
if claims, ok := middleware.ClaimsFromContext(ctx); ok {
    return s.listFor(ctx, claims.UserID) // personalized
}
return s.listPublic(ctx)                 // anonymous — the normal case here
```

Two ways to get this wrong. `middleware.GetUser(ctx)` returns a `CodeUnauthenticated` **error** (it does not panic), so calling it here 401s every anonymous caller and defeats the annotation. And `claims, _ := ClaimsFromContext(ctx)` followed by `claims.UserID` **nil-panics** on every anonymous call — check `ok`.

To personalize for callers who DO present a token, validate it in the handler, or keep the RPC authenticated and add a separate public one. Not `AnonymousOK: true`: that makes auth non-gating service-wide, so `auth_required: true` gates nothing.

## Forge authenticates; it does not authorize

**Forge ships no authorization.** `auth_required: true` answers "is this caller
signed in", never "may they touch THIS row" — the interceptor rejects callers
with no valid token and then treats every authenticated caller alike. There is
no annotation, config field or generated hook that expresses a policy; designing
and enforcing one is application code.

Two forge-specific facts worth knowing before you write it:

- **Generated CRUD delegations carry no policy.** They compile, pass their
  scaffolded tests, and read as finished, so an unenforced rpc looks identical
  to an enforced one. `db/crud-overrides` covers the seams for putting a policy
  on one.
- **`middleware.GetUser(ctx)` is where the principal comes from.** The
  interceptor already proved the caller has an identity; that call gets it.

## Making a principal an application principal: the `Enrich` seam

The token says who the caller is; your database says what they are. `enrichClaims(ctx, claims)` runs AFTER validation and BEFORE any handler sees the claims — the one chokepoint where an IdP identity becomes an application principal. Hydrate roles/org by `claims.UserID` (the IdP `sub`), never by email.

**`Enrich` can REJECT**, and it is the earliest place you can: a `connect.Error` keeps its code verbatim (`PermissionDenied` stays `PermissionDenied`), a plain error becomes `Unauthenticated`. Right for whole-principal facts (suspended, no local account); wrong for per-resource decisions, which need the row and belong in the handler.

**Request VALIDATION is not its job.** Field rules are enforced separately and earlier by protovalidate, from `buf.validate` annotations on your protos — a malformed request is `InvalidArgument` before any handler or enricher runs. Field checks here would run only on authenticated requests and report a bad email as an auth failure.

**Provisioning is yours too** — your IdP does not populate your `users` table. `auth/authorization` has the enricher recipe with its error codes, plus the public `Register` RPC that inserts on the IdP-verified `sub`, never a body field.

## API keys

Wired in every scaffold, disabled until you supply a key validator. A key arrives as `X-API-Key: fk_…` (`middleware.APIKeyHeader`) or `Authorization: Bearer fk_…`. The authenticator routes it by its `fk_` shape to the `auth.KeyValidator` that `internal/app/auth.go`'s `APIKeyValidator(infra)` returns, which `serve.go` installs right after `OpenInfra` so the lookup runs on the handlers' own pool. You own the table (hash + indexed prefix, never the key) and the lookup, over `forge/pkg/apikey`'s primitives. Recipe: `auth/api-keys`.

## Dev mode

Dev relaxes only NON-security ergonomics (permissive CORS, verbose errors) — **authentication is enforced in every mode**, and no environment variable turns it off. A local call to a protected RPC needs a real token: mint one against an HS256 `jwt_secret` in the env's secret store (`forge secret set --env dev JWT_SECRET`), or use the dev IdP, which `forge env up` brings up as a host process and converges (`identity` on the dev Bundle) for any project that declares a frontend. It is Zitadel, and its instance is DECLARED in `idp-steps.yaml` — `forge env up` reproduces it, credential included.

Load `auth/dev-loop` before debugging a local 401: it covers the traps that are not your wiring — the dev IdP answers to ONE hostname (`localhost`, which is why its dev loop is `forge env up`, not a containerized app), and an OIDC app registered with the default token type mints OPAQUE access tokens no JWKS can validate.

## Frontend wiring

**Sign-in is NATIVE, and the browser never contacts the identity provider.** The browser POSTs credentials to this app's own API (`POST /auth/login`) and receives an HttpOnly session cookie; the server runs the whole OIDC flow against the issuer (`internal/app/login_broker.go`, over `forge/pkg/devidp`). So there is no token in JavaScript, no redirect, and no PKCE in the bundle — the issuer values (`oidc_client_id`, `oidc_redirect_uri`, `oidc_scopes`) are read SERVER-side. React Native cannot use the cookie model and keeps the `AuthProvider` seam (`src/lib/auth/provider.ts`) instead. All of it is in `auth/frontend`.

## Testing auth

Inject claims: `middleware.ContextWithClaims(ctx, &middleware.Claims{UserID: "user-1", Role: "admin"})`, or `app.AuthedContext(t, testkit.WithUserID(...))`. Omit them to assert the `GetUser` 401 on a handler that requires identity — and that a PUBLIC handler still answers.

## Rules

- Authentication is **fail-closed** (`AnonymousOK: false`): no token, or an invalid one, is rejected before the handler. A nil validator makes the server **refuse to start**.
- **Whether this server authenticates is answerable FROM SOURCE** — `AuthDeps{AnonymousOK: false}` in the generated serve wiring plus the protos' `auth_required` declarations, both readable in a diff. No env var, no config field, and no runtime condition may decide it: an opt-out settable from a shell cannot be reviewed.
- `auth_required: false` on the rpc is the ONE way to publish an endpoint; it is enforced, not documentation. Never bypass auth by removing the interceptor.
- **A public handler gets NO claims, even from a caller who offered a token** — the allow-list is checked before any credential is. Handle both cases: `ClaimsFromContext` and check `ok`, never a blind deref.
- On an authenticated RPC, `middleware.GetUser` gives you the principal — not proof of authentication, which the interceptor already established.
- Custom claim DATA rides `enrichClaims`, not a parallel claim type. Hydrate by the IdP `sub`, never by email.
- **Input validation is protovalidate's job**, via `buf.validate` rules on the proto — not `enrichClaims`, which runs only on authenticated requests and reports failures as auth errors.
- **`jwt_audience` on Zitadel, always** — it scopes the roles mapping to your project (`auth/shared-issuer`).
- Pin the dev IdP's image to an exact version; a moving tag makes "login broke today" unattributable. Declare its instance (`idp-steps.yaml`) rather than clicking through a console — a setup step that must be RUN is one a teammate's clone will not have.
- **Forge validates tokens; it never issues them.** The scaffolded login broker does not mint credentials either — it brokers to the issuer, which is what makes an `/auth/login` route legitimate here. It stays an owned HTTP handler, not an RPC on a service. Provisioning is yours: a public `Register` RPC that trusts the verified `sub`, never a body field.
- This skill ends at identity. **Forge ships no authorization** — what a caller may do is yours to design and enforce, and nothing in the framework will flag its absence.
