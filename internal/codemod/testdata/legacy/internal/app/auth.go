// internal/app/auth.go — YOURS. forge scaffolds this file once and never
// rewrites it (there is no _gen twin and no DO-NOT-EDIT banner).
//
// Authentication is CODE, not configuration: you pick the validator here.
// WHERE this deployment's issuer lives is per-environment DATA, and it
// arrives on the typed config object like every other per-environment value
// in this app — declared in proto/config/v1/config.proto, pinned per env in
// deploy/kcl/<env>/config.k, with the secret coming from the env's secret
// provider. Nothing here reads the environment directly.
//
// This file decides how a presented credential is checked:
//
//   - SetupAuth — bearer tokens (JWTs). The generated cmd serve.go calls it
//     BEFORE anything is opened and threads the authenticator it returns into
//     the auth interceptor (pkg/middleware AuthDeps.Validate), so a server
//     that cannot authenticate refuses to start.
//   - APIKeyValidator — API keys. cmd serve.go calls it right AFTER
//     OpenInfra, because checking a key reads the database, and installs the
//     result on that same authenticator.
package app

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/auth"
	"github.com/reliant-labs/forge/pkg/oauth2"

	"example.com/app/pkg/config"
)

// discoveryTimeout bounds the one OIDC discovery request SetupAuth makes at
// boot when the JWKS URL is derived from jwt_issuer rather than configured.
const discoveryTimeout = 10 * time.Second

