// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/mergequeue/ghport"
	"github.com/reliant-labs/reliant/internal/mergequeue/gitport"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
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
)

func refuse(code, format string, args ...any) Refusal {
	return Refusal{Code: code, Reason: fmt.Sprintf(format, args...)}
}

// admission decides §3.1's queue.admissible(pr, head) from DB state and the
// repository's resolved config.
type admission struct {
	store Store
	now   func() time.Time
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

// changedPaths lists the paths head changes against the base branch's tip:
// the diff from their merge base, rename detection off.
func changedPaths(ctx context.Context, wd Workdir, repo ghport.Repo, baseBranch, head string) ([]string, error) {
	tip, err := wd.GitHub.GetRef(ctx, repo, ghport.BranchRef(baseBranch))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", baseBranch, err)
	}
	if err := ensureCommits(ctx, wd, tip.SHA, head); err != nil {
		return nil, err
	}
	mb, err := gitOut(ctx, wd.Git.Dir(), "merge-base", tip.SHA, head)
	if err != nil {
		return nil, err
	}
	out, err := gitOut(ctx, wd.Git.Dir(), "diff-tree", "-r", "-z", "--name-only", "--no-renames", strings.TrimSpace(mb), head)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" && !slices.Contains(paths, p) {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	return paths, nil
}

// ensureCommits fetches the commits the workdir does not have.
func ensureCommits(ctx context.Context, wd Workdir, shas ...string) error {
	var missing []string
	for _, s := range shas {
		if _, err := wd.Git.ResolveCommit(ctx, s); err != nil && !slices.Contains(missing, s) {
			missing = append(missing, s)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return wd.Git.Fetch(ctx, wd.Remote, missing...)
}

func gitOut(ctx context.Context, gitDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + gitDir}, args...)...)
	cmd.Env = gitport.HermeticEnv(gitport.DefaultIdentity())
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}
