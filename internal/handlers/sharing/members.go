package sharing

import (
	"context"
	"errors"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/types/known/timestamppb"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Reasons (x-forge-error-reason) a change of a chat's members is refused for,
// beyond NotFound (no such chat, or one the caller may not read), ReasonNoOrg
// and the person-only refusal.
const (
	// ReasonCollaborationDisabled: the deployment has not turned chat
	// collaboration on (chat_collaboration.enabled).
	ReasonCollaborationDisabled = "chat_collaboration_disabled"
	// ReasonNeedsManage: a reader of the chat who is neither its owner nor a
	// member at manage asked to change its members.
	ReasonNeedsManage = "needs_manage"
	// ReasonOwnerNotMember: the change names the chat's owner.
	ReasonOwnerNotMember = "owner_not_member"
	// ReasonNotOrgMember: a grant to someone who is not a live member of the
	// chat's organization.
	ReasonNotOrgMember = "not_org_member"
	// ReasonMembersFull: the chat already has core.MaxChatMembers members.
	ReasonMembersFull = "chat_members_full"
)

// GrantsAllowed reports whether the deployment's rollout gate lets anyone
// grant a person access to a chat: only when chat_collaboration.enabled is
// set. nil is off.
func GrantsAllowed(gate *configv1.ChatCollaborationConfig) bool {
	return gate != nil && gate.GetEnabled()
}

// changeMember decides and applies one change of targetID's grant on chatID
// (role "" removes it), and returns the action recorded ("" for none).
func (s *Service) changeMember(ctx context.Context, chatID, targetID string, role core.ChatMemberRole) (core.ShareAction, error) {
	userID := auth.MustGetUserID(ctx)
	if err := auth.RequirePerson(ctx, "change who has access to this chat"); err != nil {
		return "", err
	}
	// Deciding who may change it reads the row as a reader could, so a
	// viewer who may not change it is told so rather than NotFound.
	ctx = authz.WithViewerReads(ctx)
	chat, err := s.deps.Chats.GetChat(ctx, chatID)
	if err != nil {
		return "", svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	change, err := authz.ChatMemberChange(ctx, userID, chat, targetID, role)
	if err != nil {
		return "", memberRefusal(ctx, err)
	}
	var action core.ShareAction
	// ReadCommitted: ApplyChatMemberChange locks the chat's row (FOR UPDATE)
	// before it reads or counts the members, so concurrent changes of one
	// chat serialize on that lock and the cap is counted under it.
	err = s.deps.Tx.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		action, err = s.deps.Members.ApplyChatMemberChange(ctx, change)
		return err
	})
	if errors.Is(err, core.ErrChatMembersFull) {
		return "", svcerr.WithReason(svcerr.FailedPrecondition(
			"this chat already has the most members it may have; remove someone first"), ReasonMembersFull)
	}
	if err != nil {
		return "", svcerr.WrapOr(err, svcerr.Internal("failed to change who has access to the chat"))
	}
	return action, nil
}

// memberRefusal is the answer to a member change authz refused.
func memberRefusal(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, authz.ErrNotChatManager):
		return svcerr.WithReason(svcerr.PermissionDenied(
			"only the chat's owner or a member who can manage it can change who has access to it"), ReasonNeedsManage)
	case errors.Is(err, authz.ErrOwnerNotMember):
		return svcerr.WithReason(svcerr.FailedPrecondition(
			"that is the chat's owner, whose access no grant changes"), ReasonOwnerNotMember)
	case errors.Is(err, authz.ErrGranteeNotOrgMember):
		return svcerr.WithReason(svcerr.FailedPrecondition(
			"that person is not a member of the chat's organization"), ReasonNotOrgMember)
	case errors.Is(err, authz.ErrInvalidChatRole):
		return svcerr.InvalidArgument("role must be VIEW, REPLY or MANAGE")
	}
	return shareRefusal(ctx, "chat", err)
}

// roleFrom is the level a request asks for.
func roleFrom(r pb.ChatMemberRole) (core.ChatMemberRole, error) {
	switch r {
	case pb.ChatMemberRole_CHAT_MEMBER_ROLE_VIEW:
		return core.ChatMemberView, nil
	case pb.ChatMemberRole_CHAT_MEMBER_ROLE_REPLY:
		return core.ChatMemberReply, nil
	case pb.ChatMemberRole_CHAT_MEMBER_ROLE_MANAGE:
		return core.ChatMemberManage, nil
	}
	return "", svcerr.InvalidArgument("role must be VIEW, REPLY or MANAGE")
}

// roleProto is a level on the wire; an unknown one is UNSPECIFIED.
func roleProto(r core.ChatMemberRole) pb.ChatMemberRole {
	switch r {
	case core.ChatMemberView:
		return pb.ChatMemberRole_CHAT_MEMBER_ROLE_VIEW
	case core.ChatMemberReply:
		return pb.ChatMemberRole_CHAT_MEMBER_ROLE_REPLY
	case core.ChatMemberManage:
		return pb.ChatMemberRole_CHAT_MEMBER_ROLE_MANAGE
	}
	return pb.ChatMemberRole_CHAT_MEMBER_ROLE_UNSPECIFIED
}

func memberProto(m *core.ChatMember) *pb.ChatMember {
	if m == nil {
		return nil
	}
	return &pb.ChatMember{
		UserId: m.UserID, Role: roleProto(m.Role), GrantedBy: m.GrantedBy, CreatedAt: timestamppb.New(m.CreatedAt),
	}
}
