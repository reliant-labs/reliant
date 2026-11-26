package trigger

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// defaultTriggerEventLimit bounds ListTriggerEvents when the caller asks for
// everything. The history of a schedule is unbounded, so an unlimited default
// would grow into a response nobody can render.
const defaultTriggerEventLimit = 50

// maxTriggerEventLimit caps what a caller can ask for.
const maxTriggerEventLimit = 500

// ListTriggerEvents returns a trigger's firings, newest first.
func (s *Service) ListTriggerEvents(
	ctx context.Context,
	req *connect.Request[reliantv1.ListTriggerEventsRequest],
) (*connect.Response[reliantv1.ListTriggerEventsResponse], error) {
	// Resolve ownership through the TRIGGER, not the events: the event rows
	// carry a user id, but checking the trigger is what makes "a trigger id
	// you do not own is NotFound" true.
	if _, err := s.ownedTrigger(ctx, req.Msg.TriggerId); err != nil {
		return nil, err
	}

	limit := int(req.Msg.Limit)
	switch {
	case limit <= 0:
		limit = defaultTriggerEventLimit
	case limit > maxTriggerEventLimit:
		limit = maxTriggerEventLimit
	}

	filters := core.TriggerEventFilters{
		UserID:    auth.MustGetUserID(ctx),
		TriggerID: req.Msg.TriggerId,
		Limit:     limit,
	}
	for _, o := range req.Msg.Outcomes {
		outcome, ok := triggerOutcomeFromProto(o)
		if !ok {
			return nil, svcerr.InvalidArgument("outcomes: unspecified outcome")
		}
		filters.Outcomes = append(filters.Outcomes, outcome)
	}
	if req.Msg.PageToken != nil && *req.Msg.PageToken != "" {
		cursor, err := decodeTriggerEventCursor(*req.Msg.PageToken)
		if err != nil {
			return nil, svcerr.InvalidArgument("invalid page_token")
		}
		filters.After = cursor
	}

	stored, hasMore, err := s.deps.FiringReads.ListTriggerEventsPage(ctx, filters)
	if err != nil {
		return nil, triggerDBError("list trigger events", err)
	}

	out := make([]*reliantv1.TriggerEvent, 0, len(stored))
	for _, ev := range stored {
		proto, err := triggers.EventWithRunToProto(ev)
		if err != nil {
			return nil, svcerr.WrapOr(err, svcerr.Internal("render trigger event"))
		}
		out = append(out, proto)
	}
	resp := &reliantv1.ListTriggerEventsResponse{Events: out}
	if hasMore && len(stored) > 0 {
		last := stored[len(stored)-1].Event
		resp.NextPageToken = encodeTriggerEventCursor(core.TriggerEventCursor{OccurredAt: last.OccurredAt, ID: last.ID})
	}
	return connect.NewResponse(resp), nil
}

// The page token is the last firing's (occurred_at, id), base64url-encoded and
// opaque to callers. It is the list's sort key, so a firing recorded
// mid-pagination can neither repeat nor be skipped.
func encodeTriggerEventCursor(c core.TriggerEventCursor) string {
	return orm.EncodeCursor(strconv.FormatInt(c.OccurredAt.UnixMicro(), 10) + "|" + c.ID)
}

func decodeTriggerEventCursor(token string) (*core.TriggerEventCursor, error) {
	raw, err := orm.DecodeCursor(token)
	if err != nil {
		return nil, err
	}
	micros, id, ok := strings.Cut(raw, "|")
	if !ok || id == "" {
		return nil, errors.New("malformed cursor")
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return nil, err
	}
	return &core.TriggerEventCursor{OccurredAt: time.UnixMicro(n).UTC(), ID: id}, nil
}

func triggerOutcomeFromProto(o reliantv1.TriggerEventOutcome) (core.TriggerEventOutcome, bool) {
	switch o {
	case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_LAUNCHED:
		return core.TriggerEventLaunched, true
	case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_SKIPPED:
		return core.TriggerEventSkipped, true
	case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_FAILED:
		return core.TriggerEventFailed, true
	case reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_PENDING:
		return core.TriggerEventPending, true
	}
	return "", false
}
