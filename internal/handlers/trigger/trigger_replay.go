// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
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
		return nil, svcerr.ToConnect(svcerr.InvalidArgument("workflow is required"))
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
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("render event %s: %w", firing.Event.ID, err))
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
	if strings.TrimSpace(msg.GetWorkflow()) == "" {
		return nil, svcerr.ToConnect(svcerr.InvalidArgument("workflow is required"))
	}
	if msg.GetReplay() == nil {
		return nil, svcerr.ToConnect(svcerr.InvalidArgument("replay is required"))
	}
	replay, err := triggers.ReplayFromProto(msg.GetReplay(), msg.GetSender())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	wf, err := launch.ResolveRunWorkflow(ctx, s.deps.Repo, userID, msg.GetWorkflow(), msg.GetProjectId())
	if err != nil {
		var lookup *launch.WorkflowLookupError
		if errors.As(err, &lookup) {
			return nil, triggerDBError("resolve workflow", err)
		}
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("workflow %q does not resolve to a runnable workflow: %w", msg.GetWorkflow(), err))
	}

	mapping, err := triggers.MapReplay(ctx, s.deps.Repo, userID, msg.GetWorkflow(), wf, s.activationOf(ctx, userID, msg, replay.TriggerName), replay)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	inputs := make(map[string]*structpb.Value, len(mapping.Inputs))
	for name, value := range mapping.Inputs {
		v, err := structpb.NewValue(value)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("input %q is not representable: %w", name, err))
		}
		inputs[name] = v
	}
	return connect.NewResponse(&reliantv1.PreviewReplayResponse{Inputs: inputs, Prompt: mapping.Prompt}), nil
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
