// Package auth provides JWT and API-key authentication for forge-generated
// Connect RPC services.
//
// The library exposes a [Validator] that authenticates incoming Connect
// requests against a configured provider ("jwt", "api_key", "both", or
// "none") and a Connect interceptor that attaches the resulting [Claims] to
// the request context.
//
// Generated forge projects import this package via a thin shim in
// pkg/middleware/auth_gen.go. The shim wires the project's [Config] from
// forge.yaml and exposes a project-local Claims alias (type Claims = auth.Claims).
package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
)

// Provider names recognized by [Config].
const (
	ProviderNone   = "none"
	ProviderJWT    = "jwt"
	ProviderAPIKey = "api_key"
	ProviderBoth   = "both"
)

// Claims is the canonical claims shape produced by all forge auth flows.
//
// The struct mirrors the per-project pkg/middleware/Claims that earlier
// forge versions generated. The generated shim re-exports it via
// type Claims = auth.Claims so existing project code (referring to
// middleware.Claims) keeps compiling.
//
// Raw carries the full decoded JWT payload (or any provider-specific map a
// [UserResolver] chooses to attach). Projects that need access to claims
// outside the fixed fields — Supabase user_metadata, Auth0 app_metadata,
// Clerk org_permissions, etc. — should read from Raw instead of forking
// the Claims type.
type Claims struct {
	UserID string         `json:"user_id"`
	Email  string         `json:"email"`
	OrgID  string         `json:"org_id"`
	Role   string         `json:"role"`
	Roles  []string       `json:"roles"`
	Raw    map[string]any `json:"raw,omitempty"`
}

// UserResolver is an optional hook that translates a raw decoded JWT payload
// into [Claims]. Projects implement it when they need to consume
// provider-specific claim shapes (e.g. Supabase user_metadata, Auth0
// app_metadata, Clerk org_role/org_permissions) without forking the Claims
// type.
//
// The resolver is wired via [Config.UserResolver]; when nil the validator
// falls back to the built-in shape extraction (sub, email, org_id, role,
// roles) and still populates Claims.Raw with the full payload.
type UserResolver interface {
	// Resolve receives the full decoded JWT payload (as a map, the
	// shape jwt-go hands us) and must return a populated [Claims]. The
	// raw payload should typically be copied onto Claims.Raw so
	// downstream callers can still inspect provider-specific fields.
	Resolve(rawClaims map[string]any) (*Claims, error)
}

// KeyValidator validates an API key and returns the associated claims.
// Implementations typically look up the key in a database or cache.
type KeyValidator interface {
	ValidateKey(ctx context.Context, key string) (*Claims, error)
}

// Config configures a [Validator].
//
// Provider selects the authentication scheme. JWT, APIKey and SkipMethods
// further customize each scheme. Every value is supplied by the caller —
// this package reads no environment variable of its own, so the key a
// validator trusts is always the key its constructor was handed.
type Config struct {
	// Provider is one of "jwt", "api_key", "both", "none". Required.
	Provider string

	// JWT configures JWT validation. Used when Provider is "jwt" or "both".
	JWT JWTConfig

	// APIKey configures API key validation. Used when Provider is
	// "api_key" or "both". A [KeyValidator] must be supplied separately
	// via [Validator.SetKeyValidator] or [Config.KeyValidator].
	APIKey APIKeyConfig

	// SkipMethods is the list of fully-qualified Connect procedure names
	// that bypass auth (in addition to the built-in gRPC health-check skip).
	SkipMethods []string

	// KeyValidator validates API keys. Required when APIKey auth is enabled.
	KeyValidator KeyValidator

	// TokenValidators, when non-empty, replaces the built-in single-secret
	// JWT path with an ordered fallback chain. Each entry validates the
	// bearer token independently; the first to accept wins. Use this when
	// a service must accept tokens from more than one issuer — typically
	// during an auth-provider migration (e.g. Supabase HMAC tokens
	// alongside Auth0 JWKS tokens).
	//
	// When empty the legacy JWT (single secret / JWKSURL) path is used,
	// preserving backwards compatibility for projects that only need one
	// validator.
	TokenValidators []TokenValidator

	// UserResolver, when non-nil, projects the raw decoded JWT payload onto
	// [Claims]. Use it when provider-specific claim shapes (Supabase
	// user_metadata, Auth0 app_metadata, Clerk org_permissions, etc.) need
	// to drive the Claims you hand to downstream code.
	//
	// When nil the validator uses built-in shape extraction (sub, email,
	// org_id, role, roles) and still attaches the full payload to
	// Claims.Raw so downstream code can inspect it.
	UserResolver UserResolver
}

