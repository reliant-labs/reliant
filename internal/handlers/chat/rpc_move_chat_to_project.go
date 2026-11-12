// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemonchoice"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
)

// MOVING A CHAT TO ANOTHER PROJECT.
//
// A chat's directory, system prompt and tool paths are resolved per turn from
// its project and machine (resolveCallCheckout), so the move itself is the
// chat row: its project, the new project's main worktree and a machine that
// has the new project. What does not follow is the history: earlier messages
// name the old checkout's paths. The hidden note written with the move tells
// the model so, the way a branch's note tells it what a branch is.
//
// The rules:
//   - both the chat and the project are the caller's (another user's reads as
//     not found);
//   - the chat is idle or stopped on an error. A run in progress, a question
//     waiting for the user, a paused run or a message queued for the machine
//     would carry on with the old project's paths and catalog under the new
//     project; the user stops or finishes it first. The check is repeated in
//     the write's own statement, so a run starting meanwhile wins;
//   - a branch workspace belongs to its project and stays there, so a chat in
//     one moves only with leave_workspace, onto the new project's main
//     checkout;
//   - the machine is the one named (daemonchoice.Validate: the caller's, with
//     the project installed), else the chat's own when it has the project,
//     else the project's default (projectcheckout.Resolve). A no-machine chat
//     stays one unless a machine is named.

// moveCheckoutPurpose finishes projectcheckout's "… to <purpose>".
const moveCheckoutPurpose = "move this chat there"

// crossesOrgs reports whether a chat of org from moving to a project of org to
// would change its org. A side with no org yet (written before orgs) belongs
// to its owner's default org, def, and is stamped with it later, so it is
// compared as that org: a legacy chat cannot move into another org's project
// (the backfill would then give it the project's org), and an assigned chat
// cannot move into a legacy project of another org.
func crossesOrgs(from, to *string, def string) bool {
	effective := func(org *string) string {
		if org == nil {
			return def
		}
		return *org
	}
	return effective(from) != effective(to)
}

// moveOrgs is the org decision of a move: the org the chat belongs to, the
// caller's default org when the decision needed it, and whether the target
// project belongs to another org.
type moveOrgs struct {
	org     string // "": neither side has an org yet, which the move statement re-checks
	def     string
	crosses bool
}

// moveOrg resolves the chat's org as the backfill (orgs.ListScope's stamp)
// would give it one: its own, else its project's, else the caller's default.
// Comparing the raw columns would let a chat with no org in an org's project
// move to a project with no org, and the backfill then stamp it with the
// caller's default instead of the org it was in. No orgs on the deployment
// crosses nothing; a default that cannot be resolved fails closed
// (Unavailable).
func (s *Service) moveOrg(ctx context.Context, userID string, chat *db.Chat, source, target *db.Project) (moveOrgs, error) {
	from := chat.OrgID
	if from == nil {
		from = source.OrgID
	}
	to := target.OrgID
	if from != nil && to != nil {
		return moveOrgs{org: *from, crosses: *from != *to}, nil
	}
	if (from == nil && to == nil) || s.deps.Orgs == nil {
		// Both sides take the same default when they are stamped.
		return moveOrgs{}, nil
	}
	def, err := s.deps.Orgs.DefaultOrg(ctx, userID)
	if err != nil {
		return moveOrgs{}, orgs.LookupFailed(ctx, "could not resolve your default organization; try again", err)
	}
	org := def
	if from != nil {
		org = *from
	}
	return moveOrgs{org: org, def: def, crosses: crossesOrgs(from, to, def)}, nil
}

