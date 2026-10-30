// Copyright (c) 2025 Reliant Labs
package daemonplacement

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// WorkspaceReader is what WorkspaceDaemon reads: the workspace a request
// names, and the chat it was made for.
type WorkspaceReader interface {
	WorktreeReader
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	// GetProject confirms a worktree is the caller's before its machine is
	// read: its owner is a machine id of whoever owns the project.
	GetProject(ctx context.Context, id string) (*db.Project, error)
}

// ErrForeignChat is WorkspaceDaemon's answer for a chat another user owns.
// Callers report it as the chat not being found, so ids are not an oracle.
var ErrForeignChat = errors.New("chat belongs to another user")

// ErrForeignWorkspace is WorkspaceDaemon's answer for a worktree whose project
// another user owns, or that is not of the project the request names. Callers
// report it as the worktree not being found: its machine is never read, so no
// error or reply can name another tenant's machine.
var ErrForeignWorkspace = errors.New("worktree belongs to another user or project")

// Workspace is the machine a request about a project's files, shell or git
// state goes to. DaemonID "" leaves it to default resolution. NoMachine marks
// a request made for a chat that has no machine by design: nothing done for
// it may wake one (see machinewake).
type Workspace struct {
	DaemonID  string
	NoMachine bool
}

// WorkspaceDaemon returns the machine every request about a workspace goes
// to — the Files tab, file previews, the terminal, git status and diffs — so
// they all reach the one disk that holds the files. worktreeID and chatID may
// each be "".
//
//  1. A worktree some machine owns goes to that owner, whatever chat asked
//     (owners resolves a row that does not record one; nil reads the record):
//     the checkout exists on no other disk. A chat pinned elsewhere (rows from
//     before StartChat refused that combination) would read a directory that
//     is not there.
//     A request that names a chat of this project and no worktree is about
//     the chat's workspace, so the chat's worktree is judged the same way.
//  2. Otherwise a request made for a chat of this project goes where the
//     chat's tools run (ChatDaemonID): the main checkout is on every machine
//     with the project, and the chat's machine is the one whose copy its
//     agent is changing.
//  3. Otherwise default resolution ("").
//
// A worktree or chat row that no longer exists is ignored, as it is for tool
// routing: the UI can still name one it has just deleted. A chat from another
// project is stale UI context and is ignored too. A chat another user owns is
// ErrForeignChat, and a worktree of another user's project, or of a project
// other than projectID, is ErrForeignWorkspace; any other lookup failure is
// returned.
func WorkspaceDaemon(ctx context.Context, r WorkspaceReader, owners *Resolver, userID, projectID, worktreeID, chatID string) (Workspace, error) {
	owner, err := workspaceOwner(ctx, r, owners, userID, projectID, worktreeID)
	if err != nil {
		return Workspace{}, err
	}
	if chatID == "" {
		return Workspace{DaemonID: owner}, nil
	}
	chat, err := r.GetChat(ctx, chatID)
	switch {
	case errors.Is(err, core.ErrChatNotFound):
		return Workspace{DaemonID: owner}, nil
	case err != nil:
		return Workspace{}, fmt.Errorf("load chat %s: %w", chatID, err)
	case chat == nil:
		return Workspace{DaemonID: owner}, nil
	case chat.UserID != userID:
		return Workspace{}, ErrForeignChat
	case projectID != "" && chat.ProjectID != projectID:
		return Workspace{DaemonID: owner}, nil
	}
	// A request scoped to the chat alone is about the chat's workspace: the
	// files it resolves are under chat.WorktreeID (file_system's
	// ResolveBasePath), so they go to that workspace's owner by the same rule,
	// settled on first use, never to a pin that names another machine.
	if owner == "" && worktreeID == "" && chat.WorktreeID != nil && *chat.WorktreeID != "" {
		if owner, err = workspaceOwner(ctx, r, owners, userID, chat.ProjectID, *chat.WorktreeID); err != nil {
			return Workspace{}, err
		}
	}
	if owner != "" {
		return Workspace{DaemonID: owner, NoMachine: chat.NoMachine}, nil
	}
	daemonID, err := ChatDaemonID(ctx, r, chat)
	if err != nil {
		return Workspace{}, err
	}
	return Workspace{DaemonID: daemonID, NoMachine: chat.NoMachine}, nil
}

