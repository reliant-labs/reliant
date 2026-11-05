// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
	"github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1/reliantv1connect"
)

// RunsClient starts, steers, lists and follows runs. Get one from [Client.Runs].
type RunsClient struct {
	c    *Client
	runs reliantv1connect.RunServiceClient
	chat reliantv1connect.ChatServiceClient
}

// StartRun is the request to start a run.
type StartRun struct {
	// Workflow is the workflow to run: "builtin://agent", or the name of a
	// saved one. Required. Bare builtin names are not resolved here; spell the
	// builtin:// ref.
	Workflow string
	// Inputs are the workflow's declared input parameters. Values may be
	// anything structpb can represent: strings, numbers, bools, nil, []any and
	// map[string]any.
	Inputs map[string]any
	// SessionID runs the workflow inside an existing session, so the run reads
	// and writes that conversation. Empty starts a new one: the run is hosted in
	// the caller's most recently active project.
	SessionID string
	// Message is the run's first user message. Optional: a run whose workflow
	// reads its inputs needs none.
	Message string
	// Messages seed the run's thread; Message, when set, is appended first.
	Messages []Message
	// Presets selects preset bundles by target ("" is workflow level).
	Presets map[string]string
	// Mode is the execution mode: "auto", "manual" or "plan".
	Mode string
}

// Start starts a workflow and returns as soon as it is dispatched; the run is
// PENDING or ACTIVE, not finished. Follow it with [RunsClient.Follow].
func (r *RunsClient) Start(ctx context.Context, req StartRun) (*Run, error) {
	const op = "runs.start"
	if req.Workflow == "" {
		return nil, r.c.fail(op, ErrInvalidArgument, "Workflow is required")
	}
	inputs, err := toStructValues(req.Inputs)
	if err != nil {
		return nil, r.c.fail(op, ErrInvalidArgument, "%v", err)
	}
	msgs := req.Messages
	if req.Message != "" {
		msgs = append([]Message{UserMessage(req.Message)}, msgs...)
	}
	resp, err := r.runs.StartRun(ctx, connect.NewRequest(&reliantv1.StartRunRequest{
		Workflow:  req.Workflow,
		Inputs:    inputs,
		SessionId: req.SessionID,
		Messages:  toInputMessages(msgs),
		Presets:   req.Presets,
		Mode:      req.Mode,
	}))
	if err != nil {
		return nil, r.c.wrap(op, err)
	}
	return resp.Msg.GetRun(), nil
}

// Get returns one run's current state.
func (r *RunsClient) Get(ctx context.Context, runID string) (*Run, error) {
	run, _, err := r.get(ctx, "runs.get", runID)
	return run, err
}

// RunNode is one recorded node attempt of a run: persisted history, not a live
// event. See [RunsClient.Nodes].
type RunNode = reliantv1.RunNodeExecution

// Nodes returns a run's recorded node attempts, newest first, including nested
// workflows. truncated is true when older attempts were left out.
func (r *RunsClient) Nodes(ctx context.Context, runID string) (nodes []*RunNode, truncated bool, err error) {
	resp, err := r.runs.GetRun(ctx, connect.NewRequest(&reliantv1.GetRunRequest{RunId: runID}))
	if err != nil {
		return nil, false, r.c.wrap("runs.nodes", err)
	}
	return resp.Msg.GetNodes(), resp.Msg.GetNodesTruncated(), nil
}

func (r *RunsClient) get(ctx context.Context, op, runID string) (*Run, *reliantv1.GetRunResponse, error) {
	if runID == "" {
		return nil, nil, r.c.fail(op, ErrInvalidArgument, "run id is required")
	}
	resp, err := r.runs.GetRun(ctx, connect.NewRequest(&reliantv1.GetRunRequest{RunId: runID}))
	if err != nil {
		return nil, nil, r.c.wrap(op, err)
	}
	return resp.Msg.GetRun(), resp.Msg, nil
}

// ListRuns filters a run list. With neither SessionID nor ParentID it is the
// cross-cutting list: every root run the caller owns, newest first, narrowed
// by the rest.
type ListRuns struct {
	// SessionID lists one session's runs; ParentID one run's children. They
	// ignore the filters below.
	SessionID string
	ParentID  string
	// State filters by lifecycle state (session and parent lists only).
	State WorkflowState

	ProjectID       string
	Workflow        []string
	TriggerID       string
	LaunchKind      []string
	DisplayStates   []RunDisplayState
	StartedAfter    time.Time // inclusive
	StartedBefore   time.Time // exclusive
	Query           string    // case-insensitive substring of the title
	IncludeArchived bool

	// PageSize is how many runs to request per round trip (server default 50,
	// cap 200). Limit caps the total the iterator yields; zero means all.
	PageSize int
	Limit    int
}

