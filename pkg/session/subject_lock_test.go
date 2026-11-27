package session_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/session"
)

func TestMemoryStoreLocksASubject(t *testing.T) {
	s := session.NewMemoryStore()
	subjectLockContract(t, s, s)
}

// Two PostgresStores over one database are two replicas: the lock is the
// database's, so each waits for the other.
func TestPostgresStoreLocksASubjectAcrossReplicas(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	open := func() *session.PostgresStore {
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return session.NewPostgresStore(db)
	}
	subjectLockContract(t, open(), open())
}

// subjectLockContract: a and b may be one store or two replicas of one.
func subjectLockContract(t *testing.T, a, b session.SubjectLocker) {
	ctx := context.Background()
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- a.LockSubject(ctx, "u1", func(context.Context) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	// Another subject is not held up.
	other := make(chan error, 1)
	go func() { other <- b.LockSubject(ctx, "u2", func(context.Context) error { return nil }) }()
	select {
	case err := <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("locking u2 waited for u1's lock")
	}

	// The same subject waits until the holder returns.
	entered := make(chan struct{})
	second := make(chan error, 1)
	go func() {
		second <- b.LockSubject(ctx, "u1", func(context.Context) error {
			close(entered)
			return errors.New("fn's own error")
		})
	}()
	select {
	case <-entered:
		t.Fatal("a second holder entered while u1 was locked")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err == nil || err.Error() != "fn's own error" {
		t.Errorf("second LockSubject = %v, want fn's own error", err)
	}
}

func TestManagerLockSubjectFailsClosedWithoutALockingStore(t *testing.T) {
	m, err := session.NewManager(session.Config{Store: struct{ session.Store }{session.NewMemoryStore()}})
	if err != nil {
		t.Fatal(err)
	}
	ran := false
	err = m.LockSubject(context.Background(), "u1", func(context.Context) error { ran = true; return nil })
	if !errors.Is(err, session.ErrNoSubjectLock) || !errors.Is(err, session.ErrUnavailable) || ran {
		t.Fatalf("LockSubject = %v (ran %v), want ErrNoSubjectLock and fn not run", err, ran)
	}
	m, _ = session.NewManager(session.Config{Store: session.NewMemoryStore()})
	want := errors.New("boom")
	if err := m.LockSubject(context.Background(), "u1", func(context.Context) error { return want }); !errors.Is(err, want) {
		t.Errorf("LockSubject = %v, want fn's error", err)
	}
}
