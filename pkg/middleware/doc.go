// Package middleware provides the commodity HTTP middlewares and
// Connect interceptors that every forge-generated service ships with.
//
// # Why a library
//
// These used to be scaffolded into each project's pkg/middleware as
// static files. Field evidence across downstream repos showed the
// copies stayed byte-identical to the templates — zero project intent,
// pure photocopies of security-relevant code that drifted and never
// received fixes. The code now lives here, versioned with forge, and
// the project keeps ONE thin pkg/middleware file wiring the things
// projects actually customize: the auth validator, the identity
// enricher, the unauthenticated allow-list, and dev-claims behaviour
// (see forge/pkg/authn for that policy surface).
//
// # What's here
//
// HTTP middlewares (outermost layer, wrap the whole mux):
//
//   - [CORSMiddleware] — spec-correct CORS with wildcard/credentials
//     guard rails.
//   - [SecurityHeadersMiddleware] — OWASP security response headers
//     (CSP, nosniff, Referrer-Policy, Permissions-Policy, HSTS).
//   - [RequestIDMiddleware] — per-request correlation ID, trusted from
//     the inbound X-Request-Id or minted fresh; shares pkg/observe's
//     context key so Connect log records inherit the ID.
//   - [HTTPStack] — recovery + logging + audit for plain HTTP routes
//     (webhooks, OAuth callbacks, REST) that bypass the Connect chain.
//   - [HTTPAuth] — Bearer-token auth for plain HTTP routes.
//   - [IdempotencyMiddleware] — HTTP-level Idempotency-Key replay.
//
// Connect interceptors (project chain, after serverkit's canonical
// observe.DefaultMiddlewares):
//
//   - [AuditInterceptor] — one audit log record per RPC (who, what,
//     when, result, duration), routed via log_type=audit.
//   - [RateLimitInterceptor] — per-caller token-bucket rate limiting
//     keyed by claim subject (falling back to peer IP).
//   - [IdempotencyInterceptor] — RPC-level Idempotency-Key replay.
//
// Plus [Redact], a struct→map helper for logging payloads without
// leaking PII.
//
// # Claims access
//
// Several middlewares read the authenticated principal. The claims
// CONTEXT KEY is owned by the project (its pkg/middleware), so anything
// claims-aware takes the project's ClaimsFromContext as a callback.
// Passing nil degrades gracefully (anonymous audit entries, IP-keyed
// rate limits).
//
// The authentication MECHANISM lives in forge/pkg/authn. Access-control
// decisions and row scoping stay handler logic the project writes (its own
// ClaimsFromContext helpers + WHERE clauses).
package middleware
