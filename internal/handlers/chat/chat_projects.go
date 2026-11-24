// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
)

// A CHAT'S PROJECTS.
//
// A chat has one primary project, chats.project_id, and may have others
// attached (chat_projects; the primary is never a row there). Its tools reach
// an attached project's main checkout through their `project` argument,
// resolved on the worker to that project's checkout on the machine the call
// runs on (the tool routing in internal/workflow/runtime/activities/handlers).
//
// For now a chat runs on one machine, so every attached project must be
// installed there: an attach is checked against the chat's machine (its pin,
// else where its primary project's work is placed) with projectcheckout.On,
// whose refusal carries the ProjectCheckoutMissing detail the client turns
// into "set it up on this machine". Each call re-checks, because the chat may
// switch machine after the attach. The attachment records the machine it was
// checked on, so a later release can route each project to its own machine
// without a schema change.
//
// The rules: the chat and the project are the caller's (another user's reads
// as not found); they are in one org; the chat is not archived and has a
// machine; at most core.MaxChatProjects attachments. Attaching the primary,
// or a project attached already, changes nothing.

// attachPurpose finishes projectcheckout's "… to <purpose>".
const attachPurpose = "attach it to this chat"

// chatProjectsMachine is the machine a chat's projects must be installed on:
// its pin, else the machine its primary project's work is placed on.
func (s *Service) chatProjectsMachine(ctx context.Context, userID string, chat *db.Chat, primary *db.Project) (string, error) {
	if chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "" {
		return *chat.ActiveDaemonID, nil
	}
	machines, _ := s.deps.Router.(daemonplacement.Machines)
	target, err := projectcheckout.Resolve(ctx, s.deps.Repo, machines, userID, primary, attachPurpose)
	if err != nil {
		return "", err
	}
	return target.DaemonID, nil
}

// checkAttachable refuses project for chat unless it may be attached, and
// returns the machine its checkout was found on.
func (s *Service) checkAttachable(ctx context.Context, userID string, chat *db.Chat, primary, project *db.Project) (string, error) {
	if chat.State == db.ChatStateArchived {
		return "", svcerr.FailedPrecondition("restore the chat before attaching a project to it")
	}
	if chat.NoMachine {
		return "", svcerr.FailedPrecondition("this chat runs without a machine, so it has no checkout of another project to work in; connect a machine to it first")
	}
	orgDecision, err := s.moveOrg(ctx, userID, chat, primary, project)
	if err != nil {
		return "", err
	}
	if orgDecision.crosses {
		return "", svcerr.FailedPrecondition("that project belongs to another organization; every project of a chat is in the chat's organization")
	}
	daemonID, err := s.chatProjectsMachine(ctx, userID, chat, primary)
	if err != nil {
		return "", err
	}
	if _, err := projectcheckout.On(ctx, s.deps.Repo, userID, project, daemonID, attachPurpose); err != nil {
		return "", err
	}
	return daemonID, nil
}

// attachProject checks project for chat and records the attachment. The
// primary, or a project attached already, is a no-op.
func (s *Service) attachProject(ctx context.Context, userID string, chat *db.Chat, primary *db.Project, projectID string) error {
	if projectID == "" || projectID == chat.ProjectID {
		return nil
	}
	project, err := s.ownedProject(ctx, userID, projectID)
	if err != nil {
		return err
	}
	daemonID, err := s.checkAttachable(ctx, userID, chat, primary, project)
	if err != nil {
		return err
	}
	cp := &db.ChatProject{ChatID: chat.ID, ProjectID: project.ID}
	if daemonID != "" {
		cp.DaemonID = &daemonID
	}
	if err := s.deps.Repo.AttachChatProject(ctx, cp); err != nil {
		if errors.Is(err, core.ErrTooManyChatProjects) {
			return svcerr.ResourceExhausted(err.Error())
		}
		return svcerr.WrapOr(err, svcerr.Internal("failed to attach the project to the chat"))
	}
	return nil
}

// attachStartProjects is StartChat's attached_project_ids for the chat it
// just created, run inside the launch transaction (launch.Spec.OnChatCreated):
// the chat row's machine pin is decided by then. Nil when there are none.
func (s *Service) attachStartProjects(userID string, projectIDs []string) func(ctx context.Context, chat *core.Chat) error {
	if len(projectIDs) == 0 {
		return nil
	}
	return func(ctx context.Context, chat *core.Chat) error {
		primary, err := s.ownedProject(ctx, userID, chat.ProjectID)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, id := range projectIDs {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			if err := s.attachProject(ctx, userID, chat, primary, id); err != nil {
				return err
			}
		}
		return nil
	}
}

// chatProjects is the chat's projects as ListChatProjects lists them: the
// primary first, then the attachments, oldest first. An attached project the
// caller can no longer read (deleted, or moved away) is left out.
func (s *Service) chatProjects(ctx context.Context, userID string, chat *db.Chat) ([]*reliantv1.ChatProject, error) {
	primary, err := s.ownedProject(ctx, userID, chat.ProjectID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to load the chat's project"))
	}
	out := []*reliantv1.ChatProject{{
		ProjectId: primary.ID,
		Name:      primary.Name,
		Primary:   true,
		DaemonId:  chat.ActiveDaemonID,
		Path:      s.checkoutPath(ctx, userID, primary, chat.ActiveDaemonID),
	}}
	rows, err := s.deps.Repo.ListChatProjects(ctx, chat.ID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list the chat's projects"))
	}
	for _, row := range rows {
		project, err := s.ownedProject(ctx, userID, row.ProjectID)
		if err != nil {
			continue
		}
		// The checkout is the one on the machine the chat's calls run on.
		machine := chat.ActiveDaemonID
		if machine == nil {
			machine = row.DaemonID
		}
		out = append(out, &reliantv1.ChatProject{
			ProjectId:  project.ID,
			Name:       project.Name,
			DaemonId:   row.DaemonID,
			Path:       s.checkoutPath(ctx, userID, project, machine),
			AttachedAt: row.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

// changeChatProjects runs change and announces the chat's new project list to
// the user's other clients (a chat_config_changed update carrying
// attached_project_ids, which older clients ignore), in one transaction.
func (s *Service) changeChatProjects(ctx context.Context, userID string, chat *db.Chat, change func(ctx context.Context) error) error {
	return s.deps.Repo.RunTx(ctx, orm.ReadCommitted, func(txCtx context.Context) error {
		if err := change(txCtx); err != nil {
			return err
		}
		rows, err := s.deps.Repo.ListChatProjects(txCtx, chat.ID)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ProjectID)
		}
		data, err := json.Marshal(map[string]any{
			"chat_id":              chat.ID,
			"attached_project_ids": ids,
		})
		if err != nil {
			return err
		}
		return s.deps.Repo.CreateUserUpdate(txCtx, &db.UserUpdate{
			UserID:     userID,
			ProjectID:  &chat.ProjectID,
			WorktreeID: chat.WorktreeID,
			ChatID:     &chat.ID,
			UpdateType: db.UserUpdateChatConfigChanged,
			EntityType: db.EntityTypeChat,
			EntityID:   chat.ID,
			Data:       data,
		})
	})
}
