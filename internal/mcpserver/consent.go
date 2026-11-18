// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/connectorgrant"
)

// Connector selection for OAuth callers.
//
// An OAuth access token identifies a USER; a grant says what may be touched.
// Bridging the two is the whole job of consent: the user chooses which
// connector a given client acts through, and that choice is recorded so the
// next request from that client resolves without asking again.
//
// The alternative — resolving a token to "whichever connector this user has"
// — is what this package refuses to do. With one connector it happens to be
// right; with several it silently hands a client authority the user never
// granted it, and the user has no way to see that it happened.

// BindingStore reads client-to-grant bindings: every binding of a user,
// whatever the state of its grant, so a binding whose grant was revoked or has
// expired is still seen (and asks again) rather than reading as "never bound"
// (and falling back to another grant).
//
// Only the read side is needed here: writes happen through the consent RPC,
// where the user is authenticated by session rather than by connector token.
type BindingStore interface {
	ListBindingsByUser(ctx context.Context, userID string) ([]*connectorgrant.ClientBinding, error)
}

// bindingFor is the user's binding stored under key
// (connectorgrant.BindingClientID), whatever its grant's state; nil when the
// client was never bound or no binding store is configured. Both the
// workspace and the forge endpoint resolve through it.
func bindingFor(ctx context.Context, bindings BindingStore, userID, key string) (*connectorgrant.ClientBinding, error) {
	if bindings == nil {
		return nil, nil
	}
	all, err := bindings.ListBindingsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, b := range all {
		if b.ClientID == key && b.GrantID != "" {
			return b, nil
		}
	}
	return nil, nil
}

// ErrConsentRequired means the caller is authenticated but has not chosen
// which connector this client may act through.
//
// It is distinct from an authentication failure on purpose: the client should
// send its user to the consent screen, not re-run the OAuth flow, which would
// return the same token and the same problem.
var ErrConsentRequired = errors.New("connector selection required")

// ConsentError carries the URL a user must visit to resolve ErrConsentRequired.
type ConsentError struct {
	ConsentURL string
}

func (e *ConsentError) Error() string {
	return "connector selection required: " + e.ConsentURL
}

func (e *ConsentError) Unwrap() error { return ErrConsentRequired }

// resolveForUser picks the grant an OAuth caller acts through.
//
// clientID is the OAuth client the validated token was issued to (its
// client_id claim), "" when the token names none. It is never a value the
// caller supplies in a header: an application holding a user's bearer could
// otherwise name another application and act through that one's broader grant.
//
// Order matters, and each step exists for a reason:
//
//  1. A binding of this client wins. The user chose it; nothing should
//     second-guess that. A client bound to a grant that is no longer usable
//     (revoked, expired, deleted) is asked again and never falls through to
//     step 2: the user's choice for this application no longer exists, and
//     the user's other grant may be broader than the one they chose.
//  2. Failing that — a client never bound, or a token that names no client and
//     so cannot be bound to a choice — a single live connector resolves. This
//     is not a guess: with exactly one, there is no other thing the user could
//     have meant, and requiring consent would be friction with no decision
//     behind it.
//  3. Otherwise consent is required. Zero connectors means there is nothing to
//     authorize; several means a real choice exists and only the user can make
//     it.
func resolveForUser(
	ctx context.Context,
	store connectorgrant.Store,
	bindings BindingStore,
	userID, clientID, consentURL string,
) (*Session, error) {
	consent := &ConsentError{ConsentURL: consentURLFor(consentURL, clientID)}

	if clientID != "" {
		bound, err := bindingFor(ctx, bindings, userID, connectorgrant.BindingClientID(connectorgrant.KindWorkspace, clientID))
		if err != nil {
			return nil, err
		}
		if bound != nil {
			if g, err := store.GetGrantByID(ctx, bound.GrantID); err == nil && isWorkspaceGrantOf(g, userID) && g.IsLive(time.Now()) == nil {
				return sessionForGrant(g), nil
			}
			return nil, consent
		}
	}

	grants, err := store.ListGrantsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	var live []*connectorgrant.Grant
	for _, g := range grants {
		// Only workspace grants: a forge grant (the Reliant Forge
		// connector's) reaches no daemon.
		if g.KindOrDefault() == connectorgrant.KindWorkspace && g.IsLive(time.Now()) == nil {
			live = append(live, g)
		}
	}

	if len(live) == 1 {
		return sessionForGrant(live[0]), nil
	}

	return nil, consent
}

func isWorkspaceGrantOf(g *connectorgrant.Grant, userID string) bool {
	return g != nil && g.KindOrDefault() == connectorgrant.KindWorkspace && authz.CanAccessPersonal(userID, g.UserID)
}

// consentURLFor builds the URL a client sends its user to.
//
// The client id travels in the query so the consent page can name the
// requesting application ("ChatGPT wants access…") rather than asking the user
// to authorize an anonymous something.
func consentURLFor(base, clientID string) string {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if base == "" {
		return ConsentPath
	}
	url := base + ConsentPath
	if clientID != "" {
		url += "?client_id=" + queryEscape(clientID)
	}
	return url
}

// ConsentPath is where the web app serves the consent screen.
const ConsentPath = "/settings/connectors/authorize"

// queryEscape percent-encodes a query value without pulling in net/url for one
// call, keeping the encoding visible where it is used.
func queryEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~':
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String()
}