// MoveChatToProject moves an idle chat to another of the caller's projects.
func (s *Service) MoveChatToProject(
	ctx context.Context,
	req *connect.Request[reliantv1.MoveChatToProjectRequest],
) (*connect.Response[reliantv1.MoveChatToProjectResponse], error) {
	userID := auth.MustGetUserID(ctx)
	chatID, projectID := req.Msg.GetChatId(), req.Msg.GetProjectId()
	if chatID == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if projectID == "" {
		return nil, svcerr.InvalidArgument("project_id is required")
	}

	chat, err := s.deps.Repo.GetChatWithUserCheck(ctx, chatID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	target, err := s.ownedProject(ctx, userID, projectID)
	if err != nil {
		return nil, err
	}
	if chat.ProjectID == projectID {
		return connect.NewResponse(&reliantv1.MoveChatToProjectResponse{Chat: s.chatToProtoWithSelection(ctx, chat)}), nil
	}
	if chat.State == db.ChatStateArchived {
		return nil, svcerr.FailedPrecondition("restore the chat before moving it to another project")
	}
	if reason := moveBlockedReason(chat); reason != "" {
		return nil, svcerr.FailedPrecondition(reason)
	}
	source, err := s.ownedProject(ctx, userID, chat.ProjectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load the chat's project"))
	}
	// Nothing moves between orgs (owner decision, 2026-10-26): a chat that
	// belongs to an org may move only to a project of the same org. The move
	// statement re-checks this and stamps the chat's org, so a backfill
	// between this read and the write cannot change the answer.
	orgDecision, err := s.moveOrg(ctx, userID, chat, source, target)
	if err != nil {
		return nil, err
	}
	if orgDecision.crosses {
		return nil, svcerr.FailedPrecondition("that project belongs to another organization; a chat stays in its organization")
	}

	from, err := s.moveSource(ctx, userID, chat, source)
	if err != nil {
		return nil, err
	}
	if from.workspace != nil && !req.Msg.GetLeaveWorkspace() {
		return nil, svcerr.FailedPrecondition(fmt.Sprintf(
			"this chat runs in workspace %q, which belongs to project %q and stays there; "+
				"move it with leave_workspace to continue in %q's main checkout",
			from.workspace.Name, source.Name, target.Name))
	}

	worktreeID, err := s.launcher().ResolveChatWorktreeID(ctx, target.ID, nil)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to resolve the project's main checkout"))
	}
	to, err := s.moveDestination(ctx, userID, chat, target, req.Msg.DaemonId)
	if err != nil {
		return nil, err
	}
	noteThread, err := s.moveNoteThread(ctx, chat)
	if err != nil {
		return nil, err
	}
	note := moveNote(source, from, target, to)

	// ReadCommitted, not Serializable: what makes the move correct is the chat
	// row lock its statement runs under (store.MoveChatToProject), the one a
	// workflow's admission takes too, and the activity check inside that
	// statement. The note commits with the move or not at all: a moved chat
	// whose next turn reads the old project's paths with no warning would act
	// on paths that no longer apply.
	var moved bool
	if err := s.deps.Repo.RunTx(ctx, orm.ReadCommitted, func(txCtx context.Context) error {
		moved, err = s.deps.Repo.MoveChatToProject(txCtx, db.ChatMove{
			UserID:        userID,
			ChatID:        chat.ID,
			FromProjectID: source.ID,
			ToProjectID:   target.ID,
			WorktreeID:    *worktreeID,
			DaemonID:      to.daemonID,
			OrgID:         optionalString(orgDecision.org),
			DefaultOrgID:  optionalString(orgDecision.def),
		})
		if err != nil || !moved {
			return err
		}
		configData, err := json.Marshal(map[string]interface{}{
			"chat_id":             chat.ID,
			"project_id":          target.ID,
			"previous_project_id": source.ID,
			"worktree_id":         *worktreeID,
			"active_daemon_id":    stringOrEmpty(to.daemonID),
			"no_machine":          chat.NoMachine && to.daemonID == nil,
		})
		if err != nil {
			return err
		}
		if err := s.deps.Repo.CreateUserUpdate(txCtx, &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &target.ID,
			WorktreeID: worktreeID,
			ChatID:     &chat.ID,
			UpdateType: db.UserUpdateChatConfigChanged,
			EntityType: db.EntityTypeChat,
			EntityID:   chat.ID,
			Data:       configData,
		}); err != nil {
			return err
		}
		return s.writeMoveNote(txCtx, chat.ID, noteThread, note)
	}); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to move chat"))
	}
	if !moved {
		// The chat changed between the read and the write: a run started,
		// or another move took it elsewhere.
		return nil, svcerr.FailedPrecondition("the chat changed while it was being moved (a run started, it was moved elsewhere, or its organization changed); try again once it is idle")
	}
	logging.InfoContext(ctx, "[MoveChatToProject] moved chat to another project",
		"chatID", chat.ID, "fromProjectID", source.ID, "toProjectID", target.ID,
		"daemonID", stringOrEmpty(to.daemonID), "leftWorkspace", from.workspace != nil)

	updated, err := s.getChatForUser(ctx, chat.ID, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to reload chat"))
	}
	return connect.NewResponse(&reliantv1.MoveChatToProjectResponse{Chat: s.chatToProtoWithSelection(ctx, updated)}), nil
}

