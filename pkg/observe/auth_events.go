package observe

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
)

// The standard auth events: a fixed vocabulary of product events that says
// who signed up, signed in and signed out, whatever the auth provider. They
// are ordinary product events (the same record Track writes), so they land in
// the same product_events table, and the console counts users from them:
//
//   - active over a range = distinct users with auth_signed_in in it;
//   - new = distinct users with auth_signed_up in it;
//   - returning = active users with no auth_signed_up in it.
//
// Every count is a distinct count of user.id, so a duplicate event (two
// emitters, N replicas, a retried request) never inflates one.
//
// user.id is the provider's opaque subject: a Zitadel id, "auth0|abc", a
// Clerk "user_…". Never an email: TrackAuth refuses an id that contains '@'.

// AuthEvent names one of the standard auth events. The value is the product
// event name (the event.name attribute is "product." + it).
type AuthEvent string

const (
	// AuthSignedUp records an account created by the person's own action:
	// a self sign-up (password, passkey, magic link, a social first login) or
	// an accepted invitation. An admin inviting someone is not a sign-up until
	// the invitation is accepted.
	AuthSignedUp AuthEvent = "auth_signed_up"
	// AuthSignedIn records a session established for the user: a credential
	// just verified (kind credential), or an existing valid session seen
	// (kind resumed, at most once per user per UTC day per emitter).
	AuthSignedIn AuthEvent = "auth_signed_in"
	// AuthSignedOut records the person ending the session explicitly. No
	// count depends on it.
	AuthSignedOut AuthEvent = "auth_signed_out"
)

// AuthMethod is how the person proved who they are. An unknown value is
// recorded as AuthMethodOther, so the vocabulary stays bounded.
type AuthMethod string

const (
	AuthMethodPassword  AuthMethod = "password"
	AuthMethodPasskey   AuthMethod = "passkey"
	AuthMethodMagicLink AuthMethod = "magic_link"
	AuthMethodSocial    AuthMethod = "social"
	AuthMethodInvite    AuthMethod = "invite"
	AuthMethodVerify    AuthMethod = "verify"
	AuthMethodReset     AuthMethod = "reset"
	AuthMethodSSO       AuthMethod = "sso"
	AuthMethodOther     AuthMethod = "other"
)

// AuthKind says what an auth_signed_in event saw.
type AuthKind string

const (
	// AuthKindCredential: a credential was just verified. The default.
	AuthKindCredential AuthKind = "credential"
	// AuthKindResumed: an existing valid session was seen.
	AuthKindResumed AuthKind = "resumed"
)

// Well-known auth provider names. Any name matching ^[a-z][a-z0-9_]{0,31}$ is
// accepted, so an app can name its own; anything else is recorded as "other".
const (
	// AuthProviderForge is forge's managed identity (the devidp login broker).
	AuthProviderForge = "forge"
	// AuthProviderJWT is an unnamed JWT issuer seen by forge's auth middleware.
	AuthProviderJWT = "jwt"
	// AuthProviderCustom is an app's own auth system.
	AuthProviderCustom = "custom"
)

// The props an auth event carries, stored in product_events.Props under
// these keys.
const (
	AuthPropKind           = "kind"
	AuthPropMethod         = "method"
	AuthPropSocialProvider = "social_provider"
	AuthPropProvider       = "provider"
	AuthPropVerified       = "verified"
)

// MaxAuthUserIDLen is the longest user id an auth event accepts.
const MaxAuthUserIDLen = 128

var authProviderRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

var socialProviders = map[string]bool{"google": true, "github": true, "microsoft": true, "apple": true, "other": true}

// AuthOption sets one property of an auth event.
type AuthOption func(*authEventProps)

type authEventProps struct {
	kind           AuthKind
	method         AuthMethod
	socialProvider string
	provider       string
	verified       *bool
}

// WithMethod records how the person authenticated (signed_up, signed_in).
func WithMethod(m AuthMethod) AuthOption { return func(p *authEventProps) { p.method = m } }

// WithSocialProvider names the social login used when the method is
// AuthMethodSocial: google, github, microsoft, apple, or other.
func WithSocialProvider(name string) AuthOption {
	return func(p *authEventProps) { p.socialProvider = name }
}

// WithProvider names the auth provider: AuthProviderForge, "auth0", "clerk",
// "supabase", "firebase", "cognito", AuthProviderJWT, AuthProviderCustom, or a
// name of the app's own.
func WithProvider(name string) AuthOption { return func(p *authEventProps) { p.provider = name } }

// WithKind says what an auth_signed_in saw; AuthKindCredential when unset.
// Ignored on other events.
func WithKind(k AuthKind) AuthOption { return func(p *authEventProps) { p.kind = k } }