// SetupAuth returns the authenticator this service checks every request's
// credential with. It routes a credential by its shape: an API key ("fk_…")
// to the key validator APIKeyValidator supplies once infra is open, anything
// else to the bearer-token validator built here.
//
// Default: validate JWTs using the typed config fields —
//
//	cfg.JwtSigningMethod  expected alg — HS256, RS256, ES256 (default RS256)
//	cfg.JwtIssuer         token issuer (iss) to enforce
//	cfg.JwtAudience       audience (aud) to enforce
//	cfg.JwtJwksUrl        the issuer's JWKS endpoint — the standard OIDC path
//	cfg.JwtSecret         HS* shared secret, or a PEM public key (sensitive)
//	cfg.JwtRolesClaim     where Claims.Roles comes from (default: auto-detect,
//	                      including Zitadel's project-roles map)
//	cfg.JwtJwksSnapshot*  optional last-known JWKS, so a restart during an
//	                      issuer outage still starts (see below)
//
// Point jwt_jwks_url at any OIDC issuer and this validator does real RS256
// validation against its published keys, refreshed in the background. Set
// jwt_secret INSTEAD for a static key (HS* shared secret or a pinned PEM);
// setting both is rejected at startup, because a validator has one key
// source. To accept several issuers at once — an IdP migration — build them
// explicitly with auth.Config.TokenValidators.
//
// Leave jwt_jwks_url EMPTY and set only jwt_issuer and this function
// discovers the JWKS endpoint from the issuer's
// /.well-known/openid-configuration, so a standards-compliant IdP needs one
// URL rather than two that have to agree.
//
// JWKS SNAPSHOT (optional). The default refuses to start when the issuer is
// unreachable at boot. For an issuer shared by many apps that turns one outage
// into a crash-loop of every app that restarts during it. Deliver a snapshot of
// the issuer's PUBLIC keys (jwt_jwks_snapshot_file, or jwt_jwks_snapshot from
// the secret store) and an unreachable issuer at boot starts the server on it
// instead — tokens are still fully verified, the issuer is retried in the
// background, and live keys take over on the first success. A snapshot older
// than jwt_jwks_snapshot_max_age (7 days) is ignored, and with no usable keys
// at all the server still refuses to start. The `auth` skill has the rules.
//
// NOTHING HERE IS SPECIFIC TO ANY PROVIDER. There is no vendor name in this
// function and no default pointing at one: both branches take whatever
// issuer the config names. The IdP container docker-compose.yml offers
// behind an opt-in profile reaches this code the same way a hosted provider
// does — as a URL in jwt_issuer. Swapping IdPs is editing this one function
// and/or the per-env config, never patching a provider enum.
//
// Swap the validator freely — it is arbitrary code, not a fixed enum. Each
// constructor returns a *auth.Validator; wrap its .Validate method in
// auth.NewAuthenticator, as the last line below does:
//
//	// Clerk (JWKS + org/session claim shape):
//	validator, err := auth.Clerk(auth.ClerkOpts{
//	    JWKSURL: cfg.JwtJwksUrl,
//	    Issuer:  cfg.JwtIssuer, // recommended in production
//	})
//
//	// Firebase (Google JWKS, one or more accepted project IDs):
//	validator, err := auth.Firebase(auth.FirebaseOpts{
//	    ProjectIDs: strings.Split(cfg.FirebaseProjectIds, ","),
//	})
//
// A constructor that needs a value this config does not carry (Firebase's
// project IDs, above) gets a new field in proto/config/v1/config.proto —
// that is the one place per-deployment values are declared.
//
// API keys are not configured here — they are APIKeyValidator's, below.
// A service that takes API keys ONLY passes nil to auth.NewAuthenticator:
// every bearer token is then rejected.
//
// Refuse-to-start: a nil authenticator makes the server REFUSE TO START — a
// server with no authenticator is a bug. The default below always returns
// one, so a fresh project does real JWT auth in every environment
// out of the box, failing closed on every request until signing material
// (jwt_secret, or a reachable jwt_jwks_url) is configured. To exercise a
// protected endpoint locally, present a token this validator accepts: sign
// in through the issuer behind jwt_jwks_url, or set jwt_signing_method to
// HS256 with a jwt_secret in the env's secret provider and mint a
// short-lived token signed with it.
//
// WHETHER this server authenticates is NOT decided here and cannot be
// decided by configuration: the generated cmd serve.go builds
// middleware.AuthDeps{AnonymousOK: false} and the per-RPC allow-list comes
// from the protos' `auth_required` declarations. This function only decides
// HOW a presented token is checked. There is no env var, and no value of
// any field read below, that makes this server serve a protected RPC to an
// anonymous caller.
func SetupAuth(cfg *config.Config) (*auth.Authenticator, error) {
	jwksURL := cfg.JwtJwksUrl
	snapshot := auth.JWKSSnapshot{
		File:   cfg.JwtJwksSnapshotFile,
		Inline: cfg.JwtJwksSnapshot,
		MaxAge: cfg.JwtJwksSnapshotMaxAge.AsDuration(),
	}

	// Convenience, not magic: with an issuer and no explicit JWKS URL,
	// ask the issuer where its keys are (OIDC Discovery / RFC 8414) so a
	// standards-compliant IdP needs ONE URL instead of two that must agree.
	//
	// jwt_jwks_url WINS when set, and that ordering is load-bearing rather
	// than a preference. Discovery §4.3 requires a document's `issuer` to
	// equal the URL it was fetched from, so it cannot describe an issuer
	// reachable at a DIFFERENT hostname than the one in the tokens — which
	// is the normal case for a containerized dev IdP (tokens say
	// localhost, the server-to-server hop says the service name). Those
	// deployments set both fields explicitly and never reach this branch.
	var discover func(ctx context.Context) (string, error)
	if jwksURL == "" && cfg.JwtIssuer != "" && cfg.JwtSecret == "" {
		discover = func(ctx context.Context) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
			defer cancel()

			meta, err := oauth2.Discover(ctx, nil, cfg.JwtIssuer)
			if err != nil {
				return "", fmt.Errorf("discover OIDC issuer %q: %w", cfg.JwtIssuer, err)
			}
			// Discover guarantees the authorization and token endpoints, but
			// jwks_uri is optional in its validation — and an empty one here
			// would hand auth.NewValidator no key source at all, which builds
			// a validator that rejects every token. Name the gap instead.
			if meta.JWKSURI == "" {
				return "", fmt.Errorf("OIDC issuer %q publishes no jwks_uri in its discovery document: "+
					"set jwt_jwks_url explicitly, or jwt_secret for a static key", cfg.JwtIssuer)
			}
			return meta.JWKSURI, nil
		}
	}

	// REFUSE TO START when the issuer's keys cannot be learned and no usable
	// JWKS snapshot exists: a server that cannot learn its issuer's keys
	// cannot authenticate anyone, so booting anyway would mean 401ing every
	// request for a reason no log line explains. Do NOT soften this into a
	// warning — degrading to "accept nothing" silently converts a typo into a
	// total outage. A delivered, fresh snapshot is the one thing that lets
	// the server start without the issuer: it is real key material, not an
	// absence of it.
	validator, err := auth.NewValidator(auth.Config{
		Provider: "jwt",
		JWT: auth.JWTConfig{
			SigningMethod: cfg.JwtSigningMethod,
			Issuer:        cfg.JwtIssuer,
			Audience:      cfg.JwtAudience,
			JWKSURL:       jwksURL,
			JWKSDiscover:  discover,
			JWKSSnapshot:  snapshot,
			Secret:        cfg.JwtSecret,
			RolesClaim:    cfg.JwtRolesClaim,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build JWT validator: %w", err)
	}
	return auth.NewAuthenticator(validator.Validate), nil
}

// APIKeyValidator returns the validator API keys are checked with — a
// credential that starts with apikey.KeyPrefix ("fk_"), presented as
// "X-API-Key: fk_…" (middleware.APIKeyHeader) or "Authorization: Bearer
// fk_…". The generated cmd serve.go calls it right after OpenInfra and
// installs the result with authenticator.UseKeyValidator, so a key check
// runs on the pool the handlers serve from (infra.DB / infra.ORM), never on
// a second connection pool of its own, and with the request's context.
//
// nil — the default — leaves API keys DISABLED: every "fk_" credential is
// rejected and bearer tokens are unaffected. Disabled is not unchecked;
// there is no value this function can return that lets a key through
// unverified, and a key too short to be one never reaches your store.
//
// To enable keys, own a table that stores each key's SHA-256 HASH and an
// indexed lookup prefix — never the key — and return a lookup over it:
//
//	func APIKeyValidator(infra *Infra) auth.KeyValidator {
//		return auth.KeyValidatorFunc(func(ctx context.Context, key string) (*auth.Claims, error) {
//			prefix, err := apikey.LookupPrefix(key)
//			if err != nil {
//				return nil, err
//			}
//			var hash, userID string
//			err = infra.DB.QueryRowContext(ctx, `SELECT key_hash, user_id FROM api_keys
//				WHERE prefix = $1 AND revoked_at IS NULL
//				AND (expires_at IS NULL OR expires_at > now())`, prefix).Scan(&hash, &userID)
//			if err != nil {
//				return nil, err // sql.ErrNoRows included: an unknown key
//			}
//			if !apikey.Verify(key, hash) { // constant time
//				return nil, errors.New("key does not match")
//			}
//			return &auth.Claims{UserID: userID}, nil
//		})
//	}
//
// Return errors freely: the caller only ever reads "invalid API key", and
// your error is logged server-side as its cause. The table, issuing keys
// and revoking them: `forge skill load auth/api-keys`.
func APIKeyValidator(_ *Infra) auth.KeyValidator {
	return nil
}
