// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/prpolicy"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// decision is pr.next's answer: `{next, head, findings, blockers, evidence,
// reason}` (the MVP contract), plus the risk tier and fix signature when
// they apply.
type decision struct {
	Next      string
	Head      string
	Reason    string
	Findings  []prpolicy.Finding
	Blockers  []prpolicy.Finding
	Evidence  map[string]any
	Risk      string
	Signature string
}

func (d decision) result() *serveraction.Result {
	evidence := d.Evidence
	if evidence == nil {
		evidence = map[string]any{}
	}
	data := map[string]any{
		"next": d.Next, "head": d.Head, "reason": d.Reason,
		"findings": findingsValue(d.Findings), "blockers": findingsValue(d.Blockers),
		"evidence": jsonValue(evidence),
	}
	if d.Risk != "" {
		data["risk_tier"] = d.Risk
	}
	if d.Signature != "" {
		data["signature"] = d.Signature
	}
	content := d.Next
	if d.Reason != "" {
		content += ": " + d.Reason
	}
	return &serveraction.Result{Data: data, Content: content}
}

// Next is reliant/pr.next@1: what the PR needs now.
//
// It reads the PR's row, its verdicts and attempts, and its head on GitHub,
// and answers one of review, fix, enqueue, close, escalate or none. It
// never answers `answer`: a review or issue comment that woke the run
// (`with: {comment:}`) is handed to the fixer as a blocker instead.
//
// What it writes, always as a compare-and-set on the version it read: a
// new head resets the row; review and fix claim the head for this run and
// start the attempt; a low-risk head gets its verdict here, with no model;
// none clears the due time that woke it. escalate and close leave the row
// to pr.needs_person and pr.close.
func (s *Service) Next(ctx context.Context, req *serveraction.Request) (*serveraction.Result, error) {
	p, err := s.load(ctx, req)
	if err != nil {
		return nil, err
	}
	if p.repo.Mode == core.PipelineRepoModeOff {
		return decision{Next: NextNone, Head: p.row.StateHeadSHA, Reason: "the repository's pipeline mode is off"}.result(), nil
	}
	pr, err := s.deps.GitHub.PullRequest(ctx, p.as, p.rec.Repository, p.rec.Number)
	if err != nil {
		s.log.WarnContext(ctx, "pr.next could not read the PR", "pr", prName(p), "error", err)
		return nil, &serveraction.Error{Code: serveraction.CodeUnavailable, Retryable: true,
			Message: fmt.Sprintf("could not read %s from GitHub", prName(p))}
	}
	d, err := s.decide(ctx, p, pr, req.Params["comment"])
	if err != nil {
		return nil, err
	}
	r := d.result()
	// What the rest of the `pr` workflow needs to act on the answer: the
	// record every later action names, the branches the fixer checks out
	// and pushes to, the mode (shadow pushes nothing), and the fixer's
	// scoped check, built from the base branch's config only, never from
	// anything the PR head carries.
	r.Data["record_id"] = p.rec.ID
	r.Data["repository"] = p.rec.Repository
	r.Data["number"] = p.rec.Number
	r.Data["head_ref"] = pr.HeadRef
	r.Data["base_ref"] = pr.BaseRef
	r.Data["shadow"] = p.shadow()
	r.Data["check_script"] = CheckScript(p.cfg, pr.BaseRef)
	target, subdir, err := s.checkout(ctx, p)
	if err != nil {
		return nil, err
	}
	r.Data["repo_target"], r.Data["repo_subdir"] = target, subdir
	return r, nil
}

