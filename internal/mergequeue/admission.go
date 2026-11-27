// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
	"github.com/reliant-labs/reliant/internal/pipeline/grants"
	"github.com/reliant-labs/reliant/internal/pipeline/prpolicy"
)

// Refusal is why a PR head is not admissible. The zero value admits.
type Refusal struct {
	Code   string
	Reason string
}

// Admissible reports whether the refusal admits.
func (r Refusal) Admissible() bool { return r.Code == "" }

// Refusal codes.
const (
	RefuseNotEnrolled   = "not_enrolled"
	RefuseModeOff       = "mode_off"
	RefuseNotOpen       = "not_open"
	RefuseDraft         = "draft"
	RefuseHeadMoved     = "head_moved"
	RefuseNoPass        = "no_pass_verdict"
	RefuseHeld          = "held"
	RefuseDecisions     = "decisions_required"
	RefuseCheckTier     = "check_tier"
	RefuseOtherHeadInQ  = "queued_at_another_head"
	RefuseUnknownRecord = "unknown_record"
	// RefuseLandNotGranted: in mode on, the run that approved the head does
	// not hold the land capability (internal/pipeline/grants).
	RefuseLandNotGranted = "land_not_granted"
)

func refuse(code, format string, args ...any) Refusal {
	return Refusal{Code: code, Reason: fmt.Sprintf(format, args...)}
}

// admission decides §3.1's queue.admissible(pr, head) from DB state and the
// repository's resolved config.
type admission struct {
	store Store
	// auth decides the land capability in mode on; nil grants nothing.
	auth grants.Authorizer
	now  func() time.Time
}

// admit decides whether rec's head is admissible in repo under cfg. paths
// lists the head's changed paths against the base branch; it is called only
// when an owner_approval entry needs them.
func (a admission) admit(ctx context.Context, repo *core.PipelineRepo, cfg config.Resolved, rec *core.PRRecord, head string,
	paths func(context.Context) ([]string, error)) (Refusal, error) {
	switch {
	case rec == nil:
		return refuse(RefuseUnknownRecord, "no PR record"), nil
	case rec.State != core.PRRecordOpen:
		return refuse(RefuseNotOpen, "the PR is %s", rec.State), nil
	case rec.Draft:
		return refuse(RefuseDraft, "the PR is a draft"), nil
	case rec.HeadSHA != head:
		return refuse(RefuseHeadMoved, "the PR's head is %s, not %s", rec.HeadSHA, head), nil
	}
	v, err := a.store.LatestPRHeadVerdict(ctx, rec.ID, head)
	if err != nil {
		return Refusal{}, err
	}
	if v == nil || v.Verdict != core.PRHeadVerdictPass {
		return refuse(RefuseNoPass, "no pass verdict on %s", head), nil
	}
	if repo.Mode == core.PipelineRepoModeOn {
		// Checked here, not only before the merge, so an entry whose
		// approver cannot land waits unbuilt instead of being rebuilt
		// every tick.
		if r, err := landGranted(ctx, a.auth, repo, v); err != nil || !r.Admissible() {
			return r, err
		}
	}
	now := a.now()
	hold, err := a.store.ActivePipelineHold(ctx, rec.ID, now)
	if err != nil {
		return Refusal{}, err
	}
	if hold != nil {
		return refuse(RefuseHeld, "held by decision %s", hold.ID), nil
	}
	unmet, err := a.unmetDecisions(ctx, cfg, rec, head, v, now, paths)
	if err != nil {
		return Refusal{}, err
	}
	if len(unmet) > 0 {
		kinds := make([]string, len(unmet))
		for i, d := range unmet {
			kinds[i] = string(d.Kind)
		}
		return refuse(RefuseDecisions, "owner decisions required on %s: %s", head, strings.Join(kinds, ", ")), nil
	}
	if r, err := a.checkTier(ctx, cfg, rec, head); err != nil || !r.Admissible() {
		return r, err
	}
	return Refusal{}, nil
}

