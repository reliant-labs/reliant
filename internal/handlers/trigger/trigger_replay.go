// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/triggers"
)

const (
	defaultReplayEventLimit = 20
	maxReplayEventLimit     = 50
)

// ListReplayEvents lists the recent events the caller's activations of a
// workflow's declared triggers received, newest first, so one can be run again.
//
// Only the caller's own activations are read, through the same owner-scoped
// event query ListTriggerEvents uses: the events another user's triggers
// received are not theirs to replay. A workflow the caller has not enabled has
// no activations and so no events; the answer is an empty list, not an error.
func (s *Service) ListReplayEvents(
	ctx context.Context,
	req *connect.Request[reliantv1.ListReplayEventsRequest],
) (*connect.Response[reliantv1.ListReplayEventsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	msg := req.Msg
	if strings.TrimSpace(msg.GetWorkflow()) == "" {
		return nil, svcerr.InvalidArgument("workflow is required")
	}
	limit := int(msg.GetLimit())
	switch {
	case limit <= 0:
		limit = defaultReplayEventLimit
	case limit > maxReplayEventLimit:
		limit = maxReplayEventLimit
	}

	mine, err := s.deps.Repo.ListTriggers(ctx, core.TriggerFilters{UserID: userID, ProjectID: optionalString(msg.GetProjectId())})
	if err != nil {
		return nil, triggerDBError("list triggers", err)
	}
	var candidates []*reliantv1.ReplayCandidate
	for _, t := range mine {
		if t.WorkflowTrigger == nil || !sameWorkflowRef(t.Workflow, msg.GetWorkflow()) {
			continue
		}
		firings, _, err := s.deps.Repo.ListTriggerEvents(ctx, core.TriggerEventFilters{
			UserID: userID, TriggerID: t.ID, Limit: limit,
		})
		if err != nil {
			return nil, triggerDBError("list trigger events", err)
		}
		for _, firing := range firings {
			ev, err := triggers.EventWithRunToProto(firing)
			if err != nil {
				return nil, svcerr.WrapOr(err, svcerr.Internal(fmt.Sprintf("render event %s failed", firing.Event.ID)))
			}
			candidates = append(candidates, &reliantv1.ReplayCandidate{Event: ev, TriggerName: *t.WorkflowTrigger})
		}
	}
	// Each trigger's events arrive newest first; merge them into one list.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].GetEvent().GetOccurredAt() > candidates[j].GetEvent().GetOccurredAt()
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return connect.NewResponse(&reliantv1.ListReplayEventsResponse{Events: candidates}), nil
}

// PreviewReplay maps a past event through a declared trigger's inputs and
// prompt, as a fire of that event would, without starting anything. Only the
// mapping applies: the trigger's filter and origin_filter are not evaluated,
// because the person replaying has decided the event is wanted.
//
// The workflow is resolved for the caller, so a workflow the caller cannot run
// is NotFound here as it is in RunNow.
func (s *Service) PreviewReplay(
	ctx context.Context,
	req *connect.Request[reliantv1.PreviewReplayRequest],
) (*connect.Response[reliantv1.PreviewReplayResponse], error) {
	userID := auth.MustGetUserID(ctx)
	msg := req.Msg
	if msg.GetTriggerId() != "" {
		return s.previewAutomationEvent(ctx, msg)
	}
	if strings.TrimSpace(msg.GetWorkflow()) == "" {
		return nil, svcerr.InvalidArgument("workflow is required")
	}
	if msg.GetReplay() == nil {
		return nil, svcerr.InvalidArgument("replay is required")
	}
	replay, err := triggers.ReplayFromProto(msg.GetReplay(), msg.GetSender())
	if err != nil {
		return nil, svcerr.InvalidArgument(err.Error())
	}

	wf, err := launch.ResolveRunWorkflow(ctx, s.deps.Repo, userID, msg.GetWorkflow(), msg.GetProjectId())
	if err != nil {
		var lookup *launch.WorkflowLookupError
		if errors.As(err, &lookup) {
			return nil, triggerDBError("resolve workflow", err)
		}
		return nil, unresolvedWorkflow(msg.GetWorkflow(), err)
	}

	mapping, err := triggers.MapReplay(ctx, s.deps.Repo, userID, msg.GetWorkflow(), wf, s.activationOf(ctx, userID, msg, replay.TriggerName), replay)
	if err != nil {
		return nil, svcerr.InvalidArgument(err.Error())
	}
	inputs := make(map[string]*structpb.Value, len(mapping.Inputs))
	for name, value := range mapping.Inputs {
		v, err := structpb.NewValue(value)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal(fmt.Sprintf("input %q is not representable", name)))
		}
		inputs[name] = v
	}
	return connect.NewResponse(&reliantv1.PreviewReplayResponse{Inputs: inputs, Prompt: mapping.Prompt}), nil
}