// checkout is where the enrolled repository sits in its project, as
// create_worktree names it (target_repo: the registered relative path, or
// the name of a standalone project's only repo) and as a directory inside
// the worktree ("" when the repository is the worktree's root). It matches
// the project's repos by remote, then by name.
func (s *Service) checkout(ctx context.Context, p *prCall) (target, subdir string, err error) {
	repos, err := s.deps.Store.ListReposByProject(ctx, p.repo.ProjectID)
	if err != nil {
		return "", "", err
	}
	_, name, _ := strings.Cut(p.repo.Repository, "/")
	var byName *core.Repo
	for _, r := range repos {
		if r.RemoteURL != nil && remoteNames(*r.RemoteURL, p.repo.Repository) {
			byName = r
			break
		}
		if strings.EqualFold(r.Name, name) && byName == nil {
			byName = r
		}
	}
	switch {
	case byName == nil:
		return name, "", nil
	case byName.RelativePath == "":
		return byName.Name, "", nil
	default:
		return byName.RelativePath, byName.RelativePath, nil
	}
}

// remoteNames reports whether a git remote URL names the GitHub repository
// owner/name (https or ssh, with or without .git).
func remoteNames(remote, repository string) bool {
	r := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(remote), ".git"))
	want := strings.ToLower(repository)
	return strings.HasSuffix(r, "github.com/"+want) || strings.HasSuffix(r, "github.com:"+want)
}

func (s *Service) decide(ctx context.Context, p *prCall, pr PullRequest, comment any) (decision, error) {
	if pr.Merged || pr.State == "closed" {
		state, why := core.PRPipelineClosed, "the PR is closed"
		if pr.Merged {
			state, why = core.PRPipelineLanded, "the PR is merged"
		}
		return s.idle(ctx, p, state, why)
	}
	if pr.HeadSHA != "" && pr.HeadSHA != p.row.StateHeadSHA {
		row, err := s.deps.Store.ResetPRPipelineForHead(ctx, p.rec.ID, p.row.Version, pr.HeadSHA, core.PRPipelineNext{})
		if err != nil {
			return decision{}, casError(err)
		}
		p.row = row
	}
	head := p.row.StateHeadSHA
	if pr.Draft {
		return s.idle(ctx, p, core.PRPipelineDraft, "the PR is a draft")
	}
	if p.row.State == core.PRPipelineDraft {
		p.row.State = core.PRPipelineAwaitingReview // ready for review now
	}
	hold, err := s.deps.Store.ActivePipelineHold(ctx, p.rec.ID, p.now)
	if err != nil {
		return decision{}, err
	}
	if hold != nil {
		return s.idle(ctx, p, p.row.State, "held by decision "+hold.ID)
	}
	if o := p.row.OwnerRunID; o != "" && o != p.as.RunID && p.now.Sub(p.row.UpdatedAt) < ownerLease {
		return decision{Next: NextNone, Head: head, Reason: "run " + o + " is working on this head"}, nil
	}

	entry, err := s.deps.Store.LatestQueueEntryForRecord(ctx, p.rec.ID)
	if err != nil {
		return decision{}, err
	}
	ejected := entry != nil && entry.HeadSHA == head && entry.State == core.QueueEntryEjected &&
		(entry.EjectReason == core.QueueEjectGateRed || entry.EjectReason == core.QueueEjectConflict)

	switch p.row.State {
	case core.PRPipelineLanded, core.PRPipelineClosed:
		return s.idle(ctx, p, p.row.State, "the PR is "+string(p.row.State))
	case core.PRPipelineQueued, core.PRPipelineGating:
		if !ejected {
			return s.idle(ctx, p, p.row.State, "the PR is in the merge queue")
		}
	case core.PRPipelineNeedsPerson:
		if p.row.NextActionAt != nil && !p.row.NextActionAt.After(p.now) {
			return decision{Next: NextEscalate, Head: head, Reason: p.row.NeedsPersonReason}, nil
		}
		return decision{Next: NextNone, Head: head, Reason: "waiting for a person: " + p.row.NeedsPersonReason}, nil
	}

	if blocker, ok := commentBlocker(comment); ok {
		blockers := []prpolicy.Finding{blocker}
		return s.fix(ctx, p, blockers, nil, map[string]any{"comment": jsonValue(comment)}, prpolicy.FindingsSignature(blockers),
			"a review comment asks for a change")
	}
	if ejected {
		failures := []prpolicy.CheckFailure{{Check: "queue", Test: string(entry.EjectReason)}}
		evidence := map[string]any{"queue_entry": entry.ID, "eject_reason": string(entry.EjectReason), "batch": entry.BatchID}
		return s.fix(ctx, p, nil, nil, evidence, prpolicy.FailureSignature(failures),
			fmt.Sprintf("the merge queue ejected this head (%s)", entry.EjectReason))
	}

	if d, ok, err := s.superseded(ctx, p, pr); err != nil || ok {
		return d, err
	}

	verdicts, err := s.deps.Store.ListPRHeadVerdicts(ctx, p.rec.ID)
	if err != nil {
		return decision{}, err
	}
	v := latestOnHead(verdicts, head)
	if v == nil {
		return s.review(ctx, p, pr, verdicts)
	}
	return s.afterVerdict(ctx, p, v)
}

