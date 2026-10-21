// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// Route values stored in model_endpoints.route.
const (
	ModelEndpointRouteDirect    = core.ModelEndpointRouteDirect
	ModelEndpointRouteViaDaemon = core.ModelEndpointRouteViaDaemon
)

// ErrModelEndpointNotFound is returned when an endpoint does not exist OR is
// owned by another user; the two are deliberately indistinguishable.
var ErrModelEndpointNotFound = core.ErrModelEndpointNotFound

// ErrModelEndpointNameTaken is returned when the user already has an endpoint
// with that name.
var ErrModelEndpointNameTaken = core.ErrModelEndpointNameTaken

// ModelEndpoint is an alias to the shared core model endpoint (one row of
// model_endpoints, no secret).
type ModelEndpoint = core.ModelEndpoint

// CreateModelEndpoint inserts e. e.ID, e.UserID, e.Name, e.BaseURL and e.Route
// are required.
func (r *Repo) CreateModelEndpoint(ctx context.Context, e *ModelEndpoint) error {
	if e == nil || e.ID == "" || e.UserID == "" {
		return fmt.Errorf("model endpoint id and user ID are required")
	}
	return store.InsertModelEndpoint(ctx, r.ORM(), e)
}

// GetModelEndpoint returns the user's endpoint, or ErrModelEndpointNotFound.
func (r *Repo) GetModelEndpoint(ctx context.Context, userID, id string) (*ModelEndpoint, error) {
	if userID == "" || id == "" {
		return nil, ErrModelEndpointNotFound
	}
	return store.GetModelEndpoint(ctx, r.ORM(), userID, id)
}

// ListModelEndpoints returns every endpoint the user owns, by name.
func (r *Repo) ListModelEndpoints(ctx context.Context, userID string) ([]*ModelEndpoint, error) {
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	return store.ListModelEndpoints(ctx, r.ORM(), userID)
}

// UpdateModelEndpoint replaces the mutable columns of the user's endpoint.
func (r *Repo) UpdateModelEndpoint(ctx context.Context, e *ModelEndpoint) error {
	if e == nil || e.ID == "" || e.UserID == "" {
		return ErrModelEndpointNotFound
	}
	return store.ReplaceModelEndpoint(ctx, r.ORM(), e)
}

// SetModelEndpointProbe stores a fresh probe result without touching anything
// else. A missing endpoint is not an error: a periodic refresh can race a
// delete.
func (r *Repo) SetModelEndpointProbe(ctx context.Context, userID, id, probeJSON string) error {
	return store.SetModelEndpointProbe(ctx, r.ORM(), userID, id, probeJSON)
}

// DeleteModelEndpoint removes the user's endpoint.
func (r *Repo) DeleteModelEndpoint(ctx context.Context, userID, id string) error {
	return store.RemoveModelEndpoint(ctx, r.ORM(), userID, id)
}
