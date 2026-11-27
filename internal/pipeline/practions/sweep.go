// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// The daily PR sweep's checks (redesign §6.4, `pr_sweep`; build plan M13).
// Each looks for a PR that fell through the cracks.
const (
	// CheckSuperseded: another PR of the repository carries this change
	// (prpolicy.DetectSupersession). Closed, with the evidence.
	CheckSuperseded = "superseded"
	// CheckStuckInQueue: the queue ejected this head and nothing is
	// acting on it. Woken.
	CheckStuckInQueue = "stuck_in_queue"
	// CheckStaleNeedsPerson: a PR waiting for a person whose re-page is
	// lost or overdue. Paged again.
	CheckStaleNeedsPerson = "stale_needs_person"
	// CheckDraftAbandoned: a draft untouched for two weeks. A person is
	// asked: never closed on age alone.
	CheckDraftAbandoned = "draft_abandoned"
	// CheckStalePR: an open PR with no due time and no change for a day,
	// a due time long past, or a row the PR's own state has left behind.
	// Woken.
	CheckStalePR = "stale_pr"
)

// SweepChecks is every check, in the order a PR is judged: the first that
// finds something decides the PR's one action.
var SweepChecks = []string{CheckSuperseded, CheckStuckInQueue, CheckStaleNeedsPerson, CheckDraftAbandoned, CheckStalePR}

// What the sweep does with a finding: exactly one of these.
const (
	SweepFinish = "finish" // wake the pr workflow (next_action decide, due now)
	SweepClose  = "close"  // pr.close with the evidence
	SweepAsk    = "ask"    // pr.needs_person
)

const (
	// staleAfter is how long an open PR may sit with nothing due.
	staleAfter = 24 * time.Hour
	// overdueAfter is how far past its due time a row is lost: the due
	// scan fires within seconds.
	overdueAfter = time.Hour
	// draftAbandonedAfter is how long a draft may go untouched.
	draftAbandonedAfter = 14 * 24 * time.Hour
)

