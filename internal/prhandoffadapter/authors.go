// Copyright (c) 2025 Reliant Labs

// Package prhandoffadapter supplies opt-in server receipt adapters. Nothing in
// this package is installed in application roots; review wait remains missing.
//
//forge:exclude-contract: disabled adapters implement consumer-declared PR handoff interfaces; not an app component
package prhandoffadapter

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/prhandoff"
)

// AuthorStore is the ownership evidence needed by the adapter.
type AuthorStore interface {
	GetWorkflow(context.Context, string) (*core.Workflow, error)
	GetChat(context.Context, string) (*core.Chat, error)
	GetWorktree(context.Context, string) (*core.Worktree, error)
	ListReposByProject(context.Context, string) ([]*core.Repo, error)
}

// ExecutionReader looks up the exact enrolled Temporal execution, never latest.
type ExecutionReader interface {
	DescribeWorkflowExecution(context.Context, string, string) (*client.WorkflowExecutionDescription, error)
}

// Authors resolves persisted owner/root/worktree and Temporal execution facts.
type Authors struct {
	Store    AuthorStore
	Temporal ExecutionReader
}

// ActivitySite captures server execution identity and rejects an argument that
// claims a different workflow. Only a real Temporal activity can supply it.
func ActivitySite(ctx context.Context, workflowID, chatID string) (prhandoff.Site, error) {
	if !activity.IsActivity(ctx) {
		return prhandoff.Site{}, fmt.Errorf("%w: no Temporal activity context", prhandoff.ErrRefused)
	}
	execution := activity.GetInfo(ctx).WorkflowExecution
	if execution.ID != workflowID || execution.RunID == "" {
		return prhandoff.Site{}, prhandoff.ErrRefused
	}
	return prhandoff.Site{RootID: execution.ID, ExecutionID: execution.RunID, ChatID: chatID}, nil
}

func executionID(ctx context.Context) string {
	if !activity.IsActivity(ctx) {
		return ""
	}
	return activity.GetInfo(ctx).WorkflowExecution.RunID
}

// Resolve requires a live root belonging to the same chat and owned worktree.
func (a Authors) Resolve(ctx context.Context, site prhandoff.Site) (prhandoff.Author, error) {
	if a.Store == nil || a.Temporal == nil || site.RootID == "" || site.ExecutionID == "" {
		return prhandoff.Author{}, prhandoff.ErrRefused
	}
	root, err := a.Store.GetWorkflow(ctx, site.RootID)
	if err != nil {
		return prhandoff.Author{}, err
	}
	if root == nil || root.ParentID != nil || (site.ChatID != "" && root.ChatID != site.ChatID) || !root.Status.Live() {
		return prhandoff.Author{}, prhandoff.ErrRefused
	}
	chat, err := a.Store.GetChat(ctx, root.ChatID)
	if err != nil {
		return prhandoff.Author{}, err
	}
	if chat == nil || chat.UserID == "" || chat.WorkflowID == nil || *chat.WorkflowID != root.ID || chat.WorktreeID == nil || (root.OwnerUserID != nil && *root.OwnerUserID != chat.UserID) {
		return prhandoff.Author{}, prhandoff.ErrRefused
	}
	wt, err := a.Store.GetWorktree(ctx, *chat.WorktreeID)
	if err != nil {
		return prhandoff.Author{}, err
	}
	if wt == nil || wt.DeletedAt != nil || wt.ProjectID != chat.ProjectID || wt.Path == "" {
		return prhandoff.Author{}, prhandoff.ErrRefused
	}
	author := prhandoff.Author{OwnerID: chat.UserID, RootID: root.ID, ExecutionID: site.ExecutionID, ChatID: chat.ID, WorktreeID: wt.ID, WorktreePath: wt.Path}
	live, err := a.Available(ctx, author)
	if err != nil {
		return prhandoff.Author{}, err
	}
	if !live {
		return prhandoff.Author{}, prhandoff.ErrRefused
	}
	return author, nil
}

// Checkout uses workspacecreate's workspace-root + registered RelativePath
// layout, with explicit traversal rejection. The selected remote must identify
// the provider-verified repository; a caller cannot supply its own subpath.
func (a Authors) Checkout(ctx context.Context, author prhandoff.Author, repo prhandoff.Repository) (string, error) {
	if a.Store == nil {
		return "", prhandoff.ErrRefused
	}
	chat, err := a.Store.GetChat(ctx, author.ChatID)
	if err != nil {
		return "", err
	}
	if chat == nil || chat.UserID != author.OwnerID || chat.WorktreeID == nil || *chat.WorktreeID != author.WorktreeID {
		return "", prhandoff.ErrRefused
	}
	wt, err := a.Store.GetWorktree(ctx, author.WorktreeID)
	if err != nil {
		return "", err
	}
	if wt == nil || wt.DeletedAt != nil || wt.ProjectID != chat.ProjectID || !filepath.IsAbs(wt.Path) {
		return "", prhandoff.ErrRefused
	}
	repos, err := a.Store.ListReposByProject(ctx, chat.ProjectID)
	if err != nil {
		return "", err
	}
	selected := ""
	for _, registered := range repos {
		if registered == nil || registered.ProjectID != chat.ProjectID || registered.RemoteURL == nil || githubName(*registered.RemoteURL) != strings.ToLower(repo.Name) {
			continue
		}
		rel := filepath.Clean(registered.RelativePath)
		if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", prhandoff.ErrRefused
		}
		candidate := filepath.Join(wt.Path, rel)
		if selected != "" {
			return "", prhandoff.ErrRefused
		}
		selected = candidate
	}
	if selected == "" {
		return "", prhandoff.ErrRefused
	}
	return selected, nil
}

func githubName(remote string) string {
	if strings.HasPrefix(remote, "git@github.com:") {
		return strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(remote, "git@github.com:"), ".git"))
	}
	u, err := url.Parse(remote)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") || (u.Scheme != "https" && u.Scheme != "ssh") {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git"))
}

// Available never restarts, signals, or substitutes an execution. Database or
// provider read outages return errors for retry, not a fabricated completion.
func (a Authors) Available(ctx context.Context, author prhandoff.Author) (bool, error) {
	if a.Store == nil || a.Temporal == nil {
		return false, nil
	}
	root, err := a.Store.GetWorkflow(ctx, author.RootID)
	if err != nil {
		return false, err
	}
	if root == nil || root.ParentID != nil || root.ChatID != author.ChatID || !root.Status.Live() {
		return false, nil
	}
	chat, err := a.Store.GetChat(ctx, author.ChatID)
	if err != nil {
		return false, err
	}
	if chat == nil || chat.UserID != author.OwnerID || chat.WorkflowID == nil || *chat.WorkflowID != author.RootID || chat.WorktreeID == nil || *chat.WorktreeID != author.WorktreeID || (root.OwnerUserID != nil && *root.OwnerUserID != author.OwnerID) {
		return false, nil
	}
	desc, err := a.Temporal.DescribeWorkflowExecution(ctx, author.RootID, author.ExecutionID)
	if err != nil {
		var missing *serviceerror.NotFound
		if errors.As(err, &missing) {
			return false, nil
		}
		return false, err
	}
	if desc == nil {
		return false, nil
	}
	execution := desc.WorkflowExecution
	return execution.ID == author.RootID && execution.RunID == author.ExecutionID && desc.Status == enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, nil
}
