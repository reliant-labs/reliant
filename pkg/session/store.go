package session

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrNoSession reports a session that does not exist, has expired, or was
// revoked. A caller treats all three alike: the person must sign in again.
var ErrNoSession = errors.New("session: no such session")

// ErrUnavailable wraps a failure that says nothing about the session itself —
// the store or the issuer could not be reached. The session may well be
// valid; the person should not be signed out over it.
var ErrUnavailable = errors.New("session: unavailable")

// Record is one session as a [Store] holds it. Key and Sealed are opaque to
// the store: Key is derived from the session id, Sealed is the tokens
// encrypted under another key derived from it.
type Record struct {
	// Key is the row's identity: a 32-byte hash derived from the session id.
	Key []byte
	// Subject is the identity provider's subject the session belongs to, in
	// the clear, so an account's sessions can be listed and revoked together.
	Subject string
	// Sealed holds the tokens.
	Sealed []byte
	// UserAgent is the User-Agent the session was created from, for an
	// account page's list. Informational only.
	UserAgent string
	// IssuerSession is the identity provider's own session reference, when
	// the sign-in yielded one (Meta.IssuerSession): what a sign-out at the
	// issuer, or a step-up check against it, names. Informational here.
	IssuerSession string

	CreatedAt time.Time
	// ProvedAt is when the person last PROVED a credential for this session
	// (signed in, or re-authenticated for a sensitive action), as opposed to
	// merely using it. An action that wants a recent proof compares against
	// it; a refresh never moves it.
	ProvedAt    time.Time
	LastSeenAt  time.Time
	RefreshedAt time.Time
	// IdleExpiresAt is when the session ends unless it is used before then;
	// each use pushes it out (never past ExpiresAt).
	IdleExpiresAt time.Time
	// ExpiresAt is the session's absolute end, fixed at creation.
	ExpiresAt time.Time
}

// expired reports whether r has ended at now.
func (r Record) expired(now time.Time) bool {
	return !now.Before(r.ExpiresAt) || !now.Before(r.IdleExpiresAt)
}

// Store holds session records. Implementations must be safe for concurrent
// use; [NewMemoryStore] and [NewPostgresStore] are forge's.
type Store interface {
	// Create inserts r. Its Key is new: a collision is an error.
	Create(ctx context.Context, r Record) error
	// Get returns the record for key, or ErrNoSession.
	Get(ctx context.Context, key []byte) (Record, error)
	// Update reads the record for key, calls fn on it, and writes back what
	// fn left, all while holding the record against any other Update of the
	// same key — across processes, for a shared store. That serialization
	// is what lets two replicas refresh one session without both spending
	// a rotating refresh token. fn's error aborts the write and is
	// returned; a missing record is ErrNoSession. Key and Subject are not
	// written back.
	Update(ctx context.Context, key []byte, fn func(*Record) error) error
	// Delete removes the record for key. A missing record is not an error.
	Delete(ctx context.Context, key []byte) error
	// Take removes the record for key and returns it as it was removed, or
	// ErrNoSession. It is serialized with Update like another Update of the
	// key: one in progress finishes first, and Take returns what it wrote.
	// That is what lets a sign-out revoke the refresh token a concurrent
	// renewal just rotated in, rather than the one the renewal spent.
	Take(ctx context.Context, key []byte) (Record, error)
	// DeleteSubject removes every record of subject and returns how many.
	DeleteSubject(ctx context.Context, subject string) (int64, error)
	// ListSubject returns subject's records, newest first.
	ListSubject(ctx context.Context, subject string) ([]Record, error)
	// DeleteExpired removes every record that has ended at now and returns
	// how many.
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

// MemoryStore is a [Store] in one process's memory. Sessions are lost when
// the process exits and are not shared between replicas, so it suits local
// development and tests, and a single-replica deployment that accepts
// signing everyone out on a restart — never anything behind a load balancer.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
	// locks serializes Update per key without holding mu across fn, which
	// may make a network call.
	locks map[string]*sync.Mutex
	// handoffs holds HandoffStore records (handoff.go).
	handoffs map[string]Handoff
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]Record{}, locks: map[string]*sync.Mutex{}, handoffs: map[string]Handoff{}}
}

// Create implements [Store].
func (s *MemoryStore) Create(_ context.Context, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[string(r.Key)]; ok {
		return errors.New("session: a record with this key exists")
	}
	s.records[string(r.Key)] = cloneRecord(r)
	return nil
}

// Get implements [Store].
func (s *MemoryStore) Get(_ context.Context, key []byte) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[string(key)]
	if !ok {
		return Record{}, ErrNoSession
	}
	return cloneRecord(r), nil
}

// Update implements [Store].
func (s *MemoryStore) Update(ctx context.Context, key []byte, fn func(*Record) error) error {
	s.mu.Lock()
	lock, ok := s.locks[string(key)]
	if !ok {
		lock = &sync.Mutex{}
		s.locks[string(key)] = lock
	}
	s.mu.Unlock()

	lock.Lock()
	defer lock.Unlock()
	r, err := s.Get(ctx, key)
	if err != nil {
		return err
	}
	if err := fn(&r); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.records[string(key)]
	if !ok {
		// Deleted while fn ran: a sign-out wins over a refresh.
		return ErrNoSession
	}
	r.Key, r.Subject = cur.Key, cur.Subject
	s.records[string(key)] = cloneRecord(r)
	return nil
}

// Delete implements [Store].
func (s *MemoryStore) Delete(_ context.Context, key []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, string(key))
	delete(s.locks, string(key))
	return nil
}

// Take implements [Store].
func (s *MemoryStore) Take(_ context.Context, key []byte) (Record, error) {
	s.mu.Lock()
	lock, ok := s.locks[string(key)]
	if !ok {
		lock = &sync.Mutex{}
		s.locks[string(key)] = lock
	}
	s.mu.Unlock()

	lock.Lock()
	defer lock.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[string(key)]
	delete(s.records, string(key))
	delete(s.locks, string(key))
	if !ok {
		return Record{}, ErrNoSession
	}
	return cloneRecord(r), nil
}

// DeleteSubject implements [Store].
func (s *MemoryStore) DeleteSubject(_ context.Context, subject string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, r := range s.records {
		if r.Subject == subject {
			delete(s.records, k)
			delete(s.locks, k)
			n++
		}
	}
	return n, nil
}

// ListSubject implements [Store].
func (s *MemoryStore) ListSubject(_ context.Context, subject string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, r := range s.records {
		if r.Subject == subject {
			out = append(out, cloneRecord(r))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return bytes.Compare(out[i].Key, out[j].Key) < 0
	})
	return out, nil
}

// DeleteExpired implements [Store].
func (s *MemoryStore) DeleteExpired(_ context.Context, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for k, r := range s.records {
		if r.expired(now) {
			delete(s.records, k)
			delete(s.locks, k)
			n++
		}
	}
	return n, nil
}

func cloneRecord(r Record) Record {
	r.Key = bytes.Clone(r.Key)
	r.Sealed = bytes.Clone(r.Sealed)
	return r
}
