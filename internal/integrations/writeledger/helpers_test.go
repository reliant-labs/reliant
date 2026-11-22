package writeledger_test

import (
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/store"
)

// ledgerOf is the write ledger over repo's database.
func ledgerOf(repo *db.Repo) *writeledger.Ledger {
	return writeledger.NewLedger(store.NewIntegrationWriteExtStore(repo.ORM()))
}
