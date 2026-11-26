// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// Close is reliant/pr.close@1: it closes the PR with one comment citing the
// evidence (pr.next's for a superseded or duplicate PR), then records the
// PR as closed. In shadow mode the comment and the close are recorded in
// the output, not made. A PR GitHub already shows closed is not commented
// on again, so a retried close writes nothing twice.
func (s *Service) Close(ctx context.Context, req *serveraction.Request) (*serveraction.Result, error) {
	p, err := s.loadActive(ctx, req)
	if err != nil {
		return nil, err
	}
	evidence := evidenceText(req.Params["evidence"])
	if strings.TrimSpace(evidence) == "" {
		return nil, serveraction.Errorf(serveraction.CodeInvalidArgument, "pr.close needs the evidence it cites")
	}
	w := s.writer(p)
	sig := closeSignature(req.Params["evidence"])
	if p.row.State != core.PRPipelineClosed {
		pr, err := s.deps.GitHub.PullRequest(ctx, p.as, p.rec.Repository, p.rec.Number)
		if err != nil {
			return nil, &serveraction.Error{Code: serveraction.CodeUnavailable, Retryable: true, Message: fmt.Sprintf("could not read %s from GitHub", prName(p))}
		}
		if pr.State == "open" && !pr.Merged {
			// A person reopened it after an earlier close for this
			// evidence: their call stands.
			if prior, err := s.closedBefore(ctx, p, sig); err != nil || prior {
				if err != nil {
					return nil, err
				}
				return &serveraction.Result{
					Data:    map[string]any{"closed": false, "reopened_by_person": true, "shadow": p.shadow(), "writes": []any{}},
					Content: prName(p) + " was reopened after a close for this evidence; it stays open",
				}, nil
			}
			if err := s.recordClose(ctx, p, sig, evidence); err != nil {
				return nil, err
			}
			if err := w.Comment(ctx, "Reliant is closing this pull request.\n\n"+evidence); err != nil {
				return nil, &serveraction.Error{Code: serveraction.CodeUnavailable, Retryable: true, Message: "could not comment on " + prName(p)}
			}
			if err := w.Close(ctx); err != nil {
				return nil, &serveraction.Error{Code: serveraction.CodeUnavailable, Retryable: true, Message: "could not close " + prName(p)}
			}
		}
		to := *p.row
		to.State, to.OwnerRunID, to.NextAction, to.NextActionAt = core.PRPipelineClosed, "", "", nil
		to.NeedsPersonReason, to.NeedsPersonSince = "", nil
		if err := s.transition(ctx, p, to); err != nil {
			return nil, err
		}
	}
	return &serveraction.Result{
		Data:    map[string]any{"closed": true, "shadow": p.shadow(), "writes": w.Writes()},
		Content: "closed " + prName(p),
	}, nil
}

// attemptClose is detail_json.kind of a close (stage close).
const attemptClose = "close"

// closeSignature names a close's evidence: its kind and the PR it cites
// (superseded by #12), which stay the same while the facts around them
// move (#12 merging), else the evidence's text.
func closeSignature(evidence any) string {
	ev := jsonValue(evidence)
	if m, ok := ev.(map[string]any); ok {
		if kind, _ := m["kind"].(string); kind != "" {
			return fmt.Sprintf("%s:%v", kind, m["by"])
		}
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(evidenceText(ev))))
	return "text:" + hex.EncodeToString(sum[:8])
}

// closedBefore reports whether the pipeline closed this PR before for the
// evidence sig, in another execution than this one: the PR is open again
// only because a person reopened it.
func (s *Service) closedBefore(ctx context.Context, p *prCall, sig string) (bool, error) {
	closes, err := s.deps.Store.ListPipelineAttempts(ctx, core.PipelineAttemptSubjectPR, p.rec.ID, core.PipelineAttemptClose)
	if err != nil {
		return false, err
	}
	for _, a := range closes {
		if a.Signature == sig && detailOf(a).ExecutionKey != p.req.Invocation.ExecutionKey {
			return true, nil
		}
	}
	return false, nil
}

// recordClose records this execution's close (once, however often it is
// retried), before the close is made, so a reopen can be recognised.
func (s *Service) recordClose(ctx context.Context, p *prCall, sig, evidence string) error {
	key := p.req.Invocation.ExecutionKey
	closes, err := s.deps.Store.ListPipelineAttempts(ctx, core.PipelineAttemptSubjectPR, p.rec.ID, core.PipelineAttemptClose)
	if err != nil {
		return err
	}
	if byExecution(closes, key) != nil {
		return nil
	}
	detail := attemptDetail{Kind: attemptClose, ExecutionKey: key, Evidence: map[string]any{"text": evidence}}
	a, err := s.deps.Store.NextPipelineAttempt(ctx, core.PipelineAttempt{
		OrgID: p.repo.OrgID, ProjectID: p.repo.ProjectID, SubjectKind: core.PipelineAttemptSubjectPR, SubjectID: p.rec.ID,
		HeadSHA: p.row.StateHeadSHA, Stage: core.PipelineAttemptClose, Signature: sig, RunID: p.as.RunID, DetailJSON: detail.raw(),
	})
	if err != nil {
		return err
	}
	_, err = s.deps.Store.FinishPipelineAttempt(ctx, a.ID, core.PipelineAttemptSucceeded, nil)
	return err
}

// evidenceText renders pr.close's evidence: text as it is, or pr.next's
// evidence object (its reason and facts) as a list.
func evidenceText(v any) string {
	switch e := v.(type) {
	case string:
		return e
	case []any:
		var lines []string
		for _, item := range e {
			if t := evidenceText(item); t != "" {
				lines = append(lines, "- "+t)
			}
		}
		return strings.Join(lines, "\n")
	case map[string]any:
		var parts []string
		if kind, _ := e["kind"].(string); kind != "" {
			by := ""
			if n, ok := e["by"].(float64); ok {
				by = fmt.Sprintf(" by #%d", int(n))
			}
			parts = append(parts, fmt.Sprintf("This PR is %s%s:", kind, by))
		}
		if facts := evidenceText(e["facts"]); facts != "" {
			parts = append(parts, facts)
		}
		if len(parts) == 0 {
			if b := jsonValue(e); b != nil {
				parts = append(parts, fmt.Sprint(b))
			}
		}
		return strings.Join(parts, "\n")
	case nil:
		return ""
	default:
		return fmt.Sprint(e)
	}
}
