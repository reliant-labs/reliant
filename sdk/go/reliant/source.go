// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/sdk/go/reliant/follow"
)

// chatSource adapts a session's update feed and open gates to follow.Source.
// It is a cursor poll of the same durable per-session feed the web app streams;
// a streaming source can replace it without touching the engine.
//
// It follows ONE run of the session (see runScope): the feed, the root and the
// gates are narrowed to that run's execution subtree. FollowSession's scope is
// the session's current run, pinned before any history replays.
type chatSource struct {
	chat      reliantv1connect.ChatServiceClient
	runs      *RunsClient
	gates     *GatesClient
	sessionID string
	scope     *runScope
}

func (s *chatSource) Updates(ctx context.Context, sinceSeq int64) ([]follow.RawUpdate, int64, error) {
	resp, err := s.chat.GetChatUpdates(ctx, connect.NewRequest(&reliantv1.GetChatUpdatesRequest{
		ChatId:   s.sessionID,
		SinceSeq: sinceSeq,
	}))
	if err != nil {
		return nil, sinceSeq, err
	}
	updates := make([]follow.RawUpdate, 0, len(resp.Msg.GetUpdates()))
	var poll pollState
	for _, u := range resp.Msg.GetUpdates() {
		createdAt, err := time.Parse(time.RFC3339, u.GetCreatedAt())
		if err != nil {
			createdAt = time.Now().UTC()
		}
		raw := follow.RawUpdate{
			Seq:       u.GetSequenceNumber(),
			Type:      u.GetUpdateType(),
			Data:      []byte(u.GetData()),
			CreatedAt: createdAt,
		}
		updates = append(updates, s.narrow(ctx, raw, &poll))
	}
	return updates, resp.Msg.GetLatestSequence(), nil
}

// pollState bounds narrow's extra reads to one of each per poll.
type pollState struct {
	treeRead bool
	// treeEmpty is true when this poll's tree read showed no run at all.
	treeEmpty     bool
	approvalsRead bool
}

// scopedPayload is what narrow reads of an update to attribute it to a run.
type scopedPayload struct {
	ID               string `json:"id"` // approvals
	QuestionID       string `json:"question_id"`
	WorkflowID       string `json:"workflow_id"`
	ParentWorkflowID string `json:"parent_workflow_id"`
	ThreadID         string `json:"thread_id"`
}

// narrow keeps an update of the followed run and blanks the type of any other,
// so the engine skips it but still pages past its sequence. A workflow, thread
// or gate the scope does not know yet triggers one tree re-read per poll (a
// child workflow started since the last read), and an approval it does not
// know one read of the session's approvals.
func (s *chatSource) narrow(ctx context.Context, u follow.RawUpdate, poll *pollState) follow.RawUpdate {
	var p scopedPayload
	if err := json.Unmarshal(u.Data, &p); err != nil {
		return u // not ours to judge: the engine drops what it cannot parse
	}
	isStatus := strings.Contains(u.Type, "WORKFLOW_STATUS")
	known := func() bool {
		switch {
		case strings.Contains(u.Type, "NODE_EXECUTION"):
			return s.scope.ids[p.WorkflowID]
		case isStatus:
			// A child workflow announces itself before any tree read shows it.
			if !s.scope.ids[p.WorkflowID] && p.ParentWorkflowID != "" && s.scope.ids[p.ParentWorkflowID] {
				s.scope.ids[p.WorkflowID] = true
			}
			return s.scope.ids[p.WorkflowID]
		case strings.Contains(u.Type, "QUESTION"):
			// A question's resolution names the workflow that raised it but not
			// its thread, so the workflow is what attributes it.
			g := Gate{ID: p.QuestionID, ThreadID: p.ThreadID, RunID: p.WorkflowID}
			if !s.scope.owns(g) {
				return false
			}
			s.scope.gates[g.ID] = true
			return true
		case strings.Contains(u.Type, "APPROVAL"):
			// An approval update names the workflow that raised it and its
			// thread, resolved ones included, so its historical rows replay
			// on their own run. One that names neither (written before the
			// server attributed them) falls back to the pending list.
			if p.WorkflowID == "" && p.ThreadID == "" {
				return s.scope.gates[p.ID]
			}
			g := Gate{ID: p.ID, ThreadID: p.ThreadID, RunID: p.WorkflowID}
			if !s.scope.owns(g) {
				return false
			}
			s.scope.gates[g.ID] = true
			return true
		}
		return true // a type the engine does not surface
	}
	ok := known()
	unattributedApproval := strings.Contains(u.Type, "APPROVAL") && p.WorkflowID == "" && p.ThreadID == ""
	if !ok && !poll.treeRead && !unattributedApproval {
		poll.treeRead = true
		if roots, err := s.runs.sessionTree(ctx, s.sessionID, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC); err == nil {
			s.scope.load(roots)
			poll.treeEmpty = len(roots) == 0
			ok = known()
		}
	}
	// A session with no run in its tree yet: its first top-level workflow is
	// the run being followed, as no older one exists to be confused with it.
	if !ok && !s.scope.pinned() && poll.treeEmpty && isStatus && p.ParentWorkflowID == "" && p.WorkflowID != "" {
		s.scope.pin(p.WorkflowID)
		s.scope.newest = true
		ok = true
	}
	if !ok && !poll.approvalsRead && unattributedApproval {
		poll.approvalsRead = true
		if s.learnApprovals(ctx) == nil {
			ok = known()
		}
	}
	if !ok {
		u.Type = ""
		return u
	}
	// The followed run's own lifecycle rows are its root's, even when the run is
	// a child workflow: dropping the parent is what makes its terminal end the
	// follow, as the engine ends on a terminal with no parent.
	if isStatus && p.WorkflowID == s.scope.runID && p.ParentWorkflowID != "" {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(u.Data, &m); err == nil {
			delete(m, "parent_workflow_id")
			if b, err := json.Marshal(m); err == nil {
				u.Data = b
			}
		}
	}
	return u
}

