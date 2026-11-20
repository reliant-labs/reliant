package chat

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/messageproto"
	"go.opentelemetry.io/otel/trace"
)

// GetChatUpdates fetches chat updates since a sequence number
func (s *Service) GetChatUpdates(
	ctx context.Context,
	req *reliantv1.GetChatUpdatesRequest,
) (*reliantv1.GetChatUpdatesResponse, error) {
	info := serverkit.CallInfoV2(ctx)
	userID := auth.MustGetUserID(ctx)

	if req.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}

	// Get chat with ownership check (defense-in-depth via single query)
	err := s.requireChatOwner(ctx, req.ChatId, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}

	// Get updates since sequence
	updates, err := s.deps.Repo.GetUpdatesSince(ctx, req.ChatId, req.SinceSeq, 0)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to fetch updates"))
	}

	// Get latest sequence for client
	latestSeq, err := s.deps.Repo.GetLatestUpdateSequence(ctx, req.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get latest sequence"))
	}

	// The compaction watermark, read AFTER the updates: compaction raises it
	// before it deletes, and it only rises, so a value at or below since_seq
	// proves nothing under the updates above was deleted, and anything a
	// concurrent compaction took from this read is under the value reported.
	// Unreadable, the poll fails rather than claim a log it cannot vouch for.
	compactedThrough, err := s.deps.Repo.GetChatCompactedThrough(ctx, req.ChatId)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the chat's compaction watermark"))
	}
	// Below the watermark the log no longer holds every transition: refuse the
	// replay rather than hand back a partial one a poller would advance past.
	if req.SinceSeq < compactedThrough {
		through := strconv.FormatInt(compactedThrough, 10)
		return nil, svcerr.WithMetadata(svcerr.WithReason(svcerr.OutOfRange(fmt.Sprintf(
			"chat updates through sequence %s were compacted, so a replay from %d would be incomplete: re-read the chat's current state and poll again from since_seq %s",
			through, req.SinceSeq, through)), ChatHistoryCompactedReason), ChatCompactedThroughHeader, through)
	}

	// Convert to proto messages
	toolBodies := messageproto.ResolveToolBodyMode(req.GetToolBodies(), s.deps.Config.GetTranscript().GetToolBodyStubs())
	protoUpdates := make([]*reliantv1.ChatUpdate, len(updates))
	sent := messageproto.TranscriptBytes{ToolBodies: toolBodies}
	for i, update := range updates {
		data := []byte(update.Data)
		if update.UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE {
			// The same tool-result preview every client-facing read sends.
			data = messageproto.PreviewMessageUpdate(data, toolBodies)
		}
		dataJSON := string(data)
		sent.AddChatUpdate(update.UpdateType, dataJSON)
		protoUpdates[i] = &reliantv1.ChatUpdate{
			SequenceNumber: update.SequenceNumber,
			UpdateType:     update.UpdateType.String(),
			EntityId:       update.EntityID,
			Data:           dataJSON,
			CreatedAt:      update.CreatedAt.Format(time.RFC3339),
		}
	}

	// What this read sent, by block kind, on the RPC's span.
	trace.SpanFromContext(ctx).SetAttributes(sent.Attributes("transcript")...)

	resp := &reliantv1.GetChatUpdatesResponse{
		Updates:        protoUpdates,
		Total:          int32(len(updates)),
		LatestSequence: latestSeq,
	}
	info.ResponseHeader().Set(ChatCompactedThroughHeader, strconv.FormatInt(compactedThrough, 10))
	return resp, nil
}
