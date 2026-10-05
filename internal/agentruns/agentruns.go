// Copyright (c) 2025 Reliant Labs
//
// Package agentruns lets an agent start and manage OTHER top-level runs the
// same user owns. It is the implementation behind the start_run, control_run
// and send_to_run tools, which declare the narrow interfaces this package
// satisfies (internal/llm/tools cannot import the launcher or the run service:
// both depend on it).
//
// It adds no run logic of its own. Starting goes through launch.Launcher, the
// one door every run starts through, with the trigger event recorded as
// agent.start_run so lineage and idempotency live in trigger_events. Pausing,
// resuming, cancelling and messaging go through the run API (RunService),
// which owns the ordering rules — cancel tools before pausing, resume a paused
// run when a message arrives, never start a run from a message.
//
//forge:exclude-contract: adapter that satisfies the consumer-declared RunStarter, RunLifecycle and RunMessenger interfaces in internal/llm/tools; wiring, not a service
package agentruns

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// Launcher is the one launch call start_run makes.
type Launcher interface {
	Launch(ctx context.Context, ev launch.Event, spec launch.Spec) (*launch.Result, error)
}

// RunAPI is the slice of the run service the lifecycle and messaging tools
// use. It is satisfied by *services.RunService, whose handlers authorize the
// caller from the request context — so each call below runs as the run's
// owner, never as whoever happens to be on the worker.
type RunAPI interface {
	PauseRun(context.Context, *connect.Request[reliantv1.PauseRunRequest]) (*connect.Response[reliantv1.PauseRunResponse], error)
	ResumeRun(context.Context, *connect.Request[reliantv1.ResumeRunRequest]) (*connect.Response[reliantv1.ResumeRunResponse], error)
	CancelRun(context.Context, *connect.Request[reliantv1.CancelRunRequest]) (*connect.Response[reliantv1.CancelRunResponse], error)
	SignalRun(context.Context, *connect.Request[reliantv1.SignalRunRequest]) (*connect.Response[reliantv1.SignalRunResponse], error)
}

// Runs implements tools.RunStarter, tools.RunLifecycle and tools.RunMessenger.
type Runs struct {
	launcher Launcher
	api      RunAPI
	now      func() time.Time
}

var (
	_ tools.RunStarter   = (*Runs)(nil)
	_ tools.RunLifecycle = (*Runs)(nil)
	_ tools.RunMessenger = (*Runs)(nil)
)

// New builds the adapter. Either dependency may be nil when a process has no
// use for that half; calling the missing half then returns an error rather
// than panicking.
func New(launcher Launcher, api RunAPI) *Runs {
	return &Runs{launcher: launcher, api: api, now: func() time.Time { return time.Now().UTC() }}
}

// asOwner returns ctx carrying userID the way the auth middleware would, so a
// handler's auth.MustGetUserID resolves to the run's owner.
func asOwner(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, auth.UserIDContextKey, userID)
}