// JWTConfig holds JWT-specific settings.
type JWTConfig struct {
	// SigningMethod is the expected JWT alg value (e.g. "HS256", "RS256").
	// Defaults to "RS256" when empty.
	SigningMethod string

	// Issuer, when set, is enforced via jwt.WithIssuer.
	Issuer string

	// Audience, when set, is enforced via jwt.WithAudience.
	Audience string

	// JWKSURL, when set, is the issuer's JWKS endpoint. [NewValidator]
	// fetches it ONCE at construction (so an unreachable or malformed
	// endpoint fails at server boot, not on the first request) and refreshes
	// it in the background for the life of the process — the same
	// [JWKSKeyfunc] machinery [Clerk] and [Firebase] use. This is the path
	// every standard OIDC issuer (Auth0, Keycloak, Zitadel, Supabase, Okta)
	// takes.
	//
	// Mutually exclusive with Secret: two key sources for one validator is a
	// configuration error, and [NewValidator] rejects it rather than picking
	// a winner. A service that must genuinely accept tokens from more than
	// one issuer composes them explicitly via [Config.TokenValidators].
	JWKSURL string

	// JWKSDiscover, when set and JWKSURL is empty, yields the JWKS URL by
	// asking the issuer (OIDC discovery). It runs on EVERY live attempt, so
	// with a [JWKSSnapshot] an issuer that is down at boot — discovery
	// included — still lets the validator start. Same exclusivity as JWKSURL.
	JWKSDiscover func(ctx context.Context) (string, error)

	// JWKSSnapshot is the optional offline fallback key source. With it,
	// an issuer that is unreachable at boot no longer prevents startup when a
	// fresh snapshot exists; see [JWKSSnapshot] for exactly what is trusted.
	// Requires JWKSURL or JWKSDiscover.
	JWKSSnapshot JWKSSnapshot

	// Secret is the symmetric secret (HS*) or PEM-encoded public key
	// (RS*/ES*). Mutually exclusive with JWKSURL.
	Secret string

	// KeyFunc, when non-nil, fully overrides key resolution. Useful for tests
	// and for callers wiring custom JWKS clients.
	KeyFunc jwt.Keyfunc

	// RolesClaim names the claim [Claims.Roles] is read from. Empty or "auto"
	// reads the flat `roles` array and, when the token has none, Zitadel's
	// `urn:zitadel:iam:org:project:<audience>:roles` map (every project's, when
	// Audience is unset). Set it to read exactly one claim: a flat array, a
	// space or comma separated string, or a Zitadel-shaped role map.
	RolesClaim string
}

// EffectiveSigningMethod returns SigningMethod or the "RS256" default.
func (j JWTConfig) EffectiveSigningMethod() string {
	if j.SigningMethod == "" {
		return "RS256"
	}
	return j.SigningMethod
}

// APIKeyConfig holds API-key-specific settings.
type APIKeyConfig struct {
	// Header is the HTTP header name carrying the API key.
	// Defaults to "X-API-Key" when empty.
	Header string
}

// EffectiveHeader returns Header or the "X-API-Key" default.
func (a APIKeyConfig) EffectiveHeader() string {
	if a.Header == "" {
		return "X-API-Key"
	}
	return a.Header
}

