// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/grants"
	"github.com/reliant-labs/reliant/internal/pipeline/prpolicy"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// reviewOutput is a review node's output (reliant.v1.ReviewOutput) as the
// engine recorded it.
type reviewOutput struct {
	Verdict   string             `json:"verdict"`
	Findings  []prpolicy.Finding `json:"findings"`
	RiskTier  string             `json:"risk_tier"`
	Summary   string             `json:"summary"`
	SessionID string             `json:"session_id"`
	Role      string             `json:"role"`
}

func parseReviewOutput(out map[string]any) (reviewOutput, error) {
	var r reviewOutput
	b, err := json.Marshal(out)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r)
	return r, err
}

// Verdict is reliant/pr.verdict@1: it records the verdict of the review node
// `with: {from:}` names on the PR's head, and says what the head needs next
// (fix, enqueue or escalate), as pr.next would.
//
// Writing this row is the only way a model verdict exists, so it is
// refused unless every one of these holds (§4.6, §5, G2):
//
//   - the node is an engine-owned review node whose definition declares
//     role: reviewer, and its recorded output says it ran as one, in a
//     session of its own;
//   - the action is a top-level node of the run's own workflow: a node in a
//     nested workflow (a `ref:` one above all) reports its root's
//     provenance, not its own;
//   - the run's workflow is built in, or is the repository's own file read
//     at its base branch from a pinned snapshot with nothing uncommitted;
//   - the reviewer session never worked on a fix of this PR;
//   - the head is still the PR's, and this run's review of it is the one in
//     flight (a head that moved is refused).
func (s *Service) Verdict(ctx context.Context, req *serveraction.Request) (*serveraction.Result, error) {
	p, err := s.loadActive(ctx, req)
	if err != nil {
		return nil, err
	}
	from, _ := req.Params["from"].(string)
	node := req.From
	if node == nil || node.NodeID != from {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition, "node %q is not in this action's workflow", from)
	}
	if !node.Review || node.Role != model.RoleReviewer {
		return nil, serveraction.Errorf(serveraction.CodePermissionDenied,
			"only a review node with role: %s may give a verdict; %q is not one", model.RoleReviewer, from)
	}
	if !node.Ran {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition, "review node %q has not run", from)
	}
	out, err := parseReviewOutput(node.Output)
	if err != nil || out.Role != model.RoleReviewer || out.SessionID == "" {
		return nil, serveraction.Errorf(serveraction.CodePermissionDenied, "review node %q's output is not a reviewer's verdict", from)
	}
	inv := req.Invocation
	if inv.NodePath != "" && inv.NodePath != inv.NodeID {
		return nil, serveraction.Errorf(serveraction.CodePermissionDenied,
			"a verdict is written only by a top-level node of the run's own workflow; %s is nested", inv.NodePath)
	}
	if err := checkProvenance(req.Provenance, p.repo); err != nil {
		return nil, err
	}
	if out.SessionID == inv.ThreadID {
		return nil, serveraction.Errorf(serveraction.CodePermissionDenied, "the review ran in the run's own session, not a reviewer's")
	}
	principal := "reviewer:" + out.SessionID

	head := p.row.StateHeadSHA
	verdicts, err := s.deps.Store.ListPRHeadVerdicts(ctx, p.rec.ID)
	if err != nil {
		return nil, err
	}
	// A retry of a verdict already written answers as the first call did.
	for _, v := range verdicts {
		if v.HeadSHA == head && v.RunID == inv.RunID && v.NodeID == from && v.ReviewerPrincipal == principal {
			return s.verdictResult(ctx, p, v)
		}
	}
	if err := s.sessionNeverFixed(ctx, p, out.SessionID); err != nil {
		return nil, err
	}

	pr, err := s.deps.GitHub.PullRequest(ctx, p.as, p.rec.Repository, p.rec.Number)
	if err != nil {
		return nil, &serveraction.Error{Code: serveraction.CodeUnavailable, Retryable: true, Message: fmt.Sprintf("could not read %s from GitHub", prName(p))}
	}
	if pr.HeadSHA != head {
		return nil, serveraction.Errorf(serveraction.CodeAborted,
			"%s's head moved from %s to %s since the review started; a verdict on the old head is not recorded", prName(p), short(head), short(pr.HeadSHA))
	}
	if p.row.State != core.PRPipelineReviewing || p.row.OwnerRunID != inv.RunID {
		return nil, serveraction.Errorf(serveraction.CodeAborted, "no review of %s's head %s is in flight for this run", prName(p), short(head))
	}

	reviews, err := s.attempts(ctx, p.rec.ID, core.PipelineAttemptReview, attemptReview)
	if err != nil {
		return nil, err
	}
	current := running(reviews, inv.RunID, head)
	var prior *core.PRHeadVerdictRecord
	if len(verdicts) > 0 {
		prior = verdicts[len(verdicts)-1]
	}
	dec := prpolicy.Converge(findingsOf(prior), out.Findings, newCode(prior, reviews, current))

	verdict := core.PRHeadVerdict(out.Verdict)
	if verdict == core.PRHeadVerdictChanges && !dec.Blocks() {
		verdict = core.PRHeadVerdictPass // what still blocks is only late findings: the review converged
	}
	if !slices.Contains(core.AllPRHeadVerdicts, verdict) {
		return nil, serveraction.Errorf(serveraction.CodeInvalidArgument, "review node %q gave no verdict", from)
	}
	detail := detailOf(current)
	risk := riskOf(detail.Risk, out.RiskTier)
	var paths []string
	for path := range detail.Files {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	required := requiredDecisions(p.cfg, head, paths)
	if detail.FilesUnknown {
		required = requiredDecisionsAnyPath(p.cfg, head)
	}
	counted := dec.Findings
	if counted == nil {
		counted = []prpolicy.Finding{}
	}
	findings, _ := json.Marshal(counted)
	round := 1
	if v := latestOnHead(verdicts, head); v != nil {
		round = v.Round + 1
	}

	if verdict == core.PRHeadVerdictPass {
		if err := s.authorizeApprove(ctx, p); err != nil {
			return nil, err
		}
	}

	var written *core.PRHeadVerdictRecord
	err = s.deps.Store.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		v, err := s.deps.Store.InsertPRHeadVerdict(ctx, core.PRHeadVerdictRecord{
			OrgID: p.repo.OrgID, ProjectID: p.repo.ProjectID, RecordID: p.rec.ID, HeadSHA: head, Round: round,
			Verdict: verdict, Rule: core.PRVerdictRuleModel, RiskTier: risk, FindingsJSON: findings,
			RequiredDecisions: required,
			RunID:             inv.RunID, NodeID: from, ReviewerPrincipal: principal,
			// The activation and machine the verdict came from, so the
			// land check can ask whether that activation still holds land.
			TriggerID: req.Principal.TriggerID, DaemonID: req.Principal.DaemonID,
		})
		if err != nil {
			return err
		}
		written = v
		if current != nil {
			detail.Verdict = string(verdict)
			detail.Sessions = append(detail.Sessions, out.SessionID)
			if _, err := s.deps.Store.FinishPipelineAttempt(ctx, current.ID, core.PipelineAttemptSucceeded, detail.raw()); err != nil &&
				!errors.Is(err, core.ErrPipelineVersionConflict) {
				return err
			}
		}
		to := *p.row
		to.ReviewRound = p.row.ReviewRound + 1
		to.State = core.PRPipelineChangesRequested
		if verdict == core.PRHeadVerdictPass {
			to.State = core.PRPipelineApproved
		}
		return s.transition(ctx, p, to)
	})
	if err != nil {
		if errors.Is(err, core.ErrPipelineVerdictExists) {
			return nil, serveraction.Errorf(serveraction.CodeAborted, "round %d of %s's head %s was recorded concurrently", round, prName(p), short(head))
		}
		return nil, casError(err)
	}
	return s.verdictResult(ctx, p, written)
}

