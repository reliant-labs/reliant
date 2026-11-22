// Package providerbackofftest builds the provider-backoff service over a test
// database.
package providerbackofftest

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/providerbackoff"
	"github.com/reliant-labs/reliant/internal/store"
)

// DB is what the service needs of a test database: *db.Repo is one.
type DB interface {
	providerbackoff.TxRunner
	providerbackoff.ChatActivity
	ORM() *orm.Client
}

// New is the service over repo's database.
func New(tb testing.TB, repo DB) providerbackoff.Service {
	tb.Helper()
	svc, err := providerbackoff.NewInstrumented(providerbackoff.Deps{
		Markers:  store.NewProviderBackoffExtStore(repo.ORM()),
		Tx:       repo,
		Activity: repo,
	})
	if err != nil {
		tb.Fatalf("providerbackofftest: %v", err)
	}
	return svc
}