// ownedProject is the caller's project; another user's reads as not found.
func (s *Service) ownedProject(ctx context.Context, userID, projectID string) (*db.Project, error) {
	project, err := s.deps.Repo.GetProject(ctx, projectID)
	if err != nil && !errors.Is(err, svcerr.ErrNotFound) {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load the project"))
	}
	if !authz.CanWriteProject(ctx, userID, project) {
		return nil, svcerr.NotFound("project")
	}
	return project, nil
}

// moveBlockedReason is why a chat cannot move now, "" when it can: it is idle,
// or its last run stopped on an error. Read from the activity the sidebar
// shows, so the refusal names the state the user sees.
func moveBlockedReason(chat *db.Chat) string {
	if chat.Activity == nil {
		return ""
	}
	switch reliantv1.ChatActivity(*chat.Activity) {
	case reliantv1.ChatActivity_CHAT_ACTIVITY_IDLE, reliantv1.ChatActivity_CHAT_ACTIVITY_ERROR:
		return ""
	case reliantv1.ChatActivity_CHAT_ACTIVITY_AWAITING_INPUT:
		return "this chat is waiting for your answer; answer or stop it before moving it to another project"
	case reliantv1.ChatActivity_CHAT_ACTIVITY_PAUSED:
		return "this chat is paused; resume it to the end or stop it before moving it to another project"
	case reliantv1.ChatActivity_CHAT_ACTIVITY_QUEUED_FOR_MACHINE:
		return "this chat has a message queued for its machine; wait for it to run or stop it before moving it to another project"
	default:
		return "this chat is running; wait for it to finish or stop it before moving it to another project"
	}
}

// movePlace is where a chat runs, on one side of a move: for the note.
type movePlace struct {
	// path is the directory the chat ran in, or will run in; "" when unknown
	// (no checkout on the chat's machine).
	path string
	// workspace is the branch workspace the chat leaves; nil for the main
	// checkout.
	workspace *db.Worktree
	// daemonID is the machine; nil for none pinned.
	daemonID *string
	// machine names daemonID for the note; "" when none is pinned.
	machine string
	// noMachine: the chat runs without a machine by design, so it has no
	// working directory.
	noMachine bool
}

// moveSource is where the chat runs now: its branch workspace, or its
// project's checkout on its machine. Only reading the workspace can fail the
// move; a checkout that cannot be resolved just leaves the note vaguer.
func (s *Service) moveSource(ctx context.Context, userID string, chat *db.Chat, project *db.Project) (movePlace, error) {
	if chat.WorktreeID != nil && *chat.WorktreeID != "" {
		worktree, err := s.deps.Repo.GetWorktree(ctx, *chat.WorktreeID)
		switch {
		case errors.Is(err, core.ErrWorktreeNotFound):
		case err != nil:
			return movePlace{}, svcerr.WrapOr(err, svcerr.Internal("failed to load the chat's workspace"))
		case !worktree.IsMain:
			return movePlace{workspace: worktree, path: worktree.Path}, nil
		}
	}
	return movePlace{path: s.checkoutPath(ctx, userID, project, chat.ActiveDaemonID)}, nil
}

// moveDestination picks the machine the chat runs on in project, and its
// checkout there.
func (s *Service) moveDestination(ctx context.Context, userID string, chat *db.Chat, project *db.Project, requested *string) (movePlace, error) {
	daemonID, err := s.moveMachine(ctx, userID, chat, project, requested)
	if err != nil {
		return movePlace{}, err
	}
	if chat.NoMachine && daemonID == nil {
		return movePlace{noMachine: true}, nil
	}
	to := movePlace{daemonID: daemonID, path: s.checkoutPath(ctx, userID, project, daemonID)}
	if daemonID != nil {
		to.machine = daemonplacement.MachineName(ctx, s.deps.Repo, *daemonID)
	}
	return to, nil
}

// moveMachine is the machine the chat is pinned to in project: requested when
// named; else the chat's own when the project accepts it; else none for a
// no-machine chat or a project installed nowhere; else the project's default.
func (s *Service) moveMachine(ctx context.Context, userID string, chat *db.Chat, project *db.Project, requested *string) (*string, error) {
	if requested != nil && *requested != "" {
		if err := daemonchoice.Validate(ctx, s.deps.Repo, userID, project.ID, *requested); err != nil {
			return nil, err
		}
		return requested, nil
	}
	if chat.NoMachine {
		return nil, nil
	}
	installs, err := s.deps.Repo.ListProjectDaemonsForProject(ctx, project.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to read the project's machines"))
	}
	current := chat.ActiveDaemonID
	if current != nil && *current != "" && daemonchoice.Validate(ctx, s.deps.Repo, userID, project.ID, *current) == nil {
		return current, nil
	}
	if len(installs) == 0 {
		// Installed nowhere yet: any machine is accepted, as at StartChat,
		// so the chat keeps its pin (or default resolution).
		return current, nil
	}
	machines, _ := s.deps.Router.(daemonplacement.Machines)
	checkout, err := projectcheckout.Resolve(ctx, s.deps.Repo, machines, userID, project, moveCheckoutPurpose)
	if err != nil {
		return nil, err
	}
	if err := daemonchoice.Validate(ctx, s.deps.Repo, userID, project.ID, checkout.DaemonID); err != nil {
		return nil, err
	}
	return &checkout.DaemonID, nil
}