// workspaceOwner is the machine holding worktreeID's checkout ("" for none,
// the main checkout, a row no machine settled, or a row that no longer
// exists), after confirming it is the caller's and of projectID ("" for any
// of the caller's projects).
func workspaceOwner(ctx context.Context, r WorkspaceReader, owners *Resolver, userID, projectID, worktreeID string) (string, error) {
	if worktreeID == "" {
		return "", nil
	}
	wt, err := r.GetWorktree(ctx, worktreeID)
	switch {
	case errors.Is(err, core.ErrWorktreeNotFound):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("load worktree %s: %w", worktreeID, err)
	}
	if projectID != "" && wt.ProjectID != projectID {
		return "", ErrForeignWorkspace
	}
	project, err := r.GetProject(ctx, wt.ProjectID)
	if err != nil && !errors.Is(err, svcerr.ErrNotFound) {
		return "", fmt.Errorf("load project %s: %w", wt.ProjectID, err)
	}
	if project == nil || project.UserID != userID {
		return "", ErrForeignWorkspace
	}
	return owners.Owner(ctx, userID, wt), nil
}

// WorkspaceMachineConflict reports whether a chat in worktree pinned to
// daemonID would run on a machine without the checkout: the worktree is one
// machine's and daemonID names another. owner is the worktree's machine ("" for
// the main checkout and rows that record none). daemonID "" never conflicts —
// placement then picks the owner (PinTarget).
func WorkspaceMachineConflict(worktree *db.Worktree, daemonID string) (owner string, conflict bool) {
	owner = WorktreeOwner(worktree)
	return owner, owner != "" && daemonID != "" && daemonID != owner
}

// DaemonReader reads a machine's record, for its name.
type DaemonReader interface {
	GetDaemon(ctx context.Context, id string) (*db.Daemon, error)
}

// WorkspaceElsewhereMessage is the sentence a chat in a workspace on another
// machine is refused with. It names the machine, so the user knows which one
// to pick.
func WorkspaceElsewhereMessage(ctx context.Context, r DaemonReader, ownerDaemonID string) string {
	name := MachineName(ctx, r, ownerDaemonID)
	return fmt.Sprintf("this workspace lives on %s, so a chat in it runs on %s: pick that machine, or start the chat in the main workspace", name, name)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// MachineName is what to call a machine in a sentence, by the web's rule
// (frontends/web/src/lib/machineName.ts machineDisplayName): the name its owner
// gave it; else a self-hosted machine's real hostname; else a short tag from
// its id. A managed machine's hostname is a pod name, never shown. A record
// that cannot be read is named by its id tag.
func MachineName(ctx context.Context, r DaemonReader, daemonID string) string {
	short := daemonID
	if len(short) > 8 {
		short = short[:8]
	}
	var d *db.Daemon
	if r != nil && daemonID != "" {
		d, _ = r.GetDaemon(ctx, daemonID)
	}
	if d == nil {
		return fmt.Sprintf("machine %s", short)
	}
	if name := strings.TrimSpace(d.Name); name != "" {
		return fmt.Sprintf("%q", name)
	}
	if d.DaemonType != nil && *d.DaemonType == "managed" {
		return fmt.Sprintf("cloud machine %s", short)
	}
	if d.Hostname != nil {
		if host := strings.TrimSpace(*d.Hostname); host != "" && host != daemonID && !uuidPattern.MatchString(host) {
			return fmt.Sprintf("%q", host)
		}
	}
	return fmt.Sprintf("self-hosted machine %s", short)
}
