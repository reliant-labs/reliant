package approval

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
)

// State is where an [Item] is in its life. Pending is the only state that
// changes: every other state is final, except that an approved item whose
// hand-off has not completed ([Item.HandedOff] false) is re-run by Redrive.
type State string

const (
	StatePending   State = "pending"
	StateApproved  State = "approved"
	StateRejected  State = "rejected"
	StateExpired   State = "expired"
	StateCancelled State = "cancelled"
)

// SchemaSQL is the canonical approvals table [Store] reads and writes, with
// the indexes its statements use. A project owns the table as one of its
// migrations: copy this DDL into db/migrations. Keep a hand-written table's
// columns to this shape.
//
// expires_at is NOT NULL for every row, so a pending item can never be
// without a deadline.
const SchemaSQL = `CREATE TABLE approvals (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope       text        NOT NULL DEFAULT '',
    kind        text        NOT NULL,
    subject_ref text        NOT NULL,
    run_id      text        NOT NULL DEFAULT '',
    payload     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    summary     text        NOT NULL DEFAULT '',
    state       text        NOT NULL DEFAULT 'pending'
                CHECK (state IN ('pending', 'approved', 'rejected', 'expired', 'cancelled')),
    expires_at  timestamptz NOT NULL,
    decided_by  text        NOT NULL DEFAULT '',
    decided_at  timestamptz,
    note        text        NOT NULL DEFAULT '',
    outcome     text        NOT NULL DEFAULT '',
    handed_off  boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT approvals_subject_key UNIQUE (scope, kind, subject_ref)
);

CREATE INDEX approvals_scope_created_idx ON approvals (scope, created_at DESC, id DESC);
CREATE INDEX approvals_pending_expiry_idx ON approvals (expires_at) WHERE state = 'pending';
CREATE INDEX approvals_redrive_idx ON approvals (updated_at, id) WHERE state = 'approved' AND NOT handed_off;
CREATE INDEX approvals_run_idx ON approvals (run_id) WHERE run_id <> '';
`

