package run

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/errreason"
)

// SignalRun delivers an event to a run that is already going, and never starts
// one.
//
// This is the half of SendMessage that is not "start". SendMessage decides
// between the two by reading the run's status, so a caller cannot say which it
// meant; here the choice is the RPC. A run that is not live reports
// delivered=false rather than quietly starting a new one. Nothing is persisted
// for an undelivered signal; the caller owns retrying.
func (s *Service) SignalRun(
	ctx context.Context,
	req *connect.Request[reliantv1.SignalRunRequest],
) (*connect.Response[reliantv1.SignalRunResponse], error) {
	userID := auth.MustGetUserID(ctx)

	run, chat, err := s.runOwnedBy(ctx, userID, req.Msg.RunId)
	if err != nil {
		return nil, err
	}

	if len(req.Msg.Messages) == 0 {
		return nil, svcerr.InvalidArgument("messages is required")
	}

	// Deliverable means executing, or paused and about to resume. Live() also
	// counts PENDING, but a pending run has never started and SendMessage
	// refuses to start it (the first send is StartChat's), so a signal to it
	// would surface FailedPrecondition instead of delivered=false.
	if !run.Status.Live() || run.Status == db.Pending() {
		return connect.NewResponse(&reliantv1.SignalRunResponse{Delivered: false}), nil
	}

	// Thread names a thread of the run (its id, or the node of an agent
	// running in it). With none, the message goes to the thread an agent of
	// the run is reading — the root of an agent chat, the one running agent
	// of a graph run — and is refused, listing them, when several run in
	// parallel (handlers/chat run_recipient.go). A run that is a sub-agent's
	// keeps its own thread as the default.
	thread := req.Msg.Thread
	if thread == "" && run.ParentID != nil && *run.ParentID != "" {
		thread = run.Thread
	}

	send := &reliantv1.SendMessageRequest{
		ChatId:   chat.ID,
		Messages: req.Msg.Messages,
	}
	if thread != "" {
		send.TargetThread = &thread
	}

	resp, err := s.deps.Chats.SendMessage(ctx, connect.NewRequest(send))
	if err != nil {
		// Live, but no agent is running in it to read the message: nothing
		// was queued. The documented answer to "nowhere to put the event"
		// is delivered=false, not an error; not_delivered_reason says why,
		// and the header repeats it for clients older than the field.
		if errors.Is(err, core.ErrRunNoListeningAgent) {
			out := connect.NewResponse(&reliantv1.SignalRunResponse{
				Delivered:          false,
				NotDeliveredReason: errreason.RunNoListeningAgent,
			})
			out.Header().Set(errreason.NotDeliveredHeader, errreason.RunNoListeningAgent)
			return out, nil
		}
		return nil, err
	}

	out := &reliantv1.SignalRunResponse{Delivered: true}
	if resp.Msg.MessageId != "" {
		out.MessageIds = []string{resp.Msg.MessageId}
	}
	return connect.NewResponse(out), nil
}