// unmetDecisions is the owner decisions the head needs and does not have.
// What is required is the union of what the verdict recorded (its review
// evaluated the conditional owner_approval entries, when_cmd and when, in
// the sandbox) and what the repository's current config requires of the
// head's paths without conditions, recomputed here so a config change since
// the review counts.
func (a admission) unmetDecisions(ctx context.Context, cfg config.Resolved, rec *core.PRRecord, head string, v *core.PRHeadVerdictRecord,
	now time.Time, paths func(context.Context) ([]string, error)) ([]prpolicy.RequiredDecision, error) {
	var required []prpolicy.RequiredDecision
	for _, k := range v.RequiredDecisions {
		required = append(required, prpolicy.RequiredDecision{Kind: config.ApprovalKind(k), Head: head})
	}
	conds := prpolicy.ApprovalConditions{WhenCmd: map[int]bool{}, When: map[int]bool{}}
	needPaths := false
	for i, ap := range cfg.OwnerApproval {
		// A conditional entry is the verdict's to have decided.
		if ap.WhenCmd != "" {
			conds.WhenCmd[i] = false
		}
		if ap.When != "" {
			conds.When[i] = false
		}
		needPaths = needPaths || (ap.WhenCmd == "" && ap.When == "" && len(ap.Paths) > 0)
	}
	var changed []string
	if needPaths {
		var err error
		if changed, err = paths(ctx); err != nil {
			return nil, fmt.Errorf("changed paths of %s: %w", head, err)
		}
	}
	for _, d := range prpolicy.RequiredDecisions(head, changed, "", cfg.OwnerApproval, conds) {
		if !slices.ContainsFunc(required, func(r prpolicy.RequiredDecision) bool { return r.Kind == d.Kind }) {
			required = append(required, d)
		}
	}
	if len(required) == 0 {
		return nil, nil
	}
	decisions, err := a.store.ActivePipelineDecisionsFor(ctx, core.PipelineDecisionSubjectPRHead, rec.ID, head, now)
	if err != nil {
		return nil, err
	}
	var approved []prpolicy.ApprovedDecision
	for _, d := range decisions {
		approved = append(approved, prpolicy.ApprovedDecision{Kind: config.ApprovalKind(d.Kind), Head: d.HeadSHA})
	}
	return prpolicy.UnmetDecisions(required, approved), nil
}

// checkTier requires each check the repository's check tier names to have
// passed on the head, as the webhook projection recorded it. A check tier
// that is only a command (check.run) is judged by the pr workflow's check
// node before it enqueues (pr.checked@1), not here.
func (a admission) checkTier(ctx context.Context, cfg config.Resolved, rec *core.PRRecord, head string) (Refusal, error) {
	if cfg.Check == nil || len(cfg.Check.Checks) == 0 {
		return Refusal{}, nil
	}
	checks, err := a.store.ListQueueHeadChecks(ctx, rec.ID, head)
	if err != nil {
		return Refusal{}, err
	}
	for _, name := range cfg.Check.Checks {
		ok := slices.ContainsFunc(checks, func(c core.PRRecordCheck) bool {
			return c.Name == name && c.Status == string(ghport.CheckCompleted) && c.Conclusion == string(ghport.ConclusionSuccess)
		})
		if !ok {
			return refuse(RefuseCheckTier, "check %q has not passed on %s", name, head), nil
		}
	}
	return Refusal{}, nil
}

// changedPaths lists the paths head changes against the base branch's tip,
// as a gate pool daemon computes them (the queue holds no repository bytes):
// the server reads the tip through the GitHub API and the daemon diffs.
func changedPaths(ctx context.Context, refs Refs, builder Builder, repo *core.PipelineRepo, head string) ([]string, error) {
	tip, err := refs.BranchTip(ctx, repo, repo.BaseBranch)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", repo.BaseBranch, err)
	}
	res, err := builder.ChangedPaths(ctx, repo, buildjob.PathsRequest{Repository: repo.Repository, Base: tip, Head: head})
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range res.Paths {
		if p != "" && !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	return paths, nil
}

// landGranted holds a pass verdict to the owner's land grant: the run that
// wrote it must be an activation's, on a machine, and that activation must
// hold the land capability on a pool the machine is a member of, judged now
// (a grant revoked since the review counts). A verdict that names no
// activation or machine (written before verdicts recorded them, or by a
// person's run) cannot land. A refusal is RefuseLandNotGranted; an error is
// a read that failed.
func landGranted(ctx context.Context, auth grants.Authorizer, repo *core.PipelineRepo, v *core.PRHeadVerdictRecord) (Refusal, error) {
	switch {
	case auth == nil:
		return refuse(RefuseLandNotGranted, "this server checks no land grants, so nothing lands"), nil
	case v.TriggerID == "" || v.DaemonID == "":
		return refuse(RefuseLandNotGranted, "the verdict on %s names no activation and machine to hold the land grant", v.HeadSHA), nil
	}
	err := auth.Authorize(ctx, grants.Run{RunID: v.RunID, TriggerID: v.TriggerID, DaemonID: v.DaemonID, RepoID: repo.ID}, grants.Land)
	switch {
	case err == nil:
		return Refusal{}, nil
	case errors.Is(err, grants.ErrNotAuthorized):
		return refuse(RefuseLandNotGranted, "the run that approved %s may not land it: %v", v.HeadSHA, err), nil
	}
	return Refusal{}, fmt.Errorf("land grant of %s: %w", v.HeadSHA, err)
}