// Item is one thing an agent proposed and a person has yet to decide.
type Item struct {
	ID    string
	Scope string // tenant, account or whatever partitions the app; "" if none
	// Kind is the app's label for what is being approved: "trade",
	// "publish_variant".
	Kind string
	// SubjectRef names the app's own row this approval is about
	// ("decisions/01H..."). With Scope and Kind it makes Propose idempotent.
	SubjectRef string
	RunID      string // the agent run that proposed it; "" if none
	// Payload is opaque JSON shown to the approver. It is never interpreted.
	Payload []byte
	Summary string // one line for lists and notifications
	State   State
	// ExpiresAt is the deadline after which nobody can approve the item. It is
	// required: a pending item with no deadline waits forever.
	ExpiresAt time.Time
	DecidedBy string
	DecidedAt *time.Time
	Note      string
	// Outcome is what the follow-up reported ("filled", "rejected on
	// re-validation: price moved") or, for a cancelled item, why.
	Outcome string
	// HandedOff reports that the follow-up completed. An approved item with
	// HandedOff false is one Redrive will run again.
	HandedOff bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Decision is a person's verdict on one item.
type Decision struct {
	ID      string
	Approve bool
	// By is who decided. The package does not check it: the rpc that calls
	// Decide is declared human_only and passes the name it extracted.
	By   string
	Note string
}

// FollowUp is the app's work once a person decides: re-validate and execute
// (trader), or start the next workflow (growth). It receives the item as it
// stands after the transition, and returns what to record as [Item.Outcome].
//
// With a store on *orm.Client it runs in the same transaction as the state
// change, so a returned error rolls the transition back and leaves the item
// pending. Two return values are not errors in that sense:
//
//   - a [*Rejection] turns an approval into a recorded rejection (the
//     re-check refused it). Writes the follow-up made in the transaction are
//     discarded; the rejection itself is committed.
//   - a [HandoffIncomplete] error commits the approval but leaves
//     [Item.HandedOff] false, so Redrive runs the follow-up again. Use it
//     when the work has a side effect outside the database that failed.
//
// A follow-up must be safe to run again: Redrive re-runs it, and a crash
// after an external effect but before commit does too. Derive any external
// idempotency key from [Item.ID].
type FollowUp func(ctx context.Context, it Item, d Decision) (outcome string, err error)

// Rejection is the error a [FollowUp] returns to say "approved, but I
// refuse it": the item becomes rejected and Reason becomes its Outcome.
type Rejection struct{ Reason string }

func (r *Rejection) Error() string { return "approval refused on re-check: " + r.Reason }

// HandoffError is the error [HandoffIncomplete] builds.
type HandoffError struct{ Cause error }

func (e *HandoffError) Error() string {
	if e.Cause == nil {
		return "approval hand-off incomplete"
	}
	return "approval hand-off incomplete: " + e.Cause.Error()
}

func (e *HandoffError) Unwrap() error { return e.Cause }

// HandoffIncomplete is what a [FollowUp] returns when the approval stands
// but its external side effect did not complete. The decision commits with
// HandedOff false and Decide returns nil; the cause is yours to log, since
// the follow-up is the code that saw it. An approval only: for a rejection
// it is an ordinary error.
func HandoffIncomplete(cause error) error { return &HandoffError{Cause: cause} }

// Filter selects items for List, Cancel and Redrive. Set fields are
// AND-combined; an empty field does not filter.
type Filter struct {
	IDs        []string
	Scope      string
	Kind       string
	SubjectRef string
	RunID      string
	States     []State
	// PageSize bounds List (default [DefaultPageSize], at most [MaxPageSize])
	// and Redrive (the number of items attempted per call).
	PageSize int
	// PageToken continues a List from a previous page's NextPageToken.
	PageToken string
}

// Page sizes for List and Redrive.
const (
	DefaultPageSize = 50
	MaxPageSize     = 500
)

// ErrBadPageToken is returned for a PageToken List did not issue.
var ErrBadPageToken = errors.New("approval: malformed page token")

// Page is one page of items, newest first.
type Page struct {
	Items         []Item
	NextPageToken string
}

// At returns t. It documents that a deadline is a fixed moment.
func At(t time.Time) time.Time { return t }

// Within returns the deadline d after now.
func Within(now time.Time, d time.Duration) time.Time { return now.Add(d) }

// NextOr returns next, or now+fallback when next is zero: trader's "expire at
// the next market close, else in eight hours".
func NextOr(next, now time.Time, fallback time.Duration) time.Time {
	if next.IsZero() {
		return now.Add(fallback)
	}
	return next
}

// DB is the database handle a [Store] runs statements through. *orm.Client
// satisfies it: its RunTx opens the transaction Decide runs in, and a
// statement issued with a ctx carrying that transaction joins it, so the
// follow-up's own writes (through the same client) commit or roll back with
// the decision.
type DB interface {
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}

// StoreOption configures a [Store] or a [MemoryStore].
type StoreOption func(*config)

type config struct {
	table string
	now   func() time.Time
}

const approvalsTable = "approvals"

func newConfig(opts []StoreOption) config {
	c := config{table: approvalsTable, now: time.Now}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// WithSchema names the schema approvals lives in, so every statement says
// "<schema>".approvals rather than resolving the bare name through the
// connection's search_path (see audit.WithSchema). Empty leaves it
// unqualified. A [MemoryStore] ignores it.
func WithSchema(schema string) StoreOption {
	return func(c *config) {
		c.table = approvalsTable
		if schema != "" {
			c.table = `"` + strings.ReplaceAll(schema, `"`, `""`) + `".` + approvalsTable
		}
	}
}

// WithNow sets the clock deadlines and decision times are read from. The
// default is time.Now; tests pass a fake.
func WithNow(now func() time.Time) StoreOption {
	return func(c *config) {
		if now != nil {
			c.now = now
		}
	}
}

func validateProposal(it Item) (Item, error) {
	switch {
	case it.Kind == "":
		return it, invalid("kind is required")
	case it.SubjectRef == "":
		return it, invalid("subject_ref is required")
	case it.ExpiresAt.IsZero():
		return it, invalid("expires_at is required: a pending item must have a deadline")
	}
	if len(it.Payload) == 0 {
		it.Payload = []byte("{}")
	} else if !jsonValid(it.Payload) {
		return it, invalid("payload must be valid JSON")
	}
	return it, nil
}

func validateDecision(d Decision) error {
	switch {
	case d.ID == "":
		return invalid("decision id is required")
	case d.By == "":
		return invalid("decision must record who decided (by)")
	}
	return nil
}

func validateCancel(f Filter) error {
	if len(f.IDs) == 0 && f.Scope == "" && f.Kind == "" && f.SubjectRef == "" && f.RunID == "" {
		return invalid("cancel needs a filter: set ids, scope, kind, subject_ref or run_id")
	}
	return nil
}

func pageSize(n int) int {
	switch {
	case n <= 0:
		return DefaultPageSize
	case n > MaxPageSize:
		return MaxPageSize
	}
	return n
}

func encodePageToken(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodePageToken(tok string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return time.Time{}, "", ErrBadPageToken
	}
	ts, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return time.Time{}, "", ErrBadPageToken
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, "", ErrBadPageToken
	}
	return at, id, nil
}

func decidedMessage(state State) string {
	return fmt.Sprintf("approval is already %s", state)
}
