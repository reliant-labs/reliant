// Copyright (c) 2025 Reliant Labs
package run

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/authz"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/runstateproto"
)

// runOwnedBy loads a run and verifies the caller owns the chat it belongs to.
//
// Ownership lives on the chat, not the run, so authorization goes through it.
// A run whose chat the caller does not own reports NotFound rather than
// PermissionDenied, matching ChatService — existence is not disclosed.
func (s *Service) runOwnedBy(ctx context.Context, userID, runID string) (*core.Workflow, *core.Chat, error) {
	if runID == "" {
		return nil, nil, svcerr.InvalidArgument("run_id is required")
	}

	run, err := s.deps.Workflows.LoadWorkflow(ctx, runID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to get run"))
	}
	if run == nil {
		return nil, nil, svcerr.NotFound("run")
	}

	chat, err := s.deps.Repo.GetChat(ctx, run.ChatID)
	if err != nil {
		return nil, nil, svcerr.WrapOr(err, svcerr.Internal("failed to get run"))
	}
	if !authz.CanWriteChat(ctx, userID, chat) {
		return nil, nil, svcerr.NotFound("run")
	}

	return run, chat, nil
}

// runToProto converts a workflow row to the wire Run.
func runToProto(run *core.Workflow) *reliantv1.Run {
	if run == nil {
		return nil
	}

	out := &reliantv1.Run{
		Id:           run.ID,
		WorkflowName: run.WorkflowName,
		Thread:       run.Thread,
		SessionId:    run.ChatID,
		State:        runstateproto.State(run.Status.State),
		StopReason:   runstateproto.StopReason(run.Status.StopReason),
		CreatedAtMs:  run.CreatedAt.UnixMilli(),
	}
	if run.ParentID != nil {
		out.ParentId = *run.ParentID
	}
	if run.SpawnedByNodeID != nil {
		out.SpawnedByNodeId = *run.SpawnedByNodeID
	}
	if run.LoopIteration != nil {
		out.LoopIteration = *run.LoopIteration
	}
	if run.Outcome != nil {
		out.Outcome = *run.Outcome
	}
	if run.FailureDetail != nil {
		out.FailureDetail = *run.FailureDetail
	}
	if run.RunOutcome != nil {
		out.RunOutcome = runstateproto.Outcome(*run.RunOutcome)
	}
	if run.CompletedAt != nil {
		out.CompletedAtMs = run.CompletedAt.UnixMilli()
	}
	return out
}

const (
	defaultRunsPageSize = 50
	maxRunsPageSize     = 200
)

// mergeRunLog makes one page of the run log from a page of runs and a page
// of unlaunched trigger events, each fetched with the same keyset cursor and
// up to limit rows: the newest limit of the two together, by (time, id)
// descending, and the cursor the next page continues from — nil on the last
// page. A run's time is its start, an event's the time its firing was for.
//
// Either source having more rows than it returned means the page may be cut
// short; so does the merge dropping rows past limit. In both cases the
// cursor is the last row KEPT, so the next page resumes exactly where this
// one stopped and nothing either source returned past it is lost: it comes
// back on the next page.
func mergeRunLog(
	runs []*db.RunListItem, runsMore bool,
	events []*db.UnlaunchedTriggerEvent, eventsMore bool,
	limit int,
) ([]*db.RunListItem, []*db.UnlaunchedTriggerEvent, *db.RunCursor) {
	keptRuns := make([]*db.RunListItem, 0, len(runs))
	keptEvents := make([]*db.UnlaunchedTriggerEvent, 0, len(events))
	var last *db.RunCursor
	i, j := 0, 0
	for len(keptRuns)+len(keptEvents) < limit && (i < len(runs) || j < len(events)) {
		takeRun := j >= len(events)
		if i < len(runs) && j < len(events) {
			run, event := runs[i], events[j]
			takeRun = run.CreatedAt.After(event.OccurredAt) ||
				(run.CreatedAt.Equal(event.OccurredAt) && run.ChatID > event.ID)
		}
		if takeRun {
			keptRuns = append(keptRuns, runs[i])
			last = &db.RunCursor{CreatedAt: runs[i].CreatedAt, ChatID: runs[i].ChatID}
			i++
		} else {
			keptEvents = append(keptEvents, events[j])
			last = &db.RunCursor{CreatedAt: events[j].OccurredAt, ChatID: events[j].ID}
			j++
		}
	}
	more := runsMore || eventsMore || i < len(runs) || j < len(events)
	if !more || last == nil {
		return keptRuns, keptEvents, nil
	}
	return keptRuns, keptEvents, last
}

