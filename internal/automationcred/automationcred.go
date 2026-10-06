// Copyright (c) 2025 Reliant Labs

// Package automationcred holds the delegated credential an unattended trigger
// fire uses to wake the ONE daemon its trigger names.
//
// The token is a daemon-bound `daemon:resume` rlat_ minted while the user is
// signed in, stored in api_keys under a reserved per-daemon provider name. It
// is read only through BearerFor, only for an exact daemon id.
package automationcred

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// KeyName is the device name the token is minted under.
const KeyName = "reliant-automation"

// Provider is the reserved api_keys provider for one daemon's token.
func Provider(daemonID string) string { return core.AutomationProviderPrefix + daemonID }

type keyStore interface {
	GetProviderAPIKey(ctx context.Context, userID, provider string) (string, error)
}

type allowedKey struct{}

// Allow marks ctx as belonging to an unattended run, one whose launch event
// kind reports core.TriggerEventKind.Unattended. Only such a context may fall
// back to the stored token: a run an agent started with start_run is attended
// and must keep acting as the signed-in user.
func Allow(ctx context.Context) context.Context { return context.WithValue(ctx, allowedKey{}, true) }

func allowed(ctx context.Context) bool { v, _ := ctx.Value(allowedKey{}).(bool); return v }

// Resolver picks the Bearer for a control-plane call.
type Resolver struct{ store keyStore }

func NewResolver(store keyStore) *Resolver { return &Resolver{store: store} }

// BearerFor returns, in order: the user's live JWT; the stored automation token
// for exactly daemonID (only when daemonID != "" and ctx is Allow-marked);
// otherwise "".
func (r *Resolver) BearerFor(ctx context.Context, userID, daemonID string) (string, error) {
	if jwt, ok := auth.GetUserJWT(userID); ok && strings.TrimSpace(jwt) != "" {
		return jwt, nil
	}
	if !allowed(ctx) || r == nil || r.store == nil || daemonID == "" || userID == "" {
		return "", nil
	}
	token, err := r.store.GetProviderAPIKey(ctx, userID, Provider(daemonID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(token), nil
}
