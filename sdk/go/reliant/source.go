// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"encoding/json"
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
// With a scope it follows ONE run of the session (see runScope): the feed,
// the root and the gates are narrowed to that run's execution subtree. Without
// one it follows the session's newest run, as FollowSession promises.
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
	reread := false
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
		if s.scope != nil {
			raw = s.narrow(ctx, raw, &reread)
		}
		updates = append(updates, raw)
	}
	return updates, resp.Msg.GetLatestSequence(), nil
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
// so the engine skips it but still pages past its sequence. A workflow or
// thread the scope does not know yet triggers one tree re-read per poll: a
// child workflow started since the last read.
func (s *chatSource) narrow(ctx context.Context, u follow.RawUpdate, reread *bool) follow.RawUpdate {
	var p scopedPayload
	if err := json.Unmarshal(u.Data, &p); err != nil {
		return u // not ours to judge: the engine drops what it cannot parse
	}
	known := func() bool {
		switch {
		case strings.Contains(u.Type, "NODE_EXECUTION"):
			return s.scope.ids[p.WorkflowID]
		case strings.Contains(u.Type, "WORKFLOW_STATUS"):
			// A child workflow announces itself before any tree read shows it.
			if !s.scope.ids[p.WorkflowID] && p.ParentWorkflowID != "" && s.scope.ids[p.ParentWorkflowID] {
				s.scope.ids[p.WorkflowID] = true
			}
			return s.scope.ids[p.WorkflowID]
		case strings.Contains(u.Type, "QUESTION"):
			return s.scope.owns(Gate{ID: p.QuestionID, ThreadID: p.ThreadID})
		case strings.Contains(u.Type, "APPROVAL"):
			// An approval update names no workflow or thread: it is the run's
			// once the run's pending gates included it (Pending, every poll),
			// and the engine's reconciler surfaces it from there meanwhile.
			return s.scope.gates[p.ID]
		}
		return true // a type the engine does not surface
	}
	ok := known()
	if !ok && !*reread && (strings.Contains(u.Type, "NODE_EXECUTION") || strings.Contains(u.Type, "QUESTION")) {
		*reread = true
		if roots, err := s.runs.sessionTree(ctx, s.sessionID, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC); err == nil {
			s.scope.load(roots)
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
	if strings.Contains(u.Type, "WORKFLOW_STATUS") && p.WorkflowID == s.scope.runID && p.ParentWorkflowID != "" {
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

func (s *chatSource) Root(ctx context.Context) (follow.RootState, error) {
	// Only root status is read, so the default BASIC view is enough; FULL would
	// ship every step row of the session.
	roots, err := s.runs.sessionTree(ctx, s.sessionID, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC)
	if err != nil {
		return follow.RootState{}, err
	}
	var root *reliantv1.WorkflowExecution
	switch {
	case s.scope != nil:
		// The followed run's own execution, wherever it sits in the session:
		// an older root, the newest, or a child workflow of either.
		root = s.scope.load(roots)
	case len(roots) > 0:
		root = roots[0] // newest first
	}
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
	if s.scope != nil {
		gates = s.scope.filter(gates)
	}
	out := make([]follow.PendingGate, 0, len(gates))
	for _, g := range gates {
		out = append(out, follow.PendingGate{
			Kind: g.Kind, ID: g.ID, StepID: g.StepID, ThreadID: g.ThreadID, Title: g.Title, Metadata: g.Metadata,
		})
	}
	return out, err
}
