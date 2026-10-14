// Copyright (c) 2025 Reliant Labs
package db

import (
	"github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
)

// IntegrationWrites returns the write ledger. It always uses the pool, never an
// ambient transaction: an intent row must be committed, and so visible to an
// event that arrives first, before the call it describes goes out.
func (r *Repo) IntegrationWrites() core.IntegrationWriteStore {
	return postgresstore.NewIntegrationWriteStore(pgdb.New(r.DB.SQLDB()))
}
