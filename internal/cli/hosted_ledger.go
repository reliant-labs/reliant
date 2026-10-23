package cli

// The HOSTED release ledger: the same bindingStore / releaseLedger seam as
// the file backend, served by a control plane's DeployService over Connect.
//
// forge does not import control-plane. Every request and response shape below
// is declared HERE, in proto3 JSON (lowerCamel field names, enums as their
// value names, Timestamps as RFC3339), and carried by cloud.Client.Call. A
// field forge does not read is a field forge does not break on.
//
// THE SERVER OWNS THE RULES THAT NEED A LOCK. Promote applies release.Decide
// inside a transaction that holds the environment row, so the idempotent-retry
// check is made against a history no concurrent promoter can change underneath
// it. This file therefore does
// NOT pre-check with a read — a read-then-write from a client would be the
// race the server's lock exists to close.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// hostedErrorHasCode reports whether err is a control-plane failure carrying
// the given Connect code.
//
// This is the ONE place a hosted caller classifies a wire failure, and it
// reads the code as DATA. The previous spelling was
// `strings.Contains(err.Error(), "already_exists")`, which is wrong in both
// directions: a server that improves its prose breaks forge's control flow
// silently, and forge's own message text contains the words it searches for,
// so a message that merely MENTIONS a code reads as that code.
//
// errors.As rather than a type assertion, so the classification survives the
// `fmt.Errorf("...: %w", err)` wrapping every caller adds for context.
func hostedErrorHasCode(err error, code string) bool {
	var cerr *cloud.Error
	return errors.As(err, &cerr) && cerr.HasCode(code)
}

// hostedErrorReason is the app-defined domain reason a refusal carries, or
// "" when the failure is not a control-plane error or carried none.
//
// The Connect code is the CATEGORY — every promote refusal is
// FailedPrecondition — and this is what says WHICH refusal, so it is what an
// exit code is mapped from (§3.A: every primitive maps reasons, not message
// text).
func hostedErrorReason(err error) string {
	var cerr *cloud.Error
	if errors.As(err, &cerr) {
		return cerr.Reason
	}
	return ""
}

const (
	procCutRelease     = "controlplane.v1.DeployService/CutRelease"
	procGetRelease     = "controlplane.v1.DeployService/GetRelease"
	procListReleases   = "controlplane.v1.DeployService/ListReleases"
	procPromote        = "controlplane.v1.DeployService/Promote"
	procListPromotions = "controlplane.v1.DeployService/ListPromotions"
)

// hostedReleaseListLimit bounds a release listing. The promote plan orders
// releases to decide a promote's DIRECTION; the server caps a page anyway.
const hostedReleaseListLimit = 500

// ─── Wire shapes (controlplane.v1) ───────────────────────────────────────────

