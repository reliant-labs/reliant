package approval

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Store is the Postgres [Approvals] over the approvals table ([SchemaSQL]).
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
type Store struct {
	db  DB
	cfg config
}

var _ Approvals = (*Store)(nil)

// NewStore returns a Store over db. Pass the project's *orm.Client: Decide
// and Redrive open their transaction through it, and the follow-up's own
// writes join that transaction when they go through the same client with the
// ctx the follow-up receives.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func NewStore(db DB, opts ...StoreOption) *Store {
	return &Store{db: db, cfg: newConfig(opts)}
}

const itemColumns = `id, scope, kind, subject_ref, run_id, payload, summary, state, expires_at,
	decided_by, decided_at, note, outcome, handed_off, created_at, updated_at`

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Item, error) {
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("approval store: query: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var items []Item
	for rows.Next() {
		var (
			it      Item
			state   string
			payload []byte
			decided sql.NullTime
		)
		if err := rows.Scan(&it.ID, &it.Scope, &it.Kind, &it.SubjectRef, &it.RunID, &payload, &it.Summary,
			&state, &it.ExpiresAt, &it.DecidedBy, &decided, &it.Note, &it.Outcome, &it.HandedOff,
			&it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, fmt.Errorf("approval store: scan: %w", err)
		}
		it.State = State(state)
		it.Payload = payload
		if decided.Valid {
			t := decided.Time
			it.DecidedAt = &t
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("approval store: rows: %w", err)
	}
	return items, nil
}

func (s *Store) exec(ctx context.Context, q string, args ...any) error {
	if _, err := s.db.Exec(ctx, q, args...); err != nil {
		return fmt.Errorf("approval store: exec: %w", err)
	}
	return nil
}

// Propose records a pending item and returns it. It is idempotent on
// (Scope, Kind, SubjectRef): proposing a subject that already has an item,
// in any state, returns that item unchanged, so a retried agent run
// proposes once.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (s *Store) Propose(ctx context.Context, it Item) (Item, error) {
	it, err := validateProposal(it)
	if err != nil {
		return Item{}, err
	}
	now := s.cfg.now()
	ins, err := s.query(ctx, `INSERT INTO `+s.cfg.table+` (scope, kind, subject_ref, run_id, payload, summary,
		expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $8)
		ON CONFLICT (scope, kind, subject_ref) DO NOTHING
		RETURNING `+itemColumns,
		it.Scope, it.Kind, it.SubjectRef, it.RunID, string(it.Payload), it.Summary, it.ExpiresAt, now)
	if err != nil {
		return Item{}, err
	}
	if len(ins) == 1 {
		return ins[0], nil
	}
	existing, err := s.query(ctx, `SELECT `+itemColumns+` FROM `+s.cfg.table+
		` WHERE scope = $1 AND kind = $2 AND subject_ref = $3`, it.Scope, it.Kind, it.SubjectRef)
	if err != nil {
		return Item{}, err
	}
	if len(existing) == 0 {
		return Item{}, svcerr.Aborted("approval changed while it was being proposed; retry")
	}
	return existing[0], nil
}

// Get returns one item, or a NotFound error.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (s *Store) Get(ctx context.Context, id string) (Item, error) {
	if !isUUID(id) {
		return Item{}, svcerr.NotFound("approval")
	}
	got, err := s.query(ctx, `SELECT `+itemColumns+` FROM `+s.cfg.table+` WHERE id = $1`, id)
	if err != nil {
		return Item{}, err
	}
	if len(got) == 0 {
		return Item{}, svcerr.NotFound("approval")
	}
	return got[0], nil
}

