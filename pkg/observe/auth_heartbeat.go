package observe

import (
	"context"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/authn"
)

// The auth middleware's daily sign-in heartbeat: on the first authenticated
// call of a user in a UTC day, this process records one
// auth_signed_in {kind: resumed} (auth_events.go). It is what makes active
// users work for every forge backend behind Chain on any JWT provider (Auth0,
// Clerk, Supabase, Cognito, forge's managed identity) with no code: a user
// with a long-lived session or a refresh token never signs in again, but
// they call the API.
//
// Cost: at most one record per user per replica per day. The console counts
// distinct users, so the copies from N replicas (and from a server and a
// browser both reporting) collapse.

// MaxResumedUsersPerDay bounds the users the heartbeat remembers per UTC day.
// Past it the memory is cleared and starts again: a user seen again may be
// recorded twice that day, which the distinct counts absorb, and memory stays
// bounded.
const MaxResumedUsersPerDay = 100_000

// EnvIDPBrokerToken is the managed login broker's credential. A process that
// holds it signs users in through forge's managed identity, so its heartbeat
// names the provider "forge" rather than "jwt".
const EnvIDPBrokerToken = "IDP_BROKER_TOKEN"

// resumedSeen is the process's memory of who was recorded today, shared by
// every chain in the process so two listeners record a user once.
var resumedSeen = &resumedSet{}

type resumedSet struct {
	mu   sync.Mutex
	day  string
	seen map[string]struct{}
}

// first reports whether userID has not been recorded on day, and remembers it.
func (s *resumedSet) first(userID, day string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.day != day || s.seen == nil || len(s.seen) >= MaxResumedUsersPerDay {
		s.day = day
		s.seen = make(map[string]struct{})
	}
	if _, ok := s.seen[userID]; ok {
		return false
	}
	s.seen[userID] = struct{}{}
	return true
}

// AuthHeartbeatInterceptor records auth_signed_in {kind: resumed} on the
// first authenticated call of each user per UTC day in this process. It must
// run after Auth; Chain installs it there, after IdentitySpanInterceptor.
//
// A nil caller reads forge's own claims (authn.ClaimsFromContext): an app
// whose auth keeps the caller in a context of its own (it never calls
// authn.ContextWithClaims) must pass a caller, or the heartbeat sees nobody.
// provider names the auth provider ("supabase", "auth0", "clerk", …), in
// code: the provider is self-reported on the events, never declared in
// config. Empty is "forge" when the
// process holds the managed login broker's credential (EnvIDPBrokerToken),
// else "jwt". Calls with no user id and API-key principals (machines) record
// nothing. Like the CRUD events it is one of forge's automatic product
// events, so FORGE_PRODUCT_EVENTS=off switches it off (AutoProductEvents).
func AuthHeartbeatInterceptor(caller CallerLookup, provider string) connect.Interceptor {
	if caller == nil {
		caller = authn.ClaimsFromContext
	}
	if provider == "" {
		provider = AuthProviderJWT
		if envValue(EnvIDPBrokerToken) != "" || envValue(EnvIDPBrokerToken+"_FILE") != "" {
			provider = AuthProviderForge
		}
	}
	return authHeartbeat{caller: caller, provider: provider, seen: resumedSeen, now: time.Now}
}

type authHeartbeat struct {
	caller   CallerLookup
	provider string
	seen     *resumedSet
	now      func() time.Time
}

func (h authHeartbeat) beat(ctx context.Context) {
	if !AutoProductEvents() {
		return
	}
	claims, ok := h.caller(ctx)
	if !ok || claims == nil || claims.UserID == "" || claims.IsAPIKey() {
		return
	}
	if !h.seen.first(claims.UserID, h.now().UTC().Format(time.DateOnly)) {
		return
	}
	TrackAuth(ctx, AuthSignedIn, claims.UserID, WithKind(AuthKindResumed), WithProvider(h.provider))
}

func (h authHeartbeat) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !req.Spec().IsClient {
			h.beat(ctx)
		}
		return next(ctx, req)
	}
}

func (authHeartbeat) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (h authHeartbeat) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		h.beat(ctx)
		return next(ctx, conn)
	}
}
