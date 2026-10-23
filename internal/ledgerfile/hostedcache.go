package ledgerfile

// A local mirror of a HOSTED ledger, and the queue of records made against it
// while it was unreachable.
//
// WHY THIS LIVES BESIDE THE MACHINE LEDGER. An env that declares a control
// plane keeps its ledger there, so this machine's ledger holds none of its
// history — and when the control plane is down, `forge env deploy` has nothing
// to read. The mirror is what a deploy falls back to when the operator says so
// (--offline-ledger): the env's current promotion and the release it names,
// exactly as the control plane last reported them.
//
// IT IS A CACHE, NEVER AUTHORITY. Nothing here is consulted unless the caller
// asked for the offline path, and every record made from it is queued for
// replay rather than treated as history. The hosted ledger stays the one
// source of truth; this is how an operator keeps deploying while it is away.

import (
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

const (
	hostedCacheKind = "hosted-cache"
	hostedQueueKind = "hosted-queue"
)

// HostedSnapshot is what a control plane last said an env runs.
type HostedSnapshot struct {
	Env string `json:"env"`
	// Endpoint is the control plane the snapshot came from. A snapshot is
	// only ever used for the endpoint that produced it.
	Endpoint string `json:"endpoint"`
	// RefreshedAt is when a successful ledger read or write last produced
	// this snapshot — its age is what an offline deploy prints.
	RefreshedAt time.Time `json:"refreshed_at"`
	// Bound is false when the control plane said the env was never promoted.
	Bound bool `json:"bound"`
	// Current is nil unless Bound (a Promotion refuses to decode as zero).
	Current *release.Promotion `json:"current,omitempty"`
	// Release is the release Current names, with the digests it pins. Nil
	// when it could not be read at refresh time.
	Release *release.Release `json:"release,omitempty"`
	// History is the promotions BEFORE Current, newest first, each with the
	// release it bound. Together with Current it is the break-glass window:
	// the releases an offline deploy can roll back to. Window is how many
	// promotions (Current included) the snapshot was asked to keep; zero
	// means History was never read, so a reader refetches it.
	History []CachedPromotion `json:"history,omitempty"`
	Window  int               `json:"window,omitempty"`
}

// CachedPromotion is one cached promotion and the release it bound. Release
// is nil when the release could not be read at refresh time: the promotion is
// then listed but cannot be deployed offline (its digests are unknown).
type CachedPromotion struct {
	Promotion release.Promotion `json:"promotion"`
	Release   *release.Release  `json:"release,omitempty"`
}

func (h HostedSnapshot) validate() error {
	switch {
	case h.Env == "":
		return fmt.Errorf("%w: hosted snapshot env is required", release.ErrInvalid)
	case h.Endpoint == "":
		return fmt.Errorf("%w: hosted snapshot endpoint is required", release.ErrInvalid)
	case h.Bound && h.Current == nil:
		return fmt.Errorf("%w: a bound hosted snapshot needs its promotion", release.ErrInvalid)
	case h.Bound:
		if err := h.Current.Validate(); err != nil {
			return err
		}
	}
	for _, c := range h.History {
		if err := c.Promotion.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// QueuedPromotion is a promotion made while the control plane was
// unreachable, waiting to be replayed.
type QueuedPromotion struct {
	// Key identifies this entry. It is the idempotency key of the replay's
	// evidence, so a replay interrupted after the control plane accepted it
	// converges instead of recording twice.
	Key      string `json:"key"`
	Env      string `json:"env"`
	Endpoint string `json:"endpoint"`
	Release  string `json:"release"`
	// ExpectedPromotionID is the promotion the cache said was current when
	// this was queued; the replay compare-and-sets against it. Empty means
	// the cache said the env was never promoted.
	ExpectedPromotionID string        `json:"expected_promotion_id,omitempty"`
	PromotedBy          release.Actor `json:"promoted_by,omitempty"`
	Note                string        `json:"note,omitempty"`
	// Targets is the deploy's scope (release.Promotion.Targets), replayed
	// with it so the control plane records what the offline deploy moved.
	Targets  []string  `json:"targets,omitempty"`
	QueuedAt time.Time `json:"queued_at"`
	// CacheRefreshedAt is the age of the cache this was decided from.
	CacheRefreshedAt time.Time `json:"cache_refreshed_at"`
	// AfterKey is the queued record this one was made on top of, when an
	// earlier offline deploy in the same outage had not replayed yet (a second
	// rollback). The replay sends it only once that record has landed, and
	// compare-and-sets against the promotion that record created.
	AfterKey string `json:"after_key,omitempty"`
	// Gates are the evidence recorded after the apply ran (its verdict).
	Gates []release.Gate `json:"gates,omitempty"`
}

func (q QueuedPromotion) validate() error {
	switch {
	case q.Key == "":
		return fmt.Errorf("%w: queued promotion key is required", release.ErrInvalid)
	case q.Env == "":
		return fmt.Errorf("%w: queued promotion env is required", release.ErrInvalid)
	case q.Release == "":
		return fmt.Errorf("%w: queued promotion release is required", release.ErrInvalid)
	}
	return nil
}

// PutHostedSnapshot replaces env's snapshot. Atomic: a reader sees the old
// snapshot or the new one, never a torn file.
func (s *Store) PutHostedSnapshot(snap HostedSnapshot) error {
	if err := snap.validate(); err != nil {
		return err
	}
	return s.withLock(func() error {
		return rewriteLines(s.envPath(hostedCacheKind, snap.Env), []any{snap})
	})
}

// HostedSnapshot returns env's snapshot, or (nil, nil) when none was ever
// taken.
func (s *Store) HostedSnapshot(env string) (*HostedSnapshot, error) {
	var out *HostedSnapshot
	err := s.withLock(func() error {
		all, err := decodeAll(s.envPath(hostedCacheKind, env), HostedSnapshot.validate)
		if err != nil {
			return err
		}
		if n := len(all); n > 0 {
			out = &all[n-1]
		}
		return nil
	})
	return out, err
}

// QueuePromotion appends q to env's queue. Idempotent on Key.
func (s *Store) QueuePromotion(q QueuedPromotion) error {
	if err := q.validate(); err != nil {
		return err
	}
	return s.withLock(func() error {
		path := s.envPath(hostedQueueKind, q.Env)
		existing, err := decodeAll(path, QueuedPromotion.validate)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if e.Key == q.Key {
				return nil
			}
		}
		return appendRecord(path, q)
	})
}

// QueuedPromotions returns env's queue, oldest first.
func (s *Store) QueuedPromotions(env string) ([]QueuedPromotion, error) {
	var out []QueuedPromotion
	err := s.withLock(func() error {
		var err error
		out, err = decodeAll(s.envPath(hostedQueueKind, env), QueuedPromotion.validate)
		return err
	})
	return out, err
}

// AttachQueuedGate adds g to the queued promotion key. A key that is no
// longer queued is an error: evidence with nothing to attach to is lost, and
// the caller should say so.
func (s *Store) AttachQueuedGate(env, key string, g release.Gate) error {
	return s.rewriteQueue(env, func(all []QueuedPromotion) ([]QueuedPromotion, error) {
		for i := range all {
			if all[i].Key == key {
				all[i].Gates = append(all[i].Gates, g)
				return all, nil
			}
		}
		return nil, fmt.Errorf("queued promotion %q of env %q is no longer queued", key, env)
	})
}

// DropQueuedPromotion removes key from env's queue. Idempotent.
func (s *Store) DropQueuedPromotion(env, key string) error {
	return s.rewriteQueue(env, func(all []QueuedPromotion) ([]QueuedPromotion, error) {
		kept := all[:0]
		for _, q := range all {
			if q.Key != key {
				kept = append(kept, q)
			}
		}
		return kept, nil
	})
}

// RebaseQueued re-points the records queued on top of afterKey at the
// promotion that record created, once it has landed. Persisted so a replay
// interrupted between two records resumes from the right expectation.
func (s *Store) RebaseQueued(env, afterKey, promotionID string) error {
	return s.rewriteQueue(env, func(all []QueuedPromotion) ([]QueuedPromotion, error) {
		for i := range all {
			if all[i].AfterKey == afterKey {
				all[i].AfterKey, all[i].ExpectedPromotionID = "", promotionID
			}
		}
		return all, nil
	})
}

func (s *Store) rewriteQueue(env string, fn func([]QueuedPromotion) ([]QueuedPromotion, error)) error {
	return s.withLock(func() error {
		path := s.envPath(hostedQueueKind, env)
		all, err := decodeAll(path, QueuedPromotion.validate)
		if err != nil {
			return err
		}
		next, err := fn(all)
		if err != nil {
			return err
		}
		records := make([]any, 0, len(next))
		for _, q := range next {
			records = append(records, q)
		}
		return rewriteLines(path, records)
	})
}