// clauses turns a Filter into WHERE conditions and their args, numbering
// placeholders from len(args)+1.
func clauses(f Filter, args []any) ([]string, []any) {
	var where []string
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	if len(f.IDs) > 0 {
		ids := make([]string, 0, len(f.IDs))
		for _, id := range f.IDs {
			if isUUID(id) {
				ids = append(ids, id)
			}
		}
		add("id = ANY($%d::uuid[])", ids)
	}
	for _, c := range []struct{ col, v string }{
		{"scope", f.Scope}, {"kind", f.Kind}, {"subject_ref", f.SubjectRef}, {"run_id", f.RunID},
	} {
		if c.v != "" {
			add(c.col+" = $%d", c.v)
		}
	}
	if len(f.States) > 0 {
		states := make([]string, len(f.States))
		for i, st := range f.States {
			states[i] = string(st)
		}
		add("state = ANY($%d)", states)
	}
	return where, args
}

// List returns the items matching f, newest first. Paging is keyset on
// (created_at, id), so a page never repeats or skips a row as items arrive.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (s *Store) List(ctx context.Context, f Filter) (Page, error) {
	where, args := clauses(f, nil)
	if f.PageToken != "" {
		at, id, err := decodePageToken(f.PageToken)
		if err != nil {
			return Page{}, err
		}
		if !isUUID(id) {
			return Page{}, ErrBadPageToken
		}
		args = append(args, at, id)
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d::uuid)", len(args)-1, len(args)))
	}
	size := pageSize(f.PageSize)
	args = append(args, size+1)
	q := `SELECT ` + itemColumns + ` FROM ` + s.cfg.table
	if len(where) > 0 {
		q += ` WHERE ` + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT $%d`, len(args))
	items, err := s.query(ctx, q, args...)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: items}
	if len(items) > size {
		page.Items = items[:size]
		last := page.Items[size-1]
		page.NextPageToken = encodePageToken(last.CreatedAt, last.ID)
	}
	return page, nil
}

// ExpireDue moves every pending item whose deadline has passed (ExpiresAt <=
// now) to expired, in one UPDATE ... RETURNING, and returns them. An empty
// scope covers every scope. Call it lazily on read paths, or from a sweeper
// (see the package doc); both are safe at any frequency.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (s *Store) ExpireDue(ctx context.Context, scope string) ([]Item, error) {
	now := s.cfg.now()
	args := []any{now}
	q := `UPDATE ` + s.cfg.table + ` SET state = 'expired', decided_at = $1, updated_at = $1
		WHERE state = 'pending' AND expires_at <= $1`
	if scope != "" {
		args = append(args, scope)
		q += ` AND scope = $2`
	}
	return s.query(ctx, q+` RETURNING `+itemColumns, args...)
}

// Cancel withdraws the pending items matching f (an account retired, a draft
// deleted) and returns how many it withdrew. note is recorded on each. f
// must name at least one of IDs, Scope, Kind, SubjectRef or RunID; States
// is ignored, because only a pending item can be withdrawn.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (s *Store) Cancel(ctx context.Context, f Filter, note string) (int, error) {
	if err := validateCancel(f); err != nil {
		return 0, err
	}
	now := s.cfg.now()
	f.States = nil
	where, args := clauses(f, []any{now, note})
	where = append(where, "state = 'pending'")
	res, err := s.db.Exec(ctx, `UPDATE `+s.cfg.table+` SET state = 'cancelled', note = $2, decided_at = $1,
		updated_at = $1 WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return 0, fmt.Errorf("approval store: cancel: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("approval store: cancel: %w", err)
	}
	return int(n), nil
}

