package core

import (
	"errors"
	"time"
)

// Route values stored in model_endpoints.route.
const (
	ModelEndpointRouteDirect    = "direct"
	ModelEndpointRouteViaDaemon = "via_daemon"
)

// ErrModelEndpointNotFound is returned when an endpoint does not exist OR is
// owned by another user; the two are deliberately indistinguishable.
var ErrModelEndpointNotFound = errors.New("model endpoint not found")

// ErrModelEndpointNameTaken is returned when the user already has an endpoint
// with that name.
var ErrModelEndpointNameTaken = errors.New("a model endpoint with that name already exists")

// ModelEndpoint is one row of model_endpoints. It carries NO secret:
// CredentialConnectionID points at the sealed connection store.
type ModelEndpoint struct {
	ID                     string
	UserID                 string
	Name                   string
	BaseURL                string
	Route                  string
	DaemonID               *string
	CredentialConnectionID *string
	HeaderNames            []string
	// ModelsJSON is protojson of the user's per-model settings; ProbeJSON is
	// protojson of the last probe result. Both are server-owned, replaced
	// wholesale.
	ModelsJSON string
	ProbeJSON  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