// afterVerdict is what a head with a verdict needs.
func (s *Service) afterVerdict(ctx context.Context, p *prCall, v *core.PRHeadVerdictRecord) (decision, error) {
	switch v.Verdict {
	case core.PRHeadVerdictPass:
		return s.passed(ctx, p, v)
	case core.PRHeadVerdictChanges:
		blockers := blockersOf(v)
		return s.fix(ctx, p, blockers, findingsOf(v), nil, prpolicy.FindingsSignature(blockers),
			fmt.Sprintf("review round %d asked for changes", v.Round))
	default:
		return decision{Next: NextEscalate, Head: v.HeadSHA, Findings: findingsOf(v),
			Reason: "the reviewer recommends closing this PR; a person decides"}, nil
	}
}

// passed is a passed head: enqueue it, unless an owner decision it requires
// is not in force.
func (s *Service) passed(ctx context.Context, p *prCall, v *core.PRHeadVerdictRecord) (decision, error) {
	unmet, err := s.unmetDecisions(ctx, p, v)
	if err != nil {
		return decision{}, err
	}
	if len(unmet) > 0 {
		return decision{Next: NextEscalate, Head: v.HeadSHA, Findings: findingsOf(v),
			Evidence: map[string]any{"required_decisions": unmet},
			Reason:   "this head needs the owner's decision first: " + strings.Join(unmet, ", ")}, nil
	}
	to := *p.row
	to.State, to.OwnerRunID = core.PRPipelineApproved, p.as.RunID
	to.NextAction, to.NextActionAt = NextEnqueue, timePtr(p.now.Add(ownerLease))
	if err := s.transition(ctx, p, to); err != nil {
		return decision{}, err
	}
	return decision{Next: NextEnqueue, Head: v.HeadSHA, Findings: findingsOf(v), Risk: string(v.RiskTier),
		Reason: fmt.Sprintf("round %d passed (%s)", v.Round, v.Rule)}, nil
}