// List lists runs, following page tokens until the last page.
func (r *RunsClient) List(ctx context.Context, f ListRuns) *Iter[*Run] {
	return newIter(ctx, f.Limit, func(ctx context.Context, token string) ([]*Run, string, error) {
		req := &reliantv1.ListRunsRequest{
			Limit:    int32(f.PageSize),
			Workflow: f.Workflow, LaunchKind: f.LaunchKind, DisplayStates: f.DisplayStates,
		}
		if f.SessionID != "" {
			req.SessionId = &f.SessionID
		}
		if f.ParentID != "" {
			req.ParentId = &f.ParentID
		}
		if f.State != 0 {
			req.State = &f.State
		}
		if f.ProjectID != "" {
			req.ProjectId = &f.ProjectID
		}
		if f.TriggerID != "" {
			req.TriggerId = &f.TriggerID
		}
		if f.Query != "" {
			req.Query = &f.Query
		}
		if !f.StartedAfter.IsZero() {
			req.StartedAfter = timestamppb.New(f.StartedAfter)
		}
		if !f.StartedBefore.IsZero() {
			req.StartedBefore = timestamppb.New(f.StartedBefore)
		}
		if f.IncludeArchived {
			req.IncludeArchived = &f.IncludeArchived
		}
		if token != "" {
			req.PageToken = &token
		}
		resp, err := r.runs.ListRuns(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, "", r.c.wrap("runs.list", err)
		}
		return resp.Msg.GetRuns(), resp.Msg.GetNextPageToken(), nil
	})
}

// LastPerWorkflow returns the newest root run of each workflow the caller has
// run, one per workflow name, ordered by name. workflows narrows it; empty
// means all.
func (r *RunsClient) LastPerWorkflow(ctx context.Context, projectID string, workflows ...string) ([]*Run, error) {
	req := &reliantv1.LastRunPerWorkflowRequest{Workflow: workflows}
	if projectID != "" {
		req.ProjectId = &projectID
	}
	resp, err := r.runs.LastRunPerWorkflow(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, r.c.wrap("runs.lastPerWorkflow", err)
	}
	return resp.Msg.GetRuns(), nil
}

// Signal delivers messages into a run that is already going. It never starts
// one: a run that is not live reports delivered=false, which is not an error.
// thread targets one thread; empty is the run's own.
func (r *RunsClient) Signal(ctx context.Context, runID, thread string, msgs ...Message) (delivered bool, err error) {
	const op = "runs.signal"
	if len(msgs) == 0 {
		return false, r.c.fail(op, ErrInvalidArgument, "at least one message is required")
	}
	resp, err := r.runs.SignalRun(ctx, connect.NewRequest(&reliantv1.SignalRunRequest{
		RunId: runID, Thread: thread, Messages: toInputMessages(msgs),
	}))
	if err != nil {
		return false, r.c.wrap(op, err)
	}
	return resp.Msg.GetDelivered(), nil
}

// Cancel hard-stops a run. It is terminal by intent: the position is dropped,
// so there is nothing to resume.
func (r *RunsClient) Cancel(ctx context.Context, runID string) error {
	_, err := r.CancelWithResult(ctx, runID)
	return err
}

// ActionResult is what the server said about a run-control call it applied.
type ActionResult struct {
	// Message is the server's own words about it ("Chat terminated
	// successfully"); empty when it said none.
	Message string
}

// CancelWithResult is [RunsClient.Cancel], also returning the server's message.
// A call the server declined is still an error ([ErrNotApplied]).
func (r *RunsClient) CancelWithResult(ctx context.Context, runID string) (ActionResult, error) {
	const op = "runs.cancel"
	resp, err := r.runs.CancelRun(ctx, connect.NewRequest(&reliantv1.CancelRunRequest{RunId: runID}))
	if err != nil {
		return ActionResult{}, r.c.wrap(op, err)
	}
	if err := r.c.applied(op, resp.Msg.GetSuccess(), resp.Msg.GetMessage()); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Message: resp.Msg.GetMessage()}, nil
}

// Pause parks a run: it stays live and resumable. Pausing a run that already
// stopped succeeds.
//
// Beta: the semantics of positions and interrupted tools are still settling.
func (r *RunsClient) Pause(ctx context.Context, runID string) error {
	_, err := r.PauseWithResult(ctx, runID)
	return err
}

