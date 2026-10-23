package tdd

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// SetupMockDB returns a fresh, isolated real-postgres *sql.DB suitable for
// hermetic unit tests. The DB and its underlying database are cleaned up
// via t.Cleanup, so the caller does not need to close it manually.
//
// forge is postgres-pinned: the DB is a per-test database on the
// process-shared ephemeral postgres (pkg/pgtest — embedded-postgres by
// default, or the FORGE_TEST_POSTGRES_URL server). No driver blank-import
// is required; pgtest owns the driver. The first call in a process boots
// the shared server (downloading the pg binary on a fresh machine).
func SetupMockDB(t *testing.T) *sql.DB {
	t.Helper()
	db, cleanup, err := pgtest.New()
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	return db
}

// WithTimeout returns a context.Context with the given deadline and a
// cleanup function that cancels it. Suitable for use inside a
// [Case].Setup hook or directly as Case.Ctx.
//
//	tdd.Case[Req, Resp]{
//	    Name: "slow", Req: ...,
//	    Ctx:  func() context.Context { ctx, _ := tdd.WithTimeout(2*time.Second); return ctx }(),
//	},
//
// Cancel is exposed so tests that want eager cleanup can defer it; the
// returned context will already be canceled by Go's runtime once the
// timeout elapses.
func WithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}