// authorizeApprove holds a pass to the owner's grant: the run's activation
// must hold the approve capability on a pool the run's machine is a member
// of (internal/pipeline/grants). In mode on a run without it is refused and
// nothing is written. In shadow mode, which writes nothing to GitHub, the
// refusal is only logged and the verdict is recorded, so a repository can
// be watched in shadow before its owner grants anything.
func (s *Service) authorizeApprove(ctx context.Context, p *prCall) error {
	pr, inv := p.req.Principal, p.req.Invocation
	run := grants.Run{RunID: inv.RunID, ChatID: inv.ChatID, TriggerID: pr.TriggerID, DaemonID: pr.DaemonID, UserID: pr.UserID, RepoID: p.repo.ID}
	var err error
	if s.deps.Grants == nil {
		err = &grants.NotAuthorizedError{Capability: grants.Approve, Reason: grants.ReasonNoGrant, Detail: "this server checks no capability grants"}
	} else {
		err = s.deps.Grants.Authorize(ctx, run, grants.Approve)
	}
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, grants.ErrNotAuthorized):
		return err
	case p.shadow():
		s.log.WarnContext(ctx, "shadow: a pass verdict would be refused without the approve grant; recorded",
			"record_id", p.rec.ID, "repo_id", p.repo.ID, "run_id", inv.RunID, "trigger_id", pr.TriggerID,
			"daemon_id", pr.DaemonID, "reason", string(grants.ReasonOf(err)))
		return nil
	}
	return serveraction.Errorf(serveraction.CodePermissionDenied,
		"%s's pass is not recorded: %v. Ask the owner for it with reliant/pipeline.request_grant@1", prName(p), err)
}

