package cli

// PlanDeploy (doc §6.3, owner decision O-13): the pre-deploy plan, computed
// SERVER-SIDE against recorded bundles.
//
// WHY THE SERVER COMPUTES IT. With a promotion converger on, writing a
// promotion IS the deploy — nothing stands between the write and the rollout.
// So the review has to happen before the record, and the approval has to be
// bound to exactly what was reviewed. If the client computed the plan, a
// client could approve a plan it fabricated; because the server recomputes it
// under the env row lock at approval time and compares digests, the digest is
// a guarantee instead of a claim.
//
// WHY THIS IS A SEPARATE RPC rather than `dry_run` on Promote. A boolean that
// makes a mutating RPC not mutate is the shape where one wrong default
// deploys prod. A read-only RPC cannot have that failure.
//
// THE CLIENT-SIDE DIGEST CHECK. After decoding, forge recomputes the digest
// with release.Plan.VerifyDigest and REFUSES a plan whose digest does not
// recompute. This is not defence against a hostile server — a server that
// wanted to lie would simply send a self-consistent lie. It is the check that
// both sides are running the SAME release.BuildPlan: the digest covers the
// plan's meaning, so if forge's recomputation disagrees with the server's,
// either the two forge versions differ or a field was added on one side
// without the other. Catching that here, as a refusal, is the difference
// between discovering a version skew now and discovering it as a
// `plan_stale` refusal at approval time, which would look like "someone
// promoted under me" and send an operator to the wrong investigation.

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

const procPlanDeploy = "controlplane.v1.DeployService/PlanDeploy"

// ─── Wire shapes ─────────────────────────────────────────────────────────────

// wirePlanBasis is controlplane.v1.DeployPlanBasis: what the plan was computed
// AGAINST. A change here invalidates the digest, which is the whole mechanism —
// the promote CAS extended from the promotion pointer to cover the state the
// plan was reasoned over.
type wirePlanBasis struct {
	CurrentPromotionID  string `json:"currentPromotionId,omitempty"`
	AppliedBundleID     string `json:"appliedBundleId,omitempty"`
	AppliedConfigDigest string `json:"appliedConfigDigest,omitempty"`
	DriftObserved       bool   `json:"driftObserved,omitempty"`
}

// wirePlanFinding is controlplane.v1.DeployPlanFinding.
//
// Code and Class are plain strings on the wire and are parsed into release's
// closed sets by planFromWire, so an unknown value is a named refusal rather
// than a silent zero: a finding whose class forge cannot read must never
// default to `info`, which is the class a blanket approval covers.
type wirePlanFinding struct {
	Code    string `json:"code"`
	Class   string `json:"class"`
	Section string `json:"section,omitempty"`
	Subject string `json:"subject,omitempty"`
	// Detail is human-readable and NEVER a secret value — a plan is printed
	// in CI logs. It is also not part of the digest: two forge versions may
	// word it differently without the plan meaning anything different.
	Detail string `json:"detail,omitempty"`
}

// wirePlan is controlplane.v1.DeployPlan.
type wirePlan struct {
	Digest         string            `json:"digest"`
	EnvironmentID  string            `json:"environmentId,omitempty"`
	BundleID       string            `json:"bundleId,omitempty"`
	ReleaseVersion string            `json:"releaseVersion,omitempty"`
	LiveBasis      *wirePlanBasis    `json:"liveBasis,omitempty"`
	Findings       []wirePlanFinding `json:"findings,omitempty"`
	// ConfigIdentical: the config_digest short-circuit fired, so there is
	// nothing to apply. A FIELD rather than an empty findings list, because
	// "no differences" and "not computed" must never look the same.
	ConfigIdentical bool `json:"configIdentical,omitempty"`
	// ComputedAt is excluded from the digest: the server recomputes the plan
	// at approval time, and a digest covering its own timestamp could never
	// match the one the client computed a minute earlier.
	ComputedAt *time.Time `json:"computedAt,omitempty"`
}

// errPlanDigestMismatch is a plan whose digest does not recompute from its own
// content. Typed so a caller can tell this apart from a transport failure: the
// control plane answered, and what it answered is internally inconsistent.
var errPlanDigestMismatch = fmt.Errorf("%w: the control plane's deploy plan does not match its own digest", release.ErrInvalid)

