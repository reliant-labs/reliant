// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/sdk/go/reliant/askuser"
	"github.com/reliant-labs/reliant/sdk/go/reliant/follow"
)

// Gate kinds.
const (
	GateQuestion = follow.GateQuestion
	GateApproval = follow.GateApproval
)

// Gate is something a run is parked on: a question put to a person, or an
// approval waiting for a decision.
//
// A client can answer a question ([GatesClient.Answer]). It cannot decide an
// approval: approving needs a person, and that decision is not part of this
// API. An approval gate is reported so a supervisor can tell a person.
type Gate struct {
	// Kind is [GateQuestion] or [GateApproval].
	Kind string
	// ID is the question id or approval id.
	ID string
	// StepID is the node that raised a question; ThreadID the thread it parks.
	// A fanned-out run has many threads and only some are gated.
	StepID   string
	ThreadID string
	// RunID is the workflow execution that raised the gate, when the server
	// says: the run itself, or a child workflow of it. Empty when unknown.
	RunID string
	// Title is an approval's title.
	Title string
	// Stuck marks a "stuck" escalation: the workflow parked for help because it
	// hit a problem it could not resolve, as opposed to a routine review gate.
	Stuck bool
	// Prompts are a question's sub-questions with their option labels, parsed
	// from the ask_user metadata. Empty for an approval, or for a question whose
	// metadata is not an ask_user payload (see Metadata).
	Prompts []Prompt
	// Metadata is the raw ask_user metadata JSON of a question.
	Metadata string
}

// Prompt is one sub-question of a question gate.
type Prompt struct {
	Question      string
	Options       []string
	AllowMultiple bool
}

// GatesClient lists what a run is waiting on and answers its questions. Get one
// from [Client.Gates].
type GatesClient struct {
	c         *Client
	questions reliantv1connect.QuestionServiceClient
	approvals reliantv1connect.ApprovalServiceClient
}

// Pending lists the gates a run is parked on right now: its pending questions
// (one per gated thread) and pending approvals, questions first. Only this
// run's: the gates of its own workflow and of the child workflows under it,
// not those of another run in the same session (see [GatesClient.PendingInSession]).
func (g *GatesClient) Pending(ctx context.Context, runID string) ([]Gate, error) {
	const op = "gates.pending"
	sessionID, err := g.c.Runs.sessionOf(ctx, op, runID)
	if err != nil {
		return nil, err
	}
	roots, err := g.c.Runs.sessionTree(ctx, sessionID, reliantv1.WorkflowExecutionView_WORKFLOW_EXECUTION_VIEW_BASIC)
	if err != nil {
		return nil, g.c.wrap(op, err)
	}
	scope := newRunScope(runID)
	scope.load(roots)
	gates, err := g.pending(ctx, sessionID)
	if err != nil {
		return nil, g.c.wrap(op, err)
	}
	return scope.filter(gates), nil
}

// PendingInSession is [GatesClient.Pending] for a session id (see
// [RunsClient.FollowSession]): every gate parked anywhere in that session.
func (g *GatesClient) PendingInSession(ctx context.Context, sessionID string) ([]Gate, error) {
	if sessionID == "" {
		return nil, g.c.fail("gates.pendingInSession", ErrInvalidArgument, "session id is required")
	}
	gates, err := g.pending(ctx, sessionID)
	if err != nil {
		return nil, g.c.wrap("gates.pendingInSession", err)
	}
	return gates, nil
}

// pending reads the questions and approvals of a session. On a failure to list
// approvals it still returns the questions it has, with the error.
func (g *GatesClient) pending(ctx context.Context, sessionID string) ([]Gate, error) {
	var gates []Gate

	// Every thread's question: spawned agents each park on their own.
	q, err := g.questions.ListPendingQuestions(ctx, connect.NewRequest(&reliantv1.ListPendingQuestionsRequest{ChatId: &sessionID}))
	if err != nil {
		return nil, err
	}
	for _, qi := range q.Msg.GetQuestions() {
		gate := Gate{
			Kind:     GateQuestion,
			ID:       qi.GetQuestionId(),
			StepID:   qi.GetStepId(),
			ThreadID: qi.GetThreadId(),
			RunID:    qi.GetWorkflowId(),
			Stuck:    follow.IsStuckStep(qi.GetStepId()),
			Metadata: qi.GetMetadata(),
		}
		if md, ok := askuser.ParseMetadata(qi.GetMetadata()); ok {
			for _, sq := range md.Questions {
				gate.Prompts = append(gate.Prompts, Prompt{Question: sq.Question, Options: sq.OptionLabels(), AllowMultiple: sq.AllowMultiple})
			}
		}
		gates = append(gates, gate)
	}

	a, err := g.approvals.ListApprovalsByChat(ctx, connect.NewRequest(&reliantv1.ListApprovalsByChatRequest{ChatId: sessionID}))
	if err != nil {
		return gates, err
	}
	for _, ap := range a.Msg.GetApprovals() {
		if ap.GetStatus() != reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING {
			continue
		}
		gates = append(gates, Gate{Kind: GateApproval, ID: ap.GetId(), RunID: ap.GetWorkflowId(), Title: ap.GetTitle()})
	}
	return gates, nil
}

// Answer replies to a question gate. answers has one entry per sub-question,
// naming it by its exact text; Selected holds exact option labels and Freetext
// any typed reply. A reply the server declines is an error ([ErrNotApplied]),
// not a quiet false.
func (g *GatesClient) Answer(ctx context.Context, questionID string, answers []Answer) error {
	const op = "gates.answer"
	if questionID == "" {
		return g.c.fail(op, ErrInvalidArgument, "question id is required")
	}
	if len(answers) == 0 {
		return g.c.fail(op, ErrInvalidArgument, "at least one answer is required")
	}
	data, err := askuser.BuildResponseData(answers)
	if err != nil {
		return g.c.fail(op, ErrInvalidArgument, "%v", err)
	}
	resp, err := g.questions.ResolveQuestion(ctx, connect.NewRequest(&reliantv1.ResolveQuestionRequest{
		QuestionId:   questionID,
		Action:       "reply",
		ResponseData: &data,
	}))
	if err != nil {
		return g.c.wrap(op, err)
	}
	return g.c.applied(op, resp.Msg.GetSuccess(), "the question was not resolved (already answered, or no longer pending)")
}
