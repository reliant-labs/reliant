package ledgerfile

import (
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// ROLLBACK FLOORS on the machine ledger (pkg/release floor.go has the model).
//
// Two append-only per-env logs beside `promotions`, in the layout the gates
// file uses: `floors` (one release.Floor per line, never rewritten) and
// `floor_lifts` (one lift per line, joined to its floor by id at read time).
// A floor is active when no lift names it. Like the hosted ledger's two
// tables, nothing is ever updated in place: lowering a floor is a lift plus a
// new floor, and the history of both survives.
//
// Every change runs under the store's lock WITH the env's promotion history,
// so a floor set and a promote of the same env are one critical section each
// and serialize: a floor cannot land between a promote's check and its
// append, and a promote cannot slip in between a `floor set --current`'s read
// of the current release and its write.

// FloorLift is one lift line: the floor it lifts and the lift itself.
type FloorLift struct {
	FloorID string `json:"floor_id"`
	release.FloorLift
}

// FloorChange is what one locked change to env's floors records: floors to
// add (the store assigns an empty ID and stamps SetAt) and lifts to append.
type FloorChange struct {
	Floors []release.Floor
	Lifts  []FloorLift
}

// Floors returns every floor env has recorded, oldest first, each with its
// lift joined when it was lifted.
func (s *Store) Floors(env string) ([]release.Floor, error) {
	var out []release.Floor
	err := s.withLock(func() error {
		var err error
		out, err = s.floorsLocked(env)
		return err
	})
	return out, err
}

func (s *Store) floorsLocked(env string) ([]release.Floor, error) {
	floors, err := decodeAll(s.envPath("floors", env), func(f release.Floor) error { return f.Validate() })
	if err != nil {
		return nil, err
	}
	lifts, err := decodeAll[FloorLift](s.envPath("floor_lifts", env), nil)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]int, len(floors))
	for i, f := range floors {
		if f.Env != env {
			return nil, fmt.Errorf("%s line %d records env %q, not %q", s.envPath("floors", env), i+1, f.Env, env)
		}
		byID[f.ID] = i
	}
	for _, l := range lifts {
		if i, ok := byID[l.FloorID]; ok && floors[i].Lift == nil {
			lift := l.FloorLift
			floors[i].Lift = &lift
		}
	}
	return floors, nil
}

// ChangeFloors applies one change to env's floors under the store's lock:
// decide reads the env's promotion history and every floor, and returns what
// to record (or an error, which records nothing). New floors get an id and a
// SetAt here; the change as recorded is returned.
func (s *Store) ChangeFloors(env string, decide func(history []release.Promotion, floors []release.Floor) (FloorChange, error)) (FloorChange, error) {
	var out FloorChange
	err := s.withLock(func() error {
		history, err := s.promotionsLocked(env)
		if err != nil {
			return err
		}
		floors, err := s.floorsLocked(env)
		if err != nil {
			return err
		}
		change, err := decide(history, floors)
		if err != nil {
			return err
		}
		out, err = s.recordFloorChangeLocked(env, change)
		return err
	})
	return out, err
}

// NewFloorID is a floor id for a floor a ChangeFloors decision must name
// before it is recorded (a `floor lower`'s lifts point at the floor it sets).
func NewFloorID() string { return "flr-" + newID() }

func (s *Store) recordFloorChangeLocked(env string, change FloorChange) (FloorChange, error) {
	now := time.Now().UTC().Truncate(time.Second)
	for i := range change.Floors {
		f := &change.Floors[i]
		if f.ID == "" {
			f.ID = NewFloorID()
		}
		if f.SetAt.IsZero() {
			f.SetAt = now
		}
		if f.Env != env {
			return FloorChange{}, fmt.Errorf("recording a floor of %q in %s's log", f.Env, env)
		}
		if err := f.Validate(); err != nil {
			return FloorChange{}, err
		}
		if err := appendRecord(s.envPath("floors", env), *f); err != nil {
			return FloorChange{}, err
		}
	}
	for i := range change.Lifts {
		l := &change.Lifts[i]
		if l.At.IsZero() {
			l.At = now
		}
		if err := appendRecord(s.envPath("floor_lifts", env), *l); err != nil {
			return FloorChange{}, err
		}
	}
	return change, nil
}

// FlooredAdmit is Admit with the env's floors (every one, lifted included)
// read in the same critical section.
type FlooredAdmit func(history []release.Promotion, floors []release.Floor, p release.Promotion) (*release.Promotion, error)

// FloorRaise returns the floors a promotion raises, given the env's floors
// before it and the entry the ledger now holds (appended, or the existing one
// a no-op returned).
type FloorRaise func(floors []release.Floor, promoted release.Promotion) []release.Floor

// AppendPromotionWithFloors is AppendPromotion with the env's rollback floors
// inside the same lock hold: admit judges the promotion against them, and the
// floors raise returns are appended with it, so the promotion is never
// visible without the floors it raised and no floor can land between the
// check and the append.
func (s *Store) AppendPromotionWithFloors(p release.Promotion, admit FlooredAdmit, raise FloorRaise) (release.Promotion, error) {
	if err := p.Validate(); err != nil {
		return release.Promotion{}, err
	}
	var out release.Promotion
	err := s.withLock(func() error {
		history, err := s.promotionsLocked(p.Env)
		if err != nil {
			return err
		}
		floors, err := s.floorsLocked(p.Env)
		if err != nil {
			return err
		}
		existing, err := admit(history, floors, p)
		if err != nil {
			return err
		}
		if existing != nil {
			out = *existing
		} else {
			p.ID = newID()
			p.PromotedAt = time.Now().UTC().Truncate(time.Second)
			out = p
			if err := appendRecord(s.envPath("promotions", p.Env), p); err != nil {
				return err
			}
		}
		if raise == nil {
			return nil
		}
		if raised := raise(floors, out); len(raised) > 0 {
			_, err = s.recordFloorChangeLocked(p.Env, FloorChange{Floors: raised})
		}
		return err
	})
	if err != nil {
		return release.Promotion{}, err
	}
	return out, nil
}