// InterceptorOptions configures Validator.Interceptor behaviour beyond the
// fields already on [Config].
type InterceptorOptions struct {
	// SkipMethods overrides Config.SkipMethods when non-nil.
	SkipMethods []string
}

// Validator authenticates Connect RPC requests against a configured provider.
// Construct with [NewValidator]; reuse a single Validator per service.
type Validator struct {
	cfg          Config
	skipMethods  map[string]bool
	keyValidator KeyValidator
	// chain, when non-nil, is the ordered TokenValidator fallback used in
	// place of the legacy single-secret JWT path. Built once at NewValidator
	// time so per-request authentication is allocation-free.
	chain TokenValidator
	// jwtKeyFunc is the resolved single-issuer key source, built ONCE at
	// NewValidator time. Non-nil when the caller supplied JWT.KeyFunc or a
	// JWT.JWKSURL; nil when the only key material is the static JWT.Secret
	// (resolved per request, since it needs the token's alg).
	jwtKeyFunc jwt.Keyfunc
	// stop ends the JWKS refresh and snapshot-retry goroutines; nil when the
	// validator started none.
	stop func()
}

// NewValidator returns a Validator wired for cfg.Provider.
//
// It returns an error when Provider is unrecognized. The "none" provider is
// allowed and produces a Validator whose Interceptor is a no-op (useful for
// tests and for projects that haven't enabled auth yet).
func NewValidator(cfg Config) (*Validator, error) {
	switch cfg.Provider {
	case ProviderNone, ProviderJWT, ProviderAPIKey, ProviderBoth, "":
	default:
		return nil, fmt.Errorf("auth: unknown provider %q", cfg.Provider)
	}

	v := &Validator{
		cfg:          cfg,
		keyValidator: cfg.KeyValidator,
		skipMethods:  make(map[string]bool, len(cfg.SkipMethods)),
	}
	for _, m := range cfg.SkipMethods {
		v.skipMethods[m] = true
	}

	// Build the TokenValidator chain when configured. A single-entry
	// TokenValidators slice still goes through MultiValidator so the
	// dispatch path is uniform; the wrapper is effectively free (one
	// extra indirection per token).
	if len(cfg.TokenValidators) > 0 {
		chain, err := NewMultiValidator(cfg.TokenValidators...)
		if err != nil {
			return nil, fmt.Errorf("auth: build validator chain: %w", err)
		}
		v.chain = chain
		return v, nil
	}

	// Single-issuer key resolution, decided ONCE here rather than per
	// request. An explicit KeyFunc wins; otherwise a JWKSURL is fetched now
	// so a typo'd or unreachable endpoint is a boot failure with the URL in
	// the message, never N confusing 401s at runtime.
	switch {
	case cfg.JWT.KeyFunc != nil:
		v.jwtKeyFunc = cfg.JWT.KeyFunc
	case cfg.JWT.JWKSURL != "" || cfg.JWT.JWKSDiscover != nil:
		if cfg.JWT.Secret != "" {
			return nil, fmt.Errorf("auth: JWT.JWKSURL and JWT.Secret are both set — a validator has ONE key source; drop one, or compose several issuers explicitly via Config.TokenValidators")
		}
		kf, stop, err := newJWKSKeyfunc(cfg.JWT)
		if err != nil {
			return nil, err
		}
		v.jwtKeyFunc, v.stop = kf, stop
	case cfg.JWT.JWKSSnapshot.configured():
		return nil, fmt.Errorf("auth: JWT.JWKSSnapshot is set without JWT.JWKSURL (or JWKSDiscover) — a snapshot is the fallback for a live JWKS source, not a key source of its own")
	}
	return v, nil
}

// Provider returns the configured provider name.
func (v *Validator) Provider() string { return v.cfg.Provider }

// SetKeyValidator replaces the configured [KeyValidator]. Useful when the
// validator is constructed before the storage backend is ready.
func (v *Validator) SetKeyValidator(kv KeyValidator) { v.keyValidator = kv }

