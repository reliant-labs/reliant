// Package llmusagetest builds the usage ledger over a test database.
package llmusagetest

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/llmusage"
	"github.com/reliant-labs/reliant/internal/store"
)

// DB is what the ledger needs of a test database: *db.Repo is one.
type DB interface {
	llmusage.TxRunner
	ORM() *orm.Client
}

// New is the ledger over repo's database.
func New(tb testing.TB, repo DB) llmusage.Service {
	tb.Helper()
	svc, err := llmusage.NewInstrumented(llmusage.Deps{Ledger: store.NewLlmCallUsageExtStore(repo.ORM()), Tools: store.NewToolCallUsageExtStore(repo.ORM()), Tx: repo})
	if err != nil {
		tb.Fatalf("llmusagetest: %v", err)
	}
	return svc
}
