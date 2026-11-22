// Copyright (c) 2025 Reliant Labs

package app

import (
	"github.com/reliant-labs/reliant/internal/credentials/provideraccounts"
	"github.com/reliant-labs/reliant/internal/db"
)

// orgAccountStore is what the org-account handler and the seat matcher read: the provider
// accounts (credentials/provideraccounts) joined with the org administration the facade still
// holds (grants, seats, workspace bindings). The org half moves to credentials/orgaccounts and
// this join goes with it.
type orgAccountStore struct {
	provideraccounts.Service
	*db.Repo
}
