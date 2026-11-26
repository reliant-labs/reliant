// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
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
	if p.row.State != core.PRPipelineClosed {
		pr, err := s.deps.GitHub.PullRequest(ctx, p.as, p.rec.Repository, p.rec.Number)
		if err != nil {
			return nil, &serveraction.Error{Code: serveraction.CodeUnavailable, Retryable: true, Message: fmt.Sprintf("could not read %s from GitHub", prName(p))}
		}
		if pr.State == "open" && !pr.Merged {
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
