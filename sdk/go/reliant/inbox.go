// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
)

// InboxItem is one thing waiting on the caller: an approval, a question, a
// run blocked on a machine, or an automation that failed or needs setup.
type InboxItem = reliantv1.InboxItem

// InboxItemKind says what an [InboxItem] is.
type InboxItemKind = reliantv1.InboxItemKind

// InboxClient reads the caller's inbox and dismisses what it does not need to
// see. Get one from [Client.Inbox].
//
// Reading needs the inbox:read scope, dismissing and restoring inbox:manage
// (reliant:api holds both). Nothing here resolves an item: dismissing an
// approval or a question hides it and leaves the run waiting. The machine
// storage actions of the inbox (clean up, forget a worktree) are not part of
// this API.
type InboxClient struct {
	c     *Client
	inbox reliantv1connect.InboxServiceClient
}

// ListInbox filters the inbox.
type ListInbox struct {
	// ProjectID narrows to one project (the server's own scoping, which also
	// keeps machine storage items). Empty lists every project.
	ProjectID string
	// Kinds keeps only items of these kinds. Empty keeps all.
	Kinds []InboxItemKind
	// Automation keeps only items of one automation, by trigger id or name.
	Automation string
	// Limit caps how many items the iterator yields; zero means all.
	Limit int
}

const (
	inboxFirstFetch = 100
	inboxMaxFetch   = 10000
)

// List lists the inbox in the server's priority order: items that block a run
// first, longest-waiting first.
//
// The server returns the inbox in one response, cut at a limit and flagged
// truncated. The iterator asks again with a larger limit until nothing is cut
// off (or inboxMaxFetch items), so it yields everything a filter matches. Kind
// and automation filters are applied here, after the server's cut, which is
// why the walk does not stop at the first response.
func (i *InboxClient) List(ctx context.Context, f ListInbox) *Iter[*InboxItem] {
	want := map[InboxItemKind]bool{}
	for _, k := range f.Kinds {
		want[k] = true
	}
	return newIter(ctx, f.Limit, func(ctx context.Context, _ string) ([]*InboxItem, string, error) {
		limit := int32(inboxFirstFetch)
		if f.Limit > 0 && f.Kinds == nil && f.Automation == "" && f.Limit < inboxFirstFetch {
			limit = int32(f.Limit) // nothing is dropped client-side: ask for no more than is used
		}
		for {
			req := &reliantv1.ListInboxRequest{Limit: &limit}
			if f.ProjectID != "" {
				req.ProjectId = &f.ProjectID
			}
			resp, err := i.inbox.ListInbox(ctx, connect.NewRequest(req))
			if err != nil {
				return nil, "", i.c.wrap("inbox.list", err)
			}
			enough := f.Limit > 0 && f.Kinds == nil && f.Automation == ""
			if !resp.Msg.GetTruncated() || enough || limit >= inboxMaxFetch {
				var out []*InboxItem
				for _, it := range resp.Msg.GetItems() {
					if len(want) > 0 && !want[it.GetKind()] {
						continue
					}
					if f.Automation != "" && it.GetTriggerId() != f.Automation && it.GetTriggerName() != f.Automation {
						continue
					}
					out = append(out, it)
				}
				return out, "", nil
			}
			limit = min(limit*10, inboxMaxFetch)
		}
	})
}

// InboxCounts is what the nav badge shows.
type InboxCounts struct {
	// Blocking counts the items that hold a live run: approvals, questions and
	// runs waiting for a machine.
	Blocking int
	// HasInformational is true when any item blocks nothing (a failure, an
	// async question).
	HasInformational bool
	// OtherProjects is, with a project filter, how many items wait in the
	// caller's other projects.
	OtherProjects int
}

// Counts is the cheap call: it asks for no items, only the counts. projectID
// scopes it; empty counts every project.
func (i *InboxClient) Counts(ctx context.Context, projectID string) (*InboxCounts, error) {
	zero := int32(0)
	req := &reliantv1.ListInboxRequest{Limit: &zero}
	if projectID != "" {
		req.ProjectId = &projectID
	}
	resp, err := i.inbox.ListInbox(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, i.c.wrap("inbox.counts", err)
	}
	return &InboxCounts{
		Blocking:         int(resp.Msg.GetBlockingCount()),
		HasInformational: resp.Msg.GetHasInformational(),
		OtherProjects:    int(resp.Msg.GetOtherProjectsCount()),
	}, nil
}

// Dismiss hides items from the caller's inbox. An unknown id is
// [ErrInvalidArgument]. Dismissing an approval or a question does not resolve
// it: the run still waits. A newer failure of the same automation has a new id
// and appears again.
func (i *InboxClient) Dismiss(ctx context.Context, itemIDs ...string) error {
	const op = "inbox.dismiss"
	if len(itemIDs) == 0 {
		return i.c.fail(op, ErrInvalidArgument, "at least one item id is required")
	}
	if _, err := i.inbox.DismissInboxItem(ctx, connect.NewRequest(&reliantv1.DismissInboxItemRequest{ItemIds: itemIDs})); err != nil {
		return i.c.wrap(op, err)
	}
	return nil
}

// Restore undoes [InboxClient.Dismiss]. Restoring an item that was not
// dismissed does nothing.
func (i *InboxClient) Restore(ctx context.Context, itemIDs ...string) error {
	const op = "inbox.restore"
	if len(itemIDs) == 0 {
		return i.c.fail(op, ErrInvalidArgument, "at least one item id is required")
	}
	if _, err := i.inbox.RestoreInboxItem(ctx, connect.NewRequest(&reliantv1.RestoreInboxItemRequest{ItemIds: itemIDs})); err != nil {
		return i.c.wrap(op, err)
	}
	return nil
}

// Approval is one approval request a run raised.
type Approval = reliantv1.Approval

// ApprovalStatus is where an [Approval] stands.
type ApprovalStatus = reliantv1.ApprovalStatus

// ApprovalsClient reads approvals. It cannot decide them: approving and
// denying are not part of this API. Get one from [Client.Approvals].
type ApprovalsClient struct {
	c         *Client
	approvals reliantv1connect.ApprovalServiceClient
}

// List lists the pending approvals of a chat (a session), oldest first.
func (a *ApprovalsClient) List(ctx context.Context, chatID string) ([]*Approval, error) {
	const op = "approvals.list"
	if chatID == "" {
		return nil, a.c.fail(op, ErrInvalidArgument, "chat id is required")
	}
	resp, err := a.approvals.ListApprovalsByChat(ctx, connect.NewRequest(&reliantv1.ListApprovalsByChatRequest{ChatId: chatID}))
	if err != nil {
		return nil, a.c.wrap(op, err)
	}
	return resp.Msg.GetApprovals(), nil
}

// ListForRun lists the pending approvals raised by one run: those of its
// workflow and of the child workflows under it, not of another run in the same
// session.
func (a *ApprovalsClient) ListForRun(ctx context.Context, runID string) ([]*Approval, error) {
	const op = "approvals.listForRun"
	sessionID, err := a.c.Runs.sessionOf(ctx, op, runID)
	if err != nil {
		return nil, err
	}
	roots, err := a.c.Runs.sessionTree(ctx, sessionID, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC)
	if err != nil {
		return nil, a.c.wrap(op, err)
	}
	scope := newRunScope(runID)
	scope.load(roots)
	all, err := a.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var out []*Approval
	for _, ap := range all {
		g := Gate{ID: ap.GetId(), RunID: ap.GetWorkflowId()}
		if scope.owns(g) {
			out = append(out, ap)
		}
	}
	return out, nil
}