// Close stops the background JWKS refresh (and, with a snapshot, the live
// retry) a JWKS-backed Validator started.
func (v *Validator) Close() error {
	if v.stop != nil {
		v.stop()
	}
	return nil
}

// Validate authenticates a single bearer JWT and returns the parsed claims.
// Useful outside the interceptor (e.g. webhook auth, CLI tooling).
func (v *Validator) Validate(token string) (*Claims, error) {
	return v.validateJWT(token)
}

// healthCheckPrefix is the gRPC health-checking service path. Only its RPCs
// (Check, Watch) skip authentication by default, matched on the exact service
// path — never a substring — so an application RPC such as
// GetPatientHealthRecord is never accidentally exempt.
const healthCheckPrefix = "/grpc.health.v1.Health/"

// IsUnauthenticatedProcedure reports whether procedure should bypass auth.
// This includes the built-in gRPC health-check skip plus any explicit skip
// list.
func (v *Validator) IsUnauthenticatedProcedure(procedure string, skipMethods []string) bool {
	if strings.HasPrefix(procedure, healthCheckPrefix) {
		return true
	}
	if len(skipMethods) > 0 {
		for _, m := range skipMethods {
			if m == procedure {
				return true
			}
		}
		return false
	}
	return v.skipMethods[procedure]
}

// AuthenticateHeaders authenticates a request given its headers and returns
// the parsed claims (or an error). It is the testable core of [Validator.Interceptor]
// — the interceptor wraps this with the procedure-skip logic and context plumbing.
func (v *Validator) AuthenticateHeaders(ctx context.Context, headers http.Header, opts InterceptorOptions) (*Claims, error) {
	provider := v.cfg.Provider
	if provider == "" {
		provider = ProviderNone
	}
	if provider == ProviderNone {
		return nil, nil
	}

	switch provider {
	case ProviderJWT:
		return v.authenticateJWT(headers)
	case ProviderAPIKey:
		return v.authenticateAPIKey(ctx, headers)
	case ProviderBoth:
		c, err := v.authenticateJWT(headers)
		if err == nil {
			return c, nil
		}
		c2, kerr := v.authenticateAPIKey(ctx, headers)
		if kerr == nil {
			return c2, nil
		}
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication failed: provide a valid Bearer token or API key"))
	}
	return nil, nil
}

// Interceptor returns a unary-only Connect interceptor that authenticates
// each request and stores the resulting claims in the context using the
// supplied claims-context helper.
//
// withClaims is the function the user's pkg/middleware exposes for putting
// claims into context (typically middleware.ContextWithClaims).
//
// This entrypoint is kept for backwards compatibility with projects that
// wired auth as a [connect.UnaryInterceptorFunc]. New code should prefer
// [Validator.ConnectInterceptor], which also covers streaming RPCs.
func (v *Validator) Interceptor(opts InterceptorOptions, withClaims func(context.Context, *Claims) context.Context) connect.UnaryInterceptorFunc {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if v.IsUnauthenticatedProcedure(req.Spec().Procedure, opts.SkipMethods) {
				return next(ctx, req)
			}
			claims, err := v.AuthenticateHeaders(ctx, req.Header(), opts)
			if err != nil {
				return nil, err
			}
			if claims != nil && withClaims != nil {
				ctx = withClaims(ctx, claims)
			}
			return next(ctx, req)
		})
	})
}

// ConnectInterceptor returns a full [connect.Interceptor] (unary + streaming
// handler) that authenticates each request and stores the resulting claims
// on ctx via withClaims.
//
// Use this in preference to [Validator.Interceptor] for any new wiring —
// connect-go silently bypasses [connect.UnaryInterceptorFunc] for streaming
// RPCs, which would otherwise leave streaming endpoints unauthenticated.
//
// Streaming clients are pass-through: the auth interceptor is server-side.
func (v *Validator) ConnectInterceptor(opts InterceptorOptions, withClaims func(context.Context, *Claims) context.Context) connect.Interceptor {
	return &interceptor{v: v, opts: opts, withClaims: withClaims}
}

