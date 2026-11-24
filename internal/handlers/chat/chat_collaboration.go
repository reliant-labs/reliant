// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/authortag"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgfunding"
)

// A chat's reply and manage members may send into it (control-plane
// docs/design/chat-collaboration.md §5). A member's message is delivered to
// the OWNER's agent: it runs on the owner's machine, under the owner's run
// identity and model choices, paid for by the org. So SendMessage keeps two
// people apart from its first lines:
//
//   - the sender (auth.MustGetUserID) is who is authorized, who the message is
//     recorded as from (messages.author_user_id), whose queue is capped and
//     whose client ids are theirs;
//   - the run user (chat.UserID) is everyone the run itself is about: model
//     preferences and routing, the live-run updates, the machine it wakes,
//     the launch, and the owner's attendance.
//
// With one writer the two were always the same person. Every call below that
// touches the run takes the run user; nothing a member sends can reach the
// owner's settings, because a member's request may carry text, attachments
// and a client id only (§5.2).

// maxQueuedPerMember bounds the undelivered messages one member may hold in
// someone else's chat, so a member cannot bury the owner's own follow-ups
// (§7). The owner is never bounded.
const maxQueuedPerMember = 20

// memberClientIDSpace scopes a member's client message ids: the stored id of
// a member's message is derived from (author, client id), so two people in
// one chat never share an id space (§6.3). The owner's ids are stored as
// sent.
var memberClientIDSpace = uuid.MustParse("6f1d2a3c-8b4e-4d5f-9a60-c01ab0a7e510")

// collaborationOn reports whether chat collaboration is on
// (chat_collaboration.enabled): only then may anyone but a chat's owner send
// or answer in it.
func (s *Service) collaborationOn() bool {
	return s.deps.Collaboration.GetEnabled()
}

// chatForSend reads the chat a send is for, if sender may send to it: its
// owner always, and with collaboration on a member holding reply or manage
// (authz.CanReplyChat, which a visibility never satisfies). Anyone else is
// core.ErrChatNotFound, exactly as a chat that does not exist.
func (s *Service) chatForSend(ctx context.Context, chatID, sender string) (*db.Chat, error) {
	if !s.collaborationOn() {
		return s.getChatForUser(ctx, chatID, sender)
	}
	chat, err := s.deps.Repo.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	if !authz.CanReplyChat(ctx, sender, chat) {
		return nil, fmt.Errorf("%w: %s", core.ErrChatNotFound, chatID)
	}
	return chat, nil
}

// refuseMemberRequest refuses what a member's send may not carry (§5.2): a
// reply is text, attachments and a client message id. System-role messages
// carry the owner's authority into the prompt (needs_owner); the workflow,
// temperature, token limit, parameters, mode and presets are the run's
// settings (needs_manage). The target thread is where the message goes, as
// SendAgentMessage's is, and stays allowed.
func refuseMemberRequest(msg *reliantv1.SendMessageRequest, systemMessages []*reliantv1.InputMessage) error {
	if len(systemMessages) > 0 {
		return svcerr.WithReason(svcerr.PermissionDenied("only the chat's owner can send system messages"), errreason.NeedsOwner)
	}
	var set []string
	if msg.GetWorkflow() != "" {
		set = append(set, "workflow")
	}
	if msg.Temperature != nil {
		set = append(set, "temperature")
	}
	if msg.MaxTokens != nil {
		set = append(set, "max_tokens")
	}
	if len(msg.WorkflowParams) > 0 {
		set = append(set, "workflow_params")
	}
	if msg.GetMode() != "" {
		set = append(set, "mode")
	}
	if len(msg.SelectedPresets) > 0 {
		set = append(set, "selected_presets")
	}
	if len(set) > 0 {
		return svcerr.WithReason(svcerr.PermissionDenied(fmt.Sprintf(
			"a reply to someone else's chat carries text and attachments only; %s change the chat's run and need manage",
			strings.Join(set, ", "))), errreason.NeedsManage)
	}
	return nil
}

// memberMessageID is the id a member's message is stored under: derived from
// the author and the id their client chose, so a member who learns the
// owner's client id cannot take it, and a retry of the member's own send
// still finds its message (§6.3). "" stays "".
func memberMessageID(sender, clientID string) string {
	if clientID == "" {
		return ""
	}
	return uuid.NewSHA1(memberClientIDSpace, []byte(sender+"\x00"+clientID)).String()
}