// learnApprovals attributes the session's pending approvals by the workflow
// each names: the fallback for an approval update that names no workflow or
// thread. ListApprovalsByChat lists pending approvals only, so a resolved one
// is attributed by its own update (see narrow), never here.
func (s *chatSource) learnApprovals(ctx context.Context) error {
	if !s.scope.pinned() {
		return errors.New("no run pinned yet")
	}
	a, err := s.gates.approvals.ListApprovalsByChat(ctx, connect.NewRequest(&reliantv1.ListApprovalsByChatRequest{ChatId: s.sessionID}))
	if err != nil {
		return err
	}
	for _, ap := range a.Msg.GetApprovals() {
		if g := (Gate{ID: ap.GetId(), RunID: ap.GetWorkflowId()}); s.scope.owns(g) {
			s.scope.gates[g.ID] = true
		}
	}
	return nil
}

func (s *chatSource) Root(ctx context.Context) (follow.RootState, error) {
	// Only root status is read, so the default BASIC view is enough; FULL would
	// ship every step row of the session.
	roots, err := s.runs.sessionTree(ctx, s.sessionID, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC)
	if err != nil {
		return follow.RootState{}, err
	}
	// The followed run's own execution, wherever it sits in the session: an
	// older root, the newest, or a child workflow of either.
	root := s.scope.load(roots)
	if root == nil {
		return follow.RootState{}, nil
	}
	return follow.RootState{
		Found:  true,
		Status: StatusLabel(root.GetState(), root.GetStopReason()),
		// The verdict rides along so a follower attaching AFTER the run ended
		// still learns that it did not pass: the terminal event it would have
		// carried is long gone from the tail it is watching.
		Outcome: root.GetOutcome(),
	}, nil
}

// Pending reports the gates awaiting input so the engine can surface one that
// was already open when the follower attached, or whose update it missed. An
// error is non-fatal to the engine (it logs and retries next poll), so a
// transient failure here never wedges a follow.
func (s *chatSource) Pending(ctx context.Context) ([]follow.PendingGate, error) {
	gates, err := s.gates.pending(ctx, s.sessionID)
	gates = s.scope.filter(gates)
	out := make([]follow.PendingGate, 0, len(gates))
	for _, g := range gates {
		out = append(out, follow.PendingGate{
			Kind: g.Kind, ID: g.ID, StepID: g.StepID, ThreadID: g.ThreadID, Title: g.Title, Metadata: g.Metadata,
		})
	}
	return out, err
}