// planFromWire converts a plan and VERIFIES its digest.
//
// Findings are decoded through release's closed sets, so a class or code forge
// does not recognise is refused by name rather than defaulted. A plan carrying
// an unreadable finding cannot be approved safely: the unreadable one might be
// the stop-class finding that needed acknowledging.
func planFromWire(w wirePlan) (release.Plan, error) {
	p := release.Plan{
		Digest:          w.Digest,
		EnvironmentID:   w.EnvironmentID,
		BundleID:        w.BundleID,
		ReleaseVersion:  w.ReleaseVersion,
		ConfigIdentical: w.ConfigIdentical,
	}
	if b := w.LiveBasis; b != nil {
		p.LiveBasis = release.PlanBasis{
			CurrentPromotionID:  b.CurrentPromotionID,
			AppliedBundleID:     b.AppliedBundleID,
			AppliedConfigDigest: b.AppliedConfigDigest,
			DriftObserved:       b.DriftObserved,
		}
	}
	if w.ComputedAt != nil {
		p.ComputedAt = *w.ComputedAt
	}
	// Always a list, never nil: release.BuildPlan normalizes an empty plan
	// to [], and the digest body encodes it, so a nil here would digest
	// differently from the same plan built locally.
	p.Findings = []release.PlanFinding{}
	for i, f := range w.Findings {
		class := release.FindingClass(f.Class)
		if !class.Valid() {
			return release.Plan{}, fmt.Errorf("%w: deploy plan finding %d: class %q is not one of info, warn, stop",
				release.ErrInvalid, i, f.Class)
		}
		if !containsFindingCode(f.Code) {
			return release.Plan{}, fmt.Errorf("%w: deploy plan finding %d: code %q is not one forge recognises",
				release.ErrInvalid, i, f.Code)
		}
		p.Findings = append(p.Findings, release.PlanFinding{
			Code: f.Code, Class: class, Section: f.Section, Subject: f.Subject, Detail: f.Detail,
		})
	}
	if p.Digest == "" {
		return release.Plan{}, fmt.Errorf("%w: the control plane's deploy plan carries no digest, so an approval could not name it", release.ErrInvalid)
	}
	ok, err := p.VerifyDigest()
	if err != nil {
		return release.Plan{}, fmt.Errorf("verify deploy plan digest: %w", err)
	}
	if !ok {
		// REFUSED, not warned. The digest is what an approval names, so a
		// plan whose digest does not describe its own content cannot be
		// approved at all — approving it would bind the deploy to a plan
		// nobody can reconstruct, and the server's recompute at approval
		// time would refuse it as stale anyway, with a far more
		// misleading message.
		return release.Plan{}, fmt.Errorf("%w (%s): forge and the control plane are computing different plans from the same inputs — most likely they are running different forge versions",
			errPlanDigestMismatch, p.Digest)
	}
	return p, nil
}

func containsFindingCode(code string) bool {
	for _, known := range release.FindingCodes {
		if code == known {
			return true
		}
	}
	return false
}

// ─── The client ──────────────────────────────────────────────────────────────

// hostedPlanClient computes deploy plans on one control plane.
type hostedPlanClient struct {
	client cloudCaller
}

// PlanDeploy computes the plan for deploying bundleID to environmentID.
//
// A PURE READ: it writes nothing, authorizes nothing, and is safe to call
// repeatedly. releaseVersion is optional — "" for an unreleased bundle.
func (c hostedPlanClient) PlanDeploy(ctx context.Context, environmentID, bundleID, releaseVersion string) (release.Plan, error) {
	if environmentID == "" || bundleID == "" {
		return release.Plan{}, fmt.Errorf("%w: planning a deploy needs an environment and a bundle", release.ErrInvalid)
	}
	req := map[string]any{"environmentId": environmentID, "bundleId": bundleID}
	if releaseVersion != "" {
		req["releaseVersion"] = releaseVersion
	}
	var resp struct {
		Plan wirePlan `json:"plan"`
	}
	if err := c.client.Call(ctx, procPlanDeploy, req, &resp); err != nil {
		return release.Plan{}, bundlesUnsupported(err)
	}
	return planFromWire(resp.Plan)
}
