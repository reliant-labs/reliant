// Copyright (c) 2025 Reliant Labs

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

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

// BindingStore reads client-to-grant bindings.
//
// Only the read side is needed here: writes happen through the consent RPC,
// where the user is authenticated by session rather than by connector token.
type BindingStore interface {
	GetBinding(ctx context.Context, userID, clientID string) (*connectorgrant.ClientBinding, error)
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
// Order matters, and each step exists for a reason:
//
//  1. An explicit binding wins. The user chose it; nothing should second-guess
//     that.
//  2. Failing that, a single live connector resolves. This is not a guess —
//     with exactly one, there is no other thing the user could have meant, and
//     requiring consent would be friction with no decision behind it.
//  3. Otherwise consent is required. Zero connectors means there is nothing to
//     authorize; several means a real choice exists and only the user can make
//     it.
func resolveForUser(
	ctx context.Context,
	store connectorgrant.Store,
	bindings BindingStore,
	userID, clientID, consentURL string,
) (*Session, error) {
	if bindings != nil && clientID != "" {
		binding, err := bindings.GetBinding(ctx, userID, clientID)
		if err == nil && binding != nil && binding.GrantID != "" {
			grant, err := store.GetGrantByID(ctx, binding.GrantID)
			if err == nil {
				return sessionForGrant(grant), nil
			}
			// The bound grant is gone or revoked. Fall through to consent
			// rather than silently substituting another one — the user's
			// previous choice no longer exists, so they get to make a new one.
		}
	}

	grants, err := store.ListGrantsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	var live []*connectorgrant.Grant
	for _, g := range grants {
		if g.IsLive(time.Now()) == nil {
			live = append(live, g)
		}
	}

	if len(live) == 1 {
		return sessionForGrant(live[0]), nil
	}

	return nil, &ConsentError{ConsentURL: consentURLFor(consentURL, clientID)}
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
			b.WriteString(fmt.Sprintf("%%%02X", r))
		}
	}
	return b.String()
}
