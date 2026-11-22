## review:passed

Lane: **HIGH** (Sol pass + second pass in this run).

HIGH: auth (internal/auth/authenticator.go, internal/auth/authmetrics/authmetrics.go, …); prod-deploy (deploy/kcl/config_gen.k); secrets (internal/auth/authtest/authtest.go); tenant-isolation (internal/auth/middleware.go)

Reviewed every changed hunk and the round-4 expiry changes. All earlier blockers are fixed; no new blocker found. The local expiry guard closes the pinned forge verifier's missing-exp gap without requiring a newer forge release. TestTokenChainNeverConfusesItsIssuers covers valid credentials and missing, zero, malformed and expired identity expiration claims, plus missing/zero Supabase expiration. HIGH second pass: issuer routing and credential classification fail closed; cross-issuer/audience denial tests cover identity isolation; no new secret disclosure, billing or migration changes found. Configuration additions remain optional and legacy authentication remains available. RC-02, RC-04, RC-08, RC-10 and RC-11 checked; no scope, dependency-pin or retained-resource changes. Tests were inspected, not executed.

**Second pass:** Expiry and post-deploy-plan blockers remain fixed. Supplied additional configuration confirms legacy JWKS and shared defaults, but does not show the final production workload assembly needed to establish that JWT_ISSUER is unset. That earlier coverage gap remains; no new defect found. All supplied changed hunks read. Tests inspected, not executed; no sibling test coverage relied upon.

Tests covering the risk: 
- Authentication: expiration, issuer routing, algorithm confusion and ordinary valid credentials.: `internal/auth/chain_test.go:TestTokenChainNeverConfusesItsIssuers`
- Production defaults: identity validation remains disabled without its issuer; existing Supabase sessions remain accepted.: `internal/auth/chain_test.go:TestTokenChainIsInertWithoutAnIdentity`
- Key handling: unpublished keys, substituted keys, public-key/HMAC confusion and unsigned tokens are denied.: `internal/auth/chain_test.go:TestTokenChainNeverConfusesItsIssuers`
- Project identity isolation and software credentials cannot decide approvals.: `internal/grpc/interceptors/auth_identity_test.go:TestAuthInterceptorAcceptsTheManagedIdentityAsSoftware`

Migration safety: **not_applicable**

**Earlier blockers, at this head:**
- R1.B1 fixed — Current PR body includes Post-merge check and Rollback sections covering authentication traffic, approval and CLI smoke checks, and recovery.
- R2.B1 fixed — Current PR body includes the previously missing production verification and rollback procedures.
- R3.B1 fixed — internal/auth/chain.go:217-223 rejects absent, zero and non-numeric exp; chain_test.go adds denial cases. Pinned forge:pkg/auth/auth.go:546-565@7c571e1f7b46 enforces present expiration with 5s leeway.
- R1.B1 fixed — Current PR body includes Post-merge check and Rollback sections covering authentication checks and release recovery.
- R2.B1 fixed — Current PR body includes production verification and rollback procedures.
- R3.B1 fixed — internal/auth/chain.go:219@835b15590b94 rejects missing/nonpositive expiry; chain_test.go:206 adds denial cases. reliant-labs/forge:pkg/auth/auth.go:545@7c571e1f7b46 validates present expiration.

**Generated, reviewed through their inputs (not line by line):** `cmd/reliant/cmd/roots_gen.go`, `frontends/web/src/gen/reliant/config/v1/api_server_config_pb.ts`, `gen/reliant/config/v1/api_server_config.pb.go`, `generated/docs-source/reference/cli.md`, `pkg/config/roots_gen.go`

**Not verified (non-blocking):**
- high_risk: Production identity configuration was not independently confirmed: control-plane:deploy/kcl/prod/main.k@main was not supplied in a readable window. The PR's assertion that JWT_ISSUER is unset there remains unverified.

**Not verified by the second pass, no defect suspected (non-blocking):** observation gaps (only watching it run would settle them), and coverage gaps it raised again after a first pass that passed; each names no defect, so it does not block (control-plane #2479). Check them before shipping.
- Production identity configuration was not independently confirmed. (coverage)

**Post-merge verification** (only a live environment shows these; not review blockers, each verified by the plan named):
- Production authentication compatibility and credential telemetry after release. — Inspect reliant_auth_credentials_total and RPC 401 rates using forge obs endpoints prod; sign in, decide a disposable approval, and make an rlat_ CLI call. Repeat sign-in if Supabase issuer/audience configuration is enabled.; rollback: Use the release pipeline to restore the previous release. If configured Supabase issuer/audience checks misfire, unset those optional values.
- Production authentication compatibility and credential-source metrics after release. — PR body: check Supabase traffic and absence of identity traffic, inspect forge obs endpoints prod for rising RPC 401s, sign in and decide a disposable approval, and run reliant chat list.; rollback: PR body: restore the previous release through the release pipeline; unset Supabase issuer/audience settings if their later activation causes failures.

Passed: entering the native merge queue at this exact head.

Reviewed `835b15590b94534cd5d0aae68c020b7a7b2431a2`.
<!-- review-passed sha=835b15590b94534cd5d0aae68c020b7a7b2431a2 -->