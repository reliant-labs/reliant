package trigger

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/authz"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// GetLaunchEvent returns the event that launched a chat. Ownership is the
// chat's: another user's chat is NotFound, never PermissionDenied, and a chat
// that exists but has no launch event yields an empty response.
//
// The payload is returned as recorded. What the launcher records is the start
// (workflow, presets, params), the seed fingerprint and the source's own
// fields; the caller's JWT is never part of it (it travels in the in-memory
// Spec), so owner-only access is the whole boundary.
func (s *Service) GetLaunchEvent(
	ctx context.Context,
	req *connect.Request[reliantv1.GetLaunchEventRequest],
) (*connect.Response[reliantv1.GetLaunchEventResponse], error) {
	userID := auth.MustGetUserID(ctx)
	if req.Msg.ChatId == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	chat, err := s.deps.Chats.GetChatRow(ctx, req.Msg.ChatId)
	if err != nil && !svcerr.IsNotFound(err) {
		return nil, triggerDBError("get chat", err)
	}
	if err != nil || !authz.CanReadChat(ctx, userID, chat) {
		return nil, svcerr.NotFound("chat")
	}
	ev, err := s.deps.FiringReads.GetTriggerEventByChatID(ctx, chat.ID)
	if err != nil {
		if errors.Is(err, core.ErrTriggerEventNotFound) {
			return connect.NewResponse(&reliantv1.GetLaunchEventResponse{}), nil
		}
		return nil, triggerDBError("get launch event", err)
	}
	if !authz.CanReadTriggerEvent(ctx, userID, ev) {
		return connect.NewResponse(&reliantv1.GetLaunchEventResponse{}), nil
	}
	out, err := triggers.EventToProto(ev)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("render launch event"))
	}
	return connect.NewResponse(&reliantv1.GetLaunchEventResponse{Event: out}), nil
}
