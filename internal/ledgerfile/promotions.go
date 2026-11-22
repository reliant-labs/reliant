package ledgerfile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Admit decides whether a promotion may be written, given the env's history.
//
// It is supplied BY THE CALLER rather than implemented here, because the
// compare-and-set rules (and the refusal type a refusal must carry, with its
// exit code and --json shape) belong to the command layer that owns them for
// BOTH backends. This package's job is to make the decision under a lock
// against the real history, not to re-decide what a refusal means.
//
// A non-nil first return is an idempotent no-op: that existing entry is the
// answer and nothing is appended. A non-nil error refuses the write.
type Admit func(history []release.Promotion, p release.Promotion) (*release.Promotion, error)

// Promotions returns env's promotions OLDEST FIRST. A missing log is an
// empty history.
func (s *Store) Promotions(env string) ([]release.Promotion, error) {
	var out []release.Promotion
	err := s.withLock(func() error {
		var err error
		out, err = s.promotionsLocked(env)
		return err
	})
	return out, err
}

func (s *Store) promotionsLocked(env string) ([]release.Promotion, error) {
	path := s.envPath("promotions", env)
	history, err := decodeAll(path, release.Promotion.Validate)
	if err != nil {
		return nil, err
	}
	for i, p := range history {
		if p.Env != env {
			return nil, fmt.Errorf("%s line %d records env %q, not %q", path, i+1, p.Env, env)
		}
	}
	return history, nil
}

// CurrentPromotion is env's newest promotion, and whether it has one.
// "Never promoted" is a normal state, so it is a bool rather than an error —
// distinct from a ledger that could not be read at all.
func (s *Store) CurrentPromotion(env string) (release.Promotion, bool, error) {
	history, err := s.Promotions(env)
	if err != nil || len(history) == 0 {
		return release.Promotion{}, false, err
	}
	return history[len(history)-1], true, nil
}

// AppendPromotion records p under admit's rules and returns the entry the
// ledger now holds: the newly appended one, or — for an idempotent retry —
// the EXISTING entry, unchanged.
//
// THE LOCK IS THE POINT. The history is read, the decision is made and the
// line is appended inside ONE critical section, so two concurrent promoters
// cannot both decide from the same history and both append. That is the race
// the retired in-checkout backend documented and declined to close
// ("CONCURRENCY, STATED RATHER THAN PATCHED"); under this lock, N concurrent
// appenders of the same move produce exactly one line, and the losers each
// get back the winner's entry.
func (s *Store) AppendPromotion(p release.Promotion, admit Admit) (release.Promotion, error) {
	if err := p.Validate(); err != nil {
		return release.Promotion{}, err
	}
	var out release.Promotion
	err := s.withLock(func() error {
		history, err := s.promotionsLocked(p.Env)
		if err != nil {
			return err
		}
		if admit != nil {
			existing, err := admit(history, p)
			if err != nil {
				return err
			}
			if existing != nil {
				out = *existing
				return nil
			}
		}
		p.ID = newID()
		p.PromotedAt = time.Now().UTC().Truncate(time.Second)
		out = p
		return appendRecord(s.envPath("promotions", p.Env), p)
	})
	if err != nil {
		return release.Promotion{}, err
	}
	return out, nil
}

// ImportPromotion records a promotion from another ledger VERBATIM: its id,
// its timestamp and its gates exactly as they were.
//
// IT IS NOT AppendPromotion, and the difference is the point. A promote
// DECIDES — release.Decide collapses a re-promote of the current release to a
// no-op, the CAS refuses a stale write, and the backend stamps a fresh id and
// time. Importing history must do none of that: a ledger's past contains
// consecutive promotions of one release, promotions made from a state this
// machine never saw, and ids other systems already reference. Running them
// through the promote path would silently drop lines (every idempotent-looking
// pair) and renumber the rest.
//
// Preserving the id is also what lets the import be re-run: the
// unimported-checkout check in internal/cli matches on exactly this id, so a
// resumed import converges instead of duplicating.
func (s *Store) ImportPromotion(p release.Promotion) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.ID == "" {
		return fmt.Errorf("importing a promotion of %s: the source id is required, "+
			"because an import is identified by the record it came from", p.Env)
	}
	return s.withLock(func() error {
		history, err := s.promotionsLocked(p.Env)
		if err != nil {
			return err
		}
		// Idempotent on the SOURCE id, so re-running an import that was
		// interrupted neither duplicates a line nor refuses.
		for _, existing := range history {
			if existing.ID == p.ID {
				return nil
			}
		}
		return appendRecord(s.envPath("promotions", p.Env), p)
	})
}

