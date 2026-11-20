// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// runNowDefaultMessage is what a manual run starts from when the person wrote
// nothing and no event supplied a prompt. A run needs a user message to start
// (the launcher refuses one without), and "Run now" has no better one to give.
const runNowDefaultMessage = "Run now."

// RunNow starts a workflow by hand: StartChat's attended start, recorded as a
// manual.run launch.
//
// It differs from StartChat in three ways. The launch kind is manual.run,
// which is the server's decision and not a field a client can set on StartChat.
// It works for an automation-only workflow, because pressing Run now is how
// such a workflow is run by hand (refuseChatStart only refuses chat.start). And
// it can replay a past event: the event reaches the run the way a real fire
// delivers it, as a <trigger_event> block after the prompt, and the launch
// event's payload names the event it replayed.
//
// It is not an agent tool. Agents start runs with start_run, which records
// agent.start_run and is bounded by the fork-bomb guard; nothing registers this
// RPC with the tool catalog.
func (s *Service) RunNow(
	ctx context.Context,
	req *connect.Request[reliantv1.RunNowRequest],
) (*connect.Response[reliantv1.RunNowResponse], error) {
	userID := auth.MustGetUserID(ctx)
	msg := req.Msg
	if strings.TrimSpace(msg.GetProjectId()) == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}
	if strings.TrimSpace(msg.GetWorkflow()) == "" {
		return nil, svcerr.InvalidArgument("workflow is required")
	}

	message := strings.TrimSpace(msg.GetMessage())
	var extra map[string]any
	if msg.GetReplay() != nil {
		// The replay's own validation speaks to the person who wrote it, so
		// its words are the message.
		replay, err := triggers.ReplayFromProto(msg.GetReplay(), nil)
		if err != nil {
			return nil, svcerr.InvalidArgument(err.Error())
		}
		if replay.Kind == "" {
			// A failure to read the workflow is classified (an Internal, or the
			// request's deadline); anything else is a verdict about the
			// workflow, whose words name the remedy.
			wf, err := launch.ResolveRunWorkflow(ctx, s.deps.Repo, userID, msg.GetWorkflow(), msg.GetProjectId())
			if err != nil {
				return nil, svcerr.WrapOr(err, svcerr.WithMessage(svcerr.ErrNotFound,
					fmt.Sprintf("workflow %q does not resolve to a runnable workflow: %v", msg.GetWorkflow(), err)))
			}
			replay.Kind, err = triggers.ReplayKind(wf, replay.TriggerName)
			if err != nil {
				return nil, svcerr.InvalidArgument(err.Error())
			}
		}
		message = triggers.SeedWithReplay(message, replay)
		extra = map[string]any{"replay": replayRecord(replay)}
	}
	if message == "" {
		message = runNowDefaultMessage
	}

	start := &reliantv1.StartChatRequest{
		ProjectId:       msg.GetProjectId(),
		WorktreeId:      msg.WorktreeId,
		Workflow:        msg.GetWorkflow(),
		WorkflowParams:  msg.GetInputs(),
		SelectedPresets: msg.GetPresets(),
		Messages:        []*reliantv1.InputMessage{{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: message}},
		DaemonId:        msg.DaemonId,
		ClientMessageId: msg.ClientMessageId,
		NoMachine:       proto.Bool(msg.GetNoMachine()),
	}
	resp, err := s.startChat(ctx, userID, start, core.TriggerEventKindManualRun, extra)
	if err != nil {
		return nil, err
	}
	chat := resp.Msg.GetChat()
	// The person who pressed the button owns this run from its first turn, so it
	// is adopted at birth: it lists in the sidebar and is replied to as a chat,
	// not offered for take-over like an automation's. Best effort — the run is
	// already started, and adopting it later is one click.
	if chat.GetId() != "" {
		if _, adoptErr := s.deps.Repo.SetChatAdopted(ctx, chat.GetId(), userID, true); adoptErr != nil {
			logging.WarnContext(ctx, "Failed to adopt a manual run", "chatID", chat.GetId(), "error", adoptErr)
		} else {
			now := time.Now().UTC().Format(time.RFC3339)
			chat.AdoptedAt = &now
		}
	}
	return connect.NewResponse(&reliantv1.RunNowResponse{
		Chat:       chat,
		WorkflowId: resp.Msg.GetWorkflowId(),
		RunId:      resp.Msg.GetRunId(),
	}), nil
}

// replayRecord is what a manual run's launch event remembers of the event it
// replayed: which one, through which trigger, and when it first happened. The
// payload itself is not copied: the original event row holds it, and a replay
// the person edited is in the run's seed message.
func replayRecord(r triggers.Replay) map[string]any {
	record := map[string]any{
		"trigger_name": r.TriggerName,
		"kind":         string(r.Kind),
	}
	if r.EventID != "" {
		record["event_id"] = r.EventID
	}
	if !r.OccurredAt.IsZero() {
		record["occurred_at"] = r.OccurredAt.UTC().Format(time.RFC3339)
	}
	return record
}