// checkoutPath is project's checkout on daemonID (or where its work is
// placed, with no machine pinned); "" when there is none.
func (s *Service) checkoutPath(ctx context.Context, userID string, project *db.Project, daemonID *string) string {
	machines, _ := s.deps.Router.(daemonplacement.Machines)
	target, err := projectcheckout.ForMachine(ctx, s.deps.Repo, machines, userID, project, stringOrEmpty(daemonID), moveCheckoutPurpose)
	if err != nil {
		return ""
	}
	return target.Path
}

// moveNote is the hidden note a moved chat's model reads on its next turn.
// Earlier messages name the old checkout's paths, and nothing rewrites them;
// the note says which paths those are and where the chat works now.
func moveNote(from *db.Project, src movePlace, to *db.Project, dst movePlace) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This conversation was moved from project %q to project %q.\n\n", from.Name, to.Name)

	b.WriteString("Everything above this note happened in ")
	switch {
	case src.workspace != nil:
		fmt.Fprintf(&b, "%q's workspace %q (branch `%s`)", from.Name, src.workspace.Name, src.workspace.Branch)
		if src.path != "" {
			fmt.Fprintf(&b, " at `%s`", src.path)
		}
		b.WriteString(". That workspace stayed with its project")
	case src.path != "":
		fmt.Fprintf(&b, "%q's checkout at `%s`", from.Name, src.path)
	default:
		fmt.Fprintf(&b, "%q", from.Name)
	}
	b.WriteString(": file paths, commands and tool results in it refer to that project, not this one.\n\n")

	switch {
	case dst.noMachine:
		fmt.Fprintf(&b, "You now work for %q, still without a machine: there is no working directory, and only tools that need no machine are available.", to.Name)
	case dst.path != "" && dst.machine != "":
		fmt.Fprintf(&b, "You now work in %q's main checkout: your working directory is `%s` on %s.", to.Name, dst.path, dst.machine)
	case dst.path != "":
		fmt.Fprintf(&b, "You now work in %q's main checkout: your working directory is `%s`.", to.Name, dst.path)
	default:
		fmt.Fprintf(&b, "You now work in %q's main checkout. It is not set up on this chat's machine yet, so tools that need it will say so until the user sets it up or switches machine.", to.Name)
	}
	b.WriteString(" Do not assume a file or directory from earlier exists here: check a path before you rely on it, and translate any old path you still need into this project.")
	return b.String()
}

// noteThread is where a moved chat's note goes: its root run's thread.
type noteThread struct {
	workflowID string
	thread     string
}

// moveNoteThread is the chat's root thread, where its next turn reads the
// move note; nil when the chat has never run, so has no history whose paths
// could mislead it.
func (s *Service) moveNoteThread(ctx context.Context, chat *db.Chat) (*noteThread, error) {
	workflowID := chat.MainThreadID()
	if workflowID == "" {
		return nil, nil
	}
	root, err := s.deps.Repo.GetWorkflow(ctx, workflowID)
	if err != nil && !svcerr.IsNotFound(err) {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load the chat's run"))
	}
	if root == nil {
		return nil, nil
	}
	return &noteThread{workflowID: workflowID, thread: root.Thread}, nil
}

// writeMoveNote saves note, hidden, on the chat's root thread, in the move's
// transaction (txCtx): a failure fails the move.
func (s *Service) writeMoveNote(txCtx context.Context, chatID string, at *noteThread, note string) error {
	if at == nil {
		return nil
	}
	hidden := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
	if _, err := s.deps.Repo.SaveMessageToThread(txCtx, chatID, at.thread, int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM),
		note, &at.workflowID, nil, &hidden); err != nil {
		return fmt.Errorf("save the move note: %w", err)
	}
	return nil
}

// optionalString is s, or nil when it is empty.
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
