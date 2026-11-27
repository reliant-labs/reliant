package migratekit

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// AwaitMigrations is the waiter's boot hook: it logs that it waits, returns
// once the applier's schema is current, and logs that it is.
func TestAwaitMigrationsLogsAndReturnsOnceTheSchemaIsCurrent(t *testing.T) {
	dsn := requirePG(t)
	db := openRecordDB(t, dsn)
	ctx := context.Background()
	embedded := release(olderFiles)

	if err := AutoMigrate(embedded, "migrations")(openRecordDB(t, dsn), nil); err != nil {
		t.Fatal(err)
	}
	out := logOutput(t, func(l *slog.Logger) {
		wait := AwaitMigrations(embedded, "migrations", AwaitOptions{Timeout: time.Minute, Poll: 10 * time.Millisecond})
		if err := wait(ctx, db, l); err != nil {
			t.Errorf("a current schema satisfies the waiter: %v", err)
		}
	})
	for _, want := range []string{"waiting for the database schema", "want=2", "database schema is current", "version=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}

	// A schema behind the embedded set fails, and says so, without logging success.
	out = logOutput(t, func(l *slog.Logger) {
		wait := AwaitMigrations(release(merged(olderFiles, compatibleThird)), "migrations", AwaitOptions{Timeout: 100 * time.Millisecond, Poll: 10 * time.Millisecond})
		var pending *SchemaPendingError
		if err := wait(ctx, db, l); !errors.As(err, &pending) {
			t.Errorf("err = %v, want a SchemaPendingError", err)
		}
	})
	if strings.Contains(out, "database schema is current") {
		t.Errorf("a timed-out wait must not log success:\n%s", out)
	}
}
