// Copyright (c) 2026 Reliant Labs

package checkoutteardown

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
)

// The package's surface is functions that take their collaborators per call:
// the caller's Store, and a Machine bound to the daemon holding the checkout.
// This contract declares those collaborators, and forge generates their mocks.

// Machine sends one command to the ONE daemon holding the checkout. resp may
// be nil when the caller ignores the response.
type Machine interface {
	Send(ctx context.Context, commandType string, payload, resp any, timeoutMs int32) error
}

// Store records a teardown's outcome (*db.Repo).
type Store interface {
	SettleCheckoutTeardown(ctx context.Context, id, state, detail string) (*db.CheckoutTeardown, error)
}
