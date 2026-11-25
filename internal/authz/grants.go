// Copyright (c) 2025 Reliant Labs

package authz

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Grants reads the level a person holds on a chat (chat_members; control-plane
// docs/design/chat-collaboration.md §4.2): "" and no error when they hold
// none. It is read uncached: a grant is reliant's own row, and a revoke must
// bite at the next decision. store.ChatMemberExtStore is one.
type Grants interface {
	ChatMemberRole(ctx context.Context, chatID, userID string) (core.ChatMemberRole, error)
}

type grantsKey struct{}

// processGrants is the Grants of a decision whose context carries none
// (SetProcessGrants).
var processGrants atomic.Pointer[Grants]

// SetProcessGrants installs g as the Grants of every decision whose context
// carries none. A process installs its own once, where it opens the database
// every role shares (app.NewDBRepo), so its requests, activities and
// background all read grants without each edge wiring them; a context's own
// (WithGrants) wins. nil clears it, and a decision with neither fails closed:
// no grant admits anyone.
func SetProcessGrants(g Grants) {
	if g == nil {
		processGrants.Store(nil)
		return
	}
	processGrants.Store(&g)
}

// WithGrants returns ctx carrying g, which every decision on that context
// reads a caller's grant from, ahead of the process's.
func WithGrants(ctx context.Context, g Grants) context.Context {
	return context.WithValue(ctx, grantsKey{}, g)
}

// grantsOf is the Grants a decision on ctx reads: ctx's own, else the
// process's, else nil.
func grantsOf(ctx context.Context) Grants {
	if g, ok := ctx.Value(grantsKey{}).(Grants); ok && g != nil {
		return g
	}
	if g := processGrants.Load(); g != nil {
		return *g
	}
	return nil
}

// chatGrant is userID's level on r, a chat: "" for none. No Grants wired is
// ErrDenied, logged at ERROR; an error reading the grant is
// ErrMembershipUnavailable (fail closed, and not an answer); a level this build
// does not know is ErrDenied. A resource that is not a chat has no grants.
func chatGrant(ctx context.Context, userID string, r resource) (core.ChatMemberRole, error) {
	if r.chatID == "" {
		return "", nil
	}
	g := grantsOf(ctx)
	if g == nil {
		logging.ErrorContext(ctx, "[authz] no grants reader on the context or the process; no chat grant admits anyone (authz.SetProcessGrants was never called)",
			"user_id", userID, "chat_id", r.chatID)
		return "", ErrDenied
	}
	role, err := g.ChatMemberRole(ctx, r.chatID, userID)
	if err != nil {
		logging.WarnContext(ctx, "[authz] could not read a chat grant; denying", "user_id", userID, "chat_id", r.chatID, "error", err)
		return "", ErrMembershipUnavailable
	}
	if role != "" && !role.Valid() {
		logging.WarnContext(ctx, "[authz] a chat grant with a level this build does not know; denying", "user_id", userID, "chat_id", r.chatID, "role", string(role))
		return "", ErrDenied
	}
	return role, nil
}

// memberDeny is the member rule, for a chat: the owner (deny), or a caller
// holding at least min on it (chatGrant) who is a live member of its org
// (liveMember), acting as a person (WithCredentialKind), through no credential
// bound to another org. The chat's visibility is never read: an org-wide
// viewer is not a member. The grant is asked before membership, so a caller
// who holds none is refused without asking control-plane about them. nil
// allows; a non-owner's refusal is ErrDenied, or ErrMembershipUnavailable
// when a grant or membership could not be asked.
func memberDeny(ctx context.Context, userID string, r resource, min core.ChatMemberRole) error {
	if !r.present || userID == "" || r.ownerUserID == userID {
		return deny(ctx, userID, r)
	}
	if r.chatID == "" || r.orgID == "" || r.orgless || !personCredential(ctx) {
		return ErrDenied
	}
	if bound, ok := boundOrg(ctx); ok && bound != r.orgID {
		logging.InfoContext(ctx, "[authz] a credential bound to another org; denying a chat member", "user_id", userID, "credential_org", bound, "org_id", r.orgID)
		recordCredentialRefusal(ctx, r.orgID)
		return ErrDenied
	}
	role, err := chatGrant(ctx, userID, r)
	if err != nil {
		return err
	}
	if !role.AtLeast(min) {
		return ErrDenied
	}
	return liveMember(ctx, userID, r.orgID, "a chat member")
}

// Refusals of a change to a chat's members (ChatMemberChange) beyond
// ErrDenied (the call site's NotFound), ErrPersonRequired, ErrShareNeedsOrg
// and ErrMembershipUnavailable.
var (
	// ErrNotChatManager: a reader of the chat who is neither its owner nor a
	// member at manage asked to change its members.
	ErrNotChatManager = errors.New("authz: only the chat's owner or a member at manage may change who has access to it")
	// ErrOwnerNotMember: the change names the chat's owner, who is never a
	// member: their access is ownership, which no grant changes.
	ErrOwnerNotMember = errors.New("authz: the chat's owner is not a member")
	// ErrGranteeNotOrgMember: a grant to someone who is not a live member of
	// the chat's org.
	ErrGranteeNotOrgMember = errors.New("authz: the person is not a live member of the chat's organization")
	// ErrInvalidChatRole: a level this build does not know (above manage
	// there is only ownership, which is never granted).
	ErrInvalidChatRole = errors.New("authz: unknown chat member level")
)

// ChatMemberChange decides actorID giving targetID role on chat, or, with
// role empty, removing targetID's grant (control-plane
// docs/design/chat-collaboration.md §3.2): the change to apply, or why it is
// refused.
//
//   - The owner (CanOwnChat) and a member at manage (CanManageChat) may grant
//     view, reply or manage, change a level, and remove anyone but the owner.
//     Any member may remove their own grant.
//   - Every change is a person's act (ErrPersonRequired), and a chat that
//     belongs to no org has no members (ErrShareNeedsOrg).
//   - Nobody grants above manage (ErrInvalidChatRole) or names the owner
//     (ErrOwnerNotMember).
//   - A grantee must be a live member of the chat's org, asked now
//     (ErrGranteeNotOrgMember); removing a grant asks nothing of the person,
//     so a departed member's row can always be cleared.
//   - Anyone else who may read the chat is ErrNotChatManager; anyone who may
//     not is ErrDenied (NotFound: it does not exist for them).
//
// The returned change carries the chat's owner and org, so applying it after
// either moved writes nothing (store.ApplyChatMemberChange). Whether it
// changes anything (the level already held, no grant to remove), and the cap
// of core.MaxChatMembers, are the store's to say.
func ChatMemberChange[C chatRecord](ctx context.Context, actorID string, chat C, targetID string, role core.ChatMemberRole) (core.ChatMemberChange, error) {
	if role != "" && !role.Valid() {
		return core.ChatMemberChange{}, ErrInvalidChatRole
	}
	r := chatResource(chat)
	if err := deny(ctx, actorID, r); err != nil {
		if r.present && r.ownerUserID == actorID {
			// The owner who may not act on it (removed from its org, or an
			// outage) gets what they would get for any other change.
			if errors.Is(err, ErrMembershipUnavailable) {
				return core.ChatMemberChange{}, err
			}
			return core.ChatMemberChange{}, ErrDenied
		}
		switch mErr := memberDeny(ctx, actorID, r, core.ChatMemberManage); {
		case mErr == nil:
		case errors.Is(mErr, ErrMembershipUnavailable):
			return core.ChatMemberChange{}, mErr
		case role == "" && targetID == actorID && memberDeny(ctx, actorID, r, core.ChatMemberView) == nil:
			// Leaving: a member may always give their own grant up.
		case isViewer(WithViewerReads(ctx), actorID, r):
			return core.ChatMemberChange{}, ErrNotChatManager
		default:
			return core.ChatMemberChange{}, ErrDenied
		}
	}
	if !personCredential(ctx) {
		return core.ChatMemberChange{}, ErrPersonRequired
	}
	if r.orgID == "" {
		return core.ChatMemberChange{}, ErrShareNeedsOrg
	}
	if targetID == "" {
		return core.ChatMemberChange{}, ErrDenied
	}
	if targetID == r.ownerUserID {
		return core.ChatMemberChange{}, ErrOwnerNotMember
	}
	if role != "" {
		switch err := liveMember(ctx, targetID, r.orgID, "a chat grant"); {
		case errors.Is(err, ErrMembershipUnavailable):
			return core.ChatMemberChange{}, err
		case err != nil:
			return core.ChatMemberChange{}, ErrGranteeNotOrgMember
		}
	}
	return core.ChatMemberChange{
		ChatID: r.chatID, OrgID: r.orgID, OwnerUserID: r.ownerUserID,
		ActorUserID: actorID, TargetUserID: targetID, Role: role,
	}, nil
}