// review sends the head to the reviewer, or, for a low-risk head, passes it
// here with no model. The risk tier reads the head's changed files as a gate
// pool daemon lists them (changedFiles); with no daemon to list them the
// head is high risk.
func (s *Service) review(ctx context.Context, p *prCall, pr PullRequest, verdicts []*core.PRHeadVerdictRecord) (decision, error) {
	head := p.row.StateHeadSHA
	modelRounds := 0
	for _, v := range verdicts {
		if v.Rule == core.PRVerdictRuleModel {
			modelRounds++
		}
	}
	if prpolicy.ReviewRoundsExhausted(modelRounds, p.cfg.Review) {
		return decision{Next: NextEscalate, Head: head,
			Reason: fmt.Sprintf("%d review rounds ran without the PR passing; a person needs to look", modelRounds)}, nil
	}
	files, known, err := s.changedFiles(ctx, p, pr)
	if err != nil {
		return decision{}, err
	}
	risk := filesUnavailableRisk
	if known {
		risk = prpolicy.AssessRisk(files.Diff, p.cfg)
	}
	if !risk.NeedsModel() {
		v, err := s.structuralVerdict(ctx, p, verdicts, risk, files)
		if err != nil {
			return decision{}, err
		}
		return s.passed(ctx, p, v)
	}

	var prior *core.PRHeadVerdictRecord
	if len(verdicts) > 0 {
		prior = verdicts[len(verdicts)-1]
	}
	key := p.req.Invocation.ExecutionKey
	err = s.deps.Store.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		reviews, err := s.attempts(ctx, p.rec.ID, core.PipelineAttemptReview, attemptReview)
		if err != nil {
			return err
		}
		if err := s.supersede(ctx, reviews, key); err != nil {
			return err
		}
		if byExecution(reviews, key) == nil {
			detail := attemptDetail{Kind: attemptReview, ExecutionKey: key, Risk: string(risk.Tier), RiskReasons: risk.Reasons,
				Files: files.Blobs, FilesUnknown: !known}
			if _, err := s.deps.Store.NextPipelineAttempt(ctx, core.PipelineAttempt{
				OrgID: p.repo.OrgID, ProjectID: p.repo.ProjectID, SubjectKind: core.PipelineAttemptSubjectPR, SubjectID: p.rec.ID,
				HeadSHA: head, Stage: core.PipelineAttemptReview, RunID: p.as.RunID, DetailJSON: detail.raw(),
			}); err != nil {
				return err
			}
		}
		to := *p.row
		to.State, to.OwnerRunID = core.PRPipelineReviewing, p.as.RunID
		to.NextAction, to.NextActionAt = NextReview, timePtr(p.now.Add(ownerLease))
		return s.transition(ctx, p, to)
	})
	if err != nil {
		return decision{}, casError(err)
	}
	return decision{Next: NextReview, Head: head, Findings: findingsOf(prior), Risk: string(risk.Tier),
		Evidence: map[string]any{"risk_reasons": risk.Reasons}, Reason: strings.Join(risk.Reasons, "; ")}, nil
}

