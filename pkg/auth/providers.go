package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// This file turns the hosted-IdP flows that used to ship as forge PACKS
// (clerk, firebase-auth) into first-class, versioned validators. Both
// providers are just JWKS-backed RS256 issuers with different CLAIM SHAPES;
// the only real value the packs carried was (a) building an auto-refreshing
// JWKS keyfunc and (b) mapping the provider's claim names onto [Claims].
// Both now live here so swapping providers is a one-line edit in the owned
// internal/app/auth.go SetupAuth (e.g. auth.Clerk(auth.ClerkOpts{...})).

// JWKSKeyfunc builds an auto-refreshing [jwt.Keyfunc] backed by one or more
// JWKS endpoints. Signing keys are fetched once at construction (so a
// misconfigured or unreachable endpoint fails fast, at server boot) and then
// refreshed in the background for the life of the process.
//
// The fail-fast half is what NoErrorReturnFirstHTTPReq buys: keyfunc's
// NewDefault DEFAULTS to swallowing the first fetch error and logging it,
// which turns a typo'd JWKS URL into an endless stream of per-request 401s
// nobody can attribute. A server that cannot reach its issuer's keys cannot
// authenticate anyone, so refusing to boot is both louder and no less
// available.
//
// It is the seam [Clerk] and [Firebase] use internally; call it directly when
// wiring a custom JWKS issuer through [NewJWKSValidator].
func JWKSKeyfunc(urls ...string) (jwt.Keyfunc, error) {
	return jwksKeyfuncCtx(context.Background(), 0, urls...)
}

