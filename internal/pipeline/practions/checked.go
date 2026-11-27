// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/pipeline/prpolicy"
	"github.com/reliant-labs/reliant/internal/serveraction"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// CheckStage is one stage of the fixer's scoped check, as the check's
// report gives it.
type CheckStage struct {
	// Name is lint, test or build.
	Name string `json:"name"`
	// Verdict is pass, fail or off.
	Verdict  string    `json:"verdict"`
	Failures []failure `json:"failures,omitempty"`
}

// checkReport is the JSON object the check's run node prints as the last
// line of its stdout: `{"stages": [{"name": "lint", "verdict": "fail",
// "failures": [{"check": "lint", "test": "internal/foo", "detail": "…"}]}]}`.
// A check that prints none is judged by its exit code alone.
type checkReport struct {
	Stages []CheckStage `json:"stages"`
}

// detailTail is how much of a check's output a failure with no report
// keeps.
const detailTail = 4000

// parseCheck reads a run node's output: its report when it printed one,
// else its exit code and the tail of what it printed.
func parseCheck(out map[string]any) (stages []CheckStage, failures []failure, passed bool) {
	exit := -1
	if v, ok := out["exit_code"].(float64); ok {
		exit = int(v)
	} else if _, absent := out["exit_code"]; !absent {
		exit = 0 // proto3 JSON omits a zero exit code
	}
	stdout, _ := out["stdout"].(string)
	stderr, _ := out["stderr"].(string)
	if r, ok := lastReport(stdout); ok {
		stages = r.Stages
		for _, st := range r.Stages {
			if st.Verdict != "fail" {
				continue
			}
			if len(st.Failures) == 0 {
				failures = append(failures, failure{Check: st.Name, Detail: "failed"})
			}
			for _, f := range st.Failures {
				if f.Check == "" {
					f.Check = st.Name
				}
				failures = append(failures, f)
			}
		}
		if exit != 0 && len(failures) == 0 {
			failures = append(failures, failure{Check: "check", Detail: fmt.Sprintf("the check exited %d", exit)})
		}
		return stages, failures, len(failures) == 0
	}
	if exit == 0 {
		return nil, nil, true
	}
	text := stderr
	if strings.TrimSpace(text) == "" {
		text = stdout
	}
	if len(text) > detailTail {
		text = text[len(text)-detailTail:]
	}
	return nil, []failure{{Check: "check", Detail: strings.TrimSpace(text)}}, false
}

func lastReport(stdout string) (checkReport, bool) {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var r checkReport
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &r) == nil && r.Stages != nil {
			return r, true
		}
		return checkReport{}, false
	}
	return checkReport{}, false
}

// Checked is reliant/pr.checked@1: it records the fixer's scoped check
// (the run node `with: {from:}` names: lint, then the affected tests, then
// the build) as an attempt of this run's fix, and answers fix (the check
// failed and rounds remain), push (it passed) or escalate (it failed
// DefaultCheckRounds times on this fix). The fixer never runs the check
// itself; this is its only feedback, and never admission to the queue.
func (s *Service) Checked(ctx context.Context, req *serveraction.Request) (*serveraction.Result, error) {
	p, err := s.loadActive(ctx, req)
	if err != nil {
		return nil, err
	}
	from, _ := req.Params["from"].(string)
	node := req.From
	if node == nil || node.NodeID != from || node.Type != model.NodeTypeRun {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition, "%q is not a run node of this action's workflow", from)
	}
	if !node.Ran {
		return nil, serveraction.Errorf(serveraction.CodeFailedPrecondition, "check node %q has not run", from)
	}
	inv := req.Invocation
	key := inv.ExecutionKey

	var result *serveraction.Result
	err = s.deps.Store.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		fixes, err := s.attempts(ctx, p.rec.ID, core.PipelineAttemptFix, attemptFix)
		if err != nil {
			return err
		}
		fix := running(fixes, inv.RunID, "")
		if fix == nil {
			return serveraction.Errorf(serveraction.CodeFailedPrecondition, "no fix of %s is in flight for this run", prName(p))
		}
		checks, err := s.attempts(ctx, p.rec.ID, core.PipelineAttemptFix, attemptCheck)
		if err != nil {
			return err
		}
		check := byExecution(checks, key)
		if check == nil {
			stages, failures, passed := parseCheck(node.Output)
			sig, outcome := prpolicy.Signature(""), core.PipelineAttemptSucceeded
			if !passed {
				sig, outcome = prpolicy.FailureSignature(toCheckFailures(failures)), core.PipelineAttemptFailed
			}
			detail := attemptDetail{Kind: attemptCheck, ExecutionKey: key, FixAttemptID: fix.ID, Sessions: []string{inv.ThreadID},
				Stages: stages, Failures: failures}
			started, err := s.deps.Store.NextPipelineAttempt(ctx, core.PipelineAttempt{
				OrgID: p.repo.OrgID, ProjectID: p.repo.ProjectID, SubjectKind: core.PipelineAttemptSubjectPR, SubjectID: p.rec.ID,
				HeadSHA: fix.HeadSHA, Stage: core.PipelineAttemptFix, Signature: string(sig), RunID: inv.RunID, DetailJSON: detail.raw(),
			})
			if err != nil {
				return err
			}
			if check, err = s.deps.Store.FinishPipelineAttempt(ctx, started.ID, outcome, nil); err != nil {
				return err
			}
			checks = append(checks, check)
		}
		result = checkResult(fix, check, checks)
		return nil
	})
	if err != nil {
		var public *serveraction.Error
		if errors.As(err, &public) {
			return nil, public
		}
		return nil, err
	}
	return result, nil
}

// checkResult is pr.checked's answer for check, one of fix's checks.
func checkResult(fix, check *core.PipelineAttempt, checks []*core.PipelineAttempt) *serveraction.Result {
	failed := 0
	for _, c := range checks {
		if detailOf(c).FixAttemptID == fix.ID && c.Outcome == core.PipelineAttemptFailed {
			failed++
		}
	}
	d := detailOf(check)
	next, reason := NextPush, "the check passed"
	if check.Outcome != core.PipelineAttemptSucceeded {
		next, reason = NextFix, fmt.Sprintf("the check failed (round %d of %d)", failed, DefaultCheckRounds)
		if failed >= DefaultCheckRounds {
			next = NextEscalate
			reason = fmt.Sprintf("the fixer's check failed %d times on this fix (signature %s); a person needs to look",
				failed, prpolicy.Signature(check.Signature).Short())
		}
	}
	failures := d.Failures
	if failures == nil {
		failures = []failure{}
	}
	stages := d.Stages
	if stages == nil {
		stages = []CheckStage{}
	}
	return &serveraction.Result{
		Data: map[string]any{
			"next": next, "reason": reason, "round": failed, "signature": check.Signature,
			"failures": jsonValue(failures), "stages": jsonValue(stages),
		},
		Content: next + ": " + reason,
	}
}
