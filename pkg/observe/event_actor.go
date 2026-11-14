package observe

import (
	"context"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/reliant-labs/forge/pkg/authn"
)

// Product-event actor attributes, beside AttrUserID. Every id is opaque: an
// auth subject, an org id, a random install or visitor id. Never an email,
// a hostname, a username or anything a person typed.
const (
	// AttrOrgID is the org the event is about: Claims.OrgID on an
	// authenticated request, or Actor.OrgID. It lands in product_events.OrgId.
	AttrOrgID = "org.id"
	// AttrVisitorID is a persistent anonymous id: a browser's consented
	// visitor id (web runtime tier "full") or a CLI's random install id.
	AttrVisitorID = "visitor.id"
)

// Actor names who a product event is about when the context does not say:
// a webhook, a reaper, a queue consumer or a CLI, where no auth interceptor
// installed claims. Track reads it with WithActor; a non-empty field wins
// over the request's claims, and an empty one falls back to them.
type Actor struct {
	// OrgID is the org's opaque id.
	OrgID string
	// UserID is the user's opaque auth subject.
	UserID string
	// AnonymousID is a persistent anonymous id: a CLI's install id, or a
	// browser visitor id the app already holds with consent. Recorded as
	// visitor.id.
	AnonymousID string
}

type actorContextKey struct{}

// WithActor returns ctx carrying a; Track, TrackAuth and the consent check
// read it. Use it where the code knows who the event is about but the
// request carries no claims for it:
//
//	ctx = observe.WithActor(ctx, observe.Actor{OrgID: d.OrgID, UserID: d.CreatedBy})
//	observe.Track(ctx, "deploy_succeeded", slog.String("mode", "account"))
//
// Calling it again merges: the new non-empty fields replace the old ones.
func WithActor(ctx context.Context, a Actor) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	prev, _ := ctx.Value(actorContextKey{}).(Actor)
	if a.OrgID == "" {
		a.OrgID = prev.OrgID
	}
	if a.UserID == "" {
		a.UserID = prev.UserID
	}
	if a.AnonymousID == "" {
		a.AnonymousID = prev.AnonymousID
	}
	return context.WithValue(ctx, actorContextKey{}, a)
}

// ActorFrom returns who a product event emitted on ctx is about: the
// WithActor fields, with the request's claims (UserID, OrgID) filling any
// left empty. Ids that are not opaque (see ValidActorID) come back empty.
func ActorFrom(ctx context.Context) Actor {
	if ctx == nil {
		return Actor{}
	}
	a, _ := ctx.Value(actorContextKey{}).(Actor)
	if claims, ok := authn.ClaimsFromContext(ctx); ok && claims != nil {
		if a.UserID == "" {
			a.UserID = claims.UserID
		}
		if a.OrgID == "" {
			a.OrgID = claims.OrgID
		}
	}
	return Actor{
		OrgID:       opaqueID(ctx, a.OrgID),
		UserID:      opaqueID(ctx, a.UserID),
		AnonymousID: opaqueID(ctx, a.AnonymousID),
	}
}

// ValidActorID reports whether id can name an actor on a product event: at
// most MaxAuthUserIDLen characters, no '@' (so an email is never recorded as
// an id), and not an IP address or a JWT. The empty id is valid (no actor of
// that kind). Opaque ids are long random runs by nature, so the property
// guard's other patterns do not apply.
func ValidActorID(id string) bool {
	if id == "" {
		return true
	}
	return len(id) <= MaxAuthUserIDLen && !strings.Contains(id, "@") &&
		net.ParseIP(strings.Trim(id, "[]")) == nil && !jwtRE.MatchString(id)
}

var jwtRE = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.`)

// actorIDRefused is set once the first refused actor id has been warned
// about, so a misconfigured caller logs once per process.
var actorIDRefused atomic.Bool

func opaqueID(ctx context.Context, id string) string {
	if ValidActorID(id) {
		return id
	}
	if actorIDRefused.CompareAndSwap(false, true) {
		FromContext(ctx).WarnContext(ctx, "product event actor id dropped: ids must be opaque (no email, IP, token; at most 128 characters)")
	}
	return ""
}

// actorAttrs is the actor's ids as record attributes.
func actorAttrs(out []slog.Attr, a Actor) []slog.Attr {
	if a.UserID != "" {
		out = append(out, slog.String(AttrUserID, a.UserID))
	}
	if a.OrgID != "" {
		out = append(out, slog.String(AttrOrgID, a.OrgID))
	}
	if a.AnonymousID != "" {
		out = append(out, slog.String(AttrVisitorID, a.AnonymousID))
	}
	return out
}