// StartRun launches a new top-level run owned by req.OwnerUserID.
//
// The run is NOT unattended: it belongs to a human who can watch it and answer
// it. Unattended runs are a trigger concept — no human is watching — and an
// agent-started run is not that.
func (r *Runs) StartRun(ctx context.Context, req tools.StartRunRequest) (tools.StartedRun, error) {
	if r.launcher == nil {
		return tools.StartedRun{}, errors.New("no launcher is configured")
	}

	params, err := toParams(req.Inputs)
	if err != nil {
		return tools.StartedRun{}, fmt.Errorf("inputs: %w", err)
	}

	spec := launch.Spec{
		OwnerUserID: req.OwnerUserID,
		ProjectID:   req.ProjectID,
		Workflow:    req.Workflow,
		Presets:     req.Presets,
		DaemonID:    req.DaemonID,
		NoMachine:   req.NoMachine,
		Params:      params,
		Messages: []launch.SeedMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: req.Message,
		}},
		Unattended:    false,
		GenerateTitle: req.Title == "",
	}
	if req.Title != "" {
		title := req.Title
		spec.Title = &title
	}
	// A cloud-daemon chat's first run resolves its daemon with the owner's
	// JWT. The tool runs on the worker, where there is no request to take it
	// from, so use the process's cached copy when it has one; without it the
	// run still starts and works against a local daemon.
	if jwt, ok := auth.GetUserJWT(req.OwnerUserID); ok {
		spec.UserJWT = jwt
	}

	result, err := r.launcher.Launch(ctx, launch.Event{
		Kind:       core.TriggerEventKindAgentStartRun,
		DedupeKey:  req.DedupeKey,
		OccurredAt: r.now(),
		Payload:    map[string]any{"parent_chat_id": req.ParentChatID},
	}, spec)
	if err != nil {
		var already *launch.AlreadyLaunchedError
		if errors.As(err, &already) && already.ChatID != "" {
			return tools.StartedRun{ChatID: already.ChatID, AlreadyStarted: true}, nil
		}
		return tools.StartedRun{}, err
	}
	return tools.StartedRun{ChatID: result.Chat.ID, RunID: result.RunID}, nil
}

// PauseRun parks the run. It goes through RunService, which cancels in-flight
// tool calls before the pause lands.
func (r *Runs) PauseRun(ctx context.Context, userID, chatID string) error {
	if r.api == nil {
		return errors.New("no run service is configured")
	}
	_, err := r.api.PauseRun(asOwner(ctx, userID), connect.NewRequest(&reliantv1.PauseRunRequest{RunId: chatID}))
	return err
}

// ResumeRun continues a paused or interrupted run.
func (r *Runs) ResumeRun(ctx context.Context, userID, chatID string) (tools.RunResumeResult, error) {
	if r.api == nil {
		return tools.RunResumeResult{}, errors.New("no run service is configured")
	}
	resp, err := r.api.ResumeRun(asOwner(ctx, userID), connect.NewRequest(&reliantv1.ResumeRunRequest{RunId: chatID}))
	if err != nil {
		return tools.RunResumeResult{}, err
	}
	return tools.RunResumeResult{Resumed: resp.Msg.Success, Detail: resp.Msg.Message}, nil
}

// CancelRun hard-stops the run.
func (r *Runs) CancelRun(ctx context.Context, userID, chatID string) error {
	if r.api == nil {
		return errors.New("no run service is configured")
	}
	_, err := r.api.CancelRun(asOwner(ctx, userID), connect.NewRequest(&reliantv1.CancelRunRequest{RunId: chatID}))
	return err
}

// DeliverToRun saves a message to a live run's root thread and wakes it, with
// RunService.SignalRun's semantics: only a live run receives anything, and a
// run that is not live reports Delivered=false rather than being started.
func (r *Runs) DeliverToRun(ctx context.Context, userID, chatID, message string) (tools.RunDelivery, error) {
	if r.api == nil {
		return tools.RunDelivery{}, errors.New("no run service is configured")
	}
	resp, err := r.api.SignalRun(asOwner(ctx, userID), connect.NewRequest(&reliantv1.SignalRunRequest{
		RunId: chatID,
		Messages: []*reliantv1.InputMessage{{
			Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
			Content: message,
		}},
	}))
	if err != nil {
		return tools.RunDelivery{}, err
	}
	delivery := tools.RunDelivery{Delivered: resp.Msg.Delivered}
	if len(resp.Msg.MessageIds) > 0 {
		delivery.MessageID = resp.Msg.MessageIds[0]
	}
	return delivery, nil
}

// toParams converts a tool call's JSON inputs to the launcher's param values.
func toParams(inputs map[string]any) (map[string]*structpb.Value, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	params := make(map[string]*structpb.Value, len(inputs))
	for name, value := range inputs {
		converted, err := structpb.NewValue(value)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
		params[name] = converted
	}
	return params, nil
}
