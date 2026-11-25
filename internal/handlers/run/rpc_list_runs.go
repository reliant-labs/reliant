package run

import (
	"context"

	"github.com/reliant-labs/reliant/internal/authz"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListRuns lists runs. With session_id or parent_id it lists that session's
// root runs or that run's children (one chat's worth, filtered in memory).
// With neither it is the cross-cutting run list: every root run the caller
// owns, narrowed by the request's filters and paged by keyset.
func (s *Service) ListRuns(
	ctx context.Context,
	req *connect.Request[reliantv1.ListRunsRequest],
) (*connect.Response[reliantv1.ListRunsResponse], error) {
	userID := auth.MustGetUserID(ctx)
	// The org a request names is verified (and a machine credential's applied)
	// before any branch: one chat's runs are its chat's org's, so a session or
	// parent outside the scope lists nothing.
	scope, err := s.listScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	switch {
	case req.Msg.GetParentId() != "":
		// Ownership is checked via the parent, which resolves to a chat.
		_, chat, err := s.runOwnedBy(ctx, userID, req.Msg.GetParentId())
		if err != nil {
			return nil, err
		}
		if !scope.Includes(chat.OrgID) {
			return connect.NewResponse(filterRunsToProto(nil, req.Msg)), nil
		}
		children, err := s.deps.Repo.ListChildWorkflows(ctx, req.Msg.GetParentId())
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list runs"))
		}
		return connect.NewResponse(filterRunsToProto(children, req.Msg)), nil

	case req.Msg.GetSessionId() != "":
		chat, err := s.deps.ChatRows.GetChatRow(ctx, req.Msg.GetSessionId())
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get session"))
		}
		if !authz.CanReadChat(ctx, userID, chat) {
			return nil, svcerr.NotFound("session")
		}
		if !scope.Includes(chat.OrgID) {
			return connect.NewResponse(filterRunsToProto(nil, req.Msg)), nil
		}
		runs, err := s.deps.Repo.ListRootWorkflows(ctx, req.Msg.GetSessionId())
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list runs"))
		}
		return connect.NewResponse(filterRunsToProto(runs, req.Msg)), nil

	default:
		return s.listAllRuns(ctx, userID, scope, req.Msg)
	}
}

func (s *Service) listAllRuns(
	ctx context.Context,
	userID string,
	scope *core.OrgScope,
	req *reliantv1.ListRunsRequest,
) (*connect.Response[reliantv1.ListRunsResponse], error) {
	limit := int(req.Limit)
	if limit <= 0 {
		limit = defaultRunsPageSize
	}
	if limit > maxRunsPageSize {
		limit = maxRunsPageSize
	}

	filters := db.RunListFilters{
		UserID:          userID,
		Org:             scope,
		ProjectID:       req.ProjectId,
		ProjectIDs:      req.ProjectIds,
		Workflows:       req.Workflow,
		TriggerID:       req.TriggerId,
		LaunchKinds:     req.LaunchKind,
		Query:           req.Query,
		ParentChatID:    req.ParentChatId,
		IncludeArchived: req.GetIncludeArchived(),
		Limit:           limit,
	}
	for _, st := range req.DisplayStates {
		if st == reliantv1.RunDisplayState_RUN_DISPLAY_STATE_UNSPECIFIED {
			return nil, svcerr.InvalidArgument("display_states must not contain UNSPECIFIED")
		}
		filters.DisplayStates = append(filters.DisplayStates, db.RunDisplayState(st))
	}
	if req.StartedAfter != nil {
		t := req.StartedAfter.AsTime()
		filters.StartedAfter = &t
	}
	if req.StartedBefore != nil {
		t := req.StartedBefore.AsTime()
		filters.StartedBefore = &t
	}
	if req.PageToken != nil && *req.PageToken != "" {
		cursor, err := decodeRunCursor(*req.PageToken)
		if err != nil {
			return nil, svcerr.InvalidArgument("invalid page_token")
		}
		filters.After = cursor
	}

	items, hasMore, err := s.deps.Repo.ListRuns(ctx, filters)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list runs"))
	}
	var next *db.RunCursor
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next = &db.RunCursor{CreatedAt: last.CreatedAt, ChatID: last.ChatID}
	}
	var events []*db.UnlaunchedTriggerEvent
	if req.GetIncludeUnlaunchedEvents() {
		var eventsMore bool
		events, eventsMore, err = s.deps.RunLists.ListUnlaunchedTriggerEvents(ctx, filters)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list runs"))
		}
		items, events, next = mergeRunLog(items, hasMore, events, eventsMore, limit)
	}
	// The store lists what the caller owns; a run (its chat) or a firing (its
	// trigger) of an org they have left is not theirs to see, named org or
	// not. Filtered after the page and its cursor are cut from the rows as
	// read, so a page of denied rows is short, never the end of the log.
	items = authz.Visible(ctx, userID, items, func(ctx context.Context, userID string, item *db.RunListItem) bool {
		return authz.CanReadOwn(ctx, userID, item.OrgID)
	})
	events = authz.Visible(ctx, userID, events, func(ctx context.Context, userID string, event *db.UnlaunchedTriggerEvent) bool {
		return authz.CanReadOwn(ctx, userID, event.OrgID)
	})

	resp := runLogResponse(items, events)
	if next != nil {
		resp.NextPageToken = encodeRunCursor(*next)
	}
	return connect.NewResponse(resp), nil
}

func runLogResponse(items []*db.RunListItem, events []*db.UnlaunchedTriggerEvent) *reliantv1.ListRunsResponse {
	resp := &reliantv1.ListRunsResponse{Runs: make([]*reliantv1.Run, 0, len(items))}
	for _, item := range items {
		resp.Runs = append(resp.Runs, runItemToProto(item))
	}
	for _, event := range events {
		resp.UnlaunchedEvents = append(resp.UnlaunchedEvents, unlaunchedEventToProto(event))
	}
	return resp
}

func unlaunchedEventToProto(event *db.UnlaunchedTriggerEvent) *reliantv1.UnlaunchedTriggerEvent {
	outcome := reliantv1.UnlaunchedTriggerEventOutcome_UNLAUNCHED_TRIGGER_EVENT_OUTCOME_UNSPECIFIED
	switch event.Outcome {
	case core.TriggerEventSkipped:
		outcome = reliantv1.UnlaunchedTriggerEventOutcome_UNLAUNCHED_TRIGGER_EVENT_OUTCOME_SKIPPED
	case core.TriggerEventFailed:
		outcome = reliantv1.UnlaunchedTriggerEventOutcome_UNLAUNCHED_TRIGGER_EVENT_OUTCOME_FAILED
	}
	return &reliantv1.UnlaunchedTriggerEvent{
		Id:              event.ID,
		TriggerId:       event.TriggerID,
		TriggerName:     event.TriggerName,
		WorkflowName:    event.WorkflowName,
		WorkflowTrigger: event.WorkflowTrigger,
		ProjectId:       event.ProjectID,
		LaunchKind:      event.Kind,
		OccurredAtMs:    event.OccurredAt.UnixMilli(),
		Outcome:         outcome,
		OutcomeDetail:   event.OutcomeDetail,
	}
}
