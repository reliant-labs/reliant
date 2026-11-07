package release

import (
	"fmt"
	"strings"
	"time"
)

// ─── Run ─────────────────────────────────────────────────────────────────────

// Run is the identity of ONE pipeline attempt, carried by everything that
// attempt wrote: the release it cut, the promotions it made, the gates it
// recorded. It is what turns a scatter of ledger rows into a timeline
// somebody can read.
//
// It is deliberately thin. A run is not stored as an entity anywhere: there
// is no runs table and no run events, because every stage already leaves a
// durable, timestamped fact in the ledger (a release's CreatedAt, a
// promotion's PromotedAt, a gate's window). The ID is the join key over
// those facts, and nothing more. A parallel event log would be a second
// source of truth free to disagree with the first.
type Run struct {
	// ID is opaque and CALLER-chosen, stable across retries of one
	// attempt: "github:acme/app/12345/1". Stable-across-retries is the
	// property that makes gate recording idempotent on
	// (promotion, name, run id) — a re-run of one CI step must not
	// double-record.
	ID string `json:"id"`
	// URL links to the run in whatever ran it.
	URL string `json:"url,omitempty"`
	// Provider is "github", "gitlab", "manual". DISPLAY ONLY — nothing
	// branches on it, and it is not part of the run's identity. A verb
	// that behaved differently per provider is the thing this field exists
	// to avoid needing.
	Provider string `json:"provider,omitempty"`
}

// Zero reports whether no run was supplied. A human at a terminal with
// --no-run writes one of these, and it is not an error: a promotion that
// belongs to no run is still a promotion.
func (r Run) Zero() bool { return r == Run{} }

// Validate refuses a run that carries detail but no identity. URL or
// provider without an ID cannot be joined to anything, so it would be
// detail about a run nobody can find.
func (r Run) Validate() error {
	if strings.TrimSpace(r.ID) == "" && (r.URL != "" || r.Provider != "") {
		return fmt.Errorf("%w: run url/provider given without a run id", ErrInvalid)
	}
	return nil
}

// ─── StageKind ───────────────────────────────────────────────────────────────

// StageKind says what part of a run a stage is. CLOSED.
//
// The vocabulary covers the pipeline the primitives describe —
// build → cut → check → promote → rollout — where the narrative's
// "converge" and "verify" are not separate kinds:
//
//   - CONVERGE is the rollout: [StageRollout] is one promotion's progress
//     toward the digests it pinned, derived on read from the deployment
//     rows. Naming it separately would imply a second stored thing.
//   - VERIFY is a check: a post-deploy smoke or health probe is recorded as
//     a [Gate] and appears as [StageCheck], named "verify" or "smoke".
//
// So every stage is either DERIVED from the ledger (cut, promote, rollout)
// or recorded as a gate (build, check). Nothing else is stored.
type StageKind string

const (
	// StageBuild is an artifact build. Recorded as a gate.
	StageBuild StageKind = "build"
	// StageCut is the release cut. Derived: the release row.
	StageCut StageKind = "cut"
	// StageCheck is any check — lint, test, smoke, wait, verify, a manual
	// sign-off. Recorded as a gate; Name carries which.
	StageCheck StageKind = "check"
	// StagePromote is one environment binding. Derived: the promotion row.
	StagePromote StageKind = "promote"
	// StageRollout is one promotion converging. Derived on read from the
	// deployment rows against that promotion's frozen pins.
	StageRollout StageKind = "rollout"
)

// StageKinds is the closed set, in PIPELINE order: a run's stages sort by
// time, but this is the order the kinds happen in.
var StageKinds = []StageKind{StageBuild, StageCut, StageCheck, StagePromote, StageRollout}

// Valid reports whether k is one of [StageKinds].
func (k StageKind) Valid() bool {
	for _, known := range StageKinds {
		if k == known {
			return true
		}
	}
	return false
}

func stageKindNames() []string {
	out := make([]string, len(StageKinds))
	for i, k := range StageKinds {
		out[i] = string(k)
	}
	return out
}

// ParseStageKind reads a stage kind and refuses anything outside the set.
func ParseStageKind(s string) (StageKind, error) {
	if k := StageKind(s); k.Valid() {
		return k, nil
	}
	return "", fmt.Errorf("%w: unknown stage kind %q (expected one of %s)",
		ErrInvalid, s, strings.Join(stageKindNames(), ", "))
}

// UnmarshalJSON decodes through [ParseStageKind].
func (k *StageKind) UnmarshalJSON(data []byte) error {
	return decodeClosed(data, "stage kind", func(v string) bool { return StageKind(v).Valid() }, stageKindNames(), (*string)(k))
}

// ─── StageStatus ─────────────────────────────────────────────────────────────

// StageStatus is a stage's verdict. CLOSED. It is [GateStatus] plus
// [StageRunning], because a stage can be observed WHILE it happens — a
// rollout in progress, a check still running — and a gate cannot: a gate is
// recorded once its check has finished.
type StageStatus string