// Decide records a person's verdict and, for an approval, runs fn.
//
//   - An item past its deadline is expired first (DecidedBy and Note record
//     who tried) and returned in state expired, with a nil error; fn does not
//     run. The same goes for an item a sweeper already expired.
//   - Otherwise the item moves from pending to approved or rejected with one
//     conditional UPDATE. Of two concurrent deciders exactly one wins; the
//     other gets a FailedPrecondition error. Deciding an item that is already
//     decided or cancelled is a FailedPrecondition error too, and an unknown
//     id is NotFound.
//   - For an approval, fn then runs in the same transaction (see [FollowUp]).
//     Its error rolls everything back, leaving the item pending; a
//     [*Rejection] commits the item as rejected; [HandoffIncomplete] commits
//     it approved and not handed off. A nil fn means there is nothing to
//     hand off.
//   - A rejection runs no follow-up: a person saying no must never be
//     blocked by the app's hook.
//
// Decide does not check who is calling. The rpc that calls it is declared
// human_only and passes By.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (s *Store) Decide(ctx context.Context, d Decision, fn FollowUp) (Item, error) {
	if err := validateDecision(d); err != nil {
		return Item{}, err
	}
	if !isUUID(d.ID) {
		return Item{}, svcerr.NotFound("approval")
	}
	var out Item
	err := s.db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		it, err := s.decideTx(ctx, d, fn)
		if err != nil {
			return err
		}
		out = it
		return nil
	})
	if err != nil {
		return Item{}, err
	}
	return out, nil
}

func (s *Store) decideTx(ctx context.Context, d Decision, fn FollowUp) (Item, error) {
	now := s.cfg.now()
	t := s.cfg.table

	late, err := s.query(ctx, `UPDATE `+t+` SET state = 'expired', decided_by = $2, decided_at = $3,
		note = $4, updated_at = $3
		WHERE id = $1 AND state = 'pending' AND expires_at <= $3
		RETURNING `+itemColumns, d.ID, d.By, now, d.Note)
	if err != nil {
		return Item{}, err
	}
	if len(late) == 1 {
		return late[0], nil
	}

	next := StateRejected
	if d.Approve {
		next = StateApproved
	}
	decided, err := s.query(ctx, `UPDATE `+t+` SET state = $2, decided_by = $3, decided_at = $4, note = $5,
		updated_at = $4, handed_off = $6
		WHERE id = $1 AND state = 'pending' AND expires_at > $4
		RETURNING `+itemColumns, d.ID, string(next), d.By, now, d.Note, !d.Approve || fn == nil)
	if err != nil {
		return Item{}, err
	}
	if len(decided) == 0 {
		return s.undecided(ctx, d, now)
	}
	if !d.Approve || fn == nil {
		return decided[0], nil
	}
	return s.runFollowUp(ctx, decided[0], d, fn)
}

// undecided explains why the conditional transition matched nothing.
func (s *Store) undecided(ctx context.Context, d Decision, now time.Time) (Item, error) {
	cur, err := s.Get(ctx, d.ID)
	if err != nil {
		return Item{}, err
	}
	if cur.State != StateExpired {
		return Item{}, svcerr.FailedPrecondition(decidedMessage(cur.State))
	}
	if cur.DecidedBy != "" {
		return cur, nil
	}
	// A sweeper expired it before anyone tried; record the first late decider.
	marked, err := s.query(ctx, `UPDATE `+s.cfg.table+` SET decided_by = $2, note = $3, updated_at = $4
		WHERE id = $1 AND state = 'expired' AND decided_by = ''
		RETURNING `+itemColumns, d.ID, d.By, d.Note, now)
	if err != nil {
		return Item{}, err
	}
	if len(marked) == 1 {
		return marked[0], nil
	}
	return s.Get(ctx, d.ID)
}

