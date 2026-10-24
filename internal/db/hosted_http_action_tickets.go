package db

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// CreateHostedHTTPActionTicket inserts authority derived from a live run. False
// means provenance failed or this activity already claimed an execution.
func (r *Repo) CreateHostedHTTPActionTicket(ctx context.Context, ticket core.HostedHTTPActionTicket) (bool, error) {
	return store.CreateHostedHTTPActionTicket(ctx, r.ORM(), ticket)
}

// RedeemHostedHTTPActionTicket atomically consumes one exact live ticket.
func (r *Repo) RedeemHostedHTTPActionTicket(ctx context.Context, id, grant, digest string) (*core.HostedHTTPActionTicket, error) {
	return store.RedeemHostedHTTPActionTicket(ctx, r.ORM(), id, grant, digest)
}
