// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/serveraction"
)

// inboxNamespace derives an Inbox item's id from the execution that pages,
// so a retried page is the same item.
var inboxNamespace = uuid.MustParse("6c0b1f4e-2f5d-4b8e-9a51-5d7f1d3c8e21")

// NeedsPerson is reliant/pr.needs_person@1: the PR waits for a person. It
// moves the row to needs_person with the reason, puts an item in the run
// owner's Inbox, and the first time the PR enters needs_person, posts one
// PR comment saying why. next_action_at is set two hours out: if nobody has
// acted by then, the due scan wakes the PR, pr.next answers escalate again,
// and this pages again (a new Inbox item, no new comment). A new head
// starts the PR over.
func (s *Service) NeedsPerson(ctx context.Context, req *serveraction.Request) (*serveraction.Result, error) {
	p, err := s.loadActive(ctx, req)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(fmt.Sprint(req.Params["reason"]))
	if reason == "" || reason == "<nil>" {
		return nil, serveraction.Errorf(serveraction.CodeInvalidArgument, "pr.needs_person needs a reason")
	}
	inv := req.Invocation
	entering := p.row.State != core.PRPipelineNeedsPerson
	since := p.row.NeedsPersonSince
	if since == nil {
		since = timePtr(p.now)
	}
	to := *p.row
	to.State, to.NeedsPersonReason, to.NeedsPersonSince = core.PRPipelineNeedsPerson, reason, since
	to.OwnerRunID, to.NextAction, to.NextActionAt = "", NextEscalate, timePtr(p.now.Add(repageAfter))
	if err := s.transition(ctx, p, to); err != nil {
		return nil, err
	}

	// The Inbox item first: its id is this execution's, so a retry that
	// finds it knows the comment was already handled.
	paged, repeat := false, false
	if s.deps.Inbox != nil && inv.ChatID != "" {
		item := InboxItem{
			ID:     uuid.NewSHA1(inboxNamespace, []byte(inv.ExecutionKey+"|"+p.rec.ID)).String(),
			UserID: req.Principal.UserID, ChatID: inv.ChatID, RunID: inv.RunID, ThreadID: inv.ThreadID, StepID: inv.NodeID,
			Title: fmt.Sprintf("%s needs a person", prName(p)),
			Body:  reason,
		}
		switch err := s.deps.Inbox.Page(ctx, item); {
		case errors.Is(err, ErrInboxItemExists):
			repeat = true
		case err != nil:
			return nil, err
		default:
			paged = true
		}
	}
	w := s.writer(p)
	var commentErr string
	if entering && !repeat {
		body := fmt.Sprintf("Reliant needs a person to look at this pull request.\n\n%s", reason)
		if err := w.Comment(ctx, body); err != nil {
			// The Inbox item is the page; a comment that failed is reported,
			// not retried into a second item.
			s.log.WarnContext(ctx, "pr.needs_person could not comment", "pr", prName(p), "error", err)
			commentErr = "could not comment on the PR"
		}
	}
	data := map[string]any{
		"state": string(core.PRPipelineNeedsPerson), "reason": reason, "paged": paged,
		"next_page_at": to.NextActionAt.Format("2006-01-02T15:04:05Z07:00"),
		"shadow":       p.shadow(), "writes": w.Writes(),
	}
	if commentErr != "" {
		data["comment_error"] = commentErr
	}
	return &serveraction.Result{Data: data, Content: prName(p) + " needs a person: " + reason}, nil
}