// AppendGates records post-promote evidence for env.
//
// THE OLD FILE LEDGER COULD NOT HOLD THIS AT ALL. A promotion line is
// already written when the wait, the smoke test or the manual sign-off
// finishes, and an append-only line cannot be edited — so release.Promotion
// documents that "a file ledger leaves RecordedGates empty". A separate
// per-env gates file fixes that without rewriting history: the evidence is
// joined to its promotion by id at read time.
func (s *Store) AppendGates(env, promotionID string, gates []release.Gate) error {
	if promotionID == "" {
		return fmt.Errorf("recording gates for %s: a promotion id is required to join the evidence to its promotion", env)
	}
	for _, g := range gates {
		if err := g.Validate(); err != nil {
			return err
		}
	}
	return s.withLock(func() error {
		for _, g := range gates {
			if err := appendRecord(s.envPath("gates", env), gateLine{PromotionID: promotionID, Gate: g}); err != nil {
				return err
			}
		}
		return nil
	})
}

// Gates returns the recorded gates for one promotion.
func (s *Store) Gates(env, promotionID string) ([]release.Gate, error) {
	var out []release.Gate
	err := s.withLock(func() error {
		lines, err := decodeAll[gateLine](s.envPath("gates", env), nil)
		if err != nil {
			return err
		}
		for _, l := range lines {
			if l.PromotionID == promotionID {
				out = append(out, l.Gate)
			}
		}
		return nil
	})
	return out, err
}

// gateLine is a gate plus the promotion it belongs to.
//
// The two are FLATTENED into one object — a Gate's own fields plus
// promotion_id — rather than nested under a wrapper, because that keeps the
// line importable as a gate: ImportLedger reads the gate fields and ignores
// the join key, instead of having to know about a forge-only envelope.
//
// It cannot be done by embedding release.Gate. An embedded type's
// UnmarshalJSON is promoted to the outer struct, so gateLine would decode
// through Gate's custom unmarshaler and silently never see promotion_id.
// Hence the explicit two-pass encode/decode below.
type gateLine struct {
	PromotionID string
	Gate        release.Gate
}

// MarshalJSON writes the gate's fields with promotion_id added.
func (l gateLine) MarshalJSON() ([]byte, error) {
	gate, err := canonicalJSON(l.Gate)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(gate, &fields); err != nil {
		return nil, fmt.Errorf("encode gate line: %w", err)
	}
	id, err := canonicalJSON(l.PromotionID)
	if err != nil {
		return nil, err
	}
	fields["promotion_id"] = id
	return canonicalJSON(fields)
}

// UnmarshalJSON reads the flattened form back: the join key first, then the
// same bytes through Gate's own unmarshaler.
func (l *gateLine) UnmarshalJSON(data []byte) error {
	var key struct {
		PromotionID string `json:"promotion_id"`
	}
	if err := json.Unmarshal(data, &key); err != nil {
		return fmt.Errorf("decode gate line: %w", err)
	}
	l.PromotionID = key.PromotionID
	return json.Unmarshal(data, &l.Gate)
}

// newID is a sortable-enough unique identifier for a file-ledger record: a
// UTC timestamp (so `sort` on the file is chronological) plus 8 random
// bytes. The hosted ledger assigns its own ids; this only has to be unique
// within one machine's ledger.
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}