// structuralVerdict writes the verdict of a head that needs no model: a
// pass by rule no_model_low_risk or generated_only. pr.next is that
// verdict's reviewer (§4.6, "queue structural rules").
func (s *Service) structuralVerdict(ctx context.Context, p *prCall, verdicts []*core.PRHeadVerdictRecord, risk prpolicy.RiskAssessment, files ChangedFiles) (*core.PRHeadVerdictRecord, error) {
	head := p.row.StateHeadSHA
	round := 1
	if v := latestOnHead(verdicts, head); v != nil {
		round = v.Round + 1
	}
	// A structural pass approves as a model's does, so it is held to the
	// same grant: in mode on the run's activation must hold approve.
	if err := s.authorizeApprove(ctx, p); err != nil {
		return nil, err
	}
	inv := p.req.Invocation
	v, err := s.deps.Store.InsertPRHeadVerdict(ctx, core.PRHeadVerdictRecord{
		OrgID: p.repo.OrgID, ProjectID: p.repo.ProjectID, RecordID: p.rec.ID, HeadSHA: head, Round: round,
		Verdict: core.PRHeadVerdictPass, Rule: core.PRVerdictRule(risk.Rule), RiskTier: core.PRRiskTier(risk.Tier),
		RequiredDecisions: requiredDecisions(p.cfg, head, diffPaths(files.Diff)),
		RunID:             inv.RunID, NodeID: inv.NodeID, ReviewerPrincipal: principalName,
		// The activation and machine of the run that wrote it, as a model
		// verdict records them, so the land check can ask about them.
		TriggerID: p.req.Principal.TriggerID, DaemonID: p.req.Principal.DaemonID,
	})
	if errors.Is(err, core.ErrPipelineVerdictExists) {
		// A concurrent or retried pr.next wrote this round already.
		again, lerr := s.deps.Store.ListPRHeadVerdicts(ctx, p.rec.ID)
		if lerr != nil {
			return nil, lerr
		}
		if v := latestOnHead(again, head); v != nil {
			return v, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// fix sends the head to the fixer with what blocks it, unless the fixes at
// this signature have run out. It starts this run's fix attempt, which
// pr.pushed finishes.
func (s *Service) fix(ctx context.Context, p *prCall, blockers, findings []prpolicy.Finding, evidence map[string]any, sig prpolicy.Signature, reason string) (decision, error) {
	head := p.row.StateHeadSHA
	key := p.req.Invocation.ExecutionKey
	var exhausted *decision
	err := s.deps.Store.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		fixes, err := s.attempts(ctx, p.rec.ID, core.PipelineAttemptFix, attemptFix)
		if err != nil {
			return err
		}
		mine := byExecution(fixes, key)
		made := 0
		for _, a := range fixes {
			if a.Signature == string(sig) && a != mine {
				made++
			}
		}
		state := prpolicy.AttemptState{Signature: sig, Attempts: made}
		if _, out := prpolicy.NextAttempt(state, sig, p.cfg.Fix); out {
			exhausted = &decision{Next: NextEscalate, Head: head, Blockers: blockers, Findings: findings, Evidence: evidence,
				Signature: string(sig), Reason: prpolicy.ExhaustedReason(state)}
			return nil
		}
		if err := s.supersede(ctx, fixes, key); err != nil {
			return err
		}
		if mine == nil {
			detail := attemptDetail{Kind: attemptFix, ExecutionKey: key, Blockers: blockers, Evidence: evidence}
			if _, err := s.deps.Store.NextPipelineAttempt(ctx, core.PipelineAttempt{
				OrgID: p.repo.OrgID, ProjectID: p.repo.ProjectID, SubjectKind: core.PipelineAttemptSubjectPR, SubjectID: p.rec.ID,
				HeadSHA: head, Stage: core.PipelineAttemptFix, Signature: string(sig), RunID: p.as.RunID, DetailJSON: detail.raw(),
			}); err != nil {
				return err
			}
		}
		to := *p.row
		to.State, to.OwnerRunID = core.PRPipelineFixing, p.as.RunID
		to.FixSignature, to.FixAttempts = string(sig), made+1
		to.NextAction, to.NextActionAt = NextFix, timePtr(p.now.Add(ownerLease))
		return s.transition(ctx, p, to)
	})
	if err != nil {
		return decision{}, casError(err)
	}
	if exhausted != nil {
		return *exhausted, nil
	}
	return decision{Next: NextFix, Head: head, Blockers: blockers, Findings: findings, Evidence: evidence,
		Signature: string(sig), Reason: reason}, nil
}

// supersede finishes every attempt still in flight that this execution did
// not start: a run that died, or an earlier decision this run moved past.
func (s *Service) supersede(ctx context.Context, attempts []*core.PipelineAttempt, key string) error {
	for _, a := range attempts {
		if a.Outcome != "" || detailOf(a).ExecutionKey == key {
			continue
		}
		if _, err := s.deps.Store.FinishPipelineAttempt(ctx, a.ID, core.PipelineAttemptSuperseded, nil); err != nil &&
			!errors.Is(err, core.ErrPipelineVersionConflict) {
			return err
		}
	}
	return nil
}

// superseded closes an open PR whose change another PR of the repository
// already carries at the same head (prpolicy.DetectSupersession).
func (s *Service) superseded(ctx context.Context, p *prCall, pr PullRequest) (decision, bool, error) {
	others, err := s.deps.Store.ListPRRecordsAtHead(ctx, p.rec.Provider, p.rec.RepositoryID, p.row.StateHeadSHA, p.rec.ID)
	if err != nil || len(others) == 0 {
		return decision{}, false, err
	}
	summary := func(r *core.PRRecord) prpolicy.PRSummary {
		state := prpolicy.PRStateOpen
		switch r.State {
		case core.PRRecordMerged:
			state = prpolicy.PRStateMerged
		case core.PRRecordClosed:
			state = prpolicy.PRStateClosed
		}
		return prpolicy.PRSummary{Repo: r.Repository, Number: r.Number, Author: r.AuthorLogin, State: state,
			BaseRef: r.BaseRef, HeadSHA: r.HeadSHA, Commits: []string{r.HeadSHA}}
	}
	self := summary(p.rec)
	self.State, self.HeadSHA, self.Body = prpolicy.PRStateOpen, p.row.StateHeadSHA, pr.Body
	if pr.BaseRef != "" {
		self.BaseRef = pr.BaseRef
	}
	sums := make([]prpolicy.PRSummary, 0, len(others))
	for _, o := range others {
		sums = append(sums, summary(o))
	}
	got, evidence := prpolicy.DetectSupersession(self, sums)
	if got.Kind == prpolicy.SupersessionNone {
		return decision{}, false, nil
	}
	cited := make([]string, 0, len(evidence))
	for _, e := range evidence {
		cited = append(cited, e.Detail)
	}
	ev := map[string]any{"kind": string(got.Kind), "by": got.By, "facts": cited}
	// A person reopened it after the pipeline closed it for this
	// evidence: never close it again for the same reason.
	if reopened, err := s.closedBefore(ctx, p, closeSignature(ev)); err != nil || reopened {
		return decision{}, false, err
	}
	return decision{Next: NextClose, Head: p.row.StateHeadSHA, Evidence: ev,
		Reason: fmt.Sprintf("%s by #%d: %s", got.Kind, got.By, strings.Join(cited, "; "))}, true, nil
}

// idle answers none, and leaves the row waiting for nothing: in state, with
// no due time and no owning run. It writes only what changed, so a PR that
// is already idle is not bumped.
func (s *Service) idle(ctx context.Context, p *prCall, state core.PRPipelineState, why string) (decision, error) {
	d := decision{Next: NextNone, Head: p.row.StateHeadSHA, Reason: why}
	owner := "" // this run's claim, or a lapsed one, ends; another run's live claim stays
	if o := p.row.OwnerRunID; o != "" && o != p.as.RunID && p.now.Sub(p.row.UpdatedAt) < ownerLease {
		owner = o
	}
	if p.row.State == state && p.row.NextActionAt == nil && p.row.NextAction == "" && p.row.OwnerRunID == owner {
		return d, nil
	}
	to := *p.row
	to.State, to.OwnerRunID, to.NextAction, to.NextActionAt = state, owner, "", nil
	if state != core.PRPipelineNeedsPerson {
		to.NeedsPersonReason, to.NeedsPersonSince = "", nil
	}
	if err := s.transition(ctx, p, to); err != nil {
		return decision{}, err
	}
	return d, nil
}

// commentBlocker is the comment that woke the run, as a blocker for the
// fixer. A comment from a bot (the pipeline's own, among others) or with no
// text is not one.
func commentBlocker(raw any) (prpolicy.Finding, bool) {
	c, ok := raw.(map[string]any)
	if !ok {
		return prpolicy.Finding{}, false
	}
	body, _ := c["body"].(string)
	author, _ := c["author"].(string)
	if strings.TrimSpace(body) == "" || strings.HasSuffix(strings.ToLower(author), "[bot]") {
		return prpolicy.Finding{}, false
	}
	f := prpolicy.Finding{ID: fmt.Sprintf("comment-%v", c["id"]), Severity: prpolicy.SeverityBlocker, Body: body}
	if author != "" {
		f.Body = author + " commented: " + body
	}
	f.Path, _ = c["path"].(string)
	if line, ok := c["line"].(float64); ok {
		f.Line = int(line)
	}
	return f, true
}
