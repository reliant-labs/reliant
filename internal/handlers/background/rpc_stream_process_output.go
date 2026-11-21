package background

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// StreamProcessOutput streams real-time process output by subscribing to the
// daemon's output channel via the router.
func (s *Service) StreamProcessOutput(
	ctx context.Context,
	req *connect.Request[reliantv1.StreamProcessOutputRequest],
	stream *connect.ServerStream[reliantv1.ProcessOutputEvent],
) error {
	userID, err := s.getUserID(ctx)
	if err != nil {
		return err
	}

	if req.Msg.ProcessId == "" {
		return svcerr.InvalidArgument("process_id is required")
	}

	// A process lives on one machine. The router resolves a named one among
	// the caller's own machines only, so another user's id is not found.
	if daemonID := req.Msg.GetDaemonId(); daemonID != "" {
		ctx = toolexec.WithDaemonSelector(ctx, &toolexec.DaemonSelector{ID: daemonID})
	}
	outputCh, unsub, err := s.deps.Router.SubscribeProcessOutput(ctx, userID, req.Msg.ProcessId, req.Msg.NewOnly)
	if err != nil {
		return daemonFailure("subscribe process output", err)
	}
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			return nil
		case evt, ok := <-outputCh:
			if !ok {
				// Channel closed — subscription ended.
				return nil
			}

			if evt.IsComplete {
				exitCode := evt.ExitCode
				if err := stream.Send(&reliantv1.ProcessOutputEvent{
					Event: &reliantv1.ProcessOutputEvent_Complete{
						Complete: &reliantv1.ProcessOutputComplete{
							ExitCode: &exitCode,
						},
					},
				}); err != nil {
					return err
				}
				return nil
			}

			streamType := reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_STDOUT
			if evt.Stream == "stderr" {
				streamType = reliantv1.OutputStreamType_OUTPUT_STREAM_TYPE_STDERR
			}

			if err := stream.Send(&reliantv1.ProcessOutputEvent{
				Event: &reliantv1.ProcessOutputEvent_Line{
					Line: &reliantv1.ProcessOutputLine{
						Type:     streamType,
						Text:     evt.Data,
						Sequence: int64(evt.Sequence),
					},
				},
			}); err != nil {
				return err
			}
		}
	}
}