// requireMemberQueueRoom refuses a member's send while they already hold
// maxQueuedPerMember undelivered messages in the chat (§7).
func (s *Service) requireMemberQueueRoom(ctx context.Context, chatID, sender string) error {
	n, err := s.deps.AgentMessages.CountQueuedHumanMessagesByAuthor(ctx, chatID, sender)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to count queued messages"))
	}
	if n >= maxQueuedPerMember {
		return svcerr.WithReason(svcerr.ResourceExhausted(fmt.Sprintf(
			"you already have %d messages waiting in this chat; send again once the agent has read them", n)),
			errreason.ReplyQueueFull)
	}
	return nil
}

// requireOwnAttachments refuses a member's send that names an attachment
// they did not upload. Attaching one makes it readable by the chat's owner
// (§6.4), so a member must not be able to put someone else's file there. A
// missing reader refuses, failing closed.
func (s *Service) requireOwnAttachments(ctx context.Context, sender string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if s.deps.Attachments == nil {
		return svcerr.NotFound("attachment")
	}
	uploaders, err := s.deps.Attachments.AttachmentUploaders(ctx, ids)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("failed to read attachments"))
	}
	for _, id := range ids {
		if !authz.CanAccessPersonal(sender, uploaders[id]) {
			return svcerr.NotFound("attachment")
		}
	}
	return nil
}

// displayName is userID's display name in the chat's org, "" when the chat
// has no org, no directory is wired, or control-plane has none (an older
// control plane, an outage, a member with no name). Never an email or an id.
func (s *Service) displayName(ctx context.Context, chat *db.Chat, userID string) string {
	if chat.OrgID == nil || *chat.OrgID == "" || s.deps.Members == nil {
		return ""
	}
	m, ok, err := s.deps.Members.Membership(ctx, userID, *chat.OrgID)
	if err != nil {
		logging.WarnContext(ctx, "Could not read a chat author's display name; recording none", "error", err, "chatID", chat.ID)
		return ""
	}
	if !ok || !m.Member {
		return ""
	}
	return strings.TrimSpace(m.DisplayName)
}

// sendAuthor is who a send's human messages are recorded as from (§6.1). A
// member: their id and display name. The owner, in a chat of an org: their
// display name alone (author_user_id stays NULL, which means the owner), so
// their turns are named once a second person writes, and so the worker can
// tell that the owner spoke after a member (store.LatestHumanTurnAuthor). A
// chat with no org has no members, and its owner's messages stay unstamped.
//
// The owner's name is read only with collaboration on; with it off their
// rows carry authortag's neutral fallback, which is what they render as
// anyway.
func (s *Service) sendAuthor(ctx context.Context, chat *db.Chat, sender string) db.MessageAuthor {
	if chat.OrgID == nil || *chat.OrgID == "" {
		return db.MessageAuthor{}
	}
	if !authz.IsChatOwner(sender, chat) {
		return db.MessageAuthor{UserID: sender, Name: s.displayName(ctx, chat, sender)}
	}
	name := ""
	if s.collaborationOn() {
		name = s.displayName(ctx, chat, sender)
	}
	if name == "" {
		name = authortag.FallbackOwnerName
	}
	return db.MessageAuthor{Name: name}
}

// ownerLabel is how a refusal names the chat's owner to a member.
func (s *Service) ownerLabel(ctx context.Context, chat *db.Chat) string {
	if name := s.displayName(ctx, chat, chat.UserID); name != "" {
		return name
	}
	return "the chat's owner"
}