// PauseWithResult is [RunsClient.Pause], also returning the server's message.
// A call the server declined is still an error ([ErrNotApplied]).
//
// Beta: see [RunsClient.Pause].
func (r *RunsClient) PauseWithResult(ctx context.Context, runID string) (ActionResult, error) {
	const op = "runs.pause"
	resp, err := r.runs.PauseRun(ctx, connect.NewRequest(&reliantv1.PauseRunRequest{RunId: runID}))
	if err != nil {
		return ActionResult{}, r.c.wrap(op, err)
	}
	if err := r.c.applied(op, resp.Msg.GetSuccess(), resp.Msg.GetMessage()); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{Message: resp.Msg.GetMessage()}, nil
}

// Resume continues a paused run from its recorded position. It returns the run
// as it is after the resume: resuming an interrupted run may start a NEW
// execution, so the id can differ from runID.
//
// A resume the server declines is [ErrNotApplied]; use [RunsClient.ResumeWithResult]
// to tell a run that needs recovery apart from other refusals.
//
// Beta: see [RunsClient.Pause].
func (r *RunsClient) Resume(ctx context.Context, runID string) (*Run, error) {
	const op = "runs.resume"
	resp, err := r.runs.ResumeRun(ctx, connect.NewRequest(&reliantv1.ResumeRunRequest{RunId: runID}))
	if err != nil {
		return nil, r.c.wrap(op, err)
	}
	if err := r.c.applied(op, resp.Msg.GetSuccess(), resp.Msg.GetMessage()); err != nil {
		return nil, err
	}
	return resp.Msg.GetRun(), nil
}

// ResumeResult is how a resume ended.
type ResumeResult struct {
	// Run is the run after the resume (see [RunsClient.Resume]); nil when
	// NeedsRecovery.
	Run *Run
	// Message is the server's own words about it.
	Message string
	// NeedsRecovery is true when the server answered but could not resume: the
	// run's workflow session was lost, so nothing is running and the caller
	// must send a new message to continue. It is a state to report, not a
	// failure of the call, so it is not an error.
	//
	// The server declines a resume this way (success=false with a message) only
	// when the session is lost; every other refusal is an rpc error. NeedsRecovery
	// reads that answer, since the run API has no dedicated field for it yet.
	NeedsRecovery bool
}

// ResumeWithResult is [RunsClient.Resume] for a caller that must tell "needs
// recovery" from success: it returns the answer instead of turning a declined
// resume into [ErrNotApplied].
//
// Beta: see [RunsClient.Pause].
func (r *RunsClient) ResumeWithResult(ctx context.Context, runID string) (ResumeResult, error) {
	const op = "runs.resume"
	resp, err := r.runs.ResumeRun(ctx, connect.NewRequest(&reliantv1.ResumeRunRequest{RunId: runID}))
	if err != nil {
		return ResumeResult{}, r.c.wrap(op, err)
	}
	if !resp.Msg.GetSuccess() {
		return ResumeResult{Message: resp.Msg.GetMessage(), NeedsRecovery: true}, nil
	}
	return ResumeResult{Run: resp.Msg.GetRun(), Message: resp.Msg.GetMessage()}, nil
}

// InterruptResult says what an interrupt stopped.
type InterruptResult struct {
	// CancelledToolCalls counts in-flight tool calls cancelled. Zero is normal:
	// the thread was between calls or already winding down.
	CancelledToolCalls int
	// UndeliverableToolCalls names tools whose cancellation could not be
	// delivered (an offline machine, an unreachable spawn). They MAY STILL BE
	// RUNNING; a caller about to start something else should treat the previous
	// work as not provably stopped.
	UndeliverableToolCalls []string
}

// Interrupt stops what the run is doing now without ending it: the in-flight
// model call and executing tools are cancelled, and the run parks. thread
// interrupts one thread; empty interrupts every thread.
//
// Beta: see [RunsClient.Pause].
func (r *RunsClient) Interrupt(ctx context.Context, runID, thread string) (*InterruptResult, error) {
	resp, err := r.runs.InterruptRun(ctx, connect.NewRequest(&reliantv1.InterruptRunRequest{RunId: runID, Thread: thread}))
	if err != nil {
		return nil, r.c.wrap("runs.interrupt", err)
	}
	return &InterruptResult{
		CancelledToolCalls:     int(resp.Msg.GetCancelledToolCalls()),
		UndeliverableToolCalls: resp.Msg.GetUndeliverableToolCalls(),
	}, nil
}

// sessionOf resolves a run id to the session that carries its update feed.
func (r *RunsClient) sessionOf(ctx context.Context, op, runID string) (string, error) {
	run, _, err := r.get(ctx, op, runID)
	if err != nil {
		return "", err
	}
	if run.GetSessionId() == "" {
		return "", r.c.fail(op, ErrNotFound, "run %s has no session to read its progress from", runID)
	}
	return run.GetSessionId(), nil
}