const (
	// StagePassed, StageFailed, StageSkipped and StageErrored mean exactly
	// what the matching [GateStatus] values mean.
	StagePassed  StageStatus = StageStatus(GateStatusPassed)
	StageFailed  StageStatus = StageStatus(GateStatusFailed)
	StageSkipped StageStatus = StageStatus(GateStatusSkipped)
	StageErrored StageStatus = StageStatus(GateStatusError)
	// StageRunning: the stage has started and has not reached a verdict.
	// A run with any running stage is not finished, which is why a "did
	// the whole run pass" check cannot treat this as either outcome.
	StageRunning StageStatus = "running"
)

// StageStatuses is the closed set, in a stable order.
var StageStatuses = []StageStatus{StagePassed, StageFailed, StageRunning, StageSkipped, StageErrored}

// Valid reports whether s is one of [StageStatuses].
func (s StageStatus) Valid() bool {
	for _, known := range StageStatuses {
		if s == known {
			return true
		}
	}
	return false
}

func stageStatusNames() []string {
	out := make([]string, len(StageStatuses))
	for i, s := range StageStatuses {
		out[i] = string(s)
	}
	return out
}

// ParseStageStatus reads a stage status and refuses anything outside the
// set.
func ParseStageStatus(s string) (StageStatus, error) {
	if status := StageStatus(s); status.Valid() {
		return status, nil
	}
	return "", fmt.Errorf("%w: unknown stage status %q (expected one of %s)",
		ErrInvalid, s, strings.Join(stageStatusNames(), ", "))
}

// UnmarshalJSON decodes through [ParseStageStatus].
func (s *StageStatus) UnmarshalJSON(data []byte) error {
	return decodeClosed(data, "stage status", func(v string) bool { return StageStatus(v).Valid() }, stageStatusNames(), (*string)(s))
}

// StageStatusOfGate is the one mapping from a recorded gate to its stage
// status. A gate has always finished, so it never maps to [StageRunning],
// and a gate whose stored status was unrecognised maps to [StageErrored] —
// the same refusal to read an uninterpretable claim as a pass.
func StageStatusOfGate(g Gate) StageStatus {
	if g.RawStatus != "" {
		return StageErrored
	}
	if status, err := ParseStageStatus(string(g.Status)); err == nil {
		return status
	}
	return StageErrored
}

// ─── Stage ───────────────────────────────────────────────────────────────────

// Stage is one step of a run, as a reader sees it. It is a VIEW, assembled
// on read from the ledger — the release cut, the promotions, the gates
// carrying this run's id, and each promotion's derived rollout. Nothing
// constructs a Stage to store it.
type Stage struct {
	Kind StageKind `json:"kind"`
	// Name is which one: a check's name, an environment name, an artifact.
	Name   string      `json:"name,omitempty"`
	Status StageStatus `json:"status"`
	// Env is the environment a promote or rollout stage concerns. Empty on
	// a build, a cut, or a check that is not env-specific.
	Env string `json:"env,omitempty"`
	// PromotionID ties a promote or rollout stage to its ledger entry.
	PromotionID string     `json:"promotion_id,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	URL         string     `json:"url,omitempty"`
	Summary     string     `json:"summary,omitempty"`
}

// Validate checks a stage read back from a backend.
func (s Stage) Validate() error {
	switch {
	case !s.Kind.Valid():
		return fmt.Errorf("%w: unknown stage kind %q (expected one of %s)",
			ErrInvalid, s.Kind, strings.Join(stageKindNames(), ", "))
	case !s.Status.Valid():
		return fmt.Errorf("%w: stage %q: unknown status %q (expected one of %s)",
			ErrInvalid, s.Name, s.Status, strings.Join(stageStatusNames(), ", "))
	case s.StartedAt != nil && s.FinishedAt != nil && s.FinishedAt.Before(*s.StartedAt):
		return fmt.Errorf("%w: stage %q finished before it started", ErrInvalid, s.Name)
	}
	return nil
}

// RunTimeline is one run, assembled. The release is the one a run cut, or
// the one its promotions bound when the run cut nothing.
type RunTimeline struct {
	Run Run `json:"run"`
	// Release is the version label the run concerns, empty when unknown.
	Release string `json:"release,omitempty"`
	// Stages are ordered by StartedAt, oldest first.
	Stages []Stage `json:"stages"`
}

// Verdict collapses a timeline into one answer, which is what a final "did
// this run pass" pipeline step needs. The order is deliberate and is NOT
// worst-wins alphabetically:
//
//	failed > errored > running > passed
//
// A failure outranks a still-running stage because a run with a failed
// stage has failed whatever the rest does. Running outranks passed because
// a run that has not finished has not passed. A run with no stages at all
// is [StageRunning]: an id nothing has written under is not a pass.
func (t RunTimeline) Verdict() StageStatus {
	if len(t.Stages) == 0 {
		return StageRunning
	}
	verdict := StageSkipped
	rank := map[StageStatus]int{StageSkipped: 0, StagePassed: 1, StageRunning: 2, StageErrored: 3, StageFailed: 4}
	for _, s := range t.Stages {
		if rank[s.Status] > rank[verdict] {
			verdict = s.Status
		}
	}
	if verdict == StageSkipped {
		return StagePassed
	}
	return verdict
}
