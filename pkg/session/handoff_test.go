package session_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/session"
)

func TestMemoryStoreHandoffContract(t *testing.T) {
	handoffContract(t, func(t *testing.T) session.HandoffStore { return session.NewMemoryStore() })
}

func TestPostgresStoreHandoffContract(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	handoffContract(t, func(t *testing.T) session.HandoffStore {
		dsn, cleanup, err := pgtest.NewURL()
		if err != nil {
			t.Fatalf("test postgres: %v", err)
		}
		t.Cleanup(cleanup)
		db, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		// Both tables, as the scaffolded migration creates them: the
		// sessions DDL is unchanged, the handoffs table is its own.
		if _, err := db.ExecContext(context.Background(), session.SchemaSQL+session.HandoffSchemaSQL); err != nil {
			t.Fatalf("apply the schemas: %v", err)
		}
		return session.NewPostgresStore(db)
	})
}

func handoff(t *testing.T, expires time.Time) session.Handoff {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return session.Handoff{Key: key, Sealed: []byte("sealed"), ExpiresAt: expires}
}

// handoffContract is what every session.HandoffStore must do.
func handoffContract(t *testing.T, newStore func(*testing.T) session.HandoffStore) {
	ctx := context.Background()
	base := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	t.Run("put get take", func(t *testing.T) {
		s := newStore(t)
		h := handoff(t, base.Add(time.Minute))
		if err := s.PutHandoff(ctx, h); err != nil {
			t.Fatal(err)
		}
		if err := s.PutHandoff(ctx, h); err == nil {
			t.Error("a second Put of one key succeeded")
		}
		got, err := s.GetHandoff(ctx, h.Key)
		if err != nil || !bytes.Equal(got.Sealed, h.Sealed) || !got.ExpiresAt.Equal(h.ExpiresAt) {
			t.Fatalf("Get = %+v, %v", got, err)
		}
		// Get does not consume.
		if _, err := s.GetHandoff(ctx, h.Key); err != nil {
			t.Fatalf("a second Get: %v", err)
		}
		got, err = s.TakeHandoff(ctx, h.Key)
		if err != nil || !bytes.Equal(got.Key, h.Key) || !bytes.Equal(got.Sealed, h.Sealed) {
			t.Fatalf("Take = %+v, %v", got, err)
		}
		if _, err := s.TakeHandoff(ctx, h.Key); !errors.Is(err, session.ErrNoHandoff) {
			t.Fatalf("a second Take = %v, want ErrNoHandoff", err)
		}
		if _, err := s.GetHandoff(ctx, h.Key); !errors.Is(err, session.ErrNoHandoff) {
			t.Fatalf("Get after Take = %v, want ErrNoHandoff", err)
		}
		// A taken key may be put back (a consent that failed for a reason
		// the person can fix).
		if err := s.PutHandoff(ctx, h); err != nil {
			t.Fatalf("put back: %v", err)
		}
	})

	t.Run("take is exactly once under contention", func(t *testing.T) {
		s := newStore(t)
		h := handoff(t, base.Add(time.Minute))
		if err := s.PutHandoff(ctx, h); err != nil {
			t.Fatal(err)
		}
		var won atomic.Int32
		var wg sync.WaitGroup
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := s.TakeHandoff(ctx, h.Key); err == nil {
					won.Add(1)
				} else if !errors.Is(err, session.ErrNoHandoff) {
					t.Errorf("Take: %v", err)
				}
			}()
		}
		wg.Wait()
		if n := won.Load(); n != 1 {
			t.Fatalf("%d concurrent Takes got the record, want exactly 1", n)
		}
	})

	t.Run("delete expired", func(t *testing.T) {
		s := newStore(t)
		gone, kept := handoff(t, base), handoff(t, base.Add(time.Minute))
		for _, h := range []session.Handoff{gone, kept} {
			if err := s.PutHandoff(ctx, h); err != nil {
				t.Fatal(err)
			}
		}
		n, err := s.DeleteExpiredHandoffs(ctx, base)
		if err != nil || n != 1 {
			t.Fatalf("DeleteExpiredHandoffs = %d, %v; want 1", n, err)
		}
		if _, err := s.GetHandoff(ctx, gone.Key); !errors.Is(err, session.ErrNoHandoff) {
			t.Errorf("the expired record survived: %v", err)
		}
		if _, err := s.GetHandoff(ctx, kept.Key); err != nil {
			t.Errorf("the live record was swept: %v", err)
		}
	})
}