// interceptor is the full unary+streaming implementation backing
// [Validator.ConnectInterceptor].
type interceptor struct {
	v          *Validator
	opts       InterceptorOptions
	withClaims func(context.Context, *Claims) context.Context
}

func (i *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return connect.UnaryFunc(func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if i.v.IsUnauthenticatedProcedure(req.Spec().Procedure, i.opts.SkipMethods) {
			return next(ctx, req)
		}
		claims, err := i.v.AuthenticateHeaders(ctx, req.Header(), i.opts)
		if err != nil {
			return nil, err
		}
		if claims != nil && i.withClaims != nil {
			ctx = i.withClaims(ctx, claims)
		}
		return next(ctx, req)
	})
}

// WrapStreamingClient is a pass-through. The auth interceptor is server-side:
// client-side credentials are attached by the caller before the request
// leaves the process, not by this interceptor.
func (i *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return connect.StreamingHandlerFunc(func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if i.v.IsUnauthenticatedProcedure(conn.Spec().Procedure, i.opts.SkipMethods) {
			return next(ctx, conn)
		}
		claims, err := i.v.AuthenticateHeaders(ctx, conn.RequestHeader(), i.opts)
		if err != nil {
			return err
		}
		if claims != nil && i.withClaims != nil {
			ctx = i.withClaims(ctx, claims)
		}
		return next(ctx, conn)
	})
}

// authenticateJWT extracts and validates a JWT from the Authorization header.
func (v *Validator) authenticateJWT(headers http.Header) (*Claims, error) {
	authorization := headers.Get("Authorization")
	if authorization == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("missing authorization header"))
	}
	token := strings.TrimPrefix(authorization, "Bearer ")
	if token == authorization {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid authorization format: expected Bearer token"))
	}
	claims, err := v.validateJWT(token)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid token: %w", err))
	}
	return claims, nil
}

func (v *Validator) authenticateAPIKey(ctx context.Context, headers http.Header) (*Claims, error) {
	header := v.cfg.APIKey.EffectiveHeader()
	apiKey := headers.Get(header)
	if apiKey == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("missing API key header (%s)", header))
	}
	if v.keyValidator == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("API key validator not configured"))
	}
	claims, err := v.keyValidator.ValidateKey(ctx, apiKey)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid API key: %w", err))
	}
	return claims, nil
}

func (v *Validator) validateJWT(tokenString string) (*Claims, error) {
	// Prefer the configured TokenValidator chain when present — this is
	// the path projects opt into for multi-issuer setups.
	if v.chain != nil {
		return v.chain.ValidateToken(tokenString)
	}

	signingMethod := v.cfg.JWT.EffectiveSigningMethod()

	// jwtKeyFunc is the construction-time resolution (explicit KeyFunc or a
	// live JWKS client). Only the static-secret case is left to decide here,
	// because decoding it needs the configured alg.
	keyFunc := v.jwtKeyFunc
	if keyFunc == nil {
		keyFunc = func(token *jwt.Token) (interface{}, error) {
			if token.Method.Alg() != signingMethod {
				return nil, fmt.Errorf("unexpected signing method: %s", token.Method.Alg())
			}
			if v.cfg.JWT.Secret == "" {
				return nil, fmt.Errorf("no JWT signing material configured: set JWT.Secret (an HS* shared secret or a PEM public key) or JWT.JWKSURL")
			}
			return decodeJWTKey(signingMethod, v.cfg.JWT.Secret)
		}
	}

	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{signingMethod}),
		jwt.WithLeeway(5 * time.Second),
	}
	if v.cfg.JWT.Issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(v.cfg.JWT.Issuer))
	}
	if v.cfg.JWT.Audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(v.cfg.JWT.Audience))
	}

	token, err := jwt.Parse(tokenString, keyFunc, parserOpts...)
	if err != nil {
		return nil, fmt.Errorf("token validation failed: %w", err)
	}

	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("unexpected claims type")
	}

	return projectClaims(mapClaims, v.cfg.UserResolver, claimOpts{rolesClaim: v.cfg.JWT.RolesClaim, audience: v.cfg.JWT.Audience}), nil
}

