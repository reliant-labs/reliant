// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// resolveThreadWorktree turns the `worktree` a spawn asked for into the id of a
// workspace the new thread may work in. A refusal is the reason it may not,
// worded for the agent, with the usable names listed so it can correct itself.
//
// The row must exist, belong to the chat's project, be unarchived, and be
// ACTIVE: CREATING has no checkout yet and FAILED never will.
//
// A thread that already exists (spawn's agent_id resumption) is not rebound: a
// conversation that has been working in one workspace keeps it, so naming a
// different one is refused rather than silently moving the conversation.
func (a *CreateWorkflowWithThreadActivity) resolveThreadWorktree(ctx context.Context, input CreateWorkflowWithThreadInput) (id, refusal string, err error) {
	chat, err := a.repo.GetChat(ctx, input.ChatID)
	if err != nil {
		return "", "", fmt.Errorf("load chat %s: %w", input.ChatID, err)
	}

	active, err := a.activeWorktrees(ctx, chat.ProjectID)
	if err != nil {
		return "", "", err
	}

	row, refusal, err := a.lookupWorktree(ctx, chat.ProjectID, input.Worktree, active)
	if err != nil || refusal != "" {
		return "", refusal, err
	}

	if input.ThreadID != "" {
		existing, err := a.repo.GetThread(ctx, input.ThreadID)
		switch {
		case err == nil && existing != nil:
			bound := ""
			if existing.WorktreeID != nil {
				bound = *existing.WorktreeID
			}
			if bound == "" && chat.WorktreeID != nil {
				bound = *chat.WorktreeID
			}
			if bound != row.ID {
				return "", fmt.Sprintf("Cannot resume agent %s in worktree %q: that conversation is already bound to %s and is not moved between workspaces. "+
					"Resume it without the worktree parameter, or spawn a new agent for the other workspace.",
					input.ThreadID, row.Name, describeBinding(ctx, a.repo, bound)), nil
			}
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return "", "", fmt.Errorf("load thread %s: %w", input.ThreadID, err)
		}
	}
	return row.ID, "", nil
}

func (a *CreateWorkflowWithThreadActivity) activeWorktrees(ctx context.Context, projectID string) ([]*core.Worktree, error) {
	status := int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE)
	rows, err := a.repo.ListWorktrees(ctx, core.WorktreeFilters{ProjectID: &projectID, Status: &status, Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	return rows, nil
}

func (a *CreateWorkflowWithThreadActivity) lookupWorktree(ctx context.Context, projectID, ref string, active []*core.Worktree) (*core.Worktree, string, error) {
	var byName []*core.Worktree
	for _, w := range active {
		if w.ID == ref {
			return w, "", nil
		}
		if w.Name == ref {
			byName = append(byName, w)
		}
	}
	if len(byName) == 1 {
		return byName[0], "", nil
	}
	if len(byName) > 1 {
		ids := make([]string, len(byName))
		for i, w := range byName {
			ids[i] = w.ID
		}
		sort.Strings(ids)
		return nil, fmt.Sprintf("Several active worktrees are named %q (ids: %s). Pass the id of the one you mean as the worktree parameter.", ref, strings.Join(ids, ", ")), nil
	}

	// Not an active worktree of this project. Say why when it exists at all,
	// whether the agent named it by id or by name.
	all, err := a.repo.ListWorktrees(ctx, core.WorktreeFilters{ProjectID: &projectID, IncludeArchived: true, Limit: 500})
	if err != nil {
		return nil, "", fmt.Errorf("list worktrees: %w", err)
	}
	// Archived rows no longer hold their name, so several can share one with
	// a live row. Explain the live row if there is one, else the most recently
	// created archived one.
	sort.SliceStable(all, func(i, j int) bool {
		if li, lj := all[i].DeletedAt == nil, all[j].DeletedAt == nil; li != lj {
			return li
		}
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	for _, w := range all {
		if w.ID != ref && w.Name != ref {
			continue
		}
		if w.DeletedAt != nil {
			return nil, notUsable(ref, "is archived", active), nil
		}
		return nil, notUsable(ref, worktreeStatusReason(w.Status), active), nil
	}
	// An id from another project is worth naming as such.
	if w, err := a.repo.GetWorktree(ctx, ref); err == nil && w != nil && w.ProjectID != projectID {
		return nil, notUsable(ref, "belongs to a different project", active), nil
	} else if err != nil && !errors.Is(err, core.ErrWorktreeNotFound) {
		return nil, "", fmt.Errorf("load worktree %s: %w", ref, err)
	}
	return nil, notUsable(ref, "does not exist in this project", active), nil
}

func worktreeStatusReason(status int32) string {
	switch reliantv1.WorktreeStatus(status) {
	case reliantv1.WorktreeStatus_WORKTREE_STATUS_CREATING:
		return "is still being created and has no checkout yet"
	case reliantv1.WorktreeStatus_WORKTREE_STATUS_FAILED:
		return "failed to be created"
	default:
		return fmt.Sprintf("is not active (status %s)", reliantv1.WorktreeStatus(status))
	}
}

func notUsable(ref, why string, active []*core.Worktree) string {
	names := make([]string, 0, len(active))
	for _, w := range active {
		names = append(names, fmt.Sprintf("%q", w.Name))
	}
	sort.Strings(names)
	list := "none"
	if len(names) > 0 {
		list = strings.Join(names, ", ")
	}
	return fmt.Sprintf("Cannot spawn in worktree %q: it %s. Active worktrees of this project: %s. "+
		"Create one with the worktree tool first, or omit the worktree parameter.", ref, why, list)
}

func describeBinding(ctx context.Context, repo db.Repository, worktreeID string) string {
	if worktreeID == "" {
		return "this chat's own workspace"
	}
	if w, err := repo.GetWorktree(ctx, worktreeID); err == nil && w != nil {
		return fmt.Sprintf("worktree %q", w.Name)
	}
	return "worktree " + worktreeID
}