// sweepFinding is one line of the sweep's report.
type sweepFinding struct {
	Check    string `json:"check"`
	RecordID string `json:"record_id"`
	PR       string `json:"pr"`
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	Shadow   bool   `json:"shadow"`
	Writes   []any  `json:"writes,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Sweep is reliant/sweep.pr@1: the daily PR sweep over the run's project's
// enrolled repositories (mode shadow or on). For each open PR the first
// check that finds something decides one action: finish (wake the pr
// workflow with the reason), close (pr.close with the cited evidence, never
// on age alone, and never again for evidence a person already overruled by
// reopening) or ask (pr.needs_person). It returns the report of every
// action. GitHub writes go through the pr.* actions' writer, so a shadow
// repository's are recorded, not made.
func (s *Service) Sweep(ctx context.Context, req *serveraction.Request) (*serveraction.Result, error) {
	checks, err := sweepChecks(req.Params["checks"])
	if err != nil {
		return nil, err
	}
	pr := req.Principal
	if pr.ProjectID == "" || pr.OrgID == "" {
		return nil, serveraction.Errorf(serveraction.CodePermissionDenied, "the sweep runs for a project")
	}
	repos, err := s.deps.Store.ListEnrolledPipelineRepos(ctx)
	if err != nil {
		return nil, err
	}
	findings := []sweepFinding{}
	looked := 0
	for _, repo := range repos {
		if repo.ProjectID != pr.ProjectID || repo.OrgID != pr.OrgID {
			continue
		}
		rows, err := s.deps.Store.ListOpenPRPipelines(ctx, repo.ID)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			looked++
			f, ok := s.sweepOne(ctx, req, row.RecordID, checks)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if ok {
				findings = append(findings, f)
			}
		}
	}
	counts := map[string]any{SweepFinish: 0, SweepClose: 0, SweepAsk: 0}
	lines := make([]string, 0, len(findings))
	for _, f := range findings {
		if f.Error == "" {
			counts[f.Action] = counts[f.Action].(int) + 1
		}
		lines = append(lines, fmt.Sprintf("- %s: %s (%s) %s", f.PR, f.Action, f.Check, f.Reason))
	}
	content := fmt.Sprintf("PR sweep: %d open PR(s) looked at, %d finding(s)", looked, len(findings))
	if len(lines) > 0 {
		content += "\n" + strings.Join(lines, "\n")
	}
	return &serveraction.Result{
		Data:    map[string]any{"looked": looked, "counts": counts, "findings": jsonValue(findings)},
		Content: content,
	}, nil
}

func sweepChecks(raw any) ([]string, error) {
	list, _ := raw.([]any)
	if len(list) == 0 {
		return SweepChecks, nil
	}
	var out []string
	for _, v := range list {
		name, _ := v.(string)
		if !slices.Contains(SweepChecks, name) {
			return nil, serveraction.Errorf(serveraction.CodeInvalidArgument, "%q is not a PR sweep check (%s)", name, strings.Join(SweepChecks, ", "))
		}
		out = append(out, name)
	}
	return out, nil
}

// sweepOne judges one PR and takes its action. A failure is reported in
// the finding, never fatal to the rest of the sweep.
func (s *Service) sweepOne(ctx context.Context, parent *serveraction.Request, recordID string, checks []string) (sweepFinding, bool) {
	req := s.subRequest(parent, recordID, "sweep", map[string]any{})
	p, err := s.load(ctx, req)
	if err != nil {
		return sweepFinding{RecordID: recordID, Error: err.Error()}, true
	}
	f := sweepFinding{RecordID: recordID, PR: prName(p), Shadow: p.shadow()}
	check, action, reason, evidence, err := s.judge(ctx, p, checks)
	if err != nil {
		f.Error = err.Error()
		return f, true
	}
	if check == "" {
		return f, false
	}
	f.Check, f.Action, f.Reason = check, action, reason
	var res *serveraction.Result
	switch action {
	case SweepFinish:
		to := *p.row
		to.NextAction, to.NextActionAt = NextDecide, timePtr(p.now)
		err = s.transition(ctx, p, to)
	case SweepClose:
		res, err = s.Close(ctx, s.subRequest(parent, recordID, check, map[string]any{"evidence": evidence}))
	case SweepAsk:
		res, err = s.NeedsPerson(ctx, s.subRequest(parent, recordID, check, map[string]any{"reason": reason}))
	}
	if err != nil {
		f.Error = err.Error()
	}
	if res != nil {
		f.Writes, _ = res.Data["writes"].([]any)
		if closed, ok := res.Data["closed"].(bool); ok && !closed {
			f.Error = res.Content // reopened by a person since: kept open
		}
	}
	return f, true
}

// subRequest is the call the sweep makes for one PR: the sweep's own run
// and principal, the PR's record, and an execution key of its own per PR
// and check, so a retried sweep repeats (and pages) nothing it already did.
func (s *Service) subRequest(parent *serveraction.Request, recordID, check string, params map[string]any) *serveraction.Request {
	req := *parent
	req.Params = map[string]any{"record_id": recordID}
	for k, v := range params {
		req.Params[k] = v
	}
	req.Invocation.ExecutionKey = parent.Invocation.ExecutionKey + "|" + recordID + "|" + check
	return &req
}

// judge runs the checks over one PR in order and returns the first
// finding: its check, its action, why, and the evidence a close cites.
func (s *Service) judge(ctx context.Context, p *prCall, checks []string) (check, action, reason string, evidence any, err error) {
	row, rec, now := p.row, p.rec, p.now
	on := func(c string) bool { return slices.Contains(checks, c) }
	recOpen := rec.State == core.PRRecordOpen
	idleFor := now.Sub(row.UpdatedAt)
	overdue := row.NextActionAt != nil && now.Sub(*row.NextActionAt) > overdueAfter

	if on(CheckSuperseded) && recOpen && row.State != core.PRPipelineNeedsPerson {
		d, found, err := s.superseded(ctx, p, PullRequest{BaseRef: rec.BaseRef})
		if err != nil {
			return "", "", "", nil, err
		}
		if found {
			return CheckSuperseded, SweepClose, d.Reason, d.Evidence, nil
		}
	}
	if on(CheckStuckInQueue) {
		entry, err := s.deps.Store.LatestQueueEntryForRecord(ctx, rec.ID)
		if err != nil {
			return "", "", "", nil, err
		}
		if entry != nil && entry.HeadSHA == row.StateHeadSHA && entry.State == core.QueueEntryEjected &&
			(row.NextActionAt == nil || overdue) {
			return CheckStuckInQueue, SweepFinish, fmt.Sprintf("the merge queue ejected this head (%s) and nothing is acting on it", entry.EjectReason), nil, nil
		}
	}
	if on(CheckStaleNeedsPerson) && row.State == core.PRPipelineNeedsPerson && (row.NextActionAt == nil || overdue) {
		since := "unknown"
		if row.NeedsPersonSince != nil {
			since = row.NeedsPersonSince.Format(time.RFC3339)
		}
		return CheckStaleNeedsPerson, SweepAsk, fmt.Sprintf("still waiting for a person (since %s): %s", since, row.NeedsPersonReason), nil, nil
	}
	if on(CheckDraftAbandoned) && recOpen && row.State == core.PRPipelineDraft && idleFor > draftAbandonedAfter {
		return CheckDraftAbandoned, SweepAsk, fmt.Sprintf("this draft has had no activity for %d days: finish it, mark it ready, or close it",
			int(idleFor.Hours()/24)), nil, nil
	}
	if on(CheckStalePR) {
		switch {
		case !recOpen:
			return CheckStalePR, SweepFinish, fmt.Sprintf("the PR is %s but its pipeline row is %s", rec.State, row.State), nil, nil
		case row.State == core.PRPipelineDraft || row.State == core.PRPipelineNeedsPerson:
		case overdue:
			return CheckStalePR, SweepFinish, fmt.Sprintf("its %s was due at %s and never ran", row.NextAction, row.NextActionAt.Format(time.RFC3339)), nil, nil
		case row.NextActionAt == nil && idleFor > staleAfter && !s.inQueue(ctx, p):
			return CheckStalePR, SweepFinish, fmt.Sprintf("nothing has happened for %d hours and nothing is due", int(idleFor.Hours())), nil, nil
		}
	}
	return "", "", "", nil, nil
}

// inQueue reports whether the PR's head is waiting or batched in the merge
// queue: not stale, only queued.
func (s *Service) inQueue(ctx context.Context, p *prCall) bool {
	entry, err := s.deps.Store.LatestQueueEntryForRecord(ctx, p.rec.ID)
	return err == nil && entry != nil && entry.HeadSHA == p.row.StateHeadSHA &&
		(entry.State == core.QueueEntryWaiting || entry.State == core.QueueEntryBatched)
}
