// Package daemonmirrortest builds the daemon mirror over a test database.
package daemonmirrortest

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/daemonmirror"
)

// DB is what the mirror needs of a test database: *db.Repo is one.
type DB interface {
	daemonmirror.TxRunner
	daemonmirror.Lifecycle
	daemonmirror.UserUpdates
	ORM() *orm.Client
}

// New is the mirror over repo's database.
func New(tb testing.TB, repo DB) daemonmirror.Service {
	tb.Helper()
	svc, err := daemonmirror.NewInstrumented(daemonmirror.Deps{
		Daemons: daemonmirror.NewStores(repo.ORM()), Tx: repo, Lifecycle: repo, Updates: repo,
	})
	if err != nil {
		tb.Fatalf("daemonmirrortest: %v", err)
	}
	return svc
}
