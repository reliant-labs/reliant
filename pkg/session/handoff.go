package session

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Sign-in handoffs: short-lived, single-use records a broker keeps while one
// client's signed-in session hands a NEW session to another client (a desktop
// app on a loopback redirect, a sibling host) — forge/pkg/devidp's
// HandoffRoutes. A record is a pending request (minutes) or a one-time code
// (seconds). Both must be visible to every replica and taken exactly once, so
// they live in the session store, not in process memory.
//
// Like a session row, a handoff row holds no usable secret: Key is derived
// from the one-time value the client holds (never the value itself), and
// Sealed is encrypted under another key derived from it. The store is opaque
// to both.

// HandoffSchemaSQL is the auth_handoffs table [PostgresStore] keeps handoffs
// in. The project owns it as a migration, as it owns [SchemaSQL]'s: forge
// writes both when it scaffolds a login broker with server-side sessions, and
// a project adopting handoffs later pastes this into a migration of its own.
// It is a separate table (and constant) so SchemaSQL never changes.
const HandoffSchemaSQL = `CREATE TABLE auth_handoffs (
    key_hash   bytea       PRIMARY KEY,
    sealed     bytea       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE INDEX auth_handoffs_expires_idx ON auth_handoffs (expires_at);

COMMENT ON COLUMN auth_handoffs.key_hash IS 'forge:secret';
COMMENT ON COLUMN auth_handoffs.sealed IS 'forge:secret';
`

// handoffsTable is the table a PostgresStore keeps handoffs in.
const handoffsTable = "auth_handoffs"

// ErrNoHandoff reports a handoff record that does not exist: never stored,
// already taken, or swept.
var ErrNoHandoff = errors.New("session: no such handoff")

// Handoff is one pending handoff record.
type Handoff struct {
	// Key is the row's identity: a 32-byte hash derived from the one-time
	// value the client holds. Never the value.
	Key []byte
	// Sealed is the record's content, encrypted by the caller.
	Sealed []byte
	// ExpiresAt is when the record stops being honoured. A store returns an
	// expired record as it is (the caller refuses it); the sweep removes it.
	ExpiresAt time.Time
}

// HandoffStore holds handoff records. It is a separate interface from
// [Store], so a Store implemented outside forge keeps compiling: forge's
// [MemoryStore] and [PostgresStore] implement both, and a broker finds the
// handoff store by asserting its session store to this interface.
type HandoffStore interface {
	// PutHandoff inserts h. Its Key is new: a collision is an error.
	PutHandoff(ctx context.Context, h Handoff) error
	// GetHandoff returns the record for key without removing it, or
	// ErrNoHandoff.
	GetHandoff(ctx context.Context, key []byte) (Handoff, error)
	// TakeHandoff removes the record for key and returns it, atomically:
	// of any number of concurrent calls for one key, across every replica,
	// exactly one gets the record and the rest get ErrNoHandoff. That is
	// what makes a one-time code single-use.
	TakeHandoff(ctx context.Context, key []byte) (Handoff, error)
	// DeleteExpiredHandoffs removes every record that has expired at now
	// and returns how many.
	DeleteExpiredHandoffs(ctx context.Context, now time.Time) (int64, error)
}

var (
	_ HandoffStore = (*MemoryStore)(nil)
	_ HandoffStore = (*PostgresStore)(nil)
)

// ── MemoryStore ─────────────────────────────────────────────────────────

// PutHandoff implements [HandoffStore].
func (s *MemoryStore) PutHandoff(_ context.Context, h Handoff) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handoffs == nil {
		s.handoffs = map[string]Handoff{}
	}
	if _, ok := s.handoffs[string(h.Key)]; ok {
		return errors.New("session: a handoff with this key exists")
	}
	s.handoffs[string(h.Key)] = cloneHandoff(h)
	return nil
}

// GetHandoff implements [HandoffStore].
func (s *MemoryStore) GetHandoff(_ context.Context, key []byte) (Handoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.handoffs[string(key)]
	if !ok {
		return Handoff{}, ErrNoHandoff
	}
	return cloneHandoff(h), nil
}

// TakeHandoff implements [HandoffStore].
func (s *MemoryStore) TakeHandoff(_ context.Context, key []byte) (Handoff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.handoffs[string(key)]
	if !ok {
		return Handoff{}, ErrNoHandoff
	}
	delete(s.handoffs, string(key))
	return h, nil
}

// DeleteExpiredHandoffs implements [HandoffStore].
func (s *MemoryStore) DeleteExpiredHandoffs(_ context.Context, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, h := range s.handoffs {
		if !now.Before(h.ExpiresAt) {
			delete(s.handoffs, k)
			n++
		}
	}
	return n, nil
}

func cloneHandoff(h Handoff) Handoff {
	h.Key = bytes.Clone(h.Key)
	h.Sealed = bytes.Clone(h.Sealed)
	return h
}

// ── PostgresStore ───────────────────────────────────────────────────────

// PutHandoff implements [HandoffStore]. The table must exist
// ([HandoffSchemaSQL]).
func (s *PostgresStore) PutHandoff(ctx context.Context, h Handoff) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO `+s.handoffs+` (key_hash, sealed, expires_at) VALUES ($1, $2, $3)`,
		h.Key, h.Sealed, h.ExpiresAt); err != nil {
		return fmt.Errorf("session store: insert handoff: %w", err)
	}
	return nil
}

// GetHandoff implements [HandoffStore].
func (s *PostgresStore) GetHandoff(ctx context.Context, key []byte) (Handoff, error) {
	var h Handoff
	err := s.db.QueryRowContext(ctx, `SELECT key_hash, sealed, expires_at FROM `+s.handoffs+` WHERE key_hash = $1`, key).
		Scan(&h.Key, &h.Sealed, &h.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Handoff{}, ErrNoHandoff
	}
	if err != nil {
		return Handoff{}, fmt.Errorf("session store: read handoff: %w", err)
	}
	return h, nil
}

// TakeHandoff implements [HandoffStore]: one DELETE … RETURNING, so of two
// concurrent takes the second finds the row gone.
func (s *PostgresStore) TakeHandoff(ctx context.Context, key []byte) (Handoff, error) {
	var h Handoff
	err := s.db.QueryRowContext(ctx, `DELETE FROM `+s.handoffs+` WHERE key_hash = $1 RETURNING key_hash, sealed, expires_at`, key).
		Scan(&h.Key, &h.Sealed, &h.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Handoff{}, ErrNoHandoff
	}
	if err != nil {
		return Handoff{}, fmt.Errorf("session store: take handoff: %w", err)
	}
	return h, nil
}

// DeleteExpiredHandoffs implements [HandoffStore].
func (s *PostgresStore) DeleteExpiredHandoffs(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM `+s.handoffs+` WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("session store: sweep handoffs: %w", err)
	}
	return res.RowsAffected()
}