type wireSource struct {
	Repo   string `json:"repo,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Subdir string `json:"subdir,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// wireBuildSource is release.BuildSource: the checkout an image was built in.
type wireBuildSource struct {
	Repo   string `json:"repo,omitempty"`
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
}

// wireArtifact is one DeployArtifact: exactly one per (name, variant).
type wireArtifact struct {
	Name      string      `json:"name"`
	Digest    string      `json:"digest,omitempty"`
	Platforms []string    `json:"platforms,omitempty"`
	Kind      string      `json:"kind"`
	Version   string      `json:"version,omitempty"`
	Integrity string      `json:"integrity,omitempty"`
	URI       string      `json:"uri,omitempty"`
	Mode      string      `json:"mode"`
	Variant   string      `json:"variant"`
	Source    *wireSource `json:"source,omitempty"`
	// BuiltFrom is sent ahead of the control plane storing it: a server
	// whose DeployArtifact has no built_from field discards it (Connect's
	// JSON codec ignores unknown fields), and one that has it records it —
	// so this side needs no change when the server gains it.
	BuiltFrom *wireBuildSource `json:"builtFrom,omitempty"`
}

type wireRelease struct {
	SnapshotRelease json.RawMessage `json:"snapshotRelease,omitempty"`
	ID              string          `json:"id"`
	Version         string          `json:"version"`
	GitCommit       string          `json:"gitCommit,omitempty"`
	GitTag          string          `json:"gitTag,omitempty"`
	GitDirty        bool            `json:"gitDirty,omitempty"`
	Artifacts       []wireArtifact  `json:"artifacts"`
	CreatedByUserID string          `json:"createdByUserId,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	// Run is the CI run that cut this release (DeployRelease.run, tag 9).
	Run *wireRun `json:"run,omitempty"`
	// Provenance is DeployRelease.provenance (tag 10): the tree hash, forge
	// version and checkout the release was cut from — what Cut sent, stored
	// and returned by the control plane.
	//
	// READ BACK, NOT ONLY WRITTEN. A no-version `forge env deploy` reuses a
	// release whose provenance matches the checkout (chooseDeployRelease),
	// and it finds that release through List. Before this field existed
	// every release read from a control plane came back with no provenance,
	// so the lookup never matched: on 2026-10-09 a deploy of prod, in the
	// same clean checkout `forge env build prod --release` had cut from
	// minutes earlier, rebuilt every image and named a new release.
	Provenance *wireProvenance `json:"provenance,omitempty"`
	// Commits is DeployRelease.commits (tag 14): the source history the
	// release adds over the release it was cut after, recorded at the cut
	// because no reader of a hosted ledger has the repository.
	Commits *wireCommitRange `json:"commits,omitempty"`
}

// wireCommitRange is controlplane.v1.DeployCommitRange.
type wireCommitRange struct {
	BaseRelease string       `json:"baseRelease,omitempty"`
	BaseCommit  string       `json:"baseCommit,omitempty"`
	Commits     []wireCommit `json:"commits,omitempty"`
	Total       int          `json:"total,omitempty"`
	Truncated   bool         `json:"truncated,omitempty"`
}

// wireCommit is controlplane.v1.DeployCommit.
type wireCommit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject,omitempty"`
	Author  string    `json:"author,omitempty"`
	Time    time.Time `json:"time"`
}

func commitRangeToWire(r release.CommitRange) wireCommitRange {
	w := wireCommitRange{BaseRelease: r.BaseRelease, BaseCommit: r.BaseCommit, Total: r.Total, Truncated: r.Truncated}
	for _, c := range r.Commits {
		w.Commits = append(w.Commits, wireCommit{SHA: c.SHA, Subject: c.Subject, Author: c.Author, Time: c.Time.UTC()})
	}
	return w
}

func commitRangeFromWire(w *wireCommitRange) *release.CommitRange {
	if w == nil {
		return nil
	}
	r := &release.CommitRange{
		BaseRelease: w.BaseRelease, BaseCommit: w.BaseCommit,
		Commits: []release.Commit{}, Total: w.Total, Truncated: w.Truncated,
	}
	for _, c := range w.Commits {
		r.Commits = append(r.Commits, release.Commit{SHA: c.SHA, Subject: c.Subject, Author: c.Author, Time: c.Time.UTC()})
	}
	return r
}

// wireRun is controlplane.v1.DeployRun: the id that joins a release, its
// promotions and its gates into one readable story. Opaque to the control
// plane, and `provider` is display only.
type wireRun struct {
	ID       string `json:"id"`
	URL      string `json:"url,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// wireGate is controlplane.v1.DeployGate, in full.
//
// Status is a plain STRING rather than a release.GateStatus even though the
// set is closed, because the read path must stay lenient: a control plane
// holding promotions recorded before the set closed returns whatever free
// text the old scaffold wrote, and a typed field would refuse to decode
// them — making historical promotions unrenderable. The mapping happens in
// gateFromWire via release.GateStatusFromStored; the WRITE path is strict
// (release.ParseGateStatus) in gateToWire.
type wireGate struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	URL     string `json:"url,omitempty"`
	Summary string `json:"summary,omitempty"`
	// The check's OWN window, not when it was recorded.
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// RunID ties this check to one run. Optional: a manual sign-off
	// belongs to a promotion and to no run.
	RunID string `json:"runId,omitempty"`
	// Details is small and structured — counts and verdicts, capped
	// server-side at 8 KiB. The full report is behind URL.
	Details map[string]any `json:"details,omitempty"`
	// RecordedBy and RecordedAt are SET BY THE SERVER and ignored on a
	// request: evidence whose author the author chose is not
	// attributable. forge reads them and never sends them.
	RecordedBy string     `json:"recordedBy,omitempty"`
	RecordedAt *time.Time `json:"recordedAt,omitempty"`
}

type wirePromotion struct {
	Snapshot          *release.Snapshot     `json:"snapshot,omitempty"`
	SnapshotDigest    string                `json:"snapshotDigest,omitempty"`
	BundleID          string                `json:"bundleId,omitempty"`
	BundleDigest      string                `json:"bundleDigest,omitempty"`
	ID                string                `json:"id"`
	EnvironmentID     string                `json:"environmentId"`
	ReleaseID         string                `json:"releaseId"`
	ReleaseVersion    string                `json:"releaseVersion"`
	Kind              string                `json:"kind"`
	FromEnvironmentID string                `json:"fromEnvironmentId,omitempty"`
	ResolvedArtifacts map[string]string     `json:"resolvedArtifacts,omitempty"`
	ResolvedSources   map[string]wireSource `json:"resolvedSources,omitempty"`
	// Targets is DeployPromotion.targets (tag 26): the components a scoped
	// deploy moved; empty for a whole-release promotion.
	Targets          []string   `json:"targets,omitempty"`
	PromotedByUserID string     `json:"promotedByUserId,omitempty"`
	PromotedByActor  string     `json:"promotedByActor,omitempty"`
	Gates            []wireGate `json:"gates,omitempty"`
	Note             string     `json:"note,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`

	// RecordedGates is evidence that arrived AFTER this row was written,
	// through RecordGate. Kept separate from Gates because the two answer
	// different questions: Gates is what the promoter claimed at the
	// moment they promoted, and these are results that came later.
	// Merging them would lose "was this known before the button was
	// pressed?", the first question asked about a bad release.
	RecordedGates []wireGate `json:"recordedGates,omitempty"`
	// SupersededInFlight: this promote overrode an unfinished rollout
	// (`--supersede`). Recorded because an override that leaves no trace
	// is indistinguishable from the refusal never having fired.
	SupersededInFlight bool `json:"supersededInFlight,omitempty"`
	// FromEnvironmentName is the NAME of FromEnvironmentID, so a ledger
	// reader renders "staging → prod" without a lookup per row. This is
	// what lets promotionFromWire report a name where it previously had
	// to fall back to the opaque id.
	FromEnvironmentName string `json:"fromEnvironmentName,omitempty"`
	// FromPromotionID is the specific promotion this release was taken
	// from (`promote --from staging`). FromEnvironmentID says WHERE it
	// came from; this says exactly WHAT was there, which is what keeps
	// "promote whatever staging is running" auditable after staging has
	// moved on.
	FromPromotionID string `json:"fromPromotionId,omitempty"`
	// Run is the CI run that performed this promotion.
	Run *wireRun `json:"run,omitempty"`

	// PlanDigest, ApprovedBy and AcknowledgedFindings are O-13's record of
	// the review this promotion was written under (tags 23–25): which plan,
	// who approved it, and which stop-class findings they accepted by name.
	//
	// ApprovedBy is SERVER-SET from the credential and read-only here, for
	// the reason RecordedBy on a gate is: an approval whose approver the
	// approver chose is not an approval.
	//
	// All three are empty on a promotion written before O-13, and on an env
	// whose promotions nothing converges (where the plan is optional). Empty
	// therefore means "no plan was recorded", never "the plan was empty" —
	// a reader must not render an unreviewed promotion as an approved one.
	PlanDigest           string   `json:"planDigest,omitempty"`
	ApprovedBy           string   `json:"approvedBy,omitempty"`
	AcknowledgedFindings []string `json:"acknowledgedFindings,omitempty"`
	// ImportedFrom is set on a promotion that came from `forge ledger
	// import` (§11): the git path it was read from. Its presence is what
	// labels the row as history rather than something that happened here.
	ImportedFrom string `json:"importedFrom,omitempty"`
}

// ─── Rollout, refusal and run (P0's read shapes) ─────────────────────────────
//
// These are the documents the §3.2 wait, the §3.1 refusal and the §3.7 run
// view read. They are declared HERE, with F0, rather than by each verb that
// consumes them, so the wire contract has ONE spelling: F3's wait, F2's
// promote refusal and F8's run show all decode the same structs, and a field
// the server renamed breaks in one place instead of three. §4.3 makes this
// explicit — "a missing wire field is an F0 follow-up".

// The DeployRolloutPhase enum's value names, as protojson writes them.
//
// A rollout phase is DERIVED on read, never stored, and the distinctions
// matter to the exit codes: SUPERSEDED and UNKNOWN are their own outcomes
// rather than failures, because "overtaken" and "we cannot see it" are not
// "the release is bad".
const (
	wireRolloutPhasePrefix      = "DEPLOY_ROLLOUT_PHASE_"
	wireRolloutPhaseUnspecified = "DEPLOY_ROLLOUT_PHASE_UNSPECIFIED"
	wireRolloutPhasePending     = "DEPLOY_ROLLOUT_PHASE_PENDING"
	wireRolloutPhaseProgressing = "DEPLOY_ROLLOUT_PHASE_PROGRESSING"
	wireRolloutPhaseStabilizing = "DEPLOY_ROLLOUT_PHASE_STABILIZING"
	wireRolloutPhaseSucceeded   = "DEPLOY_ROLLOUT_PHASE_SUCCEEDED"
	wireRolloutPhaseDegraded    = "DEPLOY_ROLLOUT_PHASE_DEGRADED"
	wireRolloutPhaseSuperseded  = "DEPLOY_ROLLOUT_PHASE_SUPERSEDED"
	wireRolloutPhaseUnknown     = "DEPLOY_ROLLOUT_PHASE_UNKNOWN"
	// HELD: accepted and recorded, QUEUED on a human action (billing). Not
	// in flight, not failing, not progressing — waiting on a person.
	wireRolloutPhaseHeld = deploytarget.WireRolloutPhaseHeld
)

// rolloutPhaseName renders a phase enum value for a human and for --json:
// "DEPLOY_ROLLOUT_PHASE_DEGRADED" → "degraded". An unrecognised value is
// returned verbatim rather than mapped to a known phase, because a phase
// forge does not understand must not read as a success or a failure.
func rolloutPhaseName(wire string) string {
	if wire == "" || wire == wireRolloutPhaseUnspecified {
		return "unspecified"
	}
	if trimmed := strings.TrimPrefix(wire, wireRolloutPhasePrefix); trimmed != wire {
		return strings.ToLower(trimmed)
	}
	return wire
}

// exitCodeForRolloutPhase maps a terminal phase to §3.A's exit code.
//
// The three non-obvious rows are the point of the table. A rollout still
// PENDING / PROGRESSING / STABILIZING at the deadline is exitTimedOut (5),
// not exitWrong — it was progressing, and a pipeline should retry the WAIT
// rather than conclude the release is bad. SUPERSEDED is 6, its own outcome:
// the wait's subject is gone, so neither retry nor failure is right.
// UNKNOWN is exitUndetermined (2), never folded into either: "we cannot see
// it" is not permission.
func exitCodeForRolloutPhase(wire string) int {
	switch wire {
	case wireRolloutPhaseSucceeded:
		return exitOK
	case wireRolloutPhaseDegraded:
		return exitWrong
	case wireRolloutPhaseSuperseded:
		return exitSuperseded
	case wireRolloutPhaseUnknown:
		return exitUndetermined
	case wireRolloutPhasePending, wireRolloutPhaseProgressing, wireRolloutPhaseStabilizing:
		return exitTimedOut
	case wireRolloutPhaseHeld:
		// Accepted and waiting on a person, not on time: 7, so a pipeline
		// hands the action to a human instead of retrying a wait that no
		// amount of waiting will finish.
		return exitQueued
	default:
		// A phase forge does not recognise is unobservable to forge,
		// which is exactly what 2 means. Reading it as a pass would be
		// the dangerous default.
		return exitUndetermined
	}
}

// wireWorkloadRollout is controlplane.v1.DeployWorkloadRollout: one
// workload's progress toward one promotion's pin.
type wireWorkloadRollout struct {
	DeploymentID string `json:"deploymentId"`
	Name         string `json:"name"`
	// Artifact is the release artifact key this workload runs
	// (Deployment.artifact). Empty on an unpinned row.
	Artifact string `json:"artifact,omitempty"`
	// PinnedDigest comes from THIS promotion's resolvedArtifacts — the
	// frozen pin, not whatever the deployment row declares now. That is
	// the whole point of scoping a rollout to a promotion: a wait must
	// not succeed on bytes it was never asked about because somebody
	// promoted again underneath it.
	PinnedDigest string `json:"pinnedDigest,omitempty"`
	// DesiredDigest is what the row currently declares, which differs
	// from PinnedDigest while a promotion is unapplied.
	DesiredDigest  string     `json:"desiredDigest,omitempty"`
	ObservedDigest string     `json:"observedDigest,omitempty"`
	ObservedState  string     `json:"observedState,omitempty"`
	Verdict        string     `json:"verdict,omitempty"`
	Phase          string     `json:"phase,omitempty"`
	StableSince    *time.Time `json:"stableSince,omitempty"`
	ConvergedAt    *time.Time `json:"convergedAt,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	// UpdatedReplicas and DesiredReplicas are what stop a rollout being
	// reported healthy before it is. Under RollingUpdate, old ready pods
	// keep readyReplicas up while the new ReplicaSet crash-loops, so
	// "ready ≥ wanted" is true of a release that never served a request.
	// Comparing UPDATED against DESIRED is the completion test
	// `kubectl rollout status` applies.
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`
	DesiredReplicas int32 `json:"desiredReplicas,omitempty"`
}

// wireRollout is controlplane.v1.DeployRollout: the single server-computed
// answer to "is v6 done". Computed in one place on purpose, so wait, the
// in-flight refusal, the UI and a run timeline cannot hold four different
// opinions about whether a release landed.
type wireRollout struct {
	Promotion wirePromotion         `json:"promotion"`
	Phase     string                `json:"phase,omitempty"`
	Workloads []wireWorkloadRollout `json:"workloads,omitempty"`
	// Unpinned are workloads the promotion does not pin: databases,
	// third-party images. REPORTED, never gating — a database that cannot
	// be release-bound must not be able to fail a release.
	Unpinned   []wireWorkloadRollout `json:"unpinned,omitempty"`
	StartedAt  *time.Time            `json:"startedAt,omitempty"`
	FinishedAt *time.Time            `json:"finishedAt,omitempty"`
	// 64-bit proto scalar: protojson sends a STRING. See wireInt64.
	StabilityWindowMS wireInt64 `json:"stabilityWindowMs,omitempty"`
	// ConvergesPromotions false means nothing will move without a
	// client-side deploy, so a caller should refuse FAST rather than wait
	// out a timeout whose cause is "nobody was ever going to apply this".
	ConvergesPromotions bool   `json:"convergesPromotions,omitempty"`
	Reason              string `json:"reason,omitempty"`
	// Holds is set exactly when Phase is HELD: what the queued promotion
	// waits on, each with its own sentence, remedy and link.
	Holds []deploytarget.HostedHold `json:"holds,omitempty"`
}

// wirePromoteRefusal is controlplane.v1.DeployPromoteRefusal, carried as a
// Connect error detail on the FailedPrecondition a refused promote returns.
//
// It names WHAT IS THERE, not just that something was. A refusal reading
// "someone else promoted" sends a human to a dashboard; one carrying the
// actual current promotion lets the pipeline print "prod is on v1.9.1,
// promoted by alice 4 minutes ago" without a second round trip.
type wirePromoteRefusal struct {
	// Reason is one of the reason* constants (promotion_conflict |
	// rollout_in_flight | environment_pinned | plan_stale |
	// plan_unacknowledged | bundle_missing).
	Reason string `json:"reason"`
	// Echoed back from the request, so a log line is self-contained.
	ExpectedCurrentPromotionID string `json:"expectedCurrentPromotionId,omitempty"`
	ExpectedUnbound            bool   `json:"expectedUnbound,omitempty"`
	// ActualCurrent is the promotion that landed instead.
	ActualCurrent *wirePromotion `json:"actualCurrent,omitempty"`
	// ActualPhase is set for rollout_in_flight: the phase that made it
	// in flight.
	ActualPhase string `json:"actualPhase,omitempty"`
	Detail      string `json:"detail,omitempty"`
	// CurrentPlan is the FRESHLY RECOMPUTED plan, sent with plan_stale and
	// plan_unacknowledged (DeployPromoteRefusal tag 8, O-13).
	//
	// It is on the refusal so a refused caller can show the operator WHAT
	// CHANGED without a second round trip — and more importantly without a
	// second PlanDeploy, which would be computed at yet another moment and
	// so could differ from the one that actually refused the write. The
	// refusal carries the plan the server judged against, which is the only
	// plan the operator's next decision should be based on.
	CurrentPlan *wirePlan `json:"currentPlan,omitempty"`
}

// promoteRefusalType is the protobuf full name under which a refusal rides
// as a Connect error detail.
const promoteRefusalType = "controlplane.v1.DeployPromoteRefusal"

// promoteRefusalOf decodes the refusal detail from a failed promote, if the
// server sent one.
//
// A refusal WITHOUT a decodable detail is not an error here: the reason
// header alone is enough to choose an exit code (exitCodeForRefusal), and
// the detail only enriches the message. Treating a missing detail as a
// failure would make forge refuse to report a refusal.
func promoteRefusalOf(err error) (wirePromoteRefusal, bool) {
	var cerr *cloud.Error
	if !errors.As(err, &cerr) {
		return wirePromoteRefusal{}, false
	}
	payload, ok := cerr.DetailJSON(promoteRefusalType)
	if !ok {
		return wirePromoteRefusal{}, false
	}
	var refusal wirePromoteRefusal
	if jsonErr := json.Unmarshal(payload, &refusal); jsonErr != nil {
		return wirePromoteRefusal{}, false
	}
	// The header is authoritative when both are present; a detail that
	// states no reason still carries the useful part (actual_current).
	if refusal.Reason == "" {
		refusal.Reason = cerr.Reason
	}
	return refusal, true
}

// wireRunStage is controlplane.v1.DeployRunStage: one step in a run's
// timeline, ASSEMBLED FROM THE LEDGER. Nothing here is a stored event —
// a stage is a release cut, a promotion, a recorded gate carrying the run
// id, or a promotion's derived rollout.
type wireRunStage struct {
	Kind          string     `json:"kind"`
	Name          string     `json:"name,omitempty"`
	EnvironmentID string     `json:"environmentId,omitempty"`
	PromotionID   string     `json:"promotionId,omitempty"`
	Status        string     `json:"status,omitempty"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	URL           string     `json:"url,omitempty"`
	Summary       string     `json:"summary,omitempty"`
}

// ─── Gate conversion (through R1's vocabulary) ───────────────────────────────

// gateFromWire is the LENIENT read path. A control plane holding promotions
// recorded before the gate status set closed returns whatever free text the
// old scaffold wrote, and refusing to decode it would make those promotions
// unrenderable — the ledger is append-only, so no migration could clean
// them up. An unrecognised value maps to `error` and is kept verbatim on
// RawStatus, which is exactly release.GateStatusFromStored's contract.
func gateFromWire(w wireGate) release.Gate {
	status, raw := release.GateStatusFromStored(w.Status)
	return release.Gate{
		Name:       w.Name,
		Status:     status,
		RawStatus:  raw,
		URL:        w.URL,
		Summary:    w.Summary,
		StartedAt:  w.StartedAt,
		FinishedAt: w.FinishedAt,
		RunID:      w.RunID,
		Details:    w.Details,
		RecordedBy: w.RecordedBy,
		RecordedAt: w.RecordedAt,
	}
}

func gatesFromWire(in []wireGate) []release.Gate {
	if len(in) == 0 {
		return nil
	}
	out := make([]release.Gate, 0, len(in))
	for _, w := range in {
		out = append(out, gateFromWire(w))
	}
	return out
}

// gateToWire is the STRICT write path: release.Gate.Validate refuses a
// status outside the closed set, and a gate carrying a RawStatus (one that
// only survived a READ because it was mapped) cannot be written back.
//
// RecordedBy and RecordedAt are deliberately NOT sent. The server sets them
// from the authenticated principal, and a client-supplied attribution would
// be a client claiming who vouched for a check — the one part of a piece of
// evidence that must not come from the party being vouched for.
func gateToWire(g release.Gate) (wireGate, error) {
	if err := g.Validate(); err != nil {
		return wireGate{}, err
	}
	return wireGate{
		Name:       g.Name,
		Status:     string(g.Status),
		URL:        g.URL,
		Summary:    g.Summary,
		StartedAt:  g.StartedAt,
		FinishedAt: g.FinishedAt,
		RunID:      g.RunID,
		Details:    g.Details,
	}, nil
}

func gatesToWire(in []release.Gate) ([]wireGate, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]wireGate, 0, len(in))
	for _, g := range in {
		w, err := gateToWire(g)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

// runFromWire and runToWire convert controlplane.v1.DeployRun.
func runFromWire(w *wireRun) release.Run {
	if w == nil {
		return release.Run{}
	}
	return release.Run{ID: w.ID, URL: w.URL, Provider: w.Provider}
}

// The DeployPromotionKind enum's value names, as protojson writes them.
const (
	wireKindPromote = "DEPLOY_PROMOTION_KIND_PROMOTE"
	// wireKindLegacyRollback is what a control plane returns for a ledger
	// row written by the retired Rollback RPC. READ ONLY: it binds the env
	// to its release exactly as a promote does, and decodes as one.
	wireKindLegacyRollback = "DEPLOY_PROMOTION_KIND_ROLLBACK"
)

// ─── Conversion ──────────────────────────────────────────────────────────────

// releaseToWire flattens a release into one DeployArtifact per (name,
// variant), in a stable order.
func releaseToWire(r release.Release) []wireArtifact {
	var out []wireArtifact
	for _, name := range r.ArtifactNames() {
		a := r.Artifacts[name]
		base := wireArtifact{
			Name: name, Kind: string(a.Kind), Mode: string(a.Mode),
			Platforms: a.Platforms, Version: a.Version, Integrity: a.Integrity, URI: a.URI,
		}
		if a.Source != nil {
			s := wireSource(*a.Source)
			base.Source = &s
		}
		if a.BuiltFrom != nil {
			b := wireBuildSource(*a.BuiltFrom)
			base.BuiltFrom = &b
		}
		if len(a.Digests) == 0 {
			base.Variant = release.SharedVariant
			out = append(out, base)
			continue
		}
		variants := make([]string, 0, len(a.Digests))
		for v := range a.Digests {
			variants = append(variants, v)
		}
		sort.Strings(variants)
		for _, v := range variants {
			row := base
			row.Variant = v
			row.Digest = a.Digests[v]
			out = append(out, row)
		}
	}
	return out
}

// releaseFromWire regroups DeployArtifact rows into the release map and
// validates the result — a hosted release is held to the same closed enums
// as one read from disk.
func releaseFromWire(w wireRelease) (release.Release, error) {
	if len(w.SnapshotRelease) > 0 && string(w.SnapshotRelease) != "null" {
		return snapshotReleaseFromJSON(w.SnapshotRelease, w.Version)
	}
	r := release.Release{
		Version:   w.Version,
		Git:       release.Git{Commit: w.GitCommit, Tag: w.GitTag, Dirty: w.GitDirty},
		CreatedAt: w.CreatedAt,
		CreatedBy: w.CreatedByUserID,
		Artifacts: map[string]release.Artifact{},
		Run:       runFromWire(w.Run),
	}
	// A provenance that does not validate is DROPPED, not fatal. The control
	// plane validates provenance with forge's own rule before storing it, so
	// this is unreachable in practice — but List feeds every promote's
	// direction logic, and one malformed historical row must not make the
	// whole ledger unreadable. A release with no provenance is simply never
	// matched for reuse, which is the safe reading of "not known".
	if prov, err := provenanceFromWire(w.Provenance); err == nil {
		r.Provenance = prov
	}
	// Dropped when malformed, for the same reason: evidence about the
	// release, never a reason its ledger cannot be read.
	if commits := commitRangeFromWire(w.Commits); commits != nil && commits.Validate() == nil {
		r.Commits = commits
	}
	for _, row := range w.Artifacts {
		a, seen := r.Artifacts[row.Name]
		if !seen {
			a = release.Artifact{
				Kind: release.Kind(row.Kind), Mode: release.Mode(row.Mode),
				Platforms: row.Platforms, Version: row.Version, Integrity: row.Integrity, URI: row.URI,
			}
			if row.Source != nil {
				s := release.Source(*row.Source)
				a.Source = &s
			}
			if row.BuiltFrom != nil {
				b := release.BuildSource(*row.BuiltFrom)
				a.BuiltFrom = &b
			}
		}
		if row.Digest != "" {
			if a.Digests == nil {
				a.Digests = map[string]string{}
			}
			a.Digests[row.Variant] = row.Digest
		}
		r.Artifacts[row.Name] = a
	}
	if err := r.Validate(); err != nil {
		return release.Release{}, fmt.Errorf("control plane returned release %q: %w", w.Version, err)
	}
	return r, nil
}

func promotionKindFromWire(k string) (release.PromotionKind, error) {
	switch k {
	case wireKindPromote, wireKindLegacyRollback:
		return release.KindPromote, nil
	default:
		// Never defaulted: an unknown kind read as "promote" would make a
		// future entry type indistinguishable from a binding.
		return "", fmt.Errorf("%w: control plane returned promotion kind %q", release.ErrInvalid, k)
	}
}

// ─── The store ───────────────────────────────────────────────────────────────

// hostedStore implements both bindingStore and releaseLedger against one
// control plane.
type hostedStore struct {
	client   cloudCaller
	resolver hostedEnvResolver
	endpoint string
	// project and kind address the environment a WRITE ensures: identity is
	// (org, project, name), and the kind (derived from the env's KCL) is
	// immutable server-side.
	project string
	kind    deploytarget.HostedEnvKind

	mu     sync.Mutex
	envIDs map[string]string // env name → control-plane id, per process
	// holds is promotion id → what the write said it is queued on, for the
	// promotions THIS process appended (see QueuedHolds).
	holds map[string][]deploytarget.HostedHold

	// mirror is the local read-through copy of this ledger (hosted_cache.go).
	// Nil when the project has none (a test, an unnamed project).
	mirror   *hostedMirror
	replayed map[string]bool // env → queue already replayed by this process
}

// hostedLedger binds both halves of an env's ledger to one client. project
// and kind are the ledger env's control-plane address (see hostedEnvRefFor).
func hostedLedger(client cloudCaller, endpoint, project string, kind deploytarget.HostedEnvKind) envLedger {
	s := &hostedStore{client: client, resolver: cloudEnvResolver{client: client, project: project}, endpoint: endpoint, project: project, kind: kind}
	return envLedger{Bindings: s, Releases: s, Hosted: true}
}

func (s *hostedStore) Location() string { return s.endpoint }

// envID resolves (and remembers) the control plane's id for an env name.
func (s *hostedStore) envID(ctx context.Context, env string) (string, error) {
	s.mu.Lock()
	id, ok := s.envIDs[env]
	s.mu.Unlock()
	if ok {
		return id, nil
	}
	id, err := s.resolver.ResolveEnvironmentID(ctx, env)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	if s.envIDs == nil {
		s.envIDs = map[string]string{}
	}
	s.envIDs[env] = id
	s.mu.Unlock()
	return id, nil
}

// promotionFromWire converts one ledger entry. env is the NAME this store was
// asked about; the wire carries ids.
func (s *hostedStore) promotionFromWire(env string, w wirePromotion) (release.Promotion, error) {
	kind, err := promotionKindFromWire(w.Kind)
	if err != nil {
		return release.Promotion{}, err
	}
	p := release.Promotion{
		ID:         w.ID,
		Env:        env,
		Release:    w.ReleaseVersion,
		Kind:       kind,
		Resolved:   w.ResolvedArtifacts,
		PromotedBy: release.Actor{User: w.PromotedByUserID, Actor: w.PromotedByActor},
		Note:       w.Note,
		PromotedAt: w.CreatedAt,
	}
	switch {
	case w.FromEnvironmentName != "":
		// P0 added the NAME beside the id, so a ledger reader renders
		// "staging → prod" without a lookup per row. forge speaks
		// names, so prefer it.
		p.FromEnv = w.FromEnvironmentName
	case w.FromEnvironmentID != "":
		// A control plane that predates fromEnvironmentName sends only
		// the id. Kept verbatim rather than dropped, so the promotion
		// path stays auditable even when the name is unknown.
		p.FromEnv = w.FromEnvironmentID
	}
	p.Snapshot = w.Snapshot
	p.SnapshotDigest = w.SnapshotDigest
	p.BundleID = w.BundleID
	p.BundleDigest = w.BundleDigest
	if p.Snapshot != nil {
		digest, err := p.Snapshot.Digest()
		if err != nil || digest != p.SnapshotDigest || !release.ValidDigest(p.BundleDigest) || p.BundleID == "" {
			return release.Promotion{}, fmt.Errorf("invalid frozen promotion snapshot")
		}
		if p.Snapshot.Environment != env {
			return release.Promotion{}, fmt.Errorf("promotion snapshot environment mismatch")
		}
	}
	p.FromPromotionID = w.FromPromotionID
	p.Targets = release.NormalizeTargets(w.Targets)
	p.SupersededInFlight = w.SupersededInFlight
	p.Run = runFromWire(w.Run)
	if p.Resolved == nil {
		p.Resolved = map[string]string{}
	}
	if len(w.ResolvedSources) > 0 {
		p.Sources = map[string]release.Source{}
		for name, src := range w.ResolvedSources {
			p.Sources[name] = release.Source(src)
		}
	}
	// Both halves of the evidence trail: what the promoter claimed when
	// they promoted, and what arrived afterwards through RecordGate. Kept
	// in separate fields because merging them would lose "was this known
	// before the button was pressed?".
	p.Gates = gatesFromWire(w.Gates)
	p.RecordedGates = gatesFromWire(w.RecordedGates)
	// O-13's review record, read back so a promote that just landed can
	// print which plan it was approved against and who approved it. The
	// approver comes from here and never from what forge sent.
	p.PlanDigest = w.PlanDigest
	p.ApprovedBy = w.ApprovedBy
	p.AcknowledgedFindings = w.AcknowledgedFindings
	if err := p.Validate(); err != nil {
		return release.Promotion{}, fmt.Errorf("control plane returned promotion %s: %w", w.ID, err)
	}
	return p, nil
}

// Current is the newest ListPromotions entry. An environment the control
// plane has never heard of has, by definition, never been promoted.
func (s *hostedStore) Current(ctx context.Context, env string) (release.Promotion, bool, error) {
	p, bound, err := s.current(ctx, env)
	if err != nil {
		return p, bound, err
	}
	// The control plane just answered: replay what an offline deploy queued,
	// then read again so the caller sees the ledger as that deploy left it.
	if s.replayOnContact(ctx, env) {
		return s.current(ctx, env)
	}
	return p, bound, nil
}

func (s *hostedStore) current(ctx context.Context, env string) (release.Promotion, bool, error) {
	id, err := s.envID(ctx, env)
	if errors.Is(err, errHostedEnvNotFound) {
		s.remember(ctx, env, release.Promotion{}, false)
		return release.Promotion{}, false, nil
	}
	if err != nil {
		return release.Promotion{}, false, err
	}
	var resp struct {
		Promotions []wirePromotion `json:"promotions"`
	}
	if err := s.client.Call(ctx, procListPromotions, map[string]any{"environmentId": id, "limit": 1}, &resp); err != nil {
		return release.Promotion{}, false, err
	}
	if len(resp.Promotions) == 0 {
		s.remember(ctx, env, release.Promotion{}, false)
		return release.Promotion{}, false, nil
	}
	p, err := s.promotionFromWire(env, resp.Promotions[0])
	if err != nil {
		return release.Promotion{}, false, err
	}
	s.remember(ctx, env, p, true)
	return p, true, nil
}

// Append calls Promote. The server freezes the pin set from the
// release it holds — the Resolved/Sources on p are the client's PREVIEW and
// are deliberately not sent, because a request that could state digests
// would be a request that could ship bytes nobody cut.
//
// The guard rides as PromoteReleaseRequest tags 7–9 and is checked by the
// SERVER, under the env row lock, after its own idempotent no-op. A refusal
// comes back as a *promoteRefusedError carrying what is actually current.
func (s *hostedStore) Append(ctx context.Context, p release.Promotion, guard appendGuard) (release.Promotion, error) {
	if err := p.Validate(); err != nil {
		return release.Promotion{}, err
	}
	// A promotion is a WRITE: ensure the env by name first (see
	// ensureHostedEnv).
	if s.kind == deploytarget.HostedEnvLocal {
		// A LOCAL env runs on a developer machine; it has no release to
		// bind. The control plane refuses this too — refusing here means
		// no write is attempted at all.
		return release.Promotion{}, fmt.Errorf("env %q is LOCAL (it declares control_plane but no hosted tier): it runs via `forge env up`, and a release cannot be promoted into it", p.Env)
	}
	id, err := ensureHostedEnv(ctx, s.client, deploytarget.HostedEnvRef{Project: s.project, Name: p.Env, Kind: s.kind})
	if err != nil {
		return release.Promotion{}, err
	}
	s.mu.Lock()
	if s.envIDs == nil {
		s.envIDs = map[string]string{}
	}
	s.envIDs[p.Env] = id
	s.mu.Unlock()
	req := map[string]any{"environmentId": id}
	if !guard.ResolveVersionFromSource {
		req["version"] = p.Release
	}
	// Otherwise NO version is sent (§3.4). p.Release holds the plan's
	// preview of what fromPromotionId names, and sending it would make
	// forge assert a release it read earlier, outside the target's lock —
	// exactly the stale read `--from` exists to eliminate. The server
	// resolves the release from the promotion id instead, and refuses
	// source_moved if the source has moved past it.
	if p.PromotedBy.Actor != "" {
		req["promotedByActor"] = p.PromotedBy.Actor
	}
	if p.Note != "" {
		req["note"] = p.Note
	}
	// The scope (PromoteReleaseRequest tag 15). The server runs
	// release.Decide with it under the env row lock, so a scoped promotion
	// of the bound release is a new entry when it moves something new.
	if len(p.Targets) > 0 {
		req["targets"] = p.Targets
	}
	if p.FromEnv != "" {
		fromID, ferr := s.envID(ctx, p.FromEnv)
		if ferr != nil {
			return release.Promotion{}, ferr
		}
		req["fromEnvironmentId"] = fromID
	}
	if len(p.Gates) > 0 {
		// The STRICT write path: a status outside the closed set, or a
		// gate that only survived a READ because its status was mapped,
		// is refused here rather than recorded as evidence.
		gates, gerr := gatesToWire(p.Gates)
		if gerr != nil {
			return release.Promotion{}, fmt.Errorf("promote %s to %s: %w", p.Release, p.Env, gerr)
		}
		req["gates"] = gates
	}
	// RecordedGates are NOT sent: they are the post-promote half of the
	// trail and are appended through RecordGate (§3.3), against a
	// promotion that must already exist. Sending them on the promote
	// would claim evidence that arrived later was known beforehand.
	if run := runWireFields(p.Run); run != nil {
		req["run"] = run
	}
	if p.FromPromotionID != "" {
		req["fromPromotionId"] = p.FromPromotionID
	}
	// The approved plan (O-13), as PromoteReleaseRequest tags 13–14.
	//
	// Wherever a converger applies promotions, writing the promotion IS the
	// deploy, so the review has to precede the record and this names which
	// review. The server RECOMPUTES the plan under the env row lock and
	// refuses on a mismatch (reasonPlanStale) or an unacknowledged
	// stop-class finding (reasonPlanUnacknowledged) — it never trusts the
	// digest as submitted, which is what makes it a guarantee rather than a
	// claim.
	//
	// Both are sent only when set. A control plane that predates O-13
	// discards them as unknown fields, so a new forge can send them before
	// the server deploys; a promote with no plan is still recorded, and the
	// server decides whether its own env requires one.
	//
	// ApprovedBy is NOT sent. The server sets it from the credential: an
	// approval whose approver the approver chose is not an approval (O-11).
	if p.PlanDigest != "" {
		req["planDigest"] = p.PlanDigest
	}
	if len(p.AcknowledgedFindings) > 0 {
		req["acknowledgedFindings"] = p.AcknowledgedFindings
	}
	guardWireFields(guard, req)
	var resp struct {
		Promotion wirePromotion `json:"promotion"`
		// Holds is non-empty when the control plane ACCEPTED and RECORDED
		// the promotion but QUEUED it on a human action (billing). Read
		// off the write itself, so the deploy knows at once — with no
		// extra call, even under --no-wait.
		Holds []deploytarget.HostedHold `json:"holds,omitempty"`
	}
	if err := s.client.Call(ctx, procPromote, req, &resp); err != nil {
		if refused := s.refusalFromWire(p.Env, err); refused != nil {
			return release.Promotion{}, refused
		}
		return release.Promotion{}, err
	}
	written, err := s.promotionFromWire(p.Env, resp.Promotion)
	if err == nil {
		s.remember(ctx, p.Env, written, true)
	}
	if err == nil && len(resp.Holds) > 0 {
		s.mu.Lock()
		if s.holds == nil {
			s.holds = map[string][]deploytarget.HostedHold{}
		}
		s.holds[written.ID] = resp.Holds
		s.mu.Unlock()
	}
	return written, err
}

// QueuedHolds reports what the control plane said, on the write, that a
// promotion this store appended is waiting on. Nil for a promotion admitted
// outright — or appended by someone else, which this store cannot know.
func (s *hostedStore) QueuedHolds(promotionID string) []deploytarget.HostedHold {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holds[promotionID]
}

// Cut calls CutRelease.
func (s *hostedStore) Cut(ctx context.Context, r release.Release) (bool, error) {
	if err := r.Validate(); err != nil {
		return false, err
	}
	req := map[string]any{
		"version":   r.Version,
		"artifacts": releaseToWire(r),
		"gitCommit": r.Git.Commit,
		"gitTag":    r.Git.Tag,
		"gitDirty":  r.Git.Dirty,
	}
	if run := runWireFields(r.Run); run != nil {
		req["run"] = run
	}
	// The full provenance, beside the three legacy git fields rather than
	// instead of them. gitCommit/Tag/Dirty are what a control plane that
	// predates provenance reads, and dropping them would make a new forge
	// record LESS than an old one against such a server; they are filled
	// from the same provenance (Release.SetProvenance), so the two cannot
	// disagree.
	if r.Provenance != nil {
		req["provenance"] = deploytarget.ProvenanceWireFields(*r.Provenance)
	}
	if r.Commits != nil {
		req["commits"] = commitRangeToWire(*r.Commits)
	}
	// The project the version belongs to. A release's identity on the
	// control plane is (org, version), so without this a second project in
	// the same org cutting "v1.0.0" collides with the first — and the
	// collision is reported as "a release of that version already exists
	// with different artifacts", which is true and useless.
	if r.Project != "" {
		req["project"] = r.Project
	}
	if len(r.Catalog) > 0 || len(r.SnapshotIntents) > 0 {
		if err := requireSnapshotCapabilities(ctx, s.client); err != nil {
			return false, err
		}
		// Exclusive typed document: no lossy legacy artifact flattening or
		// duplicated provenance/run fields against the strict server adapter.
		req = map[string]any{"version": r.Version, "project": r.Project, "snapshotRelease": r}
	}
	var resp struct {
		Created bool `json:"created"`
	}
	if err := s.client.Call(ctx, procCutRelease, req, &resp); err != nil {
		// THE CONNECT CODE, not the message text. A re-cut of a version
		// whose artifacts differ is the one failure this turns into a
		// domain error, and recognising it by searching the message
		// would break the moment the server reworded it — silently,
		// because the branch simply stops matching and the conflict
		// falls through as a generic failure.
		if hostedErrorHasCode(err, cloud.CodeAlreadyExists) {
			return false, fmt.Errorf("release %q: %w: %v", r.Version, release.ErrReleaseConflict, err)
		}
		return false, err
	}
	return resp.Created, nil
}

// Get calls GetRelease; a NotFound answer is (nil, nil), matching the file
// backend's "never cut".
func (s *hostedStore) Get(ctx context.Context, version string) (*release.Release, error) {
	var resp struct {
		Release wireRelease `json:"release"`
	}
	// Scoped to this project, for the reason Cut sends it: a version is
	// unique per ORG, so an unscoped read of "v1.0.0" in an org with two
	// forge projects can return the other project's release — whose
	// artifacts a deploy would then pin.
	req := map[string]any{"version": version}
	if s.project != "" {
		req["project"] = s.project
	}
	if err := s.client.Call(ctx, procGetRelease, req, &resp); err != nil {
		// not_found is the one code this read turns into an ANSWER
		// rather than a failure: a version nobody cut is the file
		// backend's "never cut", not an error.
		if hostedErrorHasCode(err, cloud.CodeNotFound) {
			return nil, nil
		}
		return nil, err
	}
	r, err := releaseFromWire(resp.Release)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// List calls ListReleases and orders the result the way the file backend
// does, so promote's direction logic sees one ordering whichever backend
// answered.
func (s *hostedStore) List(ctx context.Context) ([]release.Release, error) {
	var resp struct {
		Releases []wireRelease `json:"releases"`
	}
	// Scoped to this project: promote's direction logic orders this list to
	// decide whether a promote moves forward or back, and another project's
	// versions interleaved into it would make that ordering meaningless.
	req := map[string]any{"limit": hostedReleaseListLimit}
	if s.project != "" {
		req["project"] = s.project
	}
	if err := s.client.Call(ctx, procListReleases, req, &resp); err != nil {
		return nil, err
	}
	out := make([]release.Release, 0, len(resp.Releases))
	for _, w := range resp.Releases {
		r, err := releaseFromWire(w)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sortReleasesNewestFirst(out)
	return out, nil
}