// refusePersonallyFunded refuses a member's send to a chat its owner put on
// their own AI account (§5.3 rule 1): a live personal-funding lease, a
// personal_first funding order, or a provider the owner picked in the
// composer. Subscriptions are never shared, and the funding choice is the
// owner's alone, so only the owner may send then. Nothing is queued. A read
// that fails, or a reader that is not wired, refuses too: the worker would
// hold the turn anyway (rule 2), and a refusal the member can read beats a
// silent wait.
func (s *Service) refusePersonallyFunded(ctx context.Context, chat *db.Chat) error {
	if chat.OrgID == nil || *chat.OrgID == "" {
		return nil
	}
	personal, err := s.chatPersonallyFunded(ctx, chat)
	if err != nil {
		logging.WarnContext(ctx, "Could not read how a chat is funded; refusing a member's send", "error", err, "chatID", chat.ID)
	}
	if err == nil && !personal {
		return nil
	}
	return svcerr.WithReason(svcerr.FailedPrecondition(fmt.Sprintf(
		"this chat runs on %s's own AI account, so only they can send to it until it is back on the organization's accounts",
		s.ownerLabel(ctx, chat))), errreason.ChatPersonallyFunded)
}

// chatPersonallyFunded reports whether the chat's owner has put it on their
// own account, by the same three rows the worker's funding plan reads
// (activities/handlers personal_funding.go fundingPlanFor).
func (s *Service) chatPersonallyFunded(ctx context.Context, chat *db.Chat) (bool, error) {
	if s.deps.FundingLeases == nil || s.deps.PersonPicks == nil || s.deps.Settings == nil {
		return false, fmt.Errorf("chat funding readers are not wired")
	}
	lease, err := s.deps.FundingLeases.GetChatPersonalFunding(ctx, chat.ID)
	if err != nil {
		return false, fmt.Errorf("read the chat's personal funding: %w", err)
	}
	// A row of another person or org says nothing about this chat (as
	// fundingPlanFor ignores it). An unserved-model lease holds while the
	// chat stays on its model; the lease's own model is that model.
	if lease != nil && authz.IsChatOwner(lease.UserID, chat) && lease.OrgID == *chat.OrgID &&
		lease.LeaseActive(time.Now(), lease.Model) {
		return true, nil
	}
	// The chat's own order outranks the owner's per-org setting, as in the
	// worker; either one at personal_first puts the owner's account first.
	sel, err := s.deps.Selections.GetChatSelection(ctx, chat.ID)
	if err != nil {
		return false, fmt.Errorf("read the chat's funding order: %w", err)
	}
	var order orgfunding.Order
	if sel != nil {
		order = orgfunding.ParseOrder(string(sel.Policy.Funding))
	}
	if order == "" && s.deps.Settings != nil {
		if v, ok := s.deps.Settings.GetSettingValue(ctx, chat.UserID, orgfunding.SettingKey); ok {
			order = orgfunding.Parse(v).For(*chat.OrgID).Order
		}
	}
	if order == orgfunding.OrderPersonalFirst {
		return true, nil
	}
	pick, err := s.deps.PersonPicks.ChatPersonPickDriver(ctx, chat.ID, *chat.OrgID)
	if err != nil {
		return false, fmt.Errorf("read the chat's provider pick: %w", err)
	}
	return pick != "", nil
}

// memberSendChecks are the refusals of a member's send (§5.2, §5.3, §6.4),
// all before anything is written.
func (s *Service) memberSendChecks(ctx context.Context, req *reliantv1.SendMessageRequest, chat *db.Chat, sender string, systemMessages []*reliantv1.InputMessage) error {
	if err := refuseMemberRequest(req, systemMessages); err != nil {
		return err
	}
	if err := s.requireOwnAttachments(ctx, sender, req.Attachments); err != nil {
		return err
	}
	return s.refusePersonallyFunded(ctx, chat)
}

// MemberReplyRefusal is the refusal of a member's answer to a question in
// chat, for the question service: the same funding rule a member's send
// meets (§5.3 rule 1), because an answer drives the owner's run as a message
// does. nil for the owner.
func (s *Service) MemberReplyRefusal(ctx context.Context, chat *db.Chat, sender string) error {
	if chat == nil || authz.IsChatOwner(sender, chat) {
		return nil
	}
	return s.refusePersonallyFunded(ctx, chat)
}

// ReplyAuthor is who a human message sender writes into chat is recorded as
// from (sendAuthor), for the question service's answers.
func (s *Service) ReplyAuthor(ctx context.Context, chat *db.Chat, sender string) db.MessageAuthor {
	if chat == nil {
		return db.MessageAuthor{}
	}
	return s.sendAuthor(ctx, chat, sender)
}