// jwksKeyfuncCtx is [JWKSKeyfunc] with the refresh goroutine bound to ctx and
// an optional bound on each fetch (0 keeps keyfunc's one-minute default).
func jwksKeyfuncCtx(ctx context.Context, fetchTimeout time.Duration, urls ...string) (jwt.Keyfunc, error) {
	if len(urls) == 0 {
		return nil, fmt.Errorf("auth: JWKSKeyfunc requires at least one JWKS URL")
	}
	failFast := false
	kf, err := keyfunc.NewDefaultOverrideCtx(ctx, urls, keyfunc.Override{
		NoErrorReturnFirstHTTPReq: &failFast,
		HTTPTimeout:               fetchTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("auth: fetch JWKS from %v: %w", urls, err)
	}
	return kf.Keyfunc, nil
}

// ClerkOpts configures a [Clerk] validator.
type ClerkOpts struct {
	// JWKSURL is the Clerk instance's JWKS endpoint, e.g.
	// https://<your-subdomain>.clerk.accounts.dev/.well-known/jwks.json
	// (typically read from CLERK_JWKS_URL). Required unless KeyFunc is set.
	JWKSURL string

	// Issuer, when set, is enforced (iss). Clerk's issuer is the instance's
	// Frontend API URL, e.g. https://<your-subdomain>.clerk.accounts.dev.
	// Strongly recommended in production so a token minted for a different
	// Clerk instance is rejected.
	Issuer string

	// Audience, when set, is enforced (aud).
	Audience string

	// KeyFunc overrides JWKS fetching. Set it to inject a pre-built keyfunc
	// (tests, or a custom JWKS client); when nil the validator builds one
	// from JWKSURL via [JWKSKeyfunc].
	KeyFunc jwt.Keyfunc
}

// Clerk returns a [Validator] for Clerk session tokens. Clerk issues RS256
// JWTs whose signing keys come from a per-instance JWKS endpoint; the claim
// shape (sub / org_id / org_role / org_permissions) is mapped onto [Claims]
// by [clerkResolver].
//
//	// in SetupAuth(cfg *config.Config) — the values are typed config
//	// fields, declared in proto/<project>/config/v1/config.proto and pinned per
//	// environment, never read out of the process environment here.
//	validator, err := auth.Clerk(auth.ClerkOpts{
//	    JWKSURL: cfg.JwtJwksUrl,
//	    Issuer:  cfg.JwtIssuer, // optional but recommended
//	})
//	return validator.Validate, nil
func Clerk(opts ClerkOpts) (*Validator, error) {
	kf := opts.KeyFunc
	if kf == nil {
		if opts.JWKSURL == "" {
			return nil, fmt.Errorf("auth: Clerk requires JWKSURL (or KeyFunc)")
		}
		var err error
		kf, err = JWKSKeyfunc(opts.JWKSURL)
		if err != nil {
			return nil, err
		}
	}
	jv, err := NewJWKSValidator(JWKSValidatorConfig{
		SigningMethod: "RS256",
		KeyFunc:       kf,
		Issuer:        opts.Issuer,
		Audience:      opts.Audience,
		Resolver:      clerkResolver{},
	})
	if err != nil {
		return nil, fmt.Errorf("auth: build Clerk validator: %w", err)
	}
	return NewValidator(Config{Provider: ProviderJWT, TokenValidators: []TokenValidator{jv}})
}

// clerkResolver maps Clerk session-token claims onto [Claims].
//
// Clerk tokens carry:
//   - sub: Clerk user ID (e.g. "user_2x...")
//   - org_id: active organization ID (when an org is selected)
//   - org_role: organization role (e.g. "org:admin")
//   - org_permissions: organization permission slugs
//
// Email is NOT in the token by default — sync it via a Clerk webhook into
// your user entity. org_role is also appended to Roles so it arrives
// alongside the permission slugs.
type clerkResolver struct{}

func (clerkResolver) Resolve(mc map[string]any) (*Claims, error) {
	m := jwt.MapClaims(mc)
	c := &Claims{
		UserID: getStringClaim(m, "sub"),
		Email:  getStringClaim(m, "email"), // present only in custom JWT templates
		OrgID:  getStringClaim(m, "org_id"),
		Role:   getStringClaim(m, "org_role"),
		Roles:  getStringSliceClaim(m, "org_permissions"),
		Raw:    mc,
	}
	if c.Role != "" {
		c.Roles = append(c.Roles, c.Role)
	}
	return c, nil
}

// Google's JWKS endpoint for Firebase ID tokens, and the issuer prefix a
// Firebase token carries (the project ID is appended to form the full iss).
const (
	firebaseJWKSURL      = "https://www.googleapis.com/service_accounts/v1/jwk/securetoken@system.gserviceaccount.com"
	firebaseIssuerPrefix = "https://securetoken.google.com/"
)

// FirebaseOpts configures a [Firebase] validator.
type FirebaseOpts struct {
	// ProjectIDs is the set of accepted Firebase project IDs (typically read
	// from FIREBASE_PROJECT_IDS, comma-separated). A token is accepted only
	// when its aud is one of these AND its iss is
	// securetoken.google.com/<that same project>. At least one is required.
	// Accepting several lets one binary serve clients from multiple Firebase
	// projects.
	ProjectIDs []string

	// KeyFunc overrides Google's JWKS endpoint (tests, or a custom key
	// source). When nil the validator builds one from the well-known
	// Firebase JWKS URL via [JWKSKeyfunc].
	KeyFunc jwt.Keyfunc
}

// Firebase returns a [Validator] for Firebase ID tokens. Firebase issues
// RS256 JWTs signed with Google's rotating keys; each accepted project pins
// its own iss+aud, so per-project enforcement is expressed as one
// [JWKSValidator] per project composed through a [MultiValidator]. Claims are
// mapped by [firebaseResolver] (sub = uid).
//
//	// A value this app's config does not carry yet gets a new field in
//	// proto/<project>/config/v1/config.proto — that is the one place a
//	// per-deployment value is declared.
//	validator, err := auth.Firebase(auth.FirebaseOpts{
//	    ProjectIDs: strings.Split(cfg.FirebaseProjectIds, ","),
//	})
func Firebase(opts FirebaseOpts) (*Validator, error) {
	ids := dedupeNonEmpty(opts.ProjectIDs)
	if len(ids) == 0 {
		return nil, fmt.Errorf("auth: Firebase requires at least one project ID")
	}
	kf := opts.KeyFunc
	if kf == nil {
		var err error
		kf, err = JWKSKeyfunc(firebaseJWKSURL)
		if err != nil {
			return nil, err
		}
	}
	tvs := make([]TokenValidator, 0, len(ids))
	for _, id := range ids {
		jv, err := NewJWKSValidator(JWKSValidatorConfig{
			SigningMethod: "RS256",
			KeyFunc:       kf,
			Issuer:        firebaseIssuerPrefix + id,
			Audience:      id,
			Resolver:      firebaseResolver{},
		})
		if err != nil {
			return nil, fmt.Errorf("auth: build Firebase validator for project %q: %w", id, err)
		}
		tvs = append(tvs, jv)
	}
	return NewValidator(Config{Provider: ProviderJWT, TokenValidators: tvs})
}

// firebaseResolver maps Firebase ID-token claims onto [Claims]. It reuses the
// built-in extraction (sub → UserID, email, org_id/role/roles from custom
// claims) and adds Firebase's uid fallback for the rare token missing sub.
type firebaseResolver struct{}

func (firebaseResolver) Resolve(mc map[string]any) (*Claims, error) {
	c := defaultProjectClaims(mc, claimOpts{})
	if c.UserID == "" {
		m := jwt.MapClaims(mc)
		if uid := getStringClaim(m, "user_id"); uid != "" {
			c.UserID = uid
		} else if uid := getStringClaim(m, "uid"); uid != "" {
			c.UserID = uid
		}
	}
	return c, nil
}

// dedupeNonEmpty trims, drops empties, and de-duplicates while preserving
// order — used to normalize a comma-split list of project IDs.
func dedupeNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