// previewAutomationEvent is a read-only test of the stored automation's mapping.
// Load by owner before inspecting payloads; project/owner/credentials are never
// taken from the sample. No event id lookup, intake or launcher is involved.
func (s *Service) previewAutomationEvent(ctx context.Context, msg *reliantv1.PreviewReplayRequest) (*connect.Response[reliantv1.PreviewReplayResponse], error) {
	trigger, err := s.ownedTrigger(ctx, msg.GetTriggerId())
	if err != nil {
		return nil, err
	}
	if !core.TriggerEventKind(trigger.Kind).IsInbound() {
		return nil, svcerr.InvalidArgument("only event automations accept a test event")
	}
	if msg.GetReplay() == nil {
		return nil, svcerr.InvalidArgument("a sample event is required")
	}
	if msg.GetReplay().GetPayload() != nil && proto.Size(msg.GetReplay().GetPayload()) > 64*1024 {
		return nil, svcerr.InvalidArgument("sample event exceeds 64 KiB")
	}
	replay, err := triggers.ReplayFromProto(msg.GetReplay(), msg.GetSender())
	if err != nil {
		return nil, svcerr.InvalidArgument("Invalid sample event metadata.")
	}
	if replay.OccurredAt.IsZero() {
		replay.OccurredAt = time.Now().UTC()
	}
	if trigger.Kind == core.TriggerKindWebhook {
		// A real generic delivery's sender is this authenticated trigger, never
		// a person claimed by its body (or by preview metadata).
		replay.Sender = &core.TriggerSender{Kind: core.TriggerSenderKindWebhook, ID: trigger.ID, DisplayName: trigger.Name, Verified: true}
	}
	spec, err := triggers.PreviewEvent(ctx, s.deps.Repo, triggers.LaunchWorkflows{Repo: s.deps.Repo}, machinepool.Catalogs{R: s.deps.Repo}, trigger, &core.TriggerEvent{
		Kind: core.TriggerEventKind(trigger.Kind), Payload: replay.Payload, OccurredAt: replay.OccurredAt, Sender: replay.Sender,
	})
	if err != nil {
		return nil, svcerr.InvalidArgument("This sample could not be mapped. Check its fields and the workflow's event settings.")
	}
	inputs := make(map[string]*structpb.Value, len(spec.Params))
	for key, value := range spec.Params {
		inputs[key] = value
	}
	return connect.NewResponse(&reliantv1.PreviewReplayResponse{Inputs: inputs, Prompt: spec.Messages[0].Content}), nil
}

// activationOf is the caller's own activation of the declared trigger a replay
// maps through, when they have one: its params and owner are what
// trigger.activation reads. It is best effort — a replay without an activation
// maps against the workflow's input defaults, and a failure to list is not a
// reason to refuse the preview.
func (s *Service) activationOf(ctx context.Context, userID string, msg *reliantv1.PreviewReplayRequest, triggerName string) *core.Trigger {
	mine, err := s.deps.Repo.ListTriggers(ctx, core.TriggerFilters{UserID: userID, ProjectID: optionalString(msg.GetProjectId())})
	if err != nil {
		return nil
	}
	for _, t := range mine {
		if t.WorkflowTrigger != nil && sameWorkflowRef(t.Workflow, msg.GetWorkflow()) &&
			(triggerName == "" || *t.WorkflowTrigger == triggerName) {
			return t
		}
	}
	return nil
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