// runItemToProto converts a run-list row, which carries the chat and trigger
// columns on top of the workflow row's.
func runItemToProto(item *db.RunListItem) *reliantv1.Run {
	out := &reliantv1.Run{
		Id:           item.RunID,
		WorkflowName: item.WorkflowName,
		Thread:       item.RunID,
		SessionId:    item.ChatID,
		State:        runstateproto.State(item.RootStatus.State),
		StopReason:   runstateproto.StopReason(item.RootStatus.StopReason),
		Outcome:      item.Outcome,
		RunOutcome:   runstateproto.Outcome(item.RunOutcome),
		CreatedAtMs:  item.CreatedAt.UnixMilli(),
		Title:        item.Title,
		ProjectId:    item.ProjectID,
		LaunchKind:   item.LaunchKind,
		TriggerId:    item.TriggerID,
		TriggerName:  item.TriggerName,
		DaemonId:     item.DaemonID,
		Activity:     reliantv1.ChatActivity(item.Activity),
		DisplayState: reliantv1.RunDisplayState(item.DisplayState),
		Archived:     item.Archived,
	}
	if item.CompletedAt != nil {
		out.CompletedAtMs = item.CompletedAt.UnixMilli()
	}
	if item.ParentChatID != "" {
		out.ParentChatId = &item.ParentChatID
		if item.ParentChatTitle != "" {
			out.ParentChatTitle = &item.ParentChatTitle
		}
	}
	return out
}

// The page token is the last row's (created_at, chat id), base64url-encoded.
// It is opaque to callers; the keyset it encodes is the list's sort key, so a
// row inserted mid-pagination can neither repeat nor be skipped.
func encodeRunCursor(c db.RunCursor) string {
	return orm.EncodeCursor(strconv.FormatInt(c.CreatedAt.UnixMicro(), 10) + "|" + c.ChatID)
}

func decodeRunCursor(token string) (*db.RunCursor, error) {
	raw, err := orm.DecodeCursor(token)
	if err != nil {
		return nil, err
	}
	micros, id, ok := strings.Cut(raw, "|")
	if !ok || id == "" {
		return nil, fmt.Errorf("malformed cursor")
	}
	n, err := strconv.ParseInt(micros, 10, 64)
	if err != nil {
		return nil, err
	}
	return &db.RunCursor{CreatedAt: time.UnixMicro(n).UTC(), ChatID: id}, nil
}

// filterRunsToProto applies the state filter and paging, then converts.
func filterRunsToProto(runs []*core.Workflow, req *reliantv1.ListRunsRequest) *reliantv1.ListRunsResponse {
	filtered := make([]*core.Workflow, 0, len(runs))
	for _, run := range runs {
		if run == nil {
			continue
		}
		if req.GetState() != reliantv1.WorkflowState_WORKFLOW_STATE_UNSPECIFIED &&
			runstateproto.State(run.Status.State) != req.GetState() {
			continue
		}
		filtered = append(filtered, run)
	}

	total := int32(len(filtered))

	windowed := filtered
	if req.Limit > 0 && int(req.Limit) < len(windowed) {
		windowed = windowed[:req.Limit]
	}

	out := make([]*reliantv1.Run, 0, len(windowed))
	for _, run := range windowed {
		out = append(out, runToProto(run))
	}
	return &reliantv1.ListRunsResponse{Runs: out, Total: total}
}
