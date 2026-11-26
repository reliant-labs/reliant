package trigger

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
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

	mine, err := s.deps.Triggers.ListTriggersForUser(ctx, core.TriggerFilters{UserID: userID, ProjectID: optionalString(msg.GetProjectId())})
	if err != nil {
		return nil, triggerDBError("list triggers", err)
	}
	// Firings of a trigger the caller may no longer read (an org they left,
	// a credential's other org) are not candidates.
	mine = authz.Visible(ctx, userID, mine, authz.CanReadTrigger)
	var candidates []*reliantv1.ReplayCandidate
	for _, t := range mine {
		if t.WorkflowTrigger == nil || !sameWorkflowRef(t.Workflow, msg.GetWorkflow()) {
			continue
		}
		firings, _, err := s.deps.FiringReads.ListTriggerEventsPage(ctx, core.TriggerEventFilters{
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