// runFollowUp runs fn for an approved, not-yet-handed-off item inside the
// caller's transaction and records its result. A savepoint lets a Rejection
// discard fn's writes while the rejection itself commits.
func (s *Store) runFollowUp(ctx context.Context, it Item, d Decision, fn FollowUp) (Item, error) {
	const sp = "approval_followup"
	if err := s.exec(ctx, "SAVEPOINT "+sp); err != nil {
		return Item{}, err
	}
	outcome, err := fn(ctx, it, d)
	now := s.cfg.now()

	var rej *Rejection
	var hand *HandoffError
	switch {
	case err == nil:
		if err := s.exec(ctx, "RELEASE SAVEPOINT "+sp); err != nil {
			return Item{}, err
		}
		return s.settle(ctx, `UPDATE `+s.cfg.table+` SET handed_off = true, outcome = $2, updated_at = $3
			WHERE id = $1 AND state = 'approved' RETURNING `+itemColumns, it.ID, outcome, now)
	case errors.As(err, &rej):
		if err := s.exec(ctx, "ROLLBACK TO SAVEPOINT "+sp); err != nil {
			return Item{}, err
		}
		return s.settle(ctx, `UPDATE `+s.cfg.table+` SET state = 'rejected', handed_off = true, outcome = $2,
			updated_at = $3 WHERE id = $1 AND state = 'approved' RETURNING `+itemColumns, it.ID, rej.Reason, now)
	case errors.As(err, &hand):
		if err := s.exec(ctx, "RELEASE SAVEPOINT "+sp); err != nil {
			return Item{}, err
		}
		return s.settle(ctx, `UPDATE `+s.cfg.table+` SET outcome = $2, updated_at = $3
			WHERE id = $1 AND state = 'approved' RETURNING `+itemColumns, it.ID, outcome, now)
	default:
		return Item{}, err
	}
}

func (s *Store) settle(ctx context.Context, q string, args ...any) (Item, error) {
	got, err := s.query(ctx, q, args...)
	if err != nil {
		return Item{}, err
	}
	if len(got) != 1 {
		return Item{}, svcerr.Aborted("approval changed while its follow-up ran; retry")
	}
	return got[0], nil
}

// Redrive runs fn again for approved items whose hand-off never completed
// (state approved and HandedOff false), oldest first, at most f.PageSize per
// call, each in its own transaction. Only the Filter's identifying fields
// (IDs, Scope, Kind, SubjectRef, RunID) apply; States does not.
//
// It returns the items whose follow-up completed, or that a Rejection turned
// into rejected. An item whose follow-up failed or reported
// [HandoffIncomplete] stays as it was, to be tried on the next call; failures
// are returned joined. Several redrivers may run at once: each item is
// locked while it is worked, and a redriver that reaches an item another
// holds waits for it, then re-checks it. So when Redrive returns no error,
// every item it selected was handed off (by this call or another) or was
// tried by this call; none is passed over because someone else held it.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (s *Store) Redrive(ctx context.Context, f Filter, fn FollowUp) ([]Item, error) {
	if fn == nil {
		return nil, invalid("redrive needs a follow-up")
	}
	f.States = nil
	where, args := clauses(f, nil)
	where = append(where, "state = 'approved'", "NOT handed_off")
	args = append(args, pageSize(f.PageSize))
	rows, err := s.query(ctx, `SELECT `+itemColumns+` FROM `+s.cfg.table+` WHERE `+strings.Join(where, " AND ")+
		fmt.Sprintf(` ORDER BY updated_at, id LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, err
	}

	var done []Item
	var errs []error
	for _, cand := range rows {
		var out Item
		var worked bool
		err := s.db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
			// FOR UPDATE (not SKIP LOCKED) waits out a concurrent redriver;
			// under READ COMMITTED the WHERE is then re-evaluated against the
			// committed row, so an item it handed off matches nothing and one
			// it left incomplete is tried again here.
			locked, err := s.query(ctx, `SELECT `+itemColumns+` FROM `+s.cfg.table+
				` WHERE id = $1 AND state = 'approved' AND NOT handed_off FOR UPDATE`, cand.ID)
			if err != nil || len(locked) == 0 {
				return err
			}
			it := locked[0]
			res, err := s.runFollowUp(ctx, it, Decision{ID: it.ID, Approve: true, By: it.DecidedBy, Note: it.Note}, fn)
			if err != nil {
				return err
			}
			out, worked = res, res.HandedOff
			return nil
		})
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("approval %s: %w", cand.ID, err))
		case worked:
			done = append(done, out)
		}
	}
	return done, errors.Join(errs...)
}

func invalid(msg string) error { return svcerr.InvalidArgument(msg) }

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
