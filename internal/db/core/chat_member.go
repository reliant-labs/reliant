// Copyright (c) 2025 Reliant Labs

package core

import (
	"errors"
	"time"
)

// ChatMemberRole is the level a person holds on a chat they do not own
// (chat_members; control-plane docs/design/chat-collaboration.md §2). Levels
// nest: each includes everything below it. The owner is never a member.
//
// Only internal/authz decides from it and only internal/store reads or writes
// it: every other package asks authz (CanReplyChat, CanManageChat,
// ReadChatAccess) rather than reading a role.
type ChatMemberRole string

const (
	// ChatMemberView reads the chat, as an org-wide viewer of a shared chat
	// does, even when it is private.
	ChatMemberView ChatMemberRole = "view"
	// ChatMemberReply is view, plus acting in the chat (send, answer).
	ChatMemberReply ChatMemberRole = "reply"
	// ChatMemberManage is reply, plus steering the run, its settings and its
	// members.
	ChatMemberManage ChatMemberRole = "manage"
)

// MaxChatMembers is the most members one chat may have (§3.1): it bounds the
// re-authorization and feed fan-out a chat's members cost.
const MaxChatMembers = 25

// ErrChatMembersFull is a grant refused because the chat already has
// MaxChatMembers members.
var ErrChatMembersFull = errors.New("the chat already has the most members it may have")

// rank orders the roles; 0 is not a role.
func (r ChatMemberRole) rank() int {
	switch r {
	case ChatMemberView:
		return 1
	case ChatMemberReply:
		return 2
	case ChatMemberManage:
		return 3
	}
	return 0
}

// Valid reports whether r is a role this build knows.
func (r ChatMemberRole) Valid() bool { return r.rank() > 0 }

// AtLeast reports whether r includes min. An unknown role includes nothing,
// and nothing includes an unknown min.
func (r ChatMemberRole) AtLeast(min ChatMemberRole) bool {
	return r.Valid() && min.Valid() && r.rank() >= min.rank()
}

// ChatMember is one person's grant on a chat.
type ChatMember struct {
	ChatID string
	UserID string
	Role   ChatMemberRole
	// GrantedBy is who set the level they hold now.
	GrantedBy string
	// CreatedAt is when they first became a member.
	CreatedAt time.Time
}

// ChatMemberChange is one change of one person's grant on a chat and the
// audit row it writes (share_events), applied together
// (store.ApplyChatMemberChange). internal/authz decides it
// (authz.ChatMemberChange).
type ChatMemberChange struct {
	ChatID string
	// OrgID is the chat's org; OwnerUserID its owner. The write applies only
	// while the chat still has both, so a change decided on a stale read
	// writes nothing.
	OrgID       string
	OwnerUserID string
	// ActorUserID is who made the change: the owner, a member at manage, or
	// the member removing their own grant.
	ActorUserID  string
	TargetUserID string
	// Role is the level the change leaves; empty removes the grant.
	Role ChatMemberRole
	At   time.Time
}

// Share actions of a change to a chat's members (share_events.action).
const (
	// ShareActionGrant is a person given a level on a chat.
	ShareActionGrant ShareAction = "grant"
	// ShareActionRoleChange is a member's level changed.
	ShareActionRoleChange ShareAction = "role_change"
	// ShareActionRevoke is a member's grant removed.
	ShareActionRevoke ShareAction = "revoke"
)
