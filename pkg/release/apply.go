package release

import (
	"fmt"
	"strings"
	"time"
)

// Apply is ONE attempt to make an environment run a bundle — "who tried to
// make it so, from where" — recorded before anything moves. Its outcome is a
// SEPARATE record ([ApplyOutcome]) written at most once, so both halves stay
// append-only and "the apply never reported back" is visible as the absence
// of an outcome rather than as a status somebody had to remember to set.
//
// forge writes applies for the envs it applies (self-managed, and the
// declaration half of a hosted deploy). A converger writes none: a hosted
// promotion's outcome is DERIVED from observation, and a derived timeline is
// never also stored.
type Apply struct {
	ID       string `json:"id"`
	Env      string `json:"env"`
	BundleID string `json:"bundle_id"`
	// PromotionID is the binding this apply realizes; "" only for an
	// unreleased bundle (a dev-shaped "deploy my worktree").
	PromotionID string `json:"promotion_id,omitempty"`
	AppliedBy   string `json:"applied_by,omitempty"`
	Run         Run    `json:"run,omitempty"`
	// PlanDigest is the approved plan this apply realizes (O-13).
	PlanDigest string `json:"plan_digest,omitempty"`
	// AcknowledgedFindings are the stop-class finding codes the approver
	// accepted by name.
	AcknowledgedFindings []string `json:"acknowledged_findings,omitempty"`
	// SupersededInFlight records that this apply was started over one that
	// had not finished — a deliberate override.
	SupersededInFlight bool `json:"superseded_in_flight,omitempty"`
	// DeadlineAt is forge's wait budget. Past it with no outcome, the apply
	// reads as abandoned ([ApplyAbandoned]).
	DeadlineAt   time.Time `json:"deadline_at"`
	ImportedFrom string    `json:"imported_from,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Validate checks an apply before it is recorded.
func (a Apply) Validate() error {
	switch {
	case strings.TrimSpace(a.Env) == "":
		return fmt.Errorf("%w: apply environment is required", ErrInvalid)
	case strings.TrimSpace(a.BundleID) == "":
		return fmt.Errorf("%w: apply bundle is required", ErrInvalid)
	case a.CreatedAt.IsZero() || a.DeadlineAt.IsZero():
		return fmt.Errorf("%w: apply needs a start and a deadline", ErrInvalid)
	case !a.DeadlineAt.After(a.CreatedAt):
		return fmt.Errorf("%w: apply deadline %s is not after its start %s", ErrInvalid, a.DeadlineAt, a.CreatedAt)
	case a.PlanDigest != "" && !ValidDigest(a.PlanDigest):
		return fmt.Errorf("%w: apply plan digest %q is not canonical", ErrInvalid, a.PlanDigest)
	}
	return a.Run.Validate()
}

// ApplyStatus is how an apply ENDED, as its applier reported it. Closed.
type ApplyStatus string

const (
	ApplySucceeded ApplyStatus = "succeeded"
	ApplyFailed    ApplyStatus = "failed"
	// ApplyTimedOut: the applier stopped waiting before the rollout
	// answered. Not a success and not a failure.
	ApplyTimedOut ApplyStatus = "timed_out"
)

// ApplyStatuses is the closed set.
var ApplyStatuses = []ApplyStatus{ApplySucceeded, ApplyFailed, ApplyTimedOut}

// Valid reports whether s is one of [ApplyStatuses].
func (s ApplyStatus) Valid() bool {
	for _, known := range ApplyStatuses {
		if s == known {
			return true
		}
	}
	return false
}

// UnmarshalJSON refuses an empty or unknown status.
func (s *ApplyStatus) UnmarshalJSON(data []byte) error {
	names := make([]string, len(ApplyStatuses))
	for i, v := range ApplyStatuses {
		names[i] = string(v)
	}
	return decodeClosed(data, "apply status", func(v string) bool { return ApplyStatus(v).Valid() }, names, (*string)(s))
}

// ApplyOutcome is the END of an apply, written at most once. It is a REPORT:
// for a cluster the platform does not observe, the applier is the only
// observer there is, and a reader must label it as such ("reported by forge").
// It never feeds billing or policy.
type ApplyOutcome struct {
	ApplyID string      `json:"apply_id"`
	Status  ApplyStatus `json:"status"`
	Summary string      `json:"summary,omitempty"`
	// Workloads is the applier's per-resource rollout result: one entry per
	// resource it waited on.
	Workloads []ApplyWorkload `json:"workloads,omitempty"`
	// ReportedBy is set by the BACKEND from the credential, never by the
	// client.
	ReportedBy string    `json:"reported_by,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

// ApplyWorkload is one resource's rollout result. State is forge's rollout
// vocabulary: ready | failed | timed_out | not_waited.
type ApplyWorkload struct {
	Name    string `json:"name"`
	Cluster string `json:"cluster,omitempty"`
	State   string `json:"state"`
	Detail  string `json:"detail,omitempty"`
}

// Validate checks an outcome before it is recorded.
func (o ApplyOutcome) Validate() error {
	switch {
	case strings.TrimSpace(o.ApplyID) == "":
		return fmt.Errorf("%w: apply outcome names no apply", ErrInvalid)
	case !o.Status.Valid():
		return fmt.Errorf("%w: apply outcome status %q", ErrInvalid, o.Status)
	case o.FinishedAt.IsZero():
		return fmt.Errorf("%w: apply outcome needs a finish time", ErrInvalid)
	}
	return nil
}

// SameReport reports whether two outcomes say the same thing — the
// idempotency rule for a retried report: the same outcome again is a retry,
// a different one is a conflict. Who reported it and when the retry arrived
// do not change what was reported.
func (o ApplyOutcome) SameReport(other ApplyOutcome) bool {
	if o.ApplyID != other.ApplyID || o.Status != other.Status || o.Summary != other.Summary ||
		!o.FinishedAt.Equal(other.FinishedAt) || len(o.Workloads) != len(other.Workloads) {
		return false
	}
	for i := range o.Workloads {
		if o.Workloads[i] != other.Workloads[i] {
			return false
		}
	}
	return true
}

// ApplyState is an apply's state as a READER sees it, DERIVED from the apply,
// its outcome (if any) and the clock. Never stored: "abandoned" in particular
// is a conclusion about the absence of a report, and a stored conclusion
// written by a sweeper could be wrong in a way a derivation cannot.
type ApplyState string

const (
	ApplyRunning   ApplyState = "running"
	ApplyStateOK   ApplyState = "succeeded"
	ApplyStateFail ApplyState = "failed"
	ApplyStateTO   ApplyState = "timed_out"
	// ApplyAbandoned: no outcome arrived by the deadline. "We could not
	// look" is its own answer — never read as success.
	ApplyAbandoned ApplyState = "abandoned"
)

// DeriveApplyState is the one derivation every backend and every reader uses.
func DeriveApplyState(a Apply, outcome *ApplyOutcome, now time.Time) ApplyState {
	if outcome != nil {
		switch outcome.Status {
		case ApplySucceeded:
			return ApplyStateOK
		case ApplyFailed:
			return ApplyStateFail
		case ApplyTimedOut:
			return ApplyStateTO
		}
	}
	if !now.Before(a.DeadlineAt) {
		return ApplyAbandoned
	}
	return ApplyRunning
}

// InFlight reports whether an apply in this state still blocks the next one
// on the same env. An abandoned apply does not: blocking on a report that will
// never come would wedge the env.
func (s ApplyState) InFlight() bool { return s == ApplyRunning }

// ─── Local sessions ──────────────────────────────────────────────────────────

// LocalSession is one `forge env up` stack, on one machine, in one checkout:
// PRESENCE, not ledger. It names no promotion and no release, is mutable
// (heartbeats), is garbage-collected, and nothing authorizes or bills on it.
// It exists so Live can show what is running where without asking a daemon.
type LocalSession struct {
	// ID is minted by forge and stable across heartbeats.
	ID         string     `json:"id"`
	Env        string     `json:"env"`
	Worktree   Worktree   `json:"worktree"`
	Provenance Provenance `json:"provenance"`
	// BundleDigest names the LOCAL bundle the stack runs; the blob never
	// leaves the machine.
	BundleDigest string     `json:"bundle_digest,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	LastSeenAt   time.Time  `json:"last_seen_at"`
	StoppedAt    *time.Time `json:"stopped_at,omitempty"`
}

// SessionStaleAfter is how long a live session may go unseen before a reader
// greys it out. Heartbeats are every 60s.
const SessionStaleAfter = 3 * time.Minute

// Live reports whether the session has not been stopped.
func (s LocalSession) Live() bool { return s.StoppedAt == nil }

// Stale reports whether a live session has gone quiet.
func (s LocalSession) Stale(now time.Time) bool {
	return s.Live() && now.Sub(s.LastSeenAt) > SessionStaleAfter
}

// Validate checks a session report.
func (s LocalSession) Validate() error {
	switch {
	case strings.TrimSpace(s.ID) == "":
		return fmt.Errorf("%w: local session id is required", ErrInvalid)
	case strings.TrimSpace(s.Env) == "":
		return fmt.Errorf("%w: local session environment is required", ErrInvalid)
	case strings.TrimSpace(s.Worktree.Host) == "":
		return fmt.Errorf("%w: local session host is required", ErrInvalid)
	case s.StartedAt.IsZero() || s.LastSeenAt.Before(s.StartedAt):
		return fmt.Errorf("%w: local session window is malformed", ErrInvalid)
	case s.StoppedAt != nil && s.StoppedAt.Before(s.StartedAt):
		return fmt.Errorf("%w: local session stopped before it started", ErrInvalid)
	}
	return nil
}