// verdictResult is what the head needs after verdict v: pr.next's answer,
// reached the way pr.next reaches it (a changes verdict starts the fix
// attempt here, because the workflow goes from the verdict straight to the
// fixer).
func (s *Service) verdictResult(ctx context.Context, p *prCall, v *core.PRHeadVerdictRecord) (*serveraction.Result, error) {
	d, err := s.afterVerdict(ctx, p, v)
	if err != nil {
		return nil, err
	}
	r := d.result()
	r.Data["verdict"] = string(v.Verdict)
	r.Data["round"] = v.Round
	return r, nil
}

// checkProvenance holds the run's workflow to G2: built in, or the
// repository's own file read at its base branch from a pinned snapshot with
// nothing uncommitted. Anything else, an unknown source included, is not
// authorized to write a verdict.
func checkProvenance(pv serveraction.Provenance, repo *core.PipelineRepo) error {
	deny := func(format string, args ...any) error {
		return serveraction.Errorf(serveraction.CodePermissionDenied, "this run's workflow may not write a verdict: "+format, args...)
	}
	switch pv.Source {
	case serveraction.SourceBuiltin:
		return nil
	case serveraction.SourceProject:
		r := pv.Repo
		switch {
		case pv.CatalogPin == "" || r == nil || r.Commit == "":
			return deny("its file %s was not read from a known snapshot", pv.Path)
		case r.Dirty:
			return deny("its file %s was read with uncommitted changes", pv.Path)
		case r.Branch != repo.BaseBranch:
			return deny("its file %s was read at %q, not at the base branch %q", pv.Path, r.Branch, repo.BaseBranch)
		}
		return nil
	case serveraction.SourceUser:
		return deny("it is an account workflow, not the repository's")
	default:
		return deny("where it came from is unknown")
	}
}

// sessionNeverFixed refuses a reviewer session that worked on a fix of the
// PR: a session that pushed (or checked a fix) is the author, and the author
// cannot approve itself (§5).
func (s *Service) sessionNeverFixed(ctx context.Context, p *prCall, session string) error {
	all, err := s.deps.Store.ListPipelineAttempts(ctx, core.PipelineAttemptSubjectPR, p.rec.ID, core.PipelineAttemptFix)
	if err != nil {
		return err
	}
	for _, a := range all {
		if slices.Contains(detailOf(a).Sessions, session) {
			return serveraction.Errorf(serveraction.CodePermissionDenied,
				"the reviewer session worked on a fix of %s (attempt %s); the author cannot approve itself", prName(p), a.ID)
		}
	}
	return nil
}

// newCode is the code this round may block on for the first time: the
// whole PR on its first round, else the files whose blob changed since the
// prior verdict's head was reviewed. When either side's files are unknown,
// every file of the PR is new code, so nothing is let through on a guess.
func newCode(prior *core.PRHeadVerdictRecord, reviews []*core.PipelineAttempt, current *core.PipelineAttempt) prpolicy.NewCode {
	if prior == nil {
		return prpolicy.FirstRound()
	}
	now := detailOf(current).Files
	var before map[string]string
	for i := len(reviews) - 1; i >= 0; i-- {
		if reviews[i].HeadSHA == prior.HeadSHA && len(detailOf(reviews[i]).Files) > 0 {
			before = detailOf(reviews[i]).Files
			break
		}
	}
	if now == nil || before == nil {
		if now == nil {
			return prpolicy.FirstRound()
		}
		paths := make([]string, 0, len(now))
		for p := range now {
			paths = append(paths, p)
		}
		return prpolicy.ChangedSince(paths...)
	}
	var changed []string
	for p, blob := range now {
		if before[p] != blob {
			changed = append(changed, p)
		}
	}
	for p := range before {
		if _, ok := now[p]; !ok {
			changed = append(changed, p)
		}
	}
	slices.Sort(changed)
	return prpolicy.ChangedSince(changed...)
}

// riskOf is the higher of the tier pr.next computed from the diff and the
// reviewer's own call.
func riskOf(computed, reviewer string) core.PRRiskTier {
	rank := map[string]int{"low": 1, "medium": 2, "high": 3}
	best := computed
	if rank[reviewer] > rank[best] {
		best = reviewer
	}
	if rank[best] == 0 {
		return core.PRRiskMedium
	}
	return core.PRRiskTier(best)
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
