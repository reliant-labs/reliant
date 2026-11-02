package ledgerfile

// The remaining record types. Each is the file half of the same record the
// hosted ledger holds, written as the same canonical JSON, so a line here is
// what ImportLedger accepts.

import (
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

const (
	envsFile          = "envs.jsonl"
	bundlesFile       = "bundles.jsonl"
	sessionsFile      = "sessions.jsonl"
	verificationsFile = "verifications.jsonl"
)

// ─── Environments ────────────────────────────────────────────────────────────

// PutEnv records an env's declaration. Append-only: the NEWEST line per env
// wins, so a re-declaration from a later render supersedes an earlier one
// without rewriting it. That keeps "what did this env's render say, and
// when" answerable, which is the whole reason the declaration is recorded
// rather than recomputed.
func (s *Store) PutEnv(r release.EnvRecord) error {
	if err := r.Validate(); err != nil {
		return err
	}
	return s.withLock(func() error { return appendRecord(s.path(envsFile), r) })
}

// Env returns the newest declaration for one env, or (nil, nil) when it was
// never declared.
func (s *Store) Env(name string) (*release.EnvRecord, error) {
	all, err := s.Envs()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, nil
}

// Envs returns the newest declaration per env.
func (s *Store) Envs() ([]release.EnvRecord, error) {
	var out []release.EnvRecord
	err := s.withLock(func() error {
		all, err := decodeAll(s.path(envsFile), release.EnvRecord.Validate)
		if err != nil {
			return err
		}
		newest := map[string]release.EnvRecord{}
		var order []string
		for _, r := range all {
			if _, seen := newest[r.Name]; !seen {
				order = append(order, r.Name)
			}
			newest[r.Name] = r
		}
		for _, n := range order {
			out = append(out, newest[n])
		}
		return nil
	})
	return out, err
}

// ─── Bundles ─────────────────────────────────────────────────────────────────

// RecordBundle records a bundle. Idempotent on (env, digest): a bundle IS
// its content, so re-recording the same digest for the same env is a retry
// and returns the record already held. That is what makes a push whose
// RecordBundle failed safely re-runnable (failure mode F-3).
func (s *Store) RecordBundle(b release.BundleRecord) (release.BundleRecord, bool, error) {
	switch {
	case b.Env == "":
		return release.BundleRecord{}, false, fmt.Errorf("bundle: an environment is required")
	case b.Digest == "":
		return release.BundleRecord{}, false, fmt.Errorf("bundle: a digest is required — a bundle is identified by its content")
	}
	if err := b.Shape.Validate(); err != nil {
		return release.BundleRecord{}, false, err
	}
	var (
		out     release.BundleRecord
		created bool
	)
	err := s.withLock(func() error {
		all, err := decodeAll[release.BundleRecord](s.path(bundlesFile), nil)
		if err != nil {
			return err
		}
		for _, existing := range all {
			if existing.Env == b.Env && existing.Digest == b.Digest {
				out = existing
				return nil
			}
		}
		if b.ID == "" {
			b.ID = newID()
		}
		if b.CreatedAt.IsZero() {
			b.CreatedAt = time.Now().UTC().Truncate(time.Second)
		}
		out, created = b, true
		return appendRecord(s.path(bundlesFile), b)
	})
	if err != nil {
		return release.BundleRecord{}, false, err
	}
	return out, created, nil
}

// Bundle returns one bundle by id, or (nil, nil) when it is unknown.
func (s *Store) Bundle(id string) (*release.BundleRecord, error) {
	var out *release.BundleRecord
	err := s.withLock(func() error {
		all, err := decodeAll[release.BundleRecord](s.path(bundlesFile), nil)
		if err != nil {
			return err
		}
		for i := range all {
			if all[i].ID == id {
				out = &all[i]
			}
		}
		return nil
	})
	return out, err
}

// Bundles returns every bundle for one env, oldest first.
func (s *Store) Bundles(env string) ([]release.BundleRecord, error) {
	var out []release.BundleRecord
	err := s.withLock(func() error {
		all, err := decodeAll[release.BundleRecord](s.path(bundlesFile), nil)
		if err != nil {
			return err
		}
		for _, b := range all {
			if b.Env == env {
				out = append(out, b)
			}
		}
		return nil
	})
	return out, err
}

// OCIDir is where bundle blobs live: an OCI image layout, content-addressed.
// internal/bundle owns its contents; this package owns only its location, so
// the two halves of a bundle (the record and the blob) sit together.
func (s *Store) OCIDir() string { return s.path("oci") }

// ─── Applies ─────────────────────────────────────────────────────────────────

// applyLine holds an Apply and, once it finishes, its outcome. The outcome
// is a SEPARATE appended line joined by apply id — never an edit of the
// apply's line, because the file is append-only and an apply that was
// started is a fact that stays true whatever its outcome.
type applyLine struct {
	Apply   *release.Apply        `json:"apply,omitempty"`
	Outcome *release.ApplyOutcome `json:"outcome,omitempty"`
	ApplyID string                `json:"apply_id,omitempty"`
}

// ApplyRecord is one apply joined to its outcome. A nil Outcome means the
// apply has not reported: past its deadline that reads as ABANDONED
// (release.DeriveApplyState), which is deliberately distinct from failed —
// "we could not look" is its own answer, and a stored "assume success" would
// be a lie (failure mode F-2).
type ApplyRecord struct {
	Apply   release.Apply
	Outcome *release.ApplyOutcome
}

// BeginApply records the start of an apply.
//
// It REFUSES when another apply for the same env is still in flight, unless
// supersede is set — the same semantics the hosted ledger enforces under its
// env row lock (failure mode F-5). Two appliers racing one env is the case
// the lock exists for.
func (s *Store) BeginApply(a release.Apply, supersede bool) (release.Apply, error) {
	if a.ID == "" {
		a.ID = newID()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	if err := a.Validate(); err != nil {
		return release.Apply{}, err
	}
	var out release.Apply
	err := s.withLock(func() error {
		existing, err := s.appliesLocked(a.Env)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		for _, r := range existing {
			if !release.DeriveApplyState(r.Apply, r.Outcome, now).InFlight() {
				continue
			}
			if !supersede {
				return fmt.Errorf("apply %s on %s has not finished (deadline %s).\n"+
					"  Wait for it, or override with --supersede (which is recorded on the new apply)",
					r.Apply.ID, a.Env, r.Apply.DeadlineAt.Format(time.RFC3339))
			}
			a.SupersededInFlight = true
		}
		out = a
		return appendRecord(s.envPath("applies", a.Env), applyLine{Apply: &a})
	})
	if err != nil {
		return release.Apply{}, err
	}
	return out, nil
}

// FinishApply records an apply's outcome. Idempotent: re-reporting the SAME
// outcome is a retry (a lost response, a re-run), while a DIFFERENT outcome
// for an apply that already reported is refused — an apply ended once, and
// two answers would make the record meaningless.
func (s *Store) FinishApply(env string, o release.ApplyOutcome) error {
	if err := o.Validate(); err != nil {
		return err
	}
	if o.ApplyID == "" {
		return fmt.Errorf("apply outcome: an apply id is required to join the outcome to its apply")
	}
	return s.withLock(func() error {
		all, err := s.appliesLocked(env)
		if err != nil {
			return err
		}
		for _, r := range all {
			if r.Apply.ID != o.ApplyID {
				continue
			}
			if r.Outcome == nil {
				return appendRecord(s.envPath("applies", env), applyLine{Outcome: &o, ApplyID: o.ApplyID})
			}
			if r.Outcome.SameReport(o) {
				return nil
			}
			return fmt.Errorf("apply %s already reported %q; it cannot also have ended %q",
				o.ApplyID, r.Outcome.Status, o.Status)
		}
		return fmt.Errorf("apply %s is not recorded for %s", o.ApplyID, env)
	})
}

// Applies returns env's applies, each joined to its outcome, OLDEST FIRST.
func (s *Store) Applies(env string) ([]ApplyRecord, error) {
	var out []ApplyRecord
	err := s.withLock(func() error {
		var err error
		out, err = s.appliesLocked(env)
		return err
	})
	return out, err
}

func (s *Store) appliesLocked(env string) ([]ApplyRecord, error) {
	path := s.envPath("applies", env)
	lines, err := decodeAll[applyLine](path, nil)
	if err != nil {
		return nil, err
	}
	var out []ApplyRecord
	index := map[string]int{}
	for i, l := range lines {
		switch {
		case l.Apply != nil:
			index[l.Apply.ID] = len(out)
			out = append(out, ApplyRecord{Apply: *l.Apply})
		case l.Outcome != nil:
			at, ok := index[l.ApplyID]
			if !ok {
				return nil, fmt.Errorf("%s line %d: outcome for apply %q, which the file does not record",
					path, i+1, l.ApplyID)
			}
			outcome := *l.Outcome
			out[at].Outcome = &outcome
		default:
			return nil, fmt.Errorf("%s line %d: neither an apply nor an outcome", path, i+1)
		}
	}
	return out, nil
}

// ─── Local sessions ──────────────────────────────────────────────────────────

// ReportSession records or refreshes a local-stack presence row.
//
// THE ONLY COMPACTED FILE. Sessions are heartbeats — every 60s while a stack
// supervises — so appending each one would grow without bound and say
// nothing: the useful fact is the CURRENT state of each session, not every
// time it was seen. So a report replaces the row for its (env, host,
// worktree) identity, written through a temp file and renamed, under the
// lock. Presence is observation, never a promotion and never a deploy target
// (owner decision O-8).
func (s *Store) ReportSession(sess release.LocalSession) error {
	if err := sess.Validate(); err != nil {
		return err
	}
	return s.withLock(func() error {
		all, err := decodeAll[release.LocalSession](s.path(sessionsFile), nil)
		if err != nil {
			return err
		}
		key := func(x release.LocalSession) string {
			return x.Env + "\x00" + x.Worktree.Host + "\x00" + x.Worktree.Key
		}
		replaced := false
		records := make([]any, 0, len(all)+1)
		for _, existing := range all {
			if key(existing) == key(sess) {
				records = append(records, sess)
				replaced = true
				continue
			}
			records = append(records, existing)
		}
		if !replaced {
			records = append(records, sess)
		}
		return rewriteLines(s.path(sessionsFile), records)
	})
}

// Sessions returns every recorded session. A reader decides which are live
// (release.LocalSession.Live) and which have gone quiet (Stale) — the store
// does not filter, because "no sessions" and "sessions I decided not to
// show" must not render alike.
func (s *Store) Sessions() ([]release.LocalSession, error) {
	var out []release.LocalSession
	err := s.withLock(func() error {
		var err error
		out, err = decodeAll[release.LocalSession](s.path(sessionsFile), nil)
		return err
	})
	return out, err
}

// SessionsFor returns the sessions of one env.
func (s *Store) SessionsFor(env string) ([]release.LocalSession, error) {
	all, err := s.Sessions()
	if err != nil {
		return nil, err
	}
	var out []release.LocalSession
	for _, sess := range all {
		if sess.Env == env {
			out = append(out, sess)
		}
	}
	return out, nil
}

// VerificationsPath is where source verifications will be recorded
// (verifications.jsonl, §12.3). The file is part of the documented layout,
// and the records that go in it arrive with protected environments
// (control-plane #516) — until then nothing writes it, and this names the
// location so the layout has one owner rather than two guesses.
func (s *Store) VerificationsPath() string { return s.path(verificationsFile) }