// projectClaims turns a decoded JWT payload into [Claims]. When resolver is
// non-nil it delegates the projection; otherwise it uses the built-in
// extraction (sub/email/org_id/role/roles) and stashes the raw payload onto
// Claims.Raw for callers that need provider-specific fields.
//
// A resolver that returns an error or nil claims is treated as a fallback
// signal: projectClaims falls back to the built-in shape so a stricter
// resolver can opt out for tokens it doesn't recognize.
func projectClaims(mc map[string]any, resolver UserResolver, opts claimOpts) *Claims {
	if resolver != nil {
		c, err := resolver.Resolve(mc)
		if err == nil && c != nil {
			return c
		}
	}
	return defaultProjectClaims(mc, opts)
}

// defaultProjectClaims is the built-in extraction. Roles come from
// [extractRoles]; when they were derived from a role map (Zitadel) rather
// than a flat array and the token names no `role` of its own, exactly one
// role also fills [Claims.Role]. With several roles Role stays empty: the
// token does not say which one is "the" role, and picking one by sort order
// would be a guess that grants or denies access.
func defaultProjectClaims(mc map[string]any, opts claimOpts) *Claims {
	mapClaims := jwt.MapClaims(mc)
	c := &Claims{
		UserID: getStringClaim(mapClaims, "sub"),
		Email:  getStringClaim(mapClaims, "email"),
		OrgID:  getStringClaim(mapClaims, "org_id"),
		Role:   getStringClaim(mapClaims, "role"),
		Raw:    mc,
	}
	var derived bool
	c.Roles, derived = extractRoles(mc, opts)
	if derived && c.Role == "" && len(c.Roles) == 1 {
		c.Role = c.Roles[0]
	}
	return c
}

// decodeJWTKey converts a secret string into the type that github.com/golang-jwt/jwt/v5
// expects for the configured signing method.
func decodeJWTKey(signingMethod, secret string) (interface{}, error) {
	switch {
	case strings.HasPrefix(signingMethod, "HS"):
		return []byte(secret), nil
	case strings.HasPrefix(signingMethod, "RS") || strings.HasPrefix(signingMethod, "PS"):
		key, err := jwt.ParseRSAPublicKeyFromPEM([]byte(secret))
		if err == nil {
			return key, nil
		}
		// Fall back to detecting raw PEM with no decoded blocks.
		if block, _ := pem.Decode([]byte(secret)); block == nil {
			return nil, fmt.Errorf("RSA public key is not PEM-encoded")
		}
		return nil, err
	case strings.HasPrefix(signingMethod, "ES"):
		return jwt.ParseECPublicKeyFromPEM([]byte(secret))
	}
	return []byte(secret), nil
}

// Compile-time assertions that the parsed key types are usable by jwt-go.
var (
	_ *rsa.PublicKey   = (*rsa.PublicKey)(nil)
	_ *ecdsa.PublicKey = (*ecdsa.PublicKey)(nil)
)

// getStringClaim safely extracts a string claim from JWT map claims.
func getStringClaim(claims jwt.MapClaims, key string) string {
	if val, ok := claims[key]; ok {
		if s, ok := val.(string); ok {
			return s
		}
	}
	return ""
}

// getStringSliceClaim safely extracts a string slice claim from JWT map claims.
func getStringSliceClaim(claims jwt.MapClaims, key string) []string {
	val, ok := claims[key]
	if !ok {
		return nil
	}
	switch v := val.(type) {
	case []interface{}:
		var result []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			}
		}
		return result
	case []string:
		return v
	default:
		return nil
	}
}
