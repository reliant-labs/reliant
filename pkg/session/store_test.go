package session_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/session"
)

func TestMemoryStoreContract(t *testing.T) {
	storeContract(t, func(t *testing.T) session.Store { return session.NewMemoryStore() })
}

func TestPostgresStoreContract(t *testing.T) {
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	storeContract(t, func(t *testing.T) session.Store {
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
		if _, err := db.ExecContext(context.Background(), session.SchemaSQL); err != nil {
			t.Fatalf("apply session.SchemaSQL: %v", err)
		}
		return session.NewPostgresStore(db)
	})
}

func record(t *testing.T, subject string, created time.Time) session.Record {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return session.Record{Key: key, Subject: subject, Sealed: []byte("sealed-" + subject), UserAgent: "ua",
		IssuerSession: "idp-" + subject, ProvedAt: created,
		CreatedAt: created, LastSeenAt: created, RefreshedAt: created,
		IdleExpiresAt: created.Add(time.Hour), ExpiresAt: created.Add(2 * time.Hour)}
}

// storeContract is what every session.Store must do, run against each implementation.
func storeContract(t *testing.T, newStore func(*testing.T) session.Store) {
	ctx := context.Background()
	base := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

	t.Run("create get update delete", func(t *testing.T) {
		s := newStore(t)
		r := record(t, "u1", base)
		if err := s.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := s.Create(ctx, r); err == nil {
			t.Error("a second Create of one key succeeded")
		}
		got, err := s.Get(ctx, r.Key)
		if err != nil || got.Subject != "u1" || !bytes.Equal(got.Sealed, r.Sealed) || !got.ExpiresAt.Equal(r.ExpiresAt) ||
			got.IssuerSession != "idp-u1" || !got.ProvedAt.Equal(r.ProvedAt) {
			t.Fatalf("Get = %+v, %v", got, err)
		}
		later := base.Add(time.Minute)
		if err := s.Update(ctx, r.Key, func(rec *session.Record) error {
			rec.Sealed, rec.LastSeenAt, rec.ProvedAt, rec.Subject = []byte("new"), later, later, "someone-else"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		got, _ = s.Get(ctx, r.Key)
		if string(got.Sealed) != "new" || !got.LastSeenAt.Equal(later) || !got.ProvedAt.Equal(later) || got.Subject != "u1" {
			t.Errorf("after Update: %+v (Subject must not be rewritten)", got)
		}
		abort := errors.New("abort")
		if err := s.Update(ctx, r.Key, func(rec *session.Record) error { rec.Sealed = []byte("lost"); return abort }); !errors.Is(err, abort) {
			t.Errorf("Update with a failing fn = %v", err)
		}
		if got, _ := s.Get(ctx, r.Key); string(got.Sealed) != "new" {
			t.Error("a failed Update wrote")
		}
		if err := s.Delete(ctx, r.Key); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Get(ctx, r.Key); !errors.Is(err, session.ErrNoSession) {
			t.Errorf("Get after Delete = %v", err)
		}
		if err := s.Update(ctx, r.Key, func(*session.Record) error { return nil }); !errors.Is(err, session.ErrNoSession) {
			t.Errorf("Update of a missing key = %v", err)
		}
		if err := s.Delete(ctx, r.Key); err != nil {
			t.Errorf("Delete of a missing key = %v", err)
		}
	})

	t.Run("update serializes one key", func(t *testing.T) {
		s := newStore(t)
		r := record(t, "u1", base)
		r.Sealed = []byte{0}
		if err := s.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Update(ctx, r.Key, func(rec *session.Record) error {
					n := rec.Sealed[0]
					time.Sleep(2 * time.Millisecond) // widen the window a lost update needs
					rec.Sealed = []byte{n + 1}
					return nil
				}); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if got, _ := s.Get(ctx, r.Key); got.Sealed[0] != 10 {
			t.Errorf("10 concurrent increments left %d: Update does not serialize", got.Sealed[0])
		}
	})

	t.Run("take returns what an update in progress wrote", func(t *testing.T) {
		s := newStore(t)
		r := record(t, "u1", base)
		r.Sealed = []byte("old")
		if err := s.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		inside, release := make(chan struct{}), make(chan struct{})
		updated := make(chan error, 1)
		go func() {
			updated <- s.Update(ctx, r.Key, func(rec *session.Record) error {
				close(inside)
				<-release
				rec.Sealed = []byte("rotated")
				return nil
			})
		}()
		<-inside
		took := make(chan session.Record, 1)
		tookErr := make(chan error, 1)
		go func() {
			got, err := s.Take(ctx, r.Key)
			took <- got
			tookErr <- err
		}()
		close(release)
		if err := <-updated; err != nil {
			t.Fatalf("Update: %v", err)
		}
		got, err := <-took, <-tookErr
		if err != nil || string(got.Sealed) != "rotated" || got.Subject != "u1" {
			t.Fatalf("Take = %q (%s), %v; want the update's write", got.Sealed, got.Subject, err)
		}
		if _, err := s.Get(ctx, r.Key); !errors.Is(err, session.ErrNoSession) {
			t.Errorf("Get after Take = %v", err)
		}
		if _, err := s.Take(ctx, r.Key); !errors.Is(err, session.ErrNoSession) {
			t.Errorf("Take of a missing key = %v", err)
		}
	})

	t.Run("list delete subject sweep", func(t *testing.T) {
		s := newStore(t)
		old, mid, other := record(t, "u1", base), record(t, "u1", base.Add(time.Minute)), record(t, "u2", base)
		for _, r := range []session.Record{old, mid, other} {
			if err := s.Create(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		list, err := s.ListSubject(ctx, "u1")
		if err != nil || len(list) != 2 || !bytes.Equal(list[0].Key, mid.Key) || !bytes.Equal(list[1].Key, old.Key) {
			t.Fatalf("ListSubject = %d records, %v; want u1's two, newest first", len(list), err)
		}
		// At +1h the two created at base have reached their idle end; mid's
		// is a minute later.
		n, err := s.DeleteExpired(ctx, base.Add(time.Hour))
		if err != nil || n != 2 {
			t.Fatalf("DeleteExpired = %d, %v; want 2", n, err)
		}
		if _, err := s.Get(ctx, mid.Key); err != nil {
			t.Errorf("a live session was swept: %v", err)
		}
		if n, err := s.DeleteSubject(ctx, "u1"); err != nil || n != 1 {
			t.Errorf("DeleteSubject = %d, %v; want 1", n, err)
		}
		if list, _ := s.ListSubject(ctx, "u1"); len(list) != 0 {
			t.Errorf("after DeleteSubject: %d records", len(list))
		}
	})

	t.Run("a manager over it", func(t *testing.T) {
		now := base
		var calls int
		m, err := session.NewManager(session.Config{Store: newStore(t), CacheTTL: -1, Now: func() time.Time { return now },
			Refresher: session.RefresherFunc(func(_ context.Context, rt string) (session.Tokens, error) {
				calls++
				if rt != fmt.Sprintf("rt-%d", calls) {
					return session.Tokens{}, fmt.Errorf("invalid_grant: %w", session.ErrRefreshRejected)
				}
				return session.Tokens{AccessToken: fmt.Sprintf("at-%d", calls+1), RefreshToken: fmt.Sprintf("rt-%d", calls+1), Expiry: now.Add(time.Hour)}, nil
			})})
		if err != nil {
			t.Fatal(err)
		}
		s, err := m.Create(ctx, session.Tokens{AccessToken: "at-1", RefreshToken: "rt-1", Expiry: now.Add(time.Hour)}, session.Meta{Subject: "u1"})
		if err != nil {
			t.Fatal(err)
		}
		for i := 2; i <= 3; i++ {
			now = now.Add(2 * time.Hour)
			if tok, err := m.AccessToken(ctx, s.ID); err != nil || tok != fmt.Sprintf("at-%d", i) {
				t.Fatalf("renewal %d through the store = %q, %v (the rotated refresh token must be stored)", i, tok, err)
			}
		}
		if _, _, err := m.Revoke(ctx, s.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := m.AccessToken(ctx, s.ID); !errors.Is(err, session.ErrNoSession) {
			t.Errorf("after Revoke: %v", err)
		}
	})
}
