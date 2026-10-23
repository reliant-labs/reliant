package cli

// Reading the control plane's apply records. THERE IS NO WRITE HALF.
//
// WHAT WENT, AND WHY. This file used to own BeginApply, FinishApply,
// FinishApplyWithObserved and ListApplies — the bracket around a forge-driven
// apply, with a compare-and-set on the way in and a reported outcome on the
// way out. All four are gone because NOTHING EVER CALLED THEM. Forge's own
// cluster apply (cluster.Apply, through the deploy dispatch) reports its
// outcome by succeeding or failing, and no verb in this package brackets it
// with a durable record. The four RPCs, the guard encoder and the observed-
// objects variant were a client for a protocol with no caller on either side.
//
// That is the whole justification: dead code, removed. It is deliberately NOT
// the claim that forge does not apply — it does today. The direct apply
// narrows to dev and ephemeral clusters only LATER, once every real env runs
// through build -> OCI bundle -> version store -> Flux and control-plane is
// itself on that path. This deletion neither performs nor depends on that
// step.
//
// What remains is the DECODE, which has a real consumer: hosted_live.go reads
// these records through GetLiveView. They are OBSERVATIONS the control plane's
// own observer wrote — append-only, never required for correctness — and
// reading what another system reports was never the half that lacked a caller.
//
// The reported-by label survives in the wire shape and is still set by the
// SERVER rather than sent, because the record remains a claim by whoever made
// it and a reader has to see whose. If a verb ever does want to report an
// apply of its own, the request shape should be designed for that verb rather
// than restored from here.

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// ─── Wire shapes ─────────────────────────────────────────────────────────────

// wireApplyOutcome is controlplane.v1.DeployApplyOutcome.
//
// Workloads is a google.protobuf.Struct and is kept raw for the reason
// shapeFromWire keeps a shape raw: the reporting shape is its own, and a
// camelCase mirror of it here could only ever disagree with
// release.ApplyWorkload. The Struct's content is the canonical JSON of forge's
// own type, so release's snake_case tags decode it directly.
type wireApplyOutcome struct {
	Status     string          `json:"status"`
	Summary    string          `json:"summary,omitempty"`
	Workloads  json.RawMessage `json:"workloads,omitempty"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
	// ReportedBy is SET BY THE SERVER from the credential that reported.
	// The label that keeps the report honest: a reader sees whose claim
	// this is. forge reads it and never sends it.
	ReportedBy string `json:"reportedBy,omitempty"`
}

// wireApply is controlplane.v1.DeployApply.
//
// `admittedFindings` (tag 7) is reserved for #516 and absent here.
type wireApply struct {
	ID            string   `json:"id"`
	EnvironmentID string   `json:"environmentId,omitempty"`
	BundleID      string   `json:"bundleId,omitempty"`
	PromotionID   string   `json:"promotionId,omitempty"`
	AppliedBy     string   `json:"appliedBy,omitempty"`
	Run           *wireRun `json:"run,omitempty"`
	// SupersededInFlight: this apply was started over one that had not
	// finished. Recorded because an override that leaves no trace is
	// indistinguishable from the refusal never having fired.
	SupersededInFlight bool              `json:"supersededInFlight,omitempty"`
	CreatedAt          time.Time         `json:"createdAt"`
	DeadlineAt         *time.Time        `json:"deadlineAt,omitempty"`
	Outcome            *wireApplyOutcome `json:"outcome,omitempty"`
	// State is DERIVED on read: running | succeeded | failed | timed_out |
	// abandoned. Read THIS, not the presence of Outcome, to decide what
	// happened — an absent outcome means two different things and the
	// deadline is what separates them.
	State string `json:"state,omitempty"`
	// PlanDigest and ApprovedBy are the plan this record was approved
	// against, and who approved it. The approver comes from the credential,
	// never from the client (O-11).
	PlanDigest string `json:"planDigest,omitempty"`
	ApprovedBy string `json:"approvedBy,omitempty"`
}

// applyFromWire converts one record. env is the NAME the caller asked about.
// It returns the record and its outcome SEPARATELY, mirroring the two facts
// they are: a caller that folds them has to do so knowingly, and
// release.DeriveApplyState is the folding every reader shares.
func applyFromWire(env string, w wireApply) (release.Apply, *release.ApplyOutcome, error) {
	a := release.Apply{
		ID: w.ID, Env: env, BundleID: w.BundleID, PromotionID: w.PromotionID,
		AppliedBy: w.AppliedBy, Run: runFromWire(w.Run),
		PlanDigest: w.PlanDigest, SupersededInFlight: w.SupersededInFlight,
		CreatedAt: w.CreatedAt,
	}
	if w.DeadlineAt != nil {
		a.DeadlineAt = *w.DeadlineAt
	}
	outcome, err := applyOutcomeFromWire(w.ID, w.Outcome)
	if err != nil {
		return release.Apply{}, nil, fmt.Errorf("control plane returned apply %s: %w", w.ID, err)
	}
	return a, outcome, nil
}

func applyOutcomeFromWire(applyID string, w *wireApplyOutcome) (*release.ApplyOutcome, error) {
	if w == nil {
		return nil, nil
	}
	o := release.ApplyOutcome{
		ApplyID: applyID, Status: release.ApplyStatus(w.Status),
		Summary: w.Summary, ReportedBy: w.ReportedBy,
	}
	if w.FinishedAt != nil {
		o.FinishedAt = *w.FinishedAt
	}
	if len(w.Workloads) > 0 && string(w.Workloads) != "null" {
		// A google.protobuf.Struct is an OBJECT, never a bare list, so the
		// per-resource results ride under a `workloads` member rather than
		// as the Struct itself.
		var envelope struct {
			Workloads []release.ApplyWorkload `json:"workloads"`
		}
		if err := json.Unmarshal(w.Workloads, &envelope); err != nil {
			// The per-resource detail is DISPLAY data hanging off a
			// terminal outcome. One forge cannot decode must not make the
			// outcome itself unreadable, so the status survives and the
			// detail is reported as the error it is.
			return nil, fmt.Errorf("apply outcome workloads: %w", err)
		}
		o.Workloads = envelope.Workloads
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return &o, nil
}

// ApplyRecord is one convergence record as a READER sees it: what was
// attempted, its outcome if one arrived, and the state derived from both plus
// the clock.
type ApplyRecord struct {
	Apply   release.Apply
	Outcome *release.ApplyOutcome
	// State is release.DeriveApplyState's answer. Carried rather than
	// recomputed by each caller so "abandoned" is drawn at one place.
	State release.ApplyState
}