// WithVerified records whether the new account's email is verified. Only on
// auth_signed_up; ignored on other events.
func WithVerified(v bool) AuthOption { return func(p *authEventProps) { p.verified = &v } }

// authIDRefused is set once the first refused id has been warned about, so a
// misconfigured caller logs once per process, not once per sign-in.
var authIDRefused atomic.Bool

// ValidAuthUserID reports whether id can be an auth event's user.id: not
// empty, at most MaxAuthUserIDLen characters, and no '@' (an email is the one
// mistake that would turn the events into a personal-data leak).
func ValidAuthUserID(id string) bool {
	return id != "" && len(id) <= MaxAuthUserIDLen && !strings.Contains(id, "@")
}

// TrackAuth records one standard auth event for userID, the auth provider's
// opaque subject. It takes the id explicitly because at sign-in the request
// is still anonymous, so the claims Track reads are not there yet.
//
//	observe.TrackAuth(ctx, observe.AuthSignedIn, claims.Subject,
//		observe.WithMethod(observe.AuthMethodPassword), observe.WithProvider("auth0"))
//
// It writes through Track's path (one INFO record, event.name
// product.<event>, app.client backend, user.id) with the props kind, method,
// social_provider, provider and verified, each bounded to its vocabulary.
//
// Like Track it never fails the caller. An id that ValidAuthUserID refuses is
// dropped, with one WARN per process that names the rule and never the id.
// An event the app's own code records is that code's decision, so TrackAuth
// is not switched off by FORGE_PRODUCT_EVENTS; forge's own automatic auth
// events check AutoProductEvents before calling it.
func TrackAuth(ctx context.Context, ev AuthEvent, userID string, opts ...AuthOption) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch ev {
	case AuthSignedUp, AuthSignedIn, AuthSignedOut:
	default:
		FromContext(ctx).WarnContext(ctx, "auth event dropped: not a standard auth event", slog.String("event", string(ev)))
		return
	}
	// ValidActorID too: TrackAuth sets the actor's UserID itself, after
	// ActorFrom has applied the opaque-id policy, so an IP address or a JWT
	// must be refused here before it reaches the consent source or user.id.
	if !ValidAuthUserID(userID) || !ValidActorID(userID) {
		if authIDRefused.CompareAndSwap(false, true) {
			FromContext(ctx).WarnContext(ctx, "auth event dropped: the user id must be the provider's opaque subject (not empty, no '@', no IP address or token, at most 128 characters)",
				slog.String("event", string(ev)))
		}
		return
	}
	var p authEventProps
	for _, o := range opts {
		if o != nil {
			o(&p)
		}
	}
	actor := ActorFrom(ctx)
	actor.UserID = userID
	trackAs(ctx, string(ev), actor, AppClientBackend, authEventAttrs(ev, p))
}

// authEventAttrs bounds p to the vocabulary and drops the props that do not
// belong on ev.
func authEventAttrs(ev AuthEvent, p authEventProps) []slog.Attr {
	attrs := make([]slog.Attr, 0, 5)
	if ev == AuthSignedIn {
		kind := AuthKindCredential
		if p.kind == AuthKindResumed {
			kind = AuthKindResumed
		}
		attrs = append(attrs, slog.String(AuthPropKind, string(kind)))
	}
	if ev != AuthSignedOut && p.method != "" {
		m := boundAuthMethod(p.method)
		attrs = append(attrs, slog.String(AuthPropMethod, string(m)))
		if m == AuthMethodSocial && p.socialProvider != "" {
			sp := strings.ToLower(strings.TrimSpace(p.socialProvider))
			if !socialProviders[sp] {
				sp = "other"
			}
			attrs = append(attrs, slog.String(AuthPropSocialProvider, sp))
		}
	}
	if p.provider != "" {
		attrs = append(attrs, slog.String(AuthPropProvider, boundAuthProvider(p.provider)))
	}
	if ev == AuthSignedUp && p.verified != nil {
		attrs = append(attrs, slog.String(AuthPropVerified, strconv.FormatBool(*p.verified)))
	}
	return attrs
}

func boundAuthMethod(m AuthMethod) AuthMethod {
	switch AuthMethod(strings.ToLower(strings.TrimSpace(string(m)))) {
	case AuthMethodPassword:
		return AuthMethodPassword
	case AuthMethodPasskey:
		return AuthMethodPasskey
	case AuthMethodMagicLink:
		return AuthMethodMagicLink
	case AuthMethodSocial:
		return AuthMethodSocial
	case AuthMethodInvite:
		return AuthMethodInvite
	case AuthMethodVerify:
		return AuthMethodVerify
	case AuthMethodReset:
		return AuthMethodReset
	case AuthMethodSSO:
		return AuthMethodSSO
	}
	return AuthMethodOther
}

func boundAuthProvider(name string) string {
	if authProviderRE.MatchString(name) {
		return name
	}
	return "other"
}
