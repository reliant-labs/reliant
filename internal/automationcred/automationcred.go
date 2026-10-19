// Copyright (c) 2025 Reliant Labs

package automationcred

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/reliant-labs/reliant/internal/auth"
)

// KeyName is the device name the token is minted under.
const KeyName = "reliant-automation"

type allowedKey struct{}

// Allow marks ctx as belonging to an unattended run, one whose launch event
// kind reports core.TriggerEventKind.Unattended. Only such a context may fall
// back to the stored token: a run an agent started with start_run is attended
// and must keep acting as the signed-in user.
func Allow(ctx context.Context) context.Context { return context.WithValue(ctx, allowedKey{}, true) }

func allowed(ctx context.Context) bool { v, _ := ctx.Value(allowedKey{}).(bool); return v }

// resolver is the Service.
type resolver struct{ keys KeyStore }

// New builds the resolver.
func New(deps Deps) Service { return &resolver{keys: deps.Keys} }

// BearerFor implements Service.
func (r *resolver) BearerFor(ctx context.Context, userID, daemonID string) (string, error) {
	if jwt, ok := auth.GetUserJWT(userID); ok && strings.TrimSpace(jwt) != "" {
		return jwt, nil
	}
	if !allowed(ctx) || r.keys == nil || daemonID == "" || userID == "" {
		return "", nil
	}
	token, err := r.keys.GetAutomationToken(ctx, userID, daemonID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(token), nil
}
